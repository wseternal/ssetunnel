# Gate: InputAck Protocol

## Condition
Agent sends a `FrameInputAck` (0x06) frame back to server for every input event received. Server parses and forwards it as an SSE named event (`inputack`) to the frontend. The wire protocol is correctly implemented on both sides.

## Evidence Required
- [ ] `FrameInputAck` constant (0x06) defined in `protocol.go`
- [ ] `WriteInputAck` / `ParseInputAck` functions in `protocol.go` with round-trip tests
- [ ] Agent sends InputAck on every received FrameInput in `proxy.go`
- [ ] Server handles FrameInputAck in SSE loop, forwards as named SSE event in `server/remoteapp.go`
- [ ] Unit tests cover the full InputAck write→parse→forward round-trip

## Verification Method
Run `go test -race ./internal/remoteapp/... ./internal/server/...` — all tests pass including new InputAck tests.

## Owner
Engineer
