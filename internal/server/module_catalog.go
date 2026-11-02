package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/wseternal/ssetunnel/internal/agentmod"
	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// ModuleCatalog manages the server-side catalog for a single agent session.
// It reassembles snapshot chunks from the agent and validates OpenAPI documents
// before making them available for discovery and invocation.
type ModuleCatalog struct {
	mu sync.RWMutex

	// Session ID (used as catalog epoch).
	epoch string

	// Current committed catalog.
	committed *CommittedCatalog

	// In-progress snapshot reassembly.
	reassembly *SnapshotReassembly

	// OpenAPI validation queue.
	validationQueue chan *ValidationRequest
	validationWg    sync.WaitGroup
}

// CommittedCatalog is a validated catalog ready for discovery and invocation.
type CommittedCatalog struct {
	Epoch      string
	Modules    map[string]*ModuleInfo // moduleID → info
	Revision   int
	PublishedAt time.Time
}

// ModuleInfo holds information about a registered module.
type ModuleInfo struct {
	ModuleID       string
	ServiceName    string
	ServiceVersion string
	Revision       string
	OpenAPIYAML    []byte
	ValidatedOpenAPI *agentmod.ValidatedOpenAPI
	Health         moduleproto.HealthEntry
	UI             []moduleproto.UIEntry
}

// SnapshotReassembly tracks an in-progress snapshot transfer.
type SnapshotReassembly struct {
	Epoch       string
	ModuleCount int
	TotalSize   int64
	Chunks      [][]byte
	ReceivedSize int64
	StartedAt   time.Time
}

// ValidationRequest is a request to validate an OpenAPI document.
type ValidationRequest struct {
	Entry   moduleproto.SnapshotModuleEntry
	Result  chan *ValidationResult
}

// ValidationResult is the result of OpenAPI validation.
type ValidationResult struct {
	ModuleID string
	Valid    bool
	Error    error
	Doc      *agentmod.ValidatedOpenAPI
}

// NewModuleCatalog creates a new catalog for the given session.
func NewModuleCatalog(epoch string) *ModuleCatalog {
	mc := &ModuleCatalog{
		epoch:           epoch,
		validationQueue: make(chan *ValidationRequest, moduleproto.OpenAPIValidationQueue),
	}

	// Start validation workers.
	for i := 0; i < moduleproto.OpenAPIValidationWorkers; i++ {
		mc.validationWg.Add(1)
		go mc.validationWorker()
	}

	return mc
}

// validationWorker processes OpenAPI validation requests.
func (mc *ModuleCatalog) validationWorker() {
	defer mc.validationWg.Done()
	for req := range mc.validationQueue {
		doc, err := agentmod.ValidateOpenAPI(req.Entry.OpenAPIYAML)
		req.Result <- &ValidationResult{
			ModuleID: req.Entry.ModuleID,
			Valid:    err == nil,
			Error:    err,
			Doc:      doc,
		}
	}
}

// HandleControlMessage processes a module-related control message.
func (mc *ModuleCatalog) HandleControlMessage(msg *mux.ControlMessage) error {
	switch msg.Type {
	case moduleproto.MsgSnapshotStart:
		return mc.handleSnapshotStart(msg.Payload)
	case moduleproto.MsgSnapshotChunk:
		return mc.handleSnapshotChunk(msg.Payload)
	case moduleproto.MsgSnapshotCommit:
		return mc.handleSnapshotCommit(msg.Payload)
	case moduleproto.MsgModuleHealth:
		return mc.handleModuleHealth(msg.Payload)
	case moduleproto.MsgModuleRemove:
		return mc.handleModuleRemove(msg.Payload)
	default:
		return fmt.Errorf("unknown message type: %s", msg.Type)
	}
}

// handleSnapshotStart begins a new snapshot reassembly.
func (mc *ModuleCatalog) handleSnapshotStart(payload json.RawMessage) error {
	var p moduleproto.SnapshotStartPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("unmarshal start: %w", err)
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Reject if epoch doesn't match (stale snapshot from old connection).
	if p.Epoch != mc.epoch {
		return fmt.Errorf("stale epoch: got %s, want %s", p.Epoch, mc.epoch)
	}

	// Reject if another reassembly is in progress.
	if mc.reassembly != nil {
		return fmt.Errorf("reassembly already in progress")
	}

	// Validate limits.
	if p.ModuleCount > moduleproto.MaxModulesPerAgent {
		return fmt.Errorf("too many modules: %d (max %d)", p.ModuleCount, moduleproto.MaxModulesPerAgent)
	}
	if p.TotalSize > moduleproto.MaxCatalogSize {
		return fmt.Errorf("catalog too large: %d bytes (max %d)", p.TotalSize, moduleproto.MaxCatalogSize)
	}

	mc.reassembly = &SnapshotReassembly{
		Epoch:       p.Epoch,
		ModuleCount: p.ModuleCount,
		TotalSize:   p.TotalSize,
		Chunks:      make([][]byte, 0),
		StartedAt:   time.Now(),
	}

	log.Printf("catalog: snapshot start (epoch=%s, modules=%d, size=%d)", p.Epoch, p.ModuleCount, p.TotalSize)
	return nil
}

// handleSnapshotChunk receives a chunk of snapshot data.
func (mc *ModuleCatalog) handleSnapshotChunk(payload json.RawMessage) error {
	var p moduleproto.SnapshotChunkPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("unmarshal chunk: %w", err)
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.reassembly == nil {
		return fmt.Errorf("no reassembly in progress")
	}

	// Validate sequence number.
	if p.Seq != len(mc.reassembly.Chunks) {
		return fmt.Errorf("unexpected chunk seq: got %d, want %d", p.Seq, len(mc.reassembly.Chunks))
	}

	// Validate size.
	mc.reassembly.ReceivedSize += int64(len(p.Data))
	if mc.reassembly.ReceivedSize > mc.reassembly.TotalSize {
		return fmt.Errorf("received too much data: %d > %d", mc.reassembly.ReceivedSize, mc.reassembly.TotalSize)
	}

	mc.reassembly.Chunks = append(mc.reassembly.Chunks, p.Data)
	return nil
}

// handleSnapshotCommit completes the snapshot reassembly and validates entries.
func (mc *ModuleCatalog) handleSnapshotCommit(payload json.RawMessage) error {
	var p moduleproto.SnapshotCommitPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("unmarshal commit: %w", err)
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.reassembly == nil {
		return fmt.Errorf("no reassembly in progress")
	}

	// Verify epoch.
	if p.Epoch != mc.reassembly.Epoch {
		return fmt.Errorf("epoch mismatch: got %s, want %s", p.Epoch, mc.reassembly.Epoch)
	}

	// Verify chunk count.
	if p.ChunkCount != len(mc.reassembly.Chunks) {
		return fmt.Errorf("chunk count mismatch: got %d, want %d", p.ChunkCount, len(mc.reassembly.Chunks))
	}

	// Concatenate chunks and verify digest.
	var data []byte
	for _, chunk := range mc.reassembly.Chunks {
		data = append(data, chunk...)
	}

	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])
	if digestHex != p.Digest {
		return fmt.Errorf("digest mismatch: got %s, want %s", digestHex, p.Digest)
	}

	// Parse module entries.
	entries, err := moduleproto.UnmarshalSnapshotEntries(data)
	if err != nil {
		return fmt.Errorf("unmarshal entries: %w", err)
	}

	// Clear reassembly state.
	mc.reassembly = nil

	// Validate each entry's OpenAPI document.
	newCatalog := &CommittedCatalog{
		Epoch:       p.Epoch,
		Modules:     make(map[string]*ModuleInfo),
		Revision:    1,
		PublishedAt: time.Now(),
	}

	if mc.committed != nil {
		newCatalog.Revision = mc.committed.Revision + 1
	}

	// Validate entries (synchronous for now; could be async with queue).
	for _, entry := range entries {
		doc, err := agentmod.ValidateOpenAPI(entry.OpenAPIYAML)
		if err != nil {
			log.Printf("catalog: invalid OpenAPI for module %s: %v", entry.ModuleID, err)
			continue // Skip invalid modules.
		}

		newCatalog.Modules[entry.ModuleID] = &ModuleInfo{
			ModuleID:         entry.ModuleID,
			ServiceName:      entry.ServiceName,
			ServiceVersion:   entry.ServiceVersion,
			Revision:         entry.Revision,
			OpenAPIYAML:      entry.OpenAPIYAML,
			ValidatedOpenAPI: doc,
			Health:           entry.Health,
			UI:               entry.UI,
		}
	}

	// Atomically replace the committed catalog.
	mc.committed = newCatalog

	log.Printf("catalog: snapshot committed (epoch=%s, revision=%d, modules=%d)",
		p.Epoch, newCatalog.Revision, len(newCatalog.Modules))

	return nil
}

// handleModuleHealth updates the health of a specific module.
func (mc *ModuleCatalog) handleModuleHealth(payload json.RawMessage) error {
	var p moduleproto.ModuleHealthPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("unmarshal health: %w", err)
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.committed == nil {
		return nil // No catalog yet.
	}

	info, ok := mc.committed.Modules[p.ModuleID]
	if !ok {
		return fmt.Errorf("module not found: %s", p.ModuleID)
	}

	info.Health = moduleproto.HealthEntry{
		Status:    p.Status,
		Message:   p.Message,
		CheckedAt: p.CheckedAt,
	}

	return nil
}

// handleModuleRemove removes a module from the catalog.
func (mc *ModuleCatalog) handleModuleRemove(payload json.RawMessage) error {
	var p moduleproto.ModuleRemovePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("unmarshal remove: %w", err)
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.committed == nil {
		return nil
	}

	delete(mc.committed.Modules, p.ModuleID)
	log.Printf("catalog: removed module %s", p.ModuleID)
	return nil
}

// GetModule returns information about a specific module, or nil if not found.
func (mc *ModuleCatalog) GetModule(moduleID string) *ModuleInfo {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if mc.committed == nil {
		return nil
	}
	return mc.committed.Modules[moduleID]
}

// ListModules returns all modules in the current catalog.
func (mc *ModuleCatalog) ListModules() []*ModuleInfo {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if mc.committed == nil {
		return nil
	}

	result := make([]*ModuleInfo, 0, len(mc.committed.Modules))
	for _, info := range mc.committed.Modules {
		result = append(result, info)
	}
	return result
}

// Epoch returns the catalog epoch (session ID).
func (mc *ModuleCatalog) Epoch() string {
	return mc.epoch
}

// Revision returns the current catalog revision.
func (mc *ModuleCatalog) Revision() int {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	if mc.committed == nil {
		return 0
	}
	return mc.committed.Revision
}

// Close shuts down the catalog and its validation workers.
func (mc *ModuleCatalog) Close() {
	close(mc.validationQueue)
	mc.validationWg.Wait()
}
