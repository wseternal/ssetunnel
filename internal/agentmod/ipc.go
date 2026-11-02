package agentmod

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
)

// IPC errors.
var (
	ErrIPCNotAuthenticated = errors.New("ipc: peer not authenticated")
	ErrIPCInvalidCredential = errors.New("ipc: invalid launch credential")
	ErrIPCModuleNotFound   = errors.New("ipc: module not found")
	ErrIPCClosed           = errors.New("ipc: server closed")
)

// IPCServer manages the local IPC endpoint for module registration and
// control. Module processes connect to this server to register their
// services, report health, and receive commands.
type IPCServer struct {
	mu         sync.Mutex
	listener   net.Listener
	credential string   // random token for authenticating module processes
	modules    map[string]*Registration
	closed     bool
}

// Registration represents a live module registration from a connected process.
type Registration struct {
	ModuleID    string
	ServiceName string
	Version     string
	Revision    string
	Health      string
	Conn        net.Conn // the IPC connection to the module process
}

// NewIPCServer creates a new IPC server with a random launch credential.
func NewIPCServer() (*IPCServer, error) {
	cred, err := generateCredential()
	if err != nil {
		return nil, fmt.Errorf("generate credential: %w", err)
	}
	return &IPCServer{
		credential: cred,
		modules:    make(map[string]*Registration),
	}, nil
}

// Credential returns the launch credential that should be passed to
// module processes (via environment variable or inherited file descriptor).
func (s *IPCServer) Credential() string {
	return s.credential
}

// generateCredential creates a 32-byte random hex token.
func generateCredential() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// Register adds a module registration. The connection is used for health
// updates and deregistration signaling.
func (s *IPCServer) Register(reg *Registration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrIPCClosed
	}
	if _, exists := s.modules[reg.ModuleID]; exists {
		return fmt.Errorf("module %q already registered", reg.ModuleID)
	}
	s.modules[reg.ModuleID] = reg
	return nil
}

// Deregister removes a module registration and closes its connection.
func (s *IPCServer) Deregister(moduleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reg, ok := s.modules[moduleID]; ok {
		if reg.Conn != nil {
			reg.Conn.Close()
		}
		delete(s.modules, moduleID)
	}
}

// Lookup returns the registration for a module, or nil if not found.
func (s *IPCServer) Lookup(moduleID string) *Registration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modules[moduleID]
}

// Modules returns a snapshot of all current registrations.
func (s *IPCServer) Modules() map[string]*Registration {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]*Registration, len(s.modules))
	for k, v := range s.modules {
		result[k] = v
	}
	return result
}

// Close shuts down the IPC server, closes all module connections, and
// stops accepting new registrations.
func (s *IPCServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for id, reg := range s.modules {
		if reg.Conn != nil {
			reg.Conn.Close()
		}
		delete(s.modules, id)
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// IPCClient is a client for registering a module with the IPC server.
// Module processes use this to connect to the agent's IPC endpoint.
type IPCClient struct {
	conn       net.Conn
	credential string
}

// DialIPC connects to the agent's IPC endpoint and authenticates with
// the given credential.
func DialIPC(ctx context.Context, endpoint, credential string) (*IPCClient, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, ipcNetwork, endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial ipc: %w", err)
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
	// Simple protocol: send credential as a length-prefixed string.
	cred := []byte(c.credential)
	header := []byte(fmt.Sprintf("AUTH %d\n", len(cred)))
	if _, err := c.conn.Write(header); err != nil {
		return fmt.Errorf("write auth header: %w", err)
	}
	if _, err := c.conn.Write(cred); err != nil {
		return fmt.Errorf("write credential: %w", err)
	}
	// Read response.
	buf := make([]byte, 3)
	if _, err := c.conn.Read(buf); err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if string(buf) != "OK\n" {
		return ErrIPCInvalidCredential
	}
	return nil
}

// Register sends a registration request to the server.
func (c *IPCClient) Register(moduleID, serviceName, version, revision string) error {
	msg := fmt.Sprintf("REG %s %s %s %s\n", moduleID, serviceName, version, revision)
	if _, err := c.conn.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write register: %w", err)
	}
	return nil
}

// ReportHealth sends a health update to the server.
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
