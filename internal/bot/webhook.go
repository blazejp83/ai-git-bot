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
	var maxTurnsReview, maxTurnsImpl sql.NullInt64

	err := h.db.QueryRow(`
		SELECT b.id, b.name, b.username, gi.provider_type, b.ai_integration_id, b.git_integration_id,
		       b.agent_enabled, b.prompt, b.max_turns_review, b.max_turns_implementation
		FROM bots b
		JOIN git_integrations gi ON b.git_integration_id = gi.id
		WHERE b.webhook_secret = ? AND b.enabled = 1
	`, secret).Scan(&botID, &botName, &botUsername, &providerType, &aiIntegrationID, &gitIntegrationID, &agentEnabled, &botPrompt, &maxTurnsReview, &maxTurnsImpl)
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

	bs := botSettings{
		id: botID, name: botName, username: botUsername,
		aiIntID: aiIntegrationID, gitIntID: gitIntegrationID,
		agentEnabled: agentEnabled,
	}
	// Resolve system prompt: if bot has custom prompt text, use it directly.
	// Otherwise fall back to loading from prompts/ directory.
	if botPrompt.Valid && botPrompt.String != "" {
		bs.systemPrompt = botPrompt.String
	} else {
		bs.systemPrompt = h.promptService.GetSystemPrompt("")
	}
	// Per-bot turn limit overrides (0 = use global defaults)
	if maxTurnsReview.Valid {
		bs.maxTurnsReview = int(maxTurnsReview.Int64)
	}
	if maxTurnsImpl.Valid {
		bs.maxTurnsImpl = int(maxTurnsImpl.Int64)
	}

	go h.dispatch(bs, event)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type botSettings struct {
	id              int64
	name            string
	username        string
	aiIntID         int64
	gitIntID        int64
	agentEnabled    bool
	systemPrompt    string // resolved prompt text (not a file name)
	maxTurnsReview  int    // 0 = use global default
	maxTurnsImpl    int    // 0 = use global default
}

func (bs botSettings) reviewTurns(globalDefault int) int {
	if bs.maxTurnsReview > 0 {
		return bs.maxTurnsReview
	}
	return globalDefault
}

func (bs botSettings) implTurns(globalDefault int) int {
	if bs.maxTurnsImpl > 0 {
		return bs.maxTurnsImpl
	}
	return globalDefault
}

func (h *WebhookHandler) dispatch(bs botSettings, event *webhook.Event) {
	ctx := context.Background()

	slog.Info("Dispatching webhook event",
		"bot", bs.name, "action", event.Action,
		"repo", event.Repo.FullName, "sender", event.Sender.Login)

	aiClient, err := h.aiFactory.GetClient(h.db, bs.aiIntID)
	if err != nil {
		slog.Error("Failed to get AI client", "err", err)
		h.recordError(bs.id, "Failed to get AI client: "+err.Error())
		return
	}

	repoClient, err := h.repoFactory.GetClient(h.db, bs.gitIntID)
	if err != nil {
		slog.Error("Failed to get repo client", "err", err)
		h.recordError(bs.id, "Failed to get repo client: "+err.Error())
		return
	}

	promptText := bs.systemPrompt

	botAlias := "@" + bs.username
	hasBotMention := event.Comment != nil && strings.Contains(event.Comment.Body, botAlias)

	owner := event.Repo.Owner
	repoName := event.Repo.Name

	switch event.Action {
	case "opened", "synchronized":
		if event.PullRequest != nil {
			h.runReview(ctx, aiClient, repoClient, owner, repoName, event, promptText, bs.reviewTurns(20))
		}

	case "created":
		if event.Comment != nil && hasBotMention {
			prNum := resolvePRNumber(event)
			if prNum > 0 {
				commenter := event.Comment.User
				if commenter == "" {
					commenter = event.Sender.Login
				}
				prAuthor := ""
				if event.PullRequest != nil {
					prAuthor = event.PullRequest.Author
				}
				h.runReviewFollowup(ctx, aiClient, repoClient, owner, repoName, prNum, event.Comment.Body, promptText, commenter, prAuthor)
			}
		}

	case "reviewed":
		if event.PullRequest != nil {
			// Future: inspect review comments for bot mentions
		}

	case "assigned":
		if event.Issue != nil && bs.agentEnabled {
			h.runImplementation(ctx, aiClient, repoClient, owner, repoName, event, promptText, bs.implTurns(50))
		}

	case "closed":
		if event.PullRequest != nil {
			// Clean up session
			slog.Info("PR closed", "pr", event.PullRequest.Number)
		}
	}
}

func (h *WebhookHandler) runReview(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, event *webhook.Event, promptText string, maxTurns int) {
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
		MaxTurns:       maxTurns,
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

func (h *WebhookHandler) runReviewFollowup(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, prNum int64, commentBody, promptText, commenter, prAuthor string) {
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

	// If session is new, seed it with PR context
	messages := session.LoadMessages()
	if len(messages) == 0 {
		diff, _ := repoClient.GetPRDiff(ctx, owner, repoName, prNum)
		if diff != "" {
			prContext := fmt.Sprintf("This is a pull request in %s/%s.\n\nDiff:\n```diff\n%s\n```", owner, repoName, truncate(diff, 60000))
			session.SaveMessage("user", "text", prContext, "", "", "")
			session.SaveMessage("assistant", "text", "I've reviewed the pull request context. How can I help you?", "", "", "")
		}
	}

	session.SaveMessage("user", "text", commentBody, "", "", "")

	messages = session.LoadMessages()
	response, err := aiClient.Chat(ctx, toSimpleMessages(messages), "", ai.ChatOpts{SystemPrompt: promptText})
	if err != nil {
		slog.Error("Follow-up chat failed", "err", err)
		return
	}

	session.SaveMessage("assistant", "text", response, "", "", "")

	// Mark response when a third-party (not the PR author) asked the question
	isThirdParty := prAuthor != "" && !strings.EqualFold(commenter, prAuthor)
	var comment string
	if isThirdParty {
		comment = fmt.Sprintf("## Bot Response\n\n*(Responding to @%s's question)*\n\n%s\n\n---\n*Response by AI Git Bot*", commenter, response)
	} else {
		comment = "## Bot Response\n\n" + response + "\n\n---\n*Response by AI Git Bot*"
	}
	repoClient.PostComment(ctx, owner, repoName, prNum, comment)
}

func (h *WebhookHandler) runImplementation(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, event *webhook.Event, promptText string, maxTurns int) {
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
		MaxTurns:       maxTurns,
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
