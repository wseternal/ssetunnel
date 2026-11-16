package clientserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wseternal/ssetunnel/internal/clientapp"
)

// teardownTimeout bounds waits for a run to exit. The worst case is
// auth.RefreshSession's 15 s non-cancellable HTTP client inside RunAgent's
// shutdown path, plus module teardown.
const teardownTimeout = 20 * time.Second

// errNotFound marks a connect name that is not registered.
var errNotFound = errors.New("connect instance not found")

// agentRun is one agent lifecycle. The goroutine owns err and done: it
// stores the exit error, then closes done. Readers use the atomic pointer,
// so the goroutine never needs a lifecycle lock — the property whose
// absence deadlocked the previous shared-field design.
type agentRun struct {
	cfg    clientapp.AgentConfig
	cancel context.CancelFunc
	done   chan struct{}
	err    atomic.Pointer[error]
}

func (r *agentRun) exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// agentRuntime manages a single agent instance owned by the UI server.
// opMu serializes start/stop transitions; mu guards only the current-run
// pointer so status polls never block on teardown.
type agentRuntime struct {
	opMu sync.Mutex
	mu   sync.Mutex
	cur  *agentRun
}

// start launches the agent, replacing any running instance. The previous
// run is cancelled and awaited without holding mu, so status stays
// responsive while the old goroutine exits. If an external agent is
// detected (running outside this process), it is terminated first to
// prevent dual-agent conflicts.
func (ar *agentRuntime) start(parentCtx context.Context, cfg clientapp.AgentConfig) error {
	ar.opMu.Lock()
	defer ar.opMu.Unlock()

	ar.mu.Lock()
	prev := ar.cur
	ar.mu.Unlock()

	if prev != nil {
		prev.cancel()
		if !waitDone(prev.done, teardownTimeout) {
			return errors.New("agent: previous instance did not exit within teardown timeout")
		}
	}

	// Detect and terminate any external agent before starting a new one.
	// This prevents dual-agent conflicts where both agents connect to the
	// server with the same AgentID.
	if ext := clientapp.ReadExternalAgentStatus(); ext != nil {
		if ext.PID != os.Getpid() {
			log.Printf("ui: terminating external agent (pid %d) before starting in-process", ext.PID)
			if err := clientapp.TerminateExternalAgent(ext.PID, 5*time.Second); err != nil {
				return fmt.Errorf("agent: failed to terminate external agent (pid %d): %w", ext.PID, err)
			}
		}
	}

	ctx, cancel := context.WithCancel(parentCtx)
	run := &agentRun{cfg: cfg, cancel: cancel, done: make(chan struct{})}
	go func() {
		err := clientapp.RunAgent(ctx, cfg)
		if err != nil {
			log.Printf("ui: agent exited: %v", err)
		}
		run.err.Store(&err)
		close(run.done)
	}()

	ar.mu.Lock()
	ar.cur = run
	ar.mu.Unlock()
	return nil
}

// stop cancels the running agent and waits for it to exit. It clears the
// current run only if it is still the captured one, so a replacement
// installed by a concurrent start is never orphaned.
func (ar *agentRuntime) stop() error {
	ar.opMu.Lock()
	defer ar.opMu.Unlock()

	ar.mu.Lock()
	run := ar.cur
	ar.mu.Unlock()

	if run == nil {
		return nil
	}
	run.cancel()
	if !waitDone(run.done, teardownTimeout) {
		return errors.New("agent: instance did not exit within teardown timeout")
	}

	ar.mu.Lock()
	if ar.cur == run {
		ar.cur = nil
	}
	ar.mu.Unlock()
	return nil
}

// status snapshots the current run. When no in-process agent is
// running, it checks for an external agent via the status file
// written by `cli agent run` or a service-managed agent.
func (ar *agentRuntime) status() agentStatus {
	ar.mu.Lock()
	run := ar.cur
	ar.mu.Unlock()

	if run == nil {
		// Check for an agent running outside this process.
		if ext := clientapp.ReadExternalAgentStatus(); ext != nil {
			// Guard against "ghost external": if the file's PID matches
			// our own process, it's a stale entry from this process's
			// own agent that hasn't been cleaned up yet.
			if ext.PID != os.Getpid() {
				return agentStatus{
					Running:     true,
					External:    true,
					PID:         ext.PID,
					ServerURL:   ext.ServerURL,
					Target:      ext.Target,
					AgentID:     ext.AgentID,
					BatchSize:   ext.BatchSize,
					Concurrency: ext.Concurrency,
					Compress:    ext.Compress,
				}
			}
		}
		return agentStatus{}
	}

	// run is non-nil. Check for a real external agent (not ghost) first.
	if run.exited() {
		if ext := clientapp.ReadExternalAgentStatus(); ext != nil && ext.PID != os.Getpid() {
			return agentStatus{
				Running:     true,
				External:    true,
				PID:         ext.PID,
				ServerURL:   ext.ServerURL,
				Target:      ext.Target,
				AgentID:     ext.AgentID,
				BatchSize:   ext.BatchSize,
				Concurrency: ext.Concurrency,
				Compress:    ext.Compress,
			}
		}
		// No external agent — fall through to report the exited run's error.
	}

	st := agentStatus{
		Running:     !run.exited(),
		PID:         os.Getpid(),
		ServerURL:   run.cfg.ServerURL,
		Target:      run.cfg.Target,
		AgentID:     run.cfg.AgentID,
		BatchSize:   run.cfg.BatchSize,
		Concurrency: run.cfg.Concurrency,
		Compress:    run.cfg.Compress,
	}
	if e := run.err.Load(); e != nil && *e != nil {
		st.Error = uiError(*e)
	}
	return st
}

// uiError translates CLI-shaped workflow errors into browser wording and
// neutralizes control characters so a remote error cannot forge log lines
// or inject markup into the UI.
func uiError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "--server is required", "no server configured")
	msg = strings.ReplaceAll(msg, "run login", "log in")
	msg = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, msg)
	if len(msg) > 200 {
		msg = string([]rune(msg)[:200]) + "…"
	}
	return msg
}

// agentStatus is the JSON-serializable agent state.
type agentStatus struct {
	Running     bool   `json:"running"`
	External    bool   `json:"external,omitempty"`
	PID         int    `json:"pid,omitempty"`
	ServerURL   string `json:"server_url"`
	Target      string `json:"target"`
	AgentID     string `json:"agent_id"`
	BatchSize   int    `json:"batch_size"`
	Concurrency int    `json:"concurrency"`
	Compress    bool   `json:"compress"`
	Error       string `json:"error,omitempty"`
}

func waitDone(done <-chan struct{}, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// connectRegistry manages named connect instances.
type connectRegistry struct {
	mu        sync.Mutex
	instances map[string]*clientapp.ConnectInstance
	parentCtx context.Context
}

func newConnectRegistry(ctx context.Context) *connectRegistry {
	return &connectRegistry{
		instances: make(map[string]*clientapp.ConnectInstance),
		parentCtx: ctx,
	}
}

func (cr *connectRegistry) start(name string, cfg clientapp.ConnectConfig) error {
	cr.mu.Lock()
	if prev, exists := cr.instances[name]; exists {
		// Evict dead entries so a failed connect can be retried with the
		// same name without requiring an explicit stop first.
		select {
		case <-prev.Done():
			delete(cr.instances, name)
		default:
			cr.mu.Unlock()
			return errors.New("connect instance already exists: " + name)
		}
	}
	cr.mu.Unlock()

	inst, err := clientapp.StartNamedConnect(cr.parentCtx, name, cfg)
	if err != nil {
		return err
	}

	cr.mu.Lock()
	// A concurrent goroutine may have inserted the same name while we
	// were starting; close ours as the orphan and fail.
	if _, exists := cr.instances[name]; exists {
		cr.mu.Unlock()
		_ = inst.Close()
		return errors.New("connect instance already exists: " + name)
	}
	cr.instances[name] = inst
	cr.mu.Unlock()
	return nil
}

func (cr *connectRegistry) stop(name string) error {
	cr.mu.Lock()
	inst, exists := cr.instances[name]
	if exists {
		delete(cr.instances, name)
	}
	cr.mu.Unlock()

	if !exists {
		return errNotFound
	}
	// Close returns the session's terminal run error, not a stop failure:
	// tearing down an errored session is still a successful stop.
	if err := inst.Close(); err != nil {
		log.Printf("connect[%s]: closed (last error: %v)", name, err)
	}
	return nil
}

type connectInfo struct {
	Name      string `json:"name"`
	LocalAddr string `json:"local_addr"`
	ServerURL string `json:"server_url"`
	AgentID   string `json:"agent_id"`
	Target    string `json:"target"`
	Running   bool   `json:"running"`
	Error     string `json:"error,omitempty"`
}

func (cr *connectRegistry) list() []connectInfo {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	result := make([]connectInfo, 0, len(cr.instances))
	for name, inst := range cr.instances {
		// Read Err() once to avoid an inconsistent snapshot: the connect
		// goroutine can set err between two reads, producing a contradictory
		// {Running: true, Error: "..."} JSON.
		instErr := inst.Err()
		info := connectInfo{
			Name:      name,
			LocalAddr: inst.LocalAddr,
			ServerURL: inst.Config.ServerURL,
			AgentID:   inst.Config.AgentID,
			Target:    inst.Config.Target,
			Running:   instErr == nil && !isDone(inst.Done()),
		}
		if instErr != nil {
			info.Error = uiError(instErr)
		}
		result = append(result, info)
	}
	return result
}

// isDone reports whether ch has been closed without blocking.
func isDone(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// restart atomically stops a named connect (if running) and starts a new
// one. Empty fields in cfg inherit the running instance's values so a bare
// restart keeps the same tunnel instead of silently re-targeting. The
// registry lock is released during Close so list() stays responsive.
func (cr *connectRegistry) restart(name string, cfg clientapp.ConnectConfig) error {
	cr.mu.Lock()
	inst, exists := cr.instances[name]
	if exists {
		delete(cr.instances, name)
	}
	// Merge config from the running instance before releasing the lock,
	// so the read and replacement are atomic.
	if exists {
		if cfg.ServerURL == "" {
			cfg.ServerURL = inst.Config.ServerURL
		}
		if cfg.AgentID == "" {
			cfg.AgentID = inst.Config.AgentID
		}
		if cfg.Target == "" {
			cfg.Target = inst.Config.Target
		}
		if cfg.Local == "" {
			cfg.Local = inst.Config.Local
		}
		if cfg.BatchSize == 0 {
			cfg.BatchSize = inst.Config.BatchSize
		}
	}
	cr.mu.Unlock()

	if exists {
		if err := inst.Close(); err != nil {
			log.Printf("connect[%s]: closed (last error: %v)", name, err)
		}
	}

	cr.mu.Lock()
	if orphan, exists := cr.instances[name]; exists {
		cr.mu.Unlock()
		_ = orphan.Close()
		return errors.New("connect instance already exists: " + name)
	}

	newInst, err := clientapp.StartNamedConnect(cr.parentCtx, name, cfg)
	if err != nil {
		cr.mu.Unlock()
		return err
	}
	cr.instances[name] = newInst
	cr.mu.Unlock()
	log.Printf("connect[%s]: restarted (target=%s)", name, cfg.Target)
	return nil
}

// stopAll shuts down every connect instance in parallel (called on server
// shutdown and logout). Each Close is bounded by the transport's context
// cancellation; the overall wait is capped so one stuck instance cannot
// block the rest indefinitely.
func (cr *connectRegistry) stopAll() {
	cr.mu.Lock()
	instances := cr.instances
	cr.instances = make(map[string]*clientapp.ConnectInstance)
	cr.mu.Unlock()

	if len(instances) == 0 {
		return
	}

	var wg sync.WaitGroup
	for name, inst := range instances {
		wg.Add(1)
		go func(name string, inst *clientapp.ConnectInstance) {
			defer wg.Done()
			if err := inst.Close(); err != nil {
				log.Printf("connect[%s]: closed (last error: %v)", name, err)
			}
		}(name, inst)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(teardownTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		log.Printf("connect: stopAll timed out after %s", teardownTimeout)
	}
}
