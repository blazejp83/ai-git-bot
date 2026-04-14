package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/auth"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

type OAuthHandlers struct {
	db       *sql.DB
	enc      *encrypt.Service
	tpl      *Templates
	mu       sync.Mutex
	pending  map[int64]*deviceCodeSession // integration ID → pending session
}

type deviceCodeSession struct {
	DeviceCode *auth.DeviceCodeResponse
	Cancel     context.CancelFunc
	Done       chan oauthResult
}

type oauthResult struct {
	Success bool
	Email   string
	Error   string
}

func NewOAuthHandlers(db *sql.DB, enc *encrypt.Service, tpl *Templates) *OAuthHandlers {
	return &OAuthHandlers{db: db, enc: enc, tpl: tpl, pending: make(map[int64]*deviceCodeSession)}
}

// StartOAuth initiates the device code flow for an AI integration.
// GET /ai-integrations/{id}/oauth
func (h *OAuthHandlers) StartOAuth(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid integration ID", http.StatusBadRequest)
		return
	}

	var name string
	err = h.db.QueryRow("SELECT name FROM ai_integrations WHERE id = ?", id).Scan(&name)
	if err != nil {
		http.Redirect(w, r, "/ai-integrations", http.StatusSeeOther)
		return
	}

	cfg := auth.DefaultOAuthConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)

	// Request device code
	dcr, err := auth.RequestDeviceCode(ctx, cfg)
	if err != nil {
		cancel()
		slog.Error("Failed to request device code", "err", err)
		http.Redirect(w, r, fmt.Sprintf("/ai-integrations/%d/edit?error=oauth_start_failed", id), http.StatusSeeOther)
		return
	}

	slog.Info("Device code flow started", "integration", name, "user_code", dcr.UserCode, "verification_url", dcr.VerificationURL)

	// Store pending session
	done := make(chan oauthResult, 1)
	h.mu.Lock()
	// Cancel any existing session for this integration
	if existing, ok := h.pending[id]; ok {
		existing.Cancel()
	}
	h.pending[id] = &deviceCodeSession{DeviceCode: dcr, Cancel: cancel, Done: done}
	h.mu.Unlock()

	// Poll for tokens in background
	go func() {
		defer cancel()
		tokens, err := auth.PollDeviceCode(ctx, cfg, dcr, 15*time.Minute)
		if err != nil {
			slog.Error("Device code polling failed", "err", err, "integration_id", id)
			done <- oauthResult{Error: err.Error()}
			return
		}

		slog.Info("Initial tokens received, exchanging id_token for API key")

		// Exchange id_token for an OpenAI API key — this is how Codex CLI
		// converts ChatGPT OAuth tokens into usable API credentials.
		if tokens.IDToken != "" {
			apiKey, err := auth.ExchangeForAPIKey(ctx, cfg, tokens.IDToken)
			if err != nil {
				slog.Warn("API key exchange failed, using original access_token", "err", err)
			} else {
				slog.Info("API key obtained via token exchange")
				tokens.AccessToken = apiKey
			}
		}

		if err := h.storeTokens(id, tokens); err != nil {
			slog.Error("Failed to store OAuth tokens", "err", err)
			done <- oauthResult{Error: "Failed to store tokens: " + err.Error()}
			return
		}

		email := ""
		if tokens.IDToken != "" {
			if claims, err := auth.ParseIDToken(tokens.IDToken); err == nil {
				email = claims.Email
			}
		}
		slog.Info("OAuth tokens stored via device code", "integration_id", id, "email", email)
		done <- oauthResult{Success: true, Email: email}
	}()

	// Render the device code page
	h.tpl.Render(w, "oauth-device", map[string]any{
		"IntegrationID":   id,
		"IntegrationName": name,
		"UserCode":        dcr.UserCode,
		"VerificationURL": dcr.VerificationURL,
	})
}

// PollOAuth is called by the browser to check if device code auth completed.
// GET /ai-integrations/{id}/oauth/poll
func (h *OAuthHandlers) PollOAuth(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)

	h.mu.Lock()
	session, ok := h.pending[id]
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	if !ok {
		json.NewEncoder(w).Encode(map[string]any{"status": "no_session"})
		return
	}

	select {
	case result := <-session.Done:
		// Clean up
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()

		if result.Success {
			json.NewEncoder(w).Encode(map[string]any{"status": "success", "email": result.Email})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": result.Error})
		}
	default:
		json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
	}
}

// RevokeOAuth removes OAuth tokens and reverts to API key auth.
// POST /ai-integrations/{id}/oauth/revoke
func (h *OAuthHandlers) RevokeOAuth(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)

	h.db.Exec(`
		UPDATE ai_integrations
		SET auth_method = 'api_key',
		    access_token = NULL, refresh_token = NULL, id_token = NULL,
		    token_expires_at = NULL, oauth_email = NULL, oauth_account_id = NULL,
		    updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), id)

	http.Redirect(w, r, fmt.Sprintf("/ai-integrations/%d/edit", id), http.StatusSeeOther)
}

func (h *OAuthHandlers) storeTokens(integrationID int64, tokens auth.OAuthTokens) error {
	var email, accountID string
	if tokens.IDToken != "" {
		if claims, err := auth.ParseIDToken(tokens.IDToken); err == nil {
			email = claims.Email
			accountID = claims.AccountID
		}
	}

	accessToken, err := h.enc.Encrypt(tokens.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refreshToken, err := h.enc.Encrypt(tokens.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}

	var expiresAt *time.Time
	if tokens.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	_, err = h.db.Exec(`
		UPDATE ai_integrations
		SET auth_method = 'oauth',
		    access_token = ?, refresh_token = ?, id_token = ?,
		    token_expires_at = ?, oauth_email = ?, oauth_account_id = ?,
		    updated_at = ?
		WHERE id = ?
	`, accessToken, refreshToken, tokens.IDToken, expiresAt, email, accountID, time.Now().UTC(), integrationID)

	return err
}
