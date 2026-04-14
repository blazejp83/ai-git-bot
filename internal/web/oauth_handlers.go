package web

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/auth"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

type OAuthHandlers struct {
	db  *sql.DB
	enc *encrypt.Service
}

func NewOAuthHandlers(db *sql.DB, enc *encrypt.Service) *OAuthHandlers {
	return &OAuthHandlers{db: db, enc: enc}
}

// StartOAuth initiates the browser-based OAuth flow for an AI integration.
// GET /ai-integrations/{id}/oauth
func (h *OAuthHandlers) StartOAuth(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid integration ID", http.StatusBadRequest)
		return
	}

	// Verify the integration exists
	var name string
	err = h.db.QueryRow("SELECT name FROM ai_integrations WHERE id = ?", id).Scan(&name)
	if err != nil {
		http.Redirect(w, r, "/ai-integrations", http.StatusSeeOther)
		return
	}

	cfg := auth.DefaultOAuthConfig()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)

	authURL, tokensCh, err := auth.BrowserLogin(ctx, cfg)
	if err != nil {
		cancel()
		slog.Error("Failed to start OAuth", "err", err)
		http.Redirect(w, r, fmt.Sprintf("/ai-integrations/%d/edit?error=oauth_start_failed", id), http.StatusSeeOther)
		return
	}

	// Wait for tokens in background, store them when received
	go func() {
		defer cancel()
		result := <-tokensCh
		if result.Err != nil {
			slog.Error("OAuth flow failed", "err", result.Err, "integration_id", id)
			return
		}
		if err := h.storeTokens(id, result.Tokens); err != nil {
			slog.Error("Failed to store OAuth tokens", "err", err, "integration_id", id)
		} else {
			slog.Info("OAuth tokens stored", "integration_id", id, "integration_name", name)
		}
	}()

	// Redirect user to OpenAI login
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// RevokeOAuth removes OAuth tokens and reverts to API key auth.
// POST /ai-integrations/{id}/oauth/revoke
func (h *OAuthHandlers) RevokeOAuth(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid integration ID", http.StatusBadRequest)
		return
	}

	_, err = h.db.Exec(`
		UPDATE ai_integrations
		SET auth_method = 'api_key',
		    access_token = NULL, refresh_token = NULL, id_token = NULL,
		    token_expires_at = NULL, oauth_email = NULL, oauth_account_id = NULL,
		    updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), id)
	if err != nil {
		slog.Error("Failed to revoke OAuth", "err", err)
	}

	http.Redirect(w, r, fmt.Sprintf("/ai-integrations/%d/edit", id), http.StatusSeeOther)
}

func (h *OAuthHandlers) storeTokens(integrationID int64, tokens auth.OAuthTokens) error {
	// Parse id_token for user info
	var email, accountID string
	if tokens.IDToken != "" {
		claims, err := auth.ParseIDToken(tokens.IDToken)
		if err != nil {
			slog.Warn("Failed to parse id_token", "err", err)
		} else {
			email = claims.Email
			accountID = claims.AccountID
		}
	}

	// Encrypt tokens
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
