package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
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

// legacyDefaultSecret was the built-in SESSION_SECRET fallback. It is public,
// so anyone could forge a session with it; it is treated as unset.
const legacyDefaultSecret = "change-me-in-production-please"

// NewSessionManager signs sessions with secret. Without a usable secret it
// generates a random one for this process: sessions then do not survive a
// restart, but they cannot be forged.
func NewSessionManager(secret string) *SessionManager {
	if secret == "" || secret == legacyDefaultSecret {
		slog.Warn("SESSION_SECRET is not set; using a random secret, so logins do not survive a restart")
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			panic("session secret: " + err.Error())
		}
		return &SessionManager{secret: random}
	}
	return &SessionManager{secret: []byte(secret)}
}

// isHTTPS reports whether the browser reached the app over HTTPS, directly or
// through a TLS-terminating reverse proxy (e.g. Synology DSM).
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// CreateSession sets a signed session cookie with the username. The cookie is
// Secure when the request came over HTTPS, so it is never sent over plain HTTP.
func (sm *SessionManager) CreateSession(w http.ResponseWriter, r *http.Request, username string) {
	expires := time.Now().Add(sessionMaxAge)
	token := sm.sign(username, expires)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
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
func (sm *SessionManager) DestroySession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
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
