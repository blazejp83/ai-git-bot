package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceCodeResponse is returned when requesting a device code.
type DeviceCodeResponse struct {
	DeviceAuthID    string `json:"device_auth_id"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	Interval        int    `json:"interval"`
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

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return OAuthTokens{}, ctx.Err()
		case <-time.After(interval):
		}

		payload, _ := json.Marshal(map[string]string{
			"device_auth_id": dcr.DeviceAuthID,
			"user_code":      dcr.UserCode,
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
			continue // Transient network error, retry
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusTooManyRequests {
			// Still pending — user hasn't authorized yet
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
		redirectURI := fmt.Sprintf("http://localhost:%d%s", cfg.Port, CallbackPath)
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

	return tokens, nil
}
