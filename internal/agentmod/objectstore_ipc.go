package agentmod

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
)

// ObjectStore IPC limits. A frame holds the base64 form of a 4 MiB
// object plus bounded metadata. Eight transfer slots cap concurrent
// object bodies per agent.
const (
	maxObjectStoreIPCMessageSize = 6 << 20
	maxObjectStoreIPCTransfers   = 8
)

// handleObjectStoreConnection processes an object store proxy
// connection. After AUTH, the module sends "OBJECTSTORE <module_id>".
// Request frames are "OBJECTSTORE <len>\n<json>". Response frames are
// "OBJECTSTORE_RESP <len>\n<json>".
func (s *IPCServer) handleObjectStoreConnection(conn net.Conn, reader *bufio.Reader, moduleID string) {
	defer conn.Close()

	s.mu.Lock()
	handler := s.onObjectStore
	registered := s.modules[moduleID] != nil
	s.objConns[conn] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.objConns, conn)
		s.mu.Unlock()
	}()

	if handler == nil {
		log.Printf("ipc: object store request received but no handler configured")
		writeObjectStoreResponse(conn, objectStoreErrorJSON(moduleproto.ObjectStoreErrUnavailable, "object store not configured"))
		return
	}
	if !registered {
		log.Printf("ipc: object store connection from unregistered module %q", moduleID)
		writeObjectStoreResponse(conn, objectStoreErrorJSON(moduleproto.ObjectStoreErrInvalidRequest, "module not registered"))
		return
	}

	// Acknowledge the identity before the module sends any request frame.
	// Early rejections above can then be delivered without racing a
	// request body that is already in flight.
	if _, err := conn.Write([]byte("OK\n")); err != nil {
		log.Printf("ipc: object store acknowledgment write error for %s: %v", moduleID, err)
		return
	}

	log.Printf("ipc: object store connection established for module %s", moduleID)

	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			log.Printf("ipc: object store header too long from %s", moduleID)
			return
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("ipc: object store connection closed for %s: %v", moduleID, err)
			}
			return
		}
		header := strings.TrimSuffix(string(line), "\n")
		if !strings.HasPrefix(header, "OBJECTSTORE ") {
			log.Printf("ipc: unexpected command in object store mode from %s: %q", moduleID, header)
			return
		}
		lenStr := strings.TrimPrefix(header, "OBJECTSTORE ")
		dataLen, err := strconv.Atoi(lenStr)
		if err != nil || dataLen < 0 || dataLen > maxObjectStoreIPCMessageSize {
			log.Printf("ipc: invalid OBJECTSTORE length from %s: %q", moduleID, lenStr)
			return
		}

		// Acquire one bounded transfer slot before body allocation. When
		// the slot is unavailable the request body is still in flight, so
		// drain it before answering: closing with unread bytes resets the
		// connection and the module never sees the busy error.
		select {
		case s.objSlots <- struct{}{}:
		default:
			if _, err := io.CopyN(io.Discard, reader, int64(dataLen)); err != nil {
				log.Printf("ipc: object store busy drain error from %s: %v", moduleID, err)
				return
			}
			writeObjectStoreResponse(conn, objectStoreErrorJSON(moduleproto.ObjectStoreErrBusy, "object store is busy"))
			return
		}

		dataBuf := make([]byte, dataLen)
		if dataLen > 0 {
			if _, err := io.ReadFull(reader, dataBuf); err != nil {
				<-s.objSlots
				log.Printf("ipc: object store payload read error from %s: %v", moduleID, err)
				return
			}
		}

		conn.SetDeadline(time.Now().Add(3 * time.Minute))
		respJSON, err := handler(moduleID, dataBuf)
		<-s.objSlots
		if err != nil {
			log.Printf("ipc: object store request failed for %s: %v", moduleID, err)
			writeObjectStoreResponse(conn, objectStoreErrorJSON(moduleproto.ObjectStoreErrInternal, "object store request failed"))
			continue
		}
		writeObjectStoreResponse(conn, respJSON)
	}
}

// writeObjectStoreResponse writes one length-prefixed response frame.
func writeObjectStoreResponse(conn net.Conn, respJSON []byte) {
	if len(respJSON) > maxObjectStoreIPCMessageSize {
		respJSON = objectStoreErrorJSON(moduleproto.ObjectStoreErrInternal, "response too large")
	}
	header := fmt.Sprintf("OBJECTSTORE_RESP %d\n", len(respJSON))
	if _, err := conn.Write([]byte(header)); err != nil {
		log.Printf("ipc: object store response header write error: %v", err)
		return
	}
	if _, err := conn.Write(respJSON); err != nil {
		log.Printf("ipc: object store response body write error: %v", err)
	}
}

// objectStoreErrorJSON builds one structured error response.
func objectStoreErrorJSON(code, message string) []byte {
	b, _ := json.Marshal(moduleproto.NewObjectStoreError("", "", code, message))
	return b
}
