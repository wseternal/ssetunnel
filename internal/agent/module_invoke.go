package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/agentmod"
	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// ModuleInvoker handles incoming module invocation requests from the server.
// For each invocation, it opens a reverse yamux stream and proxies the HTTP
// exchange to the module process over a fresh local IPC connection.
type ModuleInvoker struct {
	mu       sync.RWMutex
	agent    *Agent
	ipc      *agentmod.IPCServer
	registry *agentmod.Registry

	// Active invocations.
	invocations map[string]*Invocation

	// Limits.
	pendingOpens      int
	activeInvocations int
	perModuleCounts   map[string]int

	// Reserved slots (to prevent TOCTOU races).
	reservedSlots map[string]int
}

// Invocation tracks an active module invocation.
type Invocation struct {
	ID            string
	ModuleID      string
	Revision      string
	Method        string
	Path          string
	CorrelationID string
	StartedAt     time.Time
	Stream        *yamux.Stream
	cancel        context.CancelFunc
}

// NewModuleInvoker creates a module invoker for the given agent.
func NewModuleInvoker(agent *Agent, ipc *agentmod.IPCServer, registry *agentmod.Registry) *ModuleInvoker {
	return &ModuleInvoker{
		agent:           agent,
		ipc:             ipc,
		registry:        registry,
		invocations:     make(map[string]*Invocation),
		perModuleCounts: make(map[string]int),
		reservedSlots:   make(map[string]int),
	}
}

// HandleModuleOpen processes a module_open request from the server.
// It validates the request, opens a reverse stream, and proxies the HTTP exchange.
func (mi *ModuleInvoker) HandleModuleOpen(ctx context.Context, msg *mux.ControlMessage, session *yamux.Session) error {
	var payload moduleproto.ModuleOpenPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal module_open: %w", err)
	}

	// Check limits and reserve slots atomically (C7 fix: TOCTOU race).
	mi.mu.Lock()
	if mi.pendingOpens >= moduleproto.MaxPendingOpens {
		mi.mu.Unlock()
		return fmt.Errorf("too many pending opens: %d", mi.pendingOpens)
	}
	if mi.activeInvocations+mi.reservedCount() >= moduleproto.MaxActiveInvocations {
		mi.mu.Unlock()
		return fmt.Errorf("too many active invocations: %d", mi.activeInvocations)
	}
	moduleTotal := mi.perModuleCounts[payload.ModuleID] + mi.reservedSlots[payload.ModuleID]
	if moduleTotal >= moduleproto.MaxActiveInvocationsPerModule {
		mi.mu.Unlock()
		return fmt.Errorf("too many invocations for module %s", payload.ModuleID)
	}
	mi.pendingOpens++
	mi.reservedSlots[payload.ModuleID]++
	mi.mu.Unlock()

	defer func() {
		mi.mu.Lock()
		mi.pendingOpens--
		mi.mu.Unlock()
	}()

	// Validate module exists and revision matches.
	reg := mi.ipc.Lookup(payload.ModuleID)
	if reg == nil {
		mi.releaseSlot(payload.ModuleID)
		return fmt.Errorf("module not found: %s", payload.ModuleID)
	}
	if payload.Revision != "" && reg.Revision != payload.Revision {
		mi.releaseSlot(payload.ModuleID)
		return fmt.Errorf("revision mismatch: got %s, want %s", reg.Revision, payload.Revision)
	}

	// Open reverse stream to server.
	stream, err := session.OpenStream()
	if err != nil {
		mi.releaseSlot(payload.ModuleID)
		return fmt.Errorf("open stream: %w", err)
	}

	// Write module marker with invocation ID.
	marker := fmt.Sprintf("%s%s\n", moduleproto.ModuleStreamMarker, payload.InvocationID)
	if _, err := stream.Write([]byte(marker)); err != nil {
		stream.Close()
		mi.releaseSlot(payload.ModuleID)
		return fmt.Errorf("write marker: %w", err)
	}

	// Track invocation with its own context for cancellation.
	invCtx, invCancel := context.WithCancel(ctx)
	inv := &Invocation{
		ID:            payload.InvocationID,
		ModuleID:      payload.ModuleID,
		Revision:      payload.Revision,
		Method:        payload.Method,
		Path:          payload.Path,
		CorrelationID: payload.CorrelationID,
		StartedAt:     time.Now(),
		Stream:        stream,
		cancel:        invCancel,
	}

	mi.mu.Lock()
	mi.invocations[payload.InvocationID] = inv
	mi.activeInvocations++
	mi.perModuleCounts[payload.ModuleID]++
	mi.reservedSlots[payload.ModuleID]--
	mi.mu.Unlock()

	// Proxy HTTP exchange in background with a fresh IPC connection (C3 fix).
	go mi.proxyInvocation(invCtx, inv, reg)

	return nil
}

// reservedCount returns the total reserved slots across all modules.
func (mi *ModuleInvoker) reservedCount() int {
	total := 0
	for _, v := range mi.reservedSlots {
		total += v
	}
	return total
}

// releaseSlot releases a reserved slot for a module.
func (mi *ModuleInvoker) releaseSlot(moduleID string) {
	mi.mu.Lock()
	mi.reservedSlots[moduleID]--
	if mi.reservedSlots[moduleID] <= 0 {
		delete(mi.reservedSlots, moduleID)
	}
	mi.mu.Unlock()
}

// proxyInvocation proxies an HTTP exchange between the tunnel stream and the module's
// invocation endpoint over a fresh IPC connection (C3 fix: no shared connection).
func (mi *ModuleInvoker) proxyInvocation(ctx context.Context, inv *Invocation, reg *agentmod.Registration) {
	defer func() {
		inv.cancel()
		inv.Stream.Close()
		mi.mu.Lock()
		delete(mi.invocations, inv.ID)
		mi.activeInvocations--
		mi.perModuleCounts[inv.ModuleID]--
		mi.mu.Unlock()
	}()

	// Open a fresh IPC connection per invocation (C3 fix).
	invConn, err := mi.dialModule(ctx, reg)
	if err != nil {
		log.Printf("invocation %s: dial module: %v", inv.ID, err)
		return
	}
	defer invConn.Close()

	// Set deadlines from context (C13 fix).
	if deadline, ok := ctx.Deadline(); ok {
		invConn.SetDeadline(deadline)
	} else {
		// Default 30s timeout.
		invConn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	// Read HTTP request from tunnel stream.
	req, err := http.ReadRequest(bufio.NewReader(inv.Stream))
	if err != nil {
		log.Printf("invocation %s: read request: %v", inv.ID, err)
		return
	}
	defer req.Body.Close()

	// Forward to module via fresh IPC connection.
	if err := req.Write(invConn); err != nil {
		log.Printf("invocation %s: write to module: %v", inv.ID, err)
		return
	}

	// Read response from module.
	resp, err := http.ReadResponse(bufio.NewReader(invConn), req)
	if err != nil {
		log.Printf("invocation %s: read response: %v", inv.ID, err)
		return
	}
	defer resp.Body.Close()

	// Write response back to tunnel stream.
	if err := resp.Write(inv.Stream); err != nil {
		log.Printf("invocation %s: write response: %v", inv.ID, err)
		return
	}

	log.Printf("invocation %s: completed (%s %s, status=%d)", inv.ID, inv.Method, inv.Path, resp.StatusCode)
}

// dialModule opens a fresh IPC connection to the module's invocation endpoint.
// Falls back to the control connection if no invocation endpoint is available.
func (mi *ModuleInvoker) dialModule(ctx context.Context, reg *agentmod.Registration) (net.Conn, error) {
	if reg.InvEndpoint != "" {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", reg.InvEndpoint)
		if err != nil {
			return nil, fmt.Errorf("dial invocation endpoint %s: %w", reg.InvEndpoint, err)
		}
		return conn, nil
	}
	// Fallback: use the agent's IPC socket (legacy, single connection).
	return mi.ipc.DialModule(ctx)
}

// HandleModuleClose cancels an active invocation.
func (mi *ModuleInvoker) HandleModuleClose(msg *mux.ControlMessage) error {
	var payload moduleproto.ModuleClosePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal module_close: %w", err)
	}

	mi.mu.RLock()
	inv, ok := mi.invocations[payload.InvocationID]
	mi.mu.RUnlock()

	if !ok {
		return nil // Already closed or not found.
	}

	log.Printf("invocation %s: cancelled (reason=%s)", payload.InvocationID, payload.Reason)
	inv.cancel()  // Cancel the invocation context.
	inv.Stream.Close()
	return nil
}

// Close cancels all active invocations.
func (mi *ModuleInvoker) Close() {
	mi.mu.Lock()
	defer mi.mu.Unlock()

	for _, inv := range mi.invocations {
		inv.cancel()
		inv.Stream.Close()
	}
	mi.invocations = make(map[string]*Invocation)
	mi.activeInvocations = 0
	mi.perModuleCounts = make(map[string]int)
	mi.reservedSlots = make(map[string]int)
}
