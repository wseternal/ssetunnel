package metrics

import (
	"sync/atomic"
	"time"
)

// ObjectStoreMetrics tracks server-side module object-store operations.
// It is server-scoped: one instance covers all sessions and modules.
// All methods are nil-receiver safe.
type ObjectStoreMetrics struct {
	PutTotal       atomic.Int64
	GetTotal       atomic.Int64
	PutErrors      atomic.Int64
	GetErrors      atomic.Int64
	NotFound       atomic.Int64
	BusyRejections atomic.Int64
	BytesIn        atomic.Int64
	BytesOut       atomic.Int64
	LatencyTotalNs atomic.Int64
	LatencySamples atomic.Int64
}

// NewObjectStoreMetrics creates an empty object-store metrics set.
func NewObjectStoreMetrics() *ObjectStoreMetrics { return &ObjectStoreMetrics{} }

// RecordPut records one successful object write.
func (m *ObjectStoreMetrics) RecordPut(bytes int, d time.Duration) {
	if m == nil {
		return
	}
	m.PutTotal.Add(1)
	m.BytesIn.Add(int64(bytes))
	m.recordLatency(d)
}

// RecordGet records one successful object read.
func (m *ObjectStoreMetrics) RecordGet(bytes int, d time.Duration) {
	if m == nil {
		return
	}
	m.GetTotal.Add(1)
	m.BytesOut.Add(int64(bytes))
	m.recordLatency(d)
}

// RecordFailure records one failed object operation. A missing object is
// an expected result, not a backend failure: callers use RecordMiss.
func (m *ObjectStoreMetrics) RecordFailure(op string, d time.Duration) {
	if m == nil {
		return
	}
	switch op {
	case "put":
		m.PutErrors.Add(1)
	case "get":
		m.GetErrors.Add(1)
	}
	m.recordLatency(d)
}

// RecordMiss records one read for a missing object.
func (m *ObjectStoreMetrics) RecordMiss(d time.Duration) {
	if m == nil {
		return
	}
	m.NotFound.Add(1)
	m.recordLatency(d)
}

// RecordBusy records one rejected transfer because a queue or slot was full.
func (m *ObjectStoreMetrics) RecordBusy() {
	if m == nil {
		return
	}
	m.BusyRejections.Add(1)
}

func (m *ObjectStoreMetrics) recordLatency(d time.Duration) {
	if d > 0 {
		m.LatencyTotalNs.Add(d.Nanoseconds())
	}
	m.LatencySamples.Add(1)
}

// Summary returns a point-in-time view for logs and tests.
func (m *ObjectStoreMetrics) Summary() map[string]any {
	if m == nil {
		return nil
	}
	samples := m.LatencySamples.Load()
	var avgMs float64
	if samples > 0 {
		avgMs = float64(m.LatencyTotalNs.Load()) / float64(samples) / float64(time.Millisecond)
	}
	return map[string]any{
		"put_total":       m.PutTotal.Load(),
		"get_total":       m.GetTotal.Load(),
		"put_errors":      m.PutErrors.Load(),
		"get_errors":      m.GetErrors.Load(),
		"not_found":       m.NotFound.Load(),
		"busy_rejections": m.BusyRejections.Load(),
		"bytes_in":        m.BytesIn.Load(),
		"bytes_out":       m.BytesOut.Load(),
		"latency_avg_ms":  avgMs,
		"latency_samples": samples,
	}
}
