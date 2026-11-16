package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// DataStore provides CRUD operations for module records stored in the
// server's database through the control tunnel. Modules call these
// methods to persist structured data without direct database access.
//
// Transport: connects to the agent's IPC endpoint and uses the
// DATASTORE command to forward requests through the agent → server path.
// Each DataStore opens its own dedicated connection to the agent.
type DataStore struct {
	endpoint   string
	credential string
	conn       net.Conn
	mu         sync.Mutex
	closed     bool
}

// NewDataStore creates a data store client. Reads endpoint and credential
// from environment variables SSETUNNEL_MODULE_IPC_ENDPOINT and
// SSETUNNEL_MODULE_IPC_CREDENTIAL.
func NewDataStore() (*DataStore, error) {
	endpoint := os.Getenv("SSETUNNEL_MODULE_IPC_ENDPOINT")
	credential := os.Getenv("SSETUNNEL_MODULE_IPC_CREDENTIAL")
	if endpoint == "" || credential == "" {
		return nil, errors.New("SSETUNNEL_MODULE_IPC_ENDPOINT and SSETUNNEL_MODULE_IPC_CREDENTIAL must be set")
	}
	return NewDataStoreWithEndpoint(endpoint, credential)
}

// NewDataStoreWithEndpoint creates a data store client with explicit
// endpoint and credential.
func NewDataStoreWithEndpoint(endpoint, credential string) (*DataStore, error) {
	ds := &DataStore{
		endpoint:   endpoint,
		credential: credential,
	}
	if err := ds.connect(); err != nil {
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

// Close releases the data store connection.
func (ds *DataStore) Close() error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.closed = true
	if ds.conn != nil {
		return ds.conn.Close()
	}
	return nil
}

func (ds *DataStore) connect() error {
	var d net.Dialer
	conn, err := d.DialContext(context.Background(), ipcNetwork, ds.endpoint)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
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
	if _, err := conn.Read(buf); err != nil {
		conn.Close()
		return fmt.Errorf("read auth response: %w", err)
	}
	if string(buf) != "OK\n" {
		conn.Close()
		return errors.New("authentication failed")
	}

	ds.conn = conn
	return nil
}

func (ds *DataStore) sendRequest(ctx context.Context, req dataStoreRequest) (*dataStoreResponse, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	if ds.closed || ds.conn == nil {
		return nil, errors.New("data store: connection closed")
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
		return nil, fmt.Errorf("write command: %w", err)
	}
	if _, err := ds.conn.Write(data); err != nil {
		return nil, fmt.Errorf("write payload: %w", err)
	}

	// Read response: "DATASTORE_RESP <len>\n<json>" or raw JSON.
	respBuf := make([]byte, 64*1024)
	n, err := ds.conn.Read(respBuf)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Clear deadline.
	ds.conn.SetDeadline(time.Time{})

	// Parse response. The agent sends the response as raw JSON.
	var resp dataStoreResponse
	if err := json.Unmarshal(respBuf[:n], &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if resp.Error != "" {
		return &resp, fmt.Errorf("server error: %s", resp.Error)
	}
	return &resp, nil
}

// Reconnect re-establishes the IPC connection.
func (ds *DataStore) Reconnect() error {
	ds.mu.Lock()
	if ds.conn != nil {
		ds.conn.Close()
		ds.conn = nil
	}
	ds.mu.Unlock()
	return ds.connect()
}
