package structreview

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/db"
	"github.com/tmseidel/ai-git-bot/internal/runner"
)

func ptr(n int) *int { return &n }

func TestDecide(t *testing.T) {
	balanced := Thresholds{MaxBlockers: ptr(0), MaxMedium: ptr(2)}
	review := func(sevs ...Severity) *Review {
		r := &Review{ID: 1}
		for i, s := range sevs {
			r.Findings = append(r.Findings, Finding{Ordinal: i + 1, Severity: s})
		}
		return r
	}
	cases := []struct {
		name string
		r    *Review
		t    Thresholds
		want string
	}{
		{"clean", review(), balanced, DecisionApprove},
		{"lows never block", review(Low, Low, Low, Low), balanced, DecisionApprove},
		{"two medium approve", review(Medium, Medium), balanced, DecisionApprove},
		{"three medium request changes", review(Medium, Medium, Medium), balanced, DecisionRequestChanges},
		{"one blocker", review(Blocker), balanced, DecisionRequestChanges},
		{"no thresholds only comments", review(Blocker), Thresholds{}, DecisionComment},
		{"inconclusive only comments", &Review{Inconclusive: true}, balanced, DecisionComment},
	}
	for _, c := range cases {
		if got := Decide(c.r, c.t); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	withdrawn := review(Blocker)
	withdrawn.Findings[0].Withdrawn = true
	if Decide(withdrawn, balanced) != DecisionApprove {
		t.Error("a withdrawn blocker must not block")
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

func runTool(t *testing.T, tools []runner.RegisteredTool, name string, in map[string]any) (string, error) {
	t.Helper()
	for _, tool := range tools {
		if tool.Def.Name == name {
			return tool.Execute(context.Background(), nil, in)
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

func TestToolsRecordAndRender(t *testing.T) {
	s := newStore(t)
	r, err := s.Create(3, "me", "app", 12, "0123456789abcdef", 0)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{"api/handler.go": true, "db/migrate.sql": true}
	tools := Tools(s, r.ID, func(p string) bool { return known[p] })
	loc := func(path string, a, b int) []any {
		return []any{map[string]any{"path": path, "start_line": float64(a), "end_line": float64(b)}}
	}

	out, err := runTool(t, tools, "review_upsert_finding", map[string]any{
		"severity": "medium", "category": "correctness", "title": "Off-by-one in paging",
		"explanation": "The last page is skipped.", "recommendation": "Use <= instead of <.", "locations": loc("api/handler.go", 40, 44)})
	if err != nil || out != "Recorded R1-F1 (MEDIUM): Off-by-one in paging" {
		t.Fatalf("add: %q %v", out, err)
	}
	runTool(t, tools, "review_upsert_finding", map[string]any{
		"severity": "LOW", "title": "Typo", "explanation": "x", "recommendation": "y"})
	runTool(t, tools, "review_upsert_finding", map[string]any{
		"severity": "BLOCKER", "category": "data", "title": "Migration drops a column", "explanation": "Data loss.",
		"impact": "User emails are lost.", "recommendation": "Copy before dropping.", "locations": loc("db/migrate.sql", 3, 3)})

	// Correct F1, withdraw F2: numbers stay stable.
	if out, err := runTool(t, tools, "review_upsert_finding", map[string]any{"id": "F1", "severity": "LOW", "title": "Paging edge case",
		"explanation": "Only when total is a multiple of the page size.", "recommendation": "Add a test."}); err != nil || !strings.HasPrefix(out, "Updated R1-F1 (LOW)") {
		t.Errorf("update: %q %v", out, err)
	}
	if _, err := runTool(t, tools, "review_remove_finding", map[string]any{"id": "R1-F2", "reason": "not a problem"}); err != nil {
		t.Error(err)
	}
	if out, _ := runTool(t, tools, "review_upsert_finding", map[string]any{"severity": "LOW", "title": "n", "explanation": "x", "recommendation": "y"}); !strings.Contains(out, "R1-F4") {
		t.Errorf("numbers must not be reused: %q", out)
	}

	// Validation.
	for name, in := range map[string]map[string]any{
		"bad severity":   {"severity": "CRITICAL", "title": "t", "explanation": "e", "recommendation": "r"},
		"unknown path":   {"severity": "LOW", "title": "t", "explanation": "e", "recommendation": "r", "locations": loc("nope.go", 1, 2)},
		"inverted range": {"severity": "LOW", "title": "t", "explanation": "e", "recommendation": "r", "locations": loc("api/handler.go", 9, 3)},
		"no explanation": {"severity": "LOW", "title": "t", "recommendation": "r"},
		"unknown id":     {"id": "F99", "severity": "LOW", "title": "t", "explanation": "e", "recommendation": "r"},
	} {
		if _, err := runTool(t, tools, "review_upsert_finding", in); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}

	// Completing needs an assessment.
	check := FinishCheck(s, r.ID)
	if _, err := check(FinishTool, nil); err == nil {
		t.Error("completing without an assessment must be refused")
	}
	runTool(t, tools, "review_set_assessment", map[string]any{"summary": "Adds paging and a migration.",
		"areas_reviewed": []any{"handler", "migration"}, "limitations": []any{"did not run the migration"}})
	if _, err := check(FinishTool, nil); err != nil {
		t.Errorf("complete after assessment: %v", err)
	}

	got, _ := s.Get(r.ID)
	if d := Decide(got, Thresholds{MaxBlockers: ptr(0), MaxMedium: ptr(2)}); d != DecisionRequestChanges {
		t.Errorf("decision = %s", d)
	}
	md := Render(got, func(p string, a, b int) string { return "https://git.lan/" + p }, Outcome{Decision: DecisionRequestChanges, Formal: true})
	for _, want := range []string{
		"## AI Code Review · R1",
		"**Changes requested** · 1 blocker · 0 medium · 2 low · commit `0123456789`",
		"### Assessment · R1-A\n\nAdds paging and a migration.",
		"#### R1-F3 · BLOCKER · Migration drops a column",
		"**Where:** [`db/migrate.sql` L3](https://git.lan/db/migrate.sql) · **Category:** data",
		"**Impact:** User emails are lost.",
		"**Reviewed:** handler; migration",
		"**Limitations:** did not run the migration",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered review missing %q\n%s", want, md)
		}
	}
	if strings.Contains(md, "R1-F2") || strings.Index(md, "R1-F3") > strings.Index(md, "R1-F1") {
		t.Errorf("withdrawn finding shown, or blocker not first:\n%s", md)
	}
}
