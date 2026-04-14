package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPKCEGeneration(t *testing.T) {
	pair, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	// Verifier should be 86 chars (64 bytes base64url encoded)
	if len(pair.Verifier) < 43 || len(pair.Verifier) > 128 {
		t.Fatalf("Verifier length %d out of range [43,128]", len(pair.Verifier))
	}
	// Challenge should be 43 chars (32 bytes SHA-256 base64url encoded)
	if len(pair.Challenge) != 43 {
		t.Fatalf("Challenge length %d, want 43", len(pair.Challenge))
	}
	// Should not contain padding
	if strings.Contains(pair.Verifier, "=") || strings.Contains(pair.Challenge, "=") {
		t.Fatal("Should use base64url without padding")
	}
}

func TestPKCEUniqueness(t *testing.T) {
	p1, _ := GeneratePKCE()
	p2, _ := GeneratePKCE()
	if p1.Verifier == p2.Verifier {
		t.Fatal("Different calls should produce different verifiers")
	}
}

func TestRandomState(t *testing.T) {
	s1, err := RandomState()
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := RandomState()
	if s1 == s2 {
		t.Fatal("Should produce unique states")
	}
	if len(s1) < 20 {
		t.Fatal("State should be reasonably long")
	}
}

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

func TestParseIDToken(t *testing.T) {
	// This is a hand-crafted JWT with known claims (no signature validation needed)
	// Header: {"alg":"none","typ":"JWT"}
	// Payload: {"email":"test@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"plus","chatgpt_user_id":"user123","chatgpt_account_id":"acct456"},"exp":9999999999}
	token := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJlbWFpbCI6InRlc3RAZXhhbXBsZS5jb20iLCJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9wbGFuX3R5cGUiOiJwbHVzIiwiY2hhdGdwdF91c2VyX2lkIjoidXNlcjEyMyIsImNoYXRncHRfYWNjb3VudF9pZCI6ImFjY3Q0NTYifSwiZXhwIjo5OTk5OTk5OTk5fQ.signature"

	claims, err := ParseIDToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != "test@example.com" {
		t.Fatalf("Email: got %q", claims.Email)
	}
	if claims.PlanType != "plus" {
		t.Fatalf("PlanType: got %q", claims.PlanType)
	}
	if claims.UserID != "user123" {
		t.Fatalf("UserID: got %q", claims.UserID)
	}
	if claims.AccountID != "acct456" {
		t.Fatalf("AccountID: got %q", claims.AccountID)
	}
	if claims.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt should be set")
	}
}

func TestParseIDToken_Invalid(t *testing.T) {
	_, err := ParseIDToken("not-a-jwt")
	if err == nil {
		t.Fatal("Should error on invalid JWT")
	}
}

func TestTokenNeedsRefresh(t *testing.T) {
	if !TokenNeedsRefresh(time.Time{}, 30*time.Second) {
		t.Fatal("Zero time should need refresh")
	}
	if !TokenNeedsRefresh(time.Now().Add(10*time.Second), 30*time.Second) {
		t.Fatal("Token expiring in 10s with 30s skew should need refresh")
	}
	if TokenNeedsRefresh(time.Now().Add(5*time.Minute), 30*time.Second) {
		t.Fatal("Token expiring in 5m should not need refresh")
	}
}
