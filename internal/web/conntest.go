package web

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/repo"
)

// Step statuses shown by the connection test.
const (
	stepOK      = "OK"
	stepWarning = "WARNING"
	stepFailed  = "FAILED"
	stepSkipped = "SKIPPED"
)

type testStep struct {
	Title   string `json:"title"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type testResult struct {
	Steps []testStep `json:"steps"`
}

func writeTestResult(w http.ResponseWriter, steps ...testStep) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(testResult{Steps: steps})
}

// storedSecret returns the stored, decrypted secret of an integration, but
// only while provider and URL are unchanged: a blank field means "keep the
// stored one", and sending that secret to a different endpoint would leak it.
func storedSecret(db *sql.DB, dec func(string) (string, error), query string, id int64, providerType, url string) (secret string, changed bool, err error) {
	var storedType, storedURL, stored string
	if err := db.QueryRow(query, id).Scan(&storedType, &storedURL, &stored); err != nil {
		return "", false, err
	}
	if storedType != providerType || strings.TrimRight(storedURL, "/") != strings.TrimRight(url, "/") {
		return "", true, nil
	}
	if stored == "" {
		return "", false, nil
	}
	s, err := dec(stored)
	return s, false, err
}

// --- AI integrations ---

// TestConnection checks the AI integration as typed in the form, without saving.
func (h *AiHandlers) TestConnection(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	providerType := r.FormValue("providerType")
	apiURL := strings.TrimSpace(r.FormValue("apiUrl"))
	model := strings.TrimSpace(r.FormValue("model"))
	key := r.FormValue("apiKey")
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)

	if model == "" && providerType != "codex" && providerType != "gemini" {
		writeTestResult(w, testStep{Title: "Connection", Status: stepFailed, Message: "Enter a model name first."})
		return
	}
	if key == "" && id > 0 {
		stored, changed, err := storedSecret(h.db, h.enc.Decrypt,
			"SELECT provider_type, api_url, COALESCE(api_key,'') FROM ai_integrations WHERE id = ?", id, providerType, apiURL)
		switch {
		case err != nil:
			writeTestResult(w, testStep{Title: "Connection", Status: stepFailed, Message: "Could not read the stored API key: " + err.Error()})
			return
		case changed && aiNeedsKey(providerType):
			writeTestResult(w, testStep{Title: "Connection", Status: stepFailed,
				Message: "The provider or API URL changed, so the stored API key is not sent. Enter the key to test."})
			return
		default:
			key = stored
		}
	}
	if key == "" && aiNeedsKey(providerType) {
		writeTestResult(w, testStep{Title: "Connection", Status: stepFailed, Message: "Enter the API key."})
		return
	}

	client, err := ai.NewClient(providerType, apiURL, key, ai.Config{Model: model, MaxTokens: 2048})
	if err != nil {
		writeTestResult(w, testStep{Title: "Connection", Status: stepFailed, Message: err.Error()})
		return
	}
	writeTestResult(w, testAI(r.Context(), client, providerType, key)...)
}

func aiNeedsKey(providerType string) bool { return providerType == "anthropic" }

// aiTestTimeout bounds each AI test call; CLI providers start a process.
func aiTestTimeout(providerType string) time.Duration {
	if providerType == "codex" || providerType == "gemini" {
		return 120 * time.Second
	}
	return 60 * time.Second
}

// testAI runs the connection check and, where the provider supports native
// tools, a tool-calling check.
func testAI(ctx context.Context, client ai.Client, providerType, secret string) []testStep {
	ctx1, cancel := context.WithTimeout(ctx, aiTestTimeout(providerType))
	defer cancel()
	start := time.Now()
	answer, err := client.Chat(ctx1, nil, "Reply with the single word: yes", ai.ChatOpts{
		SystemPrompt: "You are a connection check. Reply with exactly one word.", MaxTokensOverride: 2048,
	})
	conn := testStep{Title: "Connection"}
	switch {
	case err != nil:
		conn.Status, conn.Message, conn.Detail = stepFailed, describeError(err), mask(err.Error(), secret)
		return []testStep{conn, {Title: "Tool calling", Status: stepSkipped, Message: "Skipped because the connection failed."}}
	case strings.HasPrefix(answer, "Unable to generate"):
		conn.Status, conn.Message = stepWarning, "Connected, but the model returned an empty answer."
	default:
		conn.Status = stepOK
		conn.Message = fmt.Sprintf("The model answered in %.1f s: %q", time.Since(start).Seconds(), shorten(strings.TrimSpace(answer), 80))
	}

	if !client.SupportsNativeTools() {
		msg := "This provider has no native tool calling; the agent uses a JSON protocol instead."
		if providerType == "codex" || providerType == "gemini" {
			msg = "CLI providers run their own tools; not checked here."
		}
		return []testStep{conn, {Title: "Tool calling", Status: stepSkipped, Message: msg}}
	}

	ctx2, cancel2 := context.WithTimeout(ctx, aiTestTimeout(providerType))
	defer cancel2()
	resp, err := client.ChatWithTools(ctx2,
		[]ai.ConversationMessage{{Role: "user", Content: `Call the connection_check tool with status "ok". Do not answer in text.`}},
		[]ai.ToolDef{{
			Name:        "connection_check",
			Description: "Confirms that tool calling works. Call it when asked to.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"status": map[string]any{"type": "string"}},
				"required":   []string{"status"},
			},
		}},
		ai.ChatOpts{SystemPrompt: "You are a connection check.", MaxTokensOverride: 2048})
	tools := testStep{Title: "Tool calling"}
	switch {
	case err != nil:
		tools.Status, tools.Message, tools.Detail = stepFailed, describeError(err), mask(err.Error(), secret)
	case hasToolCall(resp, "connection_check"):
		tools.Status, tools.Message = stepOK, "The model called the test tool, so the review and coding agents can use tools."
	default:
		tools.Status = stepWarning
		tools.Message = "The model answered without calling the test tool. The agents depend on tool calls; reviews may stop early."
		tools.Detail = shorten(resp.Text(), 300)
	}
	return []testStep{conn, tools}
}

func hasToolCall(resp *ai.ChatResponse, name string) bool {
	if resp == nil {
		return false
	}
	for _, tc := range resp.ToolCalls() {
		if tc.Name == name {
			return true
		}
	}
	return false
}

// --- Git integrations ---

// TestConnection checks the Git integration as typed in the form, without
// saving, by asking the provider which account the token belongs to.
func (h *GitHandlers) TestConnection(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	providerType := r.FormValue("providerType")
	url := strings.TrimRight(strings.TrimSpace(r.FormValue("url")), "/")
	username := strings.TrimSpace(r.FormValue("username"))
	token := r.FormValue("token")
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)

	if url == "" {
		writeTestResult(w, testStep{Title: "Account", Status: stepFailed, Message: "Enter the URL first."})
		return
	}
	if token == "" && id > 0 {
		stored, changed, err := storedSecret(h.db, h.enc.Decrypt,
			"SELECT provider_type, url, COALESCE(token,'') FROM git_integrations WHERE id = ?", id, providerType, url)
		switch {
		case err != nil:
			writeTestResult(w, testStep{Title: "Account", Status: stepFailed, Message: "Could not read the stored token: " + err.Error()})
			return
		case changed:
			writeTestResult(w, testStep{Title: "Account", Status: stepFailed,
				Message: "The provider or URL changed, so the stored token is not sent. Enter the token to test."})
			return
		default:
			token = stored
		}
	}
	if token == "" {
		writeTestResult(w, testStep{Title: "Account", Status: stepFailed, Message: "Enter the access token."})
		return
	}

	client, err := repo.NewClient(providerType, repo.Credentials{BaseURL: url, CloneURL: url, Username: username, Token: token})
	if err != nil {
		writeTestResult(w, testStep{Title: "Account", Status: stepFailed, Message: err.Error()})
		return
	}
	writeTestResult(w, testGit(r.Context(), client, providerType, url, token))
}

func testGit(ctx context.Context, client repo.Client, providerType, url, secret string) testStep {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	acct, err := client.CurrentUser(ctx)
	if err != nil {
		msg := describeError(err)
		if statusOf(err) == http.StatusNotFound {
			msg += " " + gitURLHint(providerType, url)
		}
		return testStep{Title: "Account", Status: stepFailed, Message: msg, Detail: mask(err.Error(), secret)}
	}
	return testStep{Title: "Account", Status: stepOK,
		Message: fmt.Sprintf("The token belongs to %s (account id %s). The bot recognises itself by this account.", acct.Login, acct.ID)}
}

// gitURLHint says which URL form each provider client expects.
func gitURLHint(providerType, url string) string {
	switch providerType {
	case "GITEA":
		if strings.Contains(url, "/api/") {
			return "Use the Gitea server's base URL, without /api/v1."
		}
		return "Use the Gitea server's base URL, e.g. https://git.example.com."
	case "GITHUB":
		return "For GitHub.com use https://api.github.com; for GitHub Enterprise, https://<host>/api/v3."
	case "GITLAB":
		return "Use the GitLab server's base URL, without /api/v4."
	case "BITBUCKET":
		return "For Bitbucket Cloud use https://api.bitbucket.org/2.0."
	}
	return ""
}

// --- errors in plain words ---

var httpStatusRe = regexp.MustCompile(`HTTP (\d{3})`)

func statusOf(err error) int {
	var rl *ai.RateLimitError
	if errors.As(err, &rl) {
		return rl.StatusCode
	}
	if m := httpStatusRe.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// describeError turns a connection or provider error into one plain sentence.
func describeError(err error) string {
	var dnsErr *net.DNSError
	var certErr *x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var ule *ai.UsageLimitError
	switch {
	case errors.As(err, &ule):
		return "The account's usage limit is reached: " + ule.Error() + "."
	case errors.Is(err, context.DeadlineExceeded):
		return "No answer in time. The server may be unreachable, or the model slow to load."
	case errors.As(err, &dnsErr):
		return fmt.Sprintf("Unknown host %q. Check the URL.", dnsErr.Name)
	case errors.As(err, &certErr), strings.Contains(err.Error(), "certificate signed by unknown authority"):
		return "The server's TLS certificate is not trusted (self-signed or private CA)."
	case errors.As(err, &hostErr):
		return "The server's TLS certificate does not match the host name."
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Connection refused: nothing is listening at that address and port."
	}
	s := statusOf(err)
	var msg string
	switch {
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		msg = fmt.Sprintf("Rejected (HTTP %d): the key or token is wrong, expired, or lacks permission.", s)
	case s == http.StatusNotFound:
		msg = "Not found (HTTP 404): usually a wrong model name or URL."
	case s == http.StatusTooManyRequests:
		return "Rate limited (HTTP 429). The credentials work; try again shortly."
	case s == 529:
		return "The provider is overloaded (HTTP 529). The credentials work; try again shortly."
	case s >= 500:
		return fmt.Sprintf("The provider had an error (HTTP %d). Try again later.", s)
	case s >= 400:
		msg = fmt.Sprintf("Rejected (HTTP %d).", s)
	default:
		return "Connection failed: " + shorten(err.Error(), 200)
	}
	if m := providerMessage(err.Error()); m != "" {
		msg += " Provider says: " + m
	}
	return msg
}

var jsonMessageRe = regexp.MustCompile(`"message"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// providerMessage pulls the provider's own "message" out of an error body.
func providerMessage(s string) string {
	if strings.Contains(s, "<html") || strings.Contains(s, "<!DOCTYPE") {
		return ""
	}
	if m := jsonMessageRe.FindStringSubmatch(s); m != nil {
		var msg string
		if json.Unmarshal([]byte(`"`+m[1]+`"`), &msg) == nil {
			return shorten(msg, 300)
		}
	}
	return ""
}

// mask removes the secret from text shown in the browser.
func mask(s, secret string) string {
	if len(secret) >= 4 {
		s = strings.ReplaceAll(s, secret, "***")
	}
	if strings.Contains(s, "<html") || strings.Contains(s, "<!DOCTYPE") {
		return "(HTML error page omitted)"
	}
	return shorten(s, 500)
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
