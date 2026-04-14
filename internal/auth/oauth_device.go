package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

func truncateToken(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// DeviceCodeResponse is returned when requesting a device code.
type DeviceCodeResponse struct {
	DeviceAuthID    string          `json:"device_auth_id"`
	UserCode        string          `json:"user_code"`
	VerificationURL string          `json:"verification_url"`
	RawInterval     json.RawMessage `json:"interval"`
	Interval        int             `json:"-"`
}

// DeviceCodeTokenResponse is the polling response that contains the auth code.
type DeviceCodeTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeChallenge     string `json:"code_challenge"`
	CodeVerifier      string `json:"code_verifier"`
}

// RequestDeviceCode initiates the device code flow by requesting a user code.
func RequestDeviceCode(ctx context.Context, cfg OAuthConfig) (*DeviceCodeResponse, error) {
	payload, _ := json.Marshal(map[string]string{
		"client_id": cfg.ClientID,
		"scope":     cfg.Scopes,
	})

	req, err := http.NewRequestWithContext(ctx, "POST",
		cfg.Issuer+"/api/accounts/deviceauth/usercode",
		bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var dcr DeviceCodeResponse
	if err := json.Unmarshal(body, &dcr); err != nil {
		return nil, fmt.Errorf("parse device code response: %w", err)
	}

	// Parse interval — OpenAI sends it as either int or string
	if len(dcr.RawInterval) > 0 {
		var intVal int
		if err := json.Unmarshal(dcr.RawInterval, &intVal); err == nil {
			dcr.Interval = intVal
		} else {
			var strVal string
			if err := json.Unmarshal(dcr.RawInterval, &strVal); err == nil {
				fmt.Sscanf(strVal, "%d", &dcr.Interval)
			}
		}
	}

	if dcr.VerificationURL == "" {
		dcr.VerificationURL = cfg.Issuer + "/codex/device"
	}
	if dcr.Interval == 0 {
		dcr.Interval = 5
	}

	return &dcr, nil
}

// PollDeviceCode polls for authorization completion and exchanges for tokens.
// maxWait is the maximum time to wait (typically 15 minutes).
func PollDeviceCode(ctx context.Context, cfg OAuthConfig, dcr *DeviceCodeResponse, maxWait time.Duration) (OAuthTokens, error) {
	deadline := time.Now().Add(maxWait)
	interval := time.Duration(dcr.Interval) * time.Second

	// Wait one full interval before first poll
	select {
	case <-ctx.Done():
		return OAuthTokens{}, ctx.Err()
	case <-time.After(interval):
	}

	for time.Now().Before(deadline) {
		payload, _ := json.Marshal(map[string]string{
			"device_auth_id": dcr.DeviceAuthID,
			"user_code":      dcr.UserCode,
			"client_id":      cfg.ClientID,
		})

		req, err := http.NewRequestWithContext(ctx, "POST",
			cfg.Issuer+"/api/accounts/deviceauth/token",
			bytes.NewReader(payload))
		if err != nil {
			return OAuthTokens{}, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			// Transient network error — wait and retry
			select {
			case <-ctx.Done():
				return OAuthTokens{}, ctx.Err()
			case <-time.After(interval):
			}
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// 202 = pending, 429 = slow down, 403 = not yet recognized
		if resp.StatusCode == http.StatusAccepted ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusForbidden {
			select {
			case <-ctx.Done():
				return OAuthTokens{}, ctx.Err()
			case <-time.After(interval):
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return OAuthTokens{}, fmt.Errorf("device auth poll failed (HTTP %d): %s", resp.StatusCode, string(body))
		}

		var dctr DeviceCodeTokenResponse
		if err := json.Unmarshal(body, &dctr); err != nil {
			return OAuthTokens{}, fmt.Errorf("parse device token response: %w", err)
		}

		// Exchange the authorization code for tokens using PKCE
		// Device code flow uses a different redirect_uri than browser flow
		redirectURI := cfg.Issuer + "/deviceauth/callback"
		return exchangeCodeWithVerifier(ctx, cfg, dctr.AuthorizationCode, dctr.CodeVerifier, redirectURI)
	}

	return OAuthTokens{}, fmt.Errorf("device authorization timed out after %v", maxWait)
}

func exchangeCodeWithVerifier(ctx context.Context, cfg OAuthConfig, code, verifier, redirectURI string) (OAuthTokens, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Issuer+"/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return OAuthTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return OAuthTokens{}, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return OAuthTokens{}, fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var tokens OAuthTokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return OAuthTokens{}, fmt.Errorf("parse token response: %w", err)
	}

	// Debug: log token types to diagnose which is which
	slog.Info("Token exchange result",
		"has_access_token", tokens.AccessToken != "",
		"access_token_prefix", truncateToken(tokens.AccessToken, 15),
		"has_id_token", tokens.IDToken != "",
		"id_token_prefix", truncateToken(tokens.IDToken, 15),
		"has_refresh_token", tokens.RefreshToken != "",
		"expires_in", tokens.ExpiresIn,
	)

	return tokens, nil
}
