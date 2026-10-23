# Cloud Shell Freeze After `vim` on Large Files — Root-Cause Analysis

**Date:** 2026-10-23
**Status:** Investigation complete — no code changes yet
**Method:** Doubt-driven development — two independent fresh-context adversarial
reviews (server-side, agent+frontend), every finding re-classified against the
source by the orchestrator.

---

## 1. Symptom

In the console's cloud shell, after connecting to a particular agent and running
`vim` on a file with hundreds of lines, the shell **very often** becomes
completely unresponsive: keystrokes produce no visible reaction and the terminal
never recovers. The only way out is reloading the console page.

Three prior fix attempts did not resolve it:

| Commit | Change | Layer patched |
|---|---|---|
| #67 | Batch shell input via rAF + detect HTTP errors | browser↔server input scheduling |
| #78 | Single-flight immediate input sender (replaced rAF) | browser↔server input scheduling |
| #79 | SSE parser: emit only terminated frames | browser↔server wire format |

**Why none of them worked:** all three patched the browser↔server wire path.
The actual failure lives one level down: the vim output burst kills the
**agent session**, and the architecture converts *any* session death into a
**permanent, silent browser freeze** (no close signal, no watchdog, no
auto-reconnect). Neither layer was touched by those fixes.

---

## 2. Cloud Shell Data Path (reference)

```
Browser (xterm.js v6)
  │  GET  /console/api/v1/shell/connect        (SSE: base64 data frames + ": ka" heartbeats)
  │  POST /console/api/v1/shell/connect-up     (keystrokes, single-flight)
  │  POST /console/api/v1/shell/resize         (window size, fire-and-forget)
  ▼
Server — ShellSession (internal/server/shell_session.go)
  • outputConsumer: yamux stream → SSE frame (also feeds 256 KiB ring buffer)
  • inputForwarder: up-pipe (1 MiB) → yamux stream          [writeMu]
  • resizeForwarder: resizeCh → NUL-prefixed JSON → stream  [writeMu]
  ▼  yamux over HTTP transport (SSE-down + batched-POST-up)
Agent (internal/agent/shell_unix.go)
  • forwardStreamToPTY: stream → PTY master, byte-at-a-time,
    0x00 dispatches resize JSON terminated by '\n'
  • io.Copy(stream, ptmx): PTY output → stream
  ▼
Shell + vim (raw PTY)
```

Key transport facts:

- yamux v0.1.2: single `sendLoop` per session writes frames **FIFO** to the
  conn; `KeepAliveInterval = 30 s` (set in `internal/mux/mux.go`), but
  `ConnectionWriteTimeout` is **never overridden → 10 s default** for both the
  ping-pong timeout and the `waitForSend` enqueue/write timeout.
- Agent batcher queue cap: `DefaultMaxQueuedBytes = 4 MiB`
  (`internal/transport/batcher.go`); yamux stream window: 4 MiB
  (`internal/mux/mux.go`).
- Auto-tuner (`internal/metrics/tuner.go`) can raise batch size 4 KiB → 1 MiB
  and concurrency 1 → 4 (2-minute cooldown, per agent).
- Both HTTP servers deliberately set `WriteTimeout: 0` so SSE is never killed
  (`internal/server/server.go:160`, `cmd/ssetunnel/main.go:251`) — which also
  means **no write deadline exists anywhere on the SSE paths**.

---

## 3. The Failure Model

The freeze is not one bug. It is a stack:

> **Trigger layer** — a vim burst (or a single keystroke) kills or wedges the
> session.
> **Permanence layer** — the browser is never told; the terminal freezes
> silently and forever.
> **Aggravator layer** — queueing and scheduling bugs make the shell feel dead
> for seconds at a time even before anything actually breaks.

### 3.1 Trigger T1 — yamux keepalive is miscalibrated for a store-and-forward transport (PRIMARY)

`internal/mux/mux.go` sets `KeepAliveInterval = 30 s` but leaves yamux's
`ConnectionWriteTimeout` at its **10 s default**. Two consequences:

1. **Pong starvation.** The server's yamux pings every 30 s; the pong on the
   agent travels `yamux sendLoop → transport.Conn.Write → batcher`, queued
   **FIFO behind up to 4 MiB of buffered vim redraw output**
   (`batcher.go:78-79`, `DefaultMaxQueuedBytes` at `batcher.go:19`). On a
   constrained proxy draining at ≤ ~400 KiB/s, a deep queue cannot drain within
   10 s even though every POST succeeds. The server's `Ping()` times out →
   yamux raises `ErrKeepAliveTimeout` → **the entire agent session is torn
   down** (all streams, not just the shell).
2. **Waiter timeouts.** `waitForSendErr` gives every frame-writer (stream
   writes, window updates, resize writes, pong replies) only 10 s when the
   single `sendLoop` is blocked. A blocked `sendLoop` (agent side: stuck in
   `batcher.Write` on a full queue; server side: stuck in `Session.Write` on
   the 256 KiB down pipe when the agent's SSE stalls) → writers fail with
   `ErrConnectionWriteTimeout` → `inputForwarder`/`outputConsumer` return →
   `defer ss.Close()` (`shell_session.go:248,349`).

The symmetric agent-side ping dies the same way (its request leg queues behind
the same batcher backlog).

**Auto-tune aggravation:** the tuner raises batch size up to 1 MiB (each POST
slower on a slow link) and concurrency up to 4, which arms the windowed
upstream path — where a single >25 s straggler POST triggers
`ErrGapTimeout → 409` (`internal/transport/reorder.go`,
`internal/server/session.go:181-198`) → agent fail-fast → session death.
This explains *"a certain agent"*: the one on the worst link that the tuner
has been managing.

**Evidence:** `internal/mux/mux.go:11-24`; yamux `session.go` (`keepalive`,
`Ping`, `waitForSendErr`, `sendLoop`) in module cache
`github.com/hashicorp/yamux@v0.1.2`; `internal/transport/batcher.go:19,78-79`;
`internal/transport/conn.go` (`post`, `submit`, `worker`).

**Production confirmation:** server stderr shows
`[ERR] yamux: keepalive failed: ...` followed by
`shell: agent session died, closing shell ...`; the agent logs
`agent: session ended; reconnecting` and reconnects within seconds — which
makes the console show the agent as "connected", masking the death.

### 3.2 Trigger T2 — Ctrl-D (vim's half-page-down) disconnects and DELETES the session

`frontend/console/src/App.tsx:656-659`: `trackLineInput` treats **any** `\x04`
byte as "EOF — disconnect immediately" (300 ms timer), regardless of context.
`disconnectShell` additionally **deletes the persistent server session**
(`App.tsx:524-533`), so reattach is impossible.

Ctrl-D is vim's half-page-down key — the natural way to scroll a file with
hundreds of lines. The frontend interprets bytes addressed to the remote PTY
as local commands. Same class of bug: `exit`/`logout` line-matching
(`App.tsx:651-653`) fires inside REPLs, `psql`, nested SSH, etc.

### 3.3 Trigger T3 — NUL-byte input black-hole in the agent

`internal/agent/shell_unix.go:115-146`: `forwardStreamToPTY` dispatches any
`0x00` byte into `readAndApplyResize`, which consumes bytes until `'\n'`
(0x0A). The design comment — *"NUL (\x00) cannot be generated by a terminal
keyboard"* (`shell_unix.go:67-68`) — is **false**:

- xterm.js emits `\x00` for **Ctrl-Space** (the default IME-toggle shortcut on
  CJK systems — highly likely to be pressed accidentally) and Ctrl-@.
- In a raw-mode PTY, Enter produces `\r` (0x0D), not `\n` — so after one stray
  NUL, the parser swallows **all subsequent keystrokes** into an unbounded
  buffer until a literal 0x0A happens by accident (Ctrl-J, pasted LF).

Output keeps rendering; input is black-holed; no timeout, no length cap, no
escape hatch. Screen alive, keyboard dead, never recovers.

### 3.4 Permanence P1 — session death never reaches the browser (CORE DEFECT)

`internal/server/shell.go:159` (`shellReattach`) and `shell.go:264`
(`shellCreate`): the SSE handler parks solely on `<-r.Context().Done()` — it
**never selects on `ss.Done()`**.

When the ShellSession dies (stream error → `outputConsumer` exit; agent-death
monitor at `shell.go:229-236`), `Close()` nils the writer and closes pipes,
but nothing writes to or terminates the already-flushed SSE HTTP response.
Headers were sent, so no error status can be conveyed either. Result:

- The browser's SSE connection stays **open and silent forever** — and even
  heartbeats stop, because `outputConsumer` (exited) was the only heartbeat
  writer.
- The frontend reader loop (`App.tsx:782-784`) has **no timeout, no inactivity
  watchdog, no reconnect**. Heartbeats are SSE comments (`: ka\n\n`) and are
  filtered out by `parseSSEEvents` (`frontend/console/src/lib/sse.ts:40`), so
  a watchdog doesn't even have a signal to consume today.
- Input POSTs still find the stale `connectSessions` entry (deleted only when
  the SSE handler returns) and get `409` from the closed up-pipe
  (`handlers.go:662-665`).
- Refresh works because `FindByUserAgent` skips closed sessions
  (`shell_session.go:445`) and a fresh session is created.

This converts *every* trigger in §3.1–3.3 into the identical permanent,
silent freeze.

### 3.5 Permanence P2 — silent detach / wedged writer: the zero-feedback variant

- **Silent detach.** If the browser SSE write ever fails
  (`shell_session.go:280-290`), the session silently detaches
  (`sseWriter = nil`): output goes to the 256 KiB ring buffer only, input
  POSTs keep returning **200**, and keystrokes execute *blind* on the remote
  shell. No error text at all — the exact "no feedback" presentation with a
  *live* session.
- **Wedged writer.** `outputConsumer` writes frames directly to the
  `http.ResponseWriter` with no deadline (both HTTP servers set
  `WriteTimeout: 0`). A half-open browser connection (laptop sleep, VPN flap,
  middlebox backpressure) blocks this goroutine indefinitely: the yamux stream
  is never read (window fills, PTY output blocks), heartbeats stop, the ring
  buffer stops being fed, and `inputForwarder` keeps working — input executes
  blind. External `Close()` cannot interrupt the blocked `WriteFrame`; the
  goroutine leaks until TCP eventually errors.

### 3.6 Permanence P3 — frontend has no failure taxonomy

The frontend maps hung / failed / closed / desynced onto "print a colored
line and keep accepting keystrokes" (or silently drop them):

- A hung input POST keeps `inFlight = true` forever (`App.tsx:674-712`); all
  later keystrokes accumulate in a closure-local `pending` string and are
  never sent. No error surfaced. (Server bounds the pipe write at 25 s, so
  this needs a network-level hang.)
- A failed input POST **drops the bytes** (`App.tsx:683-700`) — a byte range
  lost mid escape sequence or bracketed paste leaves vim waiting for bytes
  that will never come: a permanently desynced TUI that looks frozen.
- No auto-reconnect anywhere; reload is the only recovery.

---

## 4. Aggravators (feel dead for seconds even when healthy)

| # | Finding | Evidence | Effect |
|---|---|---|---|
| A1 | **Recurring 15 s output stall** (verified): after every read timeout, `outputConsumer` sleeps a full extra heartbeat before re-reading — even while attached. `wakeup` fires only on `Attach`. | `shell_session.go:327-333`, heartbeat default 15 s (`cmd/ssetunnel/main.go:111`) | After any 15 s idle gap, echo/output can be delayed up to 15 s more. Repeated "dead for seconds" episodes. |
| A2 | **`Attach` replay gap/duplication**: ring drained under `mu`, replay written unlocked, writer installed after. Bytes arriving in that window go to the ring only — a live gap now, stale duplicates on next reattach; a gap mid-escape-sequence corrupts terminal parser state. | `shell_session.go:153-171` | Corrupt/flickery scrollback after reattach during active output. |
| A3 | **`Session.mu` held across a blocking 25 s pipe write** in `push`/`pushWindowed`. With concurrency=4, one slow batch serializes all POST handlers. | `session.go:150-198` | POST latency inflation under congestion → provokes gap timeouts → session death (feeds T1). |
| A4 | **Echo-behind-bulk**: ~8 MiB aggregate queue (4 MiB batcher + 4 MiB yamux window + pipes + xterm buffer). Echo sits behind the entire pending redraw; vim redraws per keystroke, replenishing the queue. | `batcher.go`, `mux.go`, `conn.go:24` | Self-sustaining input lag on slow links while scrolling. |
| A5 | **No browser-side backpressure**: the reader loop enqueues into xterm's `setTimeout`-paced WriteBuffer as fast as the network delivers; sustained floods grow it unboundedly (GC jank). When the browser's stream queue is pre-filled, the `await reader.read()` microtask chain can starve the macrotask-based parser entirely during catch-up. | `App.tsx:782-803`, xterm.js v6 `WriteBuffer._innerWrite` via `setTimeout` | Display freezes during catch-up dumps; tab jank on sustained floods. |
| A6 | **`resizeForwarder` dies silently on first write error** (no `ss.Close()`, unlike the other two forwarders); resize handling is dead for the session's remaining life while resize POSTs keep returning 200. | `shell_session.go:390-395`, `handlers.go:709-719` | PTY size stuck after a transient error. |
| A7 | **Leaked resources after death-while-attached**: handler goroutine parked on `r.Context().Done()` + stale `connectSessions` entry survive indefinitely (a different map than the one `CleanupIdle` reaps). | `shell.go:138-141,258-264` | One goroutine + map entry + open connection leaked per freeze. |

---

## 5. Findings considered and rejected

| Candidate | Verdict |
|---|---|
| xterm.js synchronized-output mode (`ESC[?2026h/l`) sticking forever | **Ruled out** — xterm.js v6 has a 1 s auto-exit timeout for sync mode (verified in the bundled `xterm.mjs`). A lost end-marker costs ~1 s, not a permanent freeze. |
| yamux `recvLoop` head-of-line blocking across streams | **Ruled out** — `Stream.readData` buffers into the per-stream `recvBuf` without blocking on the application reader (verified in yamux@v0.1.2 source). Flow control is sender-side only. |
| Browser HTTP/1.1 connection-pool exhaustion starving input POSTs | **Ruled out** — only ~2 long-lived requests exist on the shell tab (SSE + at most one in-flight POST). |
| Reconnect race: stale `finally` disposes the new connection's input handler (`App.tsx:819-822`) | **Low confidence** — the aborted connection's `finally` runs at the new connection's first `await` (`fetchShellSessions`), before the new handler is registered; the exploitable window is thin. Real asymmetry worth fixing defensively, not a primary. |
| Resize/input frame interleaving corrupting the NUL protocol | **Noise** — `writeMu` serializes whole frames on the yamux stream (`shell_session.go:364,393`); atomicity holds. The real NUL hazard is T3 (collision with user-generated `\x00`). |
| Frontend SSE parser frame-splitting bugs | **Already fixed in #79**; parser now emits only `\n\n`-terminated frames with contract tests. |

---

## 6. Reproduction Recipe (trigger T1)

1. Throttle the agent↔server link to ~100 KB/s (e.g. `tc`, or a bandwidth-
   limiting proxy in front of the agent).
2. Open the cloud shell to that agent; run `vim` on a large, syntax-highlighted
   file.
3. Hold `j` to scroll continuously (each keystroke triggers a redraw,
   sustaining the upstream burst).
4. Within ~30-60 s the next yamux keepalive tick times out
   (`ErrKeepAliveTimeout`) → session torn down → browser SSE orphaned →
   permanently frozen terminal; only reload recovers.

Expected log lines: `[ERR] yamux: keepalive failed` (server stderr),
`shell: agent session died, closing shell ...` (server),
`agent: session ended; reconnecting` (agent).

---

## 7. Fix Directions (ranked, not yet implemented)

### F1 — Close the death→browser gap (permanence P1) — highest value
- In `shellCreate`/`shellReattach`, `select` on `ss.Done()` in addition to
  `r.Context().Done()`; when the session dies, terminate the SSE response
  (return from the handler) so the browser's reader gets `done`.
- Frontend: treat stream end while `shellConnected` as a death signal and
  auto-reattach (the reattach path with ring-buffer replay already exists),
  with a visible "[reconnecting…]" notice.
- Optional hardening: emit a terminal SSE event (e.g. `event: closed`) before
  closing, and add a client-side watchdog: no data *and* no heartbeat for
  3× the heartbeat interval → consider dead → reattach. (Heartbeats exist on
  the wire today but are filtered by the frontend parser; the watchdog needs
  raw-chunk timestamps, not parsed events.)

### F2 — Fix the keepalive/queue miscalibration (trigger T1)
- Set `ConnectionWriteTimeout` in `internal/mux/mux.go` well above worst-case
  control-frame latency (e.g. 60-120 s), and/or lengthen `KeepAliveInterval`.
- Structural alternative: prioritize yamux control frames (pings/pongs, window
  updates) over bulk data — e.g. a separate control channel in the transport
  or a much smaller `MaxQueuedBytes` so control latency stays bounded.
- Revisit auto-tune's 1 MiB batch ceiling for constrained links (larger
  batches = longer POSTs = longer control latency).

### F3 — Remove input interception from the raw PTY stream (trigger T2)
- Delete the Ctrl-D / `exit` / `logout` handling in `trackLineInput`
  (`App.tsx:644-666`). The UI already has explicit disconnect affordances; a
  raw PTY byte stream cannot safely support shell-command sniffing.

### F4 — Replace the NUL-delimited resize protocol (trigger T3)
- Use a length-prefixed control frame (or escape NUL in the data path), so
  user-generated `\x00` can never enter the resize parser.
- Minimum mitigation: bound `readAndApplyResize` with a max message length +
  read deadline so a stray NUL costs at most a bounded hiccup.

### F5 — `outputConsumer` stall (aggravator A1)
- While attached, do not sleep after sending a heartbeat; loop back to the
  deadline-bounded read immediately. Keep the `wakeup` wait only for the
  detached case.

### F6 — `Attach` atomicity (aggravator A2)
- Make the ring-drain → writer-install transition atomic (single lock window)
  so no live bytes are skipped or duplicated across reattach.

### F7 — Input path robustness (permanence P3)
- Add a timeout to the input POST; on hang/failure, surface a hard error and
  re-reattach rather than silently accumulating `pending`.
- Never silently drop input bytes; on POST failure either retry or tear down
  the session visibly.

### F8 — Deadline hygiene (permanence P2)
- Introduce a bounded write for SSE frames (e.g. via
  `http.NewResponseController(w).SetWriteDeadline` refreshed per write) so a
  wedged browser connection errors out and is detached/closed detectably,
  instead of blocking `outputConsumer` forever.
- Give agent-side POSTs a write deadline (`transport.Conn.SetWriteDeadline`)
  so a black-holed upload fails fast instead of hanging the batcher forever.

### Repo conventions for the fix
- Test-first for bugs (write a reproduction test before fixing — e.g. a test
  that kills an agent session while a shell is attached and asserts the SSE
  response terminates).
- The served console is the embedded bundle `frontend/console/dist/` — any
  frontend change must rebuild the bundle (`fe:test` / build tasks exist in
  `Taskfile.yml`).

---

## 8. Doubt-Cycle Metadata

- **Reviewers:** 2 fresh-context adversarial sub-agents (server-side scope;
  agent+frontend scope), each given ARTIFACT + CONTRACT only — no orchestrator
  conclusions.
- **Reconciliation:** every finding re-classified against the source
  (valid+actionable / trade-off / noise / contract-misread). Load-bearing
  claims (P1, T1, T3, A1) independently re-verified by the orchestrator in the
  code and in the vendored yamux v0.1.2 / xterm.js v6 bundles.
- **Cross-model escalation:** offered to the user; pending decision at the
  time of writing.
