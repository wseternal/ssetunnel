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
