# Gate: Operations and Compatibility

## Condition
Operators can diagnose registry, discovery, invocation, and UI failures through structured events and metrics. Mixed old and new agents and servers preserve existing tunnels without module support. Module state survives agent reconnects through fresh registration, not restored stale state. Deployment and rollback instructions cover every changed binary and UI asset.

## Evidence Required
- [ ] Mixed-version and reconnect results → `docs/as-goals/agent-module-registry/[iteration]/evidence/operations-compatibility.txt`
- [ ] Metrics, logs, correlation identifiers, and redaction review → linked in the operations evidence
- [ ] Deployment, rollback, ADR, SDK guide, and changelog paths → linked in the operations evidence
- [ ] Full backend suite and console build results → linked in the operations evidence

## Verification Method
Run old and new peers in both directions. Reconnect during discovery and invocation. Inspect diagnostic output for correlation and secret redaction. Follow the documented deployment and rollback steps. Run the permitted full verification suite.

## Owner
Release Engineer and Documentation Engineer
