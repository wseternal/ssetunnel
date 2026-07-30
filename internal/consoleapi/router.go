package consoleapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/server"
)

// minPasswordLength is the minimum allowed password length for user creation and updates.
const minPasswordLength = 8

// validRoles is the allowlist of valid user roles.
var validRoles = map[string]bool{"admin": true, "user": true}

type API struct {
	store *auth.Store
	reg   *server.Registry
	mc    *metrics.MetricsCollector // nil when metrics disabled

	// Login rate limiting: password failures per IP.
	pwRateMu    sync.Mutex
	pwRateCount map[string]int
	pwRateReset map[string]time.Time

	// TOTP rate limiting: TOTP failures per IP:username.
	totpRateMu    sync.Mutex
	totpRateCount map[string]int
	totpRateReset map[string]time.Time

	// stopCleanup signals the rate limiter cleanup goroutine to exit.
	stopCleanup chan struct{}
}

// Router wraps the HTTP router with a reference to the API for late configuration.
type Router struct {
	*mux.Router
	api *API
}

// SetMetrics attaches a metrics collector to the API for statistics endpoints.
func (r *Router) SetMetrics(mc *metrics.MetricsCollector) {
	r.api.mc = mc
}

func NewRouter(store *auth.Store, reg *server.Registry) *Router {
	api := &API{
		store:         store,
		reg:           reg,
		pwRateCount:   make(map[string]int),
		pwRateReset:   make(map[string]time.Time),
		totpRateCount: make(map[string]int),
		totpRateReset: make(map[string]time.Time),
		stopCleanup:   make(chan struct{}),
	}

	// Periodic cleanup of expired rate limit entries to prevent unbounded memory growth.
	go api.rateLimiterCleanup()

	r := mux.NewRouter()

	// CORS middleware — restrict to same-origin by default.
	r.Use(corsMiddleware)

	// Public auth routes
	r.HandleFunc("/api/v1/user-login", api.handleUserLogin).Methods("POST")
	r.HandleFunc("/api/v1/user-login-check", api.handleUserLoginCheck).Methods("POST")
	r.HandleFunc("/api/v1/logout", api.handleLogout).Methods("POST")

	// Middleware
	adminAuth := server.AdminSessionMiddleware(store)
	userAuth := server.UserSessionMiddleware(store)

	// Session listing: any authenticated user (filtered by role in handler)
	r.Handle("/api/v1/sessions", userAuth(http.HandlerFunc(api.handleSessions))).Methods("GET")

	// User management routes (admin only)
	r.Handle("/api/v1/users", adminAuth(http.HandlerFunc(api.handleUsers))).Methods("GET", "POST")
	r.Handle("/api/v1/users/{id}", adminAuth(http.HandlerFunc(api.handleUserUpdate))).Methods("PATCH", "DELETE")

	// Connected agents: live agent IDs from session registry (any authenticated user)
	r.Handle("/api/v1/connected-agents", userAuth(http.HandlerFunc(api.handleConnectedAgents))).Methods("GET")

	// Agent config routes: read for any authenticated user, write for admin only
	r.Handle("/api/v1/agents", userAuth(http.HandlerFunc(api.handleAgents))).Methods("GET")
	r.Handle("/api/v1/agents", adminAuth(http.HandlerFunc(api.handleAgents))).Methods("POST")
	r.Handle("/api/v1/agents/{id}", adminAuth(http.HandlerFunc(api.handleAgentUpdate))).Methods("PATCH", "DELETE")

	// User session routes (authenticated user)
	r.Handle("/api/v1/me", userAuth(http.HandlerFunc(api.handleMe))).Methods("GET")

	// TOTP management routes (authenticated user)
	r.Handle("/api/v1/totp/status", userAuth(http.HandlerFunc(api.handleTOTPStatus))).Methods("GET")
	r.Handle("/api/v1/totp/begin-setup", userAuth(http.HandlerFunc(api.handleTOTPBeginSetup))).Methods("POST")
	r.Handle("/api/v1/totp/verify-setup", userAuth(http.HandlerFunc(api.handleTOTPVerifySetup))).Methods("POST")
	r.Handle("/api/v1/totp", userAuth(http.HandlerFunc(api.handleTOTPDelete))).Methods("DELETE")

	// Metrics routes (authenticated user, scoped by user_id for non-admins)
	r.Handle("/api/v1/metrics/overview", userAuth(http.HandlerFunc(api.handleMetricsOverview))).Methods("GET")
	r.Handle("/api/v1/metrics/agents", userAuth(http.HandlerFunc(api.handleMetricsAgents))).Methods("GET")
	r.Handle("/api/v1/metrics/agents/{agentID}/samples", userAuth(http.HandlerFunc(api.handleMetricsSamples))).Methods("GET")
	r.Handle("/api/v1/metrics/agents/{agentID}/decisions", userAuth(http.HandlerFunc(api.handleMetricsDecisions))).Methods("GET")

	return &Router{Router: r, api: api}
}

// corsMiddleware adds restrictive CORS headers for the console API.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			// Only echo back the origin if it matches the request host (same-origin).
			if origin == "http://"+r.Host || origin == "https://"+r.Host {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "3600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Revoke the server-side session token so it can't be reused.
	if a.store != nil {
		if tokenStr := server.ExtractBearerToken(r); tokenStr != "" {
			_ = a.store.RevokeUserSession(r.Context(), tokenStr)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     server.SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type SessionInfo struct {
	ID            string    `json:"id"`
	BytesSent     uint64    `json:"bytes_sent"`
	BytesReceived uint64    `json:"bytes_received"`
	CreatedAt     time.Time `json:"created_at"`
	RemoteAddr    string    `json:"remote_addr,omitempty"`
}

func (a *API) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessInfo := server.UserSessionFromContext(r)
	isAdmin := sessInfo != nil && auth.HasPermission(sessInfo.Role, auth.PermAdmin)

	sessions := []SessionInfo{}

	if a.reg != nil {
		a.reg.Range(func(s *server.Session) bool {
			// Non-admin users only see sessions attributed to their own user ID.
			// Sessions with userID == 0 (unattributed, e.g. standalone tokens) are
			// admin-only visible.
			if !isAdmin && (sessInfo == nil || s.UserID() != sessInfo.UserID) {
				return true
			}
			sent, rec, created := s.Stats()
			sessions = append(sessions, SessionInfo{
				ID:            s.ID(),
				BytesSent:     sent,
				BytesReceived: rec,
				CreatedAt:     created,
				RemoteAddr:    s.RemoteAddr().String(),
			})
			return true
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessions)
}

func (a *API) handleUserLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request JSON", http.StatusBadRequest)
		return
	}

	if req.Username == "" || req.Password == "" {
		http.Error(w, "username and password required", http.StatusBadRequest)
		return
	}

	// Rate limiting: password failures per IP.
	clientIP := r.RemoteAddr
	if a.checkPWRateLimit(clientIP) {
		http.Error(w, "too many failed login attempts, try again later", http.StatusTooManyRequests)
		return
	}

	if a.store == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	// Validate password.
	user, err := a.store.ValidatePassword(r.Context(), req.Username, req.Password)
	if err != nil {
		a.recordPWFailure(clientIP)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if user.DisabledAt != nil {
		http.Error(w, "account disabled", http.StatusForbidden)
		return
	}

	// Per-user TOTP verification with recovery code fallback.
	totpEnrolled := user.TOTPSecret != ""
	if totpEnrolled {
		totpKey := clientIP + ":" + req.Username
		if a.checkTOTPRateLimit(totpKey) {
			http.Error(w, "too many failed TOTP attempts, try again later", http.StatusTooManyRequests)
			return
		}

		if !auth.VerifyTOTP(user.TOTPSecret, req.TOTPCode) {
			// Try recovery code fallback.
			ok, err := a.store.ConsumeRecoveryCode(r.Context(), user.ID, req.TOTPCode)
			if err != nil || !ok {
				a.recordTOTPFailure(totpKey)
				http.Error(w, "invalid TOTP or recovery code", http.StatusUnauthorized)
				return
			}
		}
		// Successful TOTP resets the TOTP rate limit.
		a.resetTOTPRateLimit(totpKey)
	}

	// Successful login resets the password rate limit for this IP.
	a.resetPWRateLimit(clientIP)

	// Create user session (30 days)
	sessionToken, err := auth.GenerateToken()
	if err != nil {
		http.Error(w, "failed to generate session", http.StatusInternalServerError)
		return
	}

	if err := a.store.CreateUserSession(r.Context(), user.ID, sessionToken, 30*24*time.Hour); err != nil {
		http.Error(w, "failed to store session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "ok",
		"token":         sessionToken,
		"user_id":       user.ID,
		"username":      user.Username,
		"role":          user.Role,
		"totp_enrolled": totpEnrolled,
	})
}

// handleUserLoginCheck returns whether a user has TOTP enrolled (pre-login check).
// Returns totp_required=true for non-existent users to prevent username enumeration.
func (a *API) handleUserLoginCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request JSON", http.StatusBadRequest)
		return
	}

	if a.store == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	// Single constant-time query: returns enrolled status and whether user exists.
	// Returns totp_required=true for non-existent users to prevent username enumeration.
	enrolled, found, err := a.store.UserTOTPEnrolled(r.Context(), req.Username)
	if err != nil {
		// Fail closed: if DB is unavailable, assume TOTP is required.
		log.Printf("user-login-check: DB error for %q: %v", req.Username, err)
		enrolled = true
	} else if !found {
		// User does not exist — return true to prevent enumeration.
		enrolled = true
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"totp_required": enrolled})
}

// --- Password rate limiting (per IP) ---

func (a *API) checkPWRateLimit(ip string) bool {
	a.pwRateMu.Lock()
	defer a.pwRateMu.Unlock()
	if reset, ok := a.pwRateReset[ip]; ok && time.Now().After(reset) {
		delete(a.pwRateCount, ip)
		delete(a.pwRateReset, ip)
	}
	return a.pwRateCount[ip] >= 10
}

func (a *API) recordPWFailure(ip string) {
	a.pwRateMu.Lock()
	defer a.pwRateMu.Unlock()
	if _, ok := a.pwRateReset[ip]; !ok {
		a.pwRateReset[ip] = time.Now().Add(5 * time.Minute)
	}
	a.pwRateCount[ip]++
}

func (a *API) resetPWRateLimit(ip string) {
	a.pwRateMu.Lock()
	defer a.pwRateMu.Unlock()
	delete(a.pwRateCount, ip)
	delete(a.pwRateReset, ip)
}

// --- TOTP rate limiting (per IP:username) ---

func (a *API) checkTOTPRateLimit(key string) bool {
	a.totpRateMu.Lock()
	defer a.totpRateMu.Unlock()
	if reset, ok := a.totpRateReset[key]; ok && time.Now().After(reset) {
		delete(a.totpRateCount, key)
		delete(a.totpRateReset, key)
	}
	return a.totpRateCount[key] >= 5
}

func (a *API) recordTOTPFailure(key string) {
	a.totpRateMu.Lock()
	defer a.totpRateMu.Unlock()
	if _, ok := a.totpRateReset[key]; !ok {
		a.totpRateReset[key] = time.Now().Add(5 * time.Minute)
	}
	a.totpRateCount[key]++
}

func (a *API) resetTOTPRateLimit(key string) {
	a.totpRateMu.Lock()
	defer a.totpRateMu.Unlock()
	delete(a.totpRateCount, key)
	delete(a.totpRateReset, key)
}

// rateLimiterCleanup periodically purges expired rate limit entries to prevent memory leaks.
// Exits when stopCleanup channel is closed.
func (a *API) rateLimiterCleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.stopCleanup:
			return
		case <-ticker.C:
		}
		now := time.Now()

		a.pwRateMu.Lock()
		for ip, reset := range a.pwRateReset {
			if now.After(reset) {
				delete(a.pwRateCount, ip)
				delete(a.pwRateReset, ip)
			}
		}
		a.pwRateMu.Unlock()

		a.totpRateMu.Lock()
		for key, reset := range a.totpRateReset {
			if now.After(reset) {
				delete(a.totpRateCount, key)
				delete(a.totpRateReset, key)
			}
		}
		a.totpRateMu.Unlock()
	}
}

func (a *API) handleUsers(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	if r.Method == "GET" {
		users, err := a.store.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if users == nil {
			users = []auth.UserInfo{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(users)
		return
	}

	// POST: create user
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		PermConnect *bool  `json:"perm_connect"`
		PermAgent   *bool  `json:"perm_agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request JSON", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" {
		http.Error(w, "username and password required", http.StatusBadRequest)
		return
	}
	if len(req.Password) < minPasswordLength {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if !validRoles[req.Role] {
		http.Error(w, "invalid role: must be 'admin' or 'user'", http.StatusBadRequest)
		return
	}

	// Default both permissions to true.
	permConnect := true
	if req.PermConnect != nil {
		permConnect = *req.PermConnect
	}
	permAgent := true
	if req.PermAgent != nil {
		permAgent = *req.PermAgent
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "failed to hash password", http.StatusInternalServerError)
		return
	}

	user, err := a.store.CreateUser(r.Context(), req.Username, hash, req.Role, permConnect, permAgent)
	if err != nil {
		if errors.Is(err, auth.ErrDuplicateUser) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(user)
}

func (a *API) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	idStr := vars["id"]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "PATCH":
		var req struct {
			Role        *string `json:"role"`
			Password    *string `json:"password"`
			Disabled    *bool   `json:"disabled"`
			PermConnect *bool   `json:"perm_connect"`
			PermAgent   *bool   `json:"perm_agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request JSON", http.StatusBadRequest)
			return
		}

		if req.Role != nil && !validRoles[*req.Role] {
			http.Error(w, "invalid role: must be 'admin' or 'user'", http.StatusBadRequest)
			return
		}

		if req.Password != nil && len(*req.Password) < minPasswordLength {
			http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
			return
		}

		var pwHash *string
		if req.Password != nil {
			h, err := auth.HashPassword(*req.Password)
			if err != nil {
				http.Error(w, "failed to hash password", http.StatusInternalServerError)
				return
			}
			pwHash = &h
		}

		// Wrap all updates (fields + disable toggle) in a single call to maintain atomicity.
		if err := a.store.UpdateUserWithDisabled(r.Context(), id, req.Role, pwHash, req.PermConnect, req.PermAgent, req.Disabled); err != nil {
			if errors.Is(err, auth.ErrUserNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "updated"})

	case "DELETE":
		if err := a.store.DeleteUser(r.Context(), id); err != nil {
			if errors.Is(err, auth.ErrUserNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}
}

// ConnectedAgentInfo describes a single connected agent visible to the user.
type ConnectedAgentInfo struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
}

func (a *API) handleConnectedAgents(w http.ResponseWriter, r *http.Request) {
	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	isAdmin := auth.HasPermission(sessInfo.Role, auth.PermAdmin)

	agents := []ConnectedAgentInfo{}
	seen := make(map[string]bool)
	if a.reg != nil {
		a.reg.Range(func(s *server.Session) bool {
			if !isAdmin && s.UserID() != sessInfo.UserID {
				return true
			}
			if aid := s.AgentID(); aid != "" && !seen[aid] {
				seen[aid] = true
				agents = append(agents, ConnectedAgentInfo{
					AgentID:   aid,
					SessionID: s.ID(),
				})
			}
			return true
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(agents)
}

func (a *API) handleAgents(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	if r.Method == "GET" {
		configs, err := a.store.ListAgentConfigs(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if configs == nil {
			configs = []auth.AgentConfig{}
		}

		// Non-admin users see agent configs but with allowed_targets redacted
		// to avoid exposing internal routing topology.
		sessInfo := server.UserSessionFromContext(r)
		isAdmin := sessInfo != nil && auth.HasPermission(sessInfo.Role, auth.PermAdmin)
		if !isAdmin {
			for i := range configs {
				configs[i].AllowedTargets = nil
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(configs)
		return
	}

	// POST: create agent config
	var req struct {
		AgentID        string   `json:"agent_id"`
		Description    string   `json:"description"`
		AllowedTargets []string `json:"allowed_targets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request JSON", http.StatusBadRequest)
		return
	}
	if req.AgentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	cfg, err := a.store.CreateAgentConfig(r.Context(), req.AgentID, req.Description, req.AllowedTargets)
	if err != nil {
		if errors.Is(err, auth.ErrDuplicateAgentID) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(cfg)
}

func (a *API) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	idStr := vars["id"]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid agent config ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "PATCH":
		var req struct {
			AgentID        *string  `json:"agent_id"`
			Description    *string  `json:"description"`
			AllowedTargets []string `json:"allowed_targets"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request JSON", http.StatusBadRequest)
			return
		}

		cfg, err := a.store.UpdateAgentConfig(r.Context(), id, req.AgentID, req.Description, req.AllowedTargets)
		if err != nil {
			if errors.Is(err, auth.ErrDuplicateAgentID) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			if errors.Is(err, auth.ErrCannotRenameDefault) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfg)

	case "DELETE":
		if err := a.store.DeleteAgentConfig(r.Context(), id); err != nil {
			if errors.Is(err, auth.ErrCannotDeleteDefault) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if errors.Is(err, auth.ErrAgentNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"user_id":    sessInfo.UserID,
		"role":       sessInfo.Role,
		"expires_at": sessInfo.ExpiresAt,
	})
}

// --- TOTP Enrollment Endpoints ---

func (a *API) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	user, err := a.store.GetUserByID(r.Context(), sessInfo.UserID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	enrolled := user.TOTPSecret != ""
	remaining := 0
	if enrolled {
		remaining, _ = a.store.CountUnusedRecoveryCodes(r.Context(), user.ID)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"enrolled":               enrolled,
		"recovery_codes_remaining": remaining,
	})
}

func (a *API) handleTOTPBeginSetup(w http.ResponseWriter, r *http.Request) {
	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	user, err := a.store.GetUserByID(r.Context(), sessInfo.UserID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Check if user is already enrolled.
	if user.TOTPSecret != "" {
		http.Error(w, "TOTP is already enrolled; use DELETE /api/v1/totp first to re-enroll", http.StatusConflict)
		return
	}

	secret, keyURL, err := auth.GenerateTOTPSecret("ssetunnel", user.Username)
	if err != nil {
		http.Error(w, "failed to generate TOTP secret", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"secret":  secret,
		"key_url": keyURL,
	})
}

func (a *API) handleTOTPVerifySetup(w http.ResponseWriter, r *http.Request) {
	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	// Reject if user is already enrolled (must DELETE first to re-enroll).
	user, err := a.store.GetUserByID(r.Context(), sessInfo.UserID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if user.TOTPSecret != "" {
		http.Error(w, "TOTP is already enrolled; use DELETE /api/v1/totp first to re-enroll", http.StatusConflict)
		return
	}

	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request JSON", http.StatusBadRequest)
		return
	}
	if req.Secret == "" || req.Code == "" {
		http.Error(w, "secret and code are required", http.StatusBadRequest)
		return
	}

	if !auth.VerifyTOTP(req.Secret, req.Code) {
		http.Error(w, "invalid TOTP code", http.StatusBadRequest)
		return
	}

	// Generate recovery codes.
	const recoveryCodeCount = 8
	codes, err := auth.GenerateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		http.Error(w, "failed to generate recovery codes", http.StatusInternalServerError)
		return
	}

	digests := make([]string, len(codes))
	for i, c := range codes {
		digests[i] = a.store.RecoveryCodeDigest(c)
	}

	// Atomically persist TOTP secret + recovery codes in a single transaction.
	// Recovery codes are saved first so failure leaves user unenrolled, not half-enrolled.
	if err := a.store.SetTOTPSecretAndRecoveryCodes(r.Context(), sessInfo.UserID, req.Secret, digests); err != nil {
		http.Error(w, "failed to save TOTP setup", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"recovery_codes": codes,
	})
}

func (a *API) handleTOTPDelete(w http.ResponseWriter, r *http.Request) {
	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request JSON", http.StatusBadRequest)
		return
	}

	// Require password confirmation to disable TOTP.
	user, err := a.store.GetUserByID(r.Context(), sessInfo.UserID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if _, err := a.store.ValidatePassword(r.Context(), user.Username, req.Password); err != nil {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}

	// Atomically clear TOTP secret and recovery codes in a single transaction.
	if err := a.store.ClearTOTPAndRecoveryCodes(r.Context(), sessInfo.UserID); err != nil {
		http.Error(w, "failed to clear TOTP", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
}

// --- Metrics endpoints ---

// userScopedAgentIDs returns the set of agent IDs visible to the current user.
// Admins see all agents; non-admins see only agents from their own sessions.
func (a *API) userScopedAgentIDs(r *http.Request) (map[string]bool, bool) {
	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		return nil, false
	}
	isAdmin := auth.HasPermission(sessInfo.Role, auth.PermAdmin)

	agentIDs := make(map[string]bool)
	if a.reg != nil {
		a.reg.Range(func(s *server.Session) bool {
			if isAdmin || s.UserID() == sessInfo.UserID {
				if aid := s.AgentID(); aid != "" {
					agentIDs[aid] = true
				}
			}
			return true
		})
	}
	return agentIDs, true
}

func (a *API) handleMetricsOverview(w http.ResponseWriter, r *http.Request) {
	if a.mc == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metrics.Overview{})
		return
	}

	agentIDs, ok := a.userScopedAgentIDs(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	// Compute overview scoped to user-visible agents.
	overview := metrics.Overview{}
	if len(agentIDs) == 0 {
		// No visible agents; return empty overview.
	} else {
		overview.ActiveAgents = len(agentIDs)
		var errRateSum float64
		var errRateCount int
		for _, am := range a.mc.AllAgentMetrics() {
			if agentIDs[am.AgentID] {
				overview.ThroughputUpBps += am.Snapshot.ThroughputUpP50
				overview.ThroughputDnBps += am.Snapshot.ThroughputDnP50
				errRateSum += am.Snapshot.ErrorRate
				errRateCount++
			}
		}
		if errRateCount > 0 {
			overview.ErrorRate = errRateSum / float64(errRateCount)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(overview)
}

func (a *API) handleMetricsAgents(w http.ResponseWriter, r *http.Request) {
	if a.mc == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]metrics.AgentMetrics{})
		return
	}

	agentIDs, ok := a.userScopedAgentIDs(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	// Filter agent metrics to user-scoped agents
	allMetrics := a.mc.AllAgentMetrics()
	filtered := make([]metrics.AgentMetrics, 0, len(agentIDs))
	for _, am := range allMetrics {
		if agentIDs[am.AgentID] {
			filtered = append(filtered, am)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(filtered)
}

func (a *API) handleMetricsSamples(w http.ResponseWriter, r *http.Request) {
	if a.mc == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]metrics.MetricSample{})
		return
	}

	agentIDs, ok := a.userScopedAgentIDs(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	vars := mux.Vars(r)
	agentID := vars["agentID"]
	if !agentIDs[agentID] {
		http.Error(w, "agent not found or access denied", http.StatusNotFound)
		return
	}

	// Parse time range from query params (default: last 24 hours)
	now := time.Now()
	from := now.Add(-24 * time.Hour)
	to := now

	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			from = t
		}
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			to = t
		}
	}

	store := a.mc.Store()
	if store == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]metrics.MetricSample{})
		return
	}

	samples, err := store.QuerySamples(agentID, from, to)
	if err != nil {
		http.Error(w, "failed to query samples: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if samples == nil {
		samples = []metrics.MetricSample{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(samples)
}

func (a *API) handleMetricsDecisions(w http.ResponseWriter, r *http.Request) {
	if a.mc == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]metrics.TuningDecision{})
		return
	}

	agentIDs, ok := a.userScopedAgentIDs(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	vars := mux.Vars(r)
	agentID := vars["agentID"]
	if !agentIDs[agentID] {
		http.Error(w, "agent not found or access denied", http.StatusNotFound)
		return
	}

	// Parse limit from query param (default: 50)
	limit := 50
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	store := a.mc.Store()
	if store == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]metrics.TuningDecision{})
		return
	}

	decisions, err := store.QueryDecisions(agentID, limit)
	if err != nil {
		http.Error(w, "failed to query decisions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if decisions == nil {
		decisions = []metrics.TuningDecision{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(decisions)
}
