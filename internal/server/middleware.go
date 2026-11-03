package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/wseternal/ssetunnel/internal/auth"
)

const SessionCookieName = "ssetunnel_session"

// ExtractBearerToken extracts bearer token from Authorization header.
// The URL query parameter `token` is intentionally not checked here to avoid
// token leakage via server logs, browser history, and referrer headers.
// The /events SSE endpoint extracts query tokens separately in the handler.
func ExtractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	return ""
}

// ExtractBearerTokenWithQuery is like ExtractBearerToken but also checks the
// URL query parameter `token`. Used only for SSE EventSource endpoints that
// cannot set custom headers.
func ExtractBearerTokenWithQuery(r *http.Request) string {
	if tok := ExtractBearerToken(r); tok != "" {
		return tok
	}
	return r.URL.Query().Get("token")
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

// requireSessionAccess verifies that the current user may access a session
// owned by ownerUserID. It writes an HTTP error and returns false when
// access is denied. When auth is disabled, UserSessionMiddleware injects a
// synthetic admin with UserID=0, so sessInfo is never nil.
func requireSessionAccess(w http.ResponseWriter, r *http.Request, ownerUserID int64) bool {
	sessInfo := UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "Unauthorized: user session required", http.StatusUnauthorized)
		return false
	}
	if !isAdmin(sessInfo) && sessInfo.UserID != ownerUserID {
		http.Error(w, "access denied", http.StatusForbidden)
		return false
	}
	return true
}

// AgentAuthMiddleware protects endpoints requiring agent-role tokens.
// It tries bearer-token validation first (tokens table), then falls back to
// user-session validation (user_sessions table, from `ssetunnel login`).
// If store is nil, auth is disabled and requests pass through.
func AgentAuthMiddleware(store *auth.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Use WithQuery because /events SSE endpoint uses EventSource
			// which cannot set custom headers.
			tokenStr := ExtractBearerTokenWithQuery(r)
			if tokenStr == "" {
				http.Error(w, "Unauthorized: missing bearer token", http.StatusUnauthorized)
				return
			}

			// Try bearer token first (standalone tokens from the tokens table).
			tokInfo, err := store.ValidateToken(r.Context(), tokenStr)
			if err == nil && auth.HasPermission(tokInfo.Role, auth.PermAgent) {
				ctx := context.WithValue(r.Context(), tokenInfoKey, &tokInfo)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Fallback: user session token (from `ssetunnel login`).
			sessInfo, err := store.ValidateUserSession(r.Context(), tokenStr)
			if err == nil && auth.UserHasPermission(sessInfo.Role, sessInfo.PermConnect, sessInfo.PermAgent, auth.PermAgent) {
				ctx := context.WithValue(r.Context(), userSessionKey, sessInfo)
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

// ConnectAuthMiddleware protects the /connect endpoint, requiring connect-level
// permissions. Like AgentAuthMiddleware, it supports both bearer tokens (from the
// tokens table) and user session tokens (from `ssetunnel login`). Query token
// extraction is enabled for SSE endpoints that cannot set custom headers.
// If store is nil, auth is disabled and a synthetic admin session is injected.
func ConnectAuthMiddleware(store *auth.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				// Auth disabled: inject synthetic admin so requireSessionAccess works.
				sessInfo := &auth.UserSessionInfo{
					UserID:      0,
					Role:        "admin",
					PermConnect: true,
					PermAgent:   true,
				}
				ctx := context.WithValue(r.Context(), userSessionKey, sessInfo)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Use WithQuery because /connect SSE endpoint uses EventSource
			// which cannot set custom headers.
			tokenStr := ExtractBearerTokenWithQuery(r)
			if tokenStr == "" {
				http.Error(w, "Unauthorized: missing bearer token", http.StatusUnauthorized)
				return
			}

			// Try bearer token first (standalone tokens from the tokens table).
			tokInfo, err := store.ValidateToken(r.Context(), tokenStr)
			if err == nil && auth.HasPermission(tokInfo.Role, auth.PermConnect) {
				ctx := context.WithValue(r.Context(), tokenInfoKey, &tokInfo)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Fallback: user session token (from `ssetunnel login`).
			sessInfo, err := store.ValidateUserSession(r.Context(), tokenStr)
			if err == nil && auth.UserHasPermission(sessInfo.Role, sessInfo.PermConnect, sessInfo.PermAgent, auth.PermConnect) {
				ctx := context.WithValue(r.Context(), userSessionKey, sessInfo)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			http.Error(w, "Unauthorized: invalid or insufficient permissions", http.StatusUnauthorized)
		})
	}
}

// UserSessionMiddleware validates user session tokens (from ~/.ssetunnel/session).
// It checks the user_sessions table and stores the UserSessionInfo in the request context.
// When store is nil (auth disabled), it injects a synthetic admin session so console
// endpoints work without requiring login.
func UserSessionMiddleware(store *auth.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				// Auth disabled: inject synthetic admin session so console works
				sessInfo := &auth.UserSessionInfo{
					UserID:      0,
					Role:        "admin",
					PermConnect: true,
					PermAgent:   true,
				}
				ctx := context.WithValue(r.Context(), userSessionKey, sessInfo)
				next.ServeHTTP(w, r.WithContext(ctx))
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
