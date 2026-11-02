package agent

import (
	"context"
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

// ModuleManager manages the agent-side module registry and publishes
// catalog snapshots to the server via the control stream. It survives
// tunnel reconnects and republishes the current state on each connection.
type ModuleManager struct {
	mu       sync.RWMutex
	registry *agentmod.Registry
	ipc      *agentmod.IPCServer
	supervisor *agentmod.Supervisor

	// Current snapshot state.
	snapshot *CatalogSnapshot

	// Pending snapshot updates (coalesced).
	pendingUpdate bool
	updateTimer   *time.Timer

	// Callback to send control messages (set by agent on connect).
	sendControl func(msg *mux.ControlMessage) error
}

// CatalogSnapshot is the current state of all modules for publication.
type CatalogSnapshot struct {
	Epoch    string
	Modules  []moduleproto.SnapshotModuleEntry
	Revision int // increments on each change
}

// NewModuleManager creates a module manager with the given registry and IPC server.
func NewModuleManager(registry *agentmod.Registry, ipc *agentmod.IPCServer, supervisor *agentmod.Supervisor) *ModuleManager {
	return &ModuleManager{
		registry:   registry,
		ipc:        ipc,
		supervisor: supervisor,
	}
}

// SetControlSender sets the callback for sending control messages.
// Called by the agent when a new control stream is established.
func (m *ModuleManager) SetControlSender(sender func(msg *mux.ControlMessage) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendControl = sender
}

// OnConnect is called when a new tunnel connection is established.
// It publishes a fresh catalog snapshot to the server.
func (m *ModuleManager) OnConnect(ctx context.Context, epoch string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Build fresh snapshot from current registry state.
	snapshot := m.buildSnapshotLocked(epoch)
	m.snapshot = snapshot

	// Publish immediately.
	return m.publishSnapshotLocked(ctx, snapshot)
}

// OnRegistryChange is called when the registry changes (install, remove, enable, disable).
// It coalesces updates and publishes after a brief delay.
func (m *ModuleManager) OnRegistryChange(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pendingUpdate = true

	// Coalesce updates: wait 100ms before publishing.
	if m.updateTimer == nil {
		m.updateTimer = time.AfterFunc(100*time.Millisecond, func() {
			m.publishCoalescedUpdate(ctx)
		})
	}
}

// publishCoalescedUpdate publishes a snapshot after coalescing multiple changes.
func (m *ModuleManager) publishCoalescedUpdate(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.pendingUpdate {
		return
	}
	m.pendingUpdate = false

	// Rebuild snapshot with current epoch.
	epoch := ""
	if m.snapshot != nil {
		epoch = m.snapshot.Epoch
	}
	snapshot := m.buildSnapshotLocked(epoch)
	snapshot.Revision = m.snapshot.Revision + 1
	m.snapshot = snapshot

	if err := m.publishSnapshotLocked(ctx, snapshot); err != nil {
		log.Printf("module manager: failed to publish snapshot: %v", err)
	}
}

// buildSnapshotLocked builds a catalog snapshot from the current registry state.
// Caller must hold m.mu.
func (m *ModuleManager) buildSnapshotLocked(epoch string) *CatalogSnapshot {
	var entries []moduleproto.SnapshotModuleEntry

	for _, moduleID := range m.registry.AllModules() {
		pkg := m.registry.Package(moduleID)
		if pkg == nil {
			continue
		}

		// Only include enabled modules in the snapshot.
		if !m.registry.IsEnabled(moduleID) {
			continue
		}

		entry := moduleproto.SnapshotModuleEntry{
			ModuleID:       pkg.ModuleID,
			ServiceName:    pkg.ServiceName,
			ServiceVersion: pkg.ServiceVersion,
			Revision:       pkg.Revision,
			OpenAPIYAML:    pkg.OpenAPIYAML,
			Health:         moduleproto.NewHealthEntry("healthy", "", time.Now()),
		}

		// Add UI descriptors if present.
		for _, desc := range pkg.UIDescriptors {
			uiEntry := moduleproto.UIEntry{
				Kind:          string(desc.Kind),
				RemoteName:    desc.RemoteName,
				PageURL:       desc.PageURL,
				PackageDigest: desc.PackageDigest,
			}
			for _, asset := range desc.AssetPaths {
				uiEntry.AssetPaths = append(uiEntry.AssetPaths, moduleproto.AssetPath{
					Path:   asset.Path,
					Size:   asset.Size,
					Digest: asset.Digest,
				})
			}
			entry.UI = append(entry.UI, uiEntry)
		}

		entries = append(entries, entry)
	}

	return &CatalogSnapshot{
		Epoch:   epoch,
		Modules: entries,
	}
}

// publishSnapshotLocked publishes a catalog snapshot to the server.
// Caller must hold m.mu.
func (m *ModuleManager) publishSnapshotLocked(ctx context.Context, snapshot *CatalogSnapshot) error {
	if m.sendControl == nil {
		return fmt.Errorf("no control sender")
	}

	// Marshal module entries into chunk data.
	data, err := moduleproto.MarshalSnapshotEntries(snapshot.Modules)
	if err != nil {
		return fmt.Errorf("marshal entries: %w", err)
	}

	// Calculate digest.
	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])

	// Send start message.
	startPayload, _ := json.Marshal(moduleproto.SnapshotStartPayload{
		Epoch:       snapshot.Epoch,
		ModuleCount: len(snapshot.Modules),
		TotalSize:   int64(len(data)),
	})
	if err := m.sendControl(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotStart,
		Payload: startPayload,
	}); err != nil {
		return fmt.Errorf("send start: %w", err)
	}

	// Send chunks (32 KiB each).
	chunkSize := moduleproto.SnapshotChunkSize
	seq := 0
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunkPayload, _ := json.Marshal(moduleproto.SnapshotChunkPayload{
			Seq:  seq,
			Data: data[i:end],
		})
		if err := m.sendControl(&mux.ControlMessage{
			Type:    moduleproto.MsgSnapshotChunk,
			Payload: chunkPayload,
		}); err != nil {
			return fmt.Errorf("send chunk[%d]: %w", seq, err)
		}
		seq++
	}

	// Send commit message.
	commitPayload, _ := json.Marshal(moduleproto.SnapshotCommitPayload{
		Epoch:      snapshot.Epoch,
		ChunkCount: seq,
		Digest:     digestHex,
	})
	if err := m.sendControl(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotCommit,
		Payload: commitPayload,
	}); err != nil {
		return fmt.Errorf("send commit: %w", err)
	}

	log.Printf("module manager: published snapshot (epoch=%s, modules=%d, size=%d)",
		snapshot.Epoch, len(snapshot.Modules), len(data))

	return nil
}

// Stop shuts down the module manager and its supervisor.
func (m *ModuleManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.updateTimer != nil {
		m.updateTimer.Stop()
	}
	if m.supervisor != nil {
		m.supervisor.Stop()
	}
}
