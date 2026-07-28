package consoleapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/server"
)

type API struct {
	store      *auth.Store
	reg        *server.Registry
	totpSecret string
}

func NewRouter(store *auth.Store, reg *server.Registry, totpSecret string) http.Handler {
	api := &API{
		store:      store,
		reg:        reg,
		totpSecret: totpSecret,
	}

	r := mux.NewRouter()

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

	// Verify TOTP if configured
	if a.totpSecret != "" {
		if !auth.VerifyTOTP(a.totpSecret, req.TOTPCode) {
			http.Error(w, "invalid TOTP code", http.StatusUnauthorized)
			return
		}
	}

	if a.store == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	user, err := a.store.ValidatePassword(r.Context(), req.Username, req.Password)
	if err != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if user.DisabledAt != nil {
		http.Error(w, "account disabled", http.StatusForbidden)
		return
	}

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
	if req.Role == "" {
		req.Role = "user"
	}
	if req.Role == "agent" {
		http.Error(w, "role 'agent' is not supported; use perm_agent toggle instead", http.StatusBadRequest)
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

		if req.Role != nil && *req.Role == "agent" {
			http.Error(w, "role 'agent' is not supported; use perm_agent toggle instead", http.StatusBadRequest)
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

		if err := a.store.UpdateUser(r.Context(), id, req.Role, pwHash, req.PermConnect, req.PermAgent); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if req.Disabled != nil {
			if *req.Disabled {
				if err := a.store.DisableUser(r.Context(), id); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			} else {
				if err := a.store.EnableUser(r.Context(), id); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
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
