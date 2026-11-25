package aiproxy

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

// fileUnavailableMessage is what a client sees when a job's stored attachment
// cannot be read. The detail, including the object key, stays in the server log.
const fileUnavailableMessage = "file attachment is unavailable; resubmit the job"

// ExtensionServer serves HTTP long-polling endpoints for the WXT browser
// extension. The extension connects to this server to receive pending
// automation jobs and return results.
//
// Endpoints:
//   - GET /poll — blocks until a job is available or timeout
//   - POST /result — extension submits the automation result
type ExtensionServer struct {
	jobs   *JobManager
	server *http.Server
}

// NewExtensionServer creates an extension long-polling server backed by
// the given job manager.
func NewExtensionServer(jobs *JobManager) *ExtensionServer {
	s := &ExtensionServer{jobs: jobs}
	mux := http.NewServeMux()
	mux.HandleFunc("/poll", s.handlePoll)
	mux.HandleFunc("/result", s.handleResult)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	})
	s.server = &http.Server{Handler: mux}
	return s
}

// ListenAndServe starts the extension server on the given address.
func (s *ExtensionServer) ListenAndServe(addr string) error {
	s.server.Addr = addr
	log.Printf("aiproxy: extension server listening on %s", addr)
	return s.server.ListenAndServe()
}

// Serve starts the extension server on the given listener.
func (s *ExtensionServer) Serve(ln net.Listener) error {
	return s.server.Serve(ln)
}

// Shutdown gracefully stops the extension server.
func (s *ExtensionServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// pollRequest carries a pending job to the extension.
type pollResponse struct {
	JobID   string            `json:"job_id"`
	Request AutomationRequest `json:"request"`
}

func (s *ExtensionServer) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required")
		return
	}

	// Parse timeout (default 30s).
	timeout := 30 * time.Second
	if v := r.URL.Query().Get("timeout"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}

	// Wait for a pending job or timeout.
	select {
	case job, ok := <-s.jobs.PendingChan():
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "job queue closed")
			return
		}
		// If client already disconnected, requeue immediately.
		if r.Context().Err() != nil {
			s.jobs.Requeue(job.ID)
			return
		}
		// Restore the attachment bytes before the job leaves the process. A job
		// whose bytes cannot be read must fail here, not reach the extension.
		req, err := s.jobs.Hydrate(r.Context(), job.Request)
		if err != nil {
			if r.Context().Err() != nil {
				s.jobs.Requeue(job.ID)
				return
			}
			log.Printf("aiproxy: job %s attachment unavailable: %v", job.ID, err)
			s.jobs.Complete(job.ID, &AutomationResult{
				Status: "error",
				Error: &AutomationError{
					Code:    "FILE_UNAVAILABLE",
					Message: fileUnavailableMessage,
				},
			})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Mark the job as running before sending to the extension. A job that
		// timed out during hydration is already terminal and is dropped here.
		if ok, status := s.jobs.MarkRunning(job.ID); !ok {
			log.Printf("aiproxy: job %s not delivered (status=%s)", job.ID, status)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, pollResponse{
			JobID:   job.ID,
			Request: req,
		})
	case <-time.After(timeout):
		// No job available — return empty response with 204 No Content.
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
		// Client disconnected.
		w.WriteHeader(http.StatusNoContent)
	}
}

// resultRequest is the JSON body for POST /result.
type resultRequest struct {
	JobID  string            `json:"job_id"`
	Result *AutomationResult `json:"result"`
}

func (s *ExtensionServer) handleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
		return
	}
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "request body required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyBodyBytes)

	var req resultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if req.JobID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "job_id required")
		return
	}
	if req.Result == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "result required")
		return
	}

	s.jobs.Complete(req.JobID, req.Result)
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}
