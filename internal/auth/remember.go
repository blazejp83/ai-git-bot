package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	rememberCookieName = "remember"
	// rotationGrace keeps the previous token valid briefly after it was
	// replaced, so two requests sent at once do not look like theft.
	rotationGrace = 60 * time.Second
)

// RememberStore implements "remember me" with stored, rotating tokens: each
// browser gets a series and a token; every use replaces the token; a token
// that was already replaced (outside the grace period) means the cookie was
// copied, and the whole series is dropped. Logout deletes the series.
type RememberStore struct {
	db       *sql.DB
	validity time.Duration
	now      func() time.Time
}

func NewRememberStore(db *sql.DB, validity time.Duration) *RememberStore {
	return &RememberStore{db: db, validity: validity, now: func() time.Time { return time.Now().UTC() }}
}

// Days is the validity in days, for the login page.
func (s *RememberStore) Days() int { return int(s.validity.Hours() / 24) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("remember-me: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *RememberStore) setCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: rememberCookieName, Value: value, Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode, Expires: expires,
	})
}

func (s *RememberStore) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: rememberCookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// Issue starts a new series for username and sets the cookie.
func (s *RememberStore) Issue(w http.ResponseWriter, r *http.Request, username string) error {
	now := s.now()
	s.db.Exec(`DELETE FROM remember_tokens WHERE expires_at < ?`, now)
	series, token := randomHex(16), randomHex(32)
	expires := now.Add(s.validity)
	if _, err := s.db.Exec(`INSERT INTO remember_tokens (series, username, token_hash, expires_at, last_used_at) VALUES (?, ?, ?, ?, ?)`,
		series, username, hashToken(token), expires, now); err != nil {
		return err
	}
	s.setCookie(w, r, series+":"+token, expires)
	return nil
}

// Consume signs a browser back in from its cookie and rotates the token. It
// returns the username, or "" when the cookie is missing, unknown, expired,
// stolen, or belongs to an admin that no longer exists.
func (s *RememberStore) Consume(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(rememberCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	series, token, ok := strings.Cut(c.Value, ":")
	if !ok || series == "" || token == "" {
		s.clearCookie(w, r)
		return ""
	}
	var username, current string
	var prev sql.NullString
	var rotatedAt sql.NullTime
	var expires time.Time
	err = s.db.QueryRow(`SELECT username, token_hash, prev_token_hash, rotated_at, expires_at FROM remember_tokens WHERE series = ?`, series).
		Scan(&username, &current, &prev, &rotatedAt, &expires)
	if err != nil {
		s.clearCookie(w, r)
		return ""
	}
	now := s.now()
	if now.After(expires) {
		s.db.Exec(`DELETE FROM remember_tokens WHERE series = ?`, series)
		s.clearCookie(w, r)
		return ""
	}
	h := hashToken(token)
	switch {
	case subtle.ConstantTimeCompare([]byte(h), []byte(current)) == 1:
		// The current token: rotate it.
	case prev.Valid && subtle.ConstantTimeCompare([]byte(h), []byte(prev.String)) == 1 &&
		rotatedAt.Valid && now.Sub(rotatedAt.Time) < rotationGrace:
		// A request sent alongside the one that rotated: accept, do not rotate again.
		if !s.adminExists(username) {
			return ""
		}
		return username
	default:
		// A replaced token presented again: the cookie was copied. Drop the series.
		slog.Warn("Remember-me token reused; signing that browser out", "username", username)
		s.db.Exec(`DELETE FROM remember_tokens WHERE series = ?`, series)
		s.clearCookie(w, r)
		return ""
	}
	if !s.adminExists(username) {
		s.db.Exec(`DELETE FROM remember_tokens WHERE series = ?`, series)
		s.clearCookie(w, r)
		return ""
	}
	next := randomHex(32)
	expires = now.Add(s.validity)
	if _, err := s.db.Exec(`UPDATE remember_tokens SET prev_token_hash = token_hash, token_hash = ?, rotated_at = ?, expires_at = ?, last_used_at = ?
		WHERE series = ?`, hashToken(next), now, expires, now, series); err != nil {
		slog.Error("Remember-me rotation failed", "err", err)
		return ""
	}
	s.setCookie(w, r, series+":"+next, expires)
	return username
}

// Forget deletes the browser's series and clears its cookie (logout).
func (s *RememberStore) Forget(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(rememberCookieName); err == nil {
		if series, _, ok := strings.Cut(c.Value, ":"); ok {
			s.db.Exec(`DELETE FROM remember_tokens WHERE series = ?`, series)
		}
	}
	s.clearCookie(w, r)
}

func (s *RememberStore) adminExists(username string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM admin_users WHERE username = ?`, username).Scan(&n)
	return n > 0
}
