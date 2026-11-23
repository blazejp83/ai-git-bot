package repo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func serve(t *testing.T, routes map[string]string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, ok := routes[r.URL.EscapedPath()]
		if !ok {
			t.Logf("unrouted %s", r.URL.EscapedPath())
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestGitHubCurrentUserCachedAndPRHead(t *testing.T) {
	srv, calls := serve(t, map[string]string{
		"/user":              `{"id": 42, "login": "ai-bot"}`,
		"/repos/o/r/pulls/7": `{"state":"open","head":{"sha":"abc123","ref":"feat","repo":{"full_name":"fork/r"}}}`,
	})
	c := NewGitHubClient(Credentials{BaseURL: srv.URL, Token: "t"})
	for i := 0; i < 3; i++ {
		a, err := c.CurrentUser(context.Background())
		if err != nil || a.ID != "42" || a.Login != "ai-bot" {
			t.Fatalf("CurrentUser = %+v, %v", a, err)
		}
	}
	if *calls != 1 {
		t.Errorf("account not cached: %d calls", *calls)
	}
	h, err := c.GetPRHead(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if h.SHA != "abc123" || h.SourceRepo != "fork/r" || h.Ref != "refs/pull/7/head" || !h.Open {
		t.Errorf("PRHead = %+v", h)
	}
}

func TestGitLabPRHeadUsesLiveBranchTip(t *testing.T) {
	srv, _ := serve(t, map[string]string{
		"/api/v4/projects/grp%2Fr/merge_requests/3":       `{"state":"opened","sha":"stale","source_branch":"feat/x","source_project_id":9,"target_project_id":9}`,
		"/api/v4/projects/9/repository/branches/feat%2Fx": `{"commit":{"id":"live"}}`,
		"/api/v4/user": `{"id": 5, "username": "gl-bot"}`,
	})
	c := NewGitLabClient(Credentials{BaseURL: srv.URL, Token: "t"})
	h, err := c.GetPRHead(context.Background(), "grp", "r", 3)
	if err != nil {
		t.Fatal(err)
	}
	if h.SHA != "live" || h.Ref != "refs/merge-requests/3/head" || h.SourceRepo != "grp/r" {
		t.Errorf("PRHead = %+v", h)
	}
	if a, _ := c.CurrentUser(context.Background()); a.ID != "5" || a.Login != "gl-bot" {
		t.Errorf("CurrentUser = %+v", a)
	}
}

func TestBitbucketPRHeadResolvesShortHash(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef01234567"
	srv, _ := serve(t, map[string]string{
		"/repositories/ws/r/pullrequests/2":         `{"state":"OPEN","source":{"branch":{"name":"feat"},"commit":{"hash":"0123456789ab"},"repository":{"full_name":"other/r"}}}`,
		"/repositories/other/r/commit/0123456789ab": `{"hash":"` + full + `"}`,
		"/user": `{"account_id":"557058:x","uuid":"{u}","nickname":"bb-bot"}`,
	})
	c := NewBitbucketClient(Credentials{BaseURL: srv.URL, Token: "t"})
	h, err := c.GetPRHead(context.Background(), "ws", "r", 2)
	if err != nil {
		t.Fatal(err)
	}
	if h.SHA != full || h.SourceRepo != "other/r" || h.Ref != "" || h.Branch != "feat" {
		t.Errorf("PRHead = %+v", h)
	}
	a, _ := c.CurrentUser(context.Background())
	if a.ID != "557058:x" || a.UUID != "{u}" || a.Login != "bb-bot" {
		t.Errorf("CurrentUser = %+v", a)
	}
}

func TestGitLabDiffHasGitHeaders(t *testing.T) {
	d := buildUnifiedDiff([]any{
		map[string]any{"old_path": "new.go", "new_path": "new.go", "new_file": true, "diff": "@@ -0,0 +1 @@\n+x\n"},
		map[string]any{"old_path": "old.go", "new_path": "old.go", "deleted_file": true, "diff": "@@ -1 +0,0 @@\n-y"},
	})
	for _, want := range []string{"diff --git a/new.go b/new.go\nnew file mode 100644\n--- /dev/null\n+++ b/new.go\n", "deleted file mode 100644\n--- a/old.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-y\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
}
