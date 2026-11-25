package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeObjectStoreIPC is an in-process agent IPC endpoint that speaks the
// real wire protocol (AUTH, OBJECTSTORE identity line, length-prefixed
// frames). Tests script the frames the agent sends after the identity
// line.
type fakeObjectStoreIPC struct {
	listener net.Listener
	conns    atomic.Int32
	handle   func(conn net.Conn) error
	wg       sync.WaitGroup
}

func newFakeObjectStoreIPC(t *testing.T, handle func(conn net.Conn) error) *fakeObjectStoreIPC {
	t.Helper()
	endpoint := fmt.Sprintf("/tmp/sdk-os-%s.sock", randomHex(4))
	ln, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeObjectStoreIPC{listener: ln, handle: handle}
	f.wg.Add(1)
	go f.serve()
	t.Cleanup(func() {
		ln.Close()
		f.wg.Wait()
		os.Remove(endpoint)
	})
	return f
}

func (f *fakeObjectStoreIPC) endpoint() string { return f.listener.Addr().String() }

func (f *fakeObjectStoreIPC) serve() {
	defer f.wg.Done()
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.conns.Add(1)
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			defer conn.Close()
			if err := f.handshake(conn); err != nil {
				return
			}
			_ = f.handle(conn)
		}()
	}
}

// handshake consumes AUTH and the OBJECTSTORE identity line, then
// acknowledges the identity.
func (f *fakeObjectStoreIPC) handshake(conn net.Conn) error {
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	var n int
	if _, err := fmt.Sscanf(line, "AUTH %d", &n); err != nil {
		return fmt.Errorf("bad auth header %q: %w", line, err)
	}
	if _, err := io.ReadFull(br, make([]byte, n)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte("OK\n")); err != nil {
		return err
	}
	if _, err := br.ReadString('\n'); err != nil {
		return err
	}
	return nil
}

// TestObjectStore_IdentityRejectionReadsFramedError proves the client
// reads the framed rejection while the connection is still open. The peer
// writes the body only after the header, so a client that closes early
// sees a closed-connection error instead of "unavailable".
func TestObjectStore_IdentityRejectionReadsFramedError(t *testing.T) {
	body, err := json.Marshal(objectStoreResponse{ErrorCode: "unavailable", Error: "object store is disabled"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	fake := newFakeObjectStoreIPC(t, func(conn net.Conn) error {
		if _, err := conn.Write([]byte(fmt.Sprintf("OBJECTSTORE_RESP %d\n", len(body)))); err != nil {
			return err
		}
		half := len(body) / 2
		if _, err := conn.Write(body[:half]); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
		_, err := conn.Write(body[half:])
		return err
	})

	store, err := NewObjectStoreWithEndpoint(fake.endpoint(), "cred", "com.example.mod")
	if err != nil {
		t.Fatalf("new object store: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = store.Put(ctx, "com.example.mod", "blob.bin", []byte("x"))
	if err == nil {
		t.Fatal("put: expected rejection error, got nil")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("put error = %q, want the framed unavailable error", err)
	}
}

// TestObjectStore_RejectsMismatchedModuleID proves a caller cannot
// silently write into a namespace by passing a different module ID.
func TestObjectStore_RejectsMismatchedModuleID(t *testing.T) {
	fake := newFakeObjectStoreIPC(t, func(conn net.Conn) error { return nil })

	store, err := NewObjectStoreWithEndpoint(fake.endpoint(), "cred", "com.example.mod")
	if err != nil {
		t.Fatalf("new object store: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := store.Put(ctx, "com.example.other", "blob.bin", []byte("x")); err == nil {
		t.Fatal("put with a mismatched module ID: expected error, got nil")
	}
	if _, err := store.Get(ctx, "com.example.other", "blob.bin"); err == nil {
		t.Fatal("get with a mismatched module ID: expected error, got nil")
	}
	if conns := fake.conns.Load(); conns != 0 {
		t.Fatalf("mismatched module ID opened %d IPC connections, want 0", conns)
	}
}
