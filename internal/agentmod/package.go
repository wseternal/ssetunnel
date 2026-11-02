// Package agentmod implements the agent-side module registry, process
// supervisor, and local IPC for separately managed module processes.
//
// The registry tracks installed module packages, their desired lifecycle
// state (enabled/disabled), and live registrations from running processes.
// The supervisor manages child processes for enabled modules, handling
// start, stop, restart, and crash recovery with jittered backoff.
//
// Module processes communicate with the registry over local IPC (Unix
// sockets on Unix, named pipes on Windows). The IPC layer authenticates
// peers using owner-only endpoints and launch credentials.
package agentmod

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/sdk"
)

// moduleIDRegexp validates a module ID: reverse-DNS, lowercase, max 128 chars.
var moduleIDRegexp = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+$`)

// semverRegexp validates a semantic version string (major.minor.patch with optional pre-release).
var semverRegexp = regexp.MustCompile(`^\d+\.\d+\.\d+(-[a-zA-Z0-9]+(\.[a-zA-Z0-9]+)*)?$`)

// Package represents an installed module package on disk. It contains
// the module manifest, executable, OpenAPI YAML, and optional UI assets.
type Package struct {
	// ModuleID is the reverse-DNS module identifier.
	ModuleID string

	// ServiceName is the human-readable service name.
	ServiceName string

	// ServiceVersion is the semantic version string.
	ServiceVersion string

	// Revision is an optional revision string.
	Revision string

	// Executable is the absolute path to the module executable.
	Executable string

	// OpenAPIYAML is the raw OpenAPI v3 YAML content.
	OpenAPIYAML []byte

	// PackageDir is the directory containing the package files.
	PackageDir string

	// UIDescriptors lists optional UI metadata for federation.
	UIDescriptors []sdk.UIDescriptor

	// AssetDigests maps relative asset paths to their SHA-256 digests.
	// Populated during installation for trusted UI assets.
	AssetDigests map[string]string
}

// Validate checks that the package fields are well-formed. It does not
// validate the OpenAPI YAML content (use ValidateOpenAPI for that).
func (p *Package) Validate() error {
	if p.ModuleID == "" {
		return fmt.Errorf("module ID is required")
	}
	if len(p.ModuleID) > moduleproto.MaxModuleIDLen {
		return fmt.Errorf("module ID too long: %d characters (max %d)", len(p.ModuleID), moduleproto.MaxModuleIDLen)
	}
	if !moduleIDRegexp.MatchString(p.ModuleID) {
		return fmt.Errorf("module ID %q is not a valid reverse-DNS identifier", p.ModuleID)
	}
	if p.ServiceName == "" {
		return fmt.Errorf("service name is required")
	}
	if len(p.ServiceName) > moduleproto.MaxServiceNameLen {
		return fmt.Errorf("service name too long: %d characters (max %d)", len(p.ServiceName), moduleproto.MaxServiceNameLen)
	}
	if p.ServiceVersion == "" {
		return fmt.Errorf("service version is required")
	}
	if len(p.ServiceVersion) > moduleproto.MaxServiceVersionLen {
		return fmt.Errorf("service version too long: %d characters (max %d)", len(p.ServiceVersion), moduleproto.MaxServiceVersionLen)
	}
	if !semverRegexp.MatchString(p.ServiceVersion) {
		return fmt.Errorf("service version %q is not a valid semantic version", p.ServiceVersion)
	}
	if len(p.Revision) > moduleproto.MaxRevisionLen {
		return fmt.Errorf("revision too long: %d characters (max %d)", len(p.Revision), moduleproto.MaxRevisionLen)
	}
	if p.Executable == "" {
		return fmt.Errorf("executable path is required")
	}
	if !filepath.IsAbs(p.Executable) {
		return fmt.Errorf("executable path must be absolute")
	}
	if len(p.OpenAPIYAML) == 0 {
		return fmt.Errorf("OpenAPI YAML is required")
	}
	if len(p.OpenAPIYAML) > moduleproto.MaxOpenAPISize {
		return fmt.Errorf("OpenAPI YAML too large: %d bytes (max %d)", len(p.OpenAPIYAML), moduleproto.MaxOpenAPISize)
	}
	if p.PackageDir == "" {
		return fmt.Errorf("package directory is required")
	}
	// Validate executable is within the package directory.
	if !isSubPath(p.PackageDir, p.Executable) {
		return fmt.Errorf("executable must be within package directory")
	}
	// Validate UI descriptors.
	for i, desc := range p.UIDescriptors {
		if err := desc.Validate(); err != nil {
			return fmt.Errorf("UI descriptor[%d]: %w", i, err)
		}
	}
	return nil
}

// ValidateExecutable checks that the executable exists and is within the
// package directory. Rejects symlinks that escape the package (C9 fix).
func (p *Package) ValidateExecutable() error {
	// Use Lstat to detect symlinks.
	info, err := os.Lstat(p.Executable)
	if err != nil {
		return fmt.Errorf("executable not found: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("executable is a directory")
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("executable is not executable")
	}

	// Reject symlinked executables (C9 fix: prevent arbitrary code execution).
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("executable is a symlink (not allowed)")
	}

	// Resolve symlinks in the path to prevent traversal attacks.
	resolvedExec, err := filepath.EvalSymlinks(p.Executable)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	resolvedDir, err := filepath.EvalSymlinks(p.PackageDir)
	if err != nil {
		return fmt.Errorf("resolve package directory: %w", err)
	}

	if !isSubPath(resolvedDir, resolvedExec) {
		return fmt.Errorf("executable is outside package directory (after symlink resolution)")
	}
	return nil
}

// isSubPath checks that child is a sub-path of parent (no symlink traversal).
// Use with EvalSymlinks-resolved paths for security (C9 fix).
func isSubPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

// HashUIAssets computes SHA-256 digests for all UI assets in the package
// directory and stores them in p.AssetDigests. This is called during
// installation to build the trust store entries.
func (p *Package) HashUIAssets() error {
	p.AssetDigests = make(map[string]string)
	for _, desc := range p.UIDescriptors {
		for _, asset := range desc.AssetPaths {
			assetPath := filepath.Join(p.PackageDir, asset.Path)
			if !isSubPath(p.PackageDir, assetPath) {
				return fmt.Errorf("asset %q escapes package directory", asset.Path)
			}
			content, err := os.ReadFile(assetPath)
			if err != nil {
				return fmt.Errorf("read asset %q: %w", asset.Path, err)
			}
			digest := sdk.DigestAsset(content)
			if digest != asset.Digest {
				return fmt.Errorf("asset %q digest mismatch: got %s, want %s", asset.Path, digest, asset.Digest)
			}
			p.AssetDigests[asset.Path] = digest
		}
	}
	return nil
}
