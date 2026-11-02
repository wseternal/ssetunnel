# Gate: Unified Go SDK

## Condition
One public Go SDK lets a provider serve an OpenAPI-described HTTP module and lets a consumer discover and invoke it. The SDK exposes stable types for manifests, revisions, discovery, invocation, streaming, cancellation, and errors. It hides local IPC and tunnel framing from module authors.

## Evidence Required
- [ ] Provider and consumer example source → `docs/as-goals/agent-module-registry/[iteration]/evidence/go-sdk.txt`
- [ ] SDK API documentation and compatibility result → linked in the SDK evidence
- [ ] Real-process example result without mocked backend tests → linked in the SDK evidence

## Verification Method
Build an example provider and consumer against only the public SDK. Register the provider, discover its contract, invoke unary and streamed operations, and decode a typed module error. Review the exported API for unnecessary transport details.

## Owner
Engineer and Documentation Engineer
