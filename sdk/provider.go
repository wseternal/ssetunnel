package sdk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// Provider serves a module through the agent's IPC endpoint. The provider
// supplies a manifest and an HTTP handler; the SDK handles IPC registration,
// health reporting, and request routing. Automatic reconnection with
// exponential backoff (500ms → 30s) survives agent restarts.
//
// Architecture:
//   - Control connection: connects to agent for AUTH, REG, HEALTH
//   - Invocation listener: listens on a separate socket for the agent to dial
//     per invocation. Each invocation gets a fresh connection.
type Provider struct {
	manifest Manifest
	handler  http.Handler
	healthCh chan Health
	client   *IPCClient // current IPC client (nil when disconnected)
	clientMu sync.Mutex
}

// Serve registers the module with the agent and serves HTTP requests
// through the IPC connection. The function blocks until the context is
// cancelled or an unrecoverable error occurs.
//
// The IPC endpoint and credential are read from environment variables:
// - SSETUNNEL_MODULE_IPC_ENDPOINT: the IPC socket/pipe path
// - SSETUNNEL_MODULE_IPC_CREDENTIAL: the authentication token
func Serve(ctx context.Context, m Manifest, handler http.Handler) error {
	if err := m.Validate(); err != nil {
		return fmt.Errorf("validate manifest: %w", err)
	}
	if handler == nil {
		return errors.New("handler is required")
	}

	endpoint := os.Getenv("SSETUNNEL_MODULE_IPC_ENDPOINT")
	credential := os.Getenv("SSETUNNEL_MODULE_IPC_CREDENTIAL")
	if endpoint == "" || credential == "" {
		return errors.New("SSETUNNEL_MODULE_IPC_ENDPOINT and SSETUNNEL_MODULE_IPC_CREDENTIAL must be set")
	}

	p := &Provider{
		manifest: m,
		handler:  handler,
		healthCh: make(chan Health, 1),
	}

	return p.serve(ctx, endpoint, credential)
}

// serve connects to the IPC endpoint and serves requests with automatic
// reconnection on IPC failures (e.g., agent restart).
func (p *Provider) serve(ctx context.Context, endpoint, credential string) error {
	invEndpoint := endpoint + "-inv"

	// Remove stale socket.
	os.Remove(invEndpoint)

	invListener, err := net.Listen("unix", invEndpoint)
	if err != nil {
		return fmt.Errorf("listen invocation socket: %w", err)
	}
	defer os.Remove(invEndpoint)
	defer invListener.Close()
	os.Chmod(invEndpoint, 0o600)

	// Serve HTTP requests independently of IPC lifecycle.
	// The agent opens a fresh connection per invocation.
	server := &http.Server{
		Handler:     p.handler,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		server.Close()
	}()
	go func() {
		if err := server.Serve(invListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("provider: http serve: %v", err)
		}
	}()

	// IPC reconnect loop with exponential backoff (500ms → 30s).
	backoff := 500 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := p.connectAndServe(ctx, endpoint, credential)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			log.Printf("provider: connection lost, reconnecting in %v...", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

// connectAndServe performs one IPC session: dial, register, health loop.
// Returns a non-nil error when the IPC connection is lost (caller reconnects).
func (p *Provider) connectAndServe(ctx context.Context, endpoint, credential string) error {
	client, err := DialIPCProvider(ctx, endpoint, credential)
	if err != nil {
		return fmt.Errorf("dial IPC: %w", err)
	}

	p.clientMu.Lock()
	p.client = client
	p.clientMu.Unlock()
	defer func() {
		client.Close()
		p.clientMu.Lock()
		p.client = nil
		p.clientMu.Unlock()
	}()

	invEndpoint := endpoint + "-inv"
	if err := client.RegisterWithEndpoint(p.manifest.ModuleID, p.manifest.ServiceName,
		p.manifest.ServiceVersion, p.manifest.Revision, invEndpoint); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	if len(p.manifest.OpenAPIYAML) > 0 {
		if err := client.SendOpenAPI(p.manifest.OpenAPIYAML); err != nil {
			return fmt.Errorf("send openapi: %w", err)
		}
	}

	log.Printf("provider: registered module %s (invocation endpoint: %s)", p.manifest.ModuleID, invEndpoint)

	if err := client.ReportHealth("healthy", "module started"); err != nil {
		log.Printf("provider: failed to report health: %v", err)
	}

	// Health loop exits on IPC error (signals caller to reconnect).
	return p.healthLoop(ctx, client)
}

// healthLoop reports health to the agent until the connection fails or
// the context is canceled. Returns nil on clean shutdown, non-nil on IPC error.
func (p *Provider) healthLoop(ctx context.Context, client *IPCClient) error {
	interval := p.manifest.HealthInterval
	if interval == 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case h := <-p.healthCh:
			if err := client.ReportHealth(string(h.Status), h.Message); err != nil {
				return fmt.Errorf("report health: %w", err)
			}
		case <-ticker.C:
			if err := client.ReportHealth("healthy", ""); err != nil {
				return fmt.Errorf("report health: %w", err)
			}
		}
	}
}

// ReportHealth sends a health update to the agent. Call this from your
// handler to report degraded or unhealthy status.
func (p *Provider) ReportHealth(h Health) {
	select {
	case p.healthCh <- h:
	default:
		// Channel full; drop update.
	}
}

// IPCClient is a client for module-to-agent IPC communication.
type IPCClient struct {
	conn       net.Conn
	credential string
	endpoint   string
}

// DialIPCProvider connects to the agent's IPC endpoint and authenticates.
func DialIPCProvider(ctx context.Context, endpoint, credential string) (*IPCClient, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	client := &IPCClient{conn: conn, credential: credential, endpoint: endpoint}
	if err := client.authenticate(); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

// authenticate sends the credential to the server for verification.
func (c *IPCClient) authenticate() error {
	cred := []byte(c.credential)
	header := []byte(fmt.Sprintf("AUTH %d\n", len(cred)))
	if _, err := c.conn.Write(header); err != nil {
		return fmt.Errorf("write auth header: %w", err)
	}
	if _, err := c.conn.Write(cred); err != nil {
		return fmt.Errorf("write credential: %w", err)
	}
	buf := make([]byte, 3)
	if _, err := c.conn.Read(buf); err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if string(buf) != "OK\n" {
		return errors.New("authentication failed")
	}
	return nil
}

// Register sends a module registration to the agent.
func (c *IPCClient) Register(moduleID, serviceName, version, revision string) error {
	return c.RegisterWithEndpoint(moduleID, serviceName, version, revision, "")
}

// RegisterWithEndpoint sends a module registration with an invocation endpoint.
func (c *IPCClient) RegisterWithEndpoint(moduleID, serviceName, version, revision, invEndpoint string) error {
	msg := fmt.Sprintf("REG %s %s %s %s %s\n", moduleID, serviceName, version, revision, invEndpoint)
	if _, err := c.conn.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write register: %w", err)
	}
	return nil
}

// SendOpenAPI sends the OpenAPI YAML to the agent after registration.
// Uses length-prefixed binary format (same as AUTH): "OPENAPI <len>\n<bytes>".
func (c *IPCClient) SendOpenAPI(yaml []byte) error {
	header := fmt.Sprintf("OPENAPI %d\n", len(yaml))
	if _, err := c.conn.Write([]byte(header)); err != nil {
		return fmt.Errorf("write openapi header: %w", err)
	}
	if _, err := c.conn.Write(yaml); err != nil {
		return fmt.Errorf("write openapi yaml: %w", err)
	}
	return nil
}

// ReportHealth sends a health update to the agent.
func (c *IPCClient) ReportHealth(status, message string) error {
	msg := fmt.Sprintf("HEALTH %s %s\n", status, message)
	if _, err := c.conn.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write health: %w", err)
	}
	return nil
}

// Close closes the IPC connection.
func (c *IPCClient) Close() error {
	return c.conn.Close()
}
