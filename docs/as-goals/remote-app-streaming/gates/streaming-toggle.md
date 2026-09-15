# Gate: streaming-toggle

## Condition
The command palette contains a streaming toggle action. When not streaming, it reads "Start Streaming" with shortcut S; activating it starts continuous screenshot delivery to the browser. While streaming, the same palette item reads "Stop Streaming"; activating it halts continuous delivery. The toggle works via both palette click and the S keyboard shortcut. The "Refresh Screenshot" (R) action remains present and functional in both modes.

## Evidence Required
- [ ] Frontend palette definition with dynamic label in `frontend/console/src/App.tsx` → file path + line range
- [ ] Frontend streaming state management (per-connection, toggle handler sending start/stop control events) → file path + line range
- [ ] Control event flow: palette action → POST `/console/api/v1/remoteapp/connect-up` → server `handleRemoteAppUp` → yamux `FrameInput` → agent `ProxyRemoteApp` interception → capture loop start/stop → file paths + line ranges across the chain
- [ ] Test proving agent handles the start/stop control events (unit or integration test) → test file path + test names
- [ ] Browser-observed behavior: label flips, S toggles, frames update while streaming (Frontend Engineer evidence) → description in evidence manifest

## Verification Method
Test Engineer inspects the code chain end-to-end, runs the agent-side tests, and reviews the Frontend Engineer's browser evidence. Pass requires: label toggles, both click and shortcut work, stream actually starts/stops frame delivery, refresh still available.

## Owner
Senior Software Engineer (backend/control flow) + Frontend Engineer (palette/UI)
