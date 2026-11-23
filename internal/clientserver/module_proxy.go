package clientserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wseternal/ssetunnel/internal/clientapp"
	"github.com/wseternal/ssetunnel/internal/moduleapi"
)

// moduleProxyTransport is dedicated to the loopback module API. It never uses
// an environment proxy (HTTP_PROXY would send the invocation to the network),
// never follows a redirect, and does not negotiate content encoding: the
// browser's own Accept-Encoding is stripped before forwarding, so the body the
// module returns is the body this proxy streams.
var moduleProxyTransport = &http.Transport{
	Proxy:               nil,
	DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	MaxIdleConns:        8,
	MaxIdleConnsPerHost: 8,
	IdleConnTimeout:     60 * time.Second,
	DisableCompression:  true,
}

var moduleProxyClient = &http.Client{
	Transport: moduleProxyTransport,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// handleModuleProxy forwards an authenticated browser request to the agent's
// loopback module API and streams the answer back.
//
// The launch secret is validated by authMiddleware on the way in and stripped
// here: the agent's API has its own boundary and must not receive this
// surface's credential. The status record is re-read on every request so an
// agent restarted by the UI publishes a new port without the browser having to
// re-read anything.
func (s *Server) handleModuleProxy(w http.ResponseWriter, r *http.Request) {
	// The server's whole-request ReadTimeout/WriteTimeout are sized for ordinary
	// API calls. A module exchange gets its own 30-second deadline inside the
	// agent, and a server deadline that starts before the proxy dial would cut a
	// valid late response. Clear both deadlines for this request only; the
	// launch-secret boundary and the module's exchange deadline still bound it.
	clearModuleDeadlines(w)

	target, err := moduleProxyTarget()
	if err != nil {
		log.Printf("api: module proxy unavailable: %v", err)
		writeError(w, http.StatusServiceUnavailable, "agent module API unavailable")
		return
	}

	outReq, err := buildModuleProxyRequest(r.Context(), r, target)
	if err != nil {
		log.Printf("api: module proxy request: %v", err)
		writeError(w, http.StatusBadGateway, "invalid module request")
		return
	}

	resp, err := moduleProxyClient.Do(outReq)
	if err != nil {
		log.Printf("api: module proxy %s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusBadGateway, "agent module API unreachable")
		return
	}
	defer resp.Body.Close()

	moduleapi.CopyResponseHeaders(w.Header(), resp.Header)
	// The surface's headers are already set by securityHeaders; only module
	// headers from the allowlist are added here. Cache-Control stays no-store.
	w.WriteHeader(resp.StatusCode)
	flush(w)

	// Stream with a flush per read so a long module response reaches the
	// browser as it is produced instead of when the buffer fills.
	if err := moduleapi.CopyStream(w, resp.Body); err != nil {
		log.Printf("api: module proxy body: %v", err)
	}
}

// flush flushes the response when the writer supports it. The logging wrapper
// forwards Flush, so this reaches the real connection.
func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// clearModuleDeadlines removes the server-wide read and write deadlines for the
// lifetime of one module proxy request. ResponseController follows the
// statusWriter's Unwrap to the real connection; an unsupported writer is not
// fatal, but it means the server defaults remain in force.
func clearModuleDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		log.Printf("api: module proxy: clear read deadline: %v", err)
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		log.Printf("api: module proxy: clear write deadline: %v", err)
	}
}

// moduleProxyTarget reads the current status record and validates its published
// module API URL. Every failure is fatal for the request: an unset or malformed
// URL means no endpoint, not a guessed one.
func moduleProxyTarget() (*url.URL, error) {
	st := clientapp.ReadExternalAgentStatus()
	if st == nil {
		return nil, errors.New("no live agent status record")
	}
	raw := strings.TrimSpace(st.ModuleAPIURL)
	if raw == "" {
		return nil, errors.New("agent published no module API URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse module API URL: %w", err)
	}
	if u.Scheme != "http" {
		return nil, fmt.Errorf("module API URL scheme %q is not http", u.Scheme)
	}
	if u.User != nil {
		return nil, errors.New("module API URL carries userinfo")
	}
	if u.Port() == "" {
		return nil, errors.New("module API URL has no port")
	}
	host := strings.Trim(u.Hostname(), "[]")
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("module API URL host %q is not loopback", host)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("module API URL must be a bare base URL")
	}
	return u, nil
}

// buildModuleProxyRequest mirrors the incoming request onto the agent's API.
func buildModuleProxyRequest(ctx context.Context, r *http.Request, target *url.URL) (*http.Request, error) {
	method, overridden := moduleapi.ResolveMethod(r.Method, r.Header.Get(moduleapi.MethodOverrideHeader))

	// RequestURI preserves the escaped path and the query exactly as the
	// browser sent them; the agent's API serves the same path shape.
	outReq, err := http.NewRequestWithContext(ctx, method, target.String()+r.URL.RequestURI(), r.Body)
	if err != nil {
		return nil, err
	}
	outReq.ContentLength = r.ContentLength

	for key, values := range r.Header {
		if moduleapi.IsHopByHopHeader(key) || moduleapi.IsCredentialHeader(key) ||
			moduleapi.IsForwardingHeader(key) || moduleapi.IsBrowserMetadataHeader(key) ||
			strings.EqualFold(key, moduleapi.MethodOverrideHeader) || strings.EqualFold(key, "Accept-Encoding") {
			continue
		}
		for _, v := range values {
			outReq.Header.Add(key, v)
		}
	}
	if overridden {
		outReq.Header.Del(moduleapi.MethodOverrideHeader)
	}
	return outReq, nil
}
