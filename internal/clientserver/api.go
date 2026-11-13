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
	r.HandleFunc("/sessions", s.handleListSessions).Methods("GET")
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

// GET /api/sessions
func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	entries, err := auth.ListAllSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read sessions")
		return
	}
	type sessionInfo struct {
		ServerURL  string `json:"server_url"`
		ConsoleURL string `json:"console_url"`
		Username   string `json:"username,omitempty"`
	}
	sessions := make([]sessionInfo, 0, len(entries))
	for _, e := range entries {
		sessions = append(sessions, sessionInfo{
			ServerURL:  e.ServerURL,
			ConsoleURL: e.ConsoleURL,
			Username:   e.Username,
		})
	}
	writeJSON(w, http.StatusOK, sessions)
}

// POST /api/logout
func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	// Resolve the current session first so we only delete this one entry,
	// preserving other saved sessions for the login page dropdown.
	sess, err := clientapp.ResolveSession("", "ui")
	if err == nil && sess.URL != "" {
		if err := auth.DeleteSession(sess.URL); err != nil {
			log.Printf("api: logout failed to clear session: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to clear session")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/agent/status
func (s *Server) handleAgentStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.agent.status())
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
	cfg := clientapp.AgentConfig{
		ServerURL:   req.ServerURL,
		Target:      req.Target,
		AgentID:     req.AgentID,
		BatchSize:   req.BatchSize,
		Concurrency: req.Concurrency,
		Compress:    req.Compress,
		NoAutoTune:  req.NoAutoTune,
		Verbose:     req.Verbose,
	}
	if err := s.agent.start(s.serverCtx, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/agent/stop
func (s *Server) handleAgentStop(w http.ResponseWriter, _ *http.Request) {
	if err := s.agent.stop(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/agent/restart
func (s *Server) handleAgentRestart(w http.ResponseWriter, r *http.Request) {
	var req agentStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg := clientapp.AgentConfig{
		ServerURL:   req.ServerURL,
		Target:      req.Target,
		AgentID:     req.AgentID,
		BatchSize:   req.BatchSize,
		Concurrency: req.Concurrency,
		Compress:    req.Compress,
		NoAutoTune:  req.NoAutoTune,
		Verbose:     req.Verbose,
	}
	if err := s.agent.start(s.serverCtx, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/connect/list
func (s *Server) handleConnectList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.connects.list())
}

// POST /api/connect/start
func (s *Server) handleConnectStart(w http.ResponseWriter, r *http.Request) {
	var req connectStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg := clientapp.ConnectConfig{
		ServerURL: req.ServerURL,
		AgentID:   req.AgentID,
		Target:    req.Target,
		Local:     req.Local,
		BatchSize: req.BatchSize,
	}
	if err := s.connects.start(req.Name, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/connect/stop/{name}
func (s *Server) handleConnectStop(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if err := s.connects.stop(name); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/connect/restart/{name}
func (s *Server) handleConnectRestart(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	var req connectStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// Stop the existing instance first.
	_ = s.connects.stop(name)
	cfg := clientapp.ConnectConfig{
		ServerURL: req.ServerURL,
		AgentID:   req.AgentID,
		Target:    req.Target,
		Local:     req.Local,
		BatchSize: req.BatchSize,
	}
	if err := s.connects.start(name, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pollingPaths are GET endpoints the frontend polls on a timer.
// Logging them would flood the terminal with repetitive lines.
var pollingPaths = map[string]bool{
	"/api/agent/status": true,
	"/api/connect/list": true,
}

// requestLogging logs API requests for audit and debugging.
// Polling GET endpoints are silenced to keep the terminal output clean.
func requestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !pollingPaths[r.URL.Path] {
			log.Printf("api: %s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
