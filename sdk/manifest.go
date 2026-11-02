package sdk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// moduleIDRegexp validates a module ID: reverse-DNS, lowercase, max 128 chars.
// Examples: "com.example.mymodule", "io.github.user.project".
var moduleIDRegexp = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+$`)

// MaxModuleIDLen caps a module ID at 128 characters.
const MaxModuleIDLen = 128

// MaxServiceNameLen caps a service name at 128 characters.
const MaxServiceNameLen = 128

// MaxServiceVersionLen caps a semantic version string at 32 characters.
const MaxServiceVersionLen = 32

// MaxRevisionLen caps a revision string at 64 characters.
const MaxRevisionLen = 64

// MaxOpenAPISize caps an OpenAPI YAML document at 1 MiB.
const MaxOpenAPISize = 1 << 20

// MaxAssetSize caps a single UI asset at 8 MiB.
const MaxAssetSize = 8 << 20

// MaxModulesPerAgent caps the number of modules per agent at 64.
const MaxModulesPerAgent = 64

// Manifest declares a module's identity, service contract, and optional UI
// metadata. A provider supplies this to [Serve] to register the module.
type Manifest struct {
	// ModuleID is a reverse-DNS identifier (e.g. "com.example.mymodule").
	// Must be lowercase, match moduleIDRegexp, and not exceed MaxModuleIDLen.
	ModuleID string

	// ServiceName is a human-readable service name (e.g. "my-service").
	// Must not exceed MaxServiceNameLen.
	ServiceName string

	// ServiceVersion is a semantic version string (e.g. "1.2.3").
	// Must not exceed MaxServiceVersionLen.
	ServiceVersion string

	// Revision is an opaque string that changes when the module's contract
	// or implementation changes. Consumers use it to detect stale references.
	// Must not exceed MaxRevisionLen. Empty means the provider does not
	// manage revisions explicitly.
	Revision string

	// OpenAPIYAML is the OpenAPI v3.0 or v3.1 YAML document describing the
	// module's HTTP operations. Must not exceed MaxOpenAPISize.
	OpenAPIYAML []byte

	// UIDescriptors lists optional UI metadata for federation integration.
	// Nil means the module provides no UI.
	UIDescriptors []UIDescriptor

	// HealthInterval is how often the provider should report health.
	// Zero means the default interval (30 seconds).
	HealthInterval time.Duration
}

// Validate checks that the manifest fields are well-formed. It does not
// validate the OpenAPI YAML content (that happens separately during
// registration).
func (m *Manifest) Validate() error {
	if m.ModuleID == "" {
		return fmt.Errorf("module ID is required")
	}
	if len(m.ModuleID) > MaxModuleIDLen {
		return fmt.Errorf("module ID too long: %d characters (max %d)", len(m.ModuleID), MaxModuleIDLen)
	}
	if !moduleIDRegexp.MatchString(m.ModuleID) {
		return fmt.Errorf("module ID %q is not a valid reverse-DNS identifier", m.ModuleID)
	}
	if m.ServiceName == "" {
		return fmt.Errorf("service name is required")
	}
	if len(m.ServiceName) > MaxServiceNameLen {
		return fmt.Errorf("service name too long: %d characters (max %d)", len(m.ServiceName), MaxServiceNameLen)
	}
	if m.ServiceVersion == "" {
		return fmt.Errorf("service version is required")
	}
	if len(m.ServiceVersion) > MaxServiceVersionLen {
		return fmt.Errorf("service version too long: %d characters (max %d)", len(m.ServiceVersion), MaxServiceVersionLen)
	}
	if len(m.Revision) > MaxRevisionLen {
		return fmt.Errorf("revision too long: %d characters (max %d)", len(m.Revision), MaxRevisionLen)
	}
	if len(m.OpenAPIYAML) == 0 {
		return fmt.Errorf("OpenAPI YAML is required")
	}
	if len(m.OpenAPIYAML) > MaxOpenAPISize {
		return fmt.Errorf("OpenAPI YAML too large: %d bytes (max %d)", len(m.OpenAPIYAML), MaxOpenAPISize)
	}
	for i, desc := range m.UIDescriptors {
		if err := desc.Validate(); err != nil {
			return fmt.Errorf("UI descriptor[%d]: %w", i, err)
		}
	}
	return nil
}

// ModuleRef identifies a specific module instance on a specific agent at a
// specific catalog epoch and revision.
type ModuleRef struct {
	AgentID  string // agent identifier (e.g. "mydevbox")
	ModuleID string // module identifier (e.g. "com.example.mymodule")
	Epoch    string // catalog epoch (transport session ID)
	Revision string // module revision at time of discovery
}

// String returns a human-readable reference: "agent/module@epoch:revision".
func (r ModuleRef) String() string {
	s := r.AgentID + "/" + r.ModuleID
	if r.Epoch != "" {
		s += "@" + r.Epoch
	}
	if r.Revision != "" {
		s += ":" + r.Revision
	}
	return s
}

// Health represents the current health of a module.
type Health struct {
	// Status is the health status: "healthy", "degraded", or "unhealthy".
	Status HealthStatus

	// Message is an optional human-readable description.
	Message string

	// CheckedAt is when the health was last verified.
	CheckedAt time.Time
}

// HealthStatus is a module's health state.
type HealthStatus string

const (
	HealthHealthy   HealthStatus = "healthy"
	HealthDegraded  HealthStatus = "degraded"
	HealthUnhealthy HealthStatus = "unhealthy"
)

// UIDescriptor declares a module's UI integration metadata. The console
// uses this to load and display the module's federation remote.
type UIDescriptor struct {
	// Kind is "provider" (module-owned UI) or "consumer" (server-local UI
	// that consumes this module's API).
	Kind UIKind

	// RemoteName is the federation remote name (e.g. "com.example.mymodule").
	RemoteName string

	// PageURL is the module page path within the console (e.g. "/modules/mymodule").
	PageURL string

	// PackageDigest is the SHA-256 hex digest of the trusted UI package.
	// Required for provider UIs; empty for consumer UIs (which use a local
	// trust store entry).
	PackageDigest string

	// AssetPaths lists the relative paths of all assets in the UI package.
	AssetPaths []AssetEntry
}

// UIKind identifies whether a UI descriptor is a provider or consumer remote.
type UIKind string

const (
	UIProvider UIKind = "provider"
	UIConsumer UIKind = "consumer"
)

// AssetEntry is one file in a UI package with its expected size and digest.
type AssetEntry struct {
	Path   string // relative path within the package (e.g. "assets/index.js")
	Size   int64  // file size in bytes
	Digest string // SHA-256 hex digest of the file content
}

// Validate checks that the UI descriptor fields are well-formed.
func (d *UIDescriptor) Validate() error {
	if d.Kind != UIProvider && d.Kind != UIConsumer {
		return fmt.Errorf("invalid UI kind: %q", d.Kind)
	}
	if d.RemoteName == "" {
		return fmt.Errorf("remote name is required")
	}
	if d.PageURL == "" {
		return fmt.Errorf("page URL is required")
	}
	if !strings.HasPrefix(d.PageURL, "/") {
		return fmt.Errorf("page URL must start with /")
	}
	if d.Kind == UIProvider && d.PackageDigest == "" {
		return fmt.Errorf("provider UI requires a package digest")
	}
	for i, a := range d.AssetPaths {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("asset[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks that the asset entry fields are well-formed.
func (a *AssetEntry) Validate() error {
	if a.Path == "" {
		return fmt.Errorf("asset path is required")
	}
	if strings.HasPrefix(a.Path, "/") || strings.Contains(a.Path, "..") {
		return fmt.Errorf("asset path must be relative and not contain ..")
	}
	if a.Size <= 0 {
		return fmt.Errorf("asset size must be positive")
	}
	if a.Size > MaxAssetSize {
		return fmt.Errorf("asset too large: %d bytes (max %d)", a.Size, MaxAssetSize)
	}
	if a.Digest == "" {
		return fmt.Errorf("asset digest is required")
	}
	if len(a.Digest) != 64 {
		return fmt.Errorf("asset digest must be 64 hex characters (SHA-256)")
	}
	return nil
}

// DigestAsset computes the SHA-256 hex digest of the given content.
func DigestAsset(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}
