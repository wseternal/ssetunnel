# RCA-001: Cloud Shell Output Freeze While Input Keeps Working

## Summary

The cloud shell terminal freezes after vim opens. The screen stops updating. Keystrokes produce no visible change. The page looks dead until the user reloads it.

**Confirmed root cause (from the browser stack trace): a frontend build bug.** esbuild's default build target miscompiles one of xterm.js's TypeScript enums, so the first time vim sends a DECRQM query, xterm throws `ReferenceError: r is not defined` inside its async write loop. That permanently wedges the write buffer, so the screen freezes while keystrokes keep reaching vim. Fixed by setting `build.target: 'es2022'` (commit `94dc3b5`).

A second, separate defect was found and fixed along the way: the server's SSE output path could detach a dead browser writer silently and leave the HTTP response open forever. That is a real permanence bug, but it was **not** the cause of this deterministic vim freeze. It is documented in the "Separate hardening" section.

## Confirmed root cause — xterm enum miscompile

The decisive evidence was a browser console stack trace captured while frozen:

```
Uncaught ReferenceError: r is not defined
    at Qte.requestMode (…/console/:281:103743)
    at Vte.parse (…)      ← xterm escape-sequence parser
    at ere._innerWrite (…) ← xterm async write loop
```

Inspecting the built bundle showed the defect. esbuild, at the default target with minify on, emitted xterm's function-local `RequestModeResult` enum as:

```js
requestMode(e,t){ (g=>(g[g.NOT_RECOGNIZED=0]=…))(void 0||(r={})); … }
```

`r` is assigned but **never declared**. The bundle is a strict-mode ES module, so the first DECRQM (`ESC [ ? … $ p`) — which vim 9 sends during startup mode detection — runs `requestMode`, which throws. The throw happens inside `_innerWrite`, xterm's scheduled write consumer. xterm's write buffer keeps its "in progress" flag, so every later `term.write()` is enqueued but never parsed. The painted screen and the internal buffer both stop advancing. Input still works because keystrokes use a separate `onData` → POST path, so vim keeps editing (and keeps writing its swap file).

Why it looked like a transport bug:
- The reload replay also stalled, because the fresh xterm instance re-parsed the same bad sequence.
- `window.__shellDbg` showed `writes` advancing while the screen was frozen — `term.write` was being *called*; xterm just never drained it.
- `term.refresh(...)` did nothing — the buffer, not the painter, was the stalled stage.

The fix keeps minification and the same bundle size (1.3 MB / 378 KB gzip) and eliminates every miscompiled enum site in the bundle (verified: 0 remaining).

## Separate hardening — server SSE writer permanence

Independent of the browser bug, `ShellSession.outputConsumer` used to detach a failed browser writer silently while `shellCreate`/`shellReattach` kept the HTTP response open. On a genuinely half-open SSE (laptop sleep, NAT/proxy drop) that is a permanent freeze with no client-side detection. Fixed in `7fffac9`/`42e8116`: `Attach` returns a per-attach `clientLost` channel, the handlers select on it and end the response, and the frontend watchdog reconnects after `max(60 s, 4 × X-SSET-Heartbeat-Ms)` of silence with `takeover=1`. This is correct hardening for a real failure mode, but it was not what made `vim .zprofile` freeze every time.

## Symptom

- The user opens the console and connects the cloud shell to an agent.
- The user runs `vim .zprofile`.
- vim draws the file correctly.
- After that, no key press changes the screen.
- The terminal never recovers. A page reload works.

## Field evidence that decided the direction

The user collected three facts from a live incident:

1. The vim process was alive (`ps aux`).
2. vim received the keystrokes. The swap file, recovered with `vim -r`, contained the typed text inside the file buffer.
3. The screen showed no change at all.

Fact 2 proves the input path works end to end: browser POST → server up-pipe → yamux stream → agent PTY → vim. Fact 3 proves the failure is on the output path only. This narrowed the search to one direction before any code changed.

## Data path (reference)

```
Input  (works during the freeze):
  browser ──POST /connect-up──▶ up-pipe ──▶ yamux stream ──SSE /events──▶ agent ──▶ PTY ──▶ vim

Output (dies during the freeze):
  vim ──▶ PTY ──▶ agent ──yamux──▶ POST /up ──▶ Session ──▶ yamux stream
      ──▶ ShellSession.outputConsumer ──SSE /shell/connect──▶ browser ──▶ xterm.js
```

The two directions use different HTTP connections. One can fail while the other works. Nothing joins them into one liveness signal.

## Separate defect — server SSE writer permanence (hardened, not this freeze)

This was found first and is a real permanence bug, but it is **not** what froze `vim .zprofile` every time. It turns any genuine output-path break into a permanent freeze. Three facts combine:

### 1. Silent detach on writer failure

`ShellSession.outputConsumer` writes SSE frames to the attached browser writer. On any write error, or when the 30 s per-frame write deadline expires, it calls `detachWriter`. That clears `sseWriter`, `flusher`, and `rc`. Output then goes only to the 256 KiB ring buffer. The session stays alive on purpose, so a later client can reattach.

The bug is what does not happen: nobody tells the browser.

### 2. The handler parks on an open response

`shellCreate` and `shellReattach` block on `r.Context().Done()` or `ss.Done()`. A detached writer triggers neither. The handler keeps the HTTP response open. The browser's fetch reader waits for data that never comes, and it never sees an end. The `event: closed` frame fires only for session death, never for client-writer failure.

### 3. The client has no liveness check

The frontend reader loop has no inactivity timeout. It filters heartbeat comments out of the event stream, so heartbeats never reach a liveness check either. A healthy stream delivers a heartbeat every 15 s. A dead stream delivers nothing, and the browser waits forever.

### Why it is permanent

A half-open TCP connection produces no error on either side. This happens after laptop sleep, a network switch, or a NAT/proxy that drops long-lived connection state. New POST connections still work, so input keeps flowing. The server's small heartbeat writes are accepted by its own kernel for a long time, so the server may never see a write error. The browser sees silence. The state never self-heals.

## Reproduction (local, deterministic) — of the permanence bug

A real server and agent ran locally. A scripted client spoke the exact cloud shell HTTP protocol. This reproduces the server-side permanence defect, not the vim freeze (the vim freeze needs a real browser and was caught by the stack trace).

1. The client attached and read the shell prompt.
2. The client stopped reading for 45 s while the shell produced about 2 MB.
3. The server's 30 s SSE write deadline fired. The server log showed the consumer detaching while the session stayed alive and `attached=false`.
4. The client resumed: it drained buffered bytes, then received silence with no EOF.
5. Input POSTs still returned 200 and still reached the shell.

This is the user-visible freeze: live session, working input, dead output, no close signal.

## Hypotheses ruled out, with evidence

| Hypothesis | How it was ruled out |
|---|---|
| Proxy buffering of the SSE stream | The freeze reproduced on localhost with no proxy. Ruled out. |
| Safari-specific xterm bug | The freeze reproduced identically in Chrome. Ruled out. |
| xterm renderer (paint) stall | `term.refresh(...)` did not repaint, and the parsed buffer was stale too. The stall was before the renderer, in the write loop. |
| Session death / yamux keepalive timeout | Prior fix #81 emits `event: closed` and the frontend auto-reconnects. No reconnect message appeared during the freeze. |
| Go `ReadTimeout` cancels the SSE context | `net/http` `startBackgroundRead` clears the connection read deadline (server.go:741). Not applicable. |
| Frontend SSE parser drops frames | Frames are single-line base64 plus `\n\n`. The parser handled chunk splits correctly in direct tests. |
| vim blocks on unanswered terminal queries | xterm.js answers DSR, DA2, and OSC 10/11. A client that skipped `CSI 14t/16t` replies still rendered and typed fine. |
| Zero-size PTY resize | A resize to 0×0 still produced output. No silent freeze. |
| Buffer backpressure | The 4 MiB yamux window, 1 MiB batcher queue, and 256 KiB pipes cannot fill from vim-sized output. |
| Stale vim swap prompt | A leftover `.swp` file blocked a test run. It was a harness artifact, not the product bug. |

The wrong turns were all plausible and each was disproved by a cheap experiment. The one that ended the search was the browser stack trace, which pointed straight at `requestMode`.

## The fix

Primary fix (the actual freeze):

| Commit | Change |
|---|---|
| `94dc3b5` | `build.target: 'es2022'` in `vite.config.ts`. Keeps the enum variable declaration through minify, so xterm's `requestMode` no longer throws on vim's DECRQM query. Verified: `requestMode(e,t){let r;…}` and 0 miscompiled enum sites remain; bundle size unchanged. |

Separate hardening (the permanence bug, fixed for its own sake):

| Commit | Change |
|---|---|
| `7fffac9` | `Attach` returns a per-attach `clientLost` channel; handlers select on it and end the SSE response so the browser reconnects and replays the ring. Frontend watchdog aborts after `max(60 s, 4 × X-SSET-Heartbeat-Ms)` of silence. |
| `42e8116` | `takeover=1` reattach evicts a stale half-open attachment; `frameMu` serializes SSE writes. |
| `5d12ad8`, `3a8afbd` | Comment/log clarity, handler-level test, refactor. |
| `31f89d6`, `a085353` | `window.__shellDbg` counters and `window.__shellTerm` handle — the instruments that produced the decisive evidence. |

## Verification

- Enum fix: built the bundle and grepped the output — `requestMode` now emits `let r;` and there are 0 remaining `void 0||(x={})` miscompiled-enum sites anywhere in the bundle. Size unchanged (1.3 MB / 378 KB gzip). Final confirmation is a browser repro of `vim .zprofile` after `go install ./cmd/ssetunnel`.
- Permanence hardening: `go build ./...`, `go vet ./...` pass; full suite passes with `-skip 'TestBench'` (the two `TestBench*` transport tests hang on the base commit too). New tests cover writer/heartbeat/deadline loss signals, takeover eviction, stale-writer isolation, ring replay after loss, and a handler-level test that `shellReattach` returns.

## Residual gaps

Remaining:

- The build-target fix is a workaround for an esbuild enum-lowering bug at the default target. A future xterm or esbuild change should re-verify the built bundle for miscompiled enums (grep for `void 0||(` before shipping).
- Ring eviction can still leave a truncated sequence that begins with a byte other than a CSI parameter — a bare final byte, a `[` from a one-byte eviction, or UTF-8 continuation bytes. The replay resync skips only the unambiguous shapes, because a wider rule would delete real output that a drained buffer cannot recover.
- `Attached()` and `Attach()` are not one atomic step, so two clients racing for the same free session can still lose after headers are flushed and see one empty stream. Both then settle: the loser gets a 409 on its next attempt.

Resolved since this RCA was written:

- The reconnect counter reset on every received chunk, so backoff never grew. It now resets only after the stream has been up for a full heartbeat interval.
- `SetWriteDeadline` returning `http.ErrNotSupported` was treated as client loss. It now means "this writer cannot express deadlines" and the frame is written unguarded; only a real deadline error detaches.
- A second client attaching an occupied session received a 200 with an empty SSE body. It now receives 409 before any header is flushed. (The old behaviour was bounded, not infinite: it exhausted 10 attempts over roughly two minutes, then stopped — but it was uninterpretable to the user and logged an attach failure per attempt.)
- The auto-tuner could push `compress=true` to a serial (concurrency 1) agent, which the server rejects with 400, killing the conn. `ApplyTune` now only enables gzip when a sender pool (and so a reorder window) exists.

## Lessons

See `docs/guide/001-frozen-streaming-ui-playbook.md`.
