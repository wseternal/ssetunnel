package aiproxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// ClientHandler serves the client-facing API endpoints through the SDK
// invocation handler. Clients submit jobs and poll for results via these
// endpoints, which are reachable through the ssetunnel tunnel.
type ClientHandler struct {
	jobs *JobManager
}

// NewClientHandler creates a client API handler backed by the given
// job manager.
func NewClientHandler(jobs *JobManager) *ClientHandler {
	return &ClientHandler{jobs: jobs}
}

// ServeHTTP routes client API requests.
func (h *ClientHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/jobs":
		h.handleSubmit(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/jobs/"):
		h.handleGet(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	default:
		http.NotFound(w, r)
	}
}

// submitRequest is the JSON body for POST /jobs.
type submitRequest struct {
	Prompt     string       `json:"prompt"`
	File       *FilePayload `json:"file,omitempty"`
	ProviderID string       `json:"provider_id,omitempty"`
	TimeoutMs  int          `json:"timeout_ms,omitempty"`
	TargetURL  string       `json:"target_url,omitempty"`
}

// submitResponse is the JSON response for POST /jobs.
type submitResponse struct {
	JobID string `json:"job_id"`
}

const maxProxyBodyBytes = 4 << 20 // 4 MiB

// maxClientWaitTimeout caps how long a client can hold a connection
// waiting for a job result (W3 fix: prevent indefinite connection pinning).
const maxClientWaitTimeout = 5 * time.Minute

func (h *ClientHandler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "request body required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyBodyBytes)

	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "prompt is required")
		return
	}

	job, err := h.jobs.Submit(AutomationRequest{
		Prompt:     req.Prompt,
		File:       req.File,
		ProviderID: req.ProviderID,
		TimeoutMs:  req.TimeoutMs,
		TargetURL:  req.TargetURL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, submitResponse{JobID: job.ID})
}

func (h *ClientHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/jobs/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "job ID required")
		return
	}

	job := h.jobs.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "job not found")
		return
	}

	// If the client passes ?wait=true, block until the job is done
	// or the wait timeout expires.
	if r.URL.Query().Get("wait") == "true" {
		waitTimeout := 60 * time.Second
		if v := r.URL.Query().Get("wait_timeout"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				waitTimeout = d
			}
		}
		if waitTimeout > maxClientWaitTimeout {
			waitTimeout = maxClientWaitTimeout
		}
		job = h.jobs.WaitForResult(job, waitTimeout)
	}

	writeJSON(w, http.StatusOK, job)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// apiError is an RFC 7807-style error response.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var resp apiError
	resp.Error.Code = code
	resp.Error.Message = message
	writeJSON(w, status, resp)
}
