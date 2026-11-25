package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/wseternal/ssetunnel/sdk"
)

// FileObjectStore stores module objects on the server local filesystem.
// Every object lives under <root>/<module-id>/<sha256(key)>. The backend
// confines all access with os.Root, rejects symlinks and non-regular
// objects, and publishes objects with an atomic same-directory rename.
//
// The lock orders every operation against Close: Close waits for active
// calls, and no call starts after the root closes. The backend trusts the
// operator-owned 0700 root. A local writer who can modify the root can
// retarget a module namespace, and Get's Lstat-then-open pair has a
// window. Both cases need local write access that the root mode denies.
type FileObjectStore struct {
	mu   sync.RWMutex
	root *os.Root
}

// NewFileObjectStore opens a storage root. It creates the root and its
// parent directories with private permissions when they do not exist.
// An empty directory, a non-directory path, or an unusable path returns
// an error.
func NewFileObjectStore(dir string) (*FileObjectStore, error) {
	if dir == "" {
		return nil, errors.New("object store directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create object store directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open object store root: %w", err)
	}
	return &FileObjectStore{root: root}, nil
}

// Close releases the storage root after active operations return. The
// backend accepts no calls after Close returns.
func (s *FileObjectStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
	}
	root := s.root
	s.root = nil
	return root.Close()
}

// Put stores data under key in the module namespace. The write goes to
// a private temporary file in the same directory, then a rename makes
// the new object visible. A failed write leaves the previous object in
// place and removes the temporary file.
func (s *FileObjectStore) Put(ctx context.Context, moduleID, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateObjectModuleID(moduleID); err != nil {
		return err
	}
	if err := sdk.ValidateObjectKey(key); err != nil {
		return err
	}
	if len(data) > sdk.MaxObjectSize {
		return fmt.Errorf("object too large: %d bytes (max %d)", len(data), sdk.MaxObjectSize)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	root := s.root
	if root == nil {
		return errors.New("object store is closed")
	}

	if err := root.Mkdir(moduleID, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create module directory: %w", err)
	}

	name := objectFileName(key)
	tmpName := filepath.Join(moduleID, "."+name+".tmp-"+randomSuffix())
	f, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary object: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(tmpName)
		}
	}()

	if len(data) > 0 {
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write object: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync object: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close object: %w", err)
	}

	if err := root.Rename(tmpName, filepath.Join(moduleID, name)); err != nil {
		return fmt.Errorf("publish object: %w", err)
	}
	committed = true
	return nil
}

// Get returns the object stored under key. A missing object returns
// sdk.ErrObjectNotFound. An empty object returns a non-nil empty slice.
func (s *FileObjectStore) Get(ctx context.Context, moduleID, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateObjectModuleID(moduleID); err != nil {
		return nil, err
	}
	if err := sdk.ValidateObjectKey(key); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	root := s.root
	if root == nil {
		return nil, errors.New("object store is closed")
	}

	path := filepath.Join(moduleID, objectFileName(key))
	info, err := root.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, sdk.ErrObjectNotFound
		}
		return nil, fmt.Errorf("stat object: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("object is not a regular file")
	}
	if info.Size() > sdk.MaxObjectSize {
		return nil, fmt.Errorf("stored object is too large: %d bytes (max %d)", info.Size(), sdk.MaxObjectSize)
	}

	f, err := root.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, sdk.ErrObjectNotFound
		}
		return nil, fmt.Errorf("open object: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, sdk.MaxObjectSize+1))
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	if len(data) > sdk.MaxObjectSize {
		return nil, fmt.Errorf("stored object is too large: more than %d bytes", sdk.MaxObjectSize)
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

// validateObjectModuleID checks a module namespace before any path use.
func validateObjectModuleID(moduleID string) error {
	if moduleID == "" {
		return errors.New("module ID is empty")
	}
	if len(moduleID) > sdk.MaxModuleIDLen {
		return fmt.Errorf("module ID too long: %d characters (max %d)", len(moduleID), sdk.MaxModuleIDLen)
	}
	if !sdk.ModuleIDRegexp.MatchString(moduleID) {
		return errors.New("module ID must be a reverse-DNS name")
	}
	return nil
}

// objectFileName returns the physical file name for a logical key. The
// hash keeps platform filename rules out of the layout.
func objectFileName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// randomSuffix returns 8 random hex bytes for a unique temporary name.
func randomSuffix() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failure leaves the temporary name non-unique only if
		// the same process repeats it; O_EXCL still rejects a collision.
		return "fallback"
	}
	return hex.EncodeToString(buf[:])
}

var _ sdk.ObjectStore = (*FileObjectStore)(nil)
