package server

import (
	"net/http"
	"strings"

	"github.com/wseternal/ssetunnel/internal/auth"
)

const SessionCookieName = "ssetunnel_session"

// ExtractBearerToken extracts bearer token from Authorization header or URL query parameter `token`.
func ExtractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		return tok
	}
	return ""
}

// AgentAuthMiddleware protects endpoints requiring agent-role tokens.
// If store is nil, auth is disabled and requests pass through.
func AgentAuthMiddleware(store *auth.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				next.ServeHTTP(w, r)
				return
			}

			tokenStr := ExtractBearerToken(r)
			if tokenStr == "" {
				http.Error(w, "Unauthorized: missing bearer token", http.StatusUnauthorized)
				return
			}

			tokInfo, err := store.ValidateToken(r.Context(), tokenStr)
			if err != nil || (tokInfo.Role != "agent" && tokInfo.Role != "admin") {
				http.Error(w, "Unauthorized: invalid or insufficient permissions", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// AdminSessionMiddleware protects management endpoints requiring active admin cookie session or admin bearer token.
// If store is nil, auth is disabled and requests pass through.
func AdminSessionMiddleware(store *auth.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				next.ServeHTTP(w, r)
				return
			}

			// 1. Check Bearer token first
			tokenStr := ExtractBearerToken(r)
			if tokenStr != "" {
				tokInfo, err := store.ValidateToken(r.Context(), tokenStr)
				if err == nil && tokInfo.Role == "admin" {
					next.ServeHTTP(w, r)
					return
				}
			}

			// 2. Check Cookie
			cookie, err := r.Cookie(SessionCookieName)
			if err == nil && cookie.Value != "" {
				if err := store.ValidateAdminSession(r.Context(), cookie.Value); err == nil {
					next.ServeHTTP(w, r)
					return
				}
			}

			http.Error(w, "Unauthorized: admin session or token required", http.StatusUnauthorized)
		})
	}
}
