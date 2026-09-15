# Gate: webp-pipeline

## Condition
All screenshot frames (streaming and manual refresh) are encoded as WebP on the agent using `github.com/deepteams/webp` (pure Go, no CGO) and rendered in the browser as WebP. JPEG encoding is fully removed from the capture path. The frontend `<img>` uses the `data:image/webp;base64,` prefix. The server remains payload-agnostic (forwards opaque bytes). Build stays CGO-free for the purego tag and `go build ./...` / `go vet ./...` are clean.

## Evidence Required
- [ ] `go.mod` includes `github.com/deepteams/webp` → go.mod diff
- [ ] Agent capture path encodes WebP via `webp.Encode` (no `image/jpeg` import remains in `internal/remoteapp/`) → file path + line range + grep evidence
- [ ] Frontend uses `data:image/webp;base64,` prefix → `frontend/console/src/App.tsx` line
- [ ] Encoded output verified as valid WebP (test decoding an encoded frame, e.g. via `webp.Decode` or RIFF header check) → test file path + test name
- [ ] Frame size comparison: WebP frame size vs prior JPEG q75 baseline on a representative capture (Performance Engineer) → note in evidence manifest
- [ ] `go build ./... ./...` clean including `-tags purego` build → command output in evidence manifest

## Verification Method
Test Engineer greps for JPEG remnants, runs the encode/decode round-trip test, verifies go.mod, and builds with and without purego tag. Performance Engineer reviews size measurements. Pass requires: zero JPEG in capture path, valid WebP round-trip proven, clean builds.

## Owner
Senior Software Engineer; Performance Engineer validates sizes
