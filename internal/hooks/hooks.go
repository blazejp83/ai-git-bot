// Package hooks delivers events (review and implementation started,
// completed, failed) to admin-configured HTTP endpoints: stored first, sent
// in the background with HMAC signatures, retried with backoff, never in the
// way of the work that produced them.
package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

// Event types.
const (
	ReviewStarted           = "review.started"
	ReviewCompleted         = "review.completed"
	ReviewFailed            = "review.failed"
	ImplementationStarted   = "implementation.started"
	ImplementationCompleted = "implementation.completed"
	ImplementationFailed    = "implementation.failed"
	Ping                    = "ping"
)

// EventTypes lists the types an endpoint can subscribe to.
var EventTypes = []string{ReviewStarted, ReviewCompleted, ReviewFailed, ImplementationStarted, ImplementationCompleted, ImplementationFailed}

const (
	maxAttempts     = 5
	firstRetry      = 30 * time.Second
	maxRetryDelay   = 30 * time.Minute
	sweepInterval   = 30 * time.Second
	keepDeliveries  = 10
	maxPayloadBytes = 64 * 1024
	sendTimeout     = 10 * time.Second
	signatureHeader = "X-EventHook-Signature-256"
)

// Event is something worth telling the outside world about.
type Event struct {
	Type  string
	BotID int64
	Bot   string
	Repo  string // "owner/name"
	Data  map[string]any
}

// Decrypter reveals stored secrets.
type Decrypter interface {
	Decrypt(string) (string, error)
}

// Service stores and delivers events.
type Service struct {
	db     *sql.DB
	enc    Decrypter
	now    func() time.Time
	wake   chan struct{}
	client func(skipTLS bool) *http.Client
}

func NewService(db *sql.DB, enc Decrypter) *Service {
	return &Service{db: db, enc: enc, now: func() time.Time { return time.Now().UTC() }, wake: make(chan struct{}, 1), client: newClient}
}

func newClient(skipTLS bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if skipTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{
		Timeout:   sendTimeout,
		Transport: tr,
		// A redirect is not a delivery: report it instead of following it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func newID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// envelope is the v1 payload every endpoint receives.
func envelope(id string, ev Event, at time.Time) []byte {
	body := map[string]any{
		"schemaVersion": 1,
		"id":            id,
		"eventType":     ev.Type,
		"timestamp":     at.Format(time.RFC3339),
		"actor":         map[string]any{"bot": ev.Bot},
		"repository":    map[string]any{"fullName": ev.Repo},
		"data":          ev.Data,
	}
	b, _ := json.Marshal(body)
	if len(b) > maxPayloadBytes {
		body["data"] = map[string]any{"truncated": true, "reason": "payload exceeded 64 KB"}
		b, _ = json.Marshal(body)
	}
	return b
}

// endpointMatches reports whether an endpoint wants an event.
func endpointMatches(eventTypes string, botID sql.NullInt64, repoFilter sql.NullString, ev Event) bool {
	wanted := false
	for _, t := range strings.Split(eventTypes, ",") {
		if strings.TrimSpace(t) == ev.Type {
			wanted = true
		}
	}
	if !wanted {
		return false
	}
	if botID.Valid && botID.Int64 != ev.BotID {
		return false
	}
	if f := strings.TrimSpace(repoFilter.String); repoFilter.Valid && f != "" {
		if ok, _ := path.Match(strings.ToLower(f), strings.ToLower(ev.Repo)); !ok {
			return false
		}
	}
	return true
}

// Publish queues an event for every matching enabled endpoint. It never
// fails the caller: problems are logged.
func (s *Service) Publish(ev Event) {
	if s == nil {
		return
	}
	rows, err := s.db.Query(`SELECT id, event_types, bot_id, repo_filter FROM hook_endpoints WHERE enabled = 1`)
	if err != nil {
		slog.Error("Hooks: cannot read endpoints", "err", err)
		return
	}
	var targets []int64
	for rows.Next() {
		var id int64
		var types string
		var botID sql.NullInt64
		var repoFilter sql.NullString
		if rows.Scan(&id, &types, &botID, &repoFilter) == nil && endpointMatches(types, botID, repoFilter, ev) {
			targets = append(targets, id)
		}
	}
	rows.Close()
	if len(targets) == 0 {
		return
	}
	now := s.now()
	id := newID("evt_")
	payload := string(envelope(id, ev, now))
	for _, ep := range targets {
		s.queue(ep, id, ev.Type, payload, now)
	}
	s.Wake()
}

func (s *Service) queue(endpointID int64, eventID, eventType, payload string, now time.Time) {
	if _, err := s.db.Exec(`INSERT INTO hook_deliveries (endpoint_id, event_id, event_type, payload, next_attempt_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, endpointID, eventID, eventType, payload, now, now, now); err != nil {
		slog.Error("Hooks: cannot queue delivery", "endpoint", endpointID, "err", err)
	}
}

// SendTest queues a ping to one endpoint, whatever its event types.
func (s *Service) SendTest(endpointID int64) {
	now := s.now()
	id := newID("evt_")
	s.queue(endpointID, id, Ping, string(envelope(id, Event{Type: Ping, Bot: "ai-git-bot", Data: map[string]any{"message": "Test event from AI Git Bot"}}, now)), now)
	s.Wake()
}

// Retry makes a delivery due again with a fresh attempt budget.
func (s *Service) Retry(deliveryID int64) error {
	now := s.now()
	_, err := s.db.Exec(`UPDATE hook_deliveries SET status='PENDING', attempts=0, next_attempt_at=?, updated_at=? WHERE id=?`, now, now, deliveryID)
	s.Wake()
	return err
}

// Wake makes the worker look for due deliveries now.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run delivers due events until stop is closed: when woken, and every 30
// seconds, which also picks up deliveries a restart left behind.
func (s *Service) Run(stop <-chan struct{}) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		s.DeliverDue(context.Background())
		select {
		case <-stop:
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

type due struct {
	id, endpointID int64
	eventType      string
	payload        string
	attempts       int
	url            string
	headers        sql.NullString
	secret         sql.NullString
	skipTLS        bool
}

// DeliverDue sends every delivery that is due.
func (s *Service) DeliverDue(ctx context.Context) {
	rows, err := s.db.Query(`SELECT d.id, d.endpoint_id, d.event_type, d.payload, d.attempts, e.url, e.headers, e.secret, e.skip_tls
		FROM hook_deliveries d JOIN hook_endpoints e ON e.id = d.endpoint_id
		WHERE d.status = 'PENDING' AND d.next_attempt_at <= ? ORDER BY d.id LIMIT 50`, s.now())
	if err != nil {
		slog.Error("Hooks: cannot read due deliveries", "err", err)
		return
	}
	var batch []due
	for rows.Next() {
		var d due
		if rows.Scan(&d.id, &d.endpointID, &d.eventType, &d.payload, &d.attempts, &d.url, &d.headers, &d.secret, &d.skipTLS) == nil {
			batch = append(batch, d)
		}
	}
	rows.Close()
	for _, d := range batch {
		s.deliver(ctx, d)
	}
}

// Sign returns the signature header value for a payload.
func Sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) deliver(ctx context.Context, d due) {
	code, err := s.send(ctx, d)
	now := s.now()
	attempts := d.attempts + 1
	if err == nil {
		s.db.Exec(`UPDATE hook_deliveries SET status='DELIVERED', attempts=?, last_status_code=?, last_error=NULL, updated_at=? WHERE id=?`,
			attempts, code, now, d.id)
		s.prune(d.endpointID)
		return
	}
	status, next := "PENDING", now.Add(RetryDelay(attempts))
	if attempts >= maxAttempts {
		status = "FAILED"
		slog.Warn("Hook delivery failed for good", "delivery", d.id, "endpoint", d.endpointID, "err", err)
	}
	var codeVal any
	if code > 0 {
		codeVal = code
	}
	s.db.Exec(`UPDATE hook_deliveries SET status=?, attempts=?, next_attempt_at=?, last_status_code=?, last_error=?, updated_at=? WHERE id=?`,
		status, attempts, next, codeVal, truncate(err.Error(), 1000), now, d.id)
	if status == "FAILED" {
		s.prune(d.endpointID)
	}
}

// RetryDelay is the wait after the given number of failed attempts.
func RetryDelay(attempts int) time.Duration {
	d := firstRetry
	for i := 1; i < attempts && d < maxRetryDelay; i++ {
		d *= 2
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	return d
}

func (s *Service) send(ctx context.Context, d due) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	payload := []byte(d.payload)
	req, err := http.NewRequestWithContext(ctx, "POST", d.url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ai-git-bot-hooks/1")
	req.Header.Set("X-EventHook-Event", d.eventType)
	req.Header.Set("X-EventHook-Delivery", fmt.Sprint(d.id))
	if d.headers.Valid && d.headers.String != "" {
		raw, err := s.enc.Decrypt(d.headers.String)
		if err != nil {
			return 0, fmt.Errorf("cannot decrypt the endpoint's headers: %w", err)
		}
		var hs map[string]string
		if err := json.Unmarshal([]byte(raw), &hs); err != nil {
			return 0, fmt.Errorf("the endpoint's headers are not a JSON object: %w", err)
		}
		for k, v := range hs {
			req.Header.Set(k, v)
		}
	}
	if d.secret.Valid && d.secret.String != "" {
		secret, err := s.enc.Decrypt(d.secret.String)
		if err != nil {
			return 0, fmt.Errorf("cannot decrypt the endpoint's secret: %w", err)
		}
		req.Header.Set(signatureHeader, Sign(secret, payload))
	}
	resp, err := s.client(d.skipTLS).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.StatusCode, nil
}

// prune keeps the newest finished deliveries of an endpoint; pending ones stay.
func (s *Service) prune(endpointID int64) {
	s.db.Exec(`DELETE FROM hook_deliveries WHERE endpoint_id = ? AND status != 'PENDING' AND id NOT IN (
		SELECT id FROM hook_deliveries WHERE endpoint_id = ? AND status != 'PENDING' ORDER BY id DESC LIMIT ?)`,
		endpointID, endpointID, keepDeliveries)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
