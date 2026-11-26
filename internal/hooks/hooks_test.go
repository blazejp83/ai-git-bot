package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/db"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

type received struct {
	headers http.Header
	body    []byte
}

type receiver struct {
	mu     sync.Mutex
	got    []received
	status int
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	rc.got = append(rc.got, received{r.Header.Clone(), body})
	status := rc.status
	rc.mu.Unlock()
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
}

func setup(t *testing.T) (*Service, *encrypt.Service, *time.Time) {
	t.Helper()
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	enc := encrypt.New("k")
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s := NewService(conn, enc)
	s.now = func() time.Time { return now }
	return s, enc, &now
}

func addEndpoint(t *testing.T, s *Service, enc *encrypt.Service, url, types string, botID any, repoFilter any, secret, headers string, skipTLS bool) int64 {
	t.Helper()
	var encSecret, encHeaders any
	if secret != "" {
		encSecret, _ = enc.Encrypt(secret)
	}
	if headers != "" {
		encHeaders, _ = enc.Encrypt(headers)
	}
	res, err := s.db.Exec(`INSERT INTO hook_endpoints (name, url, event_types, headers, secret, bot_id, repo_filter, skip_tls, enabled, created_at, updated_at)
		VALUES ('e', ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`, url, types, encHeaders, encSecret, botID, repoFilter, skipTLS, s.now(), s.now())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func deliveryState(s *Service, id int64) (status string, attempts int, next time.Time) {
	s.db.QueryRow(`SELECT status, attempts, next_attempt_at FROM hook_deliveries WHERE endpoint_id = ? ORDER BY id DESC LIMIT 1`, id).Scan(&status, &attempts, &next)
	return
}

func TestPublishMatchesAndSigns(t *testing.T) {
	s, enc, _ := setup(t)
	rc := &receiver{}
	srv := httptest.NewServer(rc)
	defer srv.Close()

	all := addEndpoint(t, s, enc, srv.URL, ReviewCompleted, nil, nil, "s3cret", `{"Authorization":"Bearer tok"}`, false)
	otherBot := addEndpoint(t, s, enc, srv.URL, ReviewCompleted, int64(99), nil, "", "", false)
	otherRepo := addEndpoint(t, s, enc, srv.URL, ReviewCompleted, nil, "other/*", "", "", false)
	otherType := addEndpoint(t, s, enc, srv.URL, ImplementationFailed, nil, nil, "", "", false)

	s.Publish(Event{Type: ReviewCompleted, BotID: 1, Bot: "reviewer", Repo: "me/app", Data: map[string]any{"pr": 7, "decision": "APPROVE"}})
	s.DeliverDue(context.Background())

	if len(rc.got) != 1 {
		t.Fatalf("deliveries = %d, want only the matching endpoint", len(rc.got))
	}
	for _, id := range []int64{otherBot, otherRepo, otherType} {
		if st, _, _ := deliveryState(s, id); st != "" {
			t.Errorf("endpoint %d should get nothing, has %q", id, st)
		}
	}
	got := rc.got[0]
	if got.headers.Get(signatureHeader) != Sign("s3cret", got.body) || got.headers.Get("Authorization") != "Bearer tok" ||
		got.headers.Get("X-EventHook-Event") != ReviewCompleted {
		t.Errorf("headers = %v", got.headers)
	}
	var env map[string]any
	json.Unmarshal(got.body, &env)
	if env["schemaVersion"] != float64(1) || env["eventType"] != ReviewCompleted || !strings.HasPrefix(env["id"].(string), "evt_") ||
		env["repository"].(map[string]any)["fullName"] != "me/app" || env["data"].(map[string]any)["decision"] != "APPROVE" {
		t.Errorf("envelope = %s", got.body)
	}
	if st, attempts, _ := deliveryState(s, all); st != "DELIVERED" || attempts != 1 {
		t.Errorf("state = %s/%d", st, attempts)
	}
}

func TestRetriesBackoffAndFailure(t *testing.T) {
	s, enc, now := setup(t)
	rc := &receiver{status: 503}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	ep := addEndpoint(t, s, enc, srv.URL, ImplementationFailed, nil, nil, "", "", false)

	s.Publish(Event{Type: ImplementationFailed, Repo: "me/app"})
	for i := 1; i <= maxAttempts; i++ {
		s.DeliverDue(context.Background())
		st, attempts, next := deliveryState(s, ep)
		if attempts != i {
			t.Fatalf("attempt %d: attempts = %d", i, attempts)
		}
		if i < maxAttempts {
			if st != "PENDING" || next.Sub(*now) != RetryDelay(i) {
				t.Fatalf("attempt %d: %s, next in %v, want %v", i, st, next.Sub(*now), RetryDelay(i))
			}
			s.DeliverDue(context.Background()) // not due yet: nothing happens
			if _, a, _ := deliveryState(s, ep); a != i {
				t.Fatalf("a delivery was sent before it was due")
			}
			*now = next
		} else if st != "FAILED" {
			t.Fatalf("after %d attempts: %s", i, st)
		}
	}
	if RetryDelay(1) != 30*time.Second || RetryDelay(2) != time.Minute || RetryDelay(10) != 30*time.Minute {
		t.Error("backoff: 30s doubling, capped at 30m")
	}

	// Manual retry with the receiver fixed.
	rc.status = 200
	var id int64
	s.db.QueryRow(`SELECT id FROM hook_deliveries WHERE endpoint_id = ?`, ep).Scan(&id)
	s.Retry(id)
	s.DeliverDue(context.Background())
	if st, _, _ := deliveryState(s, ep); st != "DELIVERED" {
		t.Errorf("after manual retry: %s", st)
	}
}

func TestRedirectIsNotADelivery(t *testing.T) {
	s, enc, _ := setup(t)
	srv := httptest.NewServer(http.RedirectHandler("https://elsewhere.example/", http.StatusFound))
	defer srv.Close()
	ep := addEndpoint(t, s, enc, srv.URL, Ping, nil, nil, "", "", false)
	s.SendTest(ep)
	s.DeliverDue(context.Background())
	var code sql.NullInt64
	s.db.QueryRow(`SELECT last_status_code FROM hook_deliveries WHERE endpoint_id = ?`, ep).Scan(&code)
	if st, _, _ := deliveryState(s, ep); st != "PENDING" || code.Int64 != 302 {
		t.Errorf("redirect: %s code %d", st, code.Int64)
	}
}

func TestPruneTruncateAndSkipTLS(t *testing.T) {
	s, enc, _ := setup(t)
	tlsSrv := httptest.NewTLSServer(&receiver{})
	defer tlsSrv.Close()

	strict := addEndpoint(t, s, enc, tlsSrv.URL, Ping, nil, nil, "", "", false)
	lax := addEndpoint(t, s, enc, tlsSrv.URL, Ping, nil, nil, "", "", true)
	s.SendTest(strict)
	s.SendTest(lax)
	s.DeliverDue(context.Background())
	if st, _, _ := deliveryState(s, strict); st != "PENDING" {
		t.Errorf("self-signed certificate must fail without skip-TLS: %s", st)
	}
	if st, _, _ := deliveryState(s, lax); st != "DELIVERED" {
		t.Errorf("skip-TLS endpoint: %s", st)
	}

	for i := 0; i < 15; i++ {
		s.SendTest(lax)
		s.DeliverDue(context.Background())
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM hook_deliveries WHERE endpoint_id = ?`, lax).Scan(&n)
	if n != keepDeliveries {
		t.Errorf("kept %d deliveries, want %d", n, keepDeliveries)
	}

	big := envelope("evt_1", Event{Type: ReviewCompleted, Data: map[string]any{"text": strings.Repeat("x", 70000)}}, time.Now())
	if len(big) > maxPayloadBytes || !strings.Contains(string(big), `"truncated":true`) {
		t.Errorf("oversized payload: %d bytes", len(big))
	}
}
