package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Catalog is a snapshot of all modules registered on a single agent at a
// specific catalog epoch. Consumers receive this from [Consumer.Discover].
type Catalog struct {
	// AgentID identifies the agent that published this catalog.
	AgentID string

	// Epoch is the catalog epoch, derived from the transport session ID.
	// It changes on every agent reconnect. Consumers must re-discover
	// after a reconnect because stale epochs are rejected.
	Epoch string

	// Modules is the list of currently registered modules.
	Modules []ModuleEntry

	// PublishedAt is when the agent published this snapshot.
	PublishedAt time.Time
}

// ModuleEntry is one module in a catalog snapshot.
type ModuleEntry struct {
	// ModuleID is the module's reverse-DNS identifier.
	ModuleID string

	// ServiceName is the human-readable service name.
	ServiceName string

	// ServiceVersion is the semantic version string.
	ServiceVersion string

	// Revision is the module's current revision. Changes indicate a
	// contract or implementation update.
	Revision string

	// Health is the module's last reported health status.
	Health Health

	// OpenAPISummary is a brief description of the module's API surface,
	// extracted from the OpenAPI info.title and info.description fields.
	// Full YAML is fetched separately via [Consumer.Contract].
	OpenAPISummary string

	// UI lists the module's UI descriptors (provider and consumer remotes).
	UI []UIDescriptor
}

// Contract holds the full OpenAPI YAML for a specific module revision.
// Fetched via [Consumer.Contract].
type Contract struct {
	// ModuleRef identifies the exact module, epoch, and revision.
	ModuleRef ModuleRef

	// OpenAPIYAML is the complete OpenAPI v3.0 or v3.1 YAML document.
	OpenAPIYAML []byte

	// RetrievedAt is when this contract was fetched from the server.
	RetrievedAt time.Time
}

// Consumer discovers and invokes modules through the tunnel. Create one
// with [NewConsumer].
type Consumer struct {
	serverURL  string
	authToken  string
	httpClient *http.Client
}

// NewConsumer creates a consumer that talks to the given tunnel server.
// The authToken is used as a Bearer token for all requests. Pass a nil
// httpClient to use the default.
func NewConsumer(serverURL, authToken string, httpClient *http.Client) *Consumer {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Consumer{
		serverURL:  strings.TrimRight(serverURL, "/"),
		authToken:  authToken,
		httpClient: httpClient,
	}
}

// Discover returns the current catalog for the given agent. The catalog
// epoch and module revisions in the result must be used for subsequent
// invocations — stale epochs are rejected with a 409 Conflict.
func (c *Consumer) Discover(ctx context.Context, agentID string) (*Catalog, error) {
	url := fmt.Sprintf("%s/api/v1/modules?agent=%s", c.serverURL, agentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ParseErrorResponse(resp)
	}

	var entries []moduleEntryJSON
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}

	catalog := &Catalog{
		AgentID:     agentID,
		Epoch:       resp.Header.Get("X-Catalog-Epoch"),
		Modules:     make([]ModuleEntry, 0, len(entries)),
		PublishedAt: time.Now(),
	}
	for _, e := range entries {
		catalog.Modules = append(catalog.Modules, ModuleEntry{
			ModuleID:       e.ModuleID,
			ServiceName:    e.ServiceName,
			ServiceVersion: e.ServiceVersion,
			Revision:       e.Revision,
			Health: Health{
				Status: HealthStatus(e.Health),
			},
		})
	}
	return catalog, nil
}

// moduleEntryJSON is the JSON structure returned by the discovery API.
type moduleEntryJSON struct {
	ModuleID       string `json:"module_id"`
	ServiceName    string `json:"service_name"`
	ServiceVersion string `json:"service_version"`
	Revision       string `json:"revision"`
	Health         string `json:"health"`
	HasUI          bool   `json:"has_ui"`
}

// Contract fetches the full OpenAPI YAML for the specified module revision.
func (c *Consumer) Contract(ctx context.Context, ref ModuleRef) (*Contract, error) {
	url := fmt.Sprintf("%s/api/v1/modules/openapi/%s/%s",
		c.serverURL, ref.AgentID, ref.ModuleID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ParseErrorResponse(resp)
	}

	yaml, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openapi: %w", err)
	}

	return &Contract{
		ModuleRef:   ref,
		OpenAPIYAML: yaml,
		RetrievedAt: time.Now(),
	}, nil
}

// Invoke sends an HTTP request to the module identified by ref. The
// request's method, path, headers, query, and body are matched against
// the module's committed OpenAPI document. Streaming responses are
// returned without full-body buffering.
//
// The caller is responsible for closing resp.Body.
func (c *Consumer) Invoke(ctx context.Context, ref ModuleRef, req *http.Request) (*http.Response, error) {
	// Build the invocation URL.
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	url := fmt.Sprintf("%s/api/v1/modules/invoke/%s/%s%s",
		c.serverURL, ref.AgentID, ref.ModuleID, path)
	if req.URL.RawQuery != "" {
		url += "?" + req.URL.RawQuery
	}

	invReq, err := http.NewRequestWithContext(ctx, req.Method, url, req.Body)
	if err != nil {
		return nil, fmt.Errorf("create invocation request: %w", err)
	}

	// Copy headers.
	for key, values := range req.Header {
		for _, v := range values {
			invReq.Header.Add(key, v)
		}
	}
	c.setAuth(invReq)

	// Add correlation ID if not present.
	if invReq.Header.Get("X-Correlation-ID") == "" {
		invReq.Header.Set("X-Correlation-ID", generateClientCorrelationID())
	}

	resp, err := c.httpClient.Do(invReq)
	if err != nil {
		return nil, fmt.Errorf("invoke: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, ParseErrorResponse(resp)
	}

	return resp, nil
}

// generateClientCorrelationID creates a client-side correlation ID.
func generateClientCorrelationID() string {
	return fmt.Sprintf("sdk-%d", time.Now().UnixNano())
}

// setAuth sets the authorization header on a request.
func (c *Consumer) setAuth(req *http.Request) {
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
}
