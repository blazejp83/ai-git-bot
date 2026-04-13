package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tmseidel/ai-git-bot/internal/agent"
	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/config"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
	"github.com/tmseidel/ai-git-bot/internal/prompt"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/review"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

// WebhookHandler handles incoming webhooks from all git platforms.
type WebhookHandler struct {
	db             *sql.DB
	cfg            *config.Config
	aiFactory      *ai.ClientFactory
	repoFactory    *repo.ClientFactory
	promptService  *prompt.Service
	sessions       *review.SessionService
	agentSessions  *agent.SessionService
}

func NewWebhookHandler(db *sql.DB, enc *encrypt.Service, promptSvc *prompt.Service, cfg *config.Config) *WebhookHandler {
	return &WebhookHandler{
		db:            db,
		cfg:           cfg,
		aiFactory:     ai.NewClientFactory(enc),
		repoFactory:   repo.NewClientFactory(enc),
		promptService: promptSvc,
		sessions:      review.NewSessionService(db),
		agentSessions: agent.NewSessionService(db),
	}
}

// Handle is the HTTP handler for POST /api/webhook/{secret}
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
		slog.Debug("No bot found for webhook secret", "err", err)
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
	reviewSvc := review.NewService(repoClient, aiClient, h.sessions, botUsername, promptText)

	botAlias := "@" + botUsername
	hasBotMention := event.Comment != nil && strings.Contains(event.Comment.Body, botAlias)

	switch event.Action {
	case "opened", "synchronized":
		if event.PullRequest != nil {
			reviewSvc.ReviewPullRequest(ctx, event)
		}
	case "created":
		if event.Comment != nil {
			if event.Comment.Path != "" && hasBotMention {
				reviewSvc.HandleInlineComment(ctx, event)
			} else if hasBotMention {
				reviewSvc.HandleBotCommand(ctx, event)
			}
		}
	case "reviewed":
		if event.Review != nil {
			reviewSvc.HandleReviewSubmitted(ctx, event)
		}
	case "closed":
		if event.PullRequest != nil {
			reviewSvc.HandlePRClosed(ctx, event)
		}
	case "assigned":
		if event.Issue != nil && agentEnabled {
			agentPrompt := h.promptService.GetSystemPrompt("agent")
			agentCfg := agent.AgentConfig{
				MaxFiles:            h.cfg.AgentMaxFiles,
				MaxTokens:           h.cfg.AgentMaxTokens,
				BranchPrefix:        h.cfg.AgentBranchPrefix,
				MaxFileContentChars: h.cfg.AgentMaxFileContentChar,
				MaxTreeFiles:        500,
				ValidationEnabled:   h.cfg.AgentValidationEnabled,
				MaxRetries:          h.cfg.AgentValidationMaxRetries,
			}
			agentSvc := agent.NewService(repoClient, aiClient, h.agentSessions, agentCfg, agentPrompt)
			agentSvc.HandleIssueAssigned(ctx, event)
		}
		if event.Issue != nil && !agentEnabled {
			// Check if this is a follow-up comment on an existing agent session
		}
	}
}

func (h *WebhookHandler) recordError(botID int64, msg string) {
	h.db.Exec("UPDATE bots SET last_error_message = ?, last_error_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?", msg, botID)
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
