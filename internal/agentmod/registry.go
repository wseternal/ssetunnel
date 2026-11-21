package agentmod

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Registry tracks installed module packages and their desired lifecycle state.
// It persists desired state (enabled/disabled) to disk so modules can be
// restored after an agent process restart.
type Registry struct {
	mu       sync.RWMutex
	dataDir  string
	packages map[string]*Package     // moduleID → installed package
	desired  map[string]DesiredState // moduleID → enabled/disabled
	// probeTimeout is the budget for one manifest probe during a scan.
	// Zero means defaultManifestProbeTimeout.
	probeTimeout time.Duration
}

// DesiredState represents the user's desired state for a module.
type DesiredState struct {
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewRegistry creates a registry that persists state in the given data directory.
func NewRegistry(dataDir string) (*Registry, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	r := &Registry{
		dataDir:  dataDir,
		packages: make(map[string]*Package),
		desired:  make(map[string]DesiredState),
	}
	if err := r.loadPackages(); err != nil {
		log.Printf("registry: failed to load packages: %v", err)
		// Continue with empty packages; will be populated as modules are installed.
	}
	if err := r.loadDesiredState(); err != nil {
		log.Printf("registry: failed to load desired state: %v", err)
		// Continue with empty state; will be populated as modules are installed.
	}
	// Prune desired state entries whose packages were not loaded from disk.
	r.pruneOrphanDesiredState()
	return r, nil
}

// SetManifestProbeTimeout sets the budget for one manifest probe during a
// scan. Values of zero or below restore the default.
func (r *Registry) SetManifestProbeTimeout(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d <= 0 {
		d = defaultManifestProbeTimeout
	}
	r.probeTimeout = d
}

// manifestProbeBudget returns the probe budget, falling back to the default
// when the registry was not configured.
func (r *Registry) manifestProbeBudget() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.probeTimeout <= 0 {
		return defaultManifestProbeTimeout
	}
	return r.probeTimeout
}

// packageForPath returns the registered package whose executable and package
// directory match the given paths, or nil. Scanning uses it to recognize an
// already-registered package before probing it.
func (r *Registry) packageForPath(packageDir, executable string) *Package {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, pkg := range r.packages {
		if pkg.PackageDir == packageDir && pkg.Executable == executable {
			return pkg
		}
	}
	return nil
}

// Install registers a new module package. The package is validated and
// its OpenAPI YAML is parsed. If a package with the same module ID already
// exists, it is replaced (version upgrade).
func (r *Registry) Install(pkg *Package) error {
	if err := pkg.Validate(); err != nil {
		return fmt.Errorf("validate package: %w", err)
	}
	if _, err := ValidateOpenAPI(pkg.OpenAPIYAML); err != nil {
		return fmt.Errorf("validate openapi: %w", err)
	}
	// Record the executable's size and mtime so the next scan can skip the
	// manifest probe while the file stays unchanged.
	pkg.stampBinary()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.packages[pkg.ModuleID] = pkg

	// Default to disabled on first install.
	if _, exists := r.desired[pkg.ModuleID]; !exists {
		r.desired[pkg.ModuleID] = DesiredState{Enabled: false, UpdatedAt: time.Now()}
	}

	if err := r.savePackages(); err != nil {
		return fmt.Errorf("save packages: %w", err)
	}
	return r.saveDesiredState()
}

// Remove uninstalls a module package. The module must be stopped first.
func (r *Registry) Remove(moduleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.packages[moduleID]; !exists {
		return fmt.Errorf("module %q not installed", moduleID)
	}

	// Check if the module is enabled (must stop first).
	if state, ok := r.desired[moduleID]; ok && state.Enabled {
		return fmt.Errorf("module %q is enabled; stop it first", moduleID)
	}

	delete(r.packages, moduleID)
	delete(r.desired, moduleID)

	if err := r.savePackages(); err != nil {
		return fmt.Errorf("save packages: %w", err)
	}
	return r.saveDesiredState()
}

// Enable marks a module as enabled (desired state). The supervisor will
// start it on the next reconciliation.
func (r *Registry) Enable(moduleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.packages[moduleID]; !exists {
		return fmt.Errorf("module %q not installed", moduleID)
	}

	r.desired[moduleID] = DesiredState{Enabled: true, UpdatedAt: time.Now()}
	return r.saveDesiredState()
}

// Disable marks a module as disabled (desired state). The supervisor will
// stop it on the next reconciliation.
func (r *Registry) Disable(moduleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.desired[moduleID]; !exists {
		return nil // Already disabled.
	}

	state := r.desired[moduleID]
	state.Enabled = false
	state.UpdatedAt = time.Now()
	r.desired[moduleID] = state
	return r.saveDesiredState()
}

// Package returns the installed package for a module, or nil if not installed.
func (r *Registry) Package(moduleID string) *Package {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.packages[moduleID]
}

// IsEnabled returns true if the module is enabled.
func (r *Registry) IsEnabled(moduleID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.desired[moduleID]
	return ok && state.Enabled
}

// EnabledModules returns the module IDs of all enabled modules.
func (r *Registry) EnabledModules() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []string
	for id, state := range r.desired {
		if state.Enabled {
			result = append(result, id)
		}
	}
	return result
}

// AllModules returns all installed module IDs.
func (r *Registry) AllModules() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, 0, len(r.packages))
	for id := range r.packages {
		result = append(result, id)
	}
	return result
}

// InstallFromIPC auto-installs or updates a module package from a live IPC
// registration. This is used when a module process connects directly to the
// agent (without going through the package installation flow). The module is
// automatically enabled so it appears in catalog snapshots.
func (r *Registry) InstallFromIPC(moduleID, serviceName, version, revision string, openAPIYAML []byte, executable string) error {
	// Validate OpenAPI before installing (same as Install flow).
	if len(openAPIYAML) > 0 {
		if _, err := ValidateOpenAPI(openAPIYAML); err != nil {
			return fmt.Errorf("validate openapi: %w", err)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	pkg := &Package{
		ModuleID:       moduleID,
		ServiceName:    serviceName,
		ServiceVersion: version,
		Revision:       revision,
		Executable:     executable,
		OpenAPIYAML:    openAPIYAML,
		PackageDir:     r.dataDir,
	}

	// Replace or install the package.
	r.packages[moduleID] = pkg

	// Auto-enable: the module process is already running.
	r.desired[moduleID] = DesiredState{Enabled: true, UpdatedAt: time.Now()}

	if err := r.savePackages(); err != nil {
		return fmt.Errorf("save packages: %w", err)
	}
	return r.saveDesiredState()
}

// Uninstall removes a module package from the registry.
// The desired state is updated to reflect that the module is no longer enabled.
func (r *Registry) Uninstall(moduleID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.packages, moduleID)
	delete(r.desired, moduleID)
	if err := r.savePackages(); err != nil {
		log.Printf("registry: failed to save packages after uninstall: %v", err)
	}
	if err := r.saveDesiredState(); err != nil {
		log.Printf("registry: failed to save desired state after uninstall: %v", err)
	}
}

// Reload re-reads packages and desired state from disk. This is called
// when the agent receives SIGHUP to pick up changes made by CLI module
// commands (install, start, stop, etc.) that modify the on-disk state
// in a separate process.
//
// The load and prune steps happen on local maps without holding r.mu,
// so concurrent readers (EnabledModules, Package) are unaffected. The
// final swap is performed under a single write lock for atomicity.
func (r *Registry) Reload() error {
	pkgs, err := r.readPackagesFromDisk()
	if err != nil {
		return fmt.Errorf("reload packages: %w", err)
	}
	desired, err := r.readDesiredStateFromDisk()
	if err != nil {
		return fmt.Errorf("reload desired state: %w", err)
	}
	// Prune orphan desired state entries locally.
	for id := range desired {
		if _, ok := pkgs[id]; !ok {
			delete(desired, id)
		}
	}
	// Persist the pruned desired state.
	data, err := json.MarshalIndent(desired, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal desired state: %w", err)
	}
	tmp := r.desiredStatePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write desired state: %w", err)
	}
	if err := os.Rename(tmp, r.desiredStatePath()); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename desired state: %w", err)
	}
	// Atomic swap under write lock.
	r.mu.Lock()
	r.packages = pkgs
	r.desired = desired
	r.mu.Unlock()
	return nil
}

// readPackagesFromDisk loads package metadata from disk into a new map.
// Does not mutate r.packages.
func (r *Registry) readPackagesFromDisk() (map[string]*Package, error) {
	data, err := os.ReadFile(r.packagesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*Package), nil
		}
		return nil, err
	}
	var pkgs map[string]*Package
	if err := json.Unmarshal(data, &pkgs); err != nil {
		return nil, err
	}
	if pkgs == nil {
		pkgs = make(map[string]*Package)
	}
	return pkgs, nil
}

// readDesiredStateFromDisk loads desired state from disk into a new map.
// Does not mutate r.desired.
func (r *Registry) readDesiredStateFromDisk() (map[string]DesiredState, error) {
	data, err := os.ReadFile(r.desiredStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]DesiredState), nil
		}
		return nil, err
	}
	var desired map[string]DesiredState
	if err := json.Unmarshal(data, &desired); err != nil {
		return nil, err
	}
	if desired == nil {
		desired = make(map[string]DesiredState)
	}
	return desired, nil
}

// packagesPath returns the path to the packages persistence file.
func (r *Registry) packagesPath() string {
	return filepath.Join(r.dataDir, "packages.json")
}

// loadPackages loads installed package metadata from disk.
func (r *Registry) loadPackages() error {
	data, err := os.ReadFile(r.packagesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No packages file yet.
		}
		return err
	}
	return json.Unmarshal(data, &r.packages)
}

// savePackages persists installed package metadata to disk atomically.
func (r *Registry) savePackages() error {
	data, err := json.MarshalIndent(r.packages, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal packages: %w", err)
	}

	// Write to a temp file and rename for atomicity.
	tmp := r.packagesPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, r.packagesPath()); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// pruneOrphanDesiredState removes desired state entries that have no
// corresponding package metadata. This keeps the two maps consistent
// after a restart where packages.json may be missing or incomplete.
func (r *Registry) pruneOrphanDesiredState() {
	for id := range r.desired {
		if _, ok := r.packages[id]; !ok {
			delete(r.desired, id)
		}
	}
	if err := r.saveDesiredState(); err != nil {
		log.Printf("registry: failed to save desired state after prune: %v", err)
	}
}

// desiredStatePath returns the path to the desired state file.
func (r *Registry) desiredStatePath() string {
	return filepath.Join(r.dataDir, "desired_state.json")
}

// loadDesiredState loads the desired state from disk.
func (r *Registry) loadDesiredState() error {
	data, err := os.ReadFile(r.desiredStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No state file yet.
		}
		return err
	}
	return json.Unmarshal(data, &r.desired)
}

// saveDesiredState persists the desired state to disk atomically.
func (r *Registry) saveDesiredState() error {
	data, err := json.MarshalIndent(r.desired, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal desired state: %w", err)
	}

	// Write to a temp file and rename for atomicity.
	tmp := r.desiredStatePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, r.desiredStatePath()); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Supervisor manages the lifecycle of module processes. It starts enabled
// modules, stops disabled ones, and restarts crashed modules with jittered
// backoff.
type Supervisor struct {
	mu        sync.Mutex
	registry  *Registry
	ipc       *IPCServer
	processes map[string]*ProcessHandle
	restarts  map[string]int // cumulative restart count per module ID
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// ProcessHandle tracks a running module process.
type ProcessHandle struct {
	ModuleID  string
	Package   *Package
	Process   *os.Process
	StartedAt time.Time
	LastExit  time.Time
	cancel    context.CancelFunc
}

// NewSupervisor creates a supervisor that manages processes for the given
// registry and IPC server.
func NewSupervisor(ctx context.Context, registry *Registry, ipc *IPCServer) *Supervisor {
	ctx, cancel := context.WithCancel(ctx)
	return &Supervisor{
		registry:  registry,
		ipc:       ipc,
		processes: make(map[string]*ProcessHandle),
		restarts:  make(map[string]int),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Reconcile starts enabled modules and stops disabled ones. This should
// be called after any state change (install, enable, disable, remove).
func (s *Supervisor) Reconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	enabled := make(map[string]bool)
	for _, id := range s.registry.EnabledModules() {
		enabled[id] = true
	}

	// Stop processes that are no longer enabled.
	for id, handle := range s.processes {
		if !enabled[id] {
			log.Printf("supervisor: stopping disabled module %s", id)
			s.stopProcess(handle)
			delete(s.processes, id)
			delete(s.restarts, id)
		}
	}

	// Start processes that are enabled but not running.
	for id := range enabled {
		if _, running := s.processes[id]; !running {
			pkg := s.registry.Package(id)
			if pkg == nil {
				log.Printf("supervisor: module %s is enabled but not installed", id)
				continue
			}
			if err := s.startProcess(pkg); err != nil {
				log.Printf("supervisor: failed to start module %s: %v", id, err)
			}
		}
	}

	return nil
}

// startProcess launches a module process.
func (s *Supervisor) startProcess(pkg *Package) error {
	ctx, cancel := context.WithCancel(s.ctx)
	handle := &ProcessHandle{
		ModuleID:  pkg.ModuleID,
		Package:   pkg,
		StartedAt: time.Now(),
		cancel:    cancel,
	}

	// Launch the process with the IPC credential.
	listenerAddr, err := s.ipc.ListenerAddr()
	if err != nil {
		cancel()
		return fmt.Errorf("get listener addr: %w", err)
	}
	cmd := startModuleProcess(ctx, pkg.Executable, s.ipc.Credential(), listenerAddr, pkg.ModuleID)
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start process: %w", err)
	}

	handle.Process = cmd.Process
	s.processes[pkg.ModuleID] = handle

	// Monitor the process in a goroutine.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.monitorProcess(handle)
	}()

	log.Printf("supervisor: started module %s (pid=%d)", pkg.ModuleID, cmd.Process.Pid)
	return nil
}

// monitorProcess waits for a process to exit and handles restart.
func (s *Supervisor) monitorProcess(handle *ProcessHandle) {
	state, err := handle.Process.Wait()
	handle.LastExit = time.Now()

	if err != nil {
		log.Printf("supervisor: module %s wait error: %v", handle.ModuleID, err)
	} else if !state.Success() {
		log.Printf("supervisor: module %s exited with status %d", handle.ModuleID, state.ExitCode())
	} else {
		log.Printf("supervisor: module %s exited normally", handle.ModuleID)
	}

	// Deregister the module from IPC.
	s.ipc.Deregister(handle.ModuleID)

	// Check if we should restart.
	s.mu.Lock()

	// If the handle is still in our map and the module is enabled, restart.
	if current, ok := s.processes[handle.ModuleID]; ok && current == handle {
		if s.registry.IsEnabled(handle.ModuleID) {
			s.restarts[handle.ModuleID]++
			count := s.restarts[handle.ModuleID]
			backoff := calculateBackoff(count)
			log.Printf("supervisor: restarting module %s in %v (attempt %d)", handle.ModuleID, backoff, count)

			// Release lock before sleeping (C6 fix: prevent deadlock).
			s.mu.Unlock()

			select {
			case <-time.After(backoff):
				// Re-acquire lock and re-validate state.
				s.mu.Lock()
				if current, ok := s.processes[handle.ModuleID]; ok && current == handle {
					delete(s.processes, handle.ModuleID)
					s.mu.Unlock()
					if err := s.startProcess(handle.Package); err != nil {
						log.Printf("supervisor: restart failed for module %s: %v", handle.ModuleID, err)
					}
				} else {
					s.mu.Unlock()
				}
			case <-s.ctx.Done():
				// Supervisor shutting down.
			}
			return
		}
		delete(s.processes, handle.ModuleID)
		delete(s.restarts, handle.ModuleID)
	}
	s.mu.Unlock()
}

// stopProcess terminates a module process gracefully, then kills it.
func (s *Supervisor) stopProcess(handle *ProcessHandle) {
	if handle.Process == nil {
		return
	}
	handle.cancel()

	// Send SIGTERM (or equivalent) and wait briefly.
	_ = handle.Process.Signal(os.Interrupt)

	// Wait up to 5 seconds for graceful shutdown.
	done := make(chan struct{})
	go func() {
		handle.Process.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Process exited.
	case <-time.After(5 * time.Second):
		// Force kill.
		log.Printf("supervisor: killing module %s (graceful timeout)", handle.ModuleID)
		handle.Process.Kill()
	}
}

// Stop terminates all running module processes.
func (s *Supervisor) Stop() {
	s.cancel()

	s.mu.Lock()
	for id, handle := range s.processes {
		s.stopProcess(handle)
		delete(s.processes, id)
		delete(s.restarts, id)
	}
	s.mu.Unlock()

	s.wg.Wait()
}

// calculateBackoff returns a jittered backoff duration based on the
// restart count. The backoff starts at 500ms and caps at 30s.
func calculateBackoff(count int) time.Duration {
	base := 500 * time.Millisecond
	max := 30 * time.Second

	d := base * time.Duration(1<<min(count-1, 6)) // Cap exponent at 6 (32x = 16s base)
	if d > max {
		d = max
	}

	// Add jitter: ±25%.
	jitter := time.Duration(float64(d) * 0.25 * (2*randFloat() - 1))
	return d + jitter
}

// randFloat returns a random float in [0, 1) using crypto/rand (W21 fix).
func randFloat() float64 {
	var b [2]byte
	cryptoRand.Read(b[:])
	return float64(uint16(b[0])<<8|uint16(b[1])) / 65536.0
}

// ErrModuleNotRunning is returned when trying to stop a module that isn't running.
var ErrModuleNotRunning = errors.New("module not running")
