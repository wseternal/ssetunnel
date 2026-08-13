# Plan: Sign a macOS .app Bundle Instead of the Raw Binary

**Scope**: `Taskfile.yml` only (`sign` task, lines 138–145). No other file is modified.
**Status**: Draft — awaiting review
**Source**: multi-agent-planning synthesis (3 read-only design agents: simplicity / correctness / minimal-risk)

## Requirements

1. Wrap the Go binary in a `.app` bundle and sign the **bundle** instead of the raw binary.
2. `CFBundleIdentifier` = `wseternal.ssetunnel`
3. `CFBundleName` = `SSE Tunnel`
4. `CFBundleExecutable` = `ssetunnel`

## Verified constraints (from exploration)

- Nothing outside `Taskfile.yml` consumes the `sign` task: CI (`.github/workflows/release.yml`) runs only `task go:release` and globs `dist/ssetunnel-*`; `local.sh`, README, and CHANGELOG have no live references to `sign`/`SIGN_BIN`.
- `go:clean` already removes all of `dist/` — no edit needed there.
- `sign:cert` establishes the quoted-heredoc-in-task idiom this plan reuses.
- Task renders `{{...}}` before the shell runs, so a quoted `<<'EOF'` heredoc does not block template interpolation.

## Critical files

| File | Role |
|------|------|
| `Taskfile.yml` | Only file modified — `sign` task (lines 138–145) |
| `.github/workflows/release.yml` | Constraint: must stay behaviorally unaffected (uses `go:release` only) |
| `dist/SSE Tunnel.app/Contents/Info.plist` | Generated acceptance artifact |

## Tasks

### Task 1 — Update `sign` task header (vars + desc)

Keep `SIGN_BIN` byte-identical (it becomes the *input* binary contract); add `SIGN_APP`; update desc:

```yaml
  sign:
    desc: Wrap the release binary in a macOS .app bundle and codesign the bundle (SIGN_BIN=source binary, SIGN_APP=output bundle)
    vars:
      SIGN_BIN: '{{.SIGN_BIN | default (printf "%s/%s-%s-%s" .DIST .BIN OS ARCH)}}'
      SIGN_APP: '{{.SIGN_APP | default (printf "%s/SSE Tunnel.app" .DIST)}}'
```

- **Acceptance**: `task --list` parses; `SIGN_BIN` default unchanged.
- **Verify**: `task --list | grep -A1 'sign'`

### Task 2 — Prepend bundle assembly to `sign` body

```yaml
    cmds:
      - rm -rf "{{.SIGN_APP}}"
      - mkdir -p "{{.SIGN_APP}}/Contents/MacOS"
      - cp -af "{{.SIGN_BIN}}" "{{.SIGN_APP}}/Contents/MacOS/{{.BIN}}"
      - |
        cat > "{{.SIGN_APP}}/Contents/Info.plist" <<'EOF'
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0">
        <dict>
          <key>CFBundleIdentifier</key>
          <string>wseternal.ssetunnel</string>
          <key>CFBundleName</key>
          <string>SSE Tunnel</string>
          <key>CFBundleExecutable</key>
          <string>ssetunnel</string>
          <key>CFBundlePackageType</key>
          <string>APPL</string>
          <key>CFBundleShortVersionString</key>
          <string>{{.RELEASE_VERSION}}</string>
          <key>CFBundleVersion</key>
          <string>{{.RELEASE_VERSION}}</string>
        </dict>
        </plist>
        EOF
      - plutil -lint "{{.SIGN_APP}}/Contents/Info.plist"
```

Notes: `rm -rf` first makes every run a clean rebuild (no stale `_CodeSignature`); quoted `<<'EOF'` mirrors the proven `sign:cert` idiom; `cp -af` matches the existing idiom at `Taskfile.yml:49`.

- **Acceptance**: executable at `dist/SSE Tunnel.app/Contents/MacOS/ssetunnel`; plist lints OK; the three required keys have exactly the required values.
- **Verify**: `task go:release && task sign`, then `plutil -p "dist/SSE Tunnel.app/Contents/Info.plist"` shows `CFBundleIdentifier => "wseternal.ssetunnel"`, `CFBundleName => "SSE Tunnel"`, `CFBundleExecutable => "ssetunnel"`.

### Task 3 — Retarget codesign to the bundle, inside-out

```yaml
      - codesign --force --sign "{{.SIGN_IDENTITY}}" "{{.SIGN_APP}}/Contents/MacOS/{{.BIN}}"
      - codesign --force --sign "{{.SIGN_IDENTITY}}" "{{.SIGN_APP}}"
      - codesign --verify --deep --strict --verbose=2 "{{.SIGN_APP}}"
      - codesign -d -vvv "{{.SIGN_APP}}"
```

- **Acceptance**: `task go:release:signed` exits 0 unchanged (its body needs no edit); `Contents/_CodeSignature/CodeResources` exists.
- **Verify**: `codesign --verify --deep --strict --verbose=2 "dist/SSE Tunnel.app"` exits 0; `codesign -d -vvv "dist/SSE Tunnel.app" 2>&1 | grep "Identifier=wseternal.ssetunnel"`.

### Task 4 — End-to-end and regression verification (no edits)

- `task go:clean && task go:release:signed` → green from clean state.
- `"dist/SSE Tunnel.app/Contents/MacOS/ssetunnel" version` → prints release version (proves the signed binary runs).
- Override contract: `SIGN_BIN=/path/to/other task sign` → `cmp /path/to/other "dist/SSE Tunnel.app/Contents/MacOS/ssetunnel"` matches.
- CI path: `rm -rf dist && task go:release && ls dist` → only `ssetunnel-darwin-arm64`, no `.app` (proves `release.yml` unaffected).
- `task go:clean && test ! -e dist` → bundle cleaned.

## Risks & mitigations

| Risk | Mitigation |
|------|------------|
| Space in `SSE Tunnel.app` breaks unquoted expansion | Quote every path in Task cmds and verify commands; `plutil -lint` fails fast on mangled paths |
| Heredoc/templating pitfalls | Quoted `<<'EOF'` per `sign:cert` precedent; plist contains no `$`; `plutil -lint` guards |
| `SIGN_BIN` semantic change (was: signed in place; now: bundled input) | Grep-verified no repo consumer relies on old semantics; new `desc` documents the contract |
| Raw `dist/ssetunnel-darwin-arm64` now stays unsigned | By design (requirement 1); verified nothing expects a signed raw binary (CI never calls `sign`) |
| `spctl` rejects the self-signed bundle | Expected Gatekeeper policy, not a defect; `codesign --verify --deep --strict` is the acceptance gate; do **not** add `spctl` to the task (non-zero exit would break it) |
| Identity missing on a fresh machine | Same as today; run `task sign:cert` once (unchanged) |
| Future CI risk (not current) | If signing is ever wired into `release.yml`, `files: dist/*` would upload a directory — zip the bundle first (note only, out of scope) |

## Rejected alternatives

- **Separate `app:bundle` task + `SIGN_APP`-only override** — larger diff, extra task layer and global vars with no consumer; bundling is only used by `sign`.
- **Keep `--deep` at sign time** — Apple documents it as a last resort and it re-seals nested code with top-level flags; explicit inside-out signing is two lines and correct.
- **Hardened runtime (`--options runtime`) + explicit `--timestamp`** — deferred: a self-signed identity can't pass Gatekeeper/notarization anyway; two flags can be added later.
- **Fuller plist keys (`LSMinimumSystemVersion`, `CFBundleSupportedPlatforms`, `CFBundleDevelopmentRegion`, …)** — dead metadata for a CLI-only bundle.
- **`sign:zip` distributable task / go-task checksum incrementality** — out of scope; the task runs in ~1 s.
