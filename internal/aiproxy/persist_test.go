package aiproxy

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSnapshotJob_RedactsAttachment verifies that a stored snapshot drops the
// base64 payload without touching the live request the manager still owns.
func TestSnapshotJob_RedactsAttachment(t *testing.T) {
	live := &Job{
		ID:     "job-1",
		Status: JobCompleted,
		Request: AutomationRequest{
			Prompt: "analyse",
			File:   &FilePayload{Name: "a.png", MimeType: "image/png", DataBase64: "QUJD", ByteLength: 3},
		},
		CreatedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 20, 10, 1, 0, 0, time.UTC),
	}

	snap := snapshotJob(live)

	if snap.Request.File == nil {
		t.Fatal("snapshot dropped the file metadata entirely")
	}
	if snap.Request.File.DataBase64 != "" {
		t.Errorf("snapshot kept attachment bytes: %q", snap.Request.File.DataBase64)
	}
	if snap.Request.File.ByteLength != 3 || snap.Request.File.Name != "a.png" {
		t.Errorf("snapshot lost file metadata: %+v", snap.Request.File)
	}
	if live.Request.File.DataBase64 != "QUJD" {
		t.Errorf("snapshot mutated the live request: %q", live.Request.File.DataBase64)
	}
	if !snap.CreatedAt.Equal(live.CreatedAt) || !snap.UpdatedAt.Equal(live.UpdatedAt) {
		t.Errorf("timestamps changed: %v / %v", snap.CreatedAt, snap.UpdatedAt)
	}
}

// TestSnapshotJob_NoFile keeps the nil-file path honest.
func TestSnapshotJob_NoFile(t *testing.T) {
	snap := snapshotJob(&Job{ID: "job-2", Status: JobRunning, Request: AutomationRequest{Prompt: "hi"}})
	if snap.Request.File != nil {
		t.Errorf("expected nil file, got %+v", snap.Request.File)
	}
	if snap.ID != "job-2" || snap.Status != JobRunning {
		t.Errorf("identity changed: %+v", snap)
	}
}

// TestDecodeStoredJob_TerminalRoundTrip proves a completed row survives the
// JSON round trip with its result and a closed done channel.
func TestDecodeStoredJob_TerminalRoundTrip(t *testing.T) {
	stored := persistedJob{
		ID:      "job-3",
		Status:  JobCompleted,
		Request: AutomationRequest{Prompt: "summarise", ProviderID: "p1"},
		Result:  &AutomationResult{Status: "ok", Text: "done"},
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	job, ok := decodeStoredJob("ignored", raw)
	if !ok {
		t.Fatal("expected decode to succeed")
	}
	if job.ID != "job-3" || job.Status != JobCompleted {
		t.Errorf("identity changed: %+v", job)
	}
	if job.Result == nil || job.Result.Text != "done" {
		t.Errorf("result lost: %+v", job.Result)
	}
	select {
	case <-job.doneCh:
	default:
		t.Error("doneCh must be closed for a terminal job")
	}
}

// TestDecodeStoredJob_NonTerminalBecomesAbandoned covers the crash-recovery
// rule: a row left "running" by a dead process must be reported as failed.
func TestDecodeStoredJob_NonTerminalBecomesAbandoned(t *testing.T) {
	for _, status := range []JobStatus{JobPending, JobRunning} {
		raw, err := json.Marshal(persistedJob{ID: "job-4", Status: status})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		job, ok := decodeStoredJob("job-4", raw)
		if !ok {
			t.Fatalf("%s: expected decode to succeed", status)
		}
		if job.Status != JobFailed {
			t.Errorf("%s: status = %q, want failed", status, job.Status)
		}
		if job.Result == nil || job.Result.Error == nil || job.Result.Error.Code != "ABANDONED" {
			t.Errorf("%s: expected ABANDONED error, got %+v", status, job.Result)
		}
		select {
		case <-job.doneCh:
		default:
			t.Errorf("%s: abandoned job must be terminal on doneCh", status)
		}
	}
}

// TestDecodeStoredJob_FillsMissingID keeps the row id authoritative when the
// snapshot did not carry one.
func TestDecodeStoredJob_FillsMissingID(t *testing.T) {
	raw, err := json.Marshal(persistedJob{Status: JobFailed})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	job, ok := decodeStoredJob("row-id", raw)
	if !ok {
		t.Fatal("expected decode to succeed")
	}
	if job.ID != "row-id" {
		t.Errorf("id = %q, want row-id", job.ID)
	}
}

// TestDecodeStoredJob_MalformedRow reports false instead of a half-built job.
func TestDecodeStoredJob_MalformedRow(t *testing.T) {
	if job, ok := decodeStoredJob("job-5", []byte("{not json")); ok || job != nil {
		t.Errorf("expected (nil,false), got (%+v,%v)", job, ok)
	}
}
