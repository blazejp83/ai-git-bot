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
	"github.com/tmseidel/ai-git-bot/internal/workflow"
)

func TestDocsPathAllowed(t *testing.T) {
	patterns := []string{"README.md", "docs/**/*.md"}
	for path, want := range map[string]bool{
		"README.md": true, "docs/guide.md": true, "docs/api/v2/auth.md": true, "sub/README.md": true,
		"src/main.go": false, "docs/notes.txt": false, "CHANGELOG.md": false,
	} {
		if got := docsPathAllowed(path, patterns); got != want {
			t.Errorf("docsPathAllowed(%q) = %v, want %v", path, got, want)
		}
	}
	tool := writeMarkdownTool(patterns)
	if _, err := tool.Execute(context.Background(), &runner.Workspace{Dir: t.TempDir()}, map[string]any{"path": "src/main.go", "content": "x"}); err == nil {
		t.Error("writing a non-documentation file must be refused")
	}
}

type readmeProvider struct {
	fakeGitProvider
	head repo.PRHead
	diff string
}

func (p *readmeProvider) GetPRHead(context.Context, string, string, int64) (repo.PRHead, error) {
	return p.head, nil
}
func (p *readmeProvider) GetPRDiff(context.Context, string, string, int64) (string, error) {
	return p.diff, nil
}

func TestReadmeSyncModes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	newOrigin := func(t *testing.T) (bare, headSHA string) {
		work := t.TempDir()
		bare = filepath.Join(t.TempDir(), "origin.git")
		gitRun(t, work, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(work, "README.md"), []byte("# App\n\nRun `app serve`.\n"), 0o644)
		gitRun(t, work, "add", ".")
		gitRun(t, work, "commit", "-q", "-m", "init")
		gitRun(t, work, "checkout", "-q", "-b", "feature")
		os.WriteFile(filepath.Join(work, "main.go"), []byte("package main // adds --port\n"), 0o644)
		gitRun(t, work, "add", ".")
		gitRun(t, work, "commit", "-q", "-m", "add --port flag")
		headSHA = gitRun(t, work, "rev-parse", "HEAD")
		gitRun(t, filepath.Dir(bare), "clone", "-q", "--bare", work, bare)
		return bare, headSHA
	}
	call := func(id, name string, in map[string]any) *ai.ChatResponse {
		return &ai.ChatResponse{Content: []ai.ContentBlock{{Type: "tool_use", ToolCall: &ai.ToolCall{ID: id, Name: name, Input: in}}}}
	}
	updateReadme := func() *recordingModel {
		return &recordingModel{responses: []*ai.ChatResponse{
			call("1", "write_markdown", map[string]any{"path": "README.md", "content": "# App\n\nRun `app serve --port 8080`.\n"}),
			call("2", "done", map[string]any{"result": "Documented the new --port flag."}),
		}}
	}
	setup := func(t *testing.T, mode, trigger string) (*WebhookHandler, *readmeProvider, botSettings, string) {
		bare, sha := newOrigin(t)
		store := reviewStore(t)
		h := &WebhookHandler{db: store.DB(), cfg: &config.Config{AgentMaxTokens: 1000},
			cloneURLOverride: func(string, string) string { return "file://" + bare }}
		p := &readmeProvider{head: repo.PRHead{SHA: sha, Branch: "feature", SourceRepo: "me/app", Ref: "refs/heads/feature", Open: true},
			diff: "diff --git a/main.go b/main.go\n+++ b/main.go\n+package main // adds --port\n"}
		bs := botSettings{id: 1, identity: newBotIdentity(repo.Account{ID: "42", Login: "ai-bot"}, ""),
			workflows: workflow.Settings{workflow.ReadmeSync: {"enabled": "true", "mode": mode, "trigger": trigger}}}
		return h, p, bs, bare
	}

	t.Run("comment", func(t *testing.T) {
		h, p, bs, bare := setup(t, "comment", "command")
		before := gitRun(t, bare, "rev-parse", "feature")
		h.runReadmeSync(context.Background(), updateReadme(), p, "me", "app", 3, bs, "mention the port flag", false)
		last := p.comments[len(p.comments)-1]
		if !strings.Contains(last, "+Run `app serve --port 8080`.") || !strings.Contains(last, "Documented the new --port flag.") {
			t.Errorf("diff comment:\n%s", last)
		}
		if gitRun(t, bare, "rev-parse", "feature") != before {
			t.Error("comment mode must not push")
		}
	})

	t.Run("commit", func(t *testing.T) {
		h, p, bs, bare := setup(t, "commit", "command")
		h.runReadmeSync(context.Background(), updateReadme(), p, "me", "app", 3, bs, "", false)
		if got := gitRun(t, bare, "log", "-2", "--format=%an|%s", "feature"); got != "ai-bot|docs: update documentation for #3\ndev|add --port flag" {
			t.Errorf("feature branch log = %q", got)
		}
		if !strings.Contains(gitRun(t, bare, "show", "feature:README.md"), "--port 8080") {
			t.Error("README not updated on the branch")
		}
	})

	t.Run("pr", func(t *testing.T) {
		h, p, bs, bare := setup(t, "pr", "command")
		h.runReadmeSync(context.Background(), updateReadme(), p, "me", "app", 3, bs, "", false)
		if len(p.prs) != 1 || p.prs[0] != "ai-docs/pr-3" {
			t.Fatalf("PRs = %v", p.prs)
		}
		if got := gitRun(t, bare, "log", "-1", "--format=%s", "ai-docs/pr-3"); got != "docs: update documentation for #3" {
			t.Errorf("docs branch = %q", got)
		}
	})

	t.Run("nothing to change", func(t *testing.T) {
		h, p, bs, _ := setup(t, "comment", "auto")
		quiet := &recordingModel{responses: []*ai.ChatResponse{call("1", "done", map[string]any{"result": "Docs are current."})}}
		h.runReadmeSync(context.Background(), quiet, p, "me", "app", 3, bs, "", true)
		if len(p.comments) != 0 {
			t.Errorf("an automatic run with nothing to change must stay silent: %v", p.comments)
		}
		asked := &recordingModel{responses: []*ai.ChatResponse{call("1", "done", map[string]any{"result": "Docs are current."})}}
		h.runReadmeSync(context.Background(), asked, p, "me", "app", 3, bs, "", false)
		if len(p.comments) != 1 || !strings.Contains(p.comments[0], "already matches") {
			t.Errorf("a requested run says so: %v", p.comments)
		}
	})
}
