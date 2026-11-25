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
