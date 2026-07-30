package metrics

import (
	"testing"
	"time"
)

func TestCollector_NilSafe(t *testing.T) {
	var c *MetricsCollector
	// All recording methods should be safe on nil receiver
	c.RecordAgentPost("agent1", 100, time.Millisecond)
	c.RecordAgentSSEBytes("agent1", 100)
	c.RecordConnectBytes("agent1", 10, 20)
	c.RecordSessionStart("agent1")
	c.RecordSessionEnd("agent1")
	c.RecordError("agent1", "test")
	c.SetParams("agent1", TransportParams{})
	c.SetLastDecision(nil)
	c.Close()

	// Query methods should return zero values
	if o := c.Overview(); o.ActiveAgents != 0 {
		t.Errorf("nil Overview: want 0 agents, got %d", o.ActiveAgents)
	}
	if m := c.AllAgentMetrics(); m != nil {
		t.Errorf("nil AllAgentMetrics: want nil, got %v", m)
	}
	if _, ok := c.AgentSnapshot("agent1"); ok {
		t.Error("nil AgentSnapshot: want false")
	}
	if ids := c.ActiveAgentIDs(); ids != nil {
		t.Errorf("nil ActiveAgentIDs: want nil, got %v", ids)
	}
	if p := c.GetParams("agent1"); p.BatchSize != 0 {
		t.Errorf("nil GetParams: want zero, got %v", p)
	}
}

func TestCollector_RecordAndSnapshot(t *testing.T) {
	store := openTestStore(t)
	c := NewCollector(store, 100*time.Millisecond, 24*time.Hour)
	defer c.Close()

	// Record some events
	c.RecordAgentPost("agent1", 1000, 10*time.Millisecond)
	c.RecordAgentPost("agent1", 2000, 20*time.Millisecond)
	c.RecordAgentPost("agent1", 3000, 50*time.Millisecond)
	c.RecordAgentSSEBytes("agent1", 5000)
	c.RecordSessionStart("agent1")
	c.RecordError("agent1", "timeout")

	// Get snapshot
	snap, ok := c.AgentSnapshot("agent1")
	if !ok {
		t.Fatal("AgentSnapshot: want true, got false")
	}
	if snap.TotalPosts != 3 {
		t.Errorf("TotalPosts: want 3, got %d", snap.TotalPosts)
	}
	if snap.TotalErrors != 1 {
		t.Errorf("TotalErrors: want 1, got %d", snap.TotalErrors)
	}
	if snap.ActiveConns != 1 {
		t.Errorf("ActiveConns: want 1, got %d", snap.ActiveConns)
	}
	// Error rate = 1/3
	if snap.ErrorRate < 0.3 || snap.ErrorRate > 0.4 {
		t.Errorf("ErrorRate: want ~0.33, got %f", snap.ErrorRate)
	}
	// P50 latency should be the middle value (20ms)
	if snap.LatencyP50Ms < 15 || snap.LatencyP50Ms > 25 {
		t.Errorf("LatencyP50Ms: want ~20, got %f", snap.LatencyP50Ms)
	}
	// P95 should be near the top (50ms)
	if snap.LatencyP95Ms < 40 || snap.LatencyP95Ms > 55 {
		t.Errorf("LatencyP95Ms: want ~50, got %f", snap.LatencyP95Ms)
	}
}

func TestCollector_SessionTracking(t *testing.T) {
	c := NewCollector(nil, time.Second, 24*time.Hour)
	defer c.Close()

	c.RecordSessionStart("agent1")
	c.RecordSessionStart("agent1")
	snap, _ := c.AgentSnapshot("agent1")
	if snap.ActiveConns != 2 {
		t.Errorf("want 2 active, got %d", snap.ActiveConns)
	}

	c.RecordSessionEnd("agent1")
	snap, _ = c.AgentSnapshot("agent1")
	if snap.ActiveConns != 1 {
		t.Errorf("want 1 active after end, got %d", snap.ActiveConns)
	}

	// Should not go below 0
	c.RecordSessionEnd("agent1")
	c.RecordSessionEnd("agent1")
	snap, _ = c.AgentSnapshot("agent1")
	if snap.ActiveConns != 0 {
		t.Errorf("want 0 active, got %d", snap.ActiveConns)
	}
}

func TestCollector_EmptyAgentID(t *testing.T) {
	c := NewCollector(nil, time.Second, 24*time.Hour)
	defer c.Close()

	// Empty agent ID should be ignored
	c.RecordAgentPost("", 100, time.Millisecond)
	c.RecordAgentSSEBytes("", 100)
	c.RecordConnectBytes("", 10, 20)
	c.RecordSessionStart("")
	c.RecordSessionEnd("")
	c.RecordError("", "test")

	// No windows should exist
	if ids := c.ActiveAgentIDs(); len(ids) != 0 {
		t.Errorf("want 0 agents, got %d", len(ids))
	}
}

func TestCollector_Params(t *testing.T) {
	c := NewCollector(nil, time.Second, 24*time.Hour)
	defer c.Close()

	// Default params
	p := c.GetParams("agent1")
	if p.BatchSize != 0 || p.Concurrency != 0 {
		t.Errorf("default params should be zero, got %v", p)
	}

	// Set and get
	c.SetParams("agent1", TransportParams{Concurrency: 4, BatchSize: 65536, Compress: true})
	p = c.GetParams("agent1")
	if p.Concurrency != 4 || p.BatchSize != 65536 || !p.Compress {
		t.Errorf("unexpected params: %v", p)
	}
}

func TestCollector_LastDecision(t *testing.T) {
	c := NewCollector(nil, time.Second, 24*time.Hour)
	defer c.Close()

	d := &TuningDecision{
		AgentID: "agent1",
		Reason:  "test decision",
	}
	c.SetLastDecision(d)

	metrics := c.AllAgentMetrics()
	found := false
	for _, m := range metrics {
		if m.AgentID == "agent1" {
			found = true
			if m.LastDecision == nil || m.LastDecision.Reason != "test decision" {
				t.Errorf("unexpected last decision: %v", m.LastDecision)
			}
		}
	}
	if !found {
		// Need to create a window first
		c.RecordSessionStart("agent1")
		metrics = c.AllAgentMetrics()
		for _, m := range metrics {
			if m.AgentID == "agent1" && m.LastDecision != nil && m.LastDecision.Reason == "test decision" {
				found = true
			}
		}
	}
}

func TestCollector_Overview(t *testing.T) {
	c := NewCollector(nil, 10*time.Second, 24*time.Hour)
	defer c.Close()

	c.RecordAgentPost("agent1", 1000, time.Millisecond)
	c.RecordAgentPost("agent2", 2000, 2*time.Millisecond)

	o := c.Overview()
	if o.ActiveAgents != 2 {
		t.Errorf("want 2 active agents, got %d", o.ActiveAgents)
	}
	if o.ThroughputUpBps <= 0 {
		t.Errorf("want positive throughput, got %f", o.ThroughputUpBps)
	}
}

func TestCollector_FlushWritesToStore(t *testing.T) {
	store := openTestStore(t)
	c := &MetricsCollector{
		windows:       make(map[string]*rollingWindow),
		store:         store,
		flushInterval: 100 * time.Millisecond,
		retention:     24 * time.Hour,
		params:        make(map[string]TransportParams),
		lastDecision:  make(map[string]*TuningDecision),
		stopCh:        make(chan struct{}),
	}

	c.RecordAgentPost("agent1", 1000, 10*time.Millisecond)
	c.RecordAgentSSEBytes("agent1", 500)

	// Manual flush
	c.flush()

	// Verify samples were written
	samples, err := store.QuerySamples("agent1", time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("QuerySamples: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("want 1 flushed sample, got %d", len(samples))
	}
	if samples[0].BytesUp != 1000 {
		t.Errorf("want BytesUp=1000, got %d", samples[0].BytesUp)
	}
}
