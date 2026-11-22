package aiproxy

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/wseternal/ssetunnel/sdk"
)

const (
	// persistTimeout bounds a single write. The SDK applies the deadline to
	// the IPC connection, so a stalled server cannot hold a job transition open.
	persistTimeout = 10 * time.Second

	// loadTimeout bounds a history read on the request path.
	loadTimeout = 5 * time.Second
)

// persistedJob is the stored shape of a job. doneCh has no meaning outside the
// process, and attachment bytes are dropped by snapshotJob, so the row stays
// small enough to keep indefinitely.
type persistedJob struct {
	ID        string            `json:"id"`
	Status    JobStatus         `json:"status"`
	Request   AutomationRequest `json:"request"`
	Result    *AutomationResult `json:"result,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// snapshotJob copies a job into its stored shape. Callers hold the manager
// lock: the returned value must not observe later mutations.
//
// The attachment payload is redacted: history should not duplicate multi-megabyte
// base64 blobs, and the bytes are only useful while the job is in flight.
func snapshotJob(job *Job) persistedJob {
	req := job.Request
	if req.File != nil {
		file := *req.File
		file.DataBase64 = ""
		req.File = &file
	}
	return persistedJob{
		ID:        job.ID,
		Status:    job.Status,
		Request:   req,
		Result:    job.Result,
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
	}
}

// saveJob writes one snapshot through the tunnel-backed data store. Failures
// are logged and dropped: persistence is best effort and never changes a job's
// outcome.
func saveJob(ctx context.Context, store *sdk.DataStore, moduleID string, snap persistedJob) {
	data, err := json.Marshal(snap)
	if err != nil {
		log.Printf("aiproxy: persist job %s: marshal: %v", snap.ID, err)
		return
	}
	if err := store.Create(ctx, moduleID, snap.ID, data); err != nil {
		log.Printf("aiproxy: persist job %s: %v", snap.ID, err)
	}
}

// loadJob reads a job back from the data store. Reports false for a missing
// record and for any store failure, which is indistinguishable at this layer.
//
// A stored status that is not terminal can only come from a previous process
// that stopped mid-job; it is reported as abandoned rather than left "running"
// forever.
func loadJob(ctx context.Context, store *sdk.DataStore, moduleID, id string) (*Job, bool) {
	record, err := store.Get(ctx, moduleID, id)
	if err != nil || record == nil {
		return nil, false
	}

	var snap persistedJob
	if err := json.Unmarshal(record.Data, &snap); err != nil {
		log.Printf("aiproxy: load job %s: decode: %v", id, err)
		return nil, false
	}
	if snap.ID == "" {
		snap.ID = id
	}

	done := make(chan struct{})
	close(done)
	job := &Job{
		ID:        snap.ID,
		Status:    snap.Status,
		Request:   snap.Request,
		Result:    snap.Result,
		CreatedAt: snap.CreatedAt,
		UpdatedAt: snap.UpdatedAt,
		doneCh:    done,
	}
	if !isTerminal(job.Status) {
		job.Status = JobFailed
		job.Result = &AutomationResult{
			Status: "error",
			Error: &AutomationError{
				Code:    "ABANDONED",
				Message: "job did not finish before the module stopped",
			},
		}
	}
	return job, true
}
