package clientserver

import (
	"context"
	"errors"
	"sync"

	"github.com/wseternal/ssetunnel/internal/clientapp"
)

// agentRuntime manages a single agent instance owned by the UI server.
// Only one agent runs at a time; start replaces a running agent.
type agentRuntime struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	cfg    clientapp.AgentConfig
	err    error
}

// start launches the agent. If one is already running, it is stopped first.
func (ar *agentRuntime) start(parentCtx context.Context, cfg clientapp.AgentConfig) error {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	// Stop existing agent if running.
	if ar.cancel != nil {
		ar.cancel()
		<-ar.done
	}

	ctx, cancel := context.WithCancel(parentCtx)
	ar.cancel = cancel
	ar.done = make(chan struct{})
	ar.cfg = cfg
	ar.err = nil

	go func() {
		defer close(ar.done)
		err := clientapp.RunAgent(ctx, cfg)
		ar.mu.Lock()
		ar.err = err
		ar.mu.Unlock()
	}()

	return nil
}

// stop cancels the running agent and waits for it to exit.
func (ar *agentRuntime) stop() error {
	ar.mu.Lock()
	cancel := ar.cancel
	done := ar.done
	ar.mu.Unlock()

	if cancel == nil {
		return nil
	}
	cancel()
	<-done

	ar.mu.Lock()
	ar.cancel = nil
	ar.done = nil
	ar.mu.Unlock()
	return nil
}

// status returns the current agent state.
func (ar *agentRuntime) status() agentStatus {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	running := ar.cancel != nil && ar.done != nil
	select {
	case <-ar.done:
		running = false
	default:
	}

	return agentStatus{
		Running:     running,
		ServerURL:   ar.cfg.ServerURL,
		Target:      ar.cfg.Target,
		AgentID:     ar.cfg.AgentID,
		BatchSize:   ar.cfg.BatchSize,
		Concurrency: ar.cfg.Concurrency,
		Compress:    ar.cfg.Compress,
		Err:         ar.err,
	}
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
	Err         error  `json:"-"`
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

func (cr *connectRegistry) stop(name string) error {
	cr.mu.Lock()
	inst, exists := cr.instances[name]
	if exists {
		delete(cr.instances, name)
	}
	cr.mu.Unlock()

	if !exists {
		return errors.New("connect instance not found: " + name)
	}
	return inst.Close()
}

type connectInfo struct {
	Name      string `json:"name"`
	LocalAddr string `json:"local_addr"`
	ServerURL string `json:"server_url"`
	AgentID   string `json:"agent_id"`
	Target    string `json:"target"`
	Running   bool   `json:"running"`
}

func (cr *connectRegistry) list() []connectInfo {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	result := make([]connectInfo, 0, len(cr.instances))
	for name, inst := range cr.instances {
		running := true
		if inst.Err() != nil {
			running = false
		}
		result = append(result, connectInfo{
			Name:      name,
			LocalAddr: inst.LocalAddr,
			Running:   running,
		})
	}
	return result
}

// stopAll shuts down every connect instance (called on server shutdown).
func (cr *connectRegistry) stopAll() {
	cr.mu.Lock()
	instances := make(map[string]*clientapp.ConnectInstance, len(cr.instances))
	for k, v := range cr.instances {
		instances[k] = v
	}
	cr.instances = make(map[string]*clientapp.ConnectInstance)
	cr.mu.Unlock()

	for _, inst := range instances {
		inst.Close()
	}
}
