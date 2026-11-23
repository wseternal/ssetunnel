package server

import (
	"context"
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

const (
	// objectStoreQueueDepth bounds queued disk work per session.
	objectStoreQueueDepth = 8

	// objectStoreAssemblyTTL expires incomplete uploads.
	objectStoreAssemblyTTL = 5 * time.Minute

	// objectStoreOperationTimeout bounds one backend call.
	objectStoreOperationTimeout = 30 * time.Second
)

// objectStoreBackend is an immutable shared backend holder. Server
// installs it once before listeners serve; each session captures the
// pointer at attach time.
type objectStoreBackend struct {
	store sdk.ObjectStore
}

// ObjectStoreHandler serves object store requests for one session. The
// control read loop performs decoding, validation, and reassembly only.
// One worker goroutine performs backend disk calls, so a slow disk
// cannot stall unrelated control traffic.
type ObjectStoreHandler struct {
	store sdk.ObjectStore
	send  func(*mux.ControlMessage) error

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	assemblies map[string]*moduleproto.ObjectStoreAssembler
	assemblyAt map[string]time.Time
	closed     bool

	queue chan objectStoreJob
}

// objectStoreJob is one bounded backend operation.
type objectStoreJob struct {
	requestID string
	op        string
	moduleID  string
	key       string
	data      []byte
}

// newObjectStoreHandler starts the per-session disk worker.
func newObjectStoreHandler(store sdk.ObjectStore, send func(*mux.ControlMessage) error) *ObjectStoreHandler {
	ctx, cancel := context.WithCancel(context.Background())
	h := &ObjectStoreHandler{
		store:      store,
		send:       send,
		ctx:        ctx,
		cancel:     cancel,
		assemblies: make(map[string]*moduleproto.ObjectStoreAssembler),
		assemblyAt: make(map[string]time.Time),
		queue:      make(chan objectStoreJob, objectStoreQueueDepth),
	}
	go h.run()
	return h
}

// Close cancels the worker and releases queued work and assemblies.
// Safe to call more than once.
func (h *ObjectStoreHandler) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.assemblies = nil
	h.assemblyAt = nil
	h.mu.Unlock()
	h.cancel()
}

// handleMessage validates and routes one object store request chunk.
// The control read loop calls it, so assembly state is single-threaded.
func (h *ObjectStoreHandler) handleMessage(msg *mux.ControlMessage) {
	var req moduleproto.ObjectStoreMessage
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		log.Printf("server: bad object store request payload: %v", err)
		return
	}
	if req.RequestID == "" {
		log.Printf("server: object store request without request_id")
		return
	}
	if err := validateObjectModuleID(req.ModuleID); err != nil {
		h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrInvalidRequest, "invalid module ID")
		return
	}
	if err := sdk.ValidateObjectKey(req.Key); err != nil {
		h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrInvalidRequest, "invalid object key")
		return
	}

	switch req.Op {
	case moduleproto.ObjectStorePut:
		h.handlePut(&req)
	case moduleproto.ObjectStoreGet:
		h.handleGet(&req)
	default:
		h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrInvalidRequest, "unknown operation")
	}
}

// handlePut reassembles one upload and queues the commit after full
// digest verification.
func (h *ObjectStoreHandler) handlePut(req *moduleproto.ObjectStoreMessage) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrUnavailable, "object store is disabled")
		return
	}
	h.expireAssembliesLocked(time.Now())

	asm, exists := h.assemblies[req.RequestID]
	if !exists {
		if req.Sequence != 0 {
			h.mu.Unlock()
			h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrInvalidRequest, "first object chunk is out of order")
			return
		}
		if len(h.assemblies) >= moduleproto.MaxActiveObjectTransfers {
			h.mu.Unlock()
			h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrBusy, "too many active object transfers")
			return
		}
		var err error
		asm, err = moduleproto.NewObjectStoreAssembler(req)
		if err != nil {
			h.mu.Unlock()
			h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrInvalidRequest, "invalid object transfer")
			return
		}
		h.assemblies[req.RequestID] = asm
		h.assemblyAt[req.RequestID] = time.Now()
	}

	if err := asm.Add(req); err != nil {
		delete(h.assemblies, req.RequestID)
		delete(h.assemblyAt, req.RequestID)
		h.mu.Unlock()
		log.Printf("server: object upload %s rejected: %v", req.RequestID, err)
		h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrInvalidRequest, "invalid object chunk")
		return
	}

	if !asm.Complete() {
		h.mu.Unlock()
		return
	}
	data := asm.Data()
	delete(h.assemblies, req.RequestID)
	delete(h.assemblyAt, req.RequestID)
	h.mu.Unlock()

	if !h.enqueue(objectStoreJob{
		requestID: req.RequestID,
		op:        req.Op,
		moduleID:  req.ModuleID,
		key:       req.Key,
		data:      data,
	}) {
		h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrBusy, "object store is busy")
	}
}

// handleGet validates one read and queues it.
func (h *ObjectStoreHandler) handleGet(req *moduleproto.ObjectStoreMessage) {
	if !h.enqueue(objectStoreJob{
		requestID: req.RequestID,
		op:        req.Op,
		moduleID:  req.ModuleID,
		key:       req.Key,
	}) {
		h.fail(req.RequestID, req.Op, moduleproto.ObjectStoreErrBusy, "object store is busy")
	}
}

// enqueue submits one bounded job without growing the queue.
func (h *ObjectStoreHandler) enqueue(job objectStoreJob) bool {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return false
	}
	select {
	case h.queue <- job:
		return true
	default:
		return false
	}
}

// expireAssembliesLocked drops incomplete uploads past their TTL.
func (h *ObjectStoreHandler) expireAssembliesLocked(now time.Time) {
	for id, at := range h.assemblyAt {
		if now.Sub(at) > objectStoreAssemblyTTL {
			delete(h.assemblies, id)
			delete(h.assemblyAt, id)
		}
	}
}

// run executes queued backend operations until Close cancels it.
func (h *ObjectStoreHandler) run() {
	for {
		select {
		case job := <-h.queue:
			h.execute(job)
		case <-h.ctx.Done():
			return
		}
	}
}

// execute performs one backend call and sends its response.
func (h *ObjectStoreHandler) execute(job objectStoreJob) {
	ctx, cancel := context.WithTimeout(h.ctx, objectStoreOperationTimeout)
	defer cancel()

	switch job.op {
	case moduleproto.ObjectStorePut:
		if err := h.store.Put(ctx, job.moduleID, job.key, job.data); err != nil {
			log.Printf("server: object store put %s failed: %v", job.requestID, err)
			h.fail(job.requestID, job.op, moduleproto.ObjectStoreErrInternal, "object store write failed")
			return
		}
		_ = h.sendControl(&moduleproto.ObjectStoreMessage{
			RequestID:  job.requestID,
			Op:         job.op,
			ModuleID:   job.moduleID,
			Key:        job.key,
			Sequence:   0,
			ChunkCount: 1,
			Final:      true,
			TotalSize:  int64(len(job.data)),
			SHA256:     moduleproto.ObjectStoreDigest(job.data),
		})
	case moduleproto.ObjectStoreGet:
		data, err := h.store.Get(ctx, job.moduleID, job.key)
		if err != nil {
			if errors.Is(err, sdk.ErrObjectNotFound) {
				h.fail(job.requestID, job.op, moduleproto.ObjectStoreErrNotFound, "object not found")
				return
			}
			log.Printf("server: object store get %s failed: %v", job.requestID, err)
			h.fail(job.requestID, job.op, moduleproto.ObjectStoreErrInternal, "object store read failed")
			return
		}
		chunks, err := moduleproto.NewObjectStoreDataChunks(job.requestID, job.op, job.moduleID, job.key, data)
		if err != nil {
			h.fail(job.requestID, job.op, moduleproto.ObjectStoreErrTooLarge, "object is too large")
			return
		}
		for _, chunk := range chunks {
			if err := h.sendControl(chunk); err != nil {
				log.Printf("server: object store response %s stopped: %v", job.requestID, err)
				return
			}
		}
	}
}

// fail sends one structured terminal error.
func (h *ObjectStoreHandler) fail(requestID, op, code, message string) {
	_ = h.sendControl(moduleproto.NewObjectStoreError(requestID, op, code, message))
}

// sendControl writes one object store response frame.
func (h *ObjectStoreHandler) sendControl(msg *moduleproto.ObjectStoreMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal object store response: %w", err)
	}
	return h.send(&mux.ControlMessage{
		Type:     moduleproto.MsgObjectStoreResp,
		StreamID: 0,
		Payload:  payload,
	})
}

// objectStoreUnavailableResponse answers a request when storage is
// disabled, so the module sees a correlated error instead of a timeout.
func objectStoreUnavailableResponse(msg *mux.ControlMessage) *mux.ControlMessage {
	var req moduleproto.ObjectStoreMessage
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		return nil
	}
	payload, err := json.Marshal(moduleproto.NewObjectStoreError(
		req.RequestID, req.Op, moduleproto.ObjectStoreErrUnavailable, "object store is disabled"))
	if err != nil {
		return nil
	}
	return &mux.ControlMessage{
		Type:     moduleproto.MsgObjectStoreResp,
		StreamID: 0,
		Payload:  payload,
	}
}
