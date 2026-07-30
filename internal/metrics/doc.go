// Package metrics collects per-agent and per-connect transport metrics,
// persists time-series data to BadgerDB, and runs an auto-tuner that
// adjusts transport parameters (concurrency, batch size, compression)
// based on observed performance. The server pushes tuning decisions to
// agents via SSE control frames (event: tune).
package metrics
