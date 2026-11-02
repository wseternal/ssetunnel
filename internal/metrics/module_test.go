package metrics

import (
	"fmt"
	"testing"
	"time"
)

func TestModuleMetrics_RecordInvocation(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordInvocation()
	if got := m.InvocationsTotal.Load(); got != 1 {
		t.Errorf("InvocationsTotal: got %d, want 1", got)
	}
	if got := m.InvocationsActive.Load(); got != 1 {
		t.Errorf("InvocationsActive: got %d, want 1", got)
	}
}

func TestModuleMetrics_RecordInvocationComplete(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordInvocation()
	m.RecordInvocation()

	// Complete one successfully.
	m.RecordInvocationComplete(false, false)
	if got := m.InvocationsActive.Load(); got != 1 {
		t.Errorf("InvocationsActive after success: got %d, want 1", got)
	}
	if got := m.InvocationsFailed.Load(); got != 0 {
		t.Errorf("InvocationsFailed: got %d, want 0", got)
	}

	// Complete one with failure.
	m.RecordInvocationComplete(true, false)
	if got := m.InvocationsActive.Load(); got != 0 {
		t.Errorf("InvocationsActive after failure: got %d, want 0", got)
	}
	if got := m.InvocationsFailed.Load(); got != 1 {
		t.Errorf("InvocationsFailed: got %d, want 1", got)
	}
}

func TestModuleMetrics_RecordInvocationCanceled(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordInvocation()
	m.RecordInvocationComplete(false, true)

	if got := m.InvocationsCanceled.Load(); got != 1 {
		t.Errorf("InvocationsCanceled: got %d, want 1", got)
	}
}

func TestModuleMetrics_RecordPending(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordPendingOpen()
	m.RecordPendingOpen()
	if got := m.InvocationsPending.Load(); got != 2 {
		t.Errorf("InvocationsPending: got %d, want 2", got)
	}

	m.RecordPendingClose()
	if got := m.InvocationsPending.Load(); got != 1 {
		t.Errorf("InvocationsPending after close: got %d, want 1", got)
	}
}

func TestModuleMetrics_RecordBytes(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordBytes(100, 200)
	m.RecordBytes(50, 75)

	if got := m.BytesSent.Load(); got != 150 {
		t.Errorf("BytesSent: got %d, want 150", got)
	}
	if got := m.BytesReceived.Load(); got != 275 {
		t.Errorf("BytesReceived: got %d, want 275", got)
	}
}

func TestModuleMetrics_RecordError(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordError("transport")
	m.RecordError("transport")
	m.RecordError("registry")
	m.RecordError("module")
	m.RecordError("unknown") // Should be ignored.

	if got := m.ErrorsTransport.Load(); got != 2 {
		t.Errorf("ErrorsTransport: got %d, want 2", got)
	}
	if got := m.ErrorsRegistry.Load(); got != 1 {
		t.Errorf("ErrorsRegistry: got %d, want 1", got)
	}
	if got := m.ErrorsModule.Load(); got != 1 {
		t.Errorf("ErrorsModule: got %d, want 1", got)
	}
}

func TestModuleMetrics_RecordSnapshot(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordSnapshot(5, 100*time.Millisecond, nil)
	if got := m.SnapshotCount.Load(); got != 1 {
		t.Errorf("SnapshotCount: got %d, want 1", got)
	}
	if got := m.CatalogSize.Load(); got != 5 {
		t.Errorf("CatalogSize: got %d, want 5", got)
	}
	if got := m.SnapshotErrors.Load(); got != 0 {
		t.Errorf("SnapshotErrors: got %d, want 0", got)
	}

	m.RecordSnapshot(3, 50*time.Millisecond, fmt.Errorf("test error"))
	if got := m.SnapshotCount.Load(); got != 2 {
		t.Errorf("SnapshotCount: got %d, want 2", got)
	}
	if got := m.SnapshotErrors.Load(); got != 1 {
		t.Errorf("SnapshotErrors: got %d, want 1", got)
	}
}

func TestModuleMetrics_RecordUIRequest(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordUIRequest(true, false)  // cache hit, success
	m.RecordUIRequest(false, false) // cache miss, success
	m.RecordUIRequest(false, true)  // cache miss, failed

	if got := m.UIRequestsTotal.Load(); got != 3 {
		t.Errorf("UIRequestsTotal: got %d, want 3", got)
	}
	if got := m.UICacheHits.Load(); got != 1 {
		t.Errorf("UICacheHits: got %d, want 1", got)
	}
	if got := m.UICacheMisses.Load(); got != 2 {
		t.Errorf("UICacheMisses: got %d, want 2", got)
	}
	if got := m.UIRequestsFailed.Load(); got != 1 {
		t.Errorf("UIRequestsFailed: got %d, want 1", got)
	}
}

func TestModuleMetrics_TakeSample(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordInvocation()
	m.RecordBytes(100, 200)
	m.RecordError("transport")

	sample := m.TakeSample()
	if sample.CatalogSize != 0 {
		t.Errorf("CatalogSize: got %d, want 0", sample.CatalogSize)
	}
	if sample.InvocationsActive != 1 {
		t.Errorf("InvocationsActive: got %d, want 1", sample.InvocationsActive)
	}
	if sample.BytesSent != 100 {
		t.Errorf("BytesSent: got %d, want 100", sample.BytesSent)
	}
	if sample.ErrorsTransport != 1 {
		t.Errorf("ErrorsTransport: got %d, want 1", sample.ErrorsTransport)
	}
	if sample.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestModuleMetrics_Samples(t *testing.T) {
	m := NewModuleMetrics(5)

	for i := 0; i < 3; i++ {
		m.TakeSample()
	}

	samples := m.Samples()
	if len(samples) != 3 {
		t.Errorf("Samples count: got %d, want 3", len(samples))
	}
}

func TestModuleMetrics_SamplesRingBuffer(t *testing.T) {
	m := NewModuleMetrics(3) // small ring

	for i := 0; i < 5; i++ {
		m.TakeSample()
	}

	samples := m.Samples()
	if len(samples) != 3 {
		t.Errorf("Samples count: got %d, want 3 (ring buffer)", len(samples))
	}
}

func TestModuleMetrics_Summary(t *testing.T) {
	m := NewModuleMetrics(10)

	m.RecordInvocation()
	m.RecordBytes(100, 200)

	summary := m.Summary()
	if summary["invocations_total"].(int64) != 1 {
		t.Errorf("invocations_total: got %d, want 1", summary["invocations_total"])
	}
	if summary["bytes_sent"].(int64) != 100 {
		t.Errorf("bytes_sent: got %d, want 100", summary["bytes_sent"])
	}
}
