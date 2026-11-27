package clientapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/moduleapi"
)

const (
	// consoleJSONLimit bounds a decoded console response. The console caps the
	// OpenAPI document at 1 MiB, and the derived operation models can be a few
	// times larger, so 32 MiB leaves room without allowing an unbounded read.
	consoleJSONLimit = 32 << 20

	// consoleErrorBodyLimit bounds the server text copied into an error. The
	// text is diagnostic, not a payload.
	consoleErrorBodyLimit = 8 << 10
)

// consoleHTTPClient serves every console request. The response header timeout
// bounds a stalled server, while the absent total timeout lets a module
// response stream to stdout. Redirects are refused so a bearer token never
// follows a Location header to another host.
var consoleHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// ConsoleClient reads the console module surface with a saved user session.
// It is the client counterpart of internal/consoleserver/module_routes.go.
type ConsoleClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// ConsoleAgent is one live agent row from the console.
type ConsoleAgent struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
}

// ConsoleModule is one module row from the console catalog.
type ConsoleModule struct {
	ModuleID       string `json:"module_id"`
	ServiceName    string `json:"service_name,omitempty"`
	ServiceVersion string `json:"service_version,omitempty"`
	Revision       string `json:"revision,omitempty"`
	Health         string `json:"health"`
	HasUI          bool   `json:"has_ui"`
}

// OperationRef identifies one callable operation in a module contract.
type OperationRef struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	OperationID string `json:"operation_id,omitempty"`
}

// InvokeRequest is one module invocation.
type InvokeRequest struct {
	Method string
	// Path is the operation path. It may carry a query string and percent
	// escapes. The client concatenates it without re-encoding, so an encoded
	// slash stays one path segment.
	Path   string
	Header http.Header
	Body   []byte
}

// ConsoleStatusError reports a non-success console answer. It carries the
// status, the console host, the correlation ID the console echoed, and a
// bounded body snippet — never a request header.
type ConsoleStatusError struct {
	StatusCode    int
	Status        string
	Host          string
	CorrelationID string
	Body          string
}

// Error implements error.
func (e *ConsoleStatusError) Error() string {
	text := "console returned HTTP " + e.Status
	if e.Host != "" {
		text += " from " + e.Host
	}
	if e.CorrelationID != "" {
		text += " (correlation " + e.CorrelationID + ")"
	}
	if e.Body != "" {
		text += ": " + e.Body
	}
	return text
}

// NewConsoleClient resolves the saved session and builds a client for the
// console API. An explicit serverURL is the console base URL and wins over the
// stored one, but it is only a selector: the session entry keeps its stored
// server URL, so a refresh cannot rewrite the tunnel endpoint. The token is
// optional: an auth-disabled server needs none, and an auth-enabled server
// answers 401, which every status error maps to a login hint.
func NewConsoleClient(serverURL string) (*ConsoleClient, error) {
	serverURL = strings.TrimRight(serverURL, "/")

	info, err := resolveConsoleSession(serverURL)
	if err != nil {
		return nil, err
	}

	consoleURL := strings.TrimRight(info.ConsoleURL, "/")
	if serverURL != "" {
		consoleURL = serverURL
	}
	if consoleURL == "" {
		return nil, errors.New("the saved session has no console URL; run login or pass --server")
	}
	if err := auth.ValidateConsoleURL(consoleURL); err != nil {
		return nil, fmt.Errorf("console URL %q: %w", consoleURL, err)
	}

	base, err := consoleAPIBase(consoleURL)
	if err != nil {
		return nil, err
	}
	if info.Token == "" {
		log.Printf("no saved token for %s; the request is unauthenticated", consoleURL)
	}

	return &ConsoleClient{baseURL: base, token: info.Token, http: consoleHTTPClient}, nil
}

// resolveConsoleSession resolves the saved session for a console command. A
// console URL selects the base and the session lookup key, but it never becomes
// the session's persisted identity: the write-back targets the entry's stored
// server URL, which is the tunnel endpoint agent and connect resolve. A host
// with no saved entry stays unauthenticated instead of borrowing another host's
// token.
func resolveConsoleSession(consoleURL string) (*SessionInfo, error) {
	if consoleURL == "" {
		return ResolveSession("", "console")
	}
	_, storedServer, _, _, err := auth.LoadSession(consoleURL)
	if err != nil {
		// An unreadable session file holds no token, so no refresh can run and
		// the plain resolve cannot rewrite anything.
		return ResolveSession(consoleURL, "console")
	}
	if storedServer == "" {
		return &SessionInfo{URL: consoleURL}, nil
	}
	return ResolveSession(storedServer, "console")
}

// ListAgents returns the agents the current session can reach.
func (c *ConsoleClient) ListAgents(ctx context.Context) ([]ConsoleAgent, error) {
	var agents []ConsoleAgent
	if err := c.getJSON(ctx, "/connected-agents", &agents); err != nil {
		return nil, err
	}
	return agents, nil
}

// ListModules returns the modules an agent exposes.
func (c *ConsoleClient) ListModules(ctx context.Context, agentID string) ([]ConsoleModule, error) {
	var modules []ConsoleModule
	path := "/modules?agent=" + url.QueryEscape(agentID)
	if err := c.getJSON(ctx, path, &modules); err != nil {
		return nil, err
	}
	return modules, nil
}

// Operations returns the operation document and the operation references it
// contains. The raw document is returned unchanged so the caller can print the
// console representation without dropping parameters or body schemas.
func (c *ConsoleClient) Operations(ctx context.Context, agentID, moduleID string) (json.RawMessage, []OperationRef, error) {
	resp, err := c.do(ctx, http.MethodGet, consoleModulePath(agentID, moduleID)+"/operations", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, NewConsoleStatusError(resp, readConsoleErrorBody(resp))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, consoleJSONLimit))
	if err != nil {
		return nil, nil, fmt.Errorf("read operations: %w", err)
	}
	var doc struct {
		Operations []OperationRef `json:"operations"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("decode operations: %w", err)
	}
	return json.RawMessage(raw), doc.Operations, nil
}

// OpenAPI returns the module OpenAPI document byte for byte.
func (c *ConsoleClient) OpenAPI(ctx context.Context, agentID, moduleID string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/modules/openapi/"+url.PathEscape(agentID)+"/"+url.PathEscape(moduleID), nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, NewConsoleStatusError(resp, readConsoleErrorBody(resp))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, consoleJSONLimit))
	if err != nil {
		return nil, fmt.Errorf("read OpenAPI document: %w", err)
	}
	return data, nil
}

// Invoke sends one operation request through the console invoke route. The
// response body is left open for the caller to print.
func (c *ConsoleClient) Invoke(ctx context.Context, agentID, moduleID string, req InvokeRequest) (*http.Response, error) {
	opPath := req.Path
	if !strings.HasPrefix(opPath, "/") {
		opPath = "/" + opPath
	}
	header := make(http.Header, len(req.Header)+1)
	for key, values := range req.Header {
		header[key] = append([]string(nil), values...)
	}
	if len(req.Body) > 0 && header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	return c.do(ctx, req.Method, "/modules/invoke/"+url.PathEscape(agentID)+"/"+url.PathEscape(moduleID)+opPath, header, body)
}

// getJSON performs a GET and decodes a 200 JSON answer.
func (c *ConsoleClient) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return NewConsoleStatusError(resp, readConsoleErrorBody(resp))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, consoleJSONLimit)).Decode(out); err != nil {
		return fmt.Errorf("decode console response: %w", err)
	}
	return nil
}

// do builds and sends one console request. Custom headers are copied first,
// then the session credential is applied, so a caller cannot replace it. The
// correlation ID is set only when the caller supplied none.
func (c *ConsoleClient) do(ctx context.Context, method, path string, header http.Header, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build console request: %w", err)
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if req.Header.Get(moduleapi.CorrelationHeader) == "" {
		req.Header.Set(moduleapi.CorrelationHeader, moduleapi.NewCorrelationID())
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// A *url.Error repeats the full URL, including any query values. Report
		// the host and the transport cause only.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("console request to %s failed: %w", req.URL.Host, err)
	}
	return resp, nil
}

// consoleModulePath returns the module route for one agent and module.
func consoleModulePath(agentID, moduleID string) string {
	return "/modules/" + url.PathEscape(agentID) + "/" + url.PathEscape(moduleID)
}

// NewConsoleStatusError builds the error for a console status answer. The body
// is the bounded snippet the caller read; `call` prints its body first and
// passes none. Host and correlation ID come from the response, so a failure can
// be matched to the console's invocation log.
func NewConsoleStatusError(resp *http.Response, body string) *ConsoleStatusError {
	e := &ConsoleStatusError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(body),
	}
	if resp.Request != nil && resp.Request.URL != nil {
		e.Host = resp.Request.URL.Host
	}
	e.CorrelationID = resp.Header.Get(moduleapi.CorrelationHeader)
	return e
}

// readConsoleErrorBody consumes the diagnostic snippet of a failed answer.
func readConsoleErrorBody(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, consoleErrorBodyLimit))
	return string(body)
}
