package aiproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/wseternal/ssetunnel/sdk"
)

// maxFileBytes caps one attachment at the object store's object size. The
// limit applies to the decoded file, so compression never enlarges the
// accepted input.
const maxFileBytes = sdk.MaxObjectSize

// gzipEncoding marks an object whose bytes are gzip-compressed.
const gzipEncoding = "gzip"

// fileOpTimeout bounds one object store round trip. A submit runs inside the
// agent's 30-second module exchange, so its put must finish inside that window.
const fileOpTimeout = 20 * time.Second

// hydrateTimeout bounds the delivery path: one object read, the integrity
// checks, and the response write. The extension aborts its poll fetch 10
// seconds after the advertised window, so hydration keeps a budget of its own
// instead of reusing the submit-side timeout.
const hydrateTimeout = 5 * time.Second

// maxFileReadAttempts bounds how many times one delivery attempt reads the
// object. A missing object is permanent; transport errors are worth another try.
const maxFileReadAttempts = 3

// fileRetryDelay is the pause before the second read attempt. Each further
// attempt waits one more delay.
const fileRetryDelay = 500 * time.Millisecond

// Typed attachment failures. The client handler maps each to one status.
var (
	errInvalidFile          = errors.New("invalid file payload")
	errFileTooLarge         = errors.New("file too large")
	errFileStoreUnavailable = errors.New("file store unavailable")
)

// fileObjectKey returns the object key for the file bytes. The key carries the
// content digest, so identical attachments share one object, and the suffix
// names the encoding so a reader never guesses.
func fileObjectKey(digest, encoding string) string {
	if encoding == gzipEncoding {
		return "file-" + digest + ".gz"
	}
	return "file-" + digest
}

// prepareFile decodes and validates a submitted attachment, compresses it when
// that shrinks it, and returns the bytes to store plus the reference the job
// keeps. The reference carries no base64 payload.
func prepareFile(file *FilePayload) (data []byte, ref *FilePayload, err error) {
	if file.Name == "" {
		return nil, nil, fmt.Errorf("%w: name is required", errInvalidFile)
	}
	if file.MimeType == "" {
		return nil, nil, fmt.Errorf("%w: mime_type is required", errInvalidFile)
	}
	raw, decodeErr := base64.StdEncoding.DecodeString(file.DataBase64)
	if decodeErr != nil {
		return nil, nil, fmt.Errorf("%w: data_base64 is not valid base64", errInvalidFile)
	}
	if len(raw) == 0 {
		// The extension requires a positive byte length, so an empty file would
		// only store an object that no job can use.
		return nil, nil, fmt.Errorf("%w: file is empty", errInvalidFile)
	}
	if len(raw) > maxFileBytes {
		return nil, nil, fmt.Errorf("%w: %d bytes (max %d)", errFileTooLarge, len(raw), maxFileBytes)
	}
	if file.ByteLength <= 0 {
		return nil, nil, fmt.Errorf("%w: byte_length must be a positive integer", errInvalidFile)
	}
	if file.ByteLength != len(raw) {
		return nil, nil, fmt.Errorf("%w: byte_length %d does not match %d decoded bytes",
			errInvalidFile, file.ByteLength, len(raw))
	}

	digest := sha256.Sum256(raw)
	digestHex := hex.EncodeToString(digest[:])

	stored, encoding := raw, ""
	if compressed, gzipErr := gzipBytes(raw); gzipErr == nil && len(compressed) < len(raw) {
		stored, encoding = compressed, gzipEncoding
	}

	return stored, &FilePayload{
		Name:       file.Name,
		MimeType:   file.MimeType,
		ByteLength: len(raw),
		ObjectKey:  fileObjectKey(digestHex, encoding),
		SHA256:     digestHex,
		Encoding:   encoding,
	}, nil
}

// gzipBytes compresses data with the default gzip level. The caller decides
// whether the result is worth storing.
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

// loadFile reads the stored bytes back, decompresses them when needed, and
// restores the wire shape the extension expects. The bytes are verified
// against the digest and length recorded at submit time.
func loadFile(ctx context.Context, store sdk.ObjectStore, moduleID string, ref *FilePayload) (*FilePayload, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: object store is not configured", errFileStoreUnavailable)
	}
	if ref.ObjectKey == "" {
		return nil, fmt.Errorf("%w: missing object key", errInvalidFile)
	}
	if ref.SHA256 == "" || ref.ByteLength <= 0 {
		// Every reference comes from prepareFile, which sets both fields. A
		// reference without them is corrupt, and skipping the checks would
		// weaken the verified-before-use guarantee.
		return nil, fmt.Errorf("file object %s is missing its digest or length", ref.ObjectKey)
	}
	data, err := readObject(ctx, store, moduleID, ref.ObjectKey)
	if err != nil {
		return nil, err
	}

	if ref.Encoding == gzipEncoding {
		data, err = gunzipBytes(data)
		if err != nil {
			return nil, fmt.Errorf("decompress file object %s: %w", ref.ObjectKey, err)
		}
	} else if ref.Encoding != "" {
		return nil, fmt.Errorf("unknown file encoding %q", ref.Encoding)
	}

	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, fmt.Errorf("file object %s digest mismatch", ref.ObjectKey)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%w: stored file is %d bytes (max %d)", errFileTooLarge, len(data), maxFileBytes)
	}
	if ref.ByteLength != len(data) {
		return nil, fmt.Errorf("file object %s length mismatch: got %d, want %d",
			ref.ObjectKey, len(data), ref.ByteLength)
	}

	return &FilePayload{
		Name:       ref.Name,
		MimeType:   ref.MimeType,
		DataBase64: base64.StdEncoding.EncodeToString(data),
		ByteLength: len(data),
	}, nil
}

// gunzipBytes decompresses one object. The reader is bounded one byte past the
// file limit, so a corrupt or hostile object cannot expand without bound.
func gunzipBytes(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer zr.Close()

	raw, err := io.ReadAll(io.LimitReader(zr, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gzip read: %w", err)
	}
	if len(raw) > maxFileBytes {
		return nil, fmt.Errorf("%w: decompressed file exceeds %d bytes", errFileTooLarge, maxFileBytes)
	}
	return raw, nil
}

// readObject reads one object, retrying transport failures a bounded number of
// times. A missing object is permanent: no retry can bring it back.
func readObject(ctx context.Context, store sdk.ObjectStore, moduleID, key string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxFileReadAttempts; attempt++ {
		data, err := store.Get(ctx, moduleID, key)
		if err == nil {
			return data, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if isPermanentStoreError(err) {
			return nil, fmt.Errorf("read file object %s: %w", key, err)
		}
		lastErr = err
		if attempt == maxFileReadAttempts {
			break
		}
		log.Printf("aiproxy: read file object %s attempt %d/%d: %v", key, attempt, maxFileReadAttempts, err)
		select {
		case <-time.After(time.Duration(attempt) * fileRetryDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("read file object %s after %d attempts: %w", key, maxFileReadAttempts, lastErr)
}

// isPermanentStoreError reports whether a store error cannot be fixed by
// retrying. Only a missing object qualifies; transport and timeout errors are
// worth another attempt.
func isPermanentStoreError(err error) bool {
	return errors.Is(err, sdk.ErrObjectNotFound)
}
