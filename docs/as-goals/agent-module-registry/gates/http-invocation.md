# Gate: HTTP Invocation Data Plane

## Condition
An authorized server consumer can invoke a discovered operation through a dedicated yamux data stream. The invocation preserves the method, path, query, headers, request body, response status, response headers, response body, bidirectional streaming, cancellation, deadlines, and module errors. Existing tunnel targets retain their behavior.

## Evidence Required
- [ ] End-to-end invocation result over the real SSE plus POST transport → `docs/as-goals/agent-module-registry/[iteration]/evidence/http-invocation.txt`
- [ ] Streaming, cancellation, deadline, and error results → linked in the invocation evidence
- [ ] Existing agent and server regression results → linked in the invocation evidence

## Verification Method
Invoke real example operations through a real agent and server. Exercise empty, large, and streamed bodies. Cancel an active invocation and verify resource cleanup. Run existing transport, agent, server, shell, and remote application tests.

## Owner
Engineer
