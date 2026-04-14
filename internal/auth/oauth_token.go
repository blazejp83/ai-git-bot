package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// IDTokenClaims holds the relevant claims parsed from OpenAI's id_token JWT.
type IDTokenClaims struct {
	Email     string
	PlanType  string // free, plus, pro, business, enterprise, edu
	UserID    string
	AccountID string
	ExpiresAt time.Time
}

// ParseIDToken extracts claims from an OpenAI id_token JWT without validating the signature.
// (Signature validation is unnecessary since we received the token directly from OpenAI's token endpoint over TLS.)
func ParseIDToken(idToken string) (*IDTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT: expected 3 parts, got %d", len(parts))
	}

	// Decode payload (part 1), adding padding if needed
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return nil, fmt.Errorf("parse JWT payload: %w", err)
	}

	claims := &IDTokenClaims{}

	// Email: try standard claim, then OpenAI-specific
	if email, ok := raw["email"].(string); ok {
		claims.Email = email
	}
	if profile, ok := raw["https://api.openai.com/profile"].(map[string]any); ok {
		if email, ok := profile["email"].(string); ok && claims.Email == "" {
			claims.Email = email
		}
	}

	// Auth claims
	if auth, ok := raw["https://api.openai.com/auth"].(map[string]any); ok {
		if pt, ok := auth["chatgpt_plan_type"].(string); ok {
			claims.PlanType = pt
		}
		if uid, ok := auth["chatgpt_user_id"].(string); ok {
			claims.UserID = uid
		} else if uid, ok := auth["user_id"].(string); ok {
			claims.UserID = uid
		}
		if aid, ok := auth["chatgpt_account_id"].(string); ok {
			claims.AccountID = aid
		}
	}

	// Expiration
	if exp, ok := raw["exp"].(float64); ok {
		claims.ExpiresAt = time.Unix(int64(exp), 0)
	}

	return claims, nil
}

// RefreshResult holds the result of a token refresh attempt.
type RefreshResult struct {
	Tokens OAuthTokens
	// Permanent is true if the error is not retryable (token expired/reused/invalidated).
	Permanent bool
}

// RefreshTokens refreshes an OAuth access token using the refresh token.
func RefreshTokens(ctx context.Context, cfg OAuthConfig, refreshToken string) (*RefreshResult, error) {
	data := url.Values{
		"client_id":     {cfg.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Issuer+"/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		// Check for permanent errors
		var errResp struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &errResp)

		switch errResp.Error {
		case "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated":
			return &RefreshResult{Permanent: true}, fmt.Errorf("permanent refresh error: %s", errResp.Error)
		default:
			return &RefreshResult{Permanent: true}, fmt.Errorf("refresh unauthorized: %s", string(body))
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var tokens OAuthTokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	return &RefreshResult{Tokens: tokens}, nil
}

// TokenNeedsRefresh returns true if the token expires within the given skew duration.
func TokenNeedsRefresh(expiresAt time.Time, skew time.Duration) bool {
	if expiresAt.IsZero() {
		return true
	}
	return time.Now().Add(skew).After(expiresAt)
}
