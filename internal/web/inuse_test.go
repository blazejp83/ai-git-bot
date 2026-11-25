package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeleteIntegrationInUse(t *testing.T) {
	aiH, gitH := testDB(t)
	d := aiH.db
	d.Exec(`INSERT INTO ai_integrations (id, name, provider_type, api_url, model, created_at, updated_at)
		VALUES (1, 'Codex', 'codex', '', 'gpt-5.4', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		       (2, 'Spare', 'anthropic', 'https://api.anthropic.com', 'claude-opus-5-5', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	d.Exec(`INSERT INTO git_integrations (id, name, provider_type, url, token, created_at, updated_at)
		VALUES (1, 'Gitea', 'GITEA', 'https://git.lan', 'x', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	for _, b := range []string{"reviewer", "coder"} {
		if _, err := d.Exec(`INSERT INTO bots (name, username, ai_integration_id, git_integration_id, webhook_secret, created_at, updated_at)
			VALUES (?, ?, 1, 1, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, b, b, "s-"+b); err != nil {
			t.Fatal(err)
		}
	}

	del := func(h http.HandlerFunc, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/x/"+id+"/delete", nil)
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		h(w, req)
		return w
	}
	exists := func(table string, id int) bool {
		var n int
		d.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&n)
		return n == 1
	}

	if w := del(aiH.Delete, "1"); w.Header().Get("Location") != "/ai-integrations?in_use=1" || !exists("ai_integrations", 1) {
		t.Errorf("in-use AI integration: location %q, still there %v", w.Header().Get("Location"), exists("ai_integrations", 1))
	}
	if w := del(gitH.Delete, "1"); w.Header().Get("Location") != "/git-integrations?in_use=1" || !exists("git_integrations", 1) {
		t.Errorf("in-use Git integration: location %q", w.Header().Get("Location"))
	}
	if w := del(aiH.Delete, "2"); w.Header().Get("Location") != "/ai-integrations" || exists("ai_integrations", 2) {
		t.Errorf("unused AI integration must be deleted: location %q, still there %v", w.Header().Get("Location"), exists("ai_integrations", 2))
	}

	got := inUseNotice(d, "ai_integrations", "ai_integration_id", "1")
	if got != `"Codex" was not deleted: it is still used by bots "coder", "reviewer". Switch them to another integration first.` {
		t.Errorf("notice = %q", got)
	}
	for _, p := range []string{"", "abc", "-1", "99"} {
		if n := inUseNotice(d, "ai_integrations", "ai_integration_id", p); n != "" {
			t.Errorf("in_use=%q must show nothing, got %q", p, n)
		}
	}
	d.Exec(`INSERT INTO ai_integrations (id, name, provider_type, api_url, model, created_at, updated_at)
		VALUES (3, 'Anthropic', 'anthropic', 'https://api.anthropic.com', 'claude-opus-5-5', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if _, err := d.Exec(`UPDATE bots SET ai_integration_id = 3`); err != nil {
		t.Fatal(err)
	}
	if n := inUseNotice(d, "ai_integrations", "ai_integration_id", "1"); n != "" {
		t.Errorf("an integration no bot uses must show nothing, got %q", n)
	}
	if !strings.Contains(inUseNotice(d, "git_integrations", "git_integration_id", "1"), `used by bots "coder", "reviewer"`) {
		t.Error("git notice missing")
	}
}
