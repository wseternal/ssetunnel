package aiproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/wseternal/ssetunnel/sdk"
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
		writeJSON(w, http.StatusOK, map[string]any{
			"status":              "healthy",
			"extension_connected": h.jobs.HasClient(),
		})
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

// maxProxyBodyBytes caps one client or extension request body. It covers the
// base64 form of a 4 MiB attachment (5.33 MiB) plus the JSON envelope; a
// smaller cap would make the documented file limit unreachable.
const maxProxyBodyBytes = 6 << 20

// maxClientWaitTimeout caps how long a client can hold a GET /jobs/{id}
// connection. The agent aborts a module exchange after sdk.InvocationTimeout,
// so a longer wait would be cut off with no response at all. The headroom
// covers the request read and the data-store lookup that can precede the wait.
const maxClientWaitTimeout = sdk.InvocationTimeout - 10*time.Second

func (h *ClientHandler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "request body required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyBodyBytes)

	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// A body over the cap reaches the decoder as a MaxBytesError. Report it
		// as the size failure it is, not as malformed JSON.
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE",
				fmt.Sprintf("request body exceeds %d bytes", maxProxyBodyBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "prompt is required")
		return
	}
	// No automation client means no path to completion: queueing the job would
	// only make the caller poll a job that can never leave pending.
	if !h.jobs.HasClient() {
		log.Printf("aiproxy: submit rejected: no automation client connected")
		writeError(w, http.StatusServiceUnavailable, "NO_AUTOMATION_CLIENT",
			"no automation client is connected; start the automation extension and retry")
		return
	}

	job, err := h.jobs.Submit(r.Context(), AutomationRequest{
		Prompt:     req.Prompt,
		File:       req.File,
		ProviderID: req.ProviderID,
		TimeoutMs:  req.TimeoutMs,
		TargetURL:  req.TargetURL,
	})
	if err != nil {
		writeSubmitError(w, err)
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
	view := h.jobs.View(job)

	// Block until the job is done or the wait budget expires. A pending job
	// cannot progress while no automation client is connected, so report its
	// state now instead of pinning the connection.
	if r.URL.Query().Get("wait") == "true" && (view.Status != JobPending || h.jobs.HasClient()) {
		waitTimeout := resolveWaitTimeout(r.URL.Query().Get("wait_timeout"))
		view = h.jobs.View(h.jobs.WaitForResult(job, waitTimeout))
	}

	writeJSON(w, http.StatusOK, view)
}

// resolveWaitTimeout turns the optional wait_timeout query value into the wait
// budget. It defaults to and never exceeds maxClientWaitTimeout, so the wait
// always leaves room for the module exchange to finish.
func resolveWaitTimeout(raw string) time.Duration {
	if d, err := time.ParseDuration(raw); err == nil && d > 0 && d < maxClientWaitTimeout {
		return d
	}
	return maxClientWaitTimeout
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

// writeSubmitError maps an attachment failure to its client status. A submit
// fails before a job exists, so the caller learns the reason instead of polling
// a job that never had usable bytes.
func writeSubmitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidFile):
		writeError(w, http.StatusBadRequest, "INVALID_FILE", err.Error())
	case errors.Is(err, errFileTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", err.Error())
	case errors.Is(err, errFileStoreUnavailable):
		writeError(w, http.StatusServiceUnavailable, "FILE_STORE_UNAVAILABLE", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
