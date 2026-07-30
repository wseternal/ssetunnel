package metrics

import "time"

// MetricSample is one time-series data point recorded by the collector
// and persisted to BadgerDB. One sample per agent per flush interval.
type MetricSample struct {
	Timestamp    time.Time `json:"timestamp"`
	AgentID      string    `json:"agent_id"`
	BytesUp      uint64    `json:"bytes_up"`
	BytesDown    uint64    `json:"bytes_down"`
	ThroughputUp float64   `json:"throughput_up_bps"`  // bytes/sec
	ThroughputDn float64   `json:"throughput_dn_bps"`  // bytes/sec
	LatencyP50   float64   `json:"latency_p50_ms"`
	LatencyP95   float64   `json:"latency_p95_ms"`
	ErrorRate    float64   `json:"error_rate"`          // 0.0–1.0
	ActiveConns  int       `json:"active_conns"`
}

// TransportParams are the tunable transport parameters that the auto-tuner
// can adjust. Sent to agents via SSE event: tune control frames.
type TransportParams struct {
	Concurrency int  `json:"concurrency"`
	BatchSize   int  `json:"batch_size"`
	Compress    bool `json:"compress"`
}

// TuningDecision records one auto-tuner output with the reasoning behind it.
type TuningDecision struct {
	Timestamp time.Time       `json:"timestamp"`
	AgentID   string          `json:"agent_id"`
	OldParams TransportParams `json:"old_params"`
	NewParams TransportParams `json:"new_params"`
	Reason    string          `json:"reason"`
	Metrics   MetricSnapshot  `json:"metrics"` // snapshot that triggered it
}

// MetricSnapshot is an aggregated view of the rolling window for one agent,
// captured at the moment a tuning decision is evaluated.
type MetricSnapshot struct {
	ThroughputUpP50 float64 `json:"throughput_up_p50_bps"`
	ThroughputUpP95 float64 `json:"throughput_up_p95_bps"`
	ThroughputDnP50 float64 `json:"throughput_dn_p50_bps"`
	ThroughputDnP95 float64 `json:"throughput_dn_p95_bps"`
	LatencyP50Ms    float64 `json:"latency_p50_ms"`
	LatencyP95Ms    float64 `json:"latency_p95_ms"`
	ErrorRate       float64 `json:"error_rate"`
	ActiveConns     int     `json:"active_conns"`
	TotalPosts      int64   `json:"total_posts"`
	TotalErrors     int64   `json:"total_errors"`
}

// AgentMetrics is the per-agent current state returned by the console API.
type AgentMetrics struct {
	AgentID      string          `json:"agent_id"`
	Snapshot     MetricSnapshot  `json:"snapshot"`
	Params       TransportParams `json:"params"`
	LastDecision *TuningDecision `json:"last_decision,omitempty"`
}

// Overview is the global summary returned by the metrics overview endpoint.
type Overview struct {
	ActiveAgents     int     `json:"active_agents"`
	ThroughputUpBps  float64 `json:"throughput_up_bps"`
	ThroughputDnBps  float64 `json:"throughput_dn_bps"`
	ErrorRate        float64 `json:"error_rate"`
}
