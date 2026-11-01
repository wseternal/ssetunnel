# Guide-001: Frozen Streaming UI — Diagnosis and Guardrails

This guide records the knowledge from RCA-001. Use it when a streaming or terminal UI stops updating while other parts still work. It has three parts: a failure model, a diagnosis playbook, and guardrails that prevent the class of bug.

## 1. The failure model

Every "frozen forever" bug has three layers. Treat them separately.

- **Trigger.** The event that breaks something: a network switch, a laptop sleep, a proxy that drops state, a slow peer.
- **Permanence.** The mechanism that turns a break into a permanent state: a silent detach, a parked handler, a missing timeout, a swallowed error.
- **Aggravator.** Anything that makes the broken state feel normal for a while: buffering, retries, heartbeats that mask silence.

Triggers are environmental. You often cannot fix them. Permanence is code. You always can. Fix the permanence layer first, then the trigger layer if evidence supports it.

**Rule: no failure path may end in a state that is invisible to the other side.**

## 2. Dual-path independence is a hazard

This product sends output over one long-lived SSE connection and input over short-lived POSTs. When one path dies, the other keeps working. The user sees a live application with a dead display.

Any system with asymmetric paths needs an explicit answer to this question: **what happens when only one direction is alive?**

- A liveness signal must cover the whole session, not one connection.
- A dead output path must produce a terminal event the client can act on: a close, an error frame, or a watchdog.
- Input POSTs that succeed while output is silent are a bug signature, not a health check.

## 3. Half-open TCP: what you must assume

- A peer can vanish without FIN or RST. Both ends then see a healthy socket.
- Small writes into a half-open socket succeed for minutes. The kernel buffers them. Retransmit give-up is around 15 minutes.
- A write deadline bounds a *blocked* write. It does not detect a connection that the peer stopped reading but the network still acknowledges.
- Therefore: server-side deadlines alone are not enough. The client needs an inactivity watchdog, and the watchdog must count *any* received bytes, including heartbeats.
- Conversely, the client cannot always see a server-side failure. A proxy can ACK data and discard it. The server never sees an error. The client's watchdog is the only detector.

## 4. Anti-patterns (each one caused or extended this bug)

1. **Silent detach.** Clearing a writer while keeping the response open. The state is dead but no one is told. This is the core defect.
2. **Parked handler.** A handler that blocks on "client gone" or "session gone" only. A third exit — "my writer broke" — must exist.
3. **Heartbeat that is invisible to liveness.** The server sends heartbeats. The client's parser filters them as comments. Liveness never sees them.
4. **One-shot recovery signals.** `event: closed` covered session death but not writer failure. Every distinct failure needs its own observable end.
5. **Fixing without direction.** Three prior fixes patched the browser↔server wire path. None tested whether the failure was input-side or output-side first.
6. **Stale-attachment ambiguity.** After adding reconnect, a second client can be rejected by a zombie attachment that will never die. Reconnect needs a takeover rule.

## 5. Diagnosis playbook for a frozen stream

Work in this order. Each step cuts the search space in half.

### Step 1 — Find the direction from field evidence

Ask what the broken application still *does*. Here, vim's swap file contained the typed text. That proved input arrived and output was lost. One field clue replaced days of code reading.

Useful probes: side effects of the process (swap files, logs, `ps`), whether commands with side effects still run, and whether a fresh consumer sees buffered data.

### Step 2 — Reproduce with the real stack and a controlled client

Run the real server, agent, and a scripted client that speaks the exact wire protocol. Do not start with the browser. A controllable client lets you stall, resume, and measure one direction at a time.

### Step 3 — Dump goroutines at the stall

Send `SIGQUIT` to the Go process. Read the stacks of the copy goroutines:

- Blocked in `read()` on the PTY: the producer stopped. The application is not writing.
- Blocked in `Write` on the stream: flow control or the transport is stuck.
- Blocked in the SSE `WriteFrame`: the delivery to the client is stuck.

This one dump distinguishes "nothing to send" from "cannot send."

### Step 4 — Test buffered replay

Reattach a fresh consumer. If it replays the missed bytes, the server received them. The break is downstream of the server. If it replays nothing, the break is upstream.

### Step 5 — Inject the failure

Stall the client reader while producing bulk output. The server's write deadline converts the stall into the exact silent-detach state. Deterministic injection proves the permanence mechanism even when the real trigger is unknown.

### Step 6 — Trust the harness last

Two harness artifacts cost hours here:

- Python 3.13 `http.client.read(n)` on a chunked response accumulates until `n` bytes. It blocks on a live stream. Use `read1(n)`.
- Leftover state (a vim `.swp` file, a still-attached earlier client) fabricates a freeze. Clean state between runs, and print per-read chunk counts so silence is provable.

## 6. Terminal-specific knowledge

- vim sends terminal queries at startup: `CSI 6n` (DSR), `CSI > c` (DA2), `OSC 10/11 ; ?` (colors), `CSI 14t/16t` (pixel sizes).
- xterm.js 6 answers DSR, DA2, and OSC 10/11 by default. It does **not** answer `14t/16t` unless the app sets `windowOptions`. vim does not block on those, so this is not a freeze cause.
- xterm.js sends its query replies through `onData`, the same channel as keystrokes. They reach the PTY as input.
- A PTY at 0×0 does not freeze vim. Output continues.
- When you suspect "the app hung," check whether the app's *side effects* continue. That separates "hung" from "unanswered."

## 7. Guardrails (make the class of bug impossible)

**Code rules for this repo:**

1. A failure path must end the response or emit a terminal event. Never clear a writer and keep serving.
2. Every SSE handler select needs three exits: context cancel, session death, and *writer-lost*. The `clientLost` channel from `Attach` is the third exit.
3. Writer state changes go through the writer-identity guard (`detachWriter(w, ...)`). A stale failure must never clear a newer client.
4. Per-attach signals (channels) must be created fresh on each `Attach`. A closed channel must never evict a later client.
5. Serialize frame writes (`frameMu`) and drain an in-flight write before a handler returns.
6. The client watchdog threshold must derive from the server heartbeat (`X-SSET-Heartbeat-Ms`), with a floor. A fixed threshold breaks when an operator raises `--heartbeat`.
7. Log every detach with its reason and session/agent IDs, in a greppable format.

**Review checklist for streaming changes:**

- [ ] Can this failure be observed by the other side within one heartbeat interval?
- [ ] Does every error path produce a close, a frame, or a watchdog-detectable silence?
- [ ] Does reconnect work when the old connection is half-open?
- [ ] Does a second client conflict with a zombie attachment, and who wins?
- [ ] Is the test asserting the *handler returns*, not only that an internal channel closed?

**Test requirements:**

- Drive real components (real yamux over `net.Pipe`, real pipes). Doubles only at the HTTP boundary.
- Cover each detach trigger separately: data write error, heartbeat write error, deadline setup error.
- Assert the session survives client loss and reattach replays the buffer.

## 7b. The built artifact can lie; the field stack trace ends it

The real cause here was not in the tunnel or in vim. It was a **production minifier bug**: esbuild, at the default build target, lowered a function-local TypeScript enum inside xterm.js's `requestMode` and dropped its variable declaration. The strict-mode bundle threw `ReferenceError: r is not defined` the first time vim sent a DECRQM query, and because the throw happened inside xterm's async write loop, the write buffer wedged forever. Source looked fine; unit tests passed; only the built bundle was broken.

Guardrails:

1. **Verify the built artifact, not just the source.** For a minifier-lowering bug, `grep` the shipped bundle. A cheap pre-ship check: search the built JS for the miscompiled enum shape `void 0||(<name>={})` and fail the build if present.
2. **Pin `build.target`.** This repo uses `es2022` in `vite.config.ts` specifically so xterm's enums survive minify. Do not lower it back to the default without re-checking the bundle.
3. **A field stack trace beats any local repro.** The freeze survived five local reproduction attempts and several plausible theories. The single browser console stack trace named `requestMode` and ended the search in one step. Build the client so a frozen state is inspectable.

Instrument the client to make that possible. This repo exposes `window.__shellDbg` (reads/frames/dataBytes/writes/keyPosts + timestamps) and `window.__shellTerm` (the xterm handle). They separate the three zones that look identical from outside:

- `writes` grows but the xterm buffer does not advance → **library write-loop stall** (this bug).
- `reads` grows but `dataBytes` flat → **delivery gap** (server not sending data frames).
- `keyPosts` flat while typing → **input not leaving** the browser.

## 8. Process lessons

1. **Direction before fix.** Establish which side of each boundary still works before touching code. One field clue (the swap file) beat three prior fix attempts.
2. **Write the RCA before the patch.** A discarded-hypotheses list prevents the next engineer from re-testing dead ends.
3. **Prior "fixes" may be adjacent bugs.** #67, #78, #79 each fixed a real defect. None addressed the permanence layer, so the user-visible bug survived. The `clientLost` work in this cycle is the same kind of adjacent (but worthwhile) hardening — it fixed a real permanence bug, not the vim freeze.
4. **Reproduce in the real environment.** A localhost scripted client could not reproduce a bug that lived in the browser's minified bundle. When the field is a browser, the browser is part of the system under test.
5. **Trust the user's "always reproducible."** "Every time with `vim .zprofile`" is data. It rules out random network causes and points at deterministic content — here, a fixed escape sequence in vim's startup.
4. **Make the unknown trigger harmless.** You may never learn which network event broke the SSE. You can guarantee that no such event produces a permanent freeze.
5. **Keep observability as a feature.** The agent log retrieval and verbose tracing (362caf3) existed before this diagnosis and shortened it. Add the probe before you need the probe.

## Related

- `docs/rca/001-cloud-shell-output-freeze.md` — the incident this guide generalizes.
- `docs/adr/001-control-data-channel-separation.md` — control/data split that made tracing possible.
- `internal/server/AGENTS.md` — Cloud Shell Handlers: client-writer failure and takeover.
- `frontend/AGENTS.md` — Failure Handling: watchdog and reconnect behavior.
