package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// ModuleMetrics tracks metrics for the module registry and invocation
// data plane. These metrics help operators diagnose overload, stale
// revisions, reconnects, and remote failures.
type ModuleMetrics struct {
	mu sync.RWMutex

	// Catalog metrics.
	CatalogSize       atomic.Int64  // Current number of modules in catalog.
	SnapshotCount     atomic.Int64  // Total snapshots received.
	SnapshotDurationNs atomic.Int64 // Last snapshot sync duration in nanoseconds.
	SnapshotErrors    atomic.Int64  // Failed snapshot commits.

	// Invocation metrics.
	InvocationsTotal   atomic.Int64 // Total invocations attempted.
	InvocationsActive  atomic.Int64 // Currently active invocations.
	InvocationsPending atomic.Int64 // Currently pending invocations.
	InvocationsFailed  atomic.Int64 // Failed invocations (timeout, error).
	InvocationsCanceled atomic.Int64 // Canceled invocations.

	// Data plane metrics.
	BytesSent     atomic.Int64 // Bytes sent to modules.
	BytesReceived atomic.Int64 // Bytes received from modules.

	// Error counters by type.
	ErrorsTransport atomic.Int64 // Transport errors (stream closed, timeout).
	ErrorsRegistry  atomic.Int64 // Registry errors (not found, stale revision).
	ErrorsModule    atomic.Int64 // Module errors (unhealthy, crash).

	// UI metrics.
	UIRequestsTotal atomic.Int64 // Total UI asset requests.
	UIRequestsFailed atomic.Int64 // Failed UI asset requests.
	UICacheHits     atomic.Int64 // Cache hits for UI assets.
	UICacheMisses   atomic.Int64 // Cache misses for UI assets.

	// Recent samples (ring buffer).
	samples    []ModuleSample
	sampleIdx  int
	sampleSize int
}

// ModuleSample is a point-in-time snapshot of module metrics.
type ModuleSample struct {
	Timestamp         time.Time `json:"timestamp"`
	CatalogSize       int64     `json:"catalog_size"`
	InvocationsActive int64     `json:"invocations_active"`
	InvocationsPending int64    `json:"invocations_pending"`
	InvocationsFailed int64     `json:"invocations_failed_total"`
	BytesSent         int64     `json:"bytes_sent"`
	BytesReceived     int64     `json:"bytes_received"`
	ErrorsTransport   int64     `json:"errors_transport"`
	ErrorsRegistry    int64     `json:"errors_registry"`
	ErrorsModule      int64     `json:"errors_module"`
}

// NewModuleMetrics creates a module metrics collector with the given
// sample buffer size.
func NewModuleMetrics(sampleSize int) *ModuleMetrics {
	if sampleSize <= 0 {
		sampleSize = 100
	}
	return &ModuleMetrics{
		samples:    make([]ModuleSample, sampleSize),
		sampleSize: sampleSize,
	}
}

// RecordInvocation records a new invocation attempt.
func (m *ModuleMetrics) RecordInvocation() {
	m.InvocationsTotal.Add(1)
	m.InvocationsActive.Add(1)
}

// RecordInvocationComplete records an invocation completion.
func (m *ModuleMetrics) RecordInvocationComplete(failed, canceled bool) {
	m.InvocationsActive.Add(-1)
	if failed {
		m.InvocationsFailed.Add(1)
	}
	if canceled {
		m.InvocationsCanceled.Add(1)
	}
}

// RecordPendingOpen records a new pending module_open.
func (m *ModuleMetrics) RecordPendingOpen() {
	m.InvocationsPending.Add(1)
}

// RecordPendingClose records a pending module_open resolution.
func (m *ModuleMetrics) RecordPendingClose() {
	m.InvocationsPending.Add(-1)
}

// RecordBytes records bytes sent or received.
func (m *ModuleMetrics) RecordBytes(sent, received int64) {
	m.BytesSent.Add(sent)
	m.BytesReceived.Add(received)
}

// RecordError records an error by type.
func (m *ModuleMetrics) RecordError(errType string) {
	switch errType {
	case "transport":
		m.ErrorsTransport.Add(1)
	case "registry":
		m.ErrorsRegistry.Add(1)
	case "module":
		m.ErrorsModule.Add(1)
	}
}

// RecordSnapshot records a catalog snapshot commit.
func (m *ModuleMetrics) RecordSnapshot(catalogSize int, duration time.Duration, err error) {
	m.SnapshotCount.Add(1)
	m.CatalogSize.Store(int64(catalogSize))
	m.SnapshotDurationNs.Store(duration.Nanoseconds())
	if err != nil {
		m.SnapshotErrors.Add(1)
	}
}

// RecordUIRequest records a UI asset request.
func (m *ModuleMetrics) RecordUIRequest(cacheHit bool, failed bool) {
	m.UIRequestsTotal.Add(1)
	if cacheHit {
		m.UICacheHits.Add(1)
	} else {
		m.UICacheMisses.Add(1)
	}
	if failed {
		m.UIRequestsFailed.Add(1)
	}
}

// TakeSample captures a point-in-time sample of current metrics.
func (m *ModuleMetrics) TakeSample() ModuleSample {
	sample := ModuleSample{
		Timestamp:          time.Now(),
		CatalogSize:        m.CatalogSize.Load(),
		InvocationsActive:  m.InvocationsActive.Load(),
		InvocationsPending: m.InvocationsPending.Load(),
		InvocationsFailed:  m.InvocationsFailed.Load(),
		BytesSent:          m.BytesSent.Load(),
		BytesReceived:      m.BytesReceived.Load(),
		ErrorsTransport:    m.ErrorsTransport.Load(),
		ErrorsRegistry:     m.ErrorsRegistry.Load(),
		ErrorsModule:       m.ErrorsModule.Load(),
	}

	m.mu.Lock()
	m.samples[m.sampleIdx] = sample
	m.sampleIdx = (m.sampleIdx + 1) % m.sampleSize
	m.mu.Unlock()

	return sample
}

// Samples returns recent metric samples (oldest first).
func (m *ModuleMetrics) Samples() []ModuleSample {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return samples in chronological order.
	result := make([]ModuleSample, 0, m.sampleSize)
	for i := 0; i < m.sampleSize; i++ {
		idx := (m.sampleIdx + i) % m.sampleSize
		s := m.samples[idx]
		if !s.Timestamp.IsZero() {
			result = append(result, s)
		}
	}
	return result
}

// Summary returns a summary of current module metrics.
func (m *ModuleMetrics) Summary() map[string]interface{} {
	return map[string]interface{}{
		"catalog_size":         m.CatalogSize.Load(),
		"snapshot_count":       m.SnapshotCount.Load(),
		"snapshot_duration_ms": m.SnapshotDurationNs.Load() / 1e6,
		"snapshot_errors":      m.SnapshotErrors.Load(),
		"invocations_total":    m.InvocationsTotal.Load(),
		"invocations_active":   m.InvocationsActive.Load(),
		"invocations_pending":  m.InvocationsPending.Load(),
		"invocations_failed":   m.InvocationsFailed.Load(),
		"invocations_canceled": m.InvocationsCanceled.Load(),
		"bytes_sent":           m.BytesSent.Load(),
		"bytes_received":       m.BytesReceived.Load(),
		"errors_transport":     m.ErrorsTransport.Load(),
		"errors_registry":      m.ErrorsRegistry.Load(),
		"errors_module":        m.ErrorsModule.Load(),
		"ui_requests_total":    m.UIRequestsTotal.Load(),
		"ui_requests_failed":   m.UIRequestsFailed.Load(),
		"ui_cache_hits":        m.UICacheHits.Load(),
		"ui_cache_misses":      m.UICacheMisses.Load(),
	}
}
