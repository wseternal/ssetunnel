# Plan — Iteration 1

## Task 1: Wire connect service dispatch in `main.go`

**File:** `cmd/ssetunnel/main.go`

**Changes:**
1. Update usage text: add `service actions: run, start, stop, restart, status, uninstall` for `connect` (no `reload`)
2. In `case "connect":`, add service action dispatch before `runConnect`:
   ```go
   case "connect":
       if len(os.Args) > 2 && serviceActions[os.Args[2]] {
           if handled, err := dispatchServiceAction("connect", os.Args[2:]); handled {
               if err != nil {
                   log.Fatal(err)
               }
               return
           }
       }
       err = runConnect(ctx, os.Args[2:])
   ```

**Acceptance:** `ssetunnel connect start ...` routes through `dispatchServiceAction`

## Task 2: Extend `dispatchServiceAction` for connect with `--name`

**File:** `cmd/ssetunnel/service.go`

**Changes:**
1. At the top of `dispatchServiceAction`, after extracting `--service-user`, extract `--name` for connect subcommand:
   - If `subcommand == "connect"`: extract `--name` from args, error if empty
   - Override `svcConfig.Name` to `"ssetunnel-connect-" + name`
   - Strip `--name` from `runtimeFlags` (it's a service-layer flag, not a runtime flag)
2. Validate: if connect service and runtime flags contain `--local -` (stdio mode), reject with clear error
3. Update `buildRunFn` to handle `"connect"`:
   ```go
   case "connect":
       return func(ctx context.Context) error { return runConnect(ctx, filtered) }
   ```
4. Skip the root-guard (`os.Getuid() == 0` embedded postgres check) for `connect` — it only applies to `server`

**Acceptance:** `--name` is mandatory for connect service actions; service name becomes `ssetunnel-connect-<name>`; stdio mode rejected

## Task 3: Tests

**File:** `cmd/ssetunnel/service_test.go`

**New tests:**
1. `TestConnectServiceRequiresName` — verify dispatching `connect start` without `--name` errors
2. `TestConnectServiceNameInServiceConfig` — verify service name includes `--name` value
3. `TestConnectServiceRejectsStdio` — verify `--local -` is rejected for connect service actions
4. `TestBuildRunFnConnect` — verify `buildRunFn("connect", ...)` returns a non-nil function
5. `TestBuildServiceArgsConnect` — verify correct args output for connect service

**Acceptance:** All new + existing tests pass

## Task 4: Build and vet

Run `go build ./...` and `go vet ./...` — both clean.
