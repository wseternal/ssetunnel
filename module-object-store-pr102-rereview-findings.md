## Multi-Agent Review — PR #102 (head `14bccb5`)

**Verdict: Request Changes** — 74/100
**0 Critical · 2 Warnings · 5 Suggestions · 4 Nits**

Reviewed the complete `c408dc7..14bccb5` range, with extra focus on the new `813aa14..14bccb5` commits. The prior hardening changes are present: forwarder initialization, closed-generation rejection, caller request-ID clearing, module-namespaced assemblies, the assembly sweep, and server-scoped counters.

**Pre-flight:** `go build ./...`, `go vet ./...`, `go test ./... -timeout 180s`, and race tests for the changed packages all pass. GitHub CI `build-and-test` passes for `14bccb5`.

### Warnings

**1. The identity-rejection path closes the connection before reading the framed error.**
`sdk/objectstore_client.go:231-247`

When the agent rejects the identity with `OBJECTSTORE_RESP <len>\n<json>`, the client calls `conn.Close()` before `io.ReadFull(reader, respBuf)`. If the body was not already buffered by `ReadSlice`, the read fails and the caller sees `use of closed network connection` instead of the documented `unavailable` or `invalid_request` error. This is the normal path when object storage is disabled or the module is unregistered.
**Fix:** read and parse the framed rejection while the connection is still open; close only after parsing. Add a regression test with the body delayed until after the header.

**2. Session shutdown can close the filesystem root while the object-store worker is still using it.**
`internal/server/objectstore.go:96-107,217-229,244-258`; `internal/server/objectstore_fs.go:46-53`; `internal/server/session.go:129-137`

`ObjectStoreHandler.Close` sets `closed`, drops assemblies, and cancels the context, but it does not wait for `run` or an active `execute`. `Session.Close` calls it, then `RunServer` eventually closes the deferred `FileObjectStore` root. A worker can still be inside `Put`/`Get` when `root.Close()` runs; `FileObjectStore.root` is read without synchronization. `enqueue` also checks `closed` and then sends outside `h.mu`, so a job can be enqueued after the worker has exited and never processed.
**Fix:** add a worker done channel or `WaitGroup` and wait in `Close`; move the non-blocking queue send under `h.mu`; make `FileObjectStore` operations and `Close` share a lock, or close the root only after all session object-store workers have stopped.

### Suggestions

**1. The SDK ignores its `moduleID` argument.**
`sdk/objectstore_client.go:92-124` — `Put` and `Get` use `c.moduleID`, not the `moduleID` parameter. A caller that passes a different namespace silently writes its own. Either validate equality and return an error, or remove the parameter and document that the client’s configured identity is authoritative.

**2. The new security-sensitive surfaces have no automated tests.**
`internal/server/objectstore_handler_test.go` and `internal/server/objectstore_key_test.go` cover key validation and assembly keying, but not `FileObjectStore`, the SDK client, or `ObjectStoreHandler.execute`. Add real-filesystem tests for empty/binary/not-found/overwrite/symlink/oversize cases and a real IPC loop test for busy and identity rejection. This is the highest-value follow-up in the PR.

**3. PR #102 still describes only the SDK contract.**
The title and body cover `sdk.ObjectStore` and the chunk codec, while the PR also adds the filesystem backend, agent forwarding, IPC mode, server flag, ADR, guide, and tests. Update the title/body with the full scope, trust-model limitation, and upgrade order.

**4. Object-store metrics have no operator-facing surface.**
`internal/metrics/objectstore.go` and `Server.ObjectStoreMetrics()` are implemented, but no console/API route or periodic log consumes them. A running operator still cannot see put/get rate, busy rejections, or backend latency. Add an admin-scoped metrics route or a periodic summary log.

**5. `os.Root` follows in-root symlinks, but the backend’s symlink defenses are incomplete.**
`internal/server/objectstore_fs.go:77-83,133-148` — `Get` performs `Lstat` and then `Open`, leaving a TOCTOU window; `Put` does not reject a symlinked module directory. A local writer able to modify the `0700` root could redirect a module namespace inside the root. Either use a no-follow open path or state the local-filesystem trust assumption in the ADR and guide.

### Nits

- `internal/server/objectstore.go:289-293` — `not_found` increments `GetErrors`; an expected miss is counted as a backend failure. Record misses separately.
- `docs/adr/008-module-object-store.md:98-102` and `docs/guide/005-module-object-store.md:94-104` — the 32 MiB memory figure understates eight 6 MiB encoded IPC bodies plus the decoded 4 MiB object buffers.
- `internal/agent/objectstore.go:267-280` — `sendObjectStoreChunk` always returns nil; a live control-stream write failure is only discovered by timeout or generation cleanup. Prefer a sender that returns an error or count the dropped frame.
- `internal/server/objectstore.go:205-213` — GET requests skip canonical `sequence`/`chunk_count`/`final`/`total_size`/`sha256` validation. Reject non-canonical GET metadata instead of silently accepting it.
