// Package aiproxy implements the AI automation proxy module that bridges
// external client requests to the gemini-automation-extension WXT browser
// extension via HTTP long polling.
package aiproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/wseternal/ssetunnel/sdk"
)

// JobStatus represents the lifecycle state of an automation job.
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobTimeout   JobStatus = "timeout"
)

// AutomationRequest is the client-submitted job payload.
type AutomationRequest struct {
	Prompt     string       `json:"prompt"`
	File       *FilePayload `json:"file,omitempty"`
	ProviderID string       `json:"provider_id,omitempty"`
	TimeoutMs  int          `json:"timeout_ms,omitempty"`
	TargetURL  string       `json:"target_url,omitempty"`
}

// FilePayload is a file attached to the automation request. A submit supplies
// the bytes as base64 and leaves the reference fields empty. A stored job keeps
// only the reference: object_key names the bytes in the module object store,
// sha256 names their content, and encoding says whether the stored object is
// gzip-compressed. The extension poll restores data_base64 from that object, so
// the extension sees the original shape.
type FilePayload struct {
	Name       string `json:"name"`
	MimeType   string `json:"mime_type"`
	DataBase64 string `json:"data_base64,omitempty"`
	ByteLength int    `json:"byte_length"`
	ObjectKey  string `json:"object_key,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Encoding   string `json:"encoding,omitempty"`
}

// AutomationResult is the extension-returned job result.
type AutomationResult struct {
	Status   string           `json:"status"` // "ok" or "error"
	Text     string           `json:"text,omitempty"`
	Markdown string           `json:"markdown,omitempty"`
	Error    *AutomationError `json:"error,omitempty"`
	Meta     json.RawMessage  `json:"meta,omitempty"`
}

// AutomationError describes a failure from the extension.
type AutomationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Step    string `json:"step,omitempty"`
}

// Job represents one automation job in the system.
type Job struct {
	ID        string            `json:"id"`
	Status    JobStatus         `json:"status"`
	Request   AutomationRequest `json:"request"`
	Result    *AutomationResult `json:"result,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`

	// doneCh is closed when the job reaches a terminal state.
	// Clients waiting for the result select on this channel.
	doneCh chan struct{}
}

// JobManager manages the job lifecycle: submission, dispatch to the
// extension, result correlation, and timeout enforcement.
type JobManager struct {
	mu           sync.Mutex
	jobs         map[string]*Job
	pending      chan *Job
	timeout      time.Duration
	retentionTTL time.Duration // how long to keep terminal jobs before eviction

	// store persists terminal jobs so history outlives the in-memory
	// retention window and process restarts. Nil means memory-only.
	store *sdk.DataStore

	// objects holds attachment bytes outside the job record, so a multi-megabyte
	// file never lives in the in-memory job or the persisted row. Nil rejects
	// submissions that carry a file.
	objects sdk.ObjectStore

	moduleID string

	persistWG sync.WaitGroup
	closing   bool
}

// NewJobManager creates a job manager with the given default timeout.
// A nil store keeps terminal job history in memory only, which is what happens
// when the data store is not reachable at startup. A nil objects keeps the
// module usable for prompt-only jobs; a submit that carries a file is rejected
// because the bytes have nowhere to go.
func NewJobManager(timeout time.Duration, store *sdk.DataStore, objects sdk.ObjectStore, moduleID string) *JobManager {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &JobManager{
		jobs:         make(map[string]*Job),
		pending:      make(chan *Job, 256),
		timeout:      timeout,
		retentionTTL: 10 * time.Minute,
		store:        store,
		objects:      objects,
		moduleID:     moduleID,
	}
}

// Close waits for in-flight persistence writes. New writes are rejected once
// closing starts, so this is safe to call while timeout goroutines still run.
// The wait is bounded: writes serialize on one IPC connection, so an unreachable
// server would otherwise multiply the timeout by the number of pending writes
// and stall module shutdown.
func (m *JobManager) Close() {
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		m.persistWG.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(closeGrace):
		log.Printf("aiproxy: closing with persistence writes still in flight")
	}
}

// schedulePersist captures the job's current state and registers the write
// with the wait group. Callers must hold m.mu; the returned snapshot is written
// by writeSnapshot after the lock is released.
func (m *JobManager) schedulePersist(job *Job) *persistedJob {
	if m.store == nil || m.closing {
		return nil
	}
	snap := snapshotJob(job)
	m.persistWG.Add(1)
	return &snap
}

// writeSnapshot performs the store write off the caller's goroutine. Only
// terminal states are persisted, so a job produces at most one write and
// ordering between writes for the same id cannot invert.
func (m *JobManager) writeSnapshot(snap *persistedJob) {
	if snap == nil {
		return
	}
	go func() {
		defer m.persistWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
		defer cancel()
		saveJob(ctx, m.store, m.moduleID, *snap)
	}()
}

// storeFile moves the attachment bytes into the object store and returns the
// reference the job keeps. A missing store or a failed put rejects the
// submission: the module has no memory-only path for file bytes.
func (m *JobManager) storeFile(ctx context.Context, file *FilePayload) (*FilePayload, error) {
	if m.objects == nil {
		return nil, fmt.Errorf("%w: object store is not configured", errFileStoreUnavailable)
	}
	data, ref, err := prepareFile(file)
	if err != nil {
		return nil, err
	}

	putCtx, cancel := context.WithTimeout(ctx, fileOpTimeout)
	defer cancel()
	if err := m.objects.Put(putCtx, m.moduleID, ref.ObjectKey, data); err != nil {
		log.Printf("aiproxy: store attachment %q as %s: %v", ref.Name, ref.ObjectKey, err)
		return nil, fmt.Errorf("%w: %v", errFileStoreUnavailable, err)
	}
	log.Printf("aiproxy: attachment %q stored as %s (%d bytes, encoding=%q)",
		ref.Name, ref.ObjectKey, ref.ByteLength, ref.Encoding)
	return ref, nil
}

// Hydrate restores the attachment bytes for a job that is about to reach the
// extension. The extension contract carries base64 inline, so the bytes are
// read back from the object store and verified before use.
func (m *JobManager) Hydrate(ctx context.Context, req AutomationRequest) (AutomationRequest, error) {
	if req.File == nil || req.File.ObjectKey == "" {
		return req, nil
	}
	getCtx, cancel := context.WithTimeout(ctx, fileOpTimeout)
	defer cancel()
	file, err := loadFile(getCtx, m.objects, m.moduleID, req.File)
	if err != nil {
		return req, err
	}
	req.File = file
	return req, nil
}

// Submit stores any attachment, creates a new job, and enqueues it for the
// extension. The file is stored before the job is enqueued, so a job the
// extension can see always has its bytes in the object store.
func (m *JobManager) Submit(ctx context.Context, req AutomationRequest) (*Job, error) {
	id := generateJobID()
	now := time.Now().UTC()

	if req.File != nil {
		ref, err := m.storeFile(ctx, req.File)
		if err != nil {
			return nil, err
		}
		req.File = ref
	}

	job := &Job{
		ID:        id,
		Status:    JobPending,
		Request:   req,
		CreatedAt: now,
		UpdatedAt: now,
		doneCh:    make(chan struct{}),
	}

	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()
	log.Printf("aiproxy: job %s submitted", id)

	// Enqueue for the extension to pick up.
	select {
	case m.pending <- job:
	default:
		// Queue full — fail the job immediately.
		m.mu.Lock()
		job.Status = JobFailed
		job.Result = &AutomationResult{
			Status: "error",
			Error:  &AutomationError{Code: "QUEUE_FULL", Message: "job queue is full"},
		}
		job.UpdatedAt = time.Now().UTC()
		close(job.doneCh)
		snap := m.schedulePersist(job)
		m.mu.Unlock()
		log.Printf("aiproxy: job %s rejected (queue full)", id)
		m.writeSnapshot(snap)
		m.scheduleEviction(id)
	}

	// Start timeout goroutine.
	go m.enforceTimeout(job)

	return job, nil
}

// Get retrieves a job by ID. Memory holds the job while it is in flight and
// for the retention window after it finishes; older history is read back from
// the data store. Returns nil if neither has it.
func (m *JobManager) Get(id string) *Job {
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job != nil {
		return job
	}
	return m.load(id)
}

// load fetches a stored job. Errors are not surfaced: the client sees the same
// "not found" whether the record never existed or the store is unavailable.
// Ids that cannot be generated ones skip the store round trip.
func (m *JobManager) load(id string) *Job {
	if m.store == nil || len(id) != jobIDLength {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	job, ok := loadJob(ctx, m.store, m.moduleID, id)
	if !ok {
		return nil
	}
	return job
}

// WaitForResult blocks until the job reaches a terminal state or the
// context deadline expires. Returns the job's final state.
func (m *JobManager) WaitForResult(job *Job, timeout time.Duration) *Job {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-job.doneCh:
	case <-timer.C:
	}
	return job
}

// PendingChan returns the channel from which the extension long-poll
// server reads pending jobs.
func (m *JobManager) PendingChan() <-chan *Job {
	return m.pending
}

// MarkRunning transitions a job from pending to running and reports whether it
// did. Called by the long-poll server when the extension picks up the job. A
// job that reached a terminal state while its attachment was being read stays
// terminal and must not be handed to the extension.
func (m *JobManager) MarkRunning(id string) bool {
	m.mu.Lock()
	job := m.jobs[id]
	ran := job != nil && job.Status == JobPending
	if ran {
		job.Status = JobRunning
		job.UpdatedAt = time.Now().UTC()
	}
	m.mu.Unlock()
	if ran {
		log.Printf("aiproxy: job %s running", id)
	}
	return ran
}

// Requeue moves a job back to pending if it was not delivered to the
// extension (e.g., write failure or client disconnect before MarkRunning).
// Accepts both JobPending (dequeued but not yet marked) and JobRunning
// (marked but write failed) statuses.
func (m *JobManager) Requeue(id string) {
	m.mu.Lock()
	job := m.jobs[id]
	if job != nil && (job.Status == JobRunning || job.Status == JobPending) {
		job.Status = JobPending
		job.UpdatedAt = time.Now().UTC()
		m.mu.Unlock()
		select {
		case m.pending <- job:
			log.Printf("aiproxy: job %s requeued", id)
		default:
			m.mu.Lock()
			job.Status = JobFailed
			job.Result = &AutomationResult{
				Status: "error",
				Error:  &AutomationError{Code: "QUEUE_FULL", Message: "requeue failed: queue full"},
			}
			job.UpdatedAt = time.Now().UTC()
			close(job.doneCh)
			snap := m.schedulePersist(job)
			m.mu.Unlock()
			log.Printf("aiproxy: job %s requeue failed (queue full)", id)
			m.writeSnapshot(snap)
			m.scheduleEviction(id)
		}
		return
	}
	m.mu.Unlock()
}

// scheduleEviction schedules a job for deletion after the retention TTL.
// Must be called after every transition to a terminal state.
func (m *JobManager) scheduleEviction(id string) {
	time.AfterFunc(m.retentionTTL, func() {
		m.mu.Lock()
		delete(m.jobs, id)
		m.mu.Unlock()
	})
}

// Complete transitions a job to a terminal state with the given result.
// Called by the long-poll server when the extension returns a result.
func (m *JobManager) Complete(id string, result *AutomationResult) {
	m.mu.Lock()
	job := m.jobs[id]
	if job == nil || isTerminal(job.Status) {
		m.mu.Unlock()
		return
	}
	if result.Status == "ok" {
		job.Status = JobCompleted
	} else {
		job.Status = JobFailed
	}
	job.Result = result
	job.UpdatedAt = time.Now().UTC()
	close(job.doneCh)
	snap := m.schedulePersist(job)
	m.mu.Unlock()
	log.Printf("aiproxy: job %s %s", id, job.Status)
	m.writeSnapshot(snap)
	m.scheduleEviction(id)
}

func (m *JobManager) enforceTimeout(job *Job) {
	timeout := m.timeout
	if job.Request.TimeoutMs > 0 {
		timeout = time.Duration(job.Request.TimeoutMs) * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-job.doneCh:
		return // already terminal
	case <-timer.C:
		m.mu.Lock()
		if !isTerminal(job.Status) {
			job.Status = JobTimeout
			job.Result = &AutomationResult{
				Status: "error",
				Error:  &AutomationError{Code: "TIMEOUT", Message: "job timed out"},
			}
			job.UpdatedAt = time.Now().UTC()
			close(job.doneCh)
			snap := m.schedulePersist(job)
			log.Printf("aiproxy: job %s timed out", job.ID)
			m.mu.Unlock()
			m.writeSnapshot(snap)
			m.scheduleEviction(job.ID)
		} else {
			m.mu.Unlock()
		}
	}
}

func isTerminal(s JobStatus) bool {
	return s == JobCompleted || s == JobFailed || s == JobTimeout
}

// jobIDLength is the hex length of a generated job id.
const jobIDLength = 32

// closeGrace bounds how long Close waits for persistence writes at shutdown.
const closeGrace = 5 * time.Second

func generateJobID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(buf[:])
}
