package aiproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/server"
)

const testModuleID = "com.wseternal.ai-automation-proxy"

// newTestManager builds a manager backed by a real filesystem object store.
func newTestManager(t *testing.T) (*JobManager, *server.FileObjectStore) {
	t.Helper()
	store, err := server.NewFileObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("open object store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewJobManager(time.Minute, nil, store, testModuleID), store
}

func textFile(t *testing.T, name string, raw []byte) *FilePayload {
	t.Helper()
	return &FilePayload{
		Name:       name,
		MimeType:   "text/plain",
		DataBase64: base64.StdEncoding.EncodeToString(raw),
		ByteLength: len(raw),
	}
}

// TestSubmitStoresCompressedFile proves the attachment leaves the job record,
// the stored object is gzip-compressed, and Hydrate restores the wire shape.
func TestSubmitStoresCompressedFile(t *testing.T) {
	manager, store := newTestManager(t)
	raw := []byte(strings.Repeat("compressible attachment text\n", 5000))

	job, err := manager.Submit(context.Background(), AutomationRequest{
		Prompt: "summarise",
		File:   textFile(t, "notes.txt", raw),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	ref := job.Request.File
	if ref == nil {
		t.Fatal("job lost the file reference")
	}
	if ref.DataBase64 != "" {
		t.Error("job still holds base64 bytes")
	}
	if ref.ObjectKey == "" || ref.SHA256 != digestHex(raw) || ref.Encoding != gzipEncoding {
		t.Errorf("reference = %+v, want key, digest, and gzip encoding", ref)
	}

	storedBytes, err := store.Get(context.Background(), testModuleID, ref.ObjectKey)
	if err != nil {
		t.Fatalf("read stored object: %v", err)
	}
	if len(storedBytes) >= len(raw) {
		t.Errorf("stored object is %d bytes, not smaller than %d raw bytes", len(storedBytes), len(raw))
	}

	hydrated, err := manager.Hydrate(context.Background(), job.Request)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if hydrated.File.DataBase64 != base64.StdEncoding.EncodeToString(raw) {
		t.Error("hydrated payload does not carry the original bytes")
	}
	if hydrated.File.ByteLength != len(raw) {
		t.Errorf("byte_length = %d, want %d", hydrated.File.ByteLength, len(raw))
	}
}

// TestSubmitWithoutObjectStoreRejectsFile keeps the failure explicit instead of
// falling back to an in-memory blob.
func TestSubmitWithoutObjectStoreRejectsFile(t *testing.T) {
	manager := NewJobManager(time.Minute, nil, nil, testModuleID)
	_, err := manager.Submit(context.Background(), AutomationRequest{
		Prompt: "summarise",
		File:   textFile(t, "a.txt", []byte("hello")),
	})
	if !errors.Is(err, errFileStoreUnavailable) {
		t.Errorf("error = %v, want %v", err, errFileStoreUnavailable)
	}
}

// TestHydrateDetectsCorruptObject proves the digest check rejects bytes that no
// longer match the reference.
func TestHydrateDetectsCorruptObject(t *testing.T) {
	manager, store := newTestManager(t)
	job, err := manager.Submit(context.Background(), AutomationRequest{
		Prompt: "summarise",
		File:   textFile(t, "a.txt", []byte("original bytes")),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := store.Put(context.Background(), testModuleID, job.Request.File.ObjectKey, []byte("replaced")); err != nil {
		t.Fatalf("replace object: %v", err)
	}
	if _, err := manager.Hydrate(context.Background(), job.Request); err == nil {
		t.Error("hydrate accepted an object whose digest does not match")
	}
}

// TestLoadFileRequiresIntegrityFields proves a reference without the digest or
// length is rejected instead of skipping verification.
func TestLoadFileRequiresIntegrityFields(t *testing.T) {
	manager, store := newTestManager(t)
	job, err := manager.Submit(context.Background(), AutomationRequest{
		Prompt: "summarise",
		File:   textFile(t, "a.txt", []byte("original bytes")),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	ref := *job.Request.File

	cases := []struct {
		name   string
		mutate func(*FilePayload)
	}{
		{name: "missing digest", mutate: func(r *FilePayload) { r.SHA256 = "" }},
		{name: "missing length", mutate: func(r *FilePayload) { r.ByteLength = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := ref
			tc.mutate(&candidate)
			if _, err := loadFile(context.Background(), store, testModuleID, &candidate); err == nil {
				t.Error("loadFile accepted a reference without integrity fields")
			}
		})
	}
}

// TestSubmitAndPollRoundTrip drives the client and extension HTTP surfaces with
// the real object store and asserts the extension sees the original payload.
func TestSubmitAndPollRoundTrip(t *testing.T) {
	manager, _ := newTestManager(t)
	raw := []byte(strings.Repeat("round trip payload\n", 400))
	handler := NewClientHandler(manager)
	ext := NewExtensionServer(manager)

	// The client API only accepts a submit while an extension is connected;
	// admission itself is covered by the handler tests.
	closed := manager.PollOpened()
	defer closed()

	body, err := json.Marshal(submitRequest{Prompt: "summarise", File: textFile(t, "a.txt", raw)})
	if err != nil {
		t.Fatalf("marshal submit: %v", err)
	}
	submitRec := httptest.NewRecorder()
	handler.ServeHTTP(submitRec, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(string(body))))
	if submitRec.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", submitRec.Code, submitRec.Body)
	}
	var submitted submitResponse
	if err := json.Unmarshal(submitRec.Body.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}

	pollRec := httptest.NewRecorder()
	ext.server.Handler.ServeHTTP(pollRec, httptest.NewRequest(http.MethodGet, "/poll?timeout=1", nil))
	if pollRec.Code != http.StatusOK {
		t.Fatalf("poll status = %d, want 200", pollRec.Code)
	}
	var polled pollResponse
	if err := json.Unmarshal(pollRec.Body.Bytes(), &polled); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}
	if polled.JobID != submitted.JobID {
		t.Errorf("polled job %q, want %q", polled.JobID, submitted.JobID)
	}
	if polled.Request.File == nil || polled.Request.File.DataBase64 != base64.StdEncoding.EncodeToString(raw) {
		t.Error("extension poll did not receive the original file bytes")
	}
	if polled.Request.File.ObjectKey != "" || polled.Request.File.SHA256 != "" || polled.Request.File.Encoding != "" {
		t.Errorf("poll response leaks reference fields: %+v", polled.Request.File)
	}
}

// TestPollFailsJobWithUnreadableAttachment proves a job whose object cannot be
// verified never reaches the extension.
func TestPollFailsJobWithUnreadableAttachment(t *testing.T) {
	manager, store := newTestManager(t)
	job, err := manager.Submit(context.Background(), AutomationRequest{
		Prompt: "summarise",
		File:   textFile(t, "a.txt", []byte("original bytes")),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := store.Put(context.Background(), testModuleID, job.Request.File.ObjectKey, []byte("different")); err != nil {
		t.Fatalf("replace object: %v", err)
	}

	ext := NewExtensionServer(manager)
	rec := httptest.NewRecorder()
	ext.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/poll?timeout=1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("poll status = %d, want 204 (body %s)", rec.Code, rec.Body)
	}

	failed := manager.Get(job.ID)
	if failed == nil || failed.Status != JobFailed {
		t.Fatalf("job = %+v, want failed", failed)
	}
	if failed.Result == nil || failed.Result.Error == nil || failed.Result.Error.Code != "FILE_UNAVAILABLE" {
		t.Errorf("result = %+v, want FILE_UNAVAILABLE", failed.Result)
	}
	if failed.Result != nil && failed.Result.Error != nil {
		const generic = "file attachment is unavailable; resubmit the job"
		if failed.Result.Error.Message != generic {
			t.Errorf("message = %q, want the generic %q", failed.Result.Error.Message, generic)
		}
		if key := job.Request.File.ObjectKey; key != "" && strings.Contains(failed.Result.Error.Message, key) {
			t.Error("message leaks the object key")
		}
	}
}

// TestSubmitFileStatusMapping pins the status a client sees for each attachment
// failure.
func TestSubmitFileStatusMapping(t *testing.T) {
	oversize := incompressibleBytes(maxFileBytes + 1)
	valid := textFile(t, "a.txt", []byte("hello"))
	invalid := &FilePayload{Name: "a.txt", MimeType: "text/plain", DataBase64: "!!!!", ByteLength: 3}

	cases := []struct {
		name       string
		file       *FilePayload
		store      bool
		wantStatus int
		wantCode   string
	}{
		{name: "invalid base64", file: invalid, store: true, wantStatus: http.StatusBadRequest, wantCode: "INVALID_FILE"},
		{
			name: "oversize",
			file: &FilePayload{
				Name:       "big.bin",
				MimeType:   "application/octet-stream",
				DataBase64: base64.StdEncoding.EncodeToString(oversize),
				ByteLength: len(oversize),
			},
			store:      true,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "FILE_TOO_LARGE",
		},
		{name: "no store", file: valid, store: false, wantStatus: http.StatusServiceUnavailable, wantCode: "FILE_STORE_UNAVAILABLE"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var manager *JobManager
			if tc.store {
				manager, _ = newTestManager(t)
			} else {
				manager = NewJobManager(time.Minute, nil, nil, testModuleID)
			}
			handler := NewClientHandler(manager)

			// Keep a client connected so the submit reaches file validation
			// instead of the admission gate.
			closed := manager.PollOpened()
			defer closed()

			body, err := json.Marshal(submitRequest{Prompt: "summarise", File: tc.file})
			if err != nil {
				t.Fatalf("marshal submit: %v", err)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(string(body))))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
			var resp apiError
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if resp.Error.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", resp.Error.Code, tc.wantCode)
			}
		})
	}
}
