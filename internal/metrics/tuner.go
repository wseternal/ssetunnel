package metrics

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Tuner parameter bounds (from spec).
const (
	minBatchSize   = 4 << 10  // 4 KiB floor
	maxBatchSize   = 1 << 20  // 1 MiB ceiling (server max)
	minConcurrency = 1        // serial
	maxConcurrency = 4        // server max
)

// Tuner thresholds (from spec).
const (
	saturationThreshold    = 0.80  // increase batch if throughput > 80% of ceiling
	undersaturationThresh  = 0.30  // decrease batch if throughput < 30% of ceiling
	latencyThresholdMs     = 500.0 // increase concurrency if p95 > 500ms
	errorRateHighThreshold = 0.05  // decrease concurrency if error rate > 5%
	lowBandwidthThreshold  = 100 * 1024.0  // enable gzip if < 100 KB/s
	highBandwidthThreshold = 1024 * 1024.0 // disable gzip if > 1 MB/s
)

// stabilityMinInterval is the minimum time between decisions per agent.
const stabilityMinInterval = 2 * time.Minute

// AutoTuner periodically evaluates each active agent's performance metrics
// and pushes transport parameter adjustments via SSE control frames.
type AutoTuner struct {
	collector *MetricsCollector
	store     *Store
	pushFn    func(agentID string, params TransportParams) error
	interval  time.Duration

	mu           sync.Mutex
	lastDecision map[string]time.Time          // per-agent cooldown
	underSat     map[string]int                // consecutive undersaturation count
	currentParams map[string]TransportParams    // last pushed params per agent
}

// NewAutoTuner creates an AutoTuner that evaluates agents every interval.
// pushFn is called to deliver tuning decisions to agents (via SSE).
// Pass nil for store to skip persisting decisions.
func NewAutoTuner(collector *MetricsCollector, store *Store, pushFn func(string, TransportParams) error, interval time.Duration) *AutoTuner {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &AutoTuner{
		collector:     collector,
		store:         store,
		pushFn:        pushFn,
		interval:      interval,
		lastDecision:  make(map[string]time.Time),
		underSat:      make(map[string]int),
		currentParams: make(map[string]TransportParams),
	}
}

// Run starts the periodic evaluation loop. It blocks until ctx is cancelled.
func (t *AutoTuner) Run(ctx context.Context) {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.evaluateAll()
		}
	}
}

// evaluateAll iterates all active agents and evaluates each.
func (t *AutoTuner) evaluateAll() {
	if t.collector == nil {
		return
	}
	for _, agentID := range t.collector.ActiveAgentIDs() {
		decision, err := t.Evaluate(agentID)
		if err != nil {
			log.Printf("tuner: evaluate %s: %v", agentID, err)
			continue
		}
		if decision == nil {
			continue // no change needed
		}

		// Persist decision
		if t.store != nil {
			if err := t.store.WriteDecision(*decision); err != nil {
				log.Printf("tuner: persist decision: %v", err)
			}
		}

		// Update state
		t.mu.Lock()
		t.lastDecision[agentID] = decision.Timestamp
		t.currentParams[agentID] = decision.NewParams
		t.mu.Unlock()

		// Update collector's param tracking
		t.collector.SetParams(agentID, decision.NewParams)
		t.collector.SetLastDecision(decision)

		// Push to agent
		if t.pushFn != nil {
			if err := t.pushFn(agentID, decision.NewParams); err != nil {
				log.Printf("tuner: push %s: %v", agentID, err)
			}
		}
	}
}

// Evaluate evaluates one agent and returns a tuning decision, or nil if
// no parameter change is warranted.
func (t *AutoTuner) Evaluate(agentID string) (*TuningDecision, error) {
	if t.collector == nil {
		return nil, nil
	}

	snap, ok := t.collector.AgentSnapshot(agentID)
	if !ok {
		return nil, nil // no data
	}

	// Stability guard: skip if too soon since last decision
	t.mu.Lock()
	lastDec := t.lastDecision[agentID]
	current := t.currentParams[agentID]
	underSatCount := t.underSat[agentID]
	t.mu.Unlock()

	if !lastDec.IsZero() && time.Since(lastDec) < stabilityMinInterval {
		return nil, nil
	}

	// Initialize current params if not tracked yet
	if current.BatchSize == 0 {
		current = TransportParams{
			Concurrency: 4,     // default server cap
			BatchSize:   16384, // 16 KiB default
			Compress:    false,
		}
	}

	old := current
	newParams := current
	reason := ""

	// Priority 1: Throughput saturation → adjust batch size
	if snap.ThroughputUpP50 > 0 {
		ceiling := float64(current.BatchSize)
		ratio := snap.ThroughputUpP50 / ceiling
		if ratio > saturationThreshold {
			// Saturated — increase batch size (double, capped)
			newBatch := current.BatchSize * 2
			if newBatch > maxBatchSize {
				newBatch = maxBatchSize
			}
			if newBatch != current.BatchSize {
				newParams.BatchSize = newBatch
				reason = fmt.Sprintf("throughput saturation (%.0f%%): increasing batch size %d → %d", ratio*100, current.BatchSize, newBatch)
			}
			t.mu.Lock()
			t.underSat[agentID] = 0
			t.mu.Unlock()
		} else if ratio < undersaturationThresh {
			// Undersaturated — track consecutive evaluations
			underSatCount++
			t.mu.Lock()
			t.underSat[agentID] = underSatCount
			t.mu.Unlock()

			if underSatCount >= 2 {
				// Decrease batch size (halve, floored)
				newBatch := current.BatchSize / 2
				if newBatch < minBatchSize {
					newBatch = minBatchSize
				}
				if newBatch != current.BatchSize {
					newParams.BatchSize = newBatch
					reason = fmt.Sprintf("throughput undersaturation (%.0f%%, %d evals): decreasing batch size %d → %d", ratio*100, underSatCount, current.BatchSize, newBatch)
				}
				t.mu.Lock()
				t.underSat[agentID] = 0
				t.mu.Unlock()
			}
		} else {
			t.mu.Lock()
			t.underSat[agentID] = 0
			t.mu.Unlock()
		}
	}

	// Priority 2: Latency-driven concurrency (only if batch size wasn't changed)
	if reason == "" {
		if snap.LatencyP95Ms > latencyThresholdMs && current.Concurrency < maxConcurrency {
			newParams.Concurrency = current.Concurrency + 1
			if newParams.Concurrency > maxConcurrency {
				newParams.Concurrency = maxConcurrency
			}
			reason = fmt.Sprintf("high p95 latency (%.0fms > %.0fms): increasing concurrency %d → %d", snap.LatencyP95Ms, latencyThresholdMs, current.Concurrency, newParams.Concurrency)
		} else if snap.ErrorRate > errorRateHighThreshold && current.Concurrency > minConcurrency {
			newParams.Concurrency = current.Concurrency - 1
			if newParams.Concurrency < minConcurrency {
				newParams.Concurrency = minConcurrency
			}
			reason = fmt.Sprintf("high error rate (%.1f%% > %.1f%%): decreasing concurrency %d → %d", snap.ErrorRate*100, errorRateHighThreshold*100, current.Concurrency, newParams.Concurrency)
		}
	}

	// Priority 3: Compression (only if neither batch nor concurrency changed)
	if reason == "" {
		if snap.ThroughputUpP50 < lowBandwidthThreshold && snap.ErrorRate < 0.01 && !current.Compress {
			newParams.Compress = true
			reason = fmt.Sprintf("low bandwidth (%.0f B/s < %.0f B/s): enabling gzip compression", snap.ThroughputUpP50, lowBandwidthThreshold)
		} else if snap.ThroughputUpP50 > highBandwidthThreshold && current.Compress {
			newParams.Compress = false
			reason = fmt.Sprintf("high bandwidth (%.0f B/s > %.0f B/s): disabling gzip compression", snap.ThroughputUpP50, highBandwidthThreshold)
		}
	}

	// No change needed
	if reason == "" || newParams == old {
		return nil, nil
	}

	return &TuningDecision{
		Timestamp: time.Now(),
		AgentID:   agentID,
		OldParams: old,
		NewParams: newParams,
		Reason:    reason,
		Metrics:   snap,
	}, nil
}
