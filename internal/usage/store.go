// Package usage stores the token usage and errors of AI calls and answers the
// questions the usage page and dashboard ask of them.
package usage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

// Store records AI usage in SQLite. It implements ai.Recorder.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// RecordUsage stores one request. Failures are logged, never returned: usage
// recording must not break an AI call.
func (s *Store) RecordUsage(r ai.UsageRecord) {
	u := r.Usage
	t := Tokens{
		Input:      int64(max(u.PromptTokens-u.CacheReadTokens-u.CacheWriteTokens, 0)),
		Output:     int64(u.CompletionTokens),
		CacheRead:  int64(u.CacheReadTokens),
		CacheWrite: int64(u.CacheWriteTokens),
	}
	var cost sql.NullFloat64
	if p, ok := PriceFor(r.Model); ok {
		cost = sql.NullFloat64{Float64: p.Cost(t), Valid: true}
	}
	_, err := s.db.Exec(`INSERT INTO ai_usage_log
		(recorded_at, integration, provider, model, bot, session, round, kind,
		 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, est_cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.now(), r.Integration, r.Provider, r.Model, r.Bot, r.Session, r.Round, r.Kind,
		t.Input, t.Output, t.CacheRead, t.CacheWrite, cost)
	if err != nil {
		slog.Error("Failed to record AI usage", "err", err)
	}
}

// RecordError stores one failed call.
func (s *Store) RecordError(r ai.ErrorRecord) {
	msg := r.Message
	if len(msg) > 4000 {
		msg = msg[:4000] + "…"
	}
	_, err := s.db.Exec(`INSERT INTO ai_error_log
		(recorded_at, integration, provider, model, bot, session, round, kind, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.now(), r.Integration, r.Provider, r.Model, r.Bot, r.Session, r.Round, r.Kind, msg)
	if err != nil {
		slog.Error("Failed to record AI error", "err", err)
	}
}

// Filter limits queries to [From, To). Zero times mean unbounded.
type Filter struct {
	From, To time.Time
}

func (f Filter) where() (string, []any) {
	clause, args := " WHERE 1=1", []any{}
	if !f.From.IsZero() {
		clause += " AND recorded_at >= ?"
		args = append(args, f.From.UTC())
	}
	if !f.To.IsZero() {
		clause += " AND recorded_at < ?"
		args = append(args, f.To.UTC())
	}
	return clause, args
}

// Summary totals the usage in a period.
type Summary struct {
	Calls                                int64
	Input, Output, CacheRead, CacheWrite int64
	Cost                                 float64
	UnpricedCalls                        int64 // calls whose model has no known price
}

// CacheHitRatio is the share of prompt tokens served from the cache.
func (s Summary) CacheHitRatio() float64 {
	total := s.Input + s.CacheRead + s.CacheWrite
	if total == 0 {
		return 0
	}
	return float64(s.CacheRead) / float64(total)
}

// TotalTokens is every prompt and output token.
func (s Summary) TotalTokens() int64 { return s.Input + s.Output + s.CacheRead + s.CacheWrite }

func (s *Store) Summary(f Filter) (Summary, error) {
	where, args := f.where()
	var sum Summary
	err := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
		COALESCE(SUM(est_cost_usd),0), COALESCE(SUM(CASE WHEN est_cost_usd IS NULL THEN 1 ELSE 0 END),0)
		FROM ai_usage_log`+where, args...).
		Scan(&sum.Calls, &sum.Input, &sum.Output, &sum.CacheRead, &sum.CacheWrite, &sum.Cost, &sum.UnpricedCalls)
	return sum, err
}

// Row is one recorded request.
type Row struct {
	ID                                   int64
	RecordedAt                           time.Time
	Integration, Model, Bot, Session     string
	Kind                                 string
	Round                                int
	Input, Output, CacheRead, CacheWrite int64
	Cost                                 sql.NullFloat64
}

// List returns one page of rows, newest first, and the total row count.
func (s *Store) List(f Filter, page, perPage int) ([]Row, int64, error) {
	where, args := f.where()
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_usage_log`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	rows, err := s.db.Query(`SELECT id, recorded_at, integration, COALESCE(model,''), COALESCE(bot,''),
		COALESCE(session,''), COALESCE(kind,''), round,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, est_cost_usd
		FROM ai_usage_log`+where+` ORDER BY recorded_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.RecordedAt, &r.Integration, &r.Model, &r.Bot, &r.Session, &r.Kind, &r.Round,
			&r.Input, &r.Output, &r.CacheRead, &r.CacheWrite, &r.Cost); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ErrorRow is one recorded failure.
type ErrorRow struct {
	RecordedAt                             time.Time
	Integration, Model, Bot, Session, Kind string
	Message                                string
}

// Errors returns the newest failures in a period.
func (s *Store) Errors(f Filter, limit int) ([]ErrorRow, error) {
	where, args := f.where()
	rows, err := s.db.Query(`SELECT recorded_at, integration, COALESCE(model,''), COALESCE(bot,''),
		COALESCE(session,''), COALESCE(kind,''), message
		FROM ai_error_log`+where+` ORDER BY recorded_at DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrorRow
	for rows.Next() {
		var e ErrorRow
		if err := rows.Scan(&e.RecordedAt, &e.Integration, &e.Model, &e.Bot, &e.Session, &e.Kind, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrorCount counts failures since t.
func (s *Store) ErrorCount(since time.Time) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_error_log WHERE recorded_at >= ?`, since.UTC()).Scan(&n)
	return n, err
}

// Clear deletes all usage and error rows.
func (s *Store) Clear() error {
	if _, err := s.db.Exec(`DELETE FROM ai_usage_log`); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM ai_error_log`)
	return err
}

// Prune deletes rows older than retention. A retention of 0 keeps everything.
func (s *Store) Prune(retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := s.now().Add(-retention)
	var deleted int64
	for _, table := range []string{"ai_usage_log", "ai_error_log"} {
		res, err := s.db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE recorded_at < ?`, table), cutoff)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}

// RunPruner prunes once now and then daily until stop is closed.
func (s *Store) RunPruner(retention time.Duration, stop <-chan struct{}) {
	if retention <= 0 {
		return
	}
	prune := func() {
		if n, err := s.Prune(retention); err != nil {
			slog.Error("Failed to prune AI usage", "err", err)
		} else if n > 0 {
			slog.Info("Pruned old AI usage rows", "deleted", n, "retention_days", int(retention.Hours()/24))
		}
	}
	prune()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			prune()
		case <-stop:
			return
		}
	}
}
