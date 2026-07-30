package metrics

import (
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore_WriteAndQuerySamples(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	samples := []MetricSample{
		{Timestamp: now, AgentID: "agent1", BytesUp: 100, ThroughputUp: 1000},
		{Timestamp: now.Add(time.Second), AgentID: "agent1", BytesUp: 200, ThroughputUp: 2000},
		{Timestamp: now.Add(2 * time.Second), AgentID: "agent2", BytesUp: 300, ThroughputUp: 3000},
	}
	if err := s.WriteSamples(samples); err != nil {
		t.Fatalf("WriteSamples: %v", err)
	}

	// Query agent1
	got, err := s.QuerySamples("agent1", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("QuerySamples: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 samples for agent1, got %d", len(got))
	}
	if got[0].BytesUp != 100 || got[1].BytesUp != 200 {
		t.Errorf("unexpected bytes: %d, %d", got[0].BytesUp, got[1].BytesUp)
	}

	// Query agent2
	got, err = s.QuerySamples("agent2", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("QuerySamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 sample for agent2, got %d", len(got))
	}

	// Query with narrow time range (should miss agent1's second sample)
	got, err = s.QuerySamples("agent1", now.Add(-time.Minute), now.Add(500*time.Millisecond))
	if err != nil {
		t.Fatalf("QuerySamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 sample in narrow range, got %d", len(got))
	}
}

func TestStore_WriteAndQueryDecisions(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()

	decisions := []TuningDecision{
		{
			Timestamp: now,
			AgentID:   "agent1",
			OldParams: TransportParams{Concurrency: 1, BatchSize: 16384},
			NewParams: TransportParams{Concurrency: 2, BatchSize: 16384},
			Reason:    "high latency detected",
		},
		{
			Timestamp: now.Add(time.Second),
			AgentID:   "agent1",
			OldParams: TransportParams{Concurrency: 2, BatchSize: 16384},
			NewParams: TransportParams{Concurrency: 2, BatchSize: 32768},
			Reason:    "throughput saturation",
		},
	}
	for _, d := range decisions {
		if err := s.WriteDecision(d); err != nil {
			t.Fatalf("WriteDecision: %v", err)
		}
	}

	got, err := s.QueryDecisions("agent1", 10)
	if err != nil {
		t.Fatalf("QueryDecisions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 decisions, got %d", len(got))
	}
	// Newest first (reverse scan)
	if got[0].Reason != "throughput saturation" {
		t.Errorf("expected newest first, got %q", got[0].Reason)
	}

	// Query all agents
	got, err = s.QueryDecisions("", 10)
	if err != nil {
		t.Fatalf("QueryDecisions all: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 decisions for all agents, got %d", len(got))
	}

	// Limit
	got, err = s.QueryDecisions("agent1", 1)
	if err != nil {
		t.Fatalf("QueryDecisions limit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 decision with limit, got %d", len(got))
	}
}

func TestStore_WindowState(t *testing.T) {
	s := openTestStore(t)

	// No state initially
	data, err := s.ReadWindow("agent1")
	if err != nil {
		t.Fatalf("ReadWindow: %v", err)
	}
	if data != nil {
		t.Fatal("expected nil window state initially")
	}

	// Write and read back
	state := []byte(`{"posts":5,"errors":1}`)
	if err := s.WriteWindow("agent1", state); err != nil {
		t.Fatalf("WriteWindow: %v", err)
	}
	got, err := s.ReadWindow("agent1")
	if err != nil {
		t.Fatalf("ReadWindow: %v", err)
	}
	if string(got) != string(state) {
		t.Errorf("want %q, got %q", state, got)
	}
}

func TestStore_PruneOlderThan(t *testing.T) {
	s := openTestStore(t)
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now()

	samples := []MetricSample{
		{Timestamp: old, AgentID: "agent1", BytesUp: 100},
		{Timestamp: recent, AgentID: "agent1", BytesUp: 200},
	}
	if err := s.WriteSamples(samples); err != nil {
		t.Fatalf("WriteSamples: %v", err)
	}

	// Prune entries older than 24 hours
	if err := s.PruneOlderThan(24 * time.Hour); err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}

	got, err := s.QuerySamples("agent1", old.Add(-time.Hour), recent.Add(time.Hour))
	if err != nil {
		t.Fatalf("QuerySamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 sample after prune, got %d", len(got))
	}
	if got[0].BytesUp != 200 {
		t.Errorf("expected recent sample, got bytes=%d", got[0].BytesUp)
	}
}

func TestStore_NilSafe(t *testing.T) {
	var s *Store
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := s.WriteSamples(nil); err != nil {
		t.Errorf("WriteSamples: %v", err)
	}
	if _, err := s.QuerySamples("a", time.Time{}, time.Time{}); err != nil {
		t.Errorf("QuerySamples: %v", err)
	}
	if err := s.WriteDecision(TuningDecision{}); err != nil {
		t.Errorf("WriteDecision: %v", err)
	}
	if _, err := s.QueryDecisions("a", 10); err != nil {
		t.Errorf("QueryDecisions: %v", err)
	}
	if err := s.PruneOlderThan(time.Hour); err != nil {
		t.Errorf("PruneOlderThan: %v", err)
	}
	if err := s.WriteWindow("a", nil); err != nil {
		t.Errorf("WriteWindow: %v", err)
	}
	if _, err := s.ReadWindow("a"); err != nil {
		t.Errorf("ReadWindow: %v", err)
	}
}
