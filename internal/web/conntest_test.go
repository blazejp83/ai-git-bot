package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/db"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

func TestDescribeError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&net.DNSError{Name: "gitea.lan", Err: "no such host"}, `Unknown host "gitea.lan"`},
		{fmt.Errorf("dial: %w", &net.OpError{Op: "dial", Err: errConnRefused()}), "Connection refused"},
		{fmt.Errorf("post: %w", context.DeadlineExceeded), "No answer in time"},
		{errors.New(`anthropic API error (HTTP 401): {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`),
			"Rejected (HTTP 401): the key or token is wrong, expired, or lacks permission. Provider says: invalid x-api-key"},
		{errors.New(`anthropic API error (HTTP 404): {"error":{"type":"not_found_error","message":"model: claude-opus-9"}}`),
			"Not found (HTTP 404): usually a wrong model name or URL. Provider says: model: claude-opus-9"},
		{&ai.RateLimitError{StatusCode: 529}, "overloaded (HTTP 529)"},
		{errors.New("HTTP 502: <html><body>Bad gateway</body></html>"), "The provider had an error (HTTP 502)"},
		{errors.New("x509: certificate signed by unknown authority"), "TLS certificate is not trusted"},
	}
	for _, c := range cases {
		if got := describeError(c.err); !strings.Contains(got, c.want) {
			t.Errorf("describeError(%v) = %q, want it to contain %q", c.err, got, c.want)
		}
	}
	if got := mask("bad key sk-ant-secret123 rejected", "sk-ant-secret123"); strings.Contains(got, "secret") {
		t.Errorf("mask left the secret: %q", got)
	}
	if got := mask("HTTP 502: <html>…</html>", ""); got != "(HTML error page omitted)" {
		t.Errorf("mask kept an HTML page: %q", got)
	}
}

func errConnRefused() error {
	_, err := net.DialTimeout("tcp", "127.0.0.1:1", time.Second) // port 1: nothing listens
	var op *net.OpError
	if errors.As(err, &op) {
		return op.Err
	}
	return err
}

// fakeAnthropic answers the two requests of an AI connection test.
type fakeAnthropic struct {
	mu       sync.Mutex
	keys     []string
	noTool   bool
	status   int
	requests int
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.keys = append(f.keys, r.Header.Get("x-api-key"))
	f.requests++
	f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
		return
	}
	var body struct {
		Tools []any `json:"tools"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if len(body.Tools) > 0 && !f.noTool {
		io.WriteString(w, `{"content":[{"type":"tool_use","id":"t1","name":"connection_check","input":{"status":"ok"}}],"stop_reason":"tool_use"}`)
		return
	}
	io.WriteString(w, `{"content":[{"type":"text","text":"yes"}],"stop_reason":"end_turn"}`)
}

func TestAIConnectionTest(t *testing.T) {
	steps := func(f *fakeAnthropic) []testStep {
		srv := httptest.NewServer(f)
		defer srv.Close()
		c, _ := ai.NewClient("anthropic", srv.URL, "k", ai.Config{Model: "claude-opus-5-5", MaxTokens: 2048})
		return testAI(context.Background(), c, "anthropic", "k")
	}

	ok := steps(&fakeAnthropic{})
	if ok[0].Status != stepOK || !strings.Contains(ok[0].Message, `"yes"`) || ok[1].Status != stepOK {
		t.Errorf("healthy provider: %+v", ok)
	}
	noTool := steps(&fakeAnthropic{noTool: true})
	if noTool[1].Status != stepWarning {
		t.Errorf("model that skips the tool: %+v", noTool[1])
	}
	f := &fakeAnthropic{status: 401}
	bad := steps(f)
	if bad[0].Status != stepFailed || !strings.Contains(bad[0].Message, "HTTP 401") || bad[1].Status != stepSkipped || f.requests != 1 {
		t.Errorf("wrong key: %+v (requests %d)", bad, f.requests)
	}
}

func testDB(t *testing.T) (*AiHandlers, *GitHandlers) {
	t.Helper()
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	enc := encrypt.New("test-key")
	return &AiHandlers{db: conn, enc: enc}, &GitHandlers{db: conn, enc: enc}
}

func postForm(h http.HandlerFunc, form url.Values) testResult {
	req := httptest.NewRequest("POST", "/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)
	var res testResult
	json.Unmarshal(w.Body.Bytes(), &res)
	return res
}

// A blank key field reuses the stored key, but only for the same provider and URL.
func TestAIConnectionTestStoredKey(t *testing.T) {
	aiH, _ := testDB(t)
	f := &fakeAnthropic{}
	srv := httptest.NewServer(f)
	defer srv.Close()

	encKey, _ := aiH.enc.Encrypt("sk-stored")
	aiH.db.Exec(`INSERT INTO ai_integrations (id, name, provider_type, api_url, api_key, model, created_at, updated_at)
		VALUES (1, 'A', 'anthropic', ?, ?, 'claude-opus-5-5', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, srv.URL, encKey)

	res := postForm(aiH.TestConnection, url.Values{"id": {"1"}, "providerType": {"anthropic"}, "apiUrl": {srv.URL}, "model": {"claude-opus-5-5"}})
	if len(res.Steps) != 2 || res.Steps[0].Status != stepOK || len(f.keys) == 0 || f.keys[0] != "sk-stored" {
		t.Fatalf("stored key not used: %+v keys=%v", res.Steps, f.keys)
	}

	sent := len(f.keys)
	res = postForm(aiH.TestConnection, url.Values{"id": {"1"}, "providerType": {"anthropic"}, "apiUrl": {"http://elsewhere.example"}, "model": {"claude-opus-5-5"}})
	if res.Steps[0].Status != stepFailed || !strings.Contains(res.Steps[0].Message, "stored API key is not sent") || len(f.keys) != sent {
		t.Errorf("stored key must not go to a changed URL: %+v", res.Steps)
	}

	res = postForm(aiH.TestConnection, url.Values{"providerType": {"anthropic"}, "apiUrl": {srv.URL}, "model": {""}})
	if res.Steps[0].Status != stepFailed || !strings.Contains(res.Steps[0].Message, "model") {
		t.Errorf("missing model: %+v", res.Steps)
	}
}

func TestGitConnectionTest(t *testing.T) {
	_, gitH := testDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token good" {
			w.WriteHeader(401)
			io.WriteString(w, `{"message":"user does not exist [uid: 0, name: ]"}`)
			return
		}
		io.WriteString(w, `{"id":7,"login":"ai-bot"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := postForm(gitH.TestConnection, url.Values{"providerType": {"GITEA"}, "url": {srv.URL + "/"}, "token": {"good"}})
	if res.Steps[0].Status != stepOK || !strings.Contains(res.Steps[0].Message, "ai-bot (account id 7)") {
		t.Errorf("good token: %+v", res.Steps)
	}
	res = postForm(gitH.TestConnection, url.Values{"providerType": {"GITEA"}, "url": {srv.URL}, "token": {"bad"}})
	if res.Steps[0].Status != stepFailed || !strings.Contains(res.Steps[0].Message, "HTTP 401") {
		t.Errorf("bad token: %+v", res.Steps)
	}
	res = postForm(gitH.TestConnection, url.Values{"providerType": {"GITEA"}, "url": {srv.URL + "/api/v1"}, "token": {"good"}})
	if res.Steps[0].Status != stepFailed || !strings.Contains(res.Steps[0].Message, "without /api/v1") {
		t.Errorf("API URL hint missing: %+v", res.Steps)
	}
}
