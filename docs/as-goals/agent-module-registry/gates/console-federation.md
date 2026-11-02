# Gate: Console Module Federation

## Condition
The console discovers authorized module pages and loads their Vite Module Federation remotes. The host supports pages supplied by agent-side providers and server-side consumers. It provides routing, authentication context, shared React dependencies, loading states, error boundaries, and remote failure recovery. Existing console pages retain their behavior.

## Evidence Required
- [ ] Host and example remote build results → `docs/as-goals/agent-module-registry/[iteration]/evidence/console-federation.txt`
- [ ] Provider and consumer UI manifest examples → linked in the console evidence
- [ ] Permission-gated browser result for navigation, invocation, failure, and recovery → linked in the console evidence
- [ ] Existing console build result with the ES2022 target intact → linked in the console evidence

## Verification Method
Build the host and two example remotes. With prior user permission, use a real browser to open each page, invoke a service, and recover from an unavailable remote. Confirm shared dependency versions and no duplicate React runtime. Check the existing console routes.

## Owner
Frontend Engineer
