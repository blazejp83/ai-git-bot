package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/hooks"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

// implementationRun is one run of the implementation agent on an issue: the
// first one (from an assignment) or a continuation (from a comment).
type implementationRun struct {
	issue     int64
	title     string
	body      string
	branch    string // existing branch to continue on; "" for a first run
	prNumber  int64  // existing PR; 0 if none yet
	request   string // the comment asking for more; "" for a first run
	requester string
	triggerID int64 // the comment that started this run (left out of the discussion)
}

func (r implementationRun) continuation() bool { return r.branch != "" }

// runImplementation starts the agent on an issue it was assigned to.
func (h *WebhookHandler) runImplementation(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, issue *webhook.Issue, promptText string, bs botSettings, allowlist []string) {
	h.implement(ctx, aiClient, repoClient, owner, repoName, implementationRun{
		issue: issue.Number, title: issue.Title, body: issue.Body,
	}, promptText, bs, allowlist)
}

// continueImplementation runs the agent again on the branch of an earlier
// run, for a comment on the issue or on the pull request the agent opened.
func (h *WebhookHandler) continueImplementation(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, prev runner.Implementation, comment *webhook.Comment, commenter, promptText string, bs botSettings, allowlist []string) {
	run := implementationRun{
		issue: prev.Issue, branch: prev.Branch, prNumber: prev.PRNumber,
		request: strings.TrimSpace(bs.identity.stripMentions(comment.Body)), requester: commenter, triggerID: comment.ID,
	}
	// The issue's title and description, for context.
	if details, err := repoClient.GetIssue(ctx, owner, repoName, prev.Issue); err == nil {
		run.title, run.body = details.Title, details.Body
	}
	h.implement(ctx, aiClient, repoClient, owner, repoName, run, promptText, bs, allowlist)
}

func (h *WebhookHandler) implement(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, run implementationRun, promptText string, bs botSettings, allowlist []string) {
	say := func(msg string) { repoClient.PostComment(ctx, owner, repoName, run.issue, "**AI Agent**: "+msg) }
	h.emit(bs, owner, repoName, hooks.ImplementationStarted, map[string]any{"issue": run.issue, "continuation": run.continuation(), "branch": run.branch})
	var session *runner.Session
	// fail tells the issue, marks the session and emits the failure event.
	fail := func(msg string) {
		say(msg)
		if session != nil {
			session.SetStatus("FAILED")
		}
		h.emit(bs, owner, repoName, hooks.ImplementationFailed, map[string]any{"issue": run.issue, "error": msg})
	}

	baseBranch, _ := repoClient.GetDefaultBranch(ctx, owner, repoName)
	if baseBranch == "" {
		baseBranch = "main"
	}
	cloneBranch := baseBranch
	if run.continuation() {
		cloneBranch = run.branch
		say(fmt.Sprintf("Working on @%s's request on branch `%s`…", run.requester, run.branch))
	} else {
		say("I've been assigned to this issue. Setting up workspace...")
	}
	slog.Info("Starting implementation", "issue", run.issue, "branch", cloneBranch, "continuation", run.continuation())

	ws, err := runner.NewWorkspace(ctx, h.cloneURL(bs.gitIntID, owner, repoName), owner, repoName, cloneBranch)
	if err != nil {
		slog.Error("Failed to create workspace", "err", err)
		if run.continuation() {
			fail(fmt.Sprintf("I could not check out branch `%s` (was it deleted or merged?). Assign me to the issue again to start over. Error: %v", run.branch, err))
		} else {
			fail("Failed to set up workspace: " + err.Error())
		}
		return
	}
	defer ws.Cleanup()

	session, err = runner.CreateSession(h.db, runner.ModeImplementation, owner, repoName, run.issue, "issue", promptText)
	if err != nil {
		slog.Error("Failed to create runner session", "err", err)
		return
	}
	if run.continuation() {
		session.SetBranch(run.branch)
		if run.prNumber > 0 {
			session.SetPR(run.prNumber)
		}
	}

	discussion := ""
	if comments, err := repoClient.ListIssueComments(ctx, owner, repoName, run.issue); err == nil {
		discussion = formatDiscussion(comments, bs.identity, run.triggerID)
	}
	branchSummary := ""
	if run.continuation() {
		branchSummary = ws.BranchSummary(ctx, baseBranch)
	}

	r := runner.New(aiClient, ws, &runner.CommentReporter{RepoClient: repoClient, Owner: owner, Repo: repoName, Number: run.issue, Verbose: true}, session, runner.Config{
		Mode:           runner.ModeImplementation,
		MaxTurns:       bs.implTurns(50),
		SystemPrompt:   promptText,
		MaxTokens:      h.cfg.AgentMaxTokens,
		ShellAllowlist: allowlist,
		ShellTimeout:   300,
	})
	result, err := r.Run(ctx, implementationPrompt(run, discussion, branchSummary))
	if err != nil {
		slog.Error("Implementation runner failed", "err", err)
		fail("Implementation failed: " + err.Error())
		return
	}

	changes, err := ws.Changes()
	if err != nil {
		fail("Could not list the changed files: " + err.Error())
		return
	}
	if len(changes) == 0 {
		say("No files were changed. " + result.Text)
		h.emit(bs, owner, repoName, hooks.ImplementationCompleted, map[string]any{"issue": run.issue, "continuation": run.continuation(), "files": 0})
		if run.continuation() {
			session.SetStatus("PR_CREATED")
		} else {
			session.SetStatus("COMPLETED")
		}
		return
	}

	author, email := commitAuthor(bs)
	message := commitMessage(run, result.Text)
	if _, err := ws.Commit(ctx, message, author, email); err != nil {
		fail("Could not commit the changes: " + err.Error())
		return
	}
	branch := run.branch
	if run.continuation() {
		if err := ws.Push(ctx, branch); err != nil {
			fail(fmt.Sprintf("Could not push to `%s`: %v. If someone pushed to the branch meanwhile, mention me again.", branch, err))
			return
		}
	} else {
		branch, err = ws.PushNewBranch(ctx, h.cfg.AgentBranchPrefix+fmt.Sprintf("issue-%d", run.issue))
		if err != nil {
			fail("Could not push the changes: " + err.Error())
			return
		}
	}
	session.SetBranch(branch)

	prNumber := run.prNumber
	if prNumber == 0 {
		prTitle := fmt.Sprintf("AI Agent: %s (fixes #%d)", run.title, run.issue)
		prBody := fmt.Sprintf("Fixes #%d\n\n%s\n\n---\n*Generated by AI Agent. Mention me on this pull request or the issue to ask for changes.*", run.issue, result.Text)
		prNumber, err = repoClient.CreatePR(ctx, owner, repoName, prTitle, prBody, branch, baseBranch)
		if err != nil {
			fail(fmt.Sprintf("Pushed to `%s`, but could not open a pull request: %v", branch, err))
			return
		}
	}
	session.SetPR(prNumber)
	session.SetStatus("PR_CREATED")
	h.emit(bs, owner, repoName, hooks.ImplementationCompleted, map[string]any{"issue": run.issue, "pr": prNumber, "branch": branch,
		"continuation": run.continuation(), "files": len(changes)})

	var files strings.Builder
	for _, c := range changes {
		if c.Deleted {
			fmt.Fprintf(&files, "- `%s` (deleted)\n", c.Path)
		} else {
			fmt.Fprintf(&files, "- `%s`\n", c.Path)
		}
	}
	if run.continuation() {
		say(fmt.Sprintf("Pushed a follow-up commit to `%s` (%s).\n\n**Files changed** (%d):\n%s\n%s",
			branch, repoClient.FormatPRRef(prNumber), len(changes), files.String(), result.Text))
	} else {
		say(fmt.Sprintf("Implementation complete! Created %s\n\n**Files changed** (%d):\n%s",
			repoClient.FormatPRRef(prNumber), len(changes), files.String()))
	}
	slog.Info("Implementation completed", "issue", run.issue, "pr", prNumber, "branch", branch, "turns", result.TurnCount)
}

// commitAuthor is the identity on the agent's commits: the bot's account.
func commitAuthor(bs botSettings) (name, email string) {
	name = bs.identity.login
	if name == "" {
		name = "ai-git-bot"
	}
	return name, name + "@users.noreply.ai-git-bot"
}

func commitMessage(run implementationRun, summary string) string {
	subject := fmt.Sprintf("agent: implement #%d", run.issue)
	if run.title != "" {
		subject += " - " + run.title
	}
	if run.continuation() {
		subject = fmt.Sprintf("agent: follow-up on #%d", run.issue)
	}
	if len(subject) > 72 {
		subject = subject[:71] + "…"
	}
	body := strings.TrimSpace(summary)
	if len(body) > 4000 {
		body = body[:4000] + "…"
	}
	if body == "" {
		return subject
	}
	return subject + "\n\n" + body
}

const (
	maxDiscussionChars = 20000
	maxCommentChars    = 4000
)

// formatDiscussion renders an issue's comments for the prompt: the bot's own
// comments, empty ones and the triggering comment are left out; each comment
// is capped and the oldest are dropped first when the total is too long.
func formatDiscussion(comments []repo.IssueComment, bot botIdentity, triggerID int64) string {
	var parts []string
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if body == "" || (triggerID != 0 && c.ID == triggerID) || bot.matches(c.UserID, "", c.User) {
			continue
		}
		if len(body) > maxCommentChars {
			body = body[:maxCommentChars] + "…(shortened)"
		}
		when := ""
		if !c.CreatedAt.IsZero() {
			when = " (" + c.CreatedAt.Format("2006-01-02") + ")"
		}
		parts = append(parts, fmt.Sprintf("**@%s**%s:\n%s", c.User, when, body))
	}
	total := 0
	for i := len(parts) - 1; i >= 0; i-- {
		total += len(parts[i])
		if total > maxDiscussionChars {
			parts = append([]string{"…(older comments left out)"}, parts[i+1:]...)
			break
		}
	}
	return strings.Join(parts, "\n\n")
}

func implementationPrompt(run implementationRun, discussion, branchSummary string) string {
	var b strings.Builder
	if run.continuation() {
		fmt.Fprintf(&b, "Continue working on this issue. You already worked on it: the checked-out branch `%s` contains your earlier changes.\n\n", run.branch)
	} else {
		b.WriteString("Implement the following issue in this repository.\n\n")
	}
	fmt.Fprintf(&b, "**Issue #%d**: %s\n**Description**: %s\n", run.issue, run.title, run.body)
	if discussion != "" {
		fmt.Fprintf(&b, "\n**Discussion on the issue** (oldest first):\n%s\n", discussion)
	}
	if branchSummary != "" {
		fmt.Fprintf(&b, "\n%s\n", branchSummary)
	}
	if run.continuation() {
		fmt.Fprintf(&b, "\n**New request from @%s**:\n%s\n\nMake the requested changes on top of the existing work. ", run.requester, run.request)
	} else {
		b.WriteString("\nExplore the repository structure, read relevant files, then implement the changes. ")
	}
	b.WriteString(`Write files using write_file, and validate your changes by running the build/test commands.
When you're done, call the "done" tool with a short summary of what you changed (it becomes the commit message body).`)
	return b.String()
}
