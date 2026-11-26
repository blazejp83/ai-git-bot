package web

import (
	"database/sql"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/hooks"
)

func TestValidateHook(t *testing.T) {
	ok := hookForm{Name: "n", URL: "https://ntfy.lan/x"}
	ev := []string{hooks.ReviewCompleted}
	cases := map[string]struct {
		f       hookForm
		events  []string
		headers string
		want    string
	}{
		"valid":          {ok, ev, `{"Authorization":"Bearer t"}`, ""},
		"no name":        {hookForm{URL: ok.URL}, ev, "", "Enter a name."},
		"ftp url":        {hookForm{Name: "n", URL: "ftp://x/y"}, ev, "", "http:// or https://"},
		"no events":      {ok, nil, "", "at least one event"},
		"bad repo":       {hookForm{Name: "n", URL: ok.URL, RepoFilter: "justaname"}, ev, "", "owner/name"},
		"headers array":  {ok, ev, `["x"]`, "JSON object"},
		"headers remove": {ok, ev, `{}`, ""},
	}
	for name, c := range cases {
		got := validateHook(c.f, c.events, c.headers)
		if (c.want == "" && got != "") || (c.want != "" && !strings.Contains(got, c.want)) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestSaveHookKeepsAndRemovesSecrets(t *testing.T) {
	aiH, _ := testDB(t)
	h := &HookHandlers{db: aiH.db, enc: aiH.enc, tpl: nil}
	save := func(form url.Values) {
		req := httptest.NewRequest("POST", "/hooks/save", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.Save(w, req)
		if w.Code != 303 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
	}
	stored := func() (secret, headers sql.NullString) {
		h.db.QueryRow(`SELECT secret, headers FROM hook_endpoints WHERE id = 1`).Scan(&secret, &headers)
		return
	}
	base := url.Values{"name": {"ntfy"}, "url": {"https://ntfy.lan/r"}, "event.review.completed": {"true"}, "enabled": {"true"}}

	first := url.Values{}
	for k, v := range base {
		first[k] = v
	}
	first.Set("secret", "s3cret")
	first.Set("headers", `{"Authorization":"Bearer t"}`)
	save(first)
	secret, headers := stored()
	if dec, _ := h.enc.Decrypt(secret.String); dec != "s3cret" || strings.Contains(secret.String, "s3cret") || !headers.Valid {
		t.Fatalf("stored secret must be encrypted: %q / headers %v", secret.String, headers.Valid)
	}

	keep := url.Values{"id": {"1"}}
	for k, v := range base {
		keep[k] = v
	}
	save(keep) // blank fields keep both
	if s2, h2 := stored(); s2.String != secret.String || h2.String != headers.String {
		t.Error("blank secret/headers must keep the stored values")
	}

	keep.Set("headers", "{}")
	keep.Set("removeSecret", "true")
	save(keep)
	if s3, h3 := stored(); s3.Valid || h3.Valid {
		t.Errorf("remove: secret %v headers %v", s3.Valid, h3.Valid)
	}
}
