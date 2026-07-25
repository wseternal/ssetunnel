package consoleapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/skip2/go-qrcode"
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
	r.HandleFunc("/api/v1/login", api.handleLogin).Methods("POST")
	r.HandleFunc("/api/v1/user-login", api.handleUserLogin).Methods("POST")
	r.HandleFunc("/api/v1/logout", api.handleLogout).Methods("POST")

	// Protected admin routes
	adminAuth := server.AdminSessionMiddleware(store)

	r.Handle("/api/v1/tokens", adminAuth(http.HandlerFunc(api.handleTokens))).Methods("GET", "POST")
	r.Handle("/api/v1/tokens/{id}", adminAuth(http.HandlerFunc(api.handleRevokeToken))).Methods("DELETE")
	r.Handle("/api/v1/enroll", adminAuth(http.HandlerFunc(api.handleEnroll))).Methods("POST")
	r.Handle("/api/v1/sessions", adminAuth(http.HandlerFunc(api.handleSessions))).Methods("GET")

	// User management routes (admin only)
	r.Handle("/api/v1/users", adminAuth(http.HandlerFunc(api.handleUsers))).Methods("GET", "POST")
	r.Handle("/api/v1/users/{id}", adminAuth(http.HandlerFunc(api.handleUserUpdate))).Methods("PATCH", "DELETE")

	// User session routes (authenticated user)
	userAuth := server.UserSessionMiddleware(store)
	r.Handle("/api/v1/me", userAuth(http.HandlerFunc(api.handleMe))).Methods("GET")

	return r
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TOTPCode string `json:"totp_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request JSON", http.StatusBadRequest)
		return
	}

	if a.totpSecret == "" {
		http.Error(w, "TOTP not configured", http.StatusServiceUnavailable)
		return
	}

	if !auth.VerifyTOTP(a.totpSecret, req.TOTPCode) {
		http.Error(w, "invalid TOTP code", http.StatusUnauthorized)
		return
	}

	sessionToken, err := auth.GenerateToken()
	if err != nil {
		http.Error(w, "failed to generate session", http.StatusInternalServerError)
		return
	}

	if a.store != nil {
		if err := a.store.CreateAdminSession(r.Context(), sessionToken, 12*time.Hour); err != nil {
			http.Error(w, "failed to store session", http.StatusInternalServerError)
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     server.SessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		Expires:  time.Now().Add(12 * time.Hour),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
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

func (a *API) handleTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		if a.store == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]auth.TokenInfo{})
			return
		}
		tokens, err := a.store.ListTokens(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tokens == nil {
			tokens = []auth.TokenInfo{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokens)
		return
	}

	if r.Method == "POST" {
		var req struct {
			Role        string `json:"role"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Role == "" {
			req.Role = "agent"
		}

		rawToken, err := auth.GenerateToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if a.store != nil {
			if err := a.store.CreateToken(r.Context(), rawToken, req.Role, req.Description, nil); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "created",
			"token":       rawToken,
			"role":        req.Role,
			"description": req.Description,
		})
		return
	}
}

func (a *API) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid token ID", http.StatusBadRequest)
		return
	}

	if a.store != nil {
		if err := a.store.RevokeTokenByID(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
}

func (a *API) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Role == "" {
		req.Role = "agent"
	}

	pinStr, err := auth.GeneratePIN()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if a.store != nil {
		if err := a.store.CreatePIN(r.Context(), pinStr, req.Role, 30*time.Minute); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var qrBase64 string
	if a.totpSecret != "" {
		keyURL := "otpauth://totp/ssetunnel:admin?secret=" + a.totpSecret + "&issuer=ssetunnel"
		pngBytes, err := qrcode.Encode(keyURL, qrcode.Medium, 256)
		if err == nil {
			qrBase64 = "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"pin":            pinStr,
		"totp_secret":    a.totpSecret,
		"qr_code_base64": qrBase64,
	})
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
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
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

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "failed to hash password", http.StatusInternalServerError)
		return
	}

	user, err := a.store.CreateUser(r.Context(), req.Username, hash, req.Role)
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
			Role     *string `json:"role"`
			Password *string `json:"password"`
			Disabled *bool   `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request JSON", http.StatusBadRequest)
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

		if err := a.store.UpdateUser(r.Context(), id, req.Role, pwHash); err != nil {
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
