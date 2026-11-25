package usage

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/db"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPriceFor(t *testing.T) {
	cases := map[string]float64{ // model -> input price
		"claude-opus-5-5":                4,
		"claude-opus-5":                  5, // not confused with 5-5
		"claude-haiku-4-5-20251001":      1, // dated id
		"us.anthropic.claude-sonnet-5-5": 2, // platform prefix
		"anthropic.claude-fable-5-2":     8,
	}
	for model, want := range cases {
		p, ok := PriceFor(model)
		if !ok || p.Input != want {
			t.Errorf("%s: %+v %v, want input %v", model, p, ok, want)
		}
	}
	if _, ok := PriceFor("llama3.1:8b"); ok {
		t.Error("local models have no price")
	}
}

func TestCost(t *testing.T) {
	opus, _ := PriceFor("claude-opus-5-5")
	// 1M uncached in, 1M out, 1M cache read, 1M cache write = 4 + 20 + 0.20 + 5
	if got := opus.Cost(Tokens{Input: 1e6, Output: 1e6, CacheRead: 1e6, CacheWrite: 1e6}); !near(got, 29.2) {
		t.Errorf("opus cost = %v", got)
	}
	haiku, _ := PriceFor("claude-haiku-5-5")
	if got := haiku.Cost(Tokens{Input: 50_000, Output: 1_000}); !near(got, (50_000*0.10+1_000*0.50)/1e6) {
		t.Errorf("haiku small prompt = %v", got)
	}
	if got := haiku.Cost(Tokens{Input: 150_000, Output: 1_000}); !near(got, (150_000*0.50+1_000*2.50)/1e6) {
		t.Errorf("haiku large prompt = %v (should use the >100K tier)", got)
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	return NewStore(conn)
}

func TestStoreRecordsSummarisesAndPrunes(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	// Prompt 10,000 = 1,000 uncached + 8,000 cache read + 1,000 cache write.
	s.RecordUsage(ai.UsageRecord{Integration: "Anthropic", Model: "claude-opus-5-5", Bot: "rev", Session: "o/r#7", Round: 2, Kind: "agent",
		Usage: ai.TokenUsage{PromptTokens: 10_000, CompletionTokens: 500, CacheReadTokens: 8_000, CacheWriteTokens: 1_000}})
	s.RecordUsage(ai.UsageRecord{Integration: "Local", Model: "llama3.1", Kind: "chat", Usage: ai.TokenUsage{PromptTokens: 100, CompletionTokens: 10}})
	s.RecordError(ai.ErrorRecord{Integration: "Anthropic", Model: "claude-opus-5-5", Kind: "agent", Message: "HTTP 529 overloaded"})

	now = now.Add(-40 * 24 * time.Hour) // an old row
	s.RecordUsage(ai.UsageRecord{Integration: "Anthropic", Model: "claude-opus-5-5", Kind: "chat", Usage: ai.TokenUsage{PromptTokens: 1, CompletionTokens: 1}})
	now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	sum, err := s.Summary(Filter{From: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	wantCost := (1_000*4.0 + 500*20.0 + 8_000*0.20 + 1_000*5.0) / 1e6
	if sum.Calls != 2 || sum.Input != 1_100 || sum.Output != 510 || sum.CacheRead != 8_000 || sum.CacheWrite != 1_000 ||
		!near(sum.Cost, wantCost) || sum.UnpricedCalls != 1 {
		t.Errorf("summary = %+v, want cost %v", sum, wantCost)
	}
	if r := sum.CacheHitRatio(); !near(r, 8_000.0/10_100) {
		t.Errorf("cache hit ratio = %v", r)
	}

	rows, total, err := s.List(Filter{}, 1, 2)
	if err != nil || total != 3 || len(rows) != 2 || rows[0].Model != "llama3.1" { // newest first; same time → higher id first
		t.Fatalf("list = %+v total %d err %v", rows, total, err)
	}
	if rows[1].Session != "o/r#7" || rows[1].Round != 2 || !rows[1].Cost.Valid {
		t.Errorf("agent row = %+v", rows[1])
	}

	if n, _ := s.ErrorCount(now.Add(-7 * 24 * time.Hour)); n != 1 {
		t.Errorf("error count = %d", n)
	}
	if n, err := s.Prune(30 * 24 * time.Hour); err != nil || n != 1 {
		t.Errorf("prune deleted %d (%v), want the 40-day-old row", n, err)
	}
	if n, _ := s.Prune(0); n != 0 {
		t.Error("retention 0 must keep everything")
	}
	s.Clear()
	if sum, _ := s.Summary(Filter{}); sum.Calls != 0 {
		t.Error("clear left rows")
	}
}
