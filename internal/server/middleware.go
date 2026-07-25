package server

import (
	"context"
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

// contextKey is an unexported type for context keys defined in this package.
type contextKey int

const (
	tokenInfoKey contextKey = iota
	userSessionKey
)

// TokenInfoFromContext retrieves the validated TokenInfo stored by middleware.
func TokenInfoFromContext(r *http.Request) *auth.TokenInfo {
	if v, ok := r.Context().Value(tokenInfoKey).(*auth.TokenInfo); ok {
		return v
	}
	return nil
}

// UserSessionFromContext retrieves the validated UserSessionInfo stored by middleware.
func UserSessionFromContext(r *http.Request) *auth.UserSessionInfo {
	if v, ok := r.Context().Value(userSessionKey).(*auth.UserSessionInfo); ok {
		return v
	}
	return nil
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
			if err == nil && auth.HasPermission(tokInfo.Role, auth.PermAgent) {
				ctx := context.WithValue(r.Context(), tokenInfoKey, &tokInfo)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			http.Error(w, "Unauthorized: invalid or insufficient permissions", http.StatusUnauthorized)
		})
	}
}

// AdminSessionMiddleware protects management endpoints requiring an active user session
// with admin permissions. If store is nil, auth is disabled and requests pass through.
func AdminSessionMiddleware(store *auth.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				next.ServeHTTP(w, r)
				return
			}

			tokenStr := ExtractBearerToken(r)
			if tokenStr == "" {
				http.Error(w, "Unauthorized: admin session required", http.StatusUnauthorized)
				return
			}

			sessInfo, err := store.ValidateUserSession(r.Context(), tokenStr)
			if err == nil && auth.HasPermission(sessInfo.Role, auth.PermAdmin) {
				ctx := context.WithValue(r.Context(), userSessionKey, sessInfo)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			http.Error(w, "Unauthorized: admin session required", http.StatusUnauthorized)
		})
	}
}

// UserSessionMiddleware validates user session tokens (from ~/.ssetunnel/session).
// It checks the user_sessions table and stores the UserSessionInfo in the request context.
func UserSessionMiddleware(store *auth.Store) func(http.Handler) http.Handler {
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

			sessInfo, err := store.ValidateUserSession(r.Context(), tokenStr)
			if err != nil {
				http.Error(w, "Unauthorized: invalid user session", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userSessionKey, sessInfo)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
