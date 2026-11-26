package aiproxy

import (
	"context"
	"testing"
	"time"
)

// TestMarkRunningReportsStatus pins the transition result and the status a
// dropped job reports, so a delivery miss leaves a trace.
func TestMarkRunningReportsStatus(t *testing.T) {
	manager := NewJobManager(time.Minute, nil, nil, testModuleID)
	job, err := manager.Submit(context.Background(), AutomationRequest{Prompt: "summarise"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if ok, status := manager.MarkRunning(job.ID); !ok || status != JobPending {
		t.Fatalf("first transition = (%v, %s), want (true, pending)", ok, status)
	}
	if ok, status := manager.MarkRunning(job.ID); ok || status != JobRunning {
		t.Fatalf("second transition = (%v, %s), want (false, running)", ok, status)
	}

	manager.Complete(job.ID, &AutomationResult{Status: "ok"})
	if ok, status := manager.MarkRunning(job.ID); ok || status != JobCompleted {
		t.Fatalf("terminal transition = (%v, %s), want (false, completed)", ok, status)
	}
}

// TestHasClientTracksExtensionContacts pins the presence rule: an open poll
// counts, a recent contact keeps counting, an aged contact does not, and a
// running job proves the client is alive even though it stops polling while the
// automation runs.
func TestHasClientTracksExtensionContacts(t *testing.T) {
	manager := NewJobManager(time.Minute, nil, nil, testModuleID)
	if manager.HasClient() {
		t.Fatal("a fresh manager must report no client")
	}

	closed := manager.PollOpened()
	if !manager.HasClient() {
		t.Fatal("an open poll must count as a connected client")
	}
	closed()
	if !manager.HasClient() {
		t.Fatal("a client inside the grace window must stay connected")
	}

	ageClient := func() {
		manager.mu.Lock()
		manager.lastClientAt = time.Now().Add(-2 * clientGrace)
		manager.mu.Unlock()
	}
	ageClient()
	if manager.HasClient() {
		t.Fatal("a client past the grace window must not count")
	}

	manager.ClientContacted()
	if !manager.HasClient() {
		t.Fatal("a posted result must count as a contact")
	}

	ageClient()
	job, err := manager.Submit(context.Background(), AutomationRequest{Prompt: "run"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ok, status := manager.MarkRunning(job.ID); !ok {
		t.Fatalf("mark running = (%v, %s), want true", ok, status)
	}
	if !manager.HasClient() {
		t.Fatal("a running job must count as a connected client")
	}
}
