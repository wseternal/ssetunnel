package shardstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// ErrObjectNotFound reports a key with no stored object. Get and Delete
// return it through errors.Is.
var ErrObjectNotFound = errors.New("shardstore: object not found")

// ErrStoreClosed reports an operation on a closed store. Every method
// returns it through errors.Is after Close.
var ErrStoreClosed = errors.New("shardstore: store is closed")

// Store stores byte objects under a shard tree on a local filesystem.
// One os.Root confines every call to the configured root. The store
// creates shard directories only during a write. The store keeps no
// object index, so each call costs one directory lookup and one file
// operation.
//
// A read lock orders every call against Close. Close waits for active
// calls, and no call starts after the root closes.
type Store struct {
	mu   sync.RWMutex
	root *os.Root
}

// NewStore opens a storage root. It creates the root and its parent
// directories with mode 0700 when they do not exist. It creates no
// shard directory. An empty path, a non-directory path, or a path that
// the process cannot open returns an error.
func NewStore(rootDir string) (*Store, error) {
	if rootDir == "" {
		return nil, errors.New("shardstore: root directory is empty")
	}
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return nil, fmt.Errorf("shardstore: create root directory: %w", err)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, fmt.Errorf("shardstore: open root: %w", err)
	}
	return &Store{root: root}, nil
}

// Close releases the storage root after active calls return. Close is
// idempotent. Every call after Close returns ErrStoreClosed.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
	}
	root := s.root
	s.root = nil
	return root.Close()
}

// Put stores data under key. The store maps the key to its digest path
// and creates the selected leaf directory when it does not exist. Put
// writes a private temporary file in the same leaf, syncs and closes it,
// then renames it to the digest name. A reader sees the old object or the
// new object, never a partial write. A failed write removes the
// temporary file and keeps the previous object. Put has no size limit.
func (s *Store) Put(key string, data []byte) error {
	shardDir, name, err := resolveKey(key)
	if err != nil {
		return err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	root, err := s.openRoot()
	if err != nil {
		return err
	}

	if err := root.MkdirAll(shardRelativePath(shardDir), 0o700); err != nil {
		return fmt.Errorf("shardstore: create shard directory: %w", err)
	}

	tmpPath, file, err := createTemporaryObject(root, shardDir, name)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(tmpPath)
		}
	}()

	if len(data) > 0 {
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return fmt.Errorf("shardstore: write object: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("shardstore: sync object: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("shardstore: close object: %w", err)
	}

	if err := root.Rename(tmpPath, objectRelativePath(shardDir, name)); err != nil {
		return fmt.Errorf("shardstore: publish object: %w", err)
	}
	committed = true
	return nil
}

// Get returns the object stored under key. An empty object returns a
// non-nil empty slice. A missing object returns ErrObjectNotFound through
// errors.Is. Get rejects a symlink or any other non-regular entry.
func (s *Store) Get(key string) ([]byte, error) {
	shardDir, name, err := resolveKey(key)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}

	path := objectRelativePath(shardDir, name)
	info, err := root.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("shardstore: stat object: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("shardstore: object is not a regular file: %s", path)
	}

	file, err := root.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("shardstore: open object: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("shardstore: read object: %w", err)
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

// Exists reports whether key has a stored object. A missing object
// returns false and no error. Exists rejects a symlink or any other
// non-regular entry.
func (s *Store) Exists(key string) (bool, error) {
	shardDir, name, err := resolveKey(key)
	if err != nil {
		return false, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	root, err := s.openRoot()
	if err != nil {
		return false, err
	}

	path := objectRelativePath(shardDir, name)
	info, err := root.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("shardstore: stat object: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("shardstore: object is not a regular file: %s", path)
	}
	return true, nil
}

// Delete removes the object stored under key. It checks the regular-file
// contract before removal. A missing object returns ErrObjectNotFound
// through errors.Is.
func (s *Store) Delete(key string) error {
	shardDir, name, err := resolveKey(key)
	if err != nil {
		return err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	root, err := s.openRoot()
	if err != nil {
		return err
	}

	path := objectRelativePath(shardDir, name)
	info, err := root.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrObjectNotFound
		}
		return fmt.Errorf("shardstore: stat object: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("shardstore: object is not a regular file: %s", path)
	}
	if err := root.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrObjectNotFound
		}
		return fmt.Errorf("shardstore: remove object: %w", err)
	}
	return nil
}

// openRoot returns the retained root. The caller holds at least a read
// lock, so the root stays open for the full call.
func (s *Store) openRoot() (*os.Root, error) {
	if s.root == nil {
		return nil, ErrStoreClosed
	}
	return s.root, nil
}

// createTemporaryObject makes a unique O_EXCL temporary file in the
// selected leaf. The name never uses a caller key.
func createTemporaryObject(root *os.Root, shardDir, name string) (string, *os.File, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", nil, fmt.Errorf("shardstore: create temporary suffix: %w", err)
	}
	path := shardRelativePath(shardDir) + string(filepath.Separator) + "." + name + ".tmp-" + hex.EncodeToString(suffix[:])
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("shardstore: create temporary object: %w", err)
	}
	return path, file, nil
}

// shardRelativePath converts a slash-separated shard directory to an
// operating-system path.
func shardRelativePath(shardDir string) string {
	return filepath.FromSlash(shardDir)
}

// objectRelativePath converts a slash-separated object path to an
// operating-system path.
func objectRelativePath(shardDir, name string) string {
	return shardRelativePath(shardDir) + string(filepath.Separator) + name
}
