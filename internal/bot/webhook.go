package bot

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

// WebhookHandler handles incoming webhooks from all git platforms.
type WebhookHandler struct {
	db *sql.DB
}

func NewWebhookHandler(db *sql.DB) *WebhookHandler {
	return &WebhookHandler{db: db}
}

// Handle is the HTTP handler for POST /api/webhook/{secret}
func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	secret := chi.URLParam(r, "secret")

	// Look up bot by webhook secret
	var botID int64
	var botName, botUsername, providerType string
	var aiIntegrationID, gitIntegrationID int64
	var agentEnabled bool

	err := h.db.QueryRow(`
		SELECT b.id, b.name, b.username, gi.provider_type, b.ai_integration_id, b.git_integration_id, b.agent_enabled
		FROM bots b
		JOIN git_integrations gi ON b.git_integration_id = gi.id
		WHERE b.webhook_secret = ? AND b.enabled = 1
	`, secret).Scan(&botID, &botName, &botUsername, &providerType, &aiIntegrationID, &gitIntegrationID, &agentEnabled)
	if err != nil {
		slog.Debug("No bot found for webhook secret", "err", err)
		w.WriteHeader(http.StatusOK) // Don't reveal whether secret exists
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	// Parse webhook based on provider type
	var event *webhook.Event
	switch providerType {
	case "GITEA":
		event, err = webhook.ParseGitea(body)
	case "GITHUB":
		eventType := r.Header.Get("X-GitHub-Event")
		event, err = webhook.ParseGitHub(eventType, body)
	case "GITLAB":
		eventType := r.Header.Get("X-Gitlab-Event")
		event, err = webhook.ParseGitLab(eventType, body)
	case "BITBUCKET":
		eventKey := r.Header.Get("X-Event-Key")
		event, err = webhook.ParseBitbucket(eventKey, body)
	default:
		slog.Warn("Unknown provider type", "provider", providerType)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "unknown_provider"})
		return
	}

	if err != nil {
		slog.Error("Failed to parse webhook", "provider", providerType, "err", err)
		http.Error(w, "Failed to parse webhook", http.StatusBadRequest)
		return
	}

	// Update webhook count
	h.db.Exec("UPDATE bots SET webhook_call_count = webhook_call_count + 1, last_webhook_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?", botID)

	// Ignore events from the bot itself
	if isBotUser(botUsername, event) {
		slog.Debug("Ignoring event from bot user", "bot", botUsername)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored_self"})
		return
	}

	// Dispatch event asynchronously
	go h.dispatch(botID, botName, botUsername, aiIntegrationID, gitIntegrationID, agentEnabled, event)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *WebhookHandler) dispatch(botID int64, botName, botUsername string, aiIntID, gitIntID int64, agentEnabled bool, event *webhook.Event) {
	slog.Info("Dispatching webhook event",
		"bot", botName,
		"action", event.Action,
		"repo", event.Repo.FullName,
		"sender", event.Sender.Login)

	// TODO: Wire up to review and agent services in Phase 6/7
	// For now, just log the event
	switch event.Action {
	case "opened", "synchronized":
		if event.PullRequest != nil {
			slog.Info("PR event", "action", event.Action, "pr", event.PullRequest.Number)
		}
	case "created":
		if event.Comment != nil {
			slog.Info("Comment event", "body_len", len(event.Comment.Body))
		}
	case "reviewed":
		if event.Review != nil {
			slog.Info("Review event", "type", event.Review.Type)
		}
	case "assigned":
		if event.Issue != nil && agentEnabled {
			slog.Info("Issue assigned", "issue", event.Issue.Number, "assignee", event.Issue.Assignee)
		}
	case "closed":
		if event.PullRequest != nil {
			slog.Info("PR closed", "pr", event.PullRequest.Number, "merged", event.PullRequest.Merged)
		}
	}
}

func isBotUser(botUsername string, event *webhook.Event) bool {
	sender := strings.ToLower(event.Sender.Login)
	bot := strings.ToLower(botUsername)
	if sender == bot {
		return true
	}
	if event.Comment != nil && strings.ToLower(event.Comment.User) == bot {
		return true
	}
	return false
}
