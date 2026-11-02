package sdk

import (
	"context"
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
	// Implementation in Task 13 (consumer SDK).
	return nil, ErrNotImplemented
}

// Contract fetches the full OpenAPI YAML for the specified module revision.
func (c *Consumer) Contract(ctx context.Context, ref ModuleRef) (*Contract, error) {
	// Implementation in Task 13 (consumer SDK).
	return nil, ErrNotImplemented
}

// Invoke sends an HTTP request to the module identified by ref. The
// request's method, path, headers, query, and body are matched against
// the module's committed OpenAPI document. Streaming responses are
// returned without full-body buffering.
//
// The caller is responsible for closing resp.Body.
func (c *Consumer) Invoke(ctx context.Context, ref ModuleRef, req *http.Request) (*http.Response, error) {
	// Implementation in Task 13 (consumer SDK).
	return nil, ErrNotImplemented
}

// Cancel cancels an in-flight invocation by its correlation ID. The
// server closes the stream, and the module's handler receives a context
// cancellation.
func (c *Consumer) Cancel(ctx context.Context, correlationID string) error {
	// Implementation in Task 13 (consumer SDK).
	return ErrNotImplemented
}
