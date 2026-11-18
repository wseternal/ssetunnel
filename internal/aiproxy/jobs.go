// Package aiproxy implements the AI automation proxy module that bridges
// external client requests to the gemini-automation-extension WXT browser
// extension via HTTP long polling.
package aiproxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
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

// FilePayload is a base64-encoded file attached to the automation request.
type FilePayload struct {
	Name      string `json:"name"`
	MimeType  string `json:"mime_type"`
	DataBase64 string `json:"data_base64"`
	ByteLength int    `json:"byte_length"`
}

// AutomationResult is the extension-returned job result.
type AutomationResult struct {
	Status   string          `json:"status"` // "ok" or "error"
	Text     string          `json:"text,omitempty"`
	Markdown string          `json:"markdown,omitempty"`
	Error    *AutomationError `json:"error,omitempty"`
	Meta     json.RawMessage `json:"meta,omitempty"`
}

// AutomationError describes a failure from the extension.
type AutomationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Step    string `json:"step,omitempty"`
}

// Job represents one automation job in the system.
type Job struct {
	ID        string             `json:"id"`
	Status    JobStatus          `json:"status"`
	Request   AutomationRequest  `json:"request"`
	Result    *AutomationResult  `json:"result,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`

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
}

// NewJobManager creates a job manager with the given default timeout.
func NewJobManager(timeout time.Duration) *JobManager {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &JobManager{
		jobs:         make(map[string]*Job),
		pending:      make(chan *Job, 256),
		timeout:      timeout,
		retentionTTL: 10 * time.Minute,
	}
}

// Submit creates a new job and enqueues it for the extension.
// Returns the job ID.
func (m *JobManager) Submit(req AutomationRequest) (*Job, error) {
	id := generateJobID()
	now := time.Now().UTC()
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
		m.mu.Unlock()
		log.Printf("aiproxy: job %s rejected (queue full)", id)
	}

	// Start timeout goroutine.
	go m.enforceTimeout(job)

	return job, nil
}

// Get retrieves a job by ID. Returns nil if not found.
func (m *JobManager) Get(id string) *Job {
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
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

// MarkRunning transitions a job from pending to running.
// Called by the long-poll server when the extension picks up the job.
func (m *JobManager) MarkRunning(id string) {
	m.mu.Lock()
	job := m.jobs[id]
	if job != nil && job.Status == JobPending {
		job.Status = JobRunning
		job.UpdatedAt = time.Now().UTC()
	}
	m.mu.Unlock()
	log.Printf("aiproxy: job %s running", id)
}

// Requeue moves a running job back to pending if it was not delivered
// to the extension (e.g., write failure or client disconnect).
func (m *JobManager) Requeue(id string) {
	m.mu.Lock()
	job := m.jobs[id]
	if job != nil && job.Status == JobRunning {
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
			m.mu.Unlock()
			log.Printf("aiproxy: job %s requeue failed (queue full)", id)
		}
		return
	}
	m.mu.Unlock()
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
	m.mu.Unlock()
	log.Printf("aiproxy: job %s %s", id, job.Status)

	// Schedule eviction after retention TTL.
	time.AfterFunc(m.retentionTTL, func() {
		m.mu.Lock()
		delete(m.jobs, id)
		m.mu.Unlock()
	})
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
			log.Printf("aiproxy: job %s timed out", job.ID)
		}
		m.mu.Unlock()
	}
}

func isTerminal(s JobStatus) bool {
	return s == JobCompleted || s == JobFailed || s == JobTimeout
}

func generateJobID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(buf[:])
}
