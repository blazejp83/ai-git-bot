package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "session"
	sessionMaxAge     = 24 * time.Hour
)

type SessionManager struct {
	secret []byte
}

func NewSessionManager(secret string) *SessionManager {
	return &SessionManager{secret: []byte(secret)}
}

// CreateSession sets a signed session cookie with the username.
func (sm *SessionManager) CreateSession(w http.ResponseWriter, username string) {
	expires := time.Now().Add(sessionMaxAge)
	token := sm.sign(username, expires)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

// GetUsername returns the authenticated username from the session cookie, or "".
func (sm *SessionManager) GetUsername(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return sm.verify(cookie.Value)
}

// DestroySession clears the session cookie.
func (sm *SessionManager) DestroySession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// sign creates a token: base64(nonce|expiry|username)|hmac
func (sm *SessionManager) sign(username string, expires time.Time) string {
	nonce := make([]byte, 8)
	rand.Read(nonce)

	payload := fmt.Sprintf("%s|%d|%s", hex.EncodeToString(nonce), expires.Unix(), username)
	mac := hmac.New(sha256.New, sm.secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// verify checks the token and returns the username, or "" if invalid/expired.
func (sm *SessionManager) verify(token string) string {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ""
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	payload := string(payloadBytes)

	mac := hmac.New(sha256.New, sm.secret)
	mac.Write(payloadBytes)
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expectedSig)) {
		return ""
	}

	// Parse: nonce|expiry|username
	fields := strings.SplitN(payload, "|", 3)
	if len(fields) != 3 {
		return ""
	}

	var expiryUnix int64
	fmt.Sscanf(fields[1], "%d", &expiryUnix)
	if time.Now().Unix() > expiryUnix {
		return ""
	}

	return fields[2]
}
