package aiproxy

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/sdk"
)

func TestPrepareFile_CompressedWhenSmaller(t *testing.T) {
	raw := bytes.Repeat([]byte("attachment text "), 8192)
	file := &FilePayload{
		Name:       "notes.txt",
		MimeType:   "text/plain",
		DataBase64: base64.StdEncoding.EncodeToString(raw),
		ByteLength: len(raw),
	}

	stored, ref, err := prepareFile(file)
	if err != nil {
		t.Fatalf("prepareFile: %v", err)
	}
	if ref.Encoding != gzipEncoding {
		t.Fatalf("encoding = %q, want %q", ref.Encoding, gzipEncoding)
	}
	if len(ref.ObjectKey) < 3 || ref.ObjectKey[len(ref.ObjectKey)-3:] != ".gz" {
		t.Errorf("key = %q, want a .gz suffix", ref.ObjectKey)
	}
	if len(stored) >= len(raw) {
		t.Errorf("stored %d bytes, not smaller than %d raw bytes", len(stored), len(raw))
	}
	if ref.ByteLength != len(raw) || ref.DataBase64 != "" {
		t.Errorf("reference = %+v, want raw length and no base64", ref)
	}

	zr, err := gzip.NewReader(bytes.NewReader(stored))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Error("stored gzip bytes do not decode to the original file")
	}
}

func TestPrepareFile_RawWhenGzipDoesNotShrink(t *testing.T) {
	raw := incompressibleBytes(8 << 10)
	file := &FilePayload{
		Name:       "noise.bin",
		MimeType:   "application/octet-stream",
		DataBase64: base64.StdEncoding.EncodeToString(raw),
		ByteLength: len(raw),
	}

	stored, ref, err := prepareFile(file)
	if err != nil {
		t.Fatalf("prepareFile: %v", err)
	}
	if ref.Encoding != "" {
		t.Errorf("encoding = %q, want raw storage", ref.Encoding)
	}
	if !bytes.Equal(stored, raw) {
		t.Error("raw storage changed the bytes")
	}
}

func TestPrepareFile_RejectsInvalidInput(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString([]byte("hello"))

	cases := []struct {
		name string
		file *FilePayload
		want error
	}{
		{
			name: "missing name",
			file: &FilePayload{MimeType: "text/plain", DataBase64: valid, ByteLength: 5},
			want: errInvalidFile,
		},
		{
			name: "missing mime type",
			file: &FilePayload{Name: "a.txt", DataBase64: valid, ByteLength: 5},
			want: errInvalidFile,
		},
		{
			name: "not base64",
			file: &FilePayload{Name: "a.txt", MimeType: "text/plain", DataBase64: "!!!!", ByteLength: 5},
			want: errInvalidFile,
		},
		{
			name: "length mismatch",
			file: &FilePayload{Name: "a.txt", MimeType: "text/plain", DataBase64: valid, ByteLength: 4},
			want: errInvalidFile,
		},
		{
			name: "empty file",
			file: &FilePayload{Name: "a.txt", MimeType: "text/plain", DataBase64: "", ByteLength: 0},
			want: errInvalidFile,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := prepareFile(tc.file); !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPrepareFile_RejectsOversize(t *testing.T) {
	raw := incompressibleBytes(maxFileBytes + 1)
	file := &FilePayload{
		Name:       "big.bin",
		MimeType:   "application/octet-stream",
		DataBase64: base64.StdEncoding.EncodeToString(raw),
		ByteLength: len(raw),
	}
	if _, _, err := prepareFile(file); !errors.Is(err, errFileTooLarge) {
		t.Errorf("error = %v, want %v", err, errFileTooLarge)
	}
}

// TestPrepareFile_RequiresByteLength pins byte_length as a submit requirement,
// matching the published schema and the extension's positive-integer check.
func TestPrepareFile_RequiresByteLength(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	for _, length := range []int{0, -1} {
		file := &FilePayload{
			Name:       "a.txt",
			MimeType:   "text/plain",
			DataBase64: encoded,
			ByteLength: length,
		}
		if _, _, err := prepareFile(file); !errors.Is(err, errInvalidFile) {
			t.Errorf("byte_length %d: error = %v, want %v", length, err, errInvalidFile)
		}
	}
}

func TestFileTimeoutsStayBelowModuleExchange(t *testing.T) {
	if fileOpTimeout >= 30*time.Second {
		t.Errorf("fileOpTimeout = %v, must stay below the 30 s module exchange", fileOpTimeout)
	}
	if hydrateTimeout >= 10*time.Second {
		t.Errorf("hydrateTimeout = %v, must leave room under the extension's 10 s post-window budget", hydrateTimeout)
	}
	// The retry backoff alone must not exhaust the hydrate budget.
	var backoff time.Duration
	for attempt := 1; attempt < maxFileReadAttempts; attempt++ {
		backoff += time.Duration(attempt) * fileRetryDelay
	}
	if backoff >= hydrateTimeout {
		t.Errorf("retry backoff %v does not fit in hydrateTimeout %v", backoff, hydrateTimeout)
	}
}

func TestIsPermanentStoreError(t *testing.T) {
	if !isPermanentStoreError(fmt.Errorf("get object: %w", sdk.ErrObjectNotFound)) {
		t.Error("a wrapped ErrObjectNotFound must be permanent")
	}
	if isPermanentStoreError(errors.New("ipc: connection reset")) {
		t.Error("a transport error must stay retryable")
	}
}

func TestFileObjectKeyIsValid(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, encoding := range []string{"", gzipEncoding} {
		key := fileObjectKey(digest, encoding)
		if err := sdk.ValidateObjectKey(key); err != nil {
			t.Errorf("key %q is rejected by the object store: %v", key, err)
		}
	}
}

// incompressibleBytes returns n bytes derived from a hash chain, so gzip cannot
// shrink them.
func incompressibleBytes(n int) []byte {
	out := make([]byte, 0, n)
	block := sha256.Sum256([]byte("seed"))
	for len(out) < n {
		out = append(out, block[:]...)
		block = sha256.Sum256(block[:])
	}
	return out[:n]
}

// digestHex returns the lowercase hex SHA-256 of data.
func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
