# Gate: OpenAPI Discovery

## Condition
A provider can register an immutable module identity, a service version, valid OpenAPI v3 YAML, health, and optional UI metadata. An authorized server consumer can discover the current catalog for a selected agent. Updates replace old revisions, and reconnects remove stale entries.

## Evidence Required
- [ ] Discovery integration result with valid and invalid OpenAPI documents → `docs/as-goals/agent-module-registry/[iteration]/evidence/openapi-discovery.txt`
- [ ] Control protocol and catalog source paths → linked in the discovery evidence
- [ ] Reconnect and revision replacement results → linked in the discovery evidence

## Verification Method
Register two service revisions and inspect the server catalog. Reject malformed YAML, unsupported OpenAPI versions, duplicate routes, and oversized metadata. Reconnect the agent and verify that stale entries disappear.

## Owner
Engineer
