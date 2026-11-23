package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/sdk"
)

// objectStoreTimeout bounds one complete object operation when the
// caller supplies no deadline.
const objectStoreTimeout = 2 * time.Minute

// errObjectStoreClosed reports a forwarder that an Agent.Close call
// stopped.
var errObjectStoreClosed = errors.New("agent: object store forwarder is closed")

// objectStorePending is one in-flight object operation.
type objectStorePending struct {
	requestID string
	gen       int
	op        string
	moduleID  string
	key       string
	done      chan struct{}
	finished  bool
	asm       *moduleproto.ObjectStoreAssembler
	resp      *moduleproto.ObjectStoreMessage
	err       error
}

// objectStoreForwarder correlates object requests with server
// response chunks. It also enforces the bound and the control stream
// generation of every pending operation.
type objectStoreForwarder struct {
	mu       sync.Mutex
	pending  map[string]*objectStorePending
	closed   bool
	closedCh chan struct{}
}

func newObjectStoreForwarder() *objectStoreForwarder {
	return &objectStoreForwarder{
		pending:  make(map[string]*objectStorePending),
		closedCh: make(chan struct{}),
	}
}

// Forward sends one object request and waits for its terminal response.
// For a get, the returned message carries the reassembled data.
func (f *objectStoreForwarder) Forward(ctx context.Context, msg moduleproto.ObjectStoreMessage, sender controlSender, gen int) (*moduleproto.ObjectStoreMessage, error) {
	if err := sdk.ValidateObjectKey(msg.Key); err != nil {
		return nil, err
	}
	if msg.Op != moduleproto.ObjectStorePut && msg.Op != moduleproto.ObjectStoreGet {
		return nil, fmt.Errorf("object store: unknown operation %q", msg.Op)
	}
	if msg.RequestID == "" {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, fmt.Errorf("object store: generate request ID: %w", err)
		}
		msg.RequestID = hex.EncodeToString(buf[:])
	}

	p := &objectStorePending{
		requestID: msg.RequestID,
		gen:       gen,
		op:        msg.Op,
		moduleID:  msg.ModuleID,
		key:       msg.Key,
		done:      make(chan struct{}),
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, errObjectStoreClosed
	}
	f.pending[p.requestID] = p
	f.mu.Unlock()

	switch msg.Op {
	case moduleproto.ObjectStorePut:
		chunks, err := moduleproto.NewObjectStoreDataChunks(msg.RequestID, msg.Op, msg.ModuleID, msg.Key, msg.Data)
		if err != nil {
			f.finish(p, nil, err)
			return nil, err
		}
		for _, chunk := range chunks {
			if err := sendObjectStoreChunk(chunk, sender); err != nil {
				f.finish(p, nil, err)
				return nil, err
			}
		}
	case moduleproto.ObjectStoreGet:
		req := moduleproto.NewObjectStoreGet(msg.RequestID, msg.ModuleID, msg.Key)
		if err := sendObjectStoreChunk(req, sender); err != nil {
			f.finish(p, nil, err)
			return nil, err
		}
	}

	timer := time.NewTimer(objectStoreTimeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.resp, p.err
	case <-timer.C:
		err := fmt.Errorf("object store: request timed out after %v", objectStoreTimeout)
		f.finish(p, nil, err)
		return nil, err
	case <-ctx.Done():
		err := ctx.Err()
		f.finish(p, nil, err)
		return nil, err
	case <-f.closedCh:
		return nil, errObjectStoreClosed
	}
}

// HandleResponse delivers one response chunk to its waiting operation.
// A response from a stale control generation is discarded.
func (f *objectStoreForwarder) HandleResponse(msg *mux.ControlMessage, gen int) {
	var resp moduleproto.ObjectStoreMessage
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		log.Printf("agent: bad object store response payload: %v", err)
		return
	}

	f.mu.Lock()
	p := f.pending[resp.RequestID]
	if p != nil && p.gen != gen {
		p = nil
	}
	f.mu.Unlock()

	if p == nil {
		log.Printf("agent: object store response for unknown request_id=%s", resp.RequestID)
		return
	}

	if resp.ErrorCode != "" {
		f.finish(p, &resp, fmt.Errorf("object store: %s", resp.ErrorCode))
		return
	}

	switch resp.Op {
	case moduleproto.ObjectStorePut:
		if !resp.Final {
			f.finish(p, nil, errors.New("object store: put response is not final"))
			return
		}
		f.finish(p, &resp, nil)
	case moduleproto.ObjectStoreGet:
		if p.asm == nil {
			asm, err := moduleproto.NewObjectStoreAssembler(&resp)
			if err != nil {
				f.finish(p, nil, err)
				return
			}
			p.asm = asm
		}
		if err := p.asm.Add(&resp); err != nil {
			f.finish(p, nil, err)
			return
		}
		if p.asm.Complete() {
			f.finish(p, &moduleproto.ObjectStoreMessage{
				RequestID:  resp.RequestID,
				Op:         moduleproto.ObjectStoreGet,
				ModuleID:   p.moduleID,
				Key:        p.key,
				Sequence:   0,
				ChunkCount: 1,
				Final:      true,
				TotalSize:  int64(len(p.asm.Data())),
				SHA256:     resp.SHA256,
				Data:       p.asm.Data(),
			}, nil)
		}
	default:
		f.finish(p, nil, fmt.Errorf("object store: unknown response operation %q", resp.Op))
	}
}

// FailGeneration fails every pending operation that belongs to the
// given control stream generation.
func (f *objectStoreForwarder) FailGeneration(gen int, err error) {
	f.mu.Lock()
	var list []*objectStorePending
	for _, p := range f.pending {
		if p.gen == gen {
			list = append(list, p)
		}
	}
	f.mu.Unlock()
	for _, p := range list {
		f.finish(p, nil, err)
	}
}

// FailAll fails every pending operation and rejects new ones. Agent
// Close calls it before the module IPC server closes.
func (f *objectStoreForwarder) FailAll(err error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	close(f.closedCh)
	var list []*objectStorePending
	for _, p := range f.pending {
		list = append(list, p)
	}
	f.mu.Unlock()
	for _, p := range list {
		f.finish(p, nil, err)
	}
}

// finish removes one pending entry and publishes its terminal result.
func (f *objectStoreForwarder) finish(p *objectStorePending, resp *moduleproto.ObjectStoreMessage, err error) {
	f.mu.Lock()
	if p.finished {
		f.mu.Unlock()
		return
	}
	p.finished = true
	delete(f.pending, p.requestID)
	p.resp = resp
	p.err = err
	f.mu.Unlock()
	close(p.done)
}

// sendObjectStoreChunk writes one control frame. The control sender
// logs write failures; the operation then fails by timeout or by
// generation cleanup.
func sendObjectStoreChunk(msg *moduleproto.ObjectStoreMessage, sender controlSender) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("object store: marshal chunk: %w", err)
	}
	sender(&mux.ControlMessage{
		Type:     moduleproto.MsgObjectStoreReq,
		StreamID: 0,
		Payload:  payload,
	})
	return nil
}
