package shardstore_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wseternal/ssetunnel/internal/shardstore"
)

func newTestStore(t *testing.T) (*shardstore.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := shardstore.NewStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		assertNoTemporaryFiles(t, root)
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store, root
}

// assertNoTemporaryFiles fails the test when a Put left a temporary
// file behind.
func assertNoTemporaryFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.Contains(entry.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Errorf("walk %s: %v", root, err)
	}
}

// assertShardLayout walks the real root and fails the test when any
// stored file is not exactly three digest-named components deep. It
// returns the number of populated leaves and object files.
func assertShardLayout(t *testing.T, root string) (leaves, objects int) {
	t.Helper()
	seen := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 {
			t.Errorf("object %q is at depth %d, want 3", rel, len(parts))
			return nil
		}
		if len(parts[0]) != 2 || len(parts[1]) != 2 || len(parts[2]) != 64 {
			t.Errorf("object %q has wrong component lengths", rel)
			return nil
		}
		if !strings.HasPrefix(parts[2], parts[0]+parts[1]) {
			t.Errorf("object %q does not start with its shard prefix", rel)
		}
		seen[parts[0]+"/"+parts[1]] = true
		objects++
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return len(seen), objects
}

func TestStoreLazyShardCreation(t *testing.T) {
	store, root := newTestStore(t)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("root after open has %d entries, want none", len(entries))
	}

	if err := store.Put("lazy-key", []byte("payload")); err != nil {
		t.Fatalf("put: %v", err)
	}

	leaves, objects := assertShardLayout(t, root)
	if leaves != 1 {
		t.Errorf("populated leaves after one write = %d, want 1", leaves)
	}
	if objects != 1 {
		t.Errorf("object count after one write = %d, want 1", objects)
	}

	top, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(top) != 1 || !top[0].IsDir() {
		t.Fatalf("root has %d entries after one write, want one shard directory", len(top))
	}
	info, err := top[0].Info()
	if err != nil {
		t.Fatalf("stat shard directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("shard directory mode = %o, want 700", perm)
	}
}

func TestStoreCRUD(t *testing.T) {
	store, _ := newTestStore(t)

	if err := store.Put("empty", nil); err != nil {
		t.Fatalf("put empty object: %v", err)
	}
	data, err := store.Get("empty")
	if err != nil {
		t.Fatalf("get empty object: %v", err)
	}
	if data == nil {
		t.Error("empty object returned a nil slice, want a non-nil empty slice")
	}
	if len(data) != 0 {
		t.Errorf("empty object returned %d bytes, want 0", len(data))
	}

	binary := []byte{0x00, 0xff, 0x01, 0x80, '\n', 0x00}
	if err := store.Put("binary", binary); err != nil {
		t.Fatalf("put binary object: %v", err)
	}
	data, err = store.Get("binary")
	if err != nil {
		t.Fatalf("get binary object: %v", err)
	}
	if !bytes.Equal(data, binary) {
		t.Errorf("binary object = %x, want %x", data, binary)
	}

	if err := store.Put("replace", []byte("first")); err != nil {
		t.Fatalf("put first payload: %v", err)
	}
	if err := store.Put("replace", []byte("second")); err != nil {
		t.Fatalf("put second payload: %v", err)
	}
	data, err = store.Get("replace")
	if err != nil {
		t.Fatalf("get replaced object: %v", err)
	}
	if string(data) != "second" {
		t.Errorf("replaced object = %q, want %q", data, "second")
	}

	exists, err := store.Exists("binary")
	if err != nil {
		t.Fatalf("exists binary: %v", err)
	}
	if !exists {
		t.Error("exists binary = false, want true")
	}
	exists, err = store.Exists("absent")
	if err != nil {
		t.Fatalf("exists absent: %v", err)
	}
	if exists {
		t.Error("exists absent = true, want false")
	}

	if err := store.Delete("binary"); err != nil {
		t.Fatalf("delete binary: %v", err)
	}
	exists, err = store.Exists("binary")
	if err != nil {
		t.Fatalf("exists deleted object: %v", err)
	}
	if exists {
		t.Error("exists deleted object = true, want false")
	}
	if _, err := store.Get("binary"); !errors.Is(err, shardstore.ErrObjectNotFound) {
		t.Errorf("get deleted object error = %v, want ErrObjectNotFound", err)
	}
	if err := store.Delete("binary"); !errors.Is(err, shardstore.ErrObjectNotFound) {
		t.Errorf("delete deleted object error = %v, want ErrObjectNotFound", err)
	}
	if _, err := store.Get("never-stored"); !errors.Is(err, shardstore.ErrObjectNotFound) {
		t.Errorf("get missing object error = %v, want ErrObjectNotFound", err)
	}
}

func TestStoreKeyIsolation(t *testing.T) {
	store, root := newTestStore(t)
	parent := filepath.Dir(root)

	before, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read parent before writes: %v", err)
	}

	keys := []string{
		"../escape",
		"..",
		".",
		"/leading",
		"trailing/",
		"with space",
		`back\slash`,
		"ключ",
		"a/b/c",
		"dot.dot",
	}
	for _, key := range keys {
		payload := []byte("payload for " + key)
		if err := store.Put(key, payload); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
		data, err := store.Get(key)
		if err != nil {
			t.Fatalf("get %q: %v", key, err)
		}
		if !bytes.Equal(data, payload) {
			t.Errorf("round trip %q = %q, want %q", key, data, payload)
		}
	}

	var emptyErr *shardstore.EmptyKeyError
	if err := store.Put("", []byte("x")); !errors.As(err, &emptyErr) {
		t.Errorf("put empty key error = %v, want *EmptyKeyError", err)
	}
	if _, err := store.Get(""); !errors.As(err, &emptyErr) {
		t.Errorf("get empty key error = %v, want *EmptyKeyError", err)
	}
	if err := store.Delete(""); !errors.As(err, &emptyErr) {
		t.Errorf("delete empty key error = %v, want *EmptyKeyError", err)
	}
	if _, err := store.Exists(""); !errors.As(err, &emptyErr) {
		t.Errorf("exists empty key error = %v, want *EmptyKeyError", err)
	}

	leaves, objects := assertShardLayout(t, root)
	if objects != len(keys) {
		t.Errorf("stored objects = %d, want %d", objects, len(keys))
	}
	if leaves > len(keys) {
		t.Errorf("populated leaves = %d, want at most %d", leaves, len(keys))
	}

	after, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read parent after writes: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("parent directory gained entries: %d before, %d after", len(before), len(after))
	}
	for i := range before {
		if before[i].Name() != after[i].Name() {
			t.Errorf("parent entry %d = %q, want %q", i, after[i].Name(), before[i].Name())
		}
	}
}

func TestStoreRejectsNonRegularObject(t *testing.T) {
	store, root := newTestStore(t)

	const key = "directory-object"
	_, objectPath, err := shardstore.ResolvePath(key)
	if err != nil {
		t.Fatalf("resolve %q: %v", key, err)
	}
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(objectPath)), 0o700); err != nil {
		t.Fatalf("create object directory: %v", err)
	}

	if _, err := store.Get(key); err == nil {
		t.Error("get directory object returned no error")
	}
	if _, err := store.Exists(key); err == nil {
		t.Error("exists directory object returned no error")
	}
	if err := store.Delete(key); err == nil {
		t.Error("delete directory object returned no error")
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(objectPath))); err != nil {
		t.Errorf("delete removed a non-regular object: %v", err)
	}
}

func TestStoreConcurrentSameLeafPut(t *testing.T) {
	store, root := newTestStore(t)

	keys := []string{"same-leaf-19", "same-leaf-91"}
	shardDir, _, err := shardstore.ResolvePath(keys[0])
	if err != nil {
		t.Fatalf("resolve %q: %v", keys[0], err)
	}
	if shardDir != "8c/b3" {
		t.Fatalf("fixed fixture shard = %q, want 8c/b3", shardDir)
	}
	for _, key := range keys[1:] {
		otherShard, _, err := shardstore.ResolvePath(key)
		if err != nil {
			t.Fatalf("resolve %q: %v", key, err)
		}
		if otherShard != shardDir {
			t.Fatalf("fixture key %q resolves to %q, want %q", key, otherShard, shardDir)
		}
	}

	start := make(chan struct{})
	errs := make([]error, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			<-start
			errs[i] = store.Put(key, []byte(key))
		}(i, key)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent put %q: %v", keys[i], err)
		}
	}
	for _, key := range keys {
		data, err := store.Get(key)
		if err != nil {
			t.Fatalf("get %q: %v", key, err)
		}
		if string(data) != key {
			t.Errorf("object %q = %q, want %q", key, data, key)
		}
	}

	leaves, objects := assertShardLayout(t, root)
	if leaves != 1 || objects != len(keys) {
		t.Errorf("layout after same-leaf writes = %d leaves, %d objects, want 1 and %d", leaves, objects, len(keys))
	}
}

func TestStoreConcurrentCRUD(t *testing.T) {
	store, _ := newTestStore(t)

	payloadA := bytes.Repeat([]byte("A"), 32<<10)
	payloadB := bytes.Repeat([]byte("B"), 48<<10)
	const shared = "concurrent-shared"
	if err := store.Put(shared, payloadA); err != nil {
		t.Fatalf("put shared object: %v", err)
	}

	errCh := make(chan error, 64)
	stop := make(chan struct{})

	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := store.Get(shared)
				if err != nil {
					errCh <- fmt.Errorf("get shared object: %w", err)
					return
				}
				if !bytes.Equal(data, payloadA) && !bytes.Equal(data, payloadB) {
					errCh <- fmt.Errorf("reader observed %d bytes matching neither payload", len(data))
					return
				}
			}
		}()
	}

	var writers sync.WaitGroup
	const writerCount = 6
	const opsPerWriter = 10
	for w := range writerCount {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := range opsPerWriter {
				key := fmt.Sprintf("concurrent-%d-%d", w, i)
				payload := []byte(key)
				if err := store.Put(key, payload); err != nil {
					errCh <- fmt.Errorf("put %q: %w", key, err)
					return
				}
				data, err := store.Get(key)
				if err != nil {
					errCh <- fmt.Errorf("get %q: %w", key, err)
					return
				}
				if !bytes.Equal(data, payload) {
					errCh <- fmt.Errorf("get %q = %q, want %q", key, data, payload)
					return
				}
				exists, err := store.Exists(key)
				if err != nil {
					errCh <- fmt.Errorf("exists %q: %w", key, err)
					return
				}
				if !exists {
					errCh <- fmt.Errorf("exists %q = false, want true", key)
					return
				}
				if err := store.Delete(key); err != nil {
					errCh <- fmt.Errorf("delete %q: %w", key, err)
					return
				}
				exists, err = store.Exists(key)
				if err != nil {
					errCh <- fmt.Errorf("exists deleted %q: %w", key, err)
					return
				}
				if exists {
					errCh <- fmt.Errorf("exists deleted %q = true, want false", key)
					return
				}
			}
		}(w)
	}

	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := range 20 {
			payload := payloadB
			if i%2 == 0 {
				payload = payloadA
			}
			if err := store.Put(shared, payload); err != nil {
				errCh <- fmt.Errorf("replace shared object: %w", err)
				return
			}
		}
	}()

	writers.Wait()
	close(stop)
	readers.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestStoreClose(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Put("before-close", []byte("value")); err != nil {
		t.Fatalf("put before close: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	if err := store.Put("after-close", []byte("value")); !errors.Is(err, shardstore.ErrStoreClosed) {
		t.Errorf("put after close error = %v, want ErrStoreClosed", err)
	}
	if _, err := store.Get("before-close"); !errors.Is(err, shardstore.ErrStoreClosed) {
		t.Errorf("get after close error = %v, want ErrStoreClosed", err)
	}
	if _, err := store.Exists("before-close"); !errors.Is(err, shardstore.ErrStoreClosed) {
		t.Errorf("exists after close error = %v, want ErrStoreClosed", err)
	}
	if err := store.Delete("before-close"); !errors.Is(err, shardstore.ErrStoreClosed) {
		t.Errorf("delete after close error = %v, want ErrStoreClosed", err)
	}
}

func TestStoreOpenRejectsInvalidRoot(t *testing.T) {
	if _, err := shardstore.NewStore(""); err == nil {
		t.Error("new store with empty root returned no error")
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := shardstore.NewStore(file); err == nil {
		t.Error("new store on a regular file returned no error")
	}
}
