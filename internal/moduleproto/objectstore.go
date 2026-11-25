package moduleproto

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/wseternal/ssetunnel/sdk"
)

// Control message types for the module object store. These extend the
// existing control stream protocol (mux.ControlMessage) without changing
// its 64 KiB codec.
const (
	// MsgObjectStoreReq carries one object store request chunk from
	// agent to server. Correlated by request_id.
	MsgObjectStoreReq = "object_store_req"

	// MsgObjectStoreResp carries one object store response chunk from
	// server to agent. Correlated by request_id.
	MsgObjectStoreResp = "object_store_resp"
)

// Object store operations.
const (
	ObjectStorePut = "put"
	ObjectStoreGet = "get"
)

// Structured object store error codes. Messages carry one code so peers
// never parse free-form error text.
const (
	ObjectStoreErrNotFound       = "not_found"
	ObjectStoreErrInvalidRequest = "invalid_request"
	ObjectStoreErrTooLarge       = "too_large"
	ObjectStoreErrBusy           = "busy"
	ObjectStoreErrUnavailable    = "unavailable"
	ObjectStoreErrInternal       = "internal"
)

// ObjectStoreMessage is the JSON payload for both object store control
// message types. Immutable transfer metadata repeats on every chunk so
// the receiver can validate each chunk on its own.
//
// For a put request, the agent sends data chunks. For a get response,
// the server sends data chunks. A terminal error response sets ErrorCode
// and carries no data.
type ObjectStoreMessage struct {
	RequestID  string `json:"request_id"`
	Op         string `json:"op"`
	ModuleID   string `json:"module_id,omitempty"`
	Key        string `json:"key,omitempty"`
	Sequence   int    `json:"sequence"`
	ChunkCount int    `json:"chunk_count"`
	Final      bool   `json:"final"`
	TotalSize  int64  `json:"total_size"`
	SHA256     string `json:"sha256,omitempty"`
	Data       []byte `json:"data,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ObjectStoreChunkCount returns the number of chunks for totalSize. An
// empty object uses one final zero-data chunk.
func ObjectStoreChunkCount(totalSize int) int {
	if totalSize <= 0 {
		return 1
	}
	return (totalSize + ObjectStoreChunkSize - 1) / ObjectStoreChunkSize
}

// ObjectStoreDigest returns the lowercase hex SHA-256 digest of data.
func ObjectStoreDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// NewObjectStoreGet builds one get request message.
func NewObjectStoreGet(requestID, moduleID, key string) *ObjectStoreMessage {
	return &ObjectStoreMessage{
		RequestID:  requestID,
		Op:         ObjectStoreGet,
		ModuleID:   moduleID,
		Key:        key,
		Sequence:   0,
		ChunkCount: 1,
		Final:      true,
	}
}

// NewObjectStoreError builds one terminal error response.
func NewObjectStoreError(requestID, op, code, message string) *ObjectStoreMessage {
	return &ObjectStoreMessage{
		RequestID:  requestID,
		Op:         op,
		Sequence:   0,
		ChunkCount: 1,
		Final:      true,
		ErrorCode:  code,
		Error:      message,
	}
}

// NewObjectStoreDataChunks fragments data into bounded messages. The
// caller must validate requestID, op, moduleID, and key first. The
// fragmenter rejects data above MaxObjectSize.
func NewObjectStoreDataChunks(requestID, op, moduleID, key string, data []byte) ([]*ObjectStoreMessage, error) {
	if len(data) > sdk.MaxObjectSize {
		return nil, fmt.Errorf("object too large: %d bytes (max %d)", len(data), sdk.MaxObjectSize)
	}
	chunkCount := ObjectStoreChunkCount(len(data))
	digest := ObjectStoreDigest(data)
	msgs := make([]*ObjectStoreMessage, 0, chunkCount)
	for seq := 0; seq < chunkCount; seq++ {
		start := seq * ObjectStoreChunkSize
		end := start + ObjectStoreChunkSize
		if end > len(data) {
			end = len(data)
		}
		msgs = append(msgs, &ObjectStoreMessage{
			RequestID:  requestID,
			Op:         op,
			ModuleID:   moduleID,
			Key:        key,
			Sequence:   seq,
			ChunkCount: chunkCount,
			Final:      seq == chunkCount-1,
			TotalSize:  int64(len(data)),
			SHA256:     digest,
			Data:       data[start:end],
		})
	}
	return msgs, nil
}

// ObjectStoreAssembler reassembles one chunked object. The receiver
// creates one assembler from the first chunk and adds the remaining
// chunks in order. The assembler rejects mismatched metadata, gaps,
// duplicate chunks, wrong lengths, trailing data, and a bad digest.
type ObjectStoreAssembler struct {
	requestID  string
	op         string
	moduleID   string
	key        string
	totalSize  int64
	chunkCount int
	sha256     string
	next       int
	valid      bool
	data       []byte
}

// NewObjectStoreAssembler validates the first chunk metadata and
// prepares the bounded object buffer.
func NewObjectStoreAssembler(msg *ObjectStoreMessage) (*ObjectStoreAssembler, error) {
	if err := validateObjectStoreMetadata(msg); err != nil {
		return nil, err
	}
	return &ObjectStoreAssembler{
		requestID:  msg.RequestID,
		op:         msg.Op,
		moduleID:   msg.ModuleID,
		key:        msg.Key,
		totalSize:  msg.TotalSize,
		chunkCount: msg.ChunkCount,
		sha256:     msg.SHA256,
		data:       make([]byte, 0, msg.TotalSize),
	}, nil
}

// validateObjectStoreMetadata checks the immutable fields every chunk
// must share.
func validateObjectStoreMetadata(msg *ObjectStoreMessage) error {
	if msg.RequestID == "" {
		return errors.New("object request_id is empty")
	}
	if msg.Op != ObjectStorePut && msg.Op != ObjectStoreGet {
		return fmt.Errorf("unknown object operation: %q", msg.Op)
	}
	if msg.TotalSize < 0 || msg.TotalSize > sdk.MaxObjectSize {
		return fmt.Errorf("object size out of bounds: %d bytes", msg.TotalSize)
	}
	if msg.ChunkCount != ObjectStoreChunkCount(int(msg.TotalSize)) {
		return fmt.Errorf("object chunk count mismatch: got %d, want %d", msg.ChunkCount, ObjectStoreChunkCount(int(msg.TotalSize)))
	}
	if msg.Sequence < 0 || msg.Sequence >= msg.ChunkCount {
		return fmt.Errorf("object sequence out of bounds: %d", msg.Sequence)
	}
	if len(msg.SHA256) != 64 {
		return errors.New("object digest must be 64 hex characters")
	}
	if _, err := hex.DecodeString(msg.SHA256); err != nil {
		return errors.New("object digest is not hexadecimal")
	}
	if msg.Op == ObjectStorePut {
		if err := sdk.ValidateObjectKey(msg.Key); err != nil {
			return err
		}
	}
	return nil
}

// Add appends one chunk. It returns an error when the chunk does not
// match the first chunk metadata or the expected sequence.
func (a *ObjectStoreAssembler) Add(msg *ObjectStoreMessage) error {
	if a.next >= a.chunkCount {
		return errors.New("object transfer is complete")
	}
	if msg.RequestID != a.requestID || msg.Op != a.op || msg.ModuleID != a.moduleID ||
		msg.Key != a.key || msg.TotalSize != a.totalSize ||
		msg.ChunkCount != a.chunkCount || msg.SHA256 != a.sha256 {
		return errors.New("object chunk metadata mismatch")
	}
	if msg.ErrorCode != "" {
		return fmt.Errorf("object chunk carries error: %s", msg.ErrorCode)
	}
	if msg.Sequence != a.next {
		return fmt.Errorf("object chunk out of order: got %d, want %d", msg.Sequence, a.next)
	}
	final := a.next == a.chunkCount-1
	if msg.Final != final {
		return errors.New("object chunk final flag mismatch")
	}
	wantLen := ObjectStoreChunkSize
	if final {
		wantLen = int(a.totalSize) - ObjectStoreChunkSize*(a.chunkCount-1)
	}
	if len(msg.Data) != wantLen {
		return fmt.Errorf("object chunk length: got %d, want %d", len(msg.Data), wantLen)
	}
	a.data = append(a.data, msg.Data...)
	a.next++
	if a.next == a.chunkCount {
		if ObjectStoreDigest(a.data) != a.sha256 {
			return errors.New("object digest mismatch")
		}
		a.valid = true
	}
	return nil
}

// Complete reports whether every chunk arrived with a valid digest.
func (a *ObjectStoreAssembler) Complete() bool { return a.valid }

// Data returns the reassembled bytes. Call Complete first.
func (a *ObjectStoreAssembler) Data() []byte { return a.data }

// RequestID returns the correlation ID.
func (a *ObjectStoreAssembler) RequestID() string { return a.requestID }

// Op returns the operation.
func (a *ObjectStoreAssembler) Op() string { return a.op }

// ModuleID returns the module namespace.
func (a *ObjectStoreAssembler) ModuleID() string { return a.moduleID }

// Key returns the object key.
func (a *ObjectStoreAssembler) Key() string { return a.key }

// TotalSize returns the declared object size.
func (a *ObjectStoreAssembler) TotalSize() int64 { return a.totalSize }

// ChunkCount returns the declared chunk count.
func (a *ObjectStoreAssembler) ChunkCount() int { return a.chunkCount }

// SHA256 returns the declared digest.
func (a *ObjectStoreAssembler) SHA256() string { return a.sha256 }
