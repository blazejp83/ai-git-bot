package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/auth"
	"github.com/tmseidel/ai-git-bot/internal/db"
)

// A browser whose session expired is signed back in from its remember cookie.
func TestRequireAuthRemember(t *testing.T) {
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := db.Migrate(conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	auth.NewAdminService(conn).CreateAdmin("admin", "pass12345")
	sm := auth.NewSessionManager("secret")
	remember := auth.NewRememberStore(conn, 14*24*time.Hour)

	w := httptest.NewRecorder()
	remember.Issue(w, httptest.NewRequest("POST", "/login", nil), "admin")
	var rememberCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		rememberCookie = c
	}

	h := RequireAuth(sm, remember, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello " + Username(r)))
	}))

	r := httptest.NewRequest("GET", "/dashboard", nil)
	r.AddCookie(rememberCookie) // no session cookie
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Body.String() != "hello admin" {
		t.Fatalf("remembered browser: %d %q", w.Code, w.Body.String())
	}
	var gotSession bool
	for _, c := range w.Result().Cookies() {
		gotSession = gotSession || c.Name == "session"
	}
	if !gotSession {
		t.Error("a fresh session cookie must be issued")
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/dashboard", nil))
	if w.Code != http.StatusSeeOther {
		t.Errorf("no cookies must redirect to login, got %d", w.Code)
	}
}
