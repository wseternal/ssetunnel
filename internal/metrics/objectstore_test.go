package metrics

import (
	"math"
	"testing"
	"time"
)

func TestObjectStoreMetricsSummary(t *testing.T) {
	m := NewObjectStoreMetrics()
	m.RecordPut(100, 10*time.Millisecond)
	m.RecordGet(200, 20*time.Millisecond)
	m.RecordFailure("put", 5*time.Millisecond)
	m.RecordBusy()

	summary := m.Summary()
	if got := summary["put_total"]; got != int64(1) {
		t.Fatalf("put_total = %v, want 1", got)
	}
	if got := summary["get_total"]; got != int64(1) {
		t.Fatalf("get_total = %v, want 1", got)
	}
	if got := summary["put_errors"]; got != int64(1) {
		t.Fatalf("put_errors = %v, want 1", got)
	}
	if got := summary["busy_rejections"]; got != int64(1) {
		t.Fatalf("busy_rejections = %v, want 1", got)
	}
	if got := summary["bytes_in"]; got != int64(100) {
		t.Fatalf("bytes_in = %v, want 100", got)
	}
	if got := summary["bytes_out"]; got != int64(200) {
		t.Fatalf("bytes_out = %v, want 200", got)
	}
	avg, ok := summary["latency_avg_ms"].(float64)
	if !ok || math.Abs(avg-(35.0/3.0)) > 0.001 {
		t.Fatalf("latency_avg_ms = %v, want %v", summary["latency_avg_ms"], 35.0/3.0)
	}
}

func TestObjectStoreMetricsNilSafe(t *testing.T) {
	var m *ObjectStoreMetrics
	m.RecordPut(1, time.Millisecond)
	m.RecordGet(1, time.Millisecond)
	m.RecordFailure("get", time.Millisecond)
	m.RecordBusy()
	if m.Summary() != nil {
		t.Fatal("nil metrics returned a summary")
	}
}
