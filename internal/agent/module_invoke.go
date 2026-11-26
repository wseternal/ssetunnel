package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/agentmod"
	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/sdk"
)

var (
	// ErrModuleNotFound reports a module the agent has no live registration for.
	ErrModuleNotFound = errors.New("module not found")

	// ErrModuleRevisionMismatch reports a caller holding a stale revision.
	ErrModuleRevisionMismatch = errors.New("module revision mismatch")

	// ErrModuleInvokerClosed reports an invocation attempted after Close.
	ErrModuleInvokerClosed = errors.New("module invoker closed")

	// ErrModuleCapacity reports the per-agent or per-module invocation limit.
	ErrModuleCapacity = errors.New("module invocation capacity exhausted")
)

// ModuleInvoker handles module invocations for an agent.
//
// Two callers reach it. The tunnel path (HandleModuleOpen) is driven by the
// server: it opens a reverse yamux stream and reads the HTTP request from it.
// The local path (InvokeLocal) is driven by the agent's own loopback API and
// holds the request in memory. Both paths share admission accounting, one
// fresh IPC connection per exchange, and the exchange deadline; only the
// tunnel path opens a stream.
type ModuleInvoker struct {
	mu       sync.Mutex
	agent    *Agent
	ipc      *agentmod.IPCServer
	registry *agentmod.Registry

	// Active invocations, keyed by invocation ID.
	invocations map[string]*Invocation

	// Limits. activeInvocations and perModuleCounts count admitted
	// invocations, whether or not the tunnel stream has opened yet.
	pendingOpens      int
	activeInvocations int
	perModuleCounts   map[string]int

	closed    bool
	nextLocal uint64
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

	// Stream is the reverse tunnel stream for a tunnel invocation. It is nil
	// for a local invocation, which has no yamux session to answer on.
	Stream *yamux.Stream

	cancel context.CancelFunc
	// release frees the admission slot; it is idempotent and owned by the
	// invocation so every exit path releases exactly once.
	release func()

	mutable sync.Mutex
	conn    net.Conn
	finish  sync.Once
}

func (inv *Invocation) setConn(conn net.Conn) {
	inv.mutable.Lock()
	inv.conn = conn
	inv.mutable.Unlock()
}

func (inv *Invocation) currentConn() net.Conn {
	inv.mutable.Lock()
	defer inv.mutable.Unlock()
	return inv.conn
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

// admitLocked reserves one invocation slot. The caller holds mi.mu.
func (mi *ModuleInvoker) admitLocked(moduleID string) (func(), error) {
	if mi.closed {
		return nil, ErrModuleInvokerClosed
	}
	if mi.activeInvocations >= moduleproto.MaxActiveInvocations {
		return nil, fmt.Errorf("%w: %d active", ErrModuleCapacity, mi.activeInvocations)
	}
	if mi.perModuleCounts[moduleID] >= moduleproto.MaxActiveInvocationsPerModule {
		return nil, fmt.Errorf("%w: module %s has %d active", ErrModuleCapacity, moduleID, mi.perModuleCounts[moduleID])
	}
	mi.activeInvocations++
	mi.perModuleCounts[moduleID]++

	var once sync.Once
	return func() {
		once.Do(func() {
			mi.mu.Lock()
			defer mi.mu.Unlock()
			mi.activeInvocations--
			mi.perModuleCounts[moduleID]--
			if mi.perModuleCounts[moduleID] <= 0 {
				delete(mi.perModuleCounts, moduleID)
			}
		})
	}, nil
}

// admit reserves one invocation slot without opening a tunnel stream.
func (mi *ModuleInvoker) admit(moduleID string) (func(), error) {
	mi.mu.Lock()
	defer mi.mu.Unlock()
	return mi.admitLocked(moduleID)
}

// beginOpen reserves one pending-open marker and one invocation slot in a
// single critical section, so the limits hold across the window in which the
// stream is being opened (C7 fix: TOCTOU race).
func (mi *ModuleInvoker) beginOpen(moduleID string) (func(), error) {
	mi.mu.Lock()
	defer mi.mu.Unlock()
	if mi.closed {
		return nil, ErrModuleInvokerClosed
	}
	if mi.pendingOpens >= moduleproto.MaxPendingOpens {
		return nil, fmt.Errorf("%w: %d pending opens", ErrModuleCapacity, mi.pendingOpens)
	}
	release, err := mi.admitLocked(moduleID)
	if err != nil {
		return nil, err
	}
	mi.pendingOpens++
	return release, nil
}

// endOpen clears one pending-open marker.
func (mi *ModuleInvoker) endOpen() {
	mi.mu.Lock()
	mi.pendingOpens--
	mi.mu.Unlock()
}

// track registers an invocation. It reports false when the invoker has been
// closed, in which case the caller must tear the invocation down itself.
func (mi *ModuleInvoker) track(inv *Invocation) bool {
	mi.mu.Lock()
	defer mi.mu.Unlock()
	if mi.closed {
		return false
	}
	mi.invocations[inv.ID] = inv
	return true
}

// HandleModuleOpen processes a module_open request from the server.
func (mi *ModuleInvoker) HandleModuleOpen(ctx context.Context, msg *mux.ControlMessage, session *yamux.Session) error {
	var payload moduleproto.ModuleOpenPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal module_open: %w", err)
	}

	release, err := mi.beginOpen(payload.ModuleID)
	if err != nil {
		return err
	}
	defer mi.endOpen()

	reg, err := mi.resolve(payload.ModuleID, payload.Revision)
	if err != nil {
		release()
		return err
	}

	stream, err := session.OpenStream()
	if err != nil {
		release()
		return fmt.Errorf("open stream: %w", err)
	}

	// Write module marker with invocation ID.
	marker := fmt.Sprintf("%s%s\n", moduleproto.ModuleStreamMarker, payload.InvocationID)
	if _, err := stream.Write([]byte(marker)); err != nil {
		stream.Close()
		release()
		return fmt.Errorf("write marker: %w", err)
	}

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
		release:       release,
	}
	if !mi.track(inv) {
		stream.Close()
		invCancel()
		release()
		return ErrModuleInvokerClosed
	}

	// Proxy HTTP exchange in background with a fresh IPC connection (C3 fix).
	go mi.proxyInvocation(invCtx, inv, reg)
	return nil
}

// resolve looks up a live registration and checks the caller's revision.
func (mi *ModuleInvoker) resolve(moduleID, revision string) (*agentmod.Registration, error) {
	reg := mi.ipc.Lookup(moduleID)
	if reg == nil {
		return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, moduleID)
	}
	if revision != "" && reg.Revision != revision {
		return nil, fmt.Errorf("%w: got %s, want %s", ErrModuleRevisionMismatch, reg.Revision, revision)
	}
	return reg, nil
}

// finish tears an invocation down exactly once: it cancels the context, closes
// the module connection and (for a tunnel invocation) the stream, unregisters
// the invocation, and releases its admission slot.
func (mi *ModuleInvoker) finish(inv *Invocation) {
	inv.finish.Do(func() {
		inv.cancel()
		if conn := inv.currentConn(); conn != nil {
			conn.Close()
		}
		if inv.Stream != nil {
			inv.Stream.Close()
		}
		mi.mu.Lock()
		delete(mi.invocations, inv.ID)
		mi.mu.Unlock()
		if inv.release != nil {
			inv.release()
		}
	})
}

// proxyInvocation proxies an HTTP exchange between the tunnel stream and the
// module's invocation endpoint over a fresh IPC connection (C3 fix).
func (mi *ModuleInvoker) proxyInvocation(ctx context.Context, inv *Invocation, reg *agentmod.Registration) {
	defer mi.finish(inv)

	invConn, err := mi.dialModule(ctx, reg)
	if err != nil {
		log.Printf("invocation %s: dial module: %v", inv.ID, err)
		// Send HTTP 503 to client before closing the stream.
		resp := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			ProtoMajor: 1, ProtoMinor: 1,
			Body: io.NopCloser(strings.NewReader("module unavailable: " + err.Error())),
		}
		resp.Write(inv.Stream)
		return
	}
	inv.setConn(invConn)

	// Set deadlines on both connections.
	deadline := invocationDeadline(ctx)
	invConn.SetDeadline(deadline)
	inv.Stream.SetDeadline(deadline)

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

// invocationDeadline returns the exchange deadline: the caller's context
// deadline when it is sooner than sdk.InvocationTimeout.
func invocationDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(sdk.InvocationTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	return deadline
}

// InvokeLocal performs one module exchange without a tunnel stream. The caller
// owns the request and must close the returned body, which closes the module
// connection and releases the admission slot.
func (mi *ModuleInvoker) InvokeLocal(ctx context.Context, moduleID, revision string, req *http.Request) (*http.Response, error) {
	release, err := mi.admit(moduleID)
	if err != nil {
		return nil, err
	}

	reg, err := mi.resolve(moduleID, revision)
	if err != nil {
		release()
		return nil, err
	}

	invCtx, invCancel := context.WithCancel(ctx)
	defer invCancel()

	mi.mu.Lock()
	mi.nextLocal++
	id := fmt.Sprintf("local-%d", mi.nextLocal)
	mi.mu.Unlock()

	inv := &Invocation{
		ID:            id,
		ModuleID:      moduleID,
		Revision:      reg.Revision,
		Method:        req.Method,
		Path:          req.URL.RequestURI(),
		CorrelationID: req.Header.Get("X-Correlation-ID"),
		StartedAt:     time.Now(),
		cancel:        invCancel,
		release:       release,
	}
	if !mi.track(inv) {
		release()
		return nil, ErrModuleInvokerClosed
	}

	conn, err := mi.dialModule(invCtx, reg)
	if err != nil {
		mi.finish(inv)
		return nil, fmt.Errorf("dial module: %w", err)
	}
	inv.setConn(conn)
	conn.SetDeadline(invocationDeadline(invCtx))

	if err := req.Write(conn); err != nil {
		mi.finish(inv)
		return nil, fmt.Errorf("write request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		mi.finish(inv)
		return nil, fmt.Errorf("read response: %w", err)
	}
	resp.Body = &invocationBody{ReadCloser: resp.Body, finish: func() { mi.finish(inv) }}
	return resp, nil
}

// invocationBody owns the module connection for a local exchange. Closing it —
// by the handler or by the invoker's Close — finishes the invocation once.
type invocationBody struct {
	io.ReadCloser
	finish func()
	once   sync.Once
}

func (b *invocationBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.finish)
	return err
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

	mi.mu.Lock()
	inv, ok := mi.invocations[payload.InvocationID]
	mi.mu.Unlock()

	if !ok {
		return nil // Already closed or not found.
	}

	log.Printf("invocation %s: cancelled (reason=%s)", payload.InvocationID, payload.Reason)
	mi.finish(inv)
	return nil
}

// Close cancels all active invocations and refuses new ones. It is idempotent;
// admission counters are left to the per-invocation releases so a second Close
// cannot zero them out from under a finishing invocation.
func (mi *ModuleInvoker) Close() {
	mi.mu.Lock()
	if mi.closed {
		mi.mu.Unlock()
		return
	}
	mi.closed = true
	invocations := mi.invocations
	mi.invocations = make(map[string]*Invocation)
	mi.mu.Unlock()

	for _, inv := range invocations {
		mi.finish(inv)
	}
}

// ActiveInvocations reports the admitted invocation count, for diagnostics.
func (mi *ModuleInvoker) ActiveInvocations() int {
	mi.mu.Lock()
	defer mi.mu.Unlock()
	return mi.activeInvocations
}
