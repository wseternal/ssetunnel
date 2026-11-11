package clientserver

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/clientapp"
)

const maxBodySize = 64 * 1024 // 64 KiB limit on JSON request bodies.

// errBodyTooLarge is returned when the request body exceeds maxBodySize.
var errBodyTooLarge = errors.New("request body too large")

// registerAPIRoutes registers the thin API routes that delegate to clientapp.
func (s *Server) registerAPIRoutes(r *mux.Router) {
	r.Use(requestLogging)
	r.HandleFunc("/login", s.handleLogin).Methods("POST")
	r.HandleFunc("/totp-check", s.handleTOTPCheck).Methods("POST")
	r.HandleFunc("/session", s.handleGetSession).Methods("GET")
	r.HandleFunc("/logout", s.handleLogout).Methods("POST")
	r.HandleFunc("/agent/status", s.handleAgentStatus).Methods("GET")
	r.HandleFunc("/agent/start", s.handleAgentStart).Methods("POST")
	r.HandleFunc("/agent/stop", s.handleAgentStop).Methods("POST")
	r.HandleFunc("/agent/restart", s.handleAgentRestart).Methods("POST")
	r.HandleFunc("/connect/list", s.handleConnectList).Methods("GET")
	r.HandleFunc("/connect/start", s.handleConnectStart).Methods("POST")
	r.HandleFunc("/connect/stop/{name}", s.handleConnectStop).Methods("POST")
	r.HandleFunc("/connect/restart/{name}", s.handleConnectRestart).Methods("POST")
}

func readJSON(r *http.Request, v any) error {
	// Read maxBodySize+1 byte so we can detect oversized payloads.
	limited := io.LimitReader(r.Body, maxBodySize+1)
	defer r.Body.Close()
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(data) > maxBodySize {
		return errBodyTooLarge
	}
	return json.Unmarshal(data, v)
}

// decodeJSON reads and decodes a JSON request body, writing the appropriate
// error response (413 for oversized, 400 for malformed) on failure.
// Returns true when decoding succeeded, false when an error was written.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := readJSON(r, v); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// POST /api/login
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Server       string `json:"server"`
		TunnelServer string `json:"tunnel_server"`
		Username     string `json:"username"`
		Password     string `json:"password"`
		TOTPCode     string `json:"totp_code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Server == "" {
		req.Server = "http://127.0.0.1:8081"
	}

	result, err := clientapp.Login(clientapp.LoginParams{
		ConsoleURL: req.Server,
		TunnelURL:  req.TunnelServer,
		Username:   req.Username,
		Password:   req.Password,
		TOTPCode:   req.TOTPCode,
	})
	if err != nil {
		log.Printf("api: login failed for %q: %v", req.Username, err)
		writeError(w, http.StatusUnauthorized, "login failed: invalid credentials or TOTP code")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// POST /api/totp-check
func (s *Server) handleTOTPCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Server   string `json:"server"`
		Username string `json:"username"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	required, _ := clientapp.CheckTOTPRequired(req.Server, req.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"totp_required": required})
}

// GET /api/session
func (s *Server) handleGetSession(w http.ResponseWriter, _ *http.Request) {
	sess, err := clientapp.ResolveSession("", "ui")
	if err != nil {
		writeError(w, http.StatusNotFound, "no active session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"username":   sess.Username,
		"role":       sess.Role,
		"tunnel_url": sess.URL,
		"expires_at": sess.ExpiresAt.Format(time.RFC3339),
	})
}

// POST /api/logout
func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	if err := auth.DeleteSession(""); err != nil {
		log.Printf("api: logout failed to clear session: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to clear session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/agent/status
func (s *Server) handleAgentStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"running": false})
}

// agentStartRequest is the JSON request for starting an agent.
type agentStartRequest struct {
	ServerURL   string `json:"server_url"`
	Target      string `json:"target"`
	AgentID     string `json:"agent_id"`
	BatchSize   int    `json:"batch_size"`
	Concurrency int    `json:"concurrency"`
	Compress    bool   `json:"compress"`
	NoAutoTune  bool   `json:"no_auto_tune"`
	Verbose     bool   `json:"verbose"`
}

// connectStartRequest is the JSON request for starting a named connect session.
type connectStartRequest struct {
	Name      string `json:"name"`
	ServerURL string `json:"server_url"`
	AgentID   string `json:"agent_id"`
	Target    string `json:"target"`
	Local     string `json:"local"`
	BatchSize int    `json:"batch_size"`
}

// POST /api/agent/start
func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	var req agentStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	_ = clientapp.AgentConfig{
		ServerURL:   req.ServerURL,
		Target:      req.Target,
		AgentID:     req.AgentID,
		BatchSize:   req.BatchSize,
		Concurrency: req.Concurrency,
		Compress:    req.Compress,
		NoAutoTune:  req.NoAutoTune,
		Verbose:     req.Verbose,
	}
	// TODO: integrate with UI-owned agent runtime from Task 7 plan.
	writeError(w, http.StatusNotImplemented, "agent runtime not yet integrated")
}

// POST /api/agent/stop
func (s *Server) handleAgentStop(w http.ResponseWriter, _ *http.Request) {
	// TODO: integrate with UI-owned agent runtime.
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/agent/restart
func (s *Server) handleAgentRestart(w http.ResponseWriter, r *http.Request) {
	var req agentStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// TODO: integrate with UI-owned agent runtime.
	writeError(w, http.StatusNotImplemented, "agent runtime not yet integrated")
}

// GET /api/connect/list
func (s *Server) handleConnectList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

// POST /api/connect/start
func (s *Server) handleConnectStart(w http.ResponseWriter, r *http.Request) {
	var req connectStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// TODO: integrate with StartNamedConnect from Task 8.
	writeError(w, http.StatusNotImplemented, "connect runtime not yet integrated")
}

// POST /api/connect/stop/{name}
func (s *Server) handleConnectStop(w http.ResponseWriter, r *http.Request) {
	_ = mux.Vars(r)["name"]
	// TODO: integrate with named connect instance management.
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/connect/restart/{name}
func (s *Server) handleConnectRestart(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	_ = vars["name"]
	var req connectStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// TODO: integrate with named connect instance management.
	writeError(w, http.StatusNotImplemented, "connect runtime not yet integrated")
}

// requestLogging logs API requests for audit and debugging.
func requestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("api: %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
