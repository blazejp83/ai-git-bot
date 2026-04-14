package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/auth"
)

type contextKey string

const usernameKey contextKey = "username"

// RequireAuth redirects unauthenticated users to /login.
// Paths in skipPrefixes are allowed without auth.
func RequireAuth(sm *auth.SessionManager, skipPrefixes []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			for _, prefix := range skipPrefixes {
				if strings.HasPrefix(path, prefix) || path == prefix {
					next.ServeHTTP(w, r)
					return
				}
			}

			username := sm.GetUsername(r)
			if username == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			ctx := context.WithValue(r.Context(), usernameKey, username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Username returns the authenticated username from context.
func Username(r *http.Request) string {
	if v, ok := r.Context().Value(usernameKey).(string); ok {
		return v
	}
	return ""
}
