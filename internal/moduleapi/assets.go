package moduleapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/wseternal/ssetunnel/sdk"
)

// MaxUIAssetSize bounds one UI asset, matching sdk.MaxAssetSize.
const MaxUIAssetSize = sdk.MaxAssetSize

var (
	// ErrAssetNotDeclared means the module's UI descriptor lists no such asset.
	ErrAssetNotDeclared = errors.New("module asset is not declared")

	// ErrUnsafeAssetPath means the path is absolute, climbs out of the
	// package, or is otherwise not a plain relative path.
	ErrUnsafeAssetPath = errors.New("module asset path is unsafe")

	// ErrAssetSize means the asset does not match its declared size.
	ErrAssetSize = errors.New("module asset size mismatch")

	// ErrAssetDigest means the asset content does not match its digest.
	ErrAssetDigest = errors.New("module asset digest mismatch")
)

// DeclaredAsset is one asset a module's UI descriptor promises, independent of
// which wire type carried the descriptor (SDK manifest or protocol snapshot).
type DeclaredAsset struct {
	Path   string
	Size   int64
	Digest string
}

// Validate checks that a declared asset is well formed and within limits.
func (a DeclaredAsset) Validate() error {
	if _, err := CleanAssetPath(a.Path); err != nil {
		return err
	}
	if a.Size <= 0 || a.Size > MaxUIAssetSize {
		return fmt.Errorf("%w: declared size %d", ErrAssetSize, a.Size)
	}
	if len(a.Digest) != 64 {
		return fmt.Errorf("%w: malformed digest", ErrAssetDigest)
	}
	if _, err := hex.DecodeString(a.Digest); err != nil {
		return fmt.Errorf("%w: digest is not hex", ErrAssetDigest)
	}
	return nil
}

// CleanAssetPath validates a relative asset path and returns its canonical
// form. Both sides use it: a descriptor is rejected when it declares an unsafe
// path, and an untrusted provider response is rejected when it names one, so
// the two cannot disagree about which file the path means.
func CleanAssetPath(p string) (string, error) {
	if p == "" {
		return "", ErrUnsafeAssetPath
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || filepath.IsAbs(p) {
		return "", ErrUnsafeAssetPath
	}
	clean := path.Clean(p)
	if clean != p || clean == "." {
		return "", ErrUnsafeAssetPath
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == ".." || segment == "." || segment == "" {
			return "", ErrUnsafeAssetPath
		}
	}
	return clean, nil
}

// DeclaredAssetsFromSDK flattens SDK UI descriptors into the wire-neutral form.
func DeclaredAssetsFromSDK(descriptors []sdk.UIDescriptor) []DeclaredAsset {
	var out []DeclaredAsset
	for _, desc := range descriptors {
		for _, asset := range desc.AssetPaths {
			out = append(out, DeclaredAsset{Path: asset.Path, Size: asset.Size, Digest: asset.Digest})
		}
	}
	return out
}

// FindDeclaredAsset returns the declared asset for relPath. The comparison is
// on the canonical path, so "a//b" never matches "a/b".
func FindDeclaredAsset(assets []DeclaredAsset, relPath string) (DeclaredAsset, bool) {
	clean, err := CleanAssetPath(relPath)
	if err != nil {
		return DeclaredAsset{}, false
	}
	for _, asset := range assets {
		declared, err := CleanAssetPath(asset.Path)
		if err != nil || declared != clean {
			continue
		}
		return asset, true
	}
	return DeclaredAsset{}, false
}

// VerifyAssetContent checks an in-memory asset (a provider response that
// arrived over the tunnel) against its declaration.
func VerifyAssetContent(content []byte, declared DeclaredAsset) error {
	if err := declared.Validate(); err != nil {
		return err
	}
	if int64(len(content)) != declared.Size {
		return fmt.Errorf("%w: got %d bytes, declared %d", ErrAssetSize, len(content), declared.Size)
	}
	digest := sha256.Sum256(content)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), declared.Digest) {
		return ErrAssetDigest
	}
	return nil
}

// ReadVerifiedAsset reads root/relPath as a plain file inside root, bounded by
// the declared size, and verifies both the size and the SHA-256 digest.
//
// A verified read is the only way to serve a local asset: the file and the
// descriptor can drift (an install overwrote one and not the other). Reading
// one byte past the declared size is what makes "bigger than declared"
// detectable without trusting the file's own length.
func ReadVerifiedAsset(root string, declared DeclaredAsset) ([]byte, error) {
	if err := declared.Validate(); err != nil {
		return nil, err
	}
	clean, err := CleanAssetPath(declared.Path)
	if err != nil {
		return nil, err
	}

	full := filepath.Join(root, filepath.FromSlash(clean))
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: not a plain file", ErrUnsafeAssetPath)
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		// A package may legitimately hold no symlinks at all; a failure here
		// means the file vanished or the link dangles.
		return nil, fmt.Errorf("%w: resolve asset: %v", ErrUnsafeAssetPath, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, ErrUnsafeAssetPath
	}

	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// One byte beyond the declared size is enough to prove a mismatch.
	content, err := io.ReadAll(io.LimitReader(f, declared.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != declared.Size {
		return nil, fmt.Errorf("%w: read %d bytes, declared %d", ErrAssetSize, len(content), declared.Size)
	}
	digest := sha256.Sum256(content)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), declared.Digest) {
		return nil, ErrAssetDigest
	}
	return content, nil
}

// ContentTypeForPath maps a module asset path to its content type. Module
// bundles are served raw, so the type comes from the extension rather than
// from sniffing: an SVG must not be served as octet-stream if the module means
// it as an image, and a .js must not be sniffed into HTML.
func ContentTypeForPath(assetPath string) string {
	switch {
	case strings.HasSuffix(assetPath, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(assetPath, ".js"), strings.HasSuffix(assetPath, ".mjs"):
		return "application/javascript"
	case strings.HasSuffix(assetPath, ".css"):
		return "text/css"
	case strings.HasSuffix(assetPath, ".json"):
		return "application/json"
	case strings.HasSuffix(assetPath, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(assetPath, ".png"):
		return "image/png"
	case strings.HasSuffix(assetPath, ".jpg"), strings.HasSuffix(assetPath, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(assetPath, ".gif"):
		return "image/gif"
	case strings.HasSuffix(assetPath, ".webp"):
		return "image/webp"
	case strings.HasSuffix(assetPath, ".ico"):
		return "image/x-icon"
	case strings.HasSuffix(assetPath, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(assetPath, ".woff"):
		return "font/woff"
	case strings.HasSuffix(assetPath, ".map"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}
