# Gate: Dynamic Module Lifecycle

## Condition
A user can install, start, stop, restart, and remove a separate module process through local CLI commands. These operations require no agent rebuild or restart. A crashed module loses its registrations without stopping the agent or unrelated modules.

## Evidence Required
- [ ] Lifecycle integration result with a real module process → `docs/as-goals/agent-module-registry/[iteration]/evidence/module-lifecycle.txt`
- [ ] Registry and process supervisor source paths → linked in the lifecycle evidence
- [ ] CLI command result and concurrent agent health result → linked in the lifecycle evidence

## Verification Method
Run a real agent and registry. Install and start an example module. Discover it without restarting the agent. Stop, restart, and remove it. Kill its process and verify prompt deregistration and agent survival.

## Owner
Engineer
