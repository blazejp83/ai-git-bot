package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/config"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
	"github.com/tmseidel/ai-git-bot/internal/prompt"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/structreview"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
	"github.com/tmseidel/ai-git-bot/internal/workflow"
)

type WebhookHandler struct {
	db            *sql.DB
	cfg           *config.Config
	enc           *encrypt.Service
	aiFactory     *ai.ClientFactory
	repoFactory   *repo.ClientFactory
	promptService *prompt.Service
	debouncer     *reviewDebouncer
	orchMu        sync.Mutex
	orch          *workflow.Orchestrator
	// cloneURLOverride replaces the integration's clone URL (tests only).
	cloneURLOverride func(owner, repoName string) string
}

func NewWebhookHandler(db *sql.DB, enc *encrypt.Service, promptSvc *prompt.Service, cfg *config.Config) *WebhookHandler {
	return &WebhookHandler{
		db:            db,
		cfg:           cfg,
		enc:           enc,
		aiFactory:     ai.NewClientFactory(enc),
		repoFactory:   repo.NewClientFactory(enc),
		promptService: promptSvc,
		debouncer:     newReviewDebouncer(),
	}
}

// SetUsageRecorder records the token usage and errors of every AI call the bots make.
func (h *WebhookHandler) SetUsageRecorder(r ai.Recorder) {
	h.aiFactory.SetRecorder(r)
}

func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	secret := chi.URLParam(r, "secret")

	var botID int64
	var botName, botUsername, providerType string
	var aiIntegrationID, gitIntegrationID int64
	var agentEnabled bool
	var botPrompt, shellAllowlist, prTargets sql.NullString
	var structured bool
	var issueWorkflow string
	var workflowsJSON sql.NullString
	var maxBlockers, maxMedium, maxLow sql.NullInt64
	var maxTurnsReview, maxTurnsImpl sql.NullInt64

	err := h.db.QueryRow(`
		SELECT b.id, b.name, b.username, gi.provider_type, b.ai_integration_id, b.git_integration_id,
		       b.agent_enabled, b.prompt, b.max_turns_review, b.max_turns_implementation, b.shell_allowlist, b.pr_target_branches,
		       b.structured_review, b.review_max_blockers, b.review_max_medium, b.review_max_low, b.issue_workflow, b.workflows
		FROM bots b
		JOIN git_integrations gi ON b.git_integration_id = gi.id
		WHERE b.webhook_secret = ? AND b.enabled = 1
	`, secret).Scan(&botID, &botName, &botUsername, &providerType, &aiIntegrationID, &gitIntegrationID, &agentEnabled, &botPrompt, &maxTurnsReview, &maxTurnsImpl, &shellAllowlist, &prTargets,
		&structured, &maxBlockers, &maxMedium, &maxLow, &issueWorkflow, &workflowsJSON)
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

	// Event types the bot does not act on (pushes, pipelines, ...) stop here, so
	// they never cost an account lookup or record an error.
	if !handledActions[event.Action] {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored_event"})
		return
	}

	// Closing a pull request ends its debounce window, also when the bot closed it.
	if event.Action == "closed" && event.PullRequest != nil {
		h.debouncer.forget(debounceKey(botID, event.Repo.FullName, event.PullRequest.Number))
	}

	repoClient, err := h.repoFactory.GetClient(h.db, gitIntegrationID)
	if err != nil {
		slog.Error("Failed to get repo client", "err", err)
		h.recordError(botID, "Failed to get repo client: "+err.Error())
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}
	identity, err := resolveBotIdentity(r.Context(), repoClient, botUsername)
	if err != nil {
		// Without knowing who the bot is it could answer itself, so ignore the event.
		slog.Error("Ignoring webhook: bot account unknown", "bot", botName, "err", err)
		h.recordError(botID, err.Error())
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored_unresolved_bot"})
		return
	}
	if identity.fromBot(event) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored_self"})
		return
	}

	bs := botSettings{
		id: botID, name: botName, username: botUsername, identity: identity,
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
	if shellAllowlist.Valid && shellAllowlist.String != "" {
		bs.shellAllowlist = parseAllowlist(shellAllowlist.String)
	}
	if prTargets.Valid {
		bs.prTargetBranches = parseAllowlist(prTargets.String)
	}
	bs.structuredReview = structured
	bs.issueWorkflow = issueWorkflow
	bs.workflows = workflow.Parse(workflowsJSON.String)
	bs.thresholds = structreview.Thresholds{
		MaxBlockers: intPtr(maxBlockers), MaxMedium: intPtr(maxMedium), MaxLow: intPtr(maxLow),
	}

	go h.dispatch(bs, event)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handledActions are the event actions dispatch acts on.
var handledActions = map[string]bool{
	"opened": true, "synchronized": true, "reopened": true, "review_requested": true,
	"created": true, "reviewed": true, "assigned": true, "closed": true,
}

type botSettings struct {
	id             int64
	name           string
	username       string
	identity       botIdentity
	aiIntID        int64
	gitIntID       int64
	agentEnabled   bool
	systemPrompt   string   // resolved prompt text (not a file name)
	maxTurnsReview int      // 0 = use global default
	maxTurnsImpl   int      // 0 = use global default
	shellAllowlist []string // nil = use global default
	// structuredReview records findings through tools and decides formally
	// (see internal/structreview); off means the free-text review.
	structuredReview bool
	thresholds       structreview.Thresholds
	// issueWorkflow is what the bot does when assigned to an issue:
	// "implement", "writer" or "none".
	issueWorkflow string
	// workflows are the PR workflows and their settings.
	workflows workflow.Settings
	// prTargetBranches limits automatic reviews to PRs into these base
	// branches (glob patterns); nil = all branches.
	prTargetBranches []string
}

// reviewsTargetBranch reports whether PRs into base are reviewed automatically.
func (bs botSettings) reviewsTargetBranch(base string) bool {
	if len(bs.prTargetBranches) == 0 {
		return true
	}
	for _, pattern := range bs.prTargetBranches {
		if ok, err := path.Match(pattern, base); err == nil && ok {
			return true
		}
	}
	return false
}

func (bs botSettings) resolveShellAllowlist() []string {
	if len(bs.shellAllowlist) > 0 {
		return bs.shellAllowlist
	}
	return defaultShellAllowlist()
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
	// Tag every AI call of this event for the usage log.
	ctx := ai.WithBot(context.Background(), bs.name)
	if num := resolvePRNumber(event); num > 0 {
		ctx = ai.WithSession(ctx, fmt.Sprintf("%s#%d", event.Repo.FullName, num))
	}

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

	hasBotMention := event.Comment != nil && bs.identity.mentioned(event.Comment.Body)

	owner := event.Repo.Owner
	repoName := event.Repo.Name
	allowlist := bs.resolveShellAllowlist()

	switch event.Action {
	case "opened", "synchronized", "reopened":
		if event.PullRequest != nil {
			if base := event.PullRequest.Base.RefName; !bs.reviewsTargetBranch(base) {
				slog.Info("PR target branch not configured for automatic review; skipping",
					"pr", event.PullRequest.Number, "base", base, "allowed", strings.Join(bs.prTargetBranches, ", "))
				return
			}
			// The enabled workflows, in order.
			if bs.workflows.Enabled(workflow.Review) {
				h.reviewOnce(ctx, bs, event, aiClient, repoClient, promptText, allowlist)
			}
			if bs.workflows.Enabled(workflow.ReadmeSync) && bs.workflows.Param(workflow.ReadmeSync, "trigger") == "auto" {
				h.runReadmeSync(ctx, aiClient, repoClient, owner, repoName, event.PullRequest.Number, bs, "", true)
			}
		}

	case "review_requested":
		// Asking the bot for a review is a deliberate request: no branch filter,
		// but debounced, since opening a PR with the bot as reviewer also fires "opened".
		if event.PullRequest != nil && event.RequestedReviewer != nil && bs.identity.is(*event.RequestedReviewer) {
			h.reviewOnce(ctx, bs, event, aiClient, repoClient, promptText, allowlist)
		}

	case "created":
		// A reply to the writer's questions continues it, no mention needed.
		if event.Comment != nil && h.routeToWriter(ctx, aiClient, repoClient, owner, repoName, event, hasBotMention, promptText, bs) {
			return
		}
		// Workflow commands other than review, e.g. #regenerate-readme.
		if event.Comment != nil && hasBotMention {
			if key, instruction, ok := workflow.ParseCommand(event.Comment.Body); ok && key == workflow.ReadmeSync {
				if prNum := resolvePRNumber(event); prNum > 0 && (event.PullRequest != nil || (event.Issue != nil && event.Issue.IsPR)) {
					repoClient.AddReaction(ctx, owner, repoName, event.Comment.ID, "eyes")
					h.runReadmeSync(ctx, aiClient, repoClient, owner, repoName, prNum, bs, instruction, false)
					return
				}
			}
		}
		if event.Comment != nil && hasBotMention && h.routeToImplementation(ctx, aiClient, repoClient, owner, repoName, event, promptText, bs, allowlist) {
			return
		}
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

				body := event.Comment.Body
				trigger := extractTrigger(body)
				if trigger == "" && isReviewAgainRequest(bs.identity.stripMentions(body)) {
					trigger = "review"
				}

				switch trigger {
				case "review", "fullreview":
					repoClient.AddReaction(ctx, owner, repoName, event.Comment.ID, "eyes")
					rctx, done := h.orchestrator().Start(ctx, runKey(bs.id, owner, repoName, prNum, workflow.Review))
					h.runReview(rctx, aiClient, repoClient, owner, repoName, prNum, event.PullRequest, promptText, bs, allowlist)
					done()
				case "investigate", "explore", "deep":
					repoClient.AddReaction(ctx, owner, repoName, event.Comment.ID, "eyes")
					h.runAgenticFollowup(ctx, aiClient, repoClient, owner, repoName, prNum, body, promptText, bs.reviewTurns(20), allowlist, bs.gitIntID, commenter, prAuthor)
				default:
					h.runReviewFollowup(ctx, aiClient, repoClient, owner, repoName, prNum, body, promptText, commenter, prAuthor)
				}
			}
		}

	case "reviewed":
		if event.PullRequest != nil {
			// Future: inspect review comments for bot mentions
		}

	case "assigned":
		if event.Issue == nil {
			return
		}
		if !bs.identity.assignedTo(event.Issue) {
			slog.Info("Issue assigned to someone else; ignoring",
				"issue", event.Issue.Number, "assignees", strings.Join(event.Issue.Assignees, ", "))
			return
		}
		switch bs.issueWorkflow {
		case workflowWriter:
			h.startWriter(ctx, aiClient, repoClient, owner, repoName, event.Issue, promptText, bs)
		case workflowNone:
			slog.Info("Bot does nothing on issue assignment", "issue", event.Issue.Number)
		default:
			if !bs.agentEnabled {
				return
			}
			if reason := h.implementationBusy(owner, repoName, event.Issue.Number); reason != "" {
				slog.Info("Not starting the agent again", "issue", event.Issue.Number, "reason", reason)
				return
			}
			h.runImplementation(ctx, aiClient, repoClient, owner, repoName, event.Issue, promptText, bs, allowlist)
		}

	case "closed":
		if event.PullRequest != nil {
			// Clean up session
			slog.Info("PR closed", "pr", event.PullRequest.Number)
		}
	}
}

// reviewOnce runs an automatic review unless one was already triggered for the
// same head commit within the debounce window; a skipped or failed review
// gives its claim back.
func (h *WebhookHandler) reviewOnce(ctx context.Context, bs botSettings, event *webhook.Event, aiClient ai.Client, repoClient repo.Client, promptText string, allowlist []string) {
	pr := event.PullRequest
	ctx, done := h.orchestrator().Start(ctx, runKey(bs.id, event.Repo.Owner, event.Repo.Name, pr.Number, workflow.Review))
	defer done()
	key := debounceKey(bs.id, event.Repo.FullName, pr.Number)
	if !h.debouncer.tryClaim(key, pr.Head.SHA) {
		slog.Info("Review already triggered for this commit; skipping", "pr", pr.Number, "sha", pr.Head.SHA, "action", event.Action)
		return
	}
	if !h.runReview(ctx, aiClient, repoClient, event.Repo.Owner, event.Repo.Name, pr.Number, pr, promptText, bs, allowlist) {
		h.debouncer.release(key, pr.Head.SHA)
	}
}

// runReview reviews a pull request and reports whether a review ran (false when
// it was skipped or failed, so the debounce claim can be given back).
// pr may be nil (e.g. a review requested from a GitHub issue comment); the
// prompt then has no title or description, and the base branch is the default branch.
func (h *WebhookHandler) runReview(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, prNum int64, pr *webhook.PullRequest, promptText string, bs botSettings, shellAllowlist []string) bool {
	if bs.structuredReview {
		return h.runStructuredReview(ctx, aiClient, repoClient, owner, repoName, prNum, pr, promptText, bs, shellAllowlist)
	}
	maxTurns, gitIntID := bs.reviewTurns(20), bs.gitIntID
	title, body, baseBranch := "", "", ""
	if pr != nil {
		title, body, baseBranch = pr.Title, pr.Body, pr.Base.RefName
	}

	// Fetch diff for the initial prompt
	diff, err := repoClient.GetPRDiff(ctx, owner, repoName, prNum)
	if err != nil || diff == "" {
		slog.Warn("No diff found for PR", "pr", prNum, "err", err)
		return false
	}

	ws, reviewedSHA, onHead, err := h.prepareReviewWorkspace(ctx, repoClient, gitIntID, owner, repoName, prNum, baseBranch)
	if err != nil {
		slog.Error("Failed to create workspace for review", "err", err)
		return false
	}
	defer ws.Cleanup()

	session, err := runner.CreateSession(h.db, runner.ModeReview, owner, repoName, prNum, "pr", promptText)
	if err != nil {
		slog.Error("Failed to create runner session", "err", err)
		return false
	}

	reporter := &runner.CommentReporter{
		RepoClient: repoClient, Owner: owner, Repo: repoName, Number: prNum,
	}

	initialPrompt := fmt.Sprintf(`Review this pull request.

**Title**: %s
**Description**: %s

**Diff**:
`+"```diff\n%s\n```"+`

%s
Explore the repository to understand the context of the changes.
Read related files, check for test coverage, look for potential issues.
To see the diff hunks of changed files with line numbers, call the "pr_diff" tool with up to 8 file paths.
When you're done, call the "done" tool with your review as markdown.`, title, body, truncateDiff(diff, 60000), workspaceNote(onHead))

	r := runner.New(aiClient, ws, reporter, session, runner.Config{
		Mode:           runner.ModeReview,
		MaxTurns:       maxTurns,
		SystemPrompt:   promptText,
		MaxTokens:      h.cfg.AgentMaxTokens,
		ShellAllowlist: shellAllowlist,
		ShellTimeout:   60,
		PRDiff:         diff,
	})

	result, err := r.Run(ctx, initialPrompt)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Info("Review superseded by a newer run", "pr", prNum)
			session.SetStatus("CANCELLED")
			return false
		}
		slog.Error("Review runner failed", "err", err)
		h.recordError(0, "Review failed: "+err.Error())
		session.SetStatus("FAILED")
		return false
	}

	// Don't publish a review of a commit the pull request no longer points at:
	// the push that replaced it triggers its own review.
	if headMoved(ctx, repoClient, owner, repoName, prNum, reviewedSHA) {
		session.SetStatus("STALE")
		return true
	}

	if result.Text != "" {
		comment := "## AI Code Review\n\n" + result.Text + "\n\n---\n*Automated review by AI Git Bot*"
		repoClient.PostReviewComment(ctx, owner, repoName, prNum, comment)
	}

	session.SetStatus("COMPLETED")
	slog.Info("Review completed", "pr", prNum, "turns", result.TurnCount)
	return true
}

// prepareReviewWorkspace clones the target repository and checks out the pull
// request's head, which also works for pull requests from forks: through the
// provider's PR ref (GitHub, Gitea, GitLab) or the source repository's branch
// (Bitbucket). If the head cannot be checked out, the workspace stays on the
// base branch and onHead is false. reviewedSHA is the commit under review.
func (h *WebhookHandler) prepareReviewWorkspace(ctx context.Context, repoClient repo.Client, gitIntID int64, owner, repoName string, prNum int64, baseBranch string) (ws *runner.Workspace, reviewedSHA string, onHead bool, err error) {
	head, headErr := repoClient.GetPRHead(ctx, owner, repoName, prNum)
	if headErr != nil {
		slog.Warn("Cannot read PR head", "pr", prNum, "err", headErr)
	}
	if baseBranch == "" {
		baseBranch, _ = repoClient.GetDefaultBranch(ctx, owner, repoName)
	}
	if baseBranch == "" {
		baseBranch = "main"
	}

	ws, err = runner.NewWorkspace(ctx, h.cloneURL(gitIntID, owner, repoName), owner, repoName, baseBranch)
	if err != nil {
		return nil, "", false, err
	}
	reviewedSHA = head.SHA
	if headErr != nil {
		return ws, reviewedSHA, false, nil
	}

	remote, ref := "origin", head.Ref
	if ref == "" && head.Branch != "" {
		ref = "refs/heads/" + head.Branch
		if head.SourceRepo != "" && !strings.EqualFold(head.SourceRepo, owner+"/"+repoName) {
			if src := strings.SplitN(head.SourceRepo, "/", 2); len(src) == 2 {
				remote = h.cloneURL(gitIntID, src[0], src[1])
			}
		}
	}
	if ref == "" {
		return ws, reviewedSHA, false, nil
	}
	sha, err := ws.CheckoutRef(ctx, remote, ref)
	if err != nil {
		slog.Warn("Cannot check out PR head; reviewing with the base branch checked out", "pr", prNum, "err", err)
		return ws, reviewedSHA, false, nil
	}
	return ws, sha, true, nil
}

// orchestrator returns the handler's workflow orchestrator, creating it on
// first use.
func (h *WebhookHandler) orchestrator() *workflow.Orchestrator {
	h.orchMu.Lock()
	defer h.orchMu.Unlock()
	if h.orch == nil {
		h.orch = workflow.NewOrchestrator()
	}
	return h.orch
}

// runKey identifies a workflow run for cancellation: a newer run with the same
// key cancels the older one.
func runKey(botID int64, owner, repoName string, prNum int64, workflowKey string) string {
	return fmt.Sprintf("%d|%s/%s|%d|%s", botID, owner, repoName, prNum, workflowKey)
}

// headMoved reports whether the PR's live head is no longer the reviewed commit.
func headMoved(ctx context.Context, repoClient repo.Client, owner, repoName string, prNum int64, reviewedSHA string) bool {
	live, err := repoClient.GetPRHead(ctx, owner, repoName, prNum)
	if err == nil && live.SHA != "" && reviewedSHA != "" && live.SHA != reviewedSHA {
		slog.Info("PR head moved during review; not publishing", "pr", prNum, "reviewed", reviewedSHA, "live", live.SHA)
		return true
	}
	return false
}

func intPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func workspaceNote(onHead bool) string {
	if onHead {
		return "The workspace has the pull request's head commit checked out."
	}
	return "Note: the pull request's head could not be checked out, so the workspace contains the BASE branch without these changes. Rely on the diff (and the pr_diff tool) for the changed code."
}

// truncateDiff shortens a diff for the initial prompt, pointing the model at
// the pr_diff tool for the rest.
func truncateDiff(diff string, maxLen int) string {
	if len(diff) <= maxLen {
		return diff
	}
	return diff[:maxLen] + "\n...(diff truncated; call pr_diff with the remaining file paths to see them)"
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

// routeToImplementation hands a comment mentioning the bot to the
// implementation agent when it asks for more work on an issue the agent
// already pushed to, or on a pull request the agent opened. Review commands
// (#review, re-review, #investigate) keep their meaning. It reports whether
// the comment was handled.
func (h *WebhookHandler) routeToImplementation(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, event *webhook.Event, promptText string, bs botSettings, allowlist []string) bool {
	if !bs.agentEnabled {
		return false
	}
	prev, ok := h.implementationTarget(owner, repoName, event, bs)
	if !ok {
		return false
	}
	issueNum := prev.Issue

	commenter := event.Comment.User
	if commenter == "" {
		commenter = event.Sender.Login
	}
	if status, updatedAt, ok := runner.LatestStatus(h.db, runner.ModeImplementation, owner, repoName, "issue", issueNum); ok &&
		status == "IN_PROGRESS" && time.Since(updatedAt) < implementationStaleAfter {
		repoClient.PostComment(ctx, owner, repoName, resolvePRNumber(event),
			fmt.Sprintf("**AI Agent**: @%s I'm still working on the previous request for #%d. Mention me again when that is done.", commenter, issueNum))
		return true
	}

	repoClient.AddReaction(ctx, owner, repoName, event.Comment.ID, "eyes")
	h.continueImplementation(ctx, aiClient, repoClient, owner, repoName, prev, event.Comment, commenter, promptText, bs, allowlist)
	return true
}

// implementationTarget finds the earlier implementation run a comment asks
// to continue: the issue commented on, or the issue behind a pull request the
// agent opened. Review commands never continue an implementation.
func (h *WebhookHandler) implementationTarget(owner, repoName string, event *webhook.Event, bs botSettings) (runner.Implementation, bool) {
	body := event.Comment.Body
	if extractTrigger(body) != "" || isReviewAgainRequest(bs.identity.stripMentions(body)) {
		return runner.Implementation{}, false
	}
	var issueNum int64
	if event.Issue != nil && !event.Issue.IsPR && event.PullRequest == nil {
		issueNum = event.Issue.Number
	} else {
		n, ok := runner.ImplementationForPR(h.db, owner, repoName, resolvePRNumber(event))
		if !ok {
			return runner.Implementation{}, false
		}
		issueNum = n
	}
	return runner.LatestImplementation(h.db, owner, repoName, issueNum)
}

// implementationStaleAfter is how long an IN_PROGRESS implementation session
// blocks a new run; older ones are treated as abandoned (e.g. a restart mid-run).
const implementationStaleAfter = 2 * time.Hour

// implementationBusy explains why a new implementation run must not start for
// an issue, or returns "". Gitea re-sends "assigned" whenever another assignee
// is added, so without this one issue would get several agents.
func (h *WebhookHandler) implementationBusy(owner, repoName string, issueNum int64) string {
	status, updatedAt, ok := runner.LatestStatus(h.db, runner.ModeImplementation, owner, repoName, "issue", issueNum)
	if !ok {
		return ""
	}
	switch {
	case status == "IN_PROGRESS" && time.Since(updatedAt) < implementationStaleAfter:
		return "an implementation is already running"
	case status == "PR_CREATED":
		return "an implementation already opened a pull request"
	}
	return ""
}

// runAgenticFollowup runs a full agentic session to investigate a specific question on a PR.
func (h *WebhookHandler) runAgenticFollowup(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, prNum int64, commentBody, promptText string, maxTurns int, shellAllowlist []string, gitIntID int64, commenter, prAuthor string) {
	diff, _ := repoClient.GetPRDiff(ctx, owner, repoName, prNum)

	ws, _, onHead, err := h.prepareReviewWorkspace(ctx, repoClient, gitIntID, owner, repoName, prNum, "")
	if err != nil {
		slog.Error("Failed to create workspace for agentic follow-up", "err", err)
		// Fall back to simple chat
		h.runReviewFollowup(ctx, aiClient, repoClient, owner, repoName, prNum, commentBody, promptText, commenter, prAuthor)
		return
	}
	defer ws.Cleanup()

	session, err := runner.CreateSession(h.db, runner.ModeReview, owner, repoName, prNum, "pr", promptText)
	if err != nil {
		slog.Error("Failed to create runner session", "err", err)
		return
	}

	reporter := &runner.CommentReporter{
		RepoClient: repoClient, Owner: owner, Repo: repoName, Number: prNum,
	}

	// Strip the trigger word from the comment for the prompt
	cleanBody := stripTrigger(commentBody)

	initialPrompt := fmt.Sprintf(`A user asked the following question about a pull request in %s/%s:

"%s"

`, owner, repoName, cleanBody)

	if diff != "" {
		initialPrompt += fmt.Sprintf("Here is the PR diff:\n```diff\n%s\n```\n\n", truncateDiff(diff, 60000))
	}
	initialPrompt += workspaceNote(onHead) + "\n"

	initialPrompt += `Explore the repository to understand the context. Read related files, search for symbols, check tests.
When you have a thorough answer, call the "done" tool with your response.`

	r := runner.New(aiClient, ws, reporter, session, runner.Config{
		Mode:           runner.ModeReview,
		MaxTurns:       maxTurns,
		SystemPrompt:   promptText,
		MaxTokens:      h.cfg.AgentMaxTokens,
		ShellAllowlist: shellAllowlist,
		ShellTimeout:   60,
		PRDiff:         diff,
	})

	result, err := r.Run(ctx, initialPrompt)
	if err != nil {
		slog.Error("Agentic follow-up failed", "err", err)
		return
	}

	if result.Text != "" {
		isThirdParty := prAuthor != "" && !strings.EqualFold(commenter, prAuthor)
		var comment string
		if isThirdParty {
			comment = fmt.Sprintf("## Bot Response\n\n*(Responding to @%s's question — deep investigation)*\n\n%s\n\n---\n*Response by AI Git Bot*", commenter, result.Text)
		} else {
			comment = "## Bot Response (deep investigation)\n\n" + result.Text + "\n\n---\n*Response by AI Git Bot*"
		}
		repoClient.PostComment(ctx, owner, repoName, prNum, comment)
	}

	session.SetStatus("COMPLETED")
}

// --- Helpers ---

// extractTrigger finds a #command in the comment body (e.g. #review, #investigate).
func extractTrigger(body string) string {
	lower := strings.ToLower(body)
	triggers := []string{"#fullreview", "#review", "#investigate", "#explore", "#deep"}
	for _, t := range triggers {
		if strings.Contains(lower, t) {
			return strings.TrimPrefix(t, "#")
		}
	}
	return ""
}

// stripTrigger removes trigger words from the comment body.
func stripTrigger(body string) string {
	triggers := []string{"#fullreview", "#review", "#investigate", "#explore", "#deep"}
	result := body
	for _, t := range triggers {
		result = strings.ReplaceAll(result, t, "")
		result = strings.ReplaceAll(result, strings.ToUpper(t), "")
	}
	return strings.TrimSpace(result)
}

func (h *WebhookHandler) recordError(botID int64, msg string) {
	if botID > 0 {
		h.db.Exec("UPDATE bots SET last_error_message = ?, last_error_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?", msg, botID)
	}
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

func (h *WebhookHandler) cloneURL(gitIntID int64, owner, repoName string) string {
	if h.cloneURLOverride != nil {
		return h.cloneURLOverride(owner, repoName)
	}
	db := h.db
	enc := h.enc
	var providerType, baseURL, token string
	var username sql.NullString
	err := db.QueryRow("SELECT provider_type, url, token, username FROM git_integrations WHERE id = ?", gitIntID).
		Scan(&providerType, &baseURL, &token, &username)
	if err != nil {
		slog.Error("Failed to load git integration for clone URL", "err", err)
		return fmt.Sprintf("https://github.com/%s/%s.git", owner, repoName)
	}

	decrypted, _ := enc.Decrypt(token)

	// Strip protocol
	protocol := "https"
	if strings.HasPrefix(baseURL, "http://") {
		protocol = "http"
	}
	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	// Remove /api/v1, /api/v4, /2.0 suffixes
	for _, suffix := range []string{"/api/v1", "/api/v4", "/api/v3", "/2.0"} {
		host = strings.TrimSuffix(host, suffix)
	}

	// Build authenticated clone URL
	if username.Valid && username.String != "" {
		// Bitbucket style: username:token@host
		return fmt.Sprintf("%s://%s:%s@%s/%s/%s.git", protocol, username.String, decrypted, host, owner, repoName)
	}
	// oauth2:token@host works for Gitea, GitHub, GitLab
	return fmt.Sprintf("%s://oauth2:%s@%s/%s/%s.git", protocol, decrypted, host, owner, repoName)
}

func parseAllowlist(s string) []string {
	var result []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
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
