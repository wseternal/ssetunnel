package consoleapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/server"
)

// minPasswordLength is the minimum allowed password length for user creation and updates.
const minPasswordLength = 8

// validRoles is the allowlist of valid user roles.
var validRoles = map[string]bool{"admin": true, "user": true}

type API struct {
	store      *auth.Store
	reg        *server.Registry
	totpSecret string

	// Login rate limiting: track failed attempts per IP.
	rateMu    sync.Mutex
	rateCount map[string]int
	rateReset map[string]time.Time
}

func NewRouter(store *auth.Store, reg *server.Registry, totpSecret string) http.Handler {
	api := &API{
		store:      store,
		reg:        reg,
		totpSecret: totpSecret,
		rateCount:  make(map[string]int),
		rateReset:  make(map[string]time.Time),
	}

	r := mux.NewRouter()

	// CORS middleware — restrict to same-origin by default.
	r.Use(corsMiddleware)

	// Public auth routes
	r.HandleFunc("/api/v1/user-login", api.handleUserLogin).Methods("POST")
	r.HandleFunc("/api/v1/logout", api.handleLogout).Methods("POST")

	// Protected admin routes
	adminAuth := server.AdminSessionMiddleware(store)

	r.Handle("/api/v1/sessions", adminAuth(http.HandlerFunc(api.handleSessions))).Methods("GET")

	// User management routes (admin only)
	r.Handle("/api/v1/users", adminAuth(http.HandlerFunc(api.handleUsers))).Methods("GET", "POST")
	r.Handle("/api/v1/users/{id}", adminAuth(http.HandlerFunc(api.handleUserUpdate))).Methods("PATCH", "DELETE")

	// Agent config management routes (admin only)
	r.Handle("/api/v1/agents", adminAuth(http.HandlerFunc(api.handleAgents))).Methods("GET", "POST")
	r.Handle("/api/v1/agents/{id}", adminAuth(http.HandlerFunc(api.handleAgentUpdate))).Methods("PATCH", "DELETE")

	// User session routes (authenticated user)
	userAuth := server.UserSessionMiddleware(store)
	r.Handle("/api/v1/me", userAuth(http.HandlerFunc(api.handleMe))).Methods("GET")

	return r
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
	sessions := []SessionInfo{}

	if a.reg != nil {
		a.reg.Range(func(s *server.Session) bool {
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

	// Rate limiting: max 10 failed attempts per IP per 5-minute window.
	clientIP := r.RemoteAddr
	if a.checkRateLimit(clientIP) {
		http.Error(w, "too many failed login attempts, try again later", http.StatusTooManyRequests)
		return
	}

	if a.store == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	// Validate password first (before TOTP) to avoid leaking TOTP validity.
	user, err := a.store.ValidatePassword(r.Context(), req.Username, req.Password)
	if err != nil {
		a.recordFailedLogin(clientIP)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if user.DisabledAt != nil {
		http.Error(w, "account disabled", http.StatusForbidden)
		return
	}

	// Verify TOTP after successful password validation.
	if a.totpSecret != "" {
		if !auth.VerifyTOTP(a.totpSecret, req.TOTPCode) {
			a.recordFailedLogin(clientIP)
			http.Error(w, "invalid TOTP code", http.StatusUnauthorized)
			return
		}
	}

	// Successful login resets the rate limit for this IP.
	a.resetRateLimit(clientIP)

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
		"status":   "ok",
		"token":    sessionToken,
		"user_id":  user.ID,
		"username": user.Username,
		"role":     user.Role,
	})
}

// checkRateLimit returns true if the client IP has exceeded the login attempt limit.
func (a *API) checkRateLimit(ip string) bool {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	if reset, ok := a.rateReset[ip]; ok && time.Now().After(reset) {
		delete(a.rateCount, ip)
		delete(a.rateReset, ip)
	}
	return a.rateCount[ip] >= 10
}

// recordFailedLogin increments the failed login counter for an IP.
func (a *API) recordFailedLogin(ip string) {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	if _, ok := a.rateReset[ip]; !ok {
		a.rateReset[ip] = time.Now().Add(5 * time.Minute)
	}
	a.rateCount[ip]++
}

// resetRateLimit clears the rate limit for an IP on successful login.
func (a *API) resetRateLimit(ip string) {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	delete(a.rateCount, ip)
	delete(a.rateReset, ip)
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
