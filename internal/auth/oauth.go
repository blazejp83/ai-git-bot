package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultIssuer   = "https://auth.openai.com"
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultPort     = 1455
	CallbackPath    = "/auth/callback"
	DefaultScopes   = "openid profile email offline_access"
)

// OAuthTokens holds the tokens returned from OpenAI's OAuth flow.
type OAuthTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// OAuthConfig holds configuration for the OAuth flow.
type OAuthConfig struct {
	Issuer   string
	ClientID string
	Port     int
	Scopes   string
}

func DefaultOAuthConfig() OAuthConfig {
	return OAuthConfig{
		Issuer:   DefaultIssuer,
		ClientID: DefaultClientID,
		Port:     DefaultPort,
		Scopes:   DefaultScopes,
	}
}

// BrowserLogin performs the browser-based OAuth PKCE flow:
// 1. Generates PKCE pair + state
// 2. Starts a local HTTP server to receive the callback
// 3. Returns the authorization URL for the browser
// 4. Waits for the callback, exchanges the code for tokens
func BrowserLogin(ctx context.Context, cfg OAuthConfig) (authURL string, tokensCh <-chan OAuthResult, err error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", nil, fmt.Errorf("generate PKCE: %w", err)
	}

	state, err := RandomState()
	if err != nil {
		return "", nil, fmt.Errorf("generate state: %w", err)
	}

	redirectURI := fmt.Sprintf("http://localhost:%d%s", cfg.Port, CallbackPath)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {cfg.Scopes},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	authURL = cfg.Issuer + "/authorize?" + params.Encode()

	ch := make(chan OAuthResult, 1)

	go runCallbackServer(ctx, cfg, pkce, state, redirectURI, ch)

	return authURL, ch, nil
}

// OAuthResult is the result of an OAuth flow attempt.
type OAuthResult struct {
	Tokens OAuthTokens
	Err    error
}

func runCallbackServer(ctx context.Context, cfg OAuthConfig, pkce PKCEPair, expectedState, redirectURI string, result chan<- OAuthResult) {
	done := make(chan struct{})
	var once sync.Once
	send := func(r OAuthResult) {
		once.Do(func() {
			result <- r
			close(done)
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc(CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errParam := r.URL.Query().Get("error")

		if errParam != "" {
			desc := r.URL.Query().Get("error_description")
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, "<html><body><h2>Login failed</h2><p>%s: %s</p><p>You can close this window.</p></body></html>", errParam, desc)
			send(OAuthResult{Err: fmt.Errorf("oauth error: %s — %s", errParam, desc)})
			return
		}

		if state != expectedState {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>State mismatch (CSRF protection).</p></body></html>")
			send(OAuthResult{Err: fmt.Errorf("state mismatch")})
			return
		}

		tokens, err := exchangeCode(ctx, cfg, code, pkce.Verifier, redirectURI)
		if err != nil {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, "<html><body><h2>Login failed</h2><p>%s</p><p>You can close this window.</p></body></html>", err)
			send(OAuthResult{Err: err})
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h2>Login successful!</h2><p>You can close this window and return to AI Git Bot.</p></body></html>")
		send(OAuthResult{Tokens: tokens})
	})

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		send(OAuthResult{Err: fmt.Errorf("listen on port %d: %w", cfg.Port, err)})
		return
	}

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	// Also close after receiving result (with a small delay for the response to flush)
	go func() {
		<-done
		time.Sleep(500 * time.Millisecond)
		srv.Close()
	}()

	slog.Info("OAuth callback server listening", "port", cfg.Port)
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		send(OAuthResult{Err: fmt.Errorf("callback server: %w", err)})
	}
}

func exchangeCode(ctx context.Context, cfg OAuthConfig, code, verifier, redirectURI string) (OAuthTokens, error) {
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
