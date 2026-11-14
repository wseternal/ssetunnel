# Gap Report: Finding #24 — `wailsBridge` is null placeholder

## 1. Current State Assessment

The `wailsBridge` in `frontend/client/src/wails.ts` is the visible tip of a three-layer integration gap. Below is the full inventory of what works, what is stubbed, and what is missing.

### Layer 1: Frontend Bridge (`wails.ts` / `bridge.ts`)

| Component | Status | Detail |
|-----------|--------|--------|
| `httpBridge` | **Complete** | All 12 `Bridge` methods implemented, calls `clientserver` HTTP API |
| `wailsBridge` | **Null placeholder** | `export const wailsBridge: Bridge \| null = null` |
| `resolveBridge()` in `App.tsx` | **Wired correctly** | Checks `wailsBridge && window.runtime`, falls back to `httpBridge` |

### Layer 2: Clix Go Service (`app/clix/main.go`)

| Method | Status | Underlying call |
|--------|--------|-----------------|
| `Login` | Stub | `fmt.Errorf("not implemented")` — should call `clientapp.Login` |
| `CheckTOTPRequired` | Stub | `fmt.Errorf("not implemented")` — should call `clientapp.CheckTOTPRequired` |
| `GetSession` | Stub | `fmt.Errorf("not implemented")` — should call `clientapp.ResolveSession` |
| `GetAgentStatus` | Partial stub | Returns `{"running": false}` — no real status tracking |
| `StartAgent` | Stub | `fmt.Errorf("not implemented")` — needs runtime manager |
| `StopAgent` | Stub | `fmt.Errorf("not implemented")` — needs runtime manager |
| `RestartAgent` | **Missing** | Not declared — compile error if the frontend calls it |
| `GetConnectInstances` | Stub | `fmt.Errorf("not implemented")` — needs runtime manager |
| `StartConnect` | Stub | `fmt.Errorf("not implemented")` — should call `clientapp.StartNamedConnect` |
| `StopConnect` | Stub | `fmt.Errorf("not implemented")` — needs runtime manager |
| `RestartConnect` | **Missing** | Not declared |

### Layer 3: Browser Server API (`internal/clientserver/api.go`)

| Endpoint | Status | Detail |
|----------|--------|--------|
| `POST /login` | **Complete** | Calls `clientapp.Login` |
| `POST /totp-check` | **Complete** | Calls `clientapp.CheckTOTPRequired` |
| `GET /session` | **Complete** | Calls `clientapp.ResolveSession` |
| `POST /logout` | **Complete** | Calls `auth.DeleteSession` |
| `GET /agent/status` | Stub | Returns `{"running": false}` hardcoded |
| `POST /agent/start` | Stub | 501 — builds `AgentConfig` but discards it |
| `POST /agent/stop` | Stub | 204 no-op |
| `POST /agent/restart` | Stub | 501 |
| `GET /connect/list` | Stub | Returns `[]` hardcoded |
| `POST /connect/start` | Stub | 501 |
| `POST /connect/stop/{name}` | Stub | 204 no-op |
| `POST /connect/restart/{name}` | Stub | 501 |

---

## 2. Gap Analysis

### Gap A: No Shared Runtime Manager (Critical — blocks both surfaces)

ADR-005 mandates: *"Each browser or desktop instance owns at most one agent and multiple named connect runtimes. These run in-process."*

Neither `clientserver` nor `clix` has a runtime manager. Both need to:
- Track a single agent lifecycle (context + cancel + status)
- Track multiple named connect instances (already modeled in `clientapp.ConnectInstance`)
- Expose start/stop/restart/status operations
- Clean up on shutdown

**This is the single root cause** blocking both the Wails bridge and 8 browser API endpoints. Without it, both surfaces are dead in the same way.

### Gap B: Clix Login/Session Methods Are Trivial but Unwired (Low effort)

`clientapp.Login`, `clientapp.CheckTOTPRequired`, and `clientapp.ResolveSession` are fully functional. The Clix stubs just need thin wrappers that convert between Wails-friendly return types (`map[string]string`) and the `clientapp` structs.

### Gap C: No Wails Binding Generation Pipeline (Build gap)

ADR-005 specifies: *"Wails generates bindings from `app/clix` into `app/clix/frontend/bindings/` with `GOWORK=off`."*

No build task generates these bindings. The `wails3 generate bindings` command has never been integrated into the Taskfile. Without generated bindings, the TypeScript side has no typed functions to call — `wails.ts` cannot import what does not exist.

### Gap D: `RestartAgent` and `RestartConnect` Missing from Clix Service (Contract gap)

The `Bridge` interface defines `restartAgent` and `restartConnect`, but `ClientService` in `app/clix/main.go` does not declare them. Wails will not generate bindings for undeclared methods. The browser path works because `clientserver` has the routes (even though they return 501).

### Gap E: Type Contract Mismatch Between Clix and Bridge (Design gap)

Clix methods use `map[string]any` / `map[string]string` for inputs and outputs. The frontend `Bridge` interface uses typed DTOs (`SessionDTO`, `AgentStatus`, `ConnectStatus`, `AgentConfig`, `ConnectConfig`). The `wailsBridge` adapter must translate between Wails-generated binding return types and these DTOs. No mapping code exists because no bindings exist.

---

## 3. Dependency Graph

```
wails.ts (null)
    │
    ▼
Wails generated bindings  ◄── DOES NOT EXIST (Gap C)
    │
    ▼
Clix ClientService        ◄── ALL STUBS (Gap B + D)
    │
    ▼
Runtime Manager           ◄── DOES NOT EXIST (Gap A)
    │
    ▼
clientapp.*               ◄── FULLY FUNCTIONAL ✓
```

The browser path has the same bottleneck:

```
bridge.ts (httpBridge)    ◄── WORKS for login/session
    │
    ▼
clientserver API          ◄── 8/12 ENDPOINTS ARE STUBS (Gap A)
    │
    ▼
Runtime Manager           ◄── DOES NOT EXIST (Gap A)
    │
    ▼
clientapp.*               ◄── FULLY FUNCTIONAL ✓
```

---

## 4. Proposals

### Proposal 1: Extract a Shared `internal/clientui` Runtime Manager

Create a new package `internal/clientui` (or `internal/clientruntime`) that owns the in-process agent and connect lifecycle:

```go
// internal/clientui/runtime.go
type Runtime struct {
    mu          sync.Mutex
    agentCancel context.CancelFunc
    agentDone   chan struct{}
    agentCfg    AgentConfigSnapshot
    agentErr    error
    connects    map[string]*clientapp.ConnectInstance
    parentCtx   context.Context
}

func NewRuntime(ctx context.Context) *Runtime
func (r *Runtime) StartAgent(cfg clientapp.AgentConfig) error
func (r *Runtime) StopAgent() error
func (r *Runtime) AgentStatus() AgentStatusDTO
func (r *Runtime) StartConnect(name string, cfg clientapp.ConnectConfig) (*ConnectDTO, error)
func (r *Runtime) StopConnect(name string) error
func (r *Runtime) RestartConnect(name string, cfg clientapp.ConnectConfig) (*ConnectDTO, error)
func (r *Runtime) ListConnects() []ConnectDTO
func (r *Runtime) Shutdown() // closes all, waits for cleanup
```

**Rationale**: Both `clientserver` and `clix` need the exact same lifecycle management. Putting it in one place avoids duplication and ensures the ADR-005 ownership contract ("closing a UI does not stop separately installed OS services") is enforced once.

**Consumers**:
- `clientserver/server.go` holds a `*clientui.Runtime`, passed to API handlers
- `app/clix/main.go` holds a `*clientui.Runtime`, exposed through `ClientService` methods
- Both call `runtime.Shutdown()` on their respective shutdown hooks

### Proposal 2: Wire Clix Login/Session Stubs First (Quick Win)

The login/session methods are 1-line delegations to `clientapp`. Wire them before the runtime manager:

```go
func (s *ClientService) Login(server, tunnelServer, username, password, totpCode string) (map[string]string, error) {
    result, err := clientapp.Login(clientapp.LoginParams{...})
    if err != nil { return nil, err }
    return map[string]string{
        "username": result.Username, "role": result.Role,
        "tunnel_url": result.TunnelURL, "expires_at": result.ExpiresAt.Format(time.RFC3339),
    }, nil
}
```

This unblocks the Clix login flow end-to-end without waiting for the runtime manager.

### Proposal 3: Two-Phase Implementation Order

**Phase A** (unblocks browser path — higher user value):
1. Create `internal/clientui` runtime manager
2. Wire `clientserver/api.go` handlers to the runtime manager
3. All 12 browser API endpoints become functional

**Phase B** (unblocks Clix desktop — depends on Phase A):
1. Wire Clix `ClientService` login/session stubs (can be done in parallel)
2. Wire Clix `ClientService` agent/connect methods to `clientui.Runtime`
3. Add missing `RestartAgent` / `RestartConnect` methods
4. Run `wails3 generate bindings` to produce TypeScript bindings
5. Implement `wails.ts` adapter mapping generated types to `Bridge` DTOs
6. Add Taskfile task for binding generation

### Proposal 4: Use Typed DTOs in Clix Instead of `map[string]any`

Replace `map[string]any` with concrete structs in `ClientService`:

```go
type SessionDTO struct {
    Username  string `json:"username"`
    Role      string `json:"role"`
    TunnelURL string `json:"tunnel_url"`
    ExpiresAt string `json:"expires_at"`
}

func (s *ClientService) Login(server, tunnelServer, username, password, totpCode string) (*SessionDTO, error)
func (s *ClientService) GetAgentStatus() (*AgentStatusDTO, error)
```

**Rationale**: Wails v3 generates TypeScript types from Go struct tags. Using `map[string]any` produces `Record<string, unknown>` in the bindings — the frontend loses all type safety. Concrete structs give the `wailsBridge` adapter zero-cost type mapping (the generated types match the `Bridge` DTOs directly).

---

## 5. Effort Estimate

| Task | Effort | Blocks |
|------|--------|--------|
| `internal/clientui` runtime manager | ~300 lines, 1 file | Phase A + B |
| Wire `clientserver/api.go` to runtime | ~80 lines changed | Phase A |
| Wire Clix login/session stubs | ~40 lines | Nothing |
| Wire Clix agent/connect to runtime | ~80 lines | Phase B |
| Add `RestartAgent`/`RestartConnect` to Clix | ~20 lines | Phase B |
| Wails binding generation + Taskfile task | ~30 lines + build config | Phase B |
| Implement `wails.ts` adapter | ~80 lines | Phase B |
| **Total** | **~630 lines** | |

---

## 6. Recommendation

**Do Phase A first.** The browser path (`cli ui`) is the primary user-facing surface today. Every user who runs `cli ui` hits 501 on agent/connect operations. Fixing the `clientserver` stubs via a shared runtime manager delivers immediate value and is independently testable.

**Phase B** (the actual `wailsBridge` wiring) is mechanical once Phase A lands. The runtime manager exists, the Clix methods delegate to it, and the only new complexity is the Wails binding generation pipeline — which is a build-system task, not an architectural one.

The `wailsBridge = null` line itself is the last thing to change. It becomes a 1-line import of the generated bindings once everything beneath it is real.
