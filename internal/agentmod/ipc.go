package agentmod

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
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
	mu         sync.RWMutex
	listener   net.Listener
	credential string   // random token for authenticating module processes
	endpoint   string   // IPC endpoint path for dialing new connections
	modules    map[string]*Registration
	closed     bool
	ctx        context.Context
	cancel     context.CancelFunc

	// onRegister is called after a module successfully registers.
	// Used by the agent to auto-install the module into the registry
	// and trigger a catalog snapshot publish.
	onRegister func(reg *Registration)
}

// Registration represents a live module registration from a connected process.
type Registration struct {
	ModuleID        string
	ServiceName     string
	Version         string
	Revision        string
	Health          string
	Conn            net.Conn // the control connection to the module process
	InvEndpoint     string   // invocation socket endpoint for fresh connections per invocation
	OpenAPIYAML     []byte   // OpenAPI spec sent by the module (may be nil)
}

// NewIPCServer creates a new IPC server with a random launch credential.
func NewIPCServer() (*IPCServer, error) {
	cred, err := generateCredential()
	if err != nil {
		return nil, fmt.Errorf("generate credential: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &IPCServer{
		credential: cred,
		modules:    make(map[string]*Registration),
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// Credential returns the launch credential that should be passed to
// module processes (via environment variable or inherited file descriptor).
func (s *IPCServer) Credential() string {
	return s.credential
}

// SetOnRegister sets a callback invoked after a module successfully registers.
// Used by the agent to auto-install the module into the registry.
func (s *IPCServer) SetOnRegister(fn func(reg *Registration)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRegister = fn
}

// SetListener attaches a listener to the IPC server for accepting
// incoming module connections. Starts an accept loop in the background.
func (s *IPCServer) SetListener(ln net.Listener, endpoint string) {
	s.mu.Lock()
	s.listener = ln
	s.endpoint = endpoint
	s.mu.Unlock()
	go s.acceptLoop(ln)
}

// Endpoint returns the IPC endpoint path for dialing new connections.
func (s *IPCServer) Endpoint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endpoint
}

// DialModule opens a fresh IPC connection to a registered module.
// Used by the invoker to get a dedicated connection per invocation.
func (s *IPCServer) DialModule(ctx context.Context) (net.Conn, error) {
	s.mu.RLock()
	endpoint := s.endpoint
	s.mu.RUnlock()
	if endpoint == "" {
		return nil, fmt.Errorf("ipc: no endpoint configured")
	}
	var d net.Dialer
	return d.DialContext(ctx, ipcNetwork, endpoint)
}

// maxIPCMessageSize is the maximum allowed IPC message line length (64 KiB).
// This prevents memory exhaustion from malicious or misbehaving module processes.
const maxIPCMessageSize = 64 << 10

// acceptLoop accepts incoming connections on the listener.
func (s *IPCServer) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.RLock()
			closed := s.closed
			s.mu.RUnlock()
			if closed {
				return
			}
			// Transient error; continue.
			continue
		}
		// Handle each connection in its own goroutine.
		go s.handleConnection(conn)
	}
}

// handleConnection processes a single IPC connection through the full protocol:
// AUTH → OK → REG → HEALTH loop.
func (s *IPCServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Set deadline for authentication phase.
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	reader := bufio.NewReaderSize(conn, maxIPCMessageSize)

	// 1. Read AUTH frame.
	authLine, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("ipc: auth read error: %v", err)
		return
	}
	if len(authLine) > maxIPCMessageSize {
		log.Printf("ipc: AUTH message too long: %d bytes", len(authLine))
		return
	}
	authLine = strings.TrimSuffix(authLine, "\n")
	if !strings.HasPrefix(authLine, "AUTH ") {
		log.Printf("ipc: expected AUTH, got %q", authLine)
		return
	}
	credLen, err := strconv.Atoi(strings.TrimPrefix(authLine, "AUTH "))
	if err != nil || credLen < 0 || credLen > 1024 {
		log.Printf("ipc: invalid credential length: %v", err)
		return
	}

	// Read credential bytes.
	credBuf := make([]byte, credLen)
	if _, err := reader.Read(credBuf); err != nil {
		log.Printf("ipc: credential read error: %v", err)
		return
	}

	// Verify credential.
	if string(credBuf) != s.credential {
		conn.Write([]byte("ERR\n"))
		log.Printf("ipc: invalid credential")
		return
	}

	// Send OK response.
	if _, err := conn.Write([]byte("OK\n")); err != nil {
		log.Printf("ipc: failed to send OK: %v", err)
		return
	}

	// 2. Read REG message.
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	regLine, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("ipc: reg read error: %v", err)
		return
	}
	if len(regLine) > maxIPCMessageSize {
		log.Printf("ipc: REG message too long: %d bytes", len(regLine))
		return
	}
	regLine = strings.TrimSuffix(regLine, "\n")
	if !strings.HasPrefix(regLine, "REG ") {
		log.Printf("ipc: expected REG, got %q", regLine)
		return
	}

	// Parse REG: moduleID serviceName version revision [invEndpoint]
	parts := strings.SplitN(regLine[4:], " ", 5)
	if len(parts) < 4 {
		log.Printf("ipc: invalid REG format: %q", regLine)
		return
	}
	moduleID, serviceName, version, revision := parts[0], parts[1], parts[2], parts[3]
	invEndpoint := ""
	if len(parts) >= 5 {
		invEndpoint = parts[4]
	}

	// Register the module.
	reg := &Registration{
		ModuleID:    moduleID,
		ServiceName: serviceName,
		Version:     version,
		Revision:    revision,
		Health:      "healthy",
		Conn:        conn,
		InvEndpoint: invEndpoint,
	}
	if err := s.Register(reg); err != nil {
		log.Printf("ipc: register failed: %v", err)
		return
	}

	log.Printf("ipc: module %s registered (version=%s, revision=%s)", moduleID, version, revision)

	// 2b. Read optional OPENAPI message (length-prefixed YAML).
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	openapiLine, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("ipc: openapi read error: %v", err)
		s.Deregister(moduleID)
		return
	}
	openapiLine = strings.TrimSuffix(openapiLine, "\n")
	if strings.HasPrefix(openapiLine, "OPENAPI ") {
		yamlLen, parseErr := strconv.Atoi(strings.TrimPrefix(openapiLine, "OPENAPI "))
		if parseErr != nil || yamlLen < 0 || yamlLen > 1<<20 {
			log.Printf("ipc: invalid OPENAPI length: %v", parseErr)
			s.Deregister(moduleID)
			return
		}
		yamlBuf := make([]byte, yamlLen)
		if yamlLen > 0 {
			if _, err := io.ReadFull(reader, yamlBuf); err != nil {
				log.Printf("ipc: openapi yaml read error: %v", err)
				s.Deregister(moduleID)
				return
			}
		}
		s.mu.Lock()
		if r, ok := s.modules[moduleID]; ok {
			r.OpenAPIYAML = yamlBuf
		}
		s.mu.Unlock()
		log.Printf("ipc: module %s sent %d bytes of OpenAPI YAML", moduleID, yamlLen)
	} else {
		// No OPENAPI message — old client. Push back the line for the health loop.
		// We cannot unread from a bufio.Reader, so we handle it inline below.
		// Process this line as the first health message.
		conn.SetDeadline(time.Time{})
		s.handleHealthLine(moduleID, openapiLine)
	}

	// Clear deadline for health loop.
	conn.SetDeadline(time.Time{})

	// Fire registration callback (auto-install into registry).
	s.mu.RLock()
	onReg := s.onRegister
	currentReg := s.modules[moduleID]
	s.mu.RUnlock()
	if onReg != nil && currentReg != nil {
		onReg(currentReg)
	}

	// 3. Health reporting loop.
	for {
		// Set read deadline to detect dead connections (60s).
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		line, err := reader.ReadString('\n')
		if err != nil {
			// Connection closed or timeout — deregister.
			s.Deregister(moduleID)
			log.Printf("ipc: module %s disconnected: %v", moduleID, err)
			return
		}
		// W20 fix: Reject oversized health messages.
		if len(line) > maxIPCMessageSize {
			log.Printf("ipc: message too long from %s: %d bytes", moduleID, len(line))
			continue
		}
		line = strings.TrimSuffix(line, "\n")
		s.handleHealthLine(moduleID, line)
	}
}

// handleHealthLine processes a single health message from a module.
func (s *IPCServer) handleHealthLine(moduleID, line string) {
	if strings.HasPrefix(line, "HEALTH ") {
		healthParts := strings.SplitN(line[7:], " ", 2)
		status := healthParts[0]
		message := ""
		if len(healthParts) > 1 {
			message = healthParts[1]
		}

		s.mu.Lock()
		if r, ok := s.modules[moduleID]; ok {
			r.Health = status
		}
		s.mu.Unlock()

		if status != "healthy" {
			log.Printf("ipc: module %s health=%s: %s", moduleID, status, message)
		}
	} else {
		log.Printf("ipc: unexpected message from %s: %q", moduleID, line)
	}
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modules[moduleID]
}

// Modules returns a snapshot of all current registrations.
func (s *IPCServer) Modules() map[string]*Registration {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.cancel()
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
