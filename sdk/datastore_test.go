package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// fakeIPC is an in-process implementation of the agent's module IPC endpoint.
// It speaks the real wire protocol (AUTH, DATASTORE identity, length-prefixed
// request/response frames) so tests exercise the production framing code.
type fakeIPC struct {
	t        *testing.T
	listener net.Listener
	opts     fakeIPCOptions

	mu       sync.Mutex
	conns    int
	requests int
	// handle receives the decoded request and its 1-based ordinal across
	// all connections. Returning an error drops the connection.
	handle func(conn net.Conn, n int, req dataStoreRequest) error
	wg     sync.WaitGroup
}

// fakeIPCOptions tunes failure injection for one fake endpoint.
type fakeIPCOptions struct {
	// stallAfterConns makes every connection after the given ordinal accept
	// the credential and then stay silent, so the client must bound the
	// handshake itself. Zero never stalls.
	stallAfterConns int
}

func newFakeIPC(t *testing.T, handle func(conn net.Conn, n int, req dataStoreRequest) error) *fakeIPC {
	t.Helper()
	return newFakeIPCWithOptions(t, fakeIPCOptions{}, handle)
}

func newFakeIPCWithOptions(t *testing.T, opts fakeIPCOptions, handle func(conn net.Conn, n int, req dataStoreRequest) error) *fakeIPC {
	t.Helper()
	endpoint := fmt.Sprintf("/tmp/sdk-ds-%s.sock", randomHex(4))
	ln, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeIPC{t: t, listener: ln, opts: opts, handle: handle}
	f.wg.Add(1)
	go f.serve()
	t.Cleanup(func() {
		ln.Close()
		f.wg.Wait()
		os.Remove(endpoint)
	})
	return f
}

func (f *fakeIPC) endpoint() string { return f.listener.Addr().String() }

func (f *fakeIPC) stats() (conns, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns, f.requests
}

func (f *fakeIPC) serve() {
	defer f.wg.Done()
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns++
		connOrdinal := f.conns
		f.mu.Unlock()
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			defer conn.Close()
			br, err := f.handshake(conn, connOrdinal)
			if err != nil {
				return
			}
			for {
				req, err := readDataStoreRequest(br)
				if err != nil {
					return
				}
				f.mu.Lock()
				f.requests++
				n := f.requests
				f.mu.Unlock()
				if err := f.handle(conn, n, req); err != nil {
					return
				}
			}
		}()
	}
}

func (f *fakeIPC) handshake(conn net.Conn, connOrdinal int) (*bufio.Reader, error) {
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var n int
	if _, err := fmt.Sscanf(line, "AUTH %d", &n); err != nil {
		return nil, fmt.Errorf("bad auth header %q: %w", line, err)
	}
	if _, err := io.ReadFull(br, make([]byte, n)); err != nil {
		return nil, err
	}
	if f.opts.stallAfterConns > 0 && connOrdinal > f.opts.stallAfterConns {
		// Stay silent after consuming the credential. Draining blocks until the
		// client gives up, so the test proves the client's own deadline bounds
		// the handshake; the server-side deadline only keeps a regressed client
		// from wedging the test cleanup forever.
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		return nil, fmt.Errorf("client closed a stalled handshake")
	}
	if _, err := conn.Write([]byte("OK\n")); err != nil {
		return nil, err
	}
	// Identity line: "DATASTORE <module_id>\n".
	if _, err := br.ReadString('\n'); err != nil {
		return nil, err
	}
	return br, nil
}

func readDataStoreRequest(br *bufio.Reader) (dataStoreRequest, error) {
	var req dataStoreRequest
	line, err := br.ReadString('\n')
	if err != nil {
		return req, err
	}
	var n int
	if _, err := fmt.Sscanf(line, "DATASTORE %d", &n); err != nil {
		return req, fmt.Errorf("bad request header %q: %w", line, err)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return req, err
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, err
	}
	return req, nil
}

func writeDataStoreResponse(conn net.Conn, resp dataStoreResponse) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := conn.Write([]byte(fmt.Sprintf("DATASTORE_RESP %d\n", len(body)))); err != nil {
		return err
	}
	_, err = conn.Write(body)
	return err
}

// TestDataStore_ReconnectsAfterFrameDesync proves the recovery path: a
// partial response frame poisons the connection, and the following request
// must transparently redial instead of failing forever.
func TestDataStore_ReconnectsAfterFrameDesync(t *testing.T) {
	fake := newFakeIPC(t, func(conn net.Conn, n int, req dataStoreRequest) error {
		if n == 1 {
			// Send a truncated frame, then drop the connection: the client
			// cannot know where the next frame starts.
			conn.Write([]byte("DATASTORE_RESP 9\n{\"rec"))
			return fmt.Errorf("drop after partial frame")
		}
		return writeDataStoreResponse(conn, dataStoreResponse{
			Record: &Record{ModuleID: req.ModuleID, RecordID: req.RecordID, Data: json.RawMessage(`{"ok":true}`)},
		})
	})

	ds, err := NewDataStoreWithEndpoint(fake.endpoint(), "cred", "mod.test")
	if err != nil {
		t.Fatalf("new data store: %v", err)
	}
	defer ds.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := ds.Get(ctx, "mod.test", "rec-1"); err == nil {
		t.Fatal("first get: expected frame error, got nil")
	}

	rec, err := ds.Get(ctx, "mod.test", "rec-1")
	if err != nil {
		t.Fatalf("second get after desync: %v", err)
	}
	if rec.RecordID != "rec-1" {
		t.Fatalf("second get returned %q", rec.RecordID)
	}

	conns, _ := fake.stats()
	if conns != 2 {
		t.Fatalf("expected a redial (2 connections), got %d", conns)
	}
}

// TestDataStore_ServerErrorKeepsConnection proves application-level errors do
// not trigger a reconnect: the frame was consumed in full, so the stream is
// still synchronized.
func TestDataStore_ServerErrorKeepsConnection(t *testing.T) {
	fake := newFakeIPC(t, func(conn net.Conn, n int, req dataStoreRequest) error {
		if n == 1 {
			return writeDataStoreResponse(conn, dataStoreResponse{Error: "boom"})
		}
		return writeDataStoreResponse(conn, dataStoreResponse{
			Record: &Record{ModuleID: req.ModuleID, RecordID: req.RecordID},
		})
	})

	ds, err := NewDataStoreWithEndpoint(fake.endpoint(), "cred", "mod.test")
	if err != nil {
		t.Fatalf("new data store: %v", err)
	}
	defer ds.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := ds.Get(ctx, "mod.test", "rec-1"); err == nil {
		t.Fatal("first get: expected server error, got nil")
	}
	if _, err := ds.Get(ctx, "mod.test", "rec-1"); err != nil {
		t.Fatalf("second get: %v", err)
	}

	conns, _ := fake.stats()
	if conns != 1 {
		t.Fatalf("server error must not redial; connections = %d", conns)
	}
}

// TestDataStore_ClosedDoesNotReconnect proves Close is terminal: no request
// after Close dials again.
func TestDataStore_ClosedDoesNotReconnect(t *testing.T) {
	fake := newFakeIPC(t, func(conn net.Conn, n int, req dataStoreRequest) error {
		return writeDataStoreResponse(conn, dataStoreResponse{})
	})

	ds, err := NewDataStoreWithEndpoint(fake.endpoint(), "cred", "mod.test")
	if err != nil {
		t.Fatalf("new data store: %v", err)
	}
	if err := ds.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ds.Get(ctx, "mod.test", "rec-1"); err == nil {
		t.Fatal("get after close: expected error, got nil")
	}

	conns, _ := fake.stats()
	if conns != 1 {
		t.Fatalf("closed store must not redial; connections = %d", conns)
	}
}

// TestDataStore_ReconnectHandshakeHonoursContextDeadline proves the redial is
// bounded by the caller's context: an agent that accepts the credential and
// then goes silent must not block the request (and ds.mu) past its deadline.
func TestDataStore_ReconnectHandshakeHonoursContextDeadline(t *testing.T) {
	fake := newFakeIPCWithOptions(t, fakeIPCOptions{stallAfterConns: 1},
		func(conn net.Conn, n int, req dataStoreRequest) error {
			// Poison the first connection so the next call must redial.
			conn.Write([]byte("DATASTORE_RESP 9\n{\"rec"))
			return fmt.Errorf("drop after partial frame")
		})

	ds, err := NewDataStoreWithEndpoint(fake.endpoint(), "cred", "mod.test")
	if err != nil {
		t.Fatalf("new data store: %v", err)
	}
	defer ds.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := ds.Get(ctx, "mod.test", "rec-1"); err == nil {
		t.Fatal("first get: expected frame error, got nil")
	}

	// The redial hits the stalled handshake. Run it off the test goroutine so a
	// regression shows up as a failure, not a hung suite.
	stalledCtx, stalledCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stalledCancel()
	done := make(chan error, 1)
	go func() {
		_, err := ds.Get(stalledCtx, "mod.test", "rec-1")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("get against a stalled handshake: expected an error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect ignored the context deadline: the handshake did not return within 2s")
	}
}
