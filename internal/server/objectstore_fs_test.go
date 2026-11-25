package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wseternal/ssetunnel/sdk"
)

const fsTestModule = "com.example.store"

func newTestFileObjectStore(t *testing.T) (*FileObjectStore, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileObjectStore(dir)
	if err != nil {
		t.Fatalf("new file object store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dir
}

// TestFileObjectStoreRoundTrip covers the documented object states: a
// found empty object, binary data, overwrite, and a missing object.
func TestFileObjectStoreRoundTrip(t *testing.T) {
	store, _ := newTestFileObjectStore(t)
	ctx := context.Background()

	if err := store.Put(ctx, fsTestModule, "empty.bin", nil); err != nil {
		t.Fatalf("put empty: %v", err)
	}
	got, err := store.Get(ctx, fsTestModule, "empty.bin")
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("get empty = %v, want a non-nil empty slice", got)
	}

	binary := []byte{0x00, 0xff, 0x7f, 0x0a, 0x00}
	if err := store.Put(ctx, fsTestModule, "blob.bin", binary); err != nil {
		t.Fatalf("put binary: %v", err)
	}
	got, err = store.Get(ctx, fsTestModule, "blob.bin")
	if err != nil {
		t.Fatalf("get binary: %v", err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("get binary = %v, want %v", got, binary)
	}

	replacement := []byte("replacement")
	if err := store.Put(ctx, fsTestModule, "blob.bin", replacement); err != nil {
		t.Fatalf("put overwrite: %v", err)
	}
	got, err = store.Get(ctx, fsTestModule, "blob.bin")
	if err != nil {
		t.Fatalf("get overwritten: %v", err)
	}
	if !bytes.Equal(got, replacement) {
		t.Fatalf("get overwritten = %q, want %q", got, replacement)
	}

	if _, err := store.Get(ctx, fsTestModule, "missing.bin"); !errors.Is(err, sdk.ErrObjectNotFound) {
		t.Fatalf("get missing error = %v, want ErrObjectNotFound", err)
	}
}

// TestFileObjectStoreRejectsUnsafeObjects covers symlinks and objects
// above the size limit on both the write and the read path.
func TestFileObjectStoreRejectsUnsafeObjects(t *testing.T) {
	store, dir := newTestFileObjectStore(t)
	ctx := context.Background()
	moduleDir := filepath.Join(dir, fsTestModule)
	if err := os.MkdirAll(moduleDir, 0o700); err != nil {
		t.Fatalf("mkdir module: %v", err)
	}

	target := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(moduleDir, objectFileName("link.bin"))
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := store.Get(ctx, fsTestModule, "link.bin"); err == nil {
		t.Fatal("get symlink: expected an error, got nil")
	}

	if err := store.Put(ctx, fsTestModule, "big.bin", make([]byte, sdk.MaxObjectSize+1)); err == nil {
		t.Fatal("put oversize: expected an error, got nil")
	}

	storedBig := filepath.Join(moduleDir, objectFileName("stored-big.bin"))
	if err := os.WriteFile(storedBig, make([]byte, sdk.MaxObjectSize+1), 0o600); err != nil {
		t.Fatalf("write stored big: %v", err)
	}
	if _, err := store.Get(ctx, fsTestModule, "stored-big.bin"); err == nil {
		t.Fatal("get oversize: expected an error, got nil")
	}
}

// TestFileObjectStoreClosed proves Put and Get fail after Close and that
// a second Close is harmless.
func TestFileObjectStoreClosed(t *testing.T) {
	store, _ := newTestFileObjectStore(t)
	ctx := context.Background()

	if err := store.Put(ctx, fsTestModule, "blob.bin", []byte("x")); err != nil {
		t.Fatalf("put before close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := store.Put(ctx, fsTestModule, "blob.bin", []byte("y")); err == nil {
		t.Fatal("put after close: expected an error, got nil")
	}
	if _, err := store.Get(ctx, fsTestModule, "blob.bin"); err == nil {
		t.Fatal("get after close: expected an error, got nil")
	}
}

// TestFileObjectStoreCloseOrdersOperations exercises concurrent writes
// during Close. Run it with -race: an unsynchronized root field reports a
// data race, and a Close that does not wait for active calls can close
// the root under them.
func TestFileObjectStoreCloseOrdersOperations(t *testing.T) {
	store, _ := newTestFileObjectStore(t)
	ctx := context.Background()

	const writers = 4
	var wg sync.WaitGroup
	var started sync.WaitGroup
	start := make(chan struct{})
	stop := make(chan struct{})
	wg.Add(writers)
	started.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			started.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = store.Put(ctx, fsTestModule, fmt.Sprintf("k-%d-%d.bin", i, n), []byte("x"))
			}
		}(i)
	}
	close(start)
	// Every writer is inside its loop before the close starts, so the
	// close overlaps the next root read. A Close that does not order the
	// root against active calls reports a race here.
	started.Wait()
	_ = store.Close()
	close(stop)
	wg.Wait()
}
