package consoleapi

import (
	"encoding/base64"
	"encoding/json"
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

	// Public auth route
	r.HandleFunc("/api/v1/login", api.handleLogin).Methods("POST")
	r.HandleFunc("/api/v1/logout", api.handleLogout).Methods("POST")

	// Protected admin routes
	adminAuth := server.AdminSessionMiddleware(store)

	r.Handle("/api/v1/tokens", adminAuth(http.HandlerFunc(api.handleTokens))).Methods("GET", "POST")
	r.Handle("/api/v1/tokens/{id}", adminAuth(http.HandlerFunc(api.handleRevokeToken))).Methods("DELETE")
	r.Handle("/api/v1/enroll", adminAuth(http.HandlerFunc(api.handleEnroll))).Methods("POST")
	r.Handle("/api/v1/sessions", adminAuth(http.HandlerFunc(api.handleSessions))).Methods("GET")

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

	if a.totpSecret != "" {
		if !auth.VerifyTOTP(a.totpSecret, req.TOTPCode) {
			http.Error(w, "invalid TOTP code", http.StatusUnauthorized)
			return
		}
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
