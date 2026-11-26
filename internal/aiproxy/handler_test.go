package aiproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/sdk"
)

// TestSubmitWithoutClientRefused pins the admission gate: when no automation
// client is connected, a submit must not create a job that nothing can run.
func TestSubmitWithoutClientRefused(t *testing.T) {
	manager := NewJobManager(time.Minute, nil, nil, testModuleID)
	handler := NewClientHandler(manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(`{"prompt":"summarise"}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusServiceUnavailable, rec.Body)
	}
	var resp apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if resp.Error.Code != "NO_AUTOMATION_CLIENT" {
		t.Fatalf("error code = %q, want NO_AUTOMATION_CLIENT", resp.Error.Code)
	}

	manager.mu.Lock()
	remaining := len(manager.jobs)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("rejected submit left %d job(s) behind", remaining)
	}
}

// TestGetPendingWithoutClientReturnsImmediately pins the no-client fast path:
// a pending job no client can take must answer now, not hold the connection
// open for the whole wait budget.
func TestGetPendingWithoutClientReturnsImmediately(t *testing.T) {
	manager := NewJobManager(time.Minute, nil, nil, testModuleID)
	job, err := manager.Submit(context.Background(), AutomationRequest{Prompt: "summarise"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	start := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/jobs/"+job.ID+"?wait=true&wait_timeout=10s", nil)
	NewClientHandler(manager).ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("no-client GET blocked %v; a pending job cannot progress without a client", elapsed)
	}

	var view Job
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if view.Status != JobPending {
		t.Fatalf("status = %s, want pending", view.Status)
	}
}

// TestSubmitWithClientAccepted proves the admission gate rejects only the
// no-client case.
func TestSubmitWithClientAccepted(t *testing.T) {
	manager := NewJobManager(time.Minute, nil, nil, testModuleID)
	closed := manager.PollOpened()
	defer closed()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(`{"prompt":"summarise"}`))
	NewClientHandler(manager).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body)
	}
	var resp submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("accepted submit returned no job id")
	}
}

// TestResolveWaitTimeout pins the wait budget: unset, unparsable, and oversized
// values resolve to the cap, which stays below the deadline that bounds the
// module exchange.
func TestResolveWaitTimeout(t *testing.T) {
	if maxClientWaitTimeout <= 0 || maxClientWaitTimeout >= sdk.InvocationTimeout {
		t.Fatalf("wait budget %v must fit inside the %v invocation timeout",
			maxClientWaitTimeout, sdk.InvocationTimeout)
	}

	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", maxClientWaitTimeout},
		{"12s", 12 * time.Second},
		{"120s", maxClientWaitTimeout},
		{"0s", maxClientWaitTimeout},
		{"-5s", maxClientWaitTimeout},
		{"not-a-duration", maxClientWaitTimeout},
	}
	for _, tc := range cases {
		if got := resolveWaitTimeout(tc.raw); got != tc.want {
			t.Errorf("resolveWaitTimeout(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// TestPollThroughExtensionServerAdmitsSubmit pins the poll side of the presence
// wiring: the gate must open because the extension actually polled the module,
// not because a test set the manager's state directly.
func TestPollThroughExtensionServerAdmitsSubmit(t *testing.T) {
	manager := NewJobManager(time.Minute, nil, nil, testModuleID)
	client := NewClientHandler(manager)
	ext := NewExtensionServer(manager)

	// An empty queue answers 204 once the one-second wait is up; the poll
	// itself is what proves a client is present.
	rec := httptest.NewRecorder()
	ext.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/poll?timeout=1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("poll status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	rec = httptest.NewRecorder()
	client.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(`{"prompt":"summarise"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit after poll status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body)
	}
}

// TestResultThroughExtensionServerRestoresPresence pins the result side of the
// presence wiring: a client that stopped polling to run a job proves it is
// still there by posting its result.
func TestResultThroughExtensionServerRestoresPresence(t *testing.T) {
	manager := NewJobManager(time.Minute, nil, nil, testModuleID)
	client := NewClientHandler(manager)
	ext := NewExtensionServer(manager)

	// Push the last contact past the grace window so only a real /result post
	// can reopen the gate.
	manager.mu.Lock()
	manager.lastClientAt = time.Now().Add(-2 * clientGrace)
	manager.mu.Unlock()

	rec := httptest.NewRecorder()
	body := `{"job_id":"` + strings.Repeat("0", jobIDLength) + `","result":{"status":"ok","text":"done"}}`
	ext.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/result", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	client.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(`{"prompt":"summarise"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit after result status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body)
	}
}
