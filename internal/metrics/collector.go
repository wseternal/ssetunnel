package metrics

import (
	"log"
	"sort"
	"sync"
	"time"
)

// postEvent records one upstream POST with its size and round-trip time.
type postEvent struct {
	bytes int
	rtt   time.Duration
}

// rollingWindow holds per-agent raw events within a sliding time window.
// The collector appends events; the flush goroutine aggregates them into
// MetricSamples. Events older than the window are discarded on each flush.
type rollingWindow struct {
	mu         sync.Mutex
	posts      []postEvent // upstream POST events
	sseBytes   uint64      // downstream SSE bytes (accumulated, not per-event)
	connectUp  uint64      // connect upstream bytes
	connectDn  uint64      // connect downstream bytes
	errors     int64       // error count
	activeConn int         // active connection count (gauge)

	// Track window boundaries for pruning
}

// MetricsCollector collects per-agent transport metrics, maintains a
// rolling window for the auto-tuner, and periodically flushes aggregated
// samples to BadgerDB. All recording methods are nil-receiver safe.
type MetricsCollector struct {
	mu            sync.Mutex
	windows       map[string]*rollingWindow
	store         *Store
	flushInterval time.Duration
	retention     time.Duration

	// Current transport params per agent (updated by the tuner via SetParams)
	paramsMu sync.Mutex
	params   map[string]TransportParams

	// Last tuning decision per agent (updated by the tuner via SetLastDecision)
	decisionMu  sync.Mutex
	lastDecision map[string]*TuningDecision

	stopCh chan struct{}
}

// NewCollector creates a MetricsCollector that flushes to store every
// flushInterval and prunes data older than retention.
// Pass nil for store to run in memory-only mode (no persistence).
func NewCollector(store *Store, flushInterval, retention time.Duration) *MetricsCollector {
	if flushInterval <= 0 {
		flushInterval = 10 * time.Second
	}
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	c := &MetricsCollector{
		windows:       make(map[string]*rollingWindow),
		store:         store,
		flushInterval: flushInterval,
		retention:     retention,
		params:        make(map[string]TransportParams),
		lastDecision:  make(map[string]*TuningDecision),
		stopCh:        make(chan struct{}),
	}
	go c.flushLoop()
	return c
}

// Close stops the flush loop.
func (c *MetricsCollector) Close() {
	if c == nil {
		return
	}
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
}

// flushLoop runs the periodic flush and prune cycle.
func (c *MetricsCollector) flushLoop() {
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.flush()
			if c.store != nil {
				if err := c.store.PruneOlderThan(c.retention); err != nil {
					log.Printf("metrics: prune: %v", err)
				}
			}
		}
	}
}

// flush aggregates each agent's rolling window into a MetricSample and
// persists it. Also persists the rolling window state for tuner recovery.
func (c *MetricsCollector) flush() {
	c.mu.Lock()
	// Snapshot and prune windows
	now := time.Now()
	var samples []MetricSample
	for agentID, w := range c.windows {
		w.mu.Lock()

		sample := c.aggregateWindow(agentID, w, now)
		w.mu.Unlock()

		if sample != nil {
			samples = append(samples, *sample)
		}
	}
	c.mu.Unlock()

	if len(samples) > 0 {
		if err := c.store.WriteSamples(samples); err != nil {
			log.Printf("metrics: flush write: %v", err)
		}
	}

	// Reset all rolling windows after aggregation so each flush
	// interval produces an independent sample. Active connection
	// gauges are preserved across resets.
	c.mu.Lock()
	for _, w := range c.windows {
		w.mu.Lock()
		w.posts = w.posts[:0]
		w.sseBytes = 0
		w.connectUp = 0
		w.connectDn = 0
		w.errors = 0
		w.mu.Unlock()
	}
	c.mu.Unlock()
}

// aggregateWindow computes a MetricSample from a rolling window.
// Must be called with w.mu held.
func (c *MetricsCollector) aggregateWindow(agentID string, w *rollingWindow, now time.Time) *MetricSample {
	if len(w.posts) == 0 && w.sseBytes == 0 {
		return nil
	}

	intervalSec := c.flushInterval.Seconds()
	if intervalSec <= 0 {
		intervalSec = 10
	}

	// Aggregate POST metrics
	var totalUpBytes uint64
	rtts := make([]float64, 0, len(w.posts))
	for _, p := range w.posts {
		totalUpBytes += uint64(p.bytes)
		rtts = append(rtts, float64(p.rtt.Milliseconds()))
	}

	// Compute latency percentiles
	sort.Float64s(rtts)
	p50 := percentile(rtts, 0.50)
	p95 := percentile(rtts, 0.95)

	// Error rate
	totalReqs := int64(len(w.posts))
	var errRate float64
	if totalReqs > 0 {
		errRate = float64(w.errors) / float64(totalReqs)
	}

	return &MetricSample{
		Timestamp:    now,
		AgentID:      agentID,
		BytesUp:      totalUpBytes,
		BytesDown:    w.sseBytes + w.connectDn,
		ThroughputUp: float64(totalUpBytes+w.connectUp) / intervalSec,
		ThroughputDn: float64(w.sseBytes+w.connectDn) / intervalSec,
		LatencyP50:   p50,
		LatencyP95:   p95,
		ErrorRate:    errRate,
		ActiveConns:  w.activeConn,
	}
}

// percentile returns the p-th percentile from a sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p * float64(len(sorted)-1)
	lo := int(idx)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// --- Recording methods (all nil-receiver safe) ---

// RecordAgentPost records one upstream POST from an agent.
func (c *MetricsCollector) RecordAgentPost(agentID string, bytes int, rtt time.Duration) {
	if c == nil || agentID == "" {
		return
	}
	w := c.getOrCreateWindow(agentID)
	w.mu.Lock()
	w.posts = append(w.posts, postEvent{bytes: bytes, rtt: rtt})
	w.mu.Unlock()
}

// RecordAgentSSEBytes records downstream SSE bytes sent to an agent.
func (c *MetricsCollector) RecordAgentSSEBytes(agentID string, bytes int) {
	if c == nil || agentID == "" {
		return
	}
	w := c.getOrCreateWindow(agentID)
	w.mu.Lock()
	w.sseBytes += uint64(bytes)
	w.mu.Unlock()
}

// RecordConnectBytes records connect client traffic for an agent.
func (c *MetricsCollector) RecordConnectBytes(agentID string, up, down int) {
	if c == nil || agentID == "" {
		return
	}
	w := c.getOrCreateWindow(agentID)
	w.mu.Lock()
	w.connectUp += uint64(up)
	w.connectDn += uint64(down)
	w.mu.Unlock()
}

// RecordSessionStart increments the active connection count for an agent.
func (c *MetricsCollector) RecordSessionStart(agentID string) {
	if c == nil || agentID == "" {
		return
	}
	w := c.getOrCreateWindow(agentID)
	w.mu.Lock()
	w.activeConn++
	w.mu.Unlock()
}

// RecordSessionEnd decrements the active connection count for an agent.
func (c *MetricsCollector) RecordSessionEnd(agentID string) {
	if c == nil || agentID == "" {
		return
	}
	w := c.getOrCreateWindow(agentID)
	w.mu.Lock()
	if w.activeConn > 0 {
		w.activeConn--
	}
	w.mu.Unlock()
}

// RecordError records an error event for an agent.
func (c *MetricsCollector) RecordError(agentID string, kind string) {
	if c == nil || agentID == "" {
		return
	}
	w := c.getOrCreateWindow(agentID)
	w.mu.Lock()
	w.errors++
	w.mu.Unlock()
}

// getOrCreateWindow returns the rolling window for an agent, creating it if needed.
func (c *MetricsCollector) getOrCreateWindow(agentID string) *rollingWindow {
	c.mu.Lock()
	w, ok := c.windows[agentID]
	if !ok {
		w = &rollingWindow{}
		c.windows[agentID] = w
	}
	c.mu.Unlock()
	return w
}

// --- Query methods for the console API ---

// Overview returns a global summary across all active agents.
func (c *MetricsCollector) Overview() Overview {
	if c == nil {
		return Overview{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var o Overview
	o.ActiveAgents = len(c.windows)
	for _, w := range c.windows {
		w.mu.Lock()
		intervalSec := c.flushInterval.Seconds()
		o.ThroughputUpBps += float64(w.connectUp) / intervalSec
		for _, p := range w.posts {
			o.ThroughputUpBps += float64(p.bytes) / intervalSec
		}
		o.ThroughputDnBps += float64(w.sseBytes+w.connectDn) / intervalSec
		if len(w.posts) > 0 {
			o.ErrorRate += float64(w.errors) / float64(len(w.posts))
		}
		w.mu.Unlock()
	}
	if o.ActiveAgents > 0 {
		o.ErrorRate /= float64(o.ActiveAgents)
	}
	return o
}

// AllAgentMetrics returns per-agent current metrics for the console API.
func (c *MetricsCollector) AllAgentMetrics() []AgentMetrics {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	agentIDs := make([]string, 0, len(c.windows))
	for id := range c.windows {
		agentIDs = append(agentIDs, id)
	}
	c.mu.Unlock()

	result := make([]AgentMetrics, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		w := c.getOrCreateWindow(agentID)
		w.mu.Lock()
		snap := c.snapshotWindow(w)
		w.mu.Unlock()

		c.paramsMu.Lock()
		params := c.params[agentID]
		c.paramsMu.Unlock()

		c.decisionMu.Lock()
		lastDec := c.lastDecision[agentID]
		c.decisionMu.Unlock()

		result = append(result, AgentMetrics{
			AgentID:      agentID,
			Snapshot:     snap,
			Params:       params,
			LastDecision: lastDec,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].AgentID < result[j].AgentID
	})
	return result
}

// AgentSnapshot returns the current MetricSnapshot for one agent.
// Used by the tuner for evaluation.
func (c *MetricsCollector) AgentSnapshot(agentID string) (MetricSnapshot, bool) {
	if c == nil {
		return MetricSnapshot{}, false
	}
	c.mu.Lock()
	w, ok := c.windows[agentID]
	c.mu.Unlock()
	if !ok {
		return MetricSnapshot{}, false
	}
	w.mu.Lock()
	snap := c.snapshotWindow(w)
	w.mu.Unlock()
	return snap, true
}

// ActiveAgentIDs returns the IDs of all agents with rolling windows.
func (c *MetricsCollector) ActiveAgentIDs() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.windows))
	for id := range c.windows {
		ids = append(ids, id)
	}
	return ids
}

// Store returns the underlying BadgerDB store for direct queries (e.g., from console API).
func (c *MetricsCollector) Store() *Store {
	if c == nil {
		return nil
	}
	return c.store
}

// SetParams updates the current transport params for an agent (called by tuner).
func (c *MetricsCollector) SetParams(agentID string, params TransportParams) {
	if c == nil {
		return
	}
	c.paramsMu.Lock()
	c.params[agentID] = params
	c.paramsMu.Unlock()
}

// GetParams returns the current transport params for an agent.
func (c *MetricsCollector) GetParams(agentID string) TransportParams {
	if c == nil {
		return TransportParams{}
	}
	c.paramsMu.Lock()
	p := c.params[agentID]
	c.paramsMu.Unlock()
	return p
}

// SetLastDecision records the last tuning decision for an agent (called by tuner).
func (c *MetricsCollector) SetLastDecision(d *TuningDecision) {
	if c == nil || d == nil {
		return
	}
	c.decisionMu.Lock()
	c.lastDecision[d.AgentID] = d
	c.decisionMu.Unlock()
}

// snapshotWindow computes a MetricSnapshot from a rolling window.
// Must be called with w.mu held.
func (c *MetricsCollector) snapshotWindow(w *rollingWindow) MetricSnapshot {
	intervalSec := c.flushInterval.Seconds()
	if intervalSec <= 0 {
		intervalSec = 10
	}

	var totalUp uint64
	rtts := make([]float64, 0, len(w.posts))
	for _, p := range w.posts {
		totalUp += uint64(p.bytes)
		rtts = append(rtts, float64(p.rtt.Milliseconds()))
	}
	sort.Float64s(rtts)

	throughputUp := float64(totalUp+w.connectUp) / intervalSec
	totalReqs := int64(len(w.posts))
	var errRate float64
	if totalReqs > 0 {
		errRate = float64(w.errors) / float64(totalReqs)
	}

	// NOTE: ThroughputUp/Dn are aggregate rates over the current flush
	// interval, not statistical percentiles. The P50/P95 field names in
	// MetricSnapshot are kept for API compatibility but hold the same
	// aggregate value.
	return MetricSnapshot{
		ThroughputUpP50: throughputUp,
		ThroughputUpP95: throughputUp,
		ThroughputDnP50: float64(w.sseBytes+w.connectDn) / intervalSec,
		ThroughputDnP95: float64(w.sseBytes+w.connectDn) / intervalSec,
		LatencyP50Ms:    percentile(rtts, 0.50),
		LatencyP95Ms:    percentile(rtts, 0.95),
		ErrorRate:       errRate,
		ActiveConns:     w.activeConn,
		TotalPosts:      totalReqs,
		TotalErrors:     w.errors,
	}
}
