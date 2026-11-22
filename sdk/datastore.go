package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// handshakeTimeout bounds the dial-and-authenticate exchange when the caller's
// context carries no deadline.
const handshakeTimeout = 30 * time.Second

// DataStore provides CRUD operations for module records stored in the
// server's database through the control tunnel. Modules call these
// methods to persist structured data without direct database access.
//
// Transport: connects to the agent's IPC endpoint and uses the
// DATASTORE command to forward requests through the agent → server path.
// Each DataStore opens its own dedicated connection to the agent.
//
// Recovery: a wire, framing or decode error drops the connection and the
// next call redials it transparently, so module authors never manage
// connection state themselves.
type DataStore struct {
	endpoint   string
	credential string
	moduleID   string
	conn       net.Conn
	reader     *bufio.Reader
	mu         sync.Mutex
	closed     bool
}

// NewDataStore creates a data store client. Reads endpoint and credential
// from environment variables SSETUNNEL_MODULE_IPC_ENDPOINT and
// SSETUNNEL_MODULE_IPC_CREDENTIAL.
func NewDataStore() (*DataStore, error) {
	endpoint := os.Getenv("SSETUNNEL_MODULE_IPC_ENDPOINT")
	credential := os.Getenv("SSETUNNEL_MODULE_IPC_CREDENTIAL")
	moduleID := os.Getenv("SSETUNNEL_MODULE_ID")
	if endpoint == "" || credential == "" || moduleID == "" {
		return nil, errors.New("SSETUNNEL_MODULE_IPC_ENDPOINT, SSETUNNEL_MODULE_IPC_CREDENTIAL, and SSETUNNEL_MODULE_ID must be set")
	}
	return NewDataStoreWithEndpoint(endpoint, credential, moduleID)
}

// NewDataStoreWithEndpoint creates a data store client with explicit
// endpoint and credential.
func NewDataStoreWithEndpoint(endpoint, credential, moduleID string) (*DataStore, error) {
	ds := &DataStore{
		endpoint:   endpoint,
		credential: credential,
		moduleID:   moduleID,
	}
	if err := ds.connectContext(context.Background()); err != nil {
		return nil, err
	}
	return ds, nil
}

// Record represents a stored record returned from Get or List.
type Record struct {
	ModuleID  string          `json:"module_id"`
	RecordID  string          `json:"record_id"`
	Data      json.RawMessage `json:"data"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

// ListOptions configures pagination for List operations.
type ListOptions struct {
	Limit  int
	Offset int
}

// dataStoreRequest is the wire format for a data store request.
type dataStoreRequest struct {
	RequestID string          `json:"request_id"`
	Op        string          `json:"op"`
	ModuleID  string          `json:"module_id"`
	RecordID  string          `json:"record_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Filter    *dsFilter       `json:"filter,omitempty"`
}

type dsFilter struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// dataStoreResponse is the wire format for a data store response.
type dataStoreResponse struct {
	RequestID string   `json:"request_id"`
	Error     string   `json:"error,omitempty"`
	Record    *Record  `json:"record,omitempty"`
	Records   []Record `json:"records,omitempty"`
	Deleted   int64    `json:"deleted,omitempty"`
}

// Create stores a record in the server database. Overwrites if the
// record already exists (upsert semantics).
func (ds *DataStore) Create(ctx context.Context, moduleID, recordID string, data json.RawMessage) error {
	req := dataStoreRequest{
		Op:       "create",
		ModuleID: moduleID,
		RecordID: recordID,
		Data:     data,
	}
	_, err := ds.sendRequest(ctx, req)
	return err
}

// Get retrieves a single record by module ID and record ID.
func (ds *DataStore) Get(ctx context.Context, moduleID, recordID string) (*Record, error) {
	req := dataStoreRequest{
		Op:       "get",
		ModuleID: moduleID,
		RecordID: recordID,
	}
	resp, err := ds.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Record == nil {
		return nil, errors.New("record not found")
	}
	return resp.Record, nil
}

// List retrieves records for a module with optional pagination.
func (ds *DataStore) List(ctx context.Context, moduleID string, opts *ListOptions) ([]Record, error) {
	req := dataStoreRequest{
		Op:       "list",
		ModuleID: moduleID,
	}
	if opts != nil {
		req.Filter = &dsFilter{
			Limit:  opts.Limit,
			Offset: opts.Offset,
		}
	}
	resp, err := ds.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Records, nil
}

// Delete removes a record by module ID and record ID.
func (ds *DataStore) Delete(ctx context.Context, moduleID, recordID string) error {
	req := dataStoreRequest{
		Op:       "delete",
		ModuleID: moduleID,
		RecordID: recordID,
	}
	_, err := ds.sendRequest(ctx, req)
	return err
}

// errDataStoreClosed is returned by a DataStore whose Close has been called.
var errDataStoreClosed = errors.New("data store: connection closed")

// Close releases the data store connection.
func (ds *DataStore) Close() error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.closed = true
	var err error
	if ds.conn != nil {
		err = ds.conn.Close()
		ds.conn = nil
	}
	ds.reader = nil
	return err
}

// connectContext dials and authenticates a fresh IPC connection. The caller
// owns the connection state: sendRequest holds ds.mu, and the constructor calls
// it before the struct is shared. On success it swaps in the new connection and
// reader.
func (ds *DataStore) connectContext(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, ipcNetwork, ds.endpoint)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	// The dial honours ctx, but the AUTH exchange below is blocking I/O, so
	// bound it here: a stalled agent must not hold the caller past its own
	// deadline while ds.mu is held. sendRequest sets the request deadline.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	}

	// Authenticate using the same protocol as Provider.
	cred := []byte(ds.credential)
	header := fmt.Sprintf("AUTH %d\n", len(cred))
	if _, err := conn.Write([]byte(header)); err != nil {
		conn.Close()
		return fmt.Errorf("write auth header: %w", err)
	}
	if _, err := conn.Write(cred); err != nil {
		conn.Close()
		return fmt.Errorf("write credential: %w", err)
	}
	buf := make([]byte, 3)
	if _, err := io.ReadFull(conn, buf); err != nil {
		conn.Close()
		return fmt.Errorf("read auth response: %w", err)
	}
	if string(buf) != "OK\n" {
		conn.Close()
		return errors.New("authentication failed")
	}

	// Identify this connection as belonging to a specific module.
	// Format: DATASTORE <module_id>\n
	identLine := fmt.Sprintf("DATASTORE %s\n", ds.moduleID)
	if _, err := conn.Write([]byte(identLine)); err != nil {
		conn.Close()
		return fmt.Errorf("write datastore identity: %w", err)
	}

	ds.conn = conn
	ds.reader = bufio.NewReaderSize(conn, 64<<10)
	_ = conn.SetDeadline(time.Time{})
	return nil
}

func (ds *DataStore) sendRequest(ctx context.Context, req dataStoreRequest) (*dataStoreResponse, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	if ds.closed {
		return nil, errDataStoreClosed
	}
	// A previous request poisoned the connection; redial before sending.
	// Reconnecting under the lock keeps a concurrent Close() serialized.
	if ds.conn == nil {
		if err := ds.connectContext(ctx); err != nil {
			return nil, fmt.Errorf("data store: reconnect: %w", err)
		}
		log.Printf("data store: reconnected to %s", ds.endpoint)
	}

	// Set deadline from context or default 30s.
	if deadline, ok := ctx.Deadline(); ok {
		ds.conn.SetDeadline(deadline)
	} else {
		ds.conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	// Marshal request.
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Send: "DATASTORE <len>\n<json>"
	cmd := fmt.Sprintf("DATASTORE %d\n", len(data))
	if _, err := ds.conn.Write([]byte(cmd)); err != nil {
		ds.resetConnLocked()
		return nil, fmt.Errorf("write command: %w", err)
	}
	if _, err := ds.conn.Write(data); err != nil {
		ds.resetConnLocked()
		return nil, fmt.Errorf("write payload: %w", err)
	}

	// Read response: "DATASTORE_RESP <len>\n<json>" (length-prefixed framing).
	respLine, err := ds.reader.ReadString('\n')
	if err != nil {
		ds.resetConnLocked()
		return nil, fmt.Errorf("read response header: %w", err)
	}
	respLine = respLine[:len(respLine)-1] // trim \n
	if len(respLine) < 15 || respLine[:15] != "DATASTORE_RESP " {
		ds.resetConnLocked()
		return nil, fmt.Errorf("unexpected response header: %q", respLine)
	}
	respLen, err := strconv.Atoi(respLine[15:])
	if err != nil || respLen < 0 || respLen > 16<<20 {
		ds.resetConnLocked()
		return nil, fmt.Errorf("invalid response length: %q", respLine[15:])
	}
	respBuf := make([]byte, respLen)
	if respLen > 0 {
		if _, err := io.ReadFull(ds.reader, respBuf); err != nil {
			ds.resetConnLocked()
			return nil, fmt.Errorf("read response body: %w", err)
		}
	}

	// Clear deadline: the frame was consumed in full, so the stream is clean.
	ds.conn.SetDeadline(time.Time{})

	// Parse response.
	var resp dataStoreResponse
	if err := json.Unmarshal(respBuf, &resp); err != nil {
		ds.resetConnLocked()
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	// Application-level errors keep the stream healthy, so the connection
	// stays usable and the caller sees the server's message.
	if resp.Error != "" {
		return &resp, fmt.Errorf("server error: %s", resp.Error)
	}
	return &resp, nil
}

// resetConnLocked drops the connection and its buffered reader so the next
// request starts from a fresh, synchronized stream. Callers hold ds.mu.
func (ds *DataStore) resetConnLocked() {
	if ds.conn != nil {
		_ = ds.conn.Close()
		ds.conn = nil
	}
	ds.reader = nil
}
