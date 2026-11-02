# Gate: Security and Isolation

## Condition
Only authorized local processes can register modules. Server consumers receive only services on agents they can access. Invocation re-checks access after agent resolution. Trusted package allowlists control UI remotes. Protocol parsers enforce size, path, schema, and concurrency limits. A malicious or failed module cannot access another module, spoof an agent, or execute an unapproved console remote.

## Evidence Required
- [ ] Local IPC authentication and authorization results → `docs/as-goals/agent-module-registry/[iteration]/evidence/security-isolation.txt`
- [ ] Cross-agent, reconnect-race, and invocation authorization results → linked in the security evidence
- [ ] Malformed metadata, resource limit, UI trust, and path validation results → linked in the security evidence
- [ ] Security review with no unresolved Critical findings → linked in the security evidence

## Verification Method
Use real processes and HTTP requests to attempt unauthorized registration, cross-agent discovery, cross-agent invocation, stale-session access, oversized metadata, path traversal, and unapproved remote loading. Verify bounded resource use and fail-closed behavior.

## Owner
Security Engineer and Test Engineer
