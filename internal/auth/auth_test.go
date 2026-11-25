package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionManager_RoundTrip(t *testing.T) {
	sm := NewSessionManager("test-secret")

	token := sm.sign("admin", time.Now().Add(time.Hour))
	username := sm.verify(token)
	if username != "admin" {
		t.Fatalf("got %q, want %q", username, "admin")
	}
}

func TestSessionManager_Expired(t *testing.T) {
	sm := NewSessionManager("test-secret")

	token := sm.sign("admin", time.Now().Add(-time.Hour))
	username := sm.verify(token)
	if username != "" {
		t.Fatal("Expired token should return empty string")
	}
}

func TestSessionManager_InvalidToken(t *testing.T) {
	sm := NewSessionManager("test-secret")
	if sm.verify("invalid") != "" {
		t.Fatal("Invalid token should return empty")
	}
	if sm.verify("") != "" {
		t.Fatal("Empty token should return empty")
	}
	if sm.verify("abc.def") != "" {
		t.Fatal("Malformed token should return empty")
	}
}

func TestSessionManager_WrongSecret(t *testing.T) {
	sm1 := NewSessionManager("secret-one")
	sm2 := NewSessionManager("secret-two")

	token := sm1.sign("admin", time.Now().Add(time.Hour))
	if sm2.verify(token) != "" {
		t.Fatal("Wrong secret should not verify")
	}
}

func TestSessionCookieSecureAndSecret(t *testing.T) {
	sm := NewSessionManager("s3cret")
	for _, tc := range []struct {
		name   string
		setup  func(r *http.Request)
		secure bool
	}{
		{"plain http", func(r *http.Request) {}, false},
		{"behind https proxy", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, true},
	} {
		r := httptest.NewRequest("POST", "/login", nil)
		tc.setup(r)
		w := httptest.NewRecorder()
		sm.CreateSession(w, r, "admin")
		c := w.Result().Cookies()[0]
		if c.Secure != tc.secure || !c.HttpOnly {
			t.Errorf("%s: Secure=%v HttpOnly=%v", tc.name, c.Secure, c.HttpOnly)
		}
		r2 := httptest.NewRequest("GET", "/", nil)
		r2.AddCookie(c)
		if sm.GetUsername(r2) != "admin" {
			t.Errorf("%s: session not accepted", tc.name)
		}
	}

	// The old public default is not used as a signing key.
	r := httptest.NewRequest("POST", "/login", nil)
	w := httptest.NewRecorder()
	NewSessionManager("change-me-in-production-please").CreateSession(w, r, "admin")
	forged := NewSessionManager("") // a different random secret
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(w.Result().Cookies()[0])
	if forged.GetUsername(r2) != "" {
		t.Error("two managers without a secret must not accept each other's sessions")
	}
	if string(NewSessionManager("change-me-in-production-please").secret) == "change-me-in-production-please" {
		t.Error("the legacy default secret must not be used")
	}
}
