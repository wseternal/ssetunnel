# Probe

Network diagnostics tool. Measures a live tunnel server's POST path characteristics to recommend optimal `--batch-size` and `--concurrency` settings.

## Three-Phase Probe

1. **Size ladder** (Phases 1+2): Escalating POST sizes from 16 KiB doubling up to `MaxBody` (default 2 MiB). Finds the body-size cliff (first rejected size) and RTT-vs-size table.
2. **Throughput** (Phase 3): 1 vs N parallel fixed-size POSTs. Compares single-stream rate to aggregate rate.
3. **Classification**: If parallel scales ≈Nx → per-connection throttle (recommend concurrency). If ≈1x → aggregate cap (concurrency won't help).

## API

```go
func Run(ctx context.Context, cfg Config) (Report, error)
```

`Report.String()` renders a plain-text report ending with a `recommendation:` line.

## Rules
* Probes go through `POST /probe`, never `/events` — an `/events` probe would hijack the live agent's yamux session.
* 404 on the first request = clean "unsupported" report, not an error.
