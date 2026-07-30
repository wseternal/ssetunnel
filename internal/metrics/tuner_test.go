package metrics

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newTestTuner creates a tuner with a collector pre-populated with synthetic metrics.
func newTestTuner(t *testing.T) (*AutoTuner, *MetricsCollector) {
	t.Helper()
	store := openTestStore(t)
	c := NewCollector(store, 10*time.Second, 24*time.Hour)
	t.Cleanup(func() { c.Close() })

	tuner := NewAutoTuner(c, store, nil, 30*time.Second)
	return tuner, c
}

// seedMetrics populates the collector with synthetic post events.
func seedMetrics(c *MetricsCollector, agentID string, bytesPerPost int, rtt time.Duration, postCount int, errors int) {
	for i := 0; i < postCount; i++ {
		c.RecordAgentPost(agentID, bytesPerPost, rtt)
	}
	for i := 0; i < errors; i++ {
		c.RecordError(agentID, "test")
	}
	c.RecordSessionStart(agentID)
}

func TestTuner_ThroughputSaturation_IncreaseBatch(t *testing.T) {
	tuner, c := newTestTuner(t)

	// Seed: high throughput relative to batch ceiling (16384)
	// We need throughput > 80% of 16384 = ~13107 B/s
	// With 10s flush interval, 20 posts of 7000 bytes each = 140000 bytes
	// Throughput = 140000 / 10 = 14000 B/s (> 80% of 16384)
	seedMetrics(c, "agent1", 7000, 5*time.Millisecond, 20, 0)

	decision, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision == nil {
		t.Fatal("expected a decision (batch increase), got nil")
	}
	if decision.NewParams.BatchSize <= decision.OldParams.BatchSize {
		t.Errorf("expected batch size increase, got %d → %d", decision.OldParams.BatchSize, decision.NewParams.BatchSize)
	}
}

func TestTuner_ThroughputUndersaturation_DecreaseBatch(t *testing.T) {
	tuner, c := newTestTuner(t)

	// Seed: very low throughput relative to batch ceiling
	// Need throughput < 30% of 16384 = < ~4915 B/s
	// 2 posts of 100 bytes = 200 bytes / 10s = 20 B/s
	seedMetrics(c, "agent1", 100, time.Millisecond, 2, 0)

	// First evaluation: sets underSat counter to 1, no decision yet
	d1, _ := tuner.Evaluate("agent1")
	if d1 != nil {
		t.Log("first eval: got decision (unexpected but ok)")
	}

	// Force past stability guard
	tuner.mu.Lock()
	tuner.lastDecision["agent1"] = time.Now().Add(-3 * time.Minute)
	tuner.mu.Unlock()

	// Second evaluation: underSat >= 2, should decrease batch
	d2, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d2 == nil {
		t.Fatal("expected batch decrease after 2 undersaturation evals, got nil")
	}
	if d2.NewParams.BatchSize >= d2.OldParams.BatchSize {
		t.Errorf("expected batch size decrease, got %d → %d", d2.OldParams.BatchSize, d2.NewParams.BatchSize)
	}
}

func TestTuner_LatencyConcurrency_Increase(t *testing.T) {
	tuner, c := newTestTuner(t)

	// Seed: moderate throughput (not triggering batch change) but high p95 latency
	// Throughput ~5000 B/s (30% of 16384), p95 latency > 500ms
	seedMetrics(c, "agent1", 500, 600*time.Millisecond, 10, 0)

	// Set initial params so we have a known baseline
	tuner.mu.Lock()
	tuner.currentParams["agent1"] = TransportParams{Concurrency: 2, BatchSize: 16384}
	tuner.mu.Unlock()

	decision, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision == nil {
		t.Fatal("expected concurrency increase decision, got nil")
	}
	if decision.NewParams.Concurrency <= decision.OldParams.Concurrency {
		t.Errorf("expected concurrency increase, got %d → %d", decision.OldParams.Concurrency, decision.NewParams.Concurrency)
	}
}

func TestTuner_ErrorRateConcurrency_Decrease(t *testing.T) {
	tuner, c := newTestTuner(t)

	// Seed: moderate throughput, low latency, but high error rate (> 5%)
	// 20 posts, 5 errors = 25% error rate
	seedMetrics(c, "agent1", 500, 10*time.Millisecond, 20, 5)

	tuner.mu.Lock()
	tuner.currentParams["agent1"] = TransportParams{Concurrency: 3, BatchSize: 16384}
	tuner.mu.Unlock()

	decision, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision == nil {
		t.Fatal("expected concurrency decrease decision, got nil")
	}
	if decision.NewParams.Concurrency >= decision.OldParams.Concurrency {
		t.Errorf("expected concurrency decrease, got %d → %d", decision.OldParams.Concurrency, decision.NewParams.Concurrency)
	}
}

func TestTuner_Compression_LowBandwidth(t *testing.T) {
	tuner, c := newTestTuner(t)

	// Seed: very low throughput (< 100 KB/s) with no errors
	// 1 post of 500 bytes = 500/10 = 50 B/s
	seedMetrics(c, "agent1", 500, 5*time.Millisecond, 1, 0)

	tuner.mu.Lock()
	tuner.currentParams["agent1"] = TransportParams{Concurrency: 2, BatchSize: 16384, Compress: false}
	tuner.mu.Unlock()

	decision, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision == nil {
		t.Fatal("expected compression enable decision, got nil")
	}
	if !decision.NewParams.Compress {
		t.Error("expected compress=true, got false")
	}
}

func TestTuner_Compression_HighBandwidth(t *testing.T) {
	tuner, c := newTestTuner(t)

	// Seed: high throughput (> 1 MB/s) with compression currently enabled.
	// 150 posts of 80000 bytes = 12M / 10 = 1.2M B/s
	seedMetrics(c, "agent1", 80000, 5*time.Millisecond, 150, 0)

	tuner.mu.Lock()
	tuner.currentParams["agent1"] = TransportParams{Concurrency: 2, BatchSize: maxBatchSize, Compress: true}
	tuner.mu.Unlock()

	decision, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision == nil {
		t.Fatal("expected compression disable decision, got nil")
	}
	if decision.NewParams.Compress {
		t.Error("expected compress=false, got true")
	}
}

func TestTuner_StabilityGuard_MinInterval(t *testing.T) {
	tuner, c := newTestTuner(t)

	seedMetrics(c, "agent1", 7000, 5*time.Millisecond, 20, 0)

	// Set last decision to just now — stability guard should block
	tuner.mu.Lock()
	tuner.lastDecision["agent1"] = time.Now()
	tuner.currentParams["agent1"] = TransportParams{Concurrency: 2, BatchSize: 16384}
	tuner.mu.Unlock()

	decision, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision != nil {
		t.Error("expected nil decision due to stability guard, got non-nil")
	}
}

func TestTuner_OneParamPerEval(t *testing.T) {
	tuner, c := newTestTuner(t)

	// Seed metrics that could trigger multiple changes (high throughput AND high latency)
	seedMetrics(c, "agent1", 7000, 600*time.Millisecond, 20, 0)

	tuner.mu.Lock()
	tuner.currentParams["agent1"] = TransportParams{Concurrency: 2, BatchSize: 16384}
	tuner.mu.Unlock()

	decision, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision == nil {
		t.Fatal("expected a decision, got nil")
	}

	// Count how many parameters changed
	changes := 0
	if decision.NewParams.BatchSize != decision.OldParams.BatchSize {
		changes++
	}
	if decision.NewParams.Concurrency != decision.OldParams.Concurrency {
		changes++
	}
	if decision.NewParams.Compress != decision.OldParams.Compress {
		changes++
	}
	if changes > 1 {
		t.Errorf("expected at most 1 param change, got %d: %v → %v", changes, decision.OldParams, decision.NewParams)
	}
}

func TestTuner_RunStopsOnContext(t *testing.T) {
	tuner, c := newTestTuner(t)
	seedMetrics(c, "agent1", 100, time.Millisecond, 1, 0)

	var pushed sync.Mutex
	var pushCount int
	tuner.pushFn = func(agentID string, params TransportParams) error {
		pushed.Lock()
		pushCount++
		pushed.Unlock()
		return nil
	}
	tuner.interval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	tuner.Run(ctx)

	pushed.Lock()
	// Should have run a few evaluations but not an infinite loop
	_ = pushCount
	pushed.Unlock()
}

func TestTuner_NilCollector(t *testing.T) {
	tuner := &AutoTuner{
		collector:     nil,
		lastDecision:  make(map[string]time.Time),
		underSat:      make(map[string]int),
		currentParams: make(map[string]TransportParams),
	}
	d, err := tuner.Evaluate("agent1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d != nil {
		t.Error("expected nil decision with nil collector")
	}
}
