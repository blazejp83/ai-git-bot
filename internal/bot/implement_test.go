package bot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/config"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
	"github.com/tmseidel/ai-git-bot/internal/hooks"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

func TestFormatDiscussion(t *testing.T) {
	bot := newBotIdentity(repo.Account{ID: "42", Login: "ai-bot"}, "")
	day := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	got := formatDiscussion([]repo.IssueComment{
		{ID: 1, User: "alice", UserID: "1", Body: "Please also handle empty input.", CreatedAt: day},
		{ID: 2, User: "ai-bot", UserID: "42", Body: "**AI Agent**: working on it"},
		{ID: 3, User: "bob", UserID: "2", Body: "   "},
		{ID: 4, User: "carol", UserID: "3", Body: "@ai-bot add a test"}, // the triggering comment
	}, bot, 4)
	if got != "**@alice** (2026-11-20):\nPlease also handle empty input." {
		t.Errorf("discussion = %q", got)
	}

	var many []repo.IssueComment
	for i := 0; i < 10; i++ {
		many = append(many, repo.IssueComment{ID: int64(i + 1), User: "u", Body: strings.Repeat("x", 3000) + string(rune('A'+i))})
	}
	long := formatDiscussion(many, bot, 0)
	if !strings.HasPrefix(long, "…(older comments left out)") || !strings.HasSuffix(long, "J") || strings.Contains(long, "xA") {
		t.Errorf("oldest comments must be dropped first (len %d)", len(long))
	}
}

func TestCommitMessage(t *testing.T) {
	first := commitMessage(implementationRun{issue: 5, title: "Add CSV export"}, "Added an export handler.")
	if first != "agent: implement #5 - Add CSV export\n\nAdded an export handler." {
		t.Errorf("first = %q", first)
	}
	follow := commitMessage(implementationRun{issue: 5, title: "x", branch: "ai-agent/issue-5"}, "")
	if follow != "agent: follow-up on #5" {
		t.Errorf("follow-up = %q", follow)
	}
}

// fakeGitProvider stands in for Gitea in an implementation run.
type fakeGitProvider struct {
	repo.Client
	mu       sync.Mutex
	comments []string
	prs      []string // head branches
}

func (f *fakeGitProvider) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", nil
}
func (f *fakeGitProvider) PostComment(_ context.Context, _, _ string, _ int64, body string) error {
	f.mu.Lock()
	f.comments = append(f.comments, body)
	f.mu.Unlock()
	return nil
}
func (f *fakeGitProvider) AddReaction(context.Context, string, string, int64, string) error {
	return nil
}
func (f *fakeGitProvider) ListIssueComments(context.Context, string, string, int64) ([]repo.IssueComment, error) {
	return []repo.IssueComment{
		{ID: 1, User: "alice", UserID: "1", Body: "CSV must use semicolons."},
		{ID: 2, User: "ai-bot", UserID: "42", Body: "**AI Agent**: I've been assigned"},
	}, nil
}
func (f *fakeGitProvider) GetIssue(context.Context, string, string, int64) (repo.IssueDetails, error) {
	return repo.IssueDetails{Title: "Add CSV export", Body: "Export orders as CSV."}, nil
}
func (f *fakeGitProvider) CreatePR(_ context.Context, _, _, _, _, head, _ string) (int64, error) {
	f.prs = append(f.prs, head)
	return 9, nil
}
func (f *fakeGitProvider) FormatPRRef(n int64) string { return "#9" }

// recordingModel returns canned responses and keeps the first prompt it saw.
type recordingModel struct {
	responses []*ai.ChatResponse
	prompt    string
}

func (m *recordingModel) SupportsNativeTools() bool { return true }
func (m *recordingModel) ReviewDiff(context.Context, ai.ReviewRequest) (string, error) {
	return "", nil
}
func (m *recordingModel) Chat(context.Context, []ai.Message, string, ai.ChatOpts) (string, error) {
	return "", nil
}
func (m *recordingModel) ChatWithTools(_ context.Context, msgs []ai.ConversationMessage, _ []ai.ToolDef, _ ai.ChatOpts) (*ai.ChatResponse, error) {
	if m.prompt == "" && len(msgs) > 0 {
		m.prompt = msgs[0].Content
	}
	if len(m.responses) == 0 {
		return nil, errors.New("script exhausted")
	}
	r := m.responses[0]
	m.responses = m.responses[1:]
	return r, nil
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=dev", "GIT_AUTHOR_EMAIL=dev@x", "GIT_COMMITTER_NAME=dev", "GIT_COMMITTER_EMAIL=dev@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestImplementationRunAndContinuation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// A bare "origin" with main, plus a stale agent branch from an earlier failed run.
	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, work, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "old.txt"), []byte("obsolete\n"), 0o644)
	os.WriteFile(filepath.Join(work, "README.md"), []byte("app\n"), 0o644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "init")
	gitRun(t, work, "checkout", "-q", "-b", "ai-agent/issue-5")
	os.WriteFile(filepath.Join(work, "stale.txt"), []byte("x\n"), 0o644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "stale attempt")
	gitRun(t, work, "checkout", "-q", "main")
	gitRun(t, filepath.Dir(bare), "clone", "-q", "--bare", work, bare)

	store := reviewStore(t)
	h := &WebhookHandler{db: store.DB(), cfg: &config.Config{AgentBranchPrefix: "ai-agent/", AgentMaxTokens: 1000},
		cloneURLOverride: func(string, string) string { return "file://" + bare }}
	bs := botSettings{id: 1, name: "coder", agentEnabled: true, identity: newBotIdentity(repo.Account{ID: "42", Login: "ai-bot"}, "")}
	provider := &fakeGitProvider{}
	// A hook subscribed to the implementation events records what is emitted.
	h.SetHooks(hooks.NewService(store.DB(), encrypt.New("k")))
	store.DB().Exec(`INSERT INTO hook_endpoints (name, url, event_types, enabled, created_at, updated_at)
		VALUES ('h', 'http://127.0.0.1:1/', 'implementation.started,implementation.completed,implementation.failed', 1, datetime(), datetime())`)

	call := func(id, name string, in map[string]any) *ai.ChatResponse {
		return &ai.ChatResponse{Content: []ai.ContentBlock{{Type: "tool_use", ToolCall: &ai.ToolCall{ID: id, Name: name, Input: in}}}}
	}

	// First run: add a file, delete one, finish.
	first := &recordingModel{responses: []*ai.ChatResponse{
		call("1", "write_file", map[string]any{"path": "export.go", "content": "package app\n"}),
		call("2", "shell", map[string]any{"command": "git rm -q old.txt"}),
		call("3", "done", map[string]any{"result": "Added the CSV export."}),
	}}
	h.runImplementation(context.Background(), first, provider, "me", "app", &webhook.Issue{Number: 5, Title: "Add CSV export", Body: "Export orders as CSV."}, "sys", bs, []string{"git"})

	if len(provider.prs) != 1 || provider.prs[0] != "ai-agent/issue-5-2" {
		t.Fatalf("PR heads = %v; the stale branch must not be overwritten", provider.prs)
	}
	if got := gitRun(t, bare, "log", "-1", "--format=%an|%s", "ai-agent/issue-5-2"); got != "ai-bot|agent: implement #5 - Add CSV export" {
		t.Errorf("commit = %q", got)
	}
	files := gitRun(t, bare, "ls-tree", "--name-only", "ai-agent/issue-5-2")
	if !strings.Contains(files, "export.go") || strings.Contains(files, "old.txt") {
		t.Errorf("files on branch: %s", files)
	}
	if gitRun(t, bare, "log", "-1", "--format=%s", "ai-agent/issue-5") != "stale attempt" {
		t.Error("the stale branch was changed")
	}
	if !strings.Contains(first.prompt, "CSV must use semicolons.") || strings.Contains(first.prompt, "I've been assigned") {
		t.Errorf("discussion in prompt:\n%s", first.prompt)
	}
	last := provider.comments[len(provider.comments)-1]
	if !strings.Contains(last, "Created #9") || !strings.Contains(last, "`old.txt` (deleted)") {
		t.Errorf("summary comment: %s", last)
	}

	// A comment on the agent's PR continues on the same branch.
	event := &webhook.Event{PullRequest: &webhook.PullRequest{Number: 9},
		Comment: &webhook.Comment{ID: 77, Body: "@ai-bot please add a header row", User: "alice"}}
	prev, ok := h.implementationTarget("me", "app", event, bs)
	if !ok || prev.Issue != 5 || prev.Branch != "ai-agent/issue-5-2" || prev.PRNumber != 9 {
		t.Fatalf("implementationTarget = %+v %v", prev, ok)
	}
	review := &webhook.Event{PullRequest: &webhook.PullRequest{Number: 9}, Comment: &webhook.Comment{Body: "@ai-bot #review"}}
	if _, ok := h.implementationTarget("me", "app", review, bs); ok {
		t.Error("#review on the agent's PR must stay a review")
	}

	second := &recordingModel{responses: []*ai.ChatResponse{
		call("1", "write_file", map[string]any{"path": "export.go", "content": "package app\n\n// header row\n"}),
		call("2", "done", map[string]any{"result": "Added a header row."}),
	}}
	h.continueImplementation(context.Background(), second, provider, "me", "app", prev, event.Comment, "alice", "sys", bs, []string{"git"})

	if len(provider.prs) != 1 {
		t.Errorf("a continuation must not open another PR: %v", provider.prs)
	}
	if got := gitRun(t, bare, "log", "--format=%s", "main..ai-agent/issue-5-2"); got != "agent: follow-up on #5\nagent: implement #5 - Add CSV export" {
		t.Errorf("branch log = %q", got)
	}
	for _, want := range []string{"branch `ai-agent/issue-5-2` contains your earlier changes", "please add a header row", "Commits on this branch:", "Add CSV export"} {
		if !strings.Contains(second.prompt, want) {
			t.Errorf("continuation prompt missing %q:\n%s", want, second.prompt)
		}
	}
	if impl, _ := runner.LatestImplementation(store.DB(), "me", "app", 5); impl.Status != "PR_CREATED" || impl.PRNumber != 9 {
		t.Errorf("session after continuation = %+v", impl)
	}

	rows, _ := store.DB().Query(`SELECT event_type, payload FROM hook_deliveries ORDER BY id`)
	var events []string
	var lastPayload string
	for rows.Next() {
		var ev string
		rows.Scan(&ev, &lastPayload)
		events = append(events, ev)
	}
	rows.Close()
	if strings.Join(events, ",") != "implementation.started,implementation.completed,implementation.started,implementation.completed" {
		t.Errorf("emitted events = %v", events)
	}
	if !strings.Contains(lastPayload, `"continuation":true`) || !strings.Contains(lastPayload, `"bot":"coder"`) || !strings.Contains(lastPayload, `"fullName":"me/app"`) {
		t.Errorf("last payload = %s", lastPayload)
	}
}
