package clientapp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/wseternal/ssetunnel/internal/connect"
)

// ConnectConfig contains the parameters for starting a connect session.
type ConnectConfig struct {
	// ServerURL overrides the saved session URL when set.
	ServerURL string
	// AgentID for routing (e.g. mydevbox).
	AgentID string
	// Target address on the agent machine (e.g. 127.0.0.1:22).
	Target string
	// Local listen TCP address (e.g. 127.0.0.1:3306) or "-" for Stdio mode.
	Local string
	// BatchSize upstream batch ceiling in bytes (0 = 256 KiB default; 1024..1048576).
	BatchSize int
}

// ConnectInstance represents a named running connect session.
// UI surfaces use Name to track and manage multiple concurrent connections.
type ConnectInstance struct {
	Name      string
	LocalAddr string
	cancel    context.CancelFunc
	listener  net.Listener
	done      chan struct{}
	err       error
	mu        sync.Mutex
}

// Close cancels the connect session and waits for cleanup.
func (ci *ConnectInstance) Close() error {
	ci.cancel()
	if ci.listener != nil {
		ci.listener.Close()
	}
	<-ci.done
	ci.mu.Lock()
	defer ci.mu.Unlock()
	return ci.err
}

// Err returns the error from the connect session, if any.
func (ci *ConnectInstance) Err() error {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	return ci.err
}

// RunConnect starts a connect session with the given configuration and blocks
// until the context is cancelled or the session exits.
func RunConnect(ctx context.Context, cfg ConnectConfig) error {
	if cfg.Local == "" {
		return errors.New("--local is required (e.g. --local 127.0.0.1:3306 or --local -)")
	}

	sess, err := ResolveSession(cfg.ServerURL, "connect")
	if err != nil {
		return err
	}

	client := connect.NewClient(sess.URL, sess.Token, cfg.AgentID, cfg.Target)
	// Clamp --batch-size: 0 means "use default" (no clamp), anything else
	// is bounded to 1 KiB..1 MiB so a typo can't trigger a 413 on the server.
	if cfg.BatchSize != 0 {
		cfg.BatchSize = ClampBatchSize(cfg.BatchSize)
	}
	client.BatchSize = cfg.BatchSize

	if cfg.Local == "-" {
		log.Printf("connect: running in Stdio mode connecting to %s (agent=%s, target=%s)", sess.URL, cfg.AgentID, cfg.Target)
		return client.ServeStdio(ctx)
	}

	ln, err := net.Listen("tcp", cfg.Local)
	if err != nil {
		return fmt.Errorf("listen local %s: %w", cfg.Local, err)
	}
	log.Printf("connect: listening on local port %s -> server %s (agent=%s, target=%s)", cfg.Local, sess.URL, cfg.AgentID, cfg.Target)
	return client.ServeListener(ctx, ln)
}

// StartNamedConnect starts a connect session in a background goroutine and
// returns a ConnectInstance for lifecycle management. The instance is tracked
// by name; duplicate names are rejected before any resource is allocated.
func StartNamedConnect(ctx context.Context, name string, cfg ConnectConfig) (*ConnectInstance, error) {
	if name == "" {
		return nil, errors.New("name is required")
	}
	if cfg.Local == "" || cfg.Local == "-" {
		return nil, errors.New("--local TCP address is required for named connections")
	}

	// Bind the listener before launching the goroutine so bind failures
	// are synchronous and leave no partial runtime entry.
	ln, err := net.Listen("tcp", cfg.Local)
	if err != nil {
		return nil, fmt.Errorf("listen local %s: %w", cfg.Local, err)
	}

	childCtx, cancel := context.WithCancel(ctx)
	inst := &ConnectInstance{
		Name:      name,
		LocalAddr: ln.Addr().String(),
		cancel:    cancel,
		listener:  ln,
		done:      make(chan struct{}),
	}

	go func() {
		defer close(inst.done)
		sess, sessErr := ResolveSession(cfg.ServerURL, "connect")
		if sessErr != nil {
			inst.mu.Lock()
			inst.err = sessErr
			inst.mu.Unlock()
			return
		}

		client := connect.NewClient(sess.URL, sess.Token, cfg.AgentID, cfg.Target)
		if cfg.BatchSize != 0 {
			cfg.BatchSize = ClampBatchSize(cfg.BatchSize)
		}
		client.BatchSize = cfg.BatchSize

		log.Printf("connect[%s]: listening on %s -> server %s (agent=%s, target=%s)",
			name, inst.LocalAddr, sess.URL, cfg.AgentID, cfg.Target)
		runErr := client.ServeListener(childCtx, ln)
		inst.mu.Lock()
		inst.err = runErr
		inst.mu.Unlock()
	}()

	return inst, nil
}
