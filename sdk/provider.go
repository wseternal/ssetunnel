package sdk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// Provider serves a module through the agent's IPC endpoint. The provider
// supplies a manifest and an HTTP handler; the SDK handles IPC registration,
// health reporting, and request routing.
//
// Example usage:
//
//	m := sdk.Manifest{
//	    ModuleID:       "com.example.mymodule",
//	    ServiceName:    "my-service",
//	    ServiceVersion: "1.0.0",
//	    OpenAPIYAML:    openapiBytes,
//	}
//	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//	    w.Write([]byte("Hello from module!"))
//	})
//	err := sdk.Serve(ctx, m, handler)
type Provider struct {
	manifest Manifest
	handler  http.Handler
	client   *IPCClient
	healthCh chan Health
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

// serve connects to the IPC endpoint and serves requests.
func (p *Provider) serve(ctx context.Context, endpoint, credential string) error {
	client, err := DialIPCProvider(ctx, endpoint, credential)
	if err != nil {
		return fmt.Errorf("dial IPC: %w", err)
	}
	p.client = client
	defer client.Close()

	// Register with the agent.
	if err := client.Register(p.manifest.ModuleID, p.manifest.ServiceName, p.manifest.ServiceVersion, p.manifest.Revision); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	log.Printf("provider: registered module %s", p.manifest.ModuleID)

	// Report initial health.
	if err := client.ReportHealth("healthy", "module started"); err != nil {
		log.Printf("provider: failed to report health: %v", err)
	}

	// Start health reporting goroutine.
	go p.healthLoop(ctx)

	// Serve HTTP requests from the IPC connection.
	// The agent forwards invocation requests as HTTP over the IPC socket.
	return p.serveHTTP(ctx)
}

// healthLoop periodically reports health to the agent.
func (p *Provider) healthLoop(ctx context.Context) {
	interval := p.manifest.HealthInterval
	if interval == 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case h := <-p.healthCh:
			if err := p.client.ReportHealth(string(h.Status), h.Message); err != nil {
				log.Printf("provider: failed to report health: %v", err)
			}
		case <-ticker.C:
			// Periodic health ping (healthy by default).
			if err := p.client.ReportHealth("healthy", ""); err != nil {
				log.Printf("provider: failed to report health: %v", err)
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

// serveHTTP handles HTTP requests from the IPC connection.
// The agent writes requests as HTTP/1.1 and reads responses.
func (p *Provider) serveHTTP(ctx context.Context) error {
	// Create an HTTP server that serves on the IPC connection.
	// The agent opens a new connection for each invocation.
	server := &http.Server{
		Handler:     p.handler,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	// Create a listener from the IPC connection.
	// For simplicity, we serve on the existing connection directly.
	// In a production implementation, this would use a more sophisticated
	// protocol for multiplexing multiple invocations.
	lis := &singleConnListener{conn: p.client.conn}
	return server.Serve(lis)
}

// singleConnListener is a net.Listener that returns a single connection once.
type singleConnListener struct {
	conn net.Conn
	used bool
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if l.used {
		return nil, errors.New("listener closed")
	}
	l.used = true
	return l.conn, nil
}

func (l *singleConnListener) Close() error {
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

// IPCClient is a client for module-to-agent IPC communication.
type IPCClient struct {
	conn       net.Conn
	credential string
}

// DialIPCProvider connects to the agent's IPC endpoint and authenticates.
func DialIPCProvider(ctx context.Context, endpoint, credential string) (*IPCClient, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	client := &IPCClient{conn: conn, credential: credential}
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
	msg := fmt.Sprintf("REG %s %s %s %s\n", moduleID, serviceName, version, revision)
	if _, err := c.conn.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write register: %w", err)
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
