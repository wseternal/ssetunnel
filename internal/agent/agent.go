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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/agentmod"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/internal/remoteapp"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// Agent runs inside the restricted network: it dials out to the server,
// multiplexes streams over the tunnel, forwards them to the configured
// TCP target, and reconnects automatically on drops (spec: agents
// recover from drops automatically).
type Agent struct {
	ServerURL  string        // tunnel server base URL
	Target     string        // TCP address to forward streams to (empty = dynamic mode)
	AgentID    string        // human-readable agent identifier (e.g. "mydevbox")
	Token      string        // Bearer token or single-use PIN for authentication
	MaxBackoff time.Duration // reconnect cap; 0 → 30 s
	MaxWait    time.Duration // batcher flush ceiling; 0 → default
	Client     *http.Client  // nil → transport default

	// RequestModifier, when set, takes precedence over Token.
	// Used for session-based auth where the token is loaded from a file.
	RequestModifier func(*http.Request)

	// Cycle-2 upstream knobs (negotiated down to the server's
	// advertisement; zero values give cycle-1 serial behavior at 256 KiB batches).
	BatchSize   int  // upstream batch ceiling; 0 → transport default
	Concurrency int  // upstream POST sender depth; 0 → 1 (serial)
	Compress    bool // negotiate gzip-per-batch

	// NoAutoTune disables the server's auto-tuning: event: tune frames
	// are ignored and the agent keeps its static CLI flags.
	NoAutoTune bool

	// Verbose enables detailed tracing of shell session lifecycle,
	// data flow (byte counts, periodic summaries), and control
	// channel activity. Useful for diagnosing cloud shell issues.
	Verbose bool

	// ModuleProbeTimeout bounds one manifest probe during a module scan.
	// Zero uses the agentmod default of 2 s.
	ModuleProbeTimeout time.Duration

	// OnTokenRefresh, when set, is called before each reconnect attempt
	// to refresh the session token. It returns the new raw token or an
	// error. On success the agent's Token field is updated; on failure
	// the current token is kept and the reconnect proceeds (the server
	// will reject an actually-expired token with 401).
	OnTokenRefresh func() (newToken string, err error)

	// registry maps yamux stream IDs to data stream handlers so the
	// control dispatcher can route resize/trace messages.
	registry *streamRegistry

	// controlMu protects sendControl and controlGen. The control handler
	// installs and clears the sender; data stream goroutines read it to
	// push trace events. controlGen is a monotonic counter used to
	// detect stale send cleanup across reconnects.
	controlMu   sync.RWMutex
	sendControl controlSender
	controlGen  int

	// traceDropLogged suppresses repeated "trace dropped" logs.
	// Reset when a new control stream is established.
	traceDropLogged atomic.Bool

	// logBuf captures recent log output so the server can request it
	// via the control channel (MsgLogRequest). Installed as a tee
	// target in Run().
	logBuf *logBuffer

	// Module infrastructure (nil when modules are not configured).
	moduleManager *ModuleManager
	moduleInvoker *ModuleInvoker
	modRegistry   *agentmod.Registry
	modIPC        *agentmod.IPCServer
	modSupervisor *agentmod.Supervisor
	// modulesDir holds the builtin module packages scanned at startup.
	modulesDir string
	// moduleAPI is the loopback module surface (nil until InitModules starts it).
	moduleAPI *moduleAPIServer

	// closeOnce makes Close idempotent: a second call must not re-run the
	// teardown sequence or reset state an in-flight handler still reads.
	closeOnce sync.Once
	closeErr  error

	// Data store forwarder correlates module data store requests with
	// server responses through the control tunnel.
	dataStoreFwd *dataStoreForwarder

	// Object store forwarder correlates module object requests with
	// server response chunks through the control tunnel.
	objectStoreFwd *objectStoreForwarder

	// forwardersOnce creates the module control forwarders once, before
	// the IPC listener can accept module requests or the supervisor can
	// start a module that calls through it.
	forwardersOnce sync.Once

	// Current yamux session (set in runOnce, cleared on disconnect).
	// Used by the module invoker to open reverse streams.
	currentSess atomic.Pointer[yamux.Session]

	// runCtx stores the Run context for use by control message handlers
	// (e.g., module_open needs a cancellable context).
	runCtx atomic.Pointer[context.Context]
}

// InitLogCapture creates the log ring buffer without touching the global
// log writer. The caller is responsible for calling log.SetOutput with a
// MultiWriter that includes the returned buffer — typically under a
// log-writer mutex so the SetOutput is serialized.
//
// When logBuf is already set, Run skips its own unprotected SetOutput,
// closing the data-race window between concurrent log writer swaps.
func (a *Agent) InitLogCapture() *LogBuffer {
	a.logBuf = newLogBuffer(256)
	return a.logBuf
}

// LogBuffer is a thread-safe ring buffer that captures recent log output.
type LogBuffer = logBuffer

// Close releases the module subsystem: it stops the loopback module API, cancels
// active invocations, stops supervised module processes, and closes the IPC
// listener. Safe to call multiple times and safe when modules were never
// initialized. Run does not call it — the Agent's owner (process exit for the
// CLI, clientapp.RunAgent for the UI) must, or each in-process restart leaks an
// accept loop, a socket FD, and module children.
func (a *Agent) Close() error {
	a.closeOnce.Do(func() {
		// Fail object store callers before the IPC server closes, so a
		// module never waits out its full timeout during teardown.
		if a.objectStoreFwd != nil {
			a.objectStoreFwd.FailAll(errObjectStoreClosed)
		}
		// Order matters: stop accepting on the loopback API first (no new
		// invocations), then drain invocations, then stop children and IPC.
		if a.moduleAPI != nil {
			a.moduleAPI.Close()
		}
		if a.moduleInvoker != nil {
			a.moduleInvoker.Close()
		}
		if a.modSupervisor != nil {
			a.modSupervisor.Stop()
		}
		if a.modIPC != nil {
			a.closeErr = a.modIPC.Close()
		}
	})
	return a.closeErr
}

// ModuleAPIURL returns the loopback module API base URL, or "" when the API is
// not running. The URL's port is kernel-assigned and only valid for this run.
func (a *Agent) ModuleAPIURL() string {
	if a.moduleAPI == nil {
		return ""
	}
	return a.moduleAPI.URL()
}

// DataStoreRequest forwards a data store CRUD request through the
// control tunnel to the server and blocks until the response arrives.
// Returns an error if the control stream is unavailable or the request
// times out. Called from the IPC layer when a module sends a DATASTORE
// command.
func (a *Agent) DataStoreRequest(req moduleproto.DataStoreRequestPayload) (*moduleproto.DataStoreResponsePayload, error) {
	if a.dataStoreFwd == nil {
		return nil, fmt.Errorf("data store not initialized")
	}
	a.controlMu.RLock()
	send := a.sendControl
	a.controlMu.RUnlock()
	if send == nil {
		return nil, fmt.Errorf("control stream not available")
	}
	return a.dataStoreFwd.Send(req, send)
}

// ObjectStoreRequest forwards one object store request through the
// control tunnel and blocks until the terminal response arrives. A get
// response carries the reassembled object bytes.
func (a *Agent) ObjectStoreRequest(ctx context.Context, req moduleproto.ObjectStoreMessage) (*moduleproto.ObjectStoreMessage, error) {
	if a.objectStoreFwd == nil {
		return nil, fmt.Errorf("object store not initialized")
	}
	a.controlMu.RLock()
	send := a.sendControl
	gen := a.controlGen
	a.controlMu.RUnlock()
	if send == nil {
		return nil, fmt.Errorf("control stream not available")
	}
	return a.objectStoreFwd.Forward(ctx, req, send, gen)
}

// initForwarders creates the module control forwarders once. It runs
// before the IPC listener starts and before supervisor.Reconcile, so a
// module that calls DataStore or ObjectStore at startup cannot observe a
// nil forwarder or race a late assignment.
func (a *Agent) initForwarders() {
	a.forwardersOnce.Do(func() {
		a.dataStoreFwd = newDataStoreForwarder()
		a.objectStoreFwd = newObjectStoreForwarder()
	})
}

// InitModules initializes the module subsystem for this agent. It creates
// the registry, IPC server, supervisor, module manager, and invoker.
// Must be called before Run if module support is desired.
func (a *Agent) InitModules(ctx context.Context, dataDir string) error {
	// Modules can call DataStore and ObjectStore as soon as Reconcile
	// starts them. Create the forwarders before the listener exists so
	// those calls never observe a nil forwarder.
	a.initForwarders()

	registry, err := agentmod.NewRegistry(dataDir)
	if err != nil {
		return fmt.Errorf("create registry: %w", err)
	}
	registry.SetManifestProbeTimeout(a.ModuleProbeTimeout)

	ipc, err := agentmod.NewIPCServer()
	if err != nil {
		return fmt.Errorf("create ipc server: %w", err)
	}

	endpoint := agentmod.IPCEndpoint(dataDir)
	listener, err := agentmod.ListenIPC(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("listen ipc: %w", err)
	}

	// Attach listener to IPC server for accept loop.
	ipc.SetListener(listener, endpoint)

	log.Printf("agent: module IPC listening on %s", endpoint)
	// S5 fix: Log only first 8 chars of credential to avoid plaintext leak.
	cred := ipc.Credential()
	if len(cred) > 8 {
		cred = cred[:8] + "..."
	}
	log.Printf("agent: module IPC credential: %s", cred)

	supervisor := agentmod.NewSupervisor(ctx, registry, ipc)
	a.modRegistry = registry
	a.modIPC = ipc
	a.modSupervisor = supervisor
	a.moduleManager = NewModuleManager(registry, ipc, supervisor)
	a.moduleInvoker = NewModuleInvoker(a, ipc, registry)

	// Wire IPC registration → auto-install into registry → snapshot publish.
	// A module started from an installed package already has a registry entry
	// naming its executable and package directory on disk. That entry is what
	// the supervisor restarts from, so a registration must not replace it with
	// an IPC-endpoint placeholder: doing so turns the next restart into a
	// failed exec of a socket and leaves a healthy module unavailable. Only a
	// module started by hand, with no package on disk, is auto-installed.
	ipc.SetOnRegister(func(reg *agentmod.Registration) {
		if existing := registry.Package(reg.ModuleID); existing != nil {
			log.Printf("agent: module %s registered from installed package %s", reg.ModuleID, existing.PackageDir)
			a.moduleManager.OnRegistryChange()
			return
		}
		// Use IPC endpoint as executable placeholder — the supervisor will
		// log a start failure, but the module is already running manually.
		if err := registry.InstallFromIPC(
			reg.ModuleID, reg.ServiceName, reg.Version, reg.Revision,
			reg.OpenAPIYAML, ipc.Endpoint(),
		); err != nil {
			log.Printf("agent: auto-install module %s: %v", reg.ModuleID, err)
			return
		}
		log.Printf("agent: auto-installed module %s into registry", reg.ModuleID)
		a.moduleManager.OnRegistryChange()
	})

	// Wire IPC deregister callback to remove the module from the registry
	// and trigger a catalog snapshot publish.
	ipc.SetOnDeregister(func(moduleID string) {
		// Keep an installed package: the process exiting (crash or restart) is
		// not an uninstall. Only the placeholder written by InstallFromIPC for
		// a hand-started module is dropped, because there is nothing on disk
		// for the supervisor to start.
		if pkg := registry.Package(moduleID); pkg != nil && pkg.Executable != ipc.Endpoint() {
			log.Printf("agent: module %s deregistered, keeping installed package", moduleID)
			a.moduleManager.OnRegistryChange()
			return
		}
		registry.Uninstall(moduleID)
		log.Printf("agent: uninstalled module %s from registry", moduleID)
		a.moduleManager.OnRegistryChange()
	})

	// Wire IPC data store handler to forward requests through the
	// control tunnel to the server's database. The authenticated module
	// ID from the IPC layer overrides the caller-supplied module_id to
	// prevent cross-module data access (C1 fix).
	ipc.SetOnDataStore(func(moduleID string, requestJSON []byte) ([]byte, error) {
		var req moduleproto.DataStoreRequestPayload
		if err := json.Unmarshal(requestJSON, &req); err != nil {
			return nil, fmt.Errorf("unmarshal data store request: %w", err)
		}
		req.ModuleID = moduleID // enforce authenticated caller identity
		resp, err := a.DataStoreRequest(req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// Wire IPC object store handler to forward requests through the
	// control tunnel to the server's local filesystem backend. The
	// authenticated module ID overrides the caller-supplied module_id
	// so a module cannot select another namespace.
	ipc.SetOnObjectStore(func(moduleID string, requestJSON []byte) ([]byte, error) {
		var req moduleproto.ObjectStoreMessage
		if err := json.Unmarshal(requestJSON, &req); err != nil {
			return nil, fmt.Errorf("unmarshal object store request: %w", err)
		}
		req.ModuleID = moduleID // enforce authenticated caller identity
		// The agent owns correlation IDs. Discard a caller-supplied one
		// so a hand-written IPC client cannot collide with another
		// module's in-flight request.
		req.RequestID = ""

		ctx, cancel := context.WithTimeout(a.runContext(), objectStoreTimeout)
		defer cancel()
		resp, err := a.ObjectStoreRequest(ctx, req)
		if err != nil {
			if resp != nil && resp.ErrorCode != "" {
				return json.Marshal(resp)
			}
			return json.Marshal(moduleproto.NewObjectStoreError(
				req.RequestID, req.Op, moduleproto.ObjectStoreErrInternal, "object store request failed"))
		}
		return json.Marshal(resp)
	})

	// Load builtin module packages installed next to the agent data
	// directory (e.g. ~/.ssetunnel/modules). Each package is registered and
	// enabled the first time it is seen.
	a.modulesDir = filepath.Join(filepath.Dir(filepath.Clean(dataDir)), "modules")
	a.loadModulePackages()

	// Reconcile desired state: start enabled modules.
	if err := supervisor.Reconcile(); err != nil {
		log.Printf("agent: module reconcile: %v", err)
	}

	// Start the loopback module API. It is independent of the tunnel: a
	// consumer on this host reaches the same modules without a session, and
	// the console keeps reaching them through the tunnel path.
	api := newModuleAPIServer(a)
	if err := api.start(); err != nil {
		log.Printf("agent: module API unavailable: %v", err)
	} else {
		a.moduleAPI = api
	}

	return nil
}

// loadModulePackages scans the modules directory, registers the packages it
// finds, and publishes a catalog update when anything changed.
func (a *Agent) loadModulePackages() {
	if a.modulesDir == "" {
		return
	}
	ids, err := a.modRegistry.ScanModules(a.modulesDir)
	if err != nil {
		log.Printf("agent: scan modules %s: %v", a.modulesDir, err)
		return
	}
	if len(ids) == 0 {
		return
	}
	log.Printf("agent: loaded %d module package(s) from %s", len(ids), a.modulesDir)
	a.moduleManager.OnRegistryChange()
}

// ReloadModules re-reads module packages and desired state from disk,
// then reconciles the supervisor to start/stop modules as needed.
// Called when the agent receives SIGHUP after CLI module commands
// modify the on-disk state.
func (a *Agent) ReloadModules() error {
	if a.modRegistry == nil || a.modSupervisor == nil {
		return fmt.Errorf("modules not initialized")
	}
	if err := a.modRegistry.Reload(); err != nil {
		return fmt.Errorf("reload registry: %w", err)
	}
	a.loadModulePackages()
	if err := a.modSupervisor.Reconcile(); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	log.Printf("agent: modules reloaded successfully")
	return nil
}

// currentYamuxSession returns the current yamux session, or nil.
func (a *Agent) currentYamuxSession() *yamux.Session {
	return a.currentSess.Load()
}

// runContext returns the agent's run context, or context.Background()
// if Run has not been called yet.
func (a *Agent) runContext() context.Context {
	if p := a.runCtx.Load(); p != nil {
		return *p
	}
	return context.Background()
}

// Run connects and reconnects until ctx is canceled. Reconnect uses
// exponential backoff (500 ms → 30 s cap with jitter) via cenkalti/backoff.
// A session that survived past the health threshold resets the backoff:
// a drop after long uptime was a network event, not a flapping server,
// so reconnect immediately.
func (a *Agent) Run(ctx context.Context) error {
	// Store run context for control message handlers (W7 fix).
	a.runCtx.Store(&ctx)

	// Create the module control forwarders before the tunnel starts.
	// InitModules normally creates them before its IPC listener; this
	// call covers a Run without modules and keeps creation single-shot.
	a.initForwarders()

	// Install log capture: all log.Printf output goes to both stderr
	// and the ring buffer. The server can request recent lines via
	// the control channel (MsgLogRequest).
	//
	// If logBuf was pre-set by the caller (e.g. clientapp.RunAgent
	// via InstallLogCapture under a log-writer mutex), skip the
	// SetOutput to avoid an unprotected concurrent write to the
	// global log writer.
	if a.logBuf == nil {
		a.logBuf = newLogBuffer(256)
		log.SetOutput(io.MultiWriter(os.Stderr, a.logBuf))
	}

	b := newAgentBackoff()
	if a.MaxBackoff > 0 {
		b.MaxInterval = a.MaxBackoff
	}
	const healthThreshold = 10 * time.Second
	for {
		// Refresh token before each (re)connect to keep long-lived
		// sessions valid. Failure is non-fatal — the current token
		// may still be accepted by the server.
		a.RefreshToken()

		start := time.Now()
		err := a.runOnce(ctx)

		// Unrecoverable errors (e.g. bad token) — exit immediately
		// instead of spinning on retries that will never succeed.
		if errors.Is(err, transport.ErrUnauthorized) {
			log.Printf("agent: %v; giving up", err)
			return err
		}
		if err != nil {
			log.Printf("agent: %v; reconnecting", err)
		} else {
			log.Printf("agent: session ended; reconnecting")
		}
		if ctx.Err() != nil {
			return nil
		}

		// A long-lived healthy session resets backoff: reconnect
		// immediately since the drop was likely transient.
		if time.Since(start) >= healthThreshold {
			b.Reset()
			continue
		}

		delay := b.NextBackOff()
		if delay == backoff.Stop {
			delay = 500 * time.Millisecond
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
	}
}

// RefreshToken calls OnTokenRefresh to obtain a new session token.
// On success it updates Agent.Token; on failure (or when OnTokenRefresh
// is nil) it logs a warning and keeps the current token.
func (a *Agent) RefreshToken() {
	if a.OnTokenRefresh == nil {
		return
	}
	newToken, err := a.OnTokenRefresh()
	if err != nil {
		log.Printf("agent: token refresh failed: %v (continuing with current token)", err)
		return
	}
	if newToken != "" && newToken != a.Token {
		a.Token = newToken
		log.Printf("agent: session token refreshed")
	}
}

// newAgentBackoff builds the exponential backoff strategy for agent
// reconnection: 500 ms initial, 2× multiplier, 30 s cap, no elapsed
// ceiling (retry forever), 10 % jitter.
func newAgentBackoff() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 500 * time.Millisecond
	b.MaxInterval = 30 * time.Second
	b.MaxElapsedTime = 0 // never stop
	b.Multiplier = 2.0
	b.RandomizationFactor = 0.1
	return b
}

// runOnce holds one session: connect → yamux client → accept streams →
// dial target → proxy. It returns when the session dies so Run can
// reconnect with a fresh session ID.
func (a *Agent) runOnce(ctx context.Context) error {
	conn, err := transport.DialAgent(ctx, transport.Config{
		URL:              a.ServerURL,
		Token:            a.Token,
		RequestModifier:  a.RequestModifier,
		MaxWait:          a.MaxWait,
		Client:           a.Client,
		MaxBatchSize:     a.BatchSize,
		Concurrency:      a.Concurrency,
		Compress:         a.Compress,
		AgentID:          a.AgentID,
		WantTargetHeader: a.Target == "", // dynamic mode: read target from stream
		OnTokenUpgrade: func(newToken string) {
			a.Token = newToken
			log.Printf("agent: PIN redeemed, upgraded to persistent token")
		},
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	// Wire auto-tuning: parse event: tune JSON and apply batch size changes.
	// Concurrency changes are deferred to reconnect (v1 limitation).
	if !a.NoAutoTune {
		conn.OnTune = func(data []byte) {
			var params metrics.TransportParams
			if err := json.Unmarshal(data, &params); err != nil {
				log.Printf("agent: bad tune frame: %v", err)
				return
			}
			log.Printf("agent: auto-tune: batch_size=%d compress=%v concurrency=%d",
				params.BatchSize, params.Compress, params.Concurrency)
			conn.ApplyTune(params.BatchSize, params.Compress)
		}
	}
	sess, err := mux.Client(conn)
	if err != nil {
		return fmt.Errorf("mux client: %w", err)
	}
	defer sess.Close()
	a.currentSess.Store(sess)
	defer a.currentSess.Store(nil)
	a.registry = newStreamRegistry()

	// Open the control stream: agent → server. The server's accept
	// loop picks it up and identifies it by the marker. This runs
	// before the data stream accept loop so the control channel is
	// ready before any data streams arrive.
	a.openControlStream(sess)

	// Wire module control sender if modules are configured.
	if a.moduleManager != nil {
		a.controlMu.RLock()
		sender := a.sendControl
		a.controlMu.RUnlock()
		if sender != nil {
			a.moduleManager.SetControlSender(func(msg *mux.ControlMessage) error {
				a.controlMu.RLock()
				s := a.sendControl
				a.controlMu.RUnlock()
				if s == nil {
					return fmt.Errorf("control stream not available")
				}
				s(msg) // controlSender returns no error; failures are logged downstream
				return nil
			})
			// Publish catalog snapshot on connect.
			if err := a.moduleManager.OnConnect(ctx, conn.SessionID()); err != nil {
				log.Printf("agent: publish catalog on connect: %v", err)
			}
		}
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-done:
		}
	}()
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			// Context canceled = clean shutdown (Run will check ctx.Err()
			// and return without logging). Otherwise, return the error
			// so Run can log the actual reason the session died.
			if ctx.Err() != nil {
				return nil
			}
			// Enrich the error with transport-level context. The SSE
			// stream close reason (ReadErr) is the most common root
			// cause: the server closed the HTTP response, killing the
			// agent's yamux session via the underlying conn.
			if rerr := conn.ReadErr(); rerr != nil {
				return fmt.Errorf("accept stream: %w (sse: %v)", err, rerr)
			}
			return fmt.Errorf("accept stream: %w", err)
		}
		go a.acceptStream(stream)
	}
}

// acceptStream handles a newly accepted yamux data stream. The control
// stream is opened separately in openControlStream and never reaches
// this function. In dynamic mode (a.Target == ""), the server writes
// the target address as the first line. In fixed mode, the stream
// carries raw data with no header.
func (a *Agent) acceptStream(stream net.Conn) {
	sid := getStreamID(stream)
	if a.Verbose {
		verboseTrace("stream accepted: yamux_id=%d dynamic=%v", sid, a.Target == "")
	}
	// In dynamic mode, read the target header line from the server.
	if a.Target == "" {
		reader := bufio.NewReader(stream)
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("agent: read target header: %v", err)
			stream.Close()
			return
		}
		target := strings.TrimSpace(line)
		if a.Verbose {
			verboseTrace("stream target header: yamux_id=%d target=%q", sid, target)
		}
		wrapped := &readerConn{Reader: reader, Conn: stream}
		a.proxyStream(wrapped, target)
		return
	}

	// Fixed mode: proxy directly, no header to read.
	a.proxyStream(stream, a.Target)
}

// proxyStream forwards one data stream to a target, bidirectionally.
// target is the trimmed first line of the stream (from acceptStream).
// In fixed-target mode (a.Target != ""), target is ignored and a.Target
// is used directly.
func (a *Agent) proxyStream(stream net.Conn, target string) {
	// In fixed-target mode, override the target from the stream header.
	if a.Target != "" {
		target = a.Target
	}

	sid := getStreamID(stream)

	if target == "" || target == "*" {
		log.Printf("agent: empty or wildcard target header, closing stream")
		stream.Close()
		return
	}

	if a.Verbose {
		verboseTrace("stream routing: yamux_id=%d target=%q", sid, target)
	}

	// Shell target: spawn a local shell with PTY instead of dialing TCP.
	if target == TargetShell {
		if a.Verbose {
			verboseTrace("shell proxy entering: yamux_id=%d", sid)
		}
		proxyShell(stream, sid, a)
		if a.Verbose {
			verboseTrace("shell proxy exited: yamux_id=%d", sid)
		}
		return
	}

	// Remote app target: screen capture + input replay via robotgo.
	if target == TargetRemoteApp {
		remoteapp.ProxyRemoteApp(stream)
		return
	}

	conn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		log.Printf("agent: dial target %s: %v", target, err)
		stream.Close()
		return
	}
	go func() { io.Copy(conn, stream); conn.Close() }()
	go func() { io.Copy(stream, conn); stream.Close() }()
}

// readerConn wraps a bufio.Reader over a net.Conn so that bytes already
// buffered by the reader are not lost when switching from header parsing
// to raw io.Copy.
type readerConn struct {
	*bufio.Reader
	net.Conn
}

func (r *readerConn) Read(b []byte) (int, error) {
	return r.Reader.Read(b)
}

func (r *readerConn) Close() error {
	return r.Conn.Close()
}

// getStreamID extracts the yamux stream ID from a net.Conn, which may
// be wrapped in a readerConn. Returns 0 if the underlying type is not
// a yamux stream.
func getStreamID(stream net.Conn) uint32 {
	if rc, ok := stream.(*readerConn); ok {
		if ys, ok := rc.Conn.(*yamux.Stream); ok {
			return ys.StreamID()
		}
	}
	if ys, ok := stream.(*yamux.Stream); ok {
		return ys.StreamID()
	}
	return 0
}
