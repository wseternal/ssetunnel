package server

import (
	"net/http"

	"github.com/wseternal/ssetunnel/internal/auth"
)

// TargetShell is the magic target name that tells the agent to spawn
// an interactive shell with a PTY instead of dialing a TCP address.
// Must match the constant in the agent package.
const TargetShell = "__shell__"

// ShellConnectHandler returns an http.Handler that serves the cloud shell
// connect endpoint (SSE downstream). It wraps the existing /connect handler
// with forced target=__shell__ and user-scoped agent access.
//
// The handler expects the request URL to contain ?agent=<id> and ?id=<session>.
// The target query parameter is always overridden to __shell__.
func (h *Handler) ShellConnectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate user session auth.
		sessInfo := UserSessionFromContext(r)
		if sessInfo == nil {
			http.Error(w, "Unauthorized: user session required", http.StatusUnauthorized)
			return
		}

		agentID := r.URL.Query().Get("agent")
		if agentID == "" {
			http.Error(w, "agent query parameter is required", http.StatusBadRequest)
			return
		}

		// User-scoped access: non-admin users can only shell into their own agents.
		if !isAdmin(sessInfo) {
			if !h.agentOwnedByUser(agentID, sessInfo.UserID) {
				http.Error(w, "agent not found or access denied", http.StatusNotFound)
				return
			}
		}

		// Force target to __shell__. Ignore any client-provided target.
		q := r.URL.Query()
		q.Set("target", TargetShell)
		r.URL.RawQuery = q.Encode()

		h.handleConnect(w, r)
	})
}

// ShellConnectUpHandler returns an http.Handler that serves the cloud shell
// upstream POST endpoint. It delegates directly to handleConnectUp which
// uses the connect session's own auth (X-SSET-Session header).
func (h *Handler) ShellConnectUpHandler() http.Handler {
	return http.HandlerFunc(h.handleConnectUp)
}

// isAdmin reports whether the user session has admin role.
func isAdmin(sessInfo *auth.UserSessionInfo) bool {
	return sessInfo != nil && auth.HasPermission(sessInfo.Role, auth.PermAdmin)
}

// agentOwnedByUser checks if any session with the given agentID belongs
// to the specified user.
func (h *Handler) agentOwnedByUser(agentID string, userID int64) bool {
	found := false
	h.reg.Range(func(s *Session) bool {
		if s.AgentID() == agentID && s.UserID() == userID {
			found = true
			return false
		}
		return true
	})
	return found
}
