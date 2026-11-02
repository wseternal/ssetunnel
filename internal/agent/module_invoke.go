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
// exchange to the module process over local IPC.
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
}

// NewModuleInvoker creates a module invoker for the given agent.
func NewModuleInvoker(agent *Agent, ipc *agentmod.IPCServer, registry *agentmod.Registry) *ModuleInvoker {
	return &ModuleInvoker{
		agent:           agent,
		ipc:             ipc,
		registry:        registry,
		invocations:     make(map[string]*Invocation),
		perModuleCounts: make(map[string]int),
	}
}

// HandleModuleOpen processes a module_open request from the server.
// It validates the request, opens a reverse stream, and proxies the HTTP exchange.
func (mi *ModuleInvoker) HandleModuleOpen(ctx context.Context, msg *mux.ControlMessage, session *yamux.Session) error {
	var payload moduleproto.ModuleOpenPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal module_open: %w", err)
	}

	// Check limits.
	mi.mu.Lock()
	if mi.pendingOpens >= moduleproto.MaxPendingOpens {
		mi.mu.Unlock()
		return fmt.Errorf("too many pending opens: %d", mi.pendingOpens)
	}
	if mi.activeInvocations >= moduleproto.MaxActiveInvocations {
		mi.mu.Unlock()
		return fmt.Errorf("too many active invocations: %d", mi.activeInvocations)
	}
	if mi.perModuleCounts[payload.ModuleID] >= moduleproto.MaxActiveInvocationsPerModule {
		mi.mu.Unlock()
		return fmt.Errorf("too many invocations for module %s", payload.ModuleID)
	}
	mi.pendingOpens++
	mi.mu.Unlock()

	defer func() {
		mi.mu.Lock()
		mi.pendingOpens--
		mi.mu.Unlock()
	}()

	// Validate module exists and revision matches.
	reg := mi.ipc.Lookup(payload.ModuleID)
	if reg == nil {
		return fmt.Errorf("module not found: %s", payload.ModuleID)
	}
	if payload.Revision != "" && reg.Revision != payload.Revision {
		return fmt.Errorf("revision mismatch: got %s, want %s", reg.Revision, payload.Revision)
	}

	// Open reverse stream to server.
	stream, err := session.OpenStream()
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	// Write module marker with invocation ID.
	marker := fmt.Sprintf("%s%s\n", moduleproto.ModuleStreamMarker, payload.InvocationID)
	if _, err := stream.Write([]byte(marker)); err != nil {
		stream.Close()
		return fmt.Errorf("write marker: %w", err)
	}

	// Track invocation.
	inv := &Invocation{
		ID:            payload.InvocationID,
		ModuleID:      payload.ModuleID,
		Revision:      payload.Revision,
		Method:        payload.Method,
		Path:          payload.Path,
		CorrelationID: payload.CorrelationID,
		StartedAt:     time.Now(),
		Stream:        stream,
	}

	mi.mu.Lock()
	mi.invocations[payload.InvocationID] = inv
	mi.activeInvocations++
	mi.perModuleCounts[payload.ModuleID]++
	mi.mu.Unlock()

	// Proxy HTTP exchange in background.
	go mi.proxyInvocation(ctx, inv, reg.Conn)

	return nil
}

// proxyInvocation proxies an HTTP exchange between the tunnel stream and the module's IPC connection.
func (mi *ModuleInvoker) proxyInvocation(ctx context.Context, inv *Invocation, ipcConn net.Conn) {
	defer func() {
		inv.Stream.Close()
		mi.mu.Lock()
		delete(mi.invocations, inv.ID)
		mi.activeInvocations--
		mi.perModuleCounts[inv.ModuleID]--
		mi.mu.Unlock()
	}()

	// Read HTTP request from stream.
	req, err := http.ReadRequest(bufio.NewReader(inv.Stream))
	if err != nil {
		log.Printf("invocation %s: read request: %v", inv.ID, err)
		return
	}
	defer req.Body.Close()

	// Forward to module via IPC connection.
	// Write the request to the IPC connection.
	if err := req.Write(ipcConn); err != nil {
		log.Printf("invocation %s: write to module: %v", inv.ID, err)
		return
	}

	// Read response from module.
	resp, err := http.ReadResponse(bufio.NewReader(ipcConn), req)
	if err != nil {
		log.Printf("invocation %s: read response: %v", inv.ID, err)
		return
	}
	defer resp.Body.Close()

	// Write response back to stream.
	if err := resp.Write(inv.Stream); err != nil {
		log.Printf("invocation %s: write response: %v", inv.ID, err)
		return
	}

	log.Printf("invocation %s: completed (%s %s, status=%d)", inv.ID, inv.Method, inv.Path, resp.StatusCode)
}

// HandleModuleClose cancels an active invocation.
func (mi *ModuleInvoker) HandleModuleClose(msg *mux.ControlMessage) error {
	var payload moduleproto.ModuleClosePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal module_close: %w", err)
	}

	mi.mu.Lock()
	inv, ok := mi.invocations[payload.InvocationID]
	mi.mu.Unlock()

	if !ok {
		return nil // Already closed or not found.
	}

	log.Printf("invocation %s: cancelled (reason=%s)", payload.InvocationID, payload.Reason)
	inv.Stream.Close()
	return nil
}

// Close cancels all active invocations.
func (mi *ModuleInvoker) Close() {
	mi.mu.Lock()
	defer mi.mu.Unlock()

	for _, inv := range mi.invocations {
		inv.Stream.Close()
	}
	mi.invocations = make(map[string]*Invocation)
	mi.activeInvocations = 0
	mi.perModuleCounts = make(map[string]int)
}
