package moduleproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Control message types for module registry operations.
// These extend the existing control stream protocol (mux.ControlMessage).
const (
	// MsgSnapshotStart begins a catalog snapshot transfer.
	// Server→agent direction: never. Agent→server direction only.
	MsgSnapshotStart = "module_snap_start"

	// MsgSnapshotChunk carries a chunk of catalog data (OpenAPI YAML,
	// UI descriptors). Sent between start and commit.
	MsgSnapshotChunk = "module_snap_chunk"

	// MsgSnapshotCommit completes a catalog snapshot. The server validates
	// the assembled snapshot and atomically replaces the previous catalog.
	MsgSnapshotCommit = "module_snap_commit"

	// MsgModuleOpen requests the agent to open a reverse yamux stream
	// for an invocation. Server→agent direction only.
	MsgModuleOpen = "module_open"

	// MsgModuleClose signals cancellation of an active invocation.
	// Bidirectional.
	MsgModuleClose = "module_close"

	// MsgModuleHealth carries a health update for a specific module.
	// Agent→server direction only.
	MsgModuleHealth = "module_health"

	// MsgModuleRemove signals that a module has been deregistered.
	// Agent→server direction only.
	MsgModuleRemove = "module_remove"
)

// Module stream marker: the first line written by the agent when opening
// a reverse stream for a module invocation. Distinguishes module streams
// from TCP, shell, and remote-app data streams.
const ModuleStreamMarker = "__module__\n"

// SnapshotStartPayload is the JSON payload for MsgSnapshotStart.
type SnapshotStartPayload struct {
	// Epoch is the catalog epoch (transport session ID). The server uses
	// this to detect stale snapshots from a previous connection.
	Epoch string `json:"epoch"`

	// ModuleCount is the number of modules in this snapshot.
	ModuleCount int `json:"module_count"`

	// TotalSize is the expected total size of all chunk data in bytes.
	TotalSize int64 `json:"total_size"`
}

// SnapshotChunkPayload is the JSON payload for MsgSnapshotChunk.
// Chunks carry encoded module entries (OpenAPI YAML, metadata, UI descriptors).
type SnapshotChunkPayload struct {
	// Seq is the zero-based chunk sequence number.
	Seq int `json:"seq"`

	// Data is the raw chunk content (encoded module entries).
	Data []byte `json:"data"`
}

// SnapshotCommitPayload is the JSON payload for MsgSnapshotCommit.
type SnapshotCommitPayload struct {
	// Epoch matches the SnapshotStartPayload.Epoch.
	Epoch string `json:"epoch"`

	// ChunkCount is the number of chunks in this snapshot.
	ChunkCount int `json:"chunk_count"`

	// Digest is the SHA-256 hex digest of the concatenated chunk data.
	Digest string `json:"digest"`
}

// ModuleOpenPayload is the JSON payload for MsgModuleOpen.
// The server sends this to request the agent to open a reverse stream.
type ModuleOpenPayload struct {
	// InvocationID is a unique identifier for this invocation.
	// The agent includes it in the module stream marker.
	InvocationID string `json:"invocation_id"`

	// ModuleID identifies the target module.
	ModuleID string `json:"module_id"`

	// Revision is the expected module revision. The agent rejects the
	// open if the current revision does not match.
	Revision string `json:"revision"`

	// Method is the HTTP method (GET, POST, etc.).
	Method string `json:"method"`

	// Path is the request path (e.g. "/api/items").
	Path string `json:"path"`

	// CorrelationID is an opaque string for log correlation.
	CorrelationID string `json:"correlation_id,omitempty"`

	// Deadline is the maximum time for the invocation, as a Unix
	// timestamp in milliseconds. Zero means no deadline.
	Deadline int64 `json:"deadline,omitempty"`
}

// ModuleClosePayload is the JSON payload for MsgModuleClose.
type ModuleClosePayload struct {
	// InvocationID identifies the invocation to cancel.
	InvocationID string `json:"invocation_id"`

	// Reason is an optional human-readable cancellation reason.
	Reason string `json:"reason,omitempty"`
}

// ModuleHealthPayload is the JSON payload for MsgModuleHealth.
type ModuleHealthPayload struct {
	// ModuleID identifies the module.
	ModuleID string `json:"module_id"`

	// Status is the health status: "healthy", "degraded", or "unhealthy".
	Status string `json:"status"`

	// Message is an optional human-readable description.
	Message string `json:"message,omitempty"`

	// CheckedAt is when the health was last verified (Unix ms).
	CheckedAt int64 `json:"checked_at"`
}

// ModuleRemovePayload is the JSON payload for MsgModuleRemove.
type ModuleRemovePayload struct {
	// ModuleID identifies the deregistered module.
	ModuleID string `json:"module_id"`
}

// SnapshotModuleEntry is one module entry in a snapshot chunk.
// Multiple entries are concatenated in the chunk data, separated by
// JSON object boundaries (each entry is a complete JSON object).
type SnapshotModuleEntry struct {
	// ModuleID is the reverse-DNS module identifier.
	ModuleID string `json:"module_id"`

	// ServiceName is the human-readable service name.
	ServiceName string `json:"service_name"`

	// ServiceVersion is the semantic version string.
	ServiceVersion string `json:"service_version"`

	// Revision is the module revision.
	Revision string `json:"revision,omitempty"`

	// OpenAPIYAML is the OpenAPI v3 YAML document.
	OpenAPIYAML []byte `json:"openapi_yaml"`

	// Health is the module's current health.
	Health HealthEntry `json:"health"`

	// UI lists optional UI descriptors.
	UI []UIEntry `json:"ui,omitempty"`
}

// HealthEntry is the health state in a snapshot entry.
type HealthEntry struct {
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	CheckedAt int64  `json:"checked_at"`
}

// UIEntry is a UI descriptor in a snapshot entry.
type UIEntry struct {
	Kind          string      `json:"kind"`
	RemoteName    string      `json:"remote_name"`
	PageURL       string      `json:"page_url"`
	PackageDigest string      `json:"package_digest,omitempty"`
	AssetPaths    []AssetPath `json:"asset_paths,omitempty"`
}

// AssetPath is a UI asset entry in a snapshot UI descriptor.
type AssetPath struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// ErrorCode identifies a module protocol error.
type ErrorCode string

const (
	// ErrModuleNotFound indicates the requested module is not registered.
	ErrModuleNotFound ErrorCode = "module_not_found"

	// ErrStaleRevision indicates the requested revision does not match
	// the current module revision.
	ErrStaleRevision ErrorCode = "stale_revision"

	// ErrStaleEpoch indicates the catalog epoch is from a previous session.
	ErrStaleEpoch ErrorCode = "stale_epoch"

	// ErrInvocationLimit indicates the agent has reached the maximum
	// number of active invocations.
	ErrInvocationLimit ErrorCode = "invocation_limit"

	// ErrEstablishmentTimeout indicates the agent did not open the
	// reverse stream within the deadline.
	ErrEstablishmentTimeout ErrorCode = "establishment_timeout"

	// ErrModuleUnhealthy indicates the module is in an unhealthy state.
	ErrModuleUnhealthy ErrorCode = "module_unhealthy"

	// ErrUnauthorized indicates the consumer lacks permission to invoke
	// the module.
	ErrUnauthorized ErrorCode = "unauthorized"

	// ErrInvalidRequest indicates the request did not match the OpenAPI
	// contract (wrong method, path, headers, etc.).
	ErrInvalidRequest ErrorCode = "invalid_request"

	// ErrInternal indicates an internal server or agent error.
	ErrInternal ErrorCode = "internal_error"
)

// Error represents a module protocol error with a structured code.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// MarshalSnapshotEntries encodes a slice of module entries as concatenated
// JSON objects for inclusion in a snapshot chunk.
func MarshalSnapshotEntries(entries []SnapshotModuleEntry) ([]byte, error) {
	var buf []byte
	for i, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("marshal entry[%d]: %w", i, err)
		}
		buf = append(buf, data...)
	}
	return buf, nil
}

// UnmarshalSnapshotEntries decodes concatenated JSON objects from a snapshot
// chunk into module entries.
func UnmarshalSnapshotEntries(data []byte) ([]SnapshotModuleEntry, error) {
	var entries []SnapshotModuleEntry
	dec := json.NewDecoder(jsonReader(data))
	for dec.More() {
		var e SnapshotModuleEntry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("decode entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func jsonReader(data []byte) *bytes.Reader {
	return bytes.NewReader(data)
}

// NewHealthEntry creates a HealthEntry from a status and timestamp.
func NewHealthEntry(status, message string, checkedAt time.Time) HealthEntry {
	return HealthEntry{
		Status:    status,
		Message:   message,
		CheckedAt: checkedAt.UnixMilli(),
	}
}
