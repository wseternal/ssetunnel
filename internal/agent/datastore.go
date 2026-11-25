package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// dataStoreTimeout bounds how long a module waits for the server to
// process a data store request before the request is considered failed.
const dataStoreTimeout = 30 * time.Second

// dataStoreForwarder correlates data store requests from modules with
// responses from the server. Modules call Send() which blocks until
// the server replies or the timeout expires.
type dataStoreForwarder struct {
	mu      sync.Mutex
	pending map[string]chan *moduleproto.DataStoreResponsePayload
}

func newDataStoreForwarder() *dataStoreForwarder {
	return &dataStoreForwarder{
		pending: make(map[string]chan *moduleproto.DataStoreResponsePayload),
	}
}

// Send forwards a data store request to the server via the control
// stream and blocks until the response arrives or timeout expires.
func (f *dataStoreForwarder) Send(req moduleproto.DataStoreRequestPayload, sender controlSender) (*moduleproto.DataStoreResponsePayload, error) {
	if req.RequestID == "" {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, fmt.Errorf("generate request ID: %w", err)
		}
		req.RequestID = hex.EncodeToString(buf[:])
	}

	// Register response channel before sending.
	respCh := make(chan *moduleproto.DataStoreResponsePayload, 1)
	f.mu.Lock()
	f.pending[req.RequestID] = respCh
	f.mu.Unlock()

	// Build and send the control message.
	payload, err := json.Marshal(req)
	if err != nil {
		f.mu.Lock()
		delete(f.pending, req.RequestID)
		f.mu.Unlock()
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &mux.ControlMessage{
		Type:     mux.MsgDataStoreReq,
		StreamID: 0,
		Payload:  payload,
	}

	if err := sender(msg); err != nil {
		f.mu.Lock()
		delete(f.pending, req.RequestID)
		f.mu.Unlock()
		return nil, fmt.Errorf("send request: %w", err)
	}

	// Wait for the response with a timeout.
	timer := time.NewTimer(dataStoreTimeout)
	defer timer.Stop()
	select {
	case resp, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("response channel closed")
		}
		if resp.Error != "" {
			return resp, fmt.Errorf("server error: %s", resp.Error)
		}
		return resp, nil
	case <-timer.C:
		f.mu.Lock()
		delete(f.pending, req.RequestID)
		f.mu.Unlock()
		return nil, fmt.Errorf("data store request timed out after %v", dataStoreTimeout)
	}
}

// HandleResponse delivers a data store response from the server to
// the waiting module. Called from dispatchControlMessage.
func (f *dataStoreForwarder) HandleResponse(msg *mux.ControlMessage) {
	var resp moduleproto.DataStoreResponsePayload
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		log.Printf("agent: bad data store response payload: %v", err)
		return
	}

	f.mu.Lock()
	ch, ok := f.pending[resp.RequestID]
	if ok {
		delete(f.pending, resp.RequestID)
	}
	f.mu.Unlock()

	if !ok {
		log.Printf("agent: data store response for unknown request_id=%s", resp.RequestID)
		return
	}
	ch <- &resp
}
