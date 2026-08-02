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

### `Config`
| Field | Type | Purpose |
|-------|------|---------|
| `URL` | `string` | Tunnel server base URL (required) |
| `BasePath` | `string` | HTTP path prefix for tunnel endpoints (e.g. `/tunnel`); empty = no prefix |
| `Client` | `*http.Client` | nil → `http.DefaultClient` |
| `Steps` | `int` | Escalation steps from 16 KiB; 0 → 7 |
| `Parallel` | `int` | Phase-3 parallel stream count; 0 → 4 |
| `MaxBody` | `int` | Escalation ceiling; 0 → 2 MiB |

## Rules
* Probes go through `POST /probe`, never `/events` — an `/events` probe would hijack the live agent's yamux session.
* 404 on the first request = clean "unsupported" report, not an error.
* BasePath is prepended to the probe endpoint path (used with `--base` flag).
