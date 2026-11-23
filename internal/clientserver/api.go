package clientserver

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
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
	r.HandleFunc("/login", s.handleLogin).Methods("POST")
	r.HandleFunc("/totp-check", s.handleTOTPCheck).Methods("POST")
	r.HandleFunc("/session", s.handleGetSession).Methods("GET")
	r.HandleFunc("/sessions", s.handleListSessions).Methods("GET")
	r.HandleFunc("/logout", s.handleLogout).Methods("POST")
	r.HandleFunc("/agent/status", s.handleAgentStatus).Methods("GET")
	r.HandleFunc("/agent/start", s.handleAgentStart).Methods("POST")
	r.HandleFunc("/agent/stop", s.handleAgentStop).Methods("POST")
	r.HandleFunc("/agent/restart", s.handleAgentStart).Methods("POST")
	r.HandleFunc("/connect/list", s.handleConnectList).Methods("GET")
	r.HandleFunc("/connect/start", s.handleConnectStart).Methods("POST")
	r.HandleFunc("/connect/stop/{name}", s.handleConnectStop).Methods("POST")
	r.HandleFunc("/connect/restart/{name}", s.handleConnectRestart).Methods("POST")

	// The module surface is proxied, not reimplemented: the browser talks to
	// this authenticated server, which forwards to the agent's loopback API
	// (the same shape the console serves through the tunnel).
	r.PathPrefix("/v1/modules").HandlerFunc(s.handleModuleProxy)
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
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: write JSON: %v", err)
	}
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
		req.Server = "http://127.0.0.1:8081/console"
	}

	result, err := clientapp.Login(r.Context(), clientapp.LoginParams{
		ConsoleURL: req.Server,
		TunnelURL:  req.TunnelServer,
		Username:   req.Username,
		Password:   req.Password,
		TOTPCode:   req.TOTPCode,
	})
	if err != nil {
		log.Printf("api: login failed for %q: %v", req.Username, err)
		switch {
		case errors.Is(err, clientapp.ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, "login failed: invalid credentials or TOTP code")
		case errors.Is(err, clientapp.ErrInvalidConsoleURL):
			// Locally generated validation message — safe to echo.
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, clientapp.ErrConsoleUnavailable):
			writeError(w, http.StatusBadGateway, "login failed: console unreachable or rejected the request")
		default:
			writeError(w, http.StatusInternalServerError, "login failed")
		}
		return
	}
	log.Printf("api: login succeeded for %q (tunnel=%s)", req.Username, result.TunnelURL)
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
	required, err := clientapp.CheckTOTPRequired(r.Context(), req.Server, req.Username)
	if err != nil {
		log.Printf("api: totp check failed for %q: %v", req.Username, err)
		writeError(w, http.StatusBadGateway, "TOTP check failed: console unreachable")
		return
	}
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
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Delete the session credential first so a concurrent agent start
	// during teardown cannot pick up a session that is about to vanish.
	var req struct {
		Server string `json:"server"`
	}
	// Body is optional — empty means "default session". Silently ignore
	// decode errors so a content-less POST still logs out the default.
	_ = readJSON(r, &req)
	if _, resolvedServer, _, _, err := auth.LoadSession(req.Server); err != nil {
		log.Printf("api: logout: cannot read session file: %v (runtimes will be stopped but session not deleted)", err)
	} else if resolvedServer != "" {
		if err := auth.DeleteSession(resolvedServer); err != nil {
			log.Printf("api: logout failed to clear session: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to clear session")
			return
		}
	}

	// Tear down runtimes owned by the UI (ADR-005): parallel and bounded
	// like Serve's shutdown, so the handler stays inside the server
	// WriteTimeout and the browser's abort budget.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := s.agent.stop(); err != nil {
			log.Printf("api: logout: agent teardown: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		s.connects.stopAll()
	}()
	wg.Wait()

	log.Printf("api: logout completed")
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

func (r connectStartRequest) config() clientapp.ConnectConfig {
	return clientapp.ConnectConfig{
		ServerURL: r.ServerURL,
		AgentID:   r.AgentID,
		Target:    r.Target,
		Local:     r.Local,
		BatchSize: r.BatchSize,
	}
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
		Version:     s.version,
	}
	if err := s.agent.start(s.serverCtx, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, uiError(err))
		return
	}
	log.Printf("api: agent started (target=%s, id=%s)", cfg.Target, cfg.AgentID)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/agent/stop
func (s *Server) handleAgentStop(w http.ResponseWriter, _ *http.Request) {
	if err := s.agent.stop(); err != nil {
		writeError(w, http.StatusInternalServerError, uiError(err))
		return
	}
	log.Printf("api: agent stopped")
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/agent/restart is an alias for start: start replaces a running
// agent, so the two operations are identical.

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
	if err := s.connects.start(req.Name, req.config()); err != nil {
		writeError(w, http.StatusInternalServerError, uiError(err))
		return
	}
	log.Printf("api: connect[%s] started (target=%s)", req.Name, req.Target)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/connect/stop/{name}
func (s *Server) handleConnectStop(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	// stop returns only errNotFound or nil: a session's terminal run error
	// is logged, not surfaced — tearing down an errored session succeeded.
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
	// restart() merges empty fields from the running instance's config
	// under the registry lock, so a bare restart keeps the same tunnel.
	if err := s.connects.restart(name, req.config()); err != nil {
		writeError(w, http.StatusInternalServerError, uiError(err))
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

// statusWriter records the response status so requestLogging can report it.
// It forwards Flush and Unwrap: the module proxy streams a module response
// through this writer, and a wrapper that hid http.Flusher would turn a
// streamed response into one delivered only when the handler returns.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards the flush to the underlying writer.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the original writer to http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// requestLogging logs API requests for audit and debugging. Successful
// polling GETs are silenced to keep the terminal output clean; errors are
// always logged, including on polling paths.
func requestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		polling := r.Method == http.MethodGet && pollingPaths[r.URL.Path]
		if !polling || sw.status >= 400 {
			log.Printf("api: %s %s -> %d (%s)", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}
