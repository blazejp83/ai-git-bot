package bot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/db"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/structreview"
)

// fakePublisher records how a review was delivered.
type fakePublisher struct {
	repo.Client
	submitPosted bool
	submitErr    error
	submitted    []string // events
	comments     []string
}

func (f *fakePublisher) SubmitReview(_ context.Context, _, _ string, _ int64, body string, ev repo.ReviewEvent, _ string) (bool, error) {
	f.submitted = append(f.submitted, string(ev))
	return f.submitPosted, f.submitErr
}

func (f *fakePublisher) PostReviewComment(_ context.Context, _, _ string, _ int64, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

func reviewStore(t *testing.T) *structreview.Store {
	t.Helper()
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	return structreview.NewStore(conn)
}

func TestPublishReview(t *testing.T) {
	s := reviewStore(t)
	rv, _ := s.Create(1, "o", "r", 5, "sha", 0)
	s.SetAssessment(rv.ID, "Fine.", nil, nil, false)
	rv, _ = s.Get(rv.ID)

	cases := []struct {
		name         string
		decision     string
		pub          *fakePublisher
		wantSubmit   int
		wantComments int
		wantNote     string
	}{
		{"comment only", structreview.DecisionComment, &fakePublisher{}, 0, 1, ""},
		{"formal approve", structreview.DecisionApprove, &fakePublisher{submitPosted: true}, 1, 0, ""},
		{"provider refuses (own PR)", structreview.DecisionApprove,
			&fakePublisher{submitErr: &repo.HTTPError{Status: 422, Body: `{"message":"approve your own pull is not allowed"}`}}, 1, 1,
			"Decision: approve. It could not be submitted as a formal review (the provider answered HTTP 422), so it is posted as a comment."},
		{"GitLab cannot request changes", structreview.DecisionRequestChanges,
			&fakePublisher{submitPosted: true, submitErr: repo.ErrDecisionUnsupported}, 1, 0, ""},
		{"unknown outcome is not re-posted", structreview.DecisionRequestChanges,
			&fakePublisher{submitErr: errors.New("context deadline exceeded")}, 1, 0, ""},
	}
	for _, c := range cases {
		publishReview(context.Background(), c.pub, s, rv, nil, c.decision, "o", "r", 5, "sha")
		if len(c.pub.submitted) != c.wantSubmit || len(c.pub.comments) != c.wantComments {
			t.Errorf("%s: submitted %v, comments %d", c.name, c.pub.submitted, len(c.pub.comments))
			continue
		}
		if c.wantNote != "" && !strings.Contains(c.pub.comments[0], c.wantNote) {
			t.Errorf("%s: fallback note missing:\n%s", c.name, c.pub.comments[0])
		}
	}
}

func TestChangedPaths(t *testing.T) {
	got := changedPaths("diff --git a/x.go b/y.go\n--- a/x.go\n+++ b/y.go\n@@\ndiff --git a/new.go b/new.go\n--- /dev/null\n+++ b/new.go\n")
	if !got["x.go"] || !got["y.go"] || !got["new.go"] || got["/dev/null"] {
		t.Errorf("changedPaths = %v", got)
	}
}

// scripted is a model that returns canned responses in order.
type scripted struct{ responses []*ai.ChatResponse }

func (m *scripted) SupportsNativeTools() bool { return true }
func (m *scripted) ReviewDiff(context.Context, ai.ReviewRequest) (string, error) {
	return "", nil
}
func (m *scripted) Chat(context.Context, []ai.Message, string, ai.ChatOpts) (string, error) {
	return "", nil
}
func (m *scripted) ChatWithTools(context.Context, []ai.ConversationMessage, []ai.ToolDef, ai.ChatOpts) (*ai.ChatResponse, error) {
	if len(m.responses) == 0 {
		return nil, errors.New("script exhausted")
	}
	r := m.responses[0]
	m.responses = m.responses[1:]
	return r, nil
}

func toolCall(id, name string, in map[string]any) *ai.ChatResponse {
	return &ai.ChatResponse{Content: []ai.ContentBlock{{Type: "tool_use", ToolCall: &ai.ToolCall{ID: id, Name: name, Input: in}}}}
}

// The agent records findings through the tools, cannot finish without an
// assessment, and the stored review decides and renders.
func TestStructuredReviewAgentRun(t *testing.T) {
	s := reviewStore(t)
	conn := s.DB()
	sess, _ := runner.CreateSession(conn, runner.ModeReview, "o", "r", 5, "pr", "p")
	rv, _ := s.Create(1, "o", "r", 5, "abc123", sess.SessionID)

	model := &scripted{responses: []*ai.ChatResponse{
		{Content: []ai.ContentBlock{{Type: "text", Text: "Looks good overall."}}}, // nudged
		toolCall("1", "review_upsert_finding", map[string]any{"severity": "BLOCKER", "title": "SQL injection", "explanation": "Query is built from input.",
			"recommendation": "Use parameters.", "locations": []any{map[string]any{"path": "db.go", "start_line": float64(12)}}}),
		toolCall("2", structreview.FinishTool, nil), // refused: no assessment
		toolCall("3", "review_set_assessment", map[string]any{"summary": "Adds a search endpoint.", "areas_reviewed": []any{"db.go"}}),
		toolCall("4", structreview.FinishTool, nil),
	}}
	r := runner.New(model, &runner.Workspace{Dir: t.TempDir()}, &runner.LogReporter{}, sess, runner.Config{
		Mode: runner.ModeReview, MaxTurns: 10, PRDiff: "diff --git a/db.go b/db.go\n+++ b/db.go\n",
		ExtraTools: structreview.Tools(s, rv.ID, func(p string) bool { return p == "db.go" }),
		FinishTool: structreview.FinishTool, OnFinish: structreview.FinishCheck(s, rv.ID), RequireFinishTool: true,
	})
	res, err := r.Run(context.Background(), "review")
	if err != nil || res.Status != runner.StatusComplete || res.TurnCount != 5 {
		t.Fatalf("run: %+v %v", res, err)
	}
	got, _ := s.Get(rv.ID)
	zero, two := 0, 2
	if d := structreview.Decide(got, structreview.Thresholds{MaxBlockers: &zero, MaxMedium: &two}); d != structreview.DecisionRequestChanges {
		t.Errorf("decision = %s", d)
	}
	md := structreview.Render(got, nil, structreview.Outcome{Decision: structreview.DecisionRequestChanges, Formal: true})
	if !strings.Contains(md, "R1-F1 · BLOCKER · SQL injection") || !strings.Contains(md, "Adds a search endpoint.") {
		t.Errorf("rendered:\n%s", md)
	}
}
