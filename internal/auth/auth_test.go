package auth

import (
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
