package clientserver

import (
	"context"
	"errors"
	"log"
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
// responsive while the old goroutine exits.
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

// status snapshots the current run. It never waits on teardown.
func (ar *agentRuntime) status() agentStatus {
	ar.mu.Lock()
	run := ar.cur
	ar.mu.Unlock()

	if run == nil {
		return agentStatus{}
	}
	st := agentStatus{
		Running:     !run.exited(),
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
	ServerURL   string `json:"server_url"`
	Target      string `json:"target"`
	AgentID     string `json:"agent_id"`
	BatchSize   int    `json:"batch_size"`
	Concurrency int    `json:"concurrency"`
	Compress    bool   `json:"compress"`
	Error       string `json:"error,omitempty"`
}

func waitDone(done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(timeout):
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
	defer cr.mu.Unlock()

	if _, exists := cr.instances[name]; exists {
		return errors.New("connect instance already exists: " + name)
	}

	inst, err := clientapp.StartNamedConnect(cr.parentCtx, name, cfg)
	if err != nil {
		return err
	}
	cr.instances[name] = inst
	return nil
}

func (cr *connectRegistry) get(name string) (*clientapp.ConnectInstance, bool) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	inst, ok := cr.instances[name]
	return inst, ok
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
		info := connectInfo{
			Name:      name,
			LocalAddr: inst.LocalAddr,
			ServerURL: inst.Config.ServerURL,
			AgentID:   inst.Config.AgentID,
			Target:    inst.Config.Target,
			Running:   inst.Err() == nil,
		}
		if e := inst.Err(); e != nil {
			info.Error = uiError(e)
		}
		result = append(result, info)
	}
	return result
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
	select {
	case <-done:
	case <-time.After(teardownTimeout):
		log.Printf("connect: stopAll timed out after %s", teardownTimeout)
	}
}
