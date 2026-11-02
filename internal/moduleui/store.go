// Package moduleui implements trusted UI package storage and asset
// serving for module federation. It provides digest-keyed caching,
// per-file verification, and bounded storage for both provider and
// server-consumer UI packages.
package moduleui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Errors returned by the UI store.
var (
	ErrPackageNotFound = errors.New("ui: package not found")
	ErrAssetNotFound   = errors.New("ui: asset not found")
	ErrDigestMismatch  = errors.New("ui: asset digest mismatch")
	ErrAssetTooLarge   = errors.New("ui: asset too large")
	ErrCacheFull       = errors.New("ui: cache full")
	ErrPathTraversal   = errors.New("ui: path traversal detected")
)

// MaxAssetSize is the maximum size of a single UI asset (8 MiB).
const MaxAssetSize = 8 << 20

// MaxCacheSize is the maximum total cache size (64 MiB).
const MaxCacheSize = 64 << 20

// PackageManifest describes a trusted UI package with its expected assets.
type PackageManifest struct {
	// PackageDigest is the SHA-256 hex digest of the complete package.
	PackageDigest string

	// RemoteName identifies the federation remote (e.g., "my-module-ui").
	RemoteName string

	// Assets lists every expected asset with its path, size, and digest.
	Assets []AssetEntry

	// InstalledAt records when this package was installed.
	InstalledAt time.Time
}

// AssetEntry describes one file in a UI package.
type AssetEntry struct {
	// Path is the relative path within the package (e.g., "index.js").
	Path string

	// Size is the expected file size in bytes.
	Size int64

	// Digest is the expected SHA-256 hex digest.
	Digest string
}

// Store manages trusted UI packages and their cached assets.
// It enforces digest verification on every asset before caching.
type Store struct {
	mu sync.RWMutex

	// Trusted packages (installed by admin).
	manifests map[string]*PackageManifest // packageDigest → manifest

	// Cached assets (verified and stored).
	cache map[string]*CachedAsset // "pkgDigest/relPath" → asset

	// Current cache usage in bytes.
	cacheUsed int64

	// Maximum cache size.
	maxCacheSize int64
}

// CachedAsset is a verified and cached UI asset.
type CachedAsset struct {
	Data      []byte
	Digest    string
	Size      int64
	CachedAt  time.Time
}

// NewStore creates a new UI store with the given maximum cache size.
func NewStore(maxCacheSize int64) *Store {
	if maxCacheSize <= 0 {
		maxCacheSize = MaxCacheSize
	}
	return &Store{
		manifests:    make(map[string]*PackageManifest),
		cache:        make(map[string]*CachedAsset),
		maxCacheSize: maxCacheSize,
	}
}

// Install registers a trusted package manifest. The manifest's digest
// must match the actual package digest for assets to be accepted.
func (s *Store) Install(manifest *PackageManifest) error {
	if manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	if manifest.PackageDigest == "" {
		return fmt.Errorf("package digest is required")
	}
	if manifest.RemoteName == "" {
		return fmt.Errorf("remote name is required")
	}

	// Validate all asset paths are relative and don't traverse.
	for _, asset := range manifest.Assets {
		if err := validateAssetPath(asset.Path); err != nil {
			return fmt.Errorf("invalid asset path %q: %w", asset.Path, err)
		}
		if asset.Size > MaxAssetSize {
			return fmt.Errorf("asset %q too large: %d bytes", asset.Path, asset.Size)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifests[manifest.PackageDigest] = manifest
	return nil
}

// Uninstall removes a trusted package and its cached assets.
func (s *Store) Uninstall(packageDigest string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.manifests, packageDigest)

	// Remove cached assets for this package.
	prefix := packageDigest + "/"
	for key, asset := range s.cache {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			s.cacheUsed -= asset.Size
			delete(s.cache, key)
		}
	}
}

// CacheAsset verifies and caches a UI asset. The asset's digest must
// match the manifest's expected digest for this path.
func (s *Store) CacheAsset(packageDigest, relPath string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	manifest, ok := s.manifests[packageDigest]
	if !ok {
		return ErrPackageNotFound
	}

	// Find the expected asset entry.
	var expected *AssetEntry
	for i := range manifest.Assets {
		if manifest.Assets[i].Path == relPath {
			expected = &manifest.Assets[i]
			break
		}
	}
	if expected == nil {
		return ErrAssetNotFound
	}

	// Verify size.
	if int64(len(data)) != expected.Size {
		return fmt.Errorf("size mismatch: got %d, want %d", len(data), expected.Size)
	}

	// Verify digest.
	digest := DigestBytes(data)
	if digest != expected.Digest {
		return ErrDigestMismatch
	}

	// Check cache capacity.
	cacheKey := packageDigest + "/" + relPath
	oldAsset := s.cache[cacheKey]
	newUsed := s.cacheUsed - oldAsset.Size + int64(len(data))
	if newUsed > s.maxCacheSize {
		return ErrCacheFull
	}

	// Store the asset.
	s.cache[cacheKey] = &CachedAsset{
		Data:     data,
		Digest:   digest,
		Size:     int64(len(data)),
		CachedAt: time.Now(),
	}
	s.cacheUsed = newUsed
	return nil
}

// GetAsset retrieves a cached asset. Returns ErrAssetNotFound if not cached.
func (s *Store) GetAsset(packageDigest, relPath string) (*CachedAsset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Verify the package is still trusted.
	if _, ok := s.manifests[packageDigest]; !ok {
		return nil, ErrPackageNotFound
	}

	cacheKey := packageDigest + "/" + relPath
	asset, ok := s.cache[cacheKey]
	if !ok {
		return nil, ErrAssetNotFound
	}

	return asset, nil
}

// GetManifest returns the manifest for a trusted package.
func (s *Store) GetManifest(packageDigest string) (*PackageManifest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	manifest, ok := s.manifests[packageDigest]
	if !ok {
		return nil, ErrPackageNotFound
	}
	return manifest, nil
}

// CacheStats returns current cache usage statistics.
func (s *Store) CacheStats() (used, max int64, packages, assets int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cacheUsed, s.maxCacheSize, len(s.manifests), len(s.cache)
}

// validateAssetPath checks that a path is a valid relative path without
// traversal or absolute components.
func validateAssetPath(path string) error {
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	if path[0] == '/' || path[0] == '\\' {
		return ErrPathTraversal
	}
	// Check for path traversal.
	for _, part := range splitPath(path) {
		if part == ".." || part == "." {
			return ErrPathTraversal
		}
	}
	return nil
}

// splitPath splits a path into components.
func splitPath(path string) []string {
	var parts []string
	current := ""
	for _, c := range path {
		if c == '/' || c == '\\' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// DigestBytes computes the SHA-256 hex digest of data.
func DigestBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
