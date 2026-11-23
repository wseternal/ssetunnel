// Package moduleapi holds the request and response policy shared by every
// surface that exposes module capabilities: the console routes, the agent's
// loopback API, and the browser client's authenticated proxy.
//
// It contains only pure transformations. Each caller owns its own transport
// (tunnel stream, module IPC, loopback dial) and its own authentication
// boundary; what they must agree on is how a browser request becomes a
// module request and how a module response becomes a surface response.
package moduleapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	// MethodOverrideHeader carries a browser request for a method that
	// fetch() refuses to send. Only TRACE can be selected with it.
	MethodOverrideHeader = "X-SSET-Method-Override"

	// TraceMethod is the only method the override may select. Browser fetch
	// forbids TRACE (it normalises it to a forbidden method), but the
	// OpenAPI contract can declare it, so the adapters tunnel it over POST.
	TraceMethod = "TRACE"

	// CorrelationHeader carries the invocation correlation ID. It is
	// forwarded to the module and echoed on the response.
	CorrelationHeader = "X-Correlation-ID"
)

// ResolveMethod returns the method a module request must use. A POST that
// carries the TRACE override becomes TRACE; anything else keeps its method.
// The second result reports whether the override applied, so callers can
// decide whether to drop the header before forwarding.
func ResolveMethod(method, override string) (string, bool) {
	if strings.EqualFold(method, http.MethodPost) && strings.EqualFold(strings.TrimSpace(override), TraceMethod) {
		return TraceMethod, true
	}
	return strings.ToUpper(method), false
}

// IsPreflight reports whether the request is a real CORS preflight. An
// OPTIONS request without Access-Control-Request-Method is an ordinary
// request and must reach a module that declares an OPTIONS operation.
func IsPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

// TrailAfterSegments returns the sub-path that follows n path segments after
// prefix, taken from an escaped path. Route variables are URL-decoded by the
// router, so an operation path parameter that legitimately contains an encoded
// slash loses its identity when it is rebuilt from them; the escaped URL keeps
// one segment one segment.
//
// It reports false when the path does not start with prefix.
func TrailAfterSegments(escapedPath, prefix string, n int) (string, bool) {
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", false
	}
	rest := escapedPath[len(prefix):]
	// A '/' never appears inside a percent escape, so splitting the escaped
	// remainder yields exactly the segments the router matched.
	segments := strings.Split(rest, "/")
	if len(segments) <= n {
		return "/", true
	}
	return "/" + strings.Join(segments[n:], "/"), true
}

// BuildRequest turns an incoming surface request into the request sent to
// the module. operationPath is escaped; the caller takes it from TrailAfterSegments
// so encoded characters survive. The incoming query string is preserved verbatim
// because module query parameters are part of the operation contract.
func BuildRequest(ctx context.Context, src *http.Request, method, operationPath string) (*http.Request, error) {
	target := operationPath
	if !strings.HasPrefix(target, "/") {
		target = "/" + target
	}
	if src.URL.RawQuery != "" {
		target += "?" + src.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(ctx, method, target, src.Body)
	if err != nil {
		return nil, err
	}
	// NewRequest cannot know the body length of an opaque reader. Copy it so
	// a module that honours Content-Length reads the same framing the client
	// sent, instead of being handed a chunked body it may not implement.
	req.ContentLength = src.ContentLength

	CopyRequestHeaders(src.Header, req.Header)
	req.Header.Del(MethodOverrideHeader)
	if req.Header.Get(CorrelationHeader) == "" {
		req.Header.Set(CorrelationHeader, NewCorrelationID())
	}
	return req, nil
}

// CopyRequestHeaders copies the headers a module may see. Credentials,
// hop-by-hop headers, and the browser's own Fetch Metadata never cross a
// surface: each surface has its own authentication, and metadata that
// describes the surface must not be presented as if it described the module
// call.
func CopyRequestHeaders(src, dst http.Header) {
	for key, values := range src {
		if IsHopByHopHeader(key) || IsCredentialHeader(key) || IsForwardingHeader(key) || IsBrowserMetadataHeader(key) {
			continue
		}
		// The surface, not the module, decides the wire encoding. Forwarding
		// Accept-Encoding lets a module gzip a body the surface then relays
		// without the header that would decode it.
		if strings.EqualFold(key, "Accept-Encoding") {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}

// IsHopByHopHeader reports headers that belong to one connection and must not
// be forwarded by a proxy.
func IsHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

// IsCredentialHeader reports headers that carry a surface credential.
func IsCredentialHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "cookie", "x-sset-token", "x-sset-launch-secret":
		return true
	}
	return false
}

// IsForwardingHeader reports headers that name an intermediate hop. A module
// that trusted them could be told which proxy called it.
func IsForwardingHeader(key string) bool {
	switch strings.ToLower(key) {
	case "forwarded", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto":
		return true
	}
	return false
}

// IsBrowserMetadataHeader reports Fetch Metadata headers. A local API refuses
// them when they describe a cross-origin caller, and a proxy must not forward
// the browser's own metadata as if it described the module call.
func IsBrowserMetadataHeader(key string) bool {
	switch strings.ToLower(key) {
	case "origin", "referer", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "sec-fetch-user":
		return true
	}
	return false
}

// ResponseAllowlist is the set of module response headers a surface may copy.
//
// cache-control is deliberately absent. Module output on the console and client
// surfaces is per-caller, the URL carries no caller identity, and nothing sets
// Vary, so honouring a module's "public, max-age=86400" would let a shared
// cache answer another caller from the same URL. Each surface keeps its own
// no-store floor instead.
var ResponseAllowlist = map[string]bool{
	"content-type":        true,
	"content-length":      true,
	"content-disposition": true,
	"etag":                true,
	"last-modified":       true,
	"x-request-id":        true,
	// The correlation ID names the exchange that produced the reply. It is
	// generated per request and names no caller, so the client surface must
	// carry it through exactly as the console and agent surfaces do.
	"x-correlation-id": true,
	"location":         true,
	// The catalog epoch names the registry snapshot that answered. It is a
	// version tag, not caller data, so proxying it is safe.
	"x-catalog-epoch": true,
}

// CopyResponseHeader transfers one allowlisted response header onto a surface
// reply. It deletes the key first because multi-valued headers must keep every
// value, which means appending — and appending onto the defaults the wrapper
// pre-declares would ship two values that disagree.
func CopyResponseHeader(dst http.Header, key string, values []string) {
	if strings.EqualFold(key, "Cache-Control") {
		return
	}
	dst.Del(key)
	for _, v := range values {
		dst.Add(key, v)
	}
}

// CopyResponseHeaders copies every allowlisted header from a module response.
func CopyResponseHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if !ResponseAllowlist[strings.ToLower(key)] {
			continue
		}
		CopyResponseHeader(dst, key, values)
	}
}

// WriteJSON writes a JSON response with the content type every module
// surface uses.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// The status line is already sent, so this cannot change the status; log
		// it because a truncated or invalid response is otherwise invisible.
		log.Printf("moduleapi: write JSON: %v", err)
	}
}

// NewCorrelationID creates a random correlation ID for invocations.
func NewCorrelationID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "corr-unavailable"
	}
	return fmt.Sprintf("corr-%x", b[:])
}

// StreamChunkSize is the read size used when relaying a module response body.
// One read is one flush, so a module that writes small pieces incrementally
// reaches the caller incrementally instead of at handler return.
const StreamChunkSize = 32 << 10

// CopyStream relays a module response body to w, flushing after every write
// when the writer supports it.
//
// net/http buffers a response until the handler returns or the write buffer
// fills, so a plain io.Copy delivers a short streamed body in one piece and
// even advertises a Content-Length the module never sent. Reading in chunks
// and flushing after each write preserves the module's own framing, which is
// what streaming module UIs and long-running operations depend on.
func CopyStream(w http.ResponseWriter, body io.Reader) error {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, StreamChunkSize)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
