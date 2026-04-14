package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/config"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
	"github.com/tmseidel/ai-git-bot/internal/prompt"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

type WebhookHandler struct {
	db            *sql.DB
	cfg           *config.Config
	aiFactory     *ai.ClientFactory
	repoFactory   *repo.ClientFactory
	promptService *prompt.Service
}

func NewWebhookHandler(db *sql.DB, enc *encrypt.Service, promptSvc *prompt.Service, cfg *config.Config) *WebhookHandler {
	return &WebhookHandler{
		db:            db,
		cfg:           cfg,
		aiFactory:     ai.NewClientFactory(enc),
		repoFactory:   repo.NewClientFactory(enc),
		promptService: promptSvc,
	}
}

func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	secret := chi.URLParam(r, "secret")

	var botID int64
	var botName, botUsername, providerType string
	var aiIntegrationID, gitIntegrationID int64
	var agentEnabled bool
	var botPrompt sql.NullString

	err := h.db.QueryRow(`
		SELECT b.id, b.name, b.username, gi.provider_type, b.ai_integration_id, b.git_integration_id,
		       b.agent_enabled, b.prompt
		FROM bots b
		JOIN git_integrations gi ON b.git_integration_id = gi.id
		WHERE b.webhook_secret = ? AND b.enabled = 1
	`, secret).Scan(&botID, &botName, &botUsername, &providerType, &aiIntegrationID, &gitIntegrationID, &agentEnabled, &botPrompt)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	var event *webhook.Event
	switch providerType {
	case "GITEA":
		event, err = webhook.ParseGitea(body)
	case "GITHUB":
		event, err = webhook.ParseGitHub(r.Header.Get("X-GitHub-Event"), body)
	case "GITLAB":
		event, err = webhook.ParseGitLab(r.Header.Get("X-Gitlab-Event"), body)
	case "BITBUCKET":
		event, err = webhook.ParseBitbucket(r.Header.Get("X-Event-Key"), body)
	default:
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "unknown_provider"})
		return
	}

	if err != nil {
		slog.Error("Failed to parse webhook", "provider", providerType, "err", err)
		http.Error(w, "Failed to parse webhook", http.StatusBadRequest)
		return
	}

	h.db.Exec("UPDATE bots SET webhook_call_count = webhook_call_count + 1, last_webhook_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?", botID)

	if isBotUser(botUsername, event) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored_self"})
		return
	}

	promptName := ""
	if botPrompt.Valid {
		promptName = botPrompt.String
	}

	go h.dispatch(botID, botName, botUsername, aiIntegrationID, gitIntegrationID, agentEnabled, promptName, event)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *WebhookHandler) dispatch(botID int64, botName, botUsername string, aiIntID, gitIntID int64, agentEnabled bool, promptName string, event *webhook.Event) {
	ctx := context.Background()

	slog.Info("Dispatching webhook event",
		"bot", botName, "action", event.Action,
		"repo", event.Repo.FullName, "sender", event.Sender.Login)

	aiClient, err := h.aiFactory.GetClient(h.db, aiIntID)
	if err != nil {
		slog.Error("Failed to get AI client", "err", err)
		h.recordError(botID, "Failed to get AI client: "+err.Error())
		return
	}

	repoClient, err := h.repoFactory.GetClient(h.db, gitIntID)
	if err != nil {
		slog.Error("Failed to get repo client", "err", err)
		h.recordError(botID, "Failed to get repo client: "+err.Error())
		return
	}

	promptText := h.promptService.GetSystemPrompt(promptName)

	botAlias := "@" + botUsername
	hasBotMention := event.Comment != nil && strings.Contains(event.Comment.Body, botAlias)

	owner := event.Repo.Owner
	repoName := event.Repo.Name

	switch event.Action {
	case "opened", "synchronized":
		if event.PullRequest != nil {
			h.runReview(ctx, aiClient, repoClient, owner, repoName, event, promptText)
		}

	case "created":
		if event.Comment != nil && hasBotMention {
			prNum := resolvePRNumber(event)
			if prNum > 0 {
				h.runReviewFollowup(ctx, aiClient, repoClient, owner, repoName, prNum, event.Comment.Body, promptText)
			}
		}

	case "reviewed":
		// Review submitted — handle like a bot command if mentions the bot
		if event.PullRequest != nil {
			// Future: inspect review comments for bot mentions
		}

	case "assigned":
		if event.Issue != nil && agentEnabled {
			h.runImplementation(ctx, aiClient, repoClient, owner, repoName, event, promptText)
		}

	case "closed":
		if event.PullRequest != nil {
			// Clean up session
			slog.Info("PR closed", "pr", event.PullRequest.Number)
		}
	}
}

func (h *WebhookHandler) runReview(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, event *webhook.Event, promptText string) {
	pr := event.PullRequest

	// Fetch diff for the initial prompt
	diff, err := repoClient.GetPRDiff(ctx, owner, repoName, pr.Number)
	if err != nil || diff == "" {
		slog.Warn("No diff found for PR", "pr", pr.Number, "err", err)
		return
	}

	// Get or clone the repo for the agent to explore
	baseBranch := pr.Base.RefName
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Build clone URL from credentials
	cloneURL := buildCloneURL(h.db, event.Repo.Owner, event.Repo.Name, h.repoFactory)

	ws, err := runner.NewWorkspace(ctx, cloneURL, owner, repoName, baseBranch)
	if err != nil {
		slog.Error("Failed to create workspace for review", "err", err)
		// Fallback: do review without workspace exploration
		// (the AI just gets the diff, no tool calling)
		return
	}
	defer ws.Cleanup()

	session, err := runner.CreateSession(h.db, runner.ModeReview, owner, repoName, pr.Number, "pr", promptText)
	if err != nil {
		slog.Error("Failed to create runner session", "err", err)
		return
	}

	reporter := &runner.CommentReporter{
		RepoClient: repoClient, Owner: owner, Repo: repoName, Number: pr.Number,
	}

	initialPrompt := fmt.Sprintf(`Review this pull request.

**Title**: %s
**Description**: %s

**Diff**:
`+"```diff\n%s\n```"+`

Explore the repository to understand the context of the changes.
Read related files, check for test coverage, look for potential issues.
When you're done, call the "done" tool with your review as markdown.`, pr.Title, pr.Body, truncate(diff, 60000))

	r := runner.New(aiClient, ws, reporter, session, runner.Config{
		Mode:           runner.ModeReview,
		MaxTurns:       h.cfg.AgentValidationMaxRetries * 5, // ~15 turns for review
		SystemPrompt:   promptText,
		MaxTokens:      h.cfg.AgentMaxTokens,
		ShellAllowlist: defaultShellAllowlist(),
		ShellTimeout:   60,
	})

	result, err := r.Run(ctx, initialPrompt)
	if err != nil {
		slog.Error("Review runner failed", "err", err)
		h.recordError(0, "Review failed: "+err.Error())
		return
	}

	if result.Text != "" {
		comment := "## AI Code Review\n\n" + result.Text + "\n\n---\n*Automated review by AI Git Bot*"
		repoClient.PostReviewComment(ctx, owner, repoName, pr.Number, comment)
	}

	session.SetStatus("COMPLETED")
	slog.Info("Review completed", "pr", pr.Number, "turns", result.TurnCount)
}

func (h *WebhookHandler) runReviewFollowup(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, prNum int64, commentBody, promptText string) {
	// Try to load existing session
	session, err := runner.LoadSession(h.db, owner, repoName, "pr", prNum)
	if err != nil {
		// No existing session — create one
		session, err = runner.CreateSession(h.db, runner.ModeReview, owner, repoName, prNum, "pr", promptText)
		if err != nil {
			slog.Error("Failed to create session for follow-up", "err", err)
			return
		}
	}

	// For follow-ups, we add the comment as a new user message and re-run
	session.SaveMessage("user", "text", commentBody, "", "", "")

	// Simple text response (no workspace needed for follow-ups)
	messages := session.LoadMessages()
	response, err := aiClient.Chat(ctx, toSimpleMessages(messages), "", ai.ChatOpts{SystemPrompt: promptText})
	if err != nil {
		slog.Error("Follow-up chat failed", "err", err)
		return
	}

	session.SaveMessage("assistant", "text", response, "", "", "")

	comment := "## Bot Response\n\n" + response + "\n\n---\n*Response by AI Git Bot*"
	repoClient.PostComment(ctx, owner, repoName, prNum, comment)
}

func (h *WebhookHandler) runImplementation(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, event *webhook.Event, promptText string) {
	issue := event.Issue

	slog.Info("Starting implementation", "issue", issue.Number, "title", issue.Title)

	repoClient.PostComment(ctx, owner, repoName, issue.Number,
		"**AI Agent**: I've been assigned to this issue. Setting up workspace...")

	baseBranch, _ := repoClient.GetDefaultBranch(ctx, owner, repoName)
	if baseBranch == "" {
		baseBranch = "main"
	}

	cloneURL := buildCloneURL(h.db, owner, repoName, h.repoFactory)

	ws, err := runner.NewWorkspace(ctx, cloneURL, owner, repoName, baseBranch)
	if err != nil {
		slog.Error("Failed to create workspace", "err", err)
		repoClient.PostComment(ctx, owner, repoName, issue.Number,
			"**AI Agent**: Failed to set up workspace: "+err.Error())
		return
	}
	defer ws.Cleanup()

	session, err := runner.CreateSession(h.db, runner.ModeImplementation, owner, repoName, issue.Number, "issue", promptText)
	if err != nil {
		slog.Error("Failed to create runner session", "err", err)
		return
	}

	reporter := &runner.CommentReporter{
		RepoClient: repoClient, Owner: owner, Repo: repoName, Number: issue.Number,
		Verbose: true,
	}

	initialPrompt := fmt.Sprintf(`Implement the following issue in this repository.

**Title**: %s
**Description**: %s

Explore the repository structure, read relevant files, then implement the changes.
Write files using write_file, and validate your changes by running the build/test commands.
When you're done, call the "done" tool with a summary of your changes.`, issue.Title, issue.Body)

	r := runner.New(aiClient, ws, reporter, session, runner.Config{
		Mode:           runner.ModeImplementation,
		MaxTurns:       50,
		SystemPrompt:   promptText,
		MaxTokens:      h.cfg.AgentMaxTokens,
		ShellAllowlist: defaultShellAllowlist(),
		ShellTimeout:   300,
	})

	result, err := r.Run(ctx, initialPrompt)
	if err != nil {
		slog.Error("Implementation runner failed", "err", err)
		repoClient.PostComment(ctx, owner, repoName, issue.Number,
			"**AI Agent**: Implementation failed: "+err.Error())
		session.SetStatus("FAILED")
		return
	}

	// Collect changed files from workspace and commit them via repo API
	changedFiles, _ := ws.ChangedFiles()
	if len(changedFiles) == 0 {
		repoClient.PostComment(ctx, owner, repoName, issue.Number,
			"**AI Agent**: No files were changed. "+result.Text)
		session.SetStatus("COMPLETED")
		return
	}

	// Create branch and commit changes
	branchName := h.cfg.AgentBranchPrefix + fmt.Sprintf("issue-%d", issue.Number)
	session.SetBranch(branchName)

	if err := repoClient.CreateBranch(ctx, owner, repoName, branchName, baseBranch); err != nil {
		repoClient.PostComment(ctx, owner, repoName, issue.Number,
			"**AI Agent**: Failed to create branch: "+err.Error())
		session.SetStatus("FAILED")
		return
	}

	for _, path := range changedFiles {
		content, err := ws.ReadFile(path)
		if err != nil {
			continue
		}
		sha, _ := repoClient.GetFileSHA(ctx, owner, repoName, path, baseBranch)
		commitMsg := fmt.Sprintf("agent: update %s (issue #%d)", path, issue.Number)
		repoClient.CreateOrUpdateFile(ctx, owner, repoName, path, content, commitMsg, branchName, sha)
	}

	// Create PR
	prTitle := fmt.Sprintf("AI Agent: %s (fixes #%d)", issue.Title, issue.Number)
	prBody := fmt.Sprintf("Fixes #%d\n\n%s\n\n---\n*Generated by AI Agent*", issue.Number, result.Text)
	prNumber, err := repoClient.CreatePR(ctx, owner, repoName, prTitle, prBody, branchName, baseBranch)
	if err != nil {
		repoClient.PostComment(ctx, owner, repoName, issue.Number,
			"**AI Agent**: Failed to create PR: "+err.Error())
		session.SetStatus("FAILED")
		return
	}

	session.SetPR(prNumber)
	session.SetStatus("PR_CREATED")

	var fileList strings.Builder
	for _, f := range changedFiles {
		fmt.Fprintf(&fileList, "- `%s`\n", f)
	}

	repoClient.PostComment(ctx, owner, repoName, issue.Number,
		fmt.Sprintf("**AI Agent**: Implementation complete! Created #%d\n\n**Files changed** (%d):\n%s",
			prNumber, len(changedFiles), fileList.String()))

	slog.Info("Implementation completed", "issue", issue.Number, "pr", prNumber, "turns", result.TurnCount)
}

// --- Helpers ---

func (h *WebhookHandler) recordError(botID int64, msg string) {
	if botID > 0 {
		h.db.Exec("UPDATE bots SET last_error_message = ?, last_error_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?", msg, botID)
	}
}

func isBotUser(botUsername string, event *webhook.Event) bool {
	bot := strings.ToLower(botUsername)
	if strings.ToLower(event.Sender.Login) == bot {
		return true
	}
	if event.Comment != nil && strings.ToLower(event.Comment.User) == bot {
		return true
	}
	return false
}

func resolvePRNumber(event *webhook.Event) int64 {
	if event.Issue != nil && event.Issue.Number > 0 {
		return event.Issue.Number
	}
	if event.PullRequest != nil && event.PullRequest.Number > 0 {
		return event.PullRequest.Number
	}
	return 0
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n...(truncated)"
}

func toSimpleMessages(msgs []ai.ConversationMessage) []ai.Message {
	var result []ai.Message
	for _, m := range msgs {
		if m.Content != "" {
			result = append(result, ai.Message{Role: m.Role, Content: m.Content})
		}
	}
	return result
}

func buildCloneURL(db *sql.DB, owner, repoName string, factory *repo.ClientFactory) string {
	// Build a simple HTTPS clone URL — in practice this would use credentials
	// For now return a basic URL; the workspace clone handles auth via git credential helpers
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repoName)
}

func defaultShellAllowlist() []string {
	return []string{
		"git", "grep", "find", "cat", "head", "tail", "wc", "sort", "uniq", "diff", "ls", "tree",
		"mvn", "gradle", "go", "npm", "npx", "yarn", "pnpm",
		"cargo", "rustc", "make", "cmake",
		"python3", "pip", "pytest",
		"gcc", "g++",
		"ruby", "bundle", "rake",
		"tsc", "dotnet",
	}
}
