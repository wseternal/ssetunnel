package agentmod

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
)

// objectStoreIPCServer starts a real IPC listener with one registered
// module and one object-store handler.
func objectStoreIPCServer(t *testing.T, moduleID string, handler func(moduleID string, requestJSON []byte) ([]byte, error)) (*IPCServer, string) {
	t.Helper()
	srv, err := NewIPCServer()
	if err != nil {
		t.Fatalf("new ipc server: %v", err)
	}
	endpoint := fmt.Sprintf("/tmp/test-ipc-os-%s.sock", randomHex(4))
	ln, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv.SetListener(ln, endpoint)
	srv.SetOnObjectStore(handler)
	if err := srv.Register(&Registration{ModuleID: moduleID, ServiceName: "ObjectStoreTest", Version: "1.0.0", Revision: "test"}); err != nil {
		t.Fatalf("register module: %v", err)
	}
	t.Cleanup(func() {
		srv.Close()
		os.Remove(endpoint)
	})
	return srv, endpoint
}

// objectStoreIPCConn performs the AUTH and identity steps. It returns the
// first line after the identity: "OK\n" for a registered module, or a
// response header when the server rejected the identity.
func objectStoreIPCConn(t *testing.T, endpoint, credential, moduleID string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	conn, err := net.DialTimeout("unix", endpoint, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	if _, err := conn.Write([]byte(fmt.Sprintf("AUTH %d\n%s", len(credential), credential))); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if line, err := reader.ReadString('\n'); err != nil || line != "OK\n" {
		t.Fatalf("auth response = %q, %v", line, err)
	}
	if _, err := conn.Write([]byte("OBJECTSTORE " + moduleID + "\n")); err != nil {
		t.Fatalf("write identity: %v", err)
	}
	ack, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read identity response: %v", err)
	}
	return conn, reader, ack
}

// readObjectStoreIPCResponse reads one length-prefixed response frame.
func readObjectStoreIPCResponse(t *testing.T, reader *bufio.Reader) moduleproto.ObjectStoreMessage {
	t.Helper()
	header, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read response header: %v", err)
	}
	return parseObjectStoreIPCResponse(t, reader, header)
}

// parseObjectStoreIPCResponse decodes a frame whose header line is
// already read.
func parseObjectStoreIPCResponse(t *testing.T, reader *bufio.Reader, header string) moduleproto.ObjectStoreMessage {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(header, "OBJECTSTORE_RESP %d", &n); err != nil {
		t.Fatalf("response header %q: %v", header, err)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var resp moduleproto.ObjectStoreMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

// writeObjectStoreIPCRequest writes one length-prefixed request frame.
func writeObjectStoreIPCRequest(t *testing.T, conn net.Conn, requestJSON []byte) {
	t.Helper()
	if _, err := conn.Write([]byte(fmt.Sprintf("OBJECTSTORE %d\n%s", len(requestJSON), requestJSON))); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

// TestObjectStoreIPCRejectsUnregisteredIdentity proves an unregistered
// module receives a framed error on the open connection, not a reset.
func TestObjectStoreIPCRejectsUnregisteredIdentity(t *testing.T) {
	var handlerRan atomic.Bool
	srv, endpoint := objectStoreIPCServer(t, "com.example.store", func(string, []byte) ([]byte, error) {
		handlerRan.Store(true)
		return nil, nil
	})
	_, reader, ack := objectStoreIPCConn(t, endpoint, srv.Credential(), "com.example.ghost")

	resp := parseObjectStoreIPCResponse(t, reader, ack)
	if resp.ErrorCode != moduleproto.ObjectStoreErrInvalidRequest || resp.Error != "module not registered" {
		t.Fatalf("response = %+v, want invalid_request module not registered", resp)
	}
	if handlerRan.Load() {
		t.Fatal("object store handler ran for an unregistered module")
	}
}

// TestObjectStoreIPCBusyDrainsBody proves that a ninth concurrent body
// receives busy after the server drains it, so the module reads the error
// instead of a reset.
func TestObjectStoreIPCBusyDrainsBody(t *testing.T) {
	const moduleID = "com.example.store"
	entered := make(chan struct{}, maxObjectStoreIPCTransfers)
	release := make(chan struct{})
	srv, endpoint := objectStoreIPCServer(t, moduleID, func(string, []byte) ([]byte, error) {
		entered <- struct{}{}
		<-release
		return []byte(`{"request_id":"req-slot","op":"get","chunk_count":1,"final":true}`), nil
	})

	request := []byte(`{"request_id":"req-slot","op":"get","module_id":"` + moduleID + `","key":"blob.bin"}`)
	var heldReaders []*bufio.Reader
	for i := 0; i < maxObjectStoreIPCTransfers; i++ {
		conn, reader, ack := objectStoreIPCConn(t, endpoint, srv.Credential(), moduleID)
		if ack != "OK\n" {
			t.Fatalf("identity response = %q, want OK", ack)
		}
		writeObjectStoreIPCRequest(t, conn, request)
		heldReaders = append(heldReaders, reader)
	}
	for i := 0; i < maxObjectStoreIPCTransfers; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for all transfer slots to fill")
		}
	}

	extra, extraReader, ack := objectStoreIPCConn(t, endpoint, srv.Credential(), moduleID)
	if ack != "OK\n" {
		t.Fatalf("identity response = %q, want OK", ack)
	}
	writeObjectStoreIPCRequest(t, extra, request)
	busy := readObjectStoreIPCResponse(t, extraReader)
	if busy.ErrorCode != moduleproto.ObjectStoreErrBusy {
		t.Fatalf("ninth request error = %q, want %q", busy.ErrorCode, moduleproto.ObjectStoreErrBusy)
	}

	close(release)
	for i, reader := range heldReaders {
		if resp := readObjectStoreIPCResponse(t, reader); resp.ErrorCode != "" {
			t.Fatalf("slot %d response = %+v, want success", i, resp)
		}
	}
}
