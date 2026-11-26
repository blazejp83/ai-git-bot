package bot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/config"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

func TestWriterIssueTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Add CSV export":                               "AI Created Issue: Add CSV export",
		"AI Created Issue: Add CSV export":             "AI Created Issue: Add CSV export",
		"ai created issue:  AI Created Issue: Add CSV": "AI Created Issue: Add CSV",
	} {
		if got := writerIssueTitle(in); got != want {
			t.Errorf("writerIssueTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterFinishCheck(t *testing.T) {
	var out writerOutcome
	check := writerFinishCheck(&out)
	if _, err := check(toolCreateIssue, map[string]any{"title": "x", "background": "b", "requirements": []any{"r"}}); err == nil {
		t.Error("missing acceptance criteria must be refused")
	}
	if _, err := check(toolAskQuestions, map[string]any{"questions": []any{"1", "2", "3", "4", "5", "6", "7"}}); err == nil {
		t.Error("too many questions must be refused")
	}
	if _, err := check(toolAskQuestions, map[string]any{"questions": []any{"Which format?"}, "context": "I see two exporters."}); err != nil || out.lead != "I see two exporters." {
		t.Errorf("questions: %v %+v", err, out)
	}
	body := renderWriterIssue(&writerIssue{Title: "t", Background: "Orders cannot be exported.", Requirements: []string{"Add /export.csv"},
		Acceptance: []string{"Downloading gives a CSV"}, ImplementationNotes: "See api/orders.go."}, 4)
	for _, want := range []string{"## Background\n\nOrders cannot be exported.", "- Add /export.csv", "- [ ] Downloading gives a CSV", "## Implementation notes\n\nSee api/orders.go.", "from #4."} {
		if !strings.Contains(body, want) {
			t.Errorf("issue body missing %q:\n%s", want, body)
		}
	}
}

type writerProvider struct {
	fakeGitProvider
	created []string // title|body
}

func (w *writerProvider) CreateIssue(_ context.Context, _, _, title, body string) (int64, error) {
	w.created = append(w.created, title+"|"+body)
	return 12, nil
}

// toolsModel records the tool names it was offered.
type toolsModel struct {
	recordingModel
	tools []string
}

func (m *toolsModel) ChatWithTools(ctx context.Context, msgs []ai.ConversationMessage, tools []ai.ToolDef, o ai.ChatOpts) (*ai.ChatResponse, error) {
	if m.tools == nil {
		for _, t := range tools {
			m.tools = append(m.tools, t.Name)
		}
	}
	return m.recordingModel.ChatWithTools(ctx, msgs, tools, o)
}

func TestWriterFlow(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, work, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "orders.go"), []byte("package app\n"), 0o644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "init")
	gitRun(t, filepath.Dir(bare), "clone", "-q", "--bare", work, bare)

	store := reviewStore(t)
	h := &WebhookHandler{db: store.DB(), cfg: &config.Config{AgentMaxTokens: 1000},
		cloneURLOverride: func(string, string) string { return "file://" + bare }}
	bs := botSettings{id: 2, issueWorkflow: workflowWriter, identity: newBotIdentity(repo.Account{ID: "42", Login: "ai-bot"}, "")}
	p := &writerProvider{}
	call := func(id, name string, in map[string]any) *ai.ChatResponse {
		return &ai.ChatResponse{Content: []ai.ContentBlock{{Type: "tool_use", ToolCall: &ai.ToolCall{ID: id, Name: name, Input: in}}}}
	}
	issue := &webhook.Issue{Number: 4, Title: "Export orders", Body: "We need an export."}

	// First run asks questions.
	first := &toolsModel{recordingModel: recordingModel{responses: []*ai.ChatResponse{
		call("1", "read_file", map[string]any{"path": "orders.go"}),
		call("2", toolAskQuestions, map[string]any{"questions": []any{"CSV or Excel?", "All orders or a date range?"}}),
	}}}
	h.startWriter(context.Background(), first, p, "me", "app", issue, "sys", bs)

	tools := strings.Join(first.tools, ",")
	if strings.Contains(tools, "shell") || strings.Contains(tools, "write_file") || strings.Contains(tools, ",done") || !strings.Contains(tools, toolCreateIssue) {
		t.Errorf("writer tools = %s", tools)
	}
	last := p.comments[len(p.comments)-1]
	if !strings.Contains(last, "1. CSV or Excel?\n2. All orders or a date range?") || !strings.Contains(last, "no need to mention me") {
		t.Errorf("questions comment:\n%s", last)
	}
	if st, _, _ := runner.LatestStatus(store.DB(), runner.ModeWriter, "me", "app", "issue", 4); st != writerWaiting {
		t.Errorf("status after questions = %s", st)
	}

	// A reply without a mention continues the run, which creates the issue.
	reply := &webhook.Event{Issue: issue, Comment: &webhook.Comment{ID: 5, Body: "CSV, all orders.", User: "alice"}}
	second := &toolsModel{recordingModel: recordingModel{responses: []*ai.ChatResponse{
		call("1", toolCreateIssue, map[string]any{"title": "Export orders as CSV", "background": "Orders cannot be exported.",
			"requirements": []any{"Add GET /orders.csv"}, "acceptance_criteria": []any{"The file lists every order"}}),
	}}}
	if !h.routeToWriter(context.Background(), second, p, "me", "app", reply, false, "sys", bs) {
		t.Fatal("a reply to the writer's questions must continue it")
	}
	if !strings.Contains(second.prompt, "the answers are in the discussion") {
		t.Errorf("continuation prompt:\n%s", second.prompt)
	}
	if len(p.created) != 1 || !strings.HasPrefix(p.created[0], "AI Created Issue: Export orders as CSV|## Background") {
		t.Fatalf("created = %v", p.created)
	}
	if last := p.comments[len(p.comments)-1]; last != "**AI Writer**: Created a refined issue: #12 — Export orders as CSV" {
		t.Errorf("link comment = %q", last)
	}
	// After the issue is created, further replies are ordinary comments.
	if h.routeToWriter(context.Background(), &toolsModel{}, p, "me", "app", reply, false, "sys", bs) {
		t.Error("replies after the issue was created must not restart the writer")
	}

	// The writer does not rewrite an issue that has a coding session.
	runner.CreateSession(store.DB(), runner.ModeImplementation, "me", "app", 8, "issue", "p")
	n := len(p.comments)
	h.startWriter(context.Background(), &toolsModel{recordingModel: recordingModel{responses: nil}}, p, "me", "app", &webhook.Issue{Number: 8}, "sys", bs)
	if len(p.comments) != n+1 || !strings.Contains(p.comments[n], "already has a coding session") {
		t.Errorf("coding-session guard: %v", p.comments[n:])
	}
}
