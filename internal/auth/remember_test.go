package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/db"
)

func rememberSetup(t *testing.T) (*RememberStore, *time.Time) {
	t.Helper()
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	if err := NewAdminService(conn).CreateAdmin("admin", "pass12345"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s := NewRememberStore(conn, 14*24*time.Hour)
	s.now = func() time.Time { return now }
	return s, &now
}

func cookieFrom(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == rememberCookieName {
			return c
		}
	}
	return nil
}

func consume(s *RememberStore, c *http.Cookie) (string, *http.Cookie) {
	r := httptest.NewRequest("GET", "/dashboard", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	return s.Consume(w, r), cookieFrom(w)
}

func TestRememberRotationGraceAndTheft(t *testing.T) {
	s, now := rememberSetup(t)
	w := httptest.NewRecorder()
	if err := s.Issue(w, httptest.NewRequest("POST", "/login", nil), "admin"); err != nil {
		t.Fatal(err)
	}
	first := cookieFrom(w)
	if first == nil || !first.HttpOnly || first.Expires.Sub(*now) != 14*24*time.Hour {
		t.Fatalf("cookie = %+v", first)
	}

	user, second := consume(s, first)
	if user != "admin" || second == nil || second.Value == first.Value {
		t.Fatalf("first use: %q, rotated cookie %+v", user, second)
	}
	// A request sent alongside, with the old token: accepted within the grace period.
	*now = now.Add(10 * time.Second)
	if user, _ := consume(s, first); user != "admin" {
		t.Error("old token within the grace period must be accepted")
	}
	// The same old token later: the cookie was copied, the series is dropped.
	*now = now.Add(2 * time.Minute)
	if user, cleared := consume(s, first); user != "" || cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("replayed token: %q, cookie %+v", user, cleared)
	}
	if user, _ := consume(s, second); user != "" {
		t.Error("after theft detection even the current token must fail")
	}
}

func TestRememberExpiryForgetAndDeletedAdmin(t *testing.T) {
	s, now := rememberSetup(t)
	issue := func() *http.Cookie {
		w := httptest.NewRecorder()
		s.Issue(w, httptest.NewRequest("POST", "/login", nil), "admin")
		return cookieFrom(w)
	}

	c := issue()
	*now = now.Add(15 * 24 * time.Hour)
	if user, _ := consume(s, c); user != "" {
		t.Error("an expired series must fail")
	}

	c = issue()
	r := httptest.NewRequest("POST", "/logout", nil)
	r.AddCookie(c)
	s.Forget(httptest.NewRecorder(), r)
	if user, _ := consume(s, c); user != "" {
		t.Error("after logout the cookie must fail")
	}

	c = issue()
	s.db.Exec(`DELETE FROM admin_users`)
	if user, _ := consume(s, c); user != "" {
		t.Error("a deleted admin must not be signed back in")
	}
	if user, _ := consume(s, &http.Cookie{Name: rememberCookieName, Value: "garbage"}); user != "" {
		t.Error("a malformed cookie must fail")
	}
}
