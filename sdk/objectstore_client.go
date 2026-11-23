package sdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

// ObjectStore IPC limits. One frame holds the base64 form of a 4 MiB
// object plus bounded metadata.
const (
	objectStoreOperationTimeout = 2 * time.Minute
	maxObjectStoreMessageSize   = 6 << 20
)

// objectStoreRequest is the wire format for an ObjectStore IPC request.
type objectStoreRequest struct {
	RequestID string `json:"request_id,omitempty"`
	Op        string `json:"op"`
	ModuleID  string `json:"module_id"`
	Key       string `json:"key"`
	Data      []byte `json:"data,omitempty"`
}

// objectStoreResponse is the wire format for an ObjectStore IPC response.
type objectStoreResponse struct {
	RequestID string `json:"request_id"`
	Op        string `json:"op"`
	Data      []byte `json:"data,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

// objectStoreClient is the SDK side of the ObjectStore IPC path. Each
// method opens one authenticated IPC connection, performs one operation,
// and closes the connection.
type objectStoreClient struct {
	endpoint   string
	credential string
	moduleID   string
}

// NewObjectStore creates an object store client. It reads the endpoint,
// credential, and module ID from the SSETUNNEL_MODULE_IPC_ENDPOINT,
// SSETUNNEL_MODULE_IPC_CREDENTIAL, and SSETUNNEL_MODULE_ID environment
// variables. The constructor validates configuration but performs no
// network I/O.
func NewObjectStore() (ObjectStore, error) {
	endpoint := os.Getenv("SSETUNNEL_MODULE_IPC_ENDPOINT")
	credential := os.Getenv("SSETUNNEL_MODULE_IPC_CREDENTIAL")
	moduleID := os.Getenv("SSETUNNEL_MODULE_ID")
	if endpoint == "" || credential == "" || moduleID == "" {
		return nil, errors.New("SSETUNNEL_MODULE_IPC_ENDPOINT, SSETUNNEL_MODULE_IPC_CREDENTIAL, and SSETUNNEL_MODULE_ID must be set")
	}
	return NewObjectStoreWithEndpoint(endpoint, credential, moduleID)
}

// NewObjectStoreWithEndpoint creates an object store client with
// explicit configuration. The client always presents this module ID;
// the agent replaces any supplied module ID with the authenticated
// identity.
func NewObjectStoreWithEndpoint(endpoint, credential, moduleID string) (ObjectStore, error) {
	if endpoint == "" {
		return nil, errors.New("object store endpoint is empty")
	}
	if credential == "" {
		return nil, errors.New("object store credential is empty")
	}
	if len(moduleID) > MaxModuleIDLen {
		return nil, fmt.Errorf("module ID too long: %d characters (max %d)", len(moduleID), MaxModuleIDLen)
	}
	if !ModuleIDRegexp.MatchString(moduleID) {
		return nil, errors.New("module ID must be a reverse-DNS name")
	}
	return &objectStoreClient{
		endpoint:   endpoint,
		credential: credential,
		moduleID:   moduleID,
	}, nil
}

// Put stores data under key. The bytes are sent in one IPC frame, and
// the agent fragments them for the control tunnel.
func (c *objectStoreClient) Put(ctx context.Context, moduleID, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateObjectKey(key); err != nil {
		return err
	}
	if len(data) > MaxObjectSize {
		return fmt.Errorf("object too large: %d bytes (max %d)", len(data), MaxObjectSize)
	}
	_, err := c.do(ctx, objectStoreRequest{
		Op:       "put",
		ModuleID: c.moduleID,
		Key:      key,
		Data:     data,
	})
	return err
}

// Get returns the object stored under key. A missing object returns an
// error that satisfies errors.Is(err, ErrObjectNotFound).
func (c *objectStoreClient) Get(ctx context.Context, moduleID, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateObjectKey(key); err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, objectStoreRequest{
		Op:       "get",
		ModuleID: c.moduleID,
		Key:      key,
	})
	if err != nil {
		return nil, err
	}
	if resp.Data == nil {
		return []byte{}, nil
	}
	return resp.Data, nil
}

// do performs one request on a fresh authenticated IPC connection.
func (c *objectStoreClient) do(ctx context.Context, req objectStoreRequest) (*objectStoreResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("object store: marshal request: %w", err)
	}
	if len(payload) > maxObjectStoreMessageSize {
		return nil, fmt.Errorf("object store: request too large: %d bytes", len(payload))
	}

	conn, reader, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(objectStoreOperationTimeout))
	}

	header := fmt.Sprintf("OBJECTSTORE %d\n", len(payload))
	if _, err := conn.Write([]byte(header)); err != nil {
		return nil, fmt.Errorf("object store: write request header: %w", err)
	}
	if _, err := conn.Write(payload); err != nil {
		return nil, fmt.Errorf("object store: write request body: %w", err)
	}

	respLen, err := readObjectStoreHeader(reader, "OBJECTSTORE_RESP ")
	if err != nil {
		return nil, err
	}
	respBuf := make([]byte, respLen)
	if respLen > 0 {
		if _, err := io.ReadFull(reader, respBuf); err != nil {
			return nil, fmt.Errorf("object store: read response body: %w", err)
		}
	}

	var resp objectStoreResponse
	if err := json.Unmarshal(respBuf, &resp); err != nil {
		return nil, fmt.Errorf("object store: unmarshal response: %w", err)
	}
	if resp.ErrorCode != "" {
		return nil, objectStoreResponseError(resp.ErrorCode, resp.Error)
	}
	return &resp, nil
}

// connect dials and authenticates one ObjectStore IPC connection. The
// handshake is bounded by the caller deadline or the handshake timeout.
func (c *objectStoreClient) connect(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, ipcNetwork, c.endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("object store: dial: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	}

	fail := func(format string, args ...any) (net.Conn, *bufio.Reader, error) {
		conn.Close()
		return nil, nil, fmt.Errorf(format, args...)
	}

	cred := []byte(c.credential)
	if _, err := conn.Write([]byte(fmt.Sprintf("AUTH %d\n", len(cred)))); err != nil {
		return fail("object store: write auth header: %w", err)
	}
	if _, err := conn.Write(cred); err != nil {
		return fail("object store: write credential: %w", err)
	}
	buf := make([]byte, 3)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fail("object store: read auth response: %w", err)
	}
	if string(buf) != "OK\n" {
		return fail("object store: authentication failed")
	}

	if _, err := conn.Write([]byte("OBJECTSTORE " + c.moduleID + "\n")); err != nil {
		return fail("object store: write identity: %w", err)
	}

	reader := bufio.NewReaderSize(conn, 64<<10)
	_ = conn.SetDeadline(time.Time{})
	return conn, reader, nil
}

// readObjectStoreHeader reads one bounded length header line.
func readObjectStoreHeader(reader *bufio.Reader, prefix string) (int, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return 0, errors.New("object store: response header too long")
	}
	if err != nil {
		return 0, fmt.Errorf("object store: read response header: %w", err)
	}
	line = line[:len(line)-1]
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return 0, fmt.Errorf("object store: unexpected response header %q", line)
	}
	n, err := strconv.Atoi(string(line[len(prefix):]))
	if err != nil || n < 0 || n > maxObjectStoreMessageSize {
		return 0, fmt.Errorf("object store: invalid response length %q", line[len(prefix):])
	}
	return n, nil
}

// objectStoreResponseError maps a structured error code to a Go error.
func objectStoreResponseError(code, message string) error {
	switch code {
	case "not_found":
		return fmt.Errorf("object store: %w", ErrObjectNotFound)
	case "too_large":
		return errors.New("object store: object too large")
	case "invalid_request":
		return errors.New("object store: invalid request")
	case "busy":
		return errors.New("object store: store is busy")
	case "unavailable":
		return errors.New("object store: store is unavailable")
	}
	if message != "" {
		return fmt.Errorf("object store: %s", message)
	}
	return errors.New("object store: request failed")
}
