package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrNotImplemented is returned by stub methods that are not yet implemented.
var ErrNotImplemented = errors.New("not implemented")

// ErrType classifies the category of a module error. Consumers use this
// to decide how to handle a failure (retry, rediscover, or surface to user).
type ErrType string

const (
	// ErrTypeTransport indicates a network or tunnel failure (connection
	// refused, timeout, DNS failure). The operation can be retried.
	ErrTypeTransport ErrType = "transport"

	// ErrTypeRegistry indicates a registry error: module not found, stale
	// revision, epoch mismatch, or authorization failure. The consumer
	// must re-discover before retrying.
	ErrTypeRegistry ErrType = "registry"

	// ErrTypeModule indicates the module itself returned an error response.
	// The module is reachable but the operation failed. Inspect the HTTP
	// status code and detail for the specific failure.
	ErrTypeModule ErrType = "module"
)

// ModuleError is a structured error from a module operation. It follows
// RFC 7807 (Problem Details for HTTP APIs) for transport and registry
// failures. Module errors (ErrTypeModule) carry the original HTTP status
// code and response body.
type ModuleError struct {
	// Type classifies the error category.
	Type ErrType

	// Title is a short, human-readable summary.
	Title string

	// Detail is a longer description of the problem.
	Detail string

	// Status is the HTTP status code for module errors, or 0 for
	// transport/registry errors that did not reach the module.
	Status int

	// CorrelationID links this error to the server-side log entry.
	// Empty if the error occurred before the server assigned one.
	CorrelationID string
}

// Error implements the error interface.
func (e *ModuleError) Error() string {
	s := fmt.Sprintf("%s: %s", e.Type, e.Title)
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.Status != 0 {
		s += fmt.Sprintf(" (status %d)", e.Status)
	}
	return s
}

// IsRetryable returns true if the error is transient and the operation
// can be retried without re-discovery. Only transport errors are retryable.
func (e *ModuleError) IsRetryable() bool {
	return e.Type == ErrTypeTransport
}

// NeedsRediscovery returns true if the consumer must call Discover again
// before retrying. Registry errors (stale epoch, module not found) require
// fresh discovery.
func (e *ModuleError) NeedsRediscovery() bool {
	return e.Type == ErrTypeRegistry
}

// NewTransportError creates a ModuleError for a network or tunnel failure.
func NewTransportError(title, detail string) *ModuleError {
	return &ModuleError{Type: ErrTypeTransport, Title: title, Detail: detail}
}

// NewRegistryError creates a ModuleError for a registry failure.
func NewRegistryError(title, detail string) *ModuleError {
	return &ModuleError{Type: ErrTypeRegistry, Title: title, Detail: detail}
}

// NewModuleError creates a ModuleError from an HTTP error response.
func NewModuleError(status int, body []byte, correlationID string) *ModuleError {
	e := &ModuleError{
		Type:          ErrTypeModule,
		Title:         http.StatusText(status),
		Status:        status,
		CorrelationID: correlationID,
	}
	if len(body) > 0 {
		e.Detail = string(body)
	}
	return e
}

// problemDetails is the RFC 7807 JSON structure for transport and registry
// error responses from the server.
type problemDetails struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Status   int    `json:"status,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// ParseErrorResponse parses an HTTP error response into a ModuleError.
// If the response body is valid RFC 7807 JSON, it extracts the structured
// error. Otherwise, it uses the status code and raw body.
func ParseErrorResponse(resp *http.Response) *ModuleError {
	if resp == nil {
		return NewTransportError("no response", "the server did not return a response")
	}
	correlationID := resp.Header.Get("X-Correlation-ID")
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if len(body) == 0 {
		return NewModuleError(resp.StatusCode, nil, correlationID)
	}
	// Try RFC 7807 parsing.
	var pd problemDetails
	if err := json.Unmarshal(body, &pd); err == nil && pd.Type != "" {
		return &ModuleError{
			Type:          classifyProblemType(pd.Type),
			Title:         pd.Title,
			Detail:        pd.Detail,
			Status:        pd.Status,
			CorrelationID: correlationID,
		}
	}
	return NewModuleError(resp.StatusCode, body, correlationID)
}

// classifyProblemType maps an RFC 7807 type URI to an ErrType.
func classifyProblemType(uri string) ErrType {
	switch {
	case containsAny(uri, "transport", "network", "timeout", "unavailable"):
		return ErrTypeTransport
	case containsAny(uri, "registry", "not-found", "stale", "conflict", "forbidden"):
		return ErrTypeRegistry
	default:
		return ErrTypeModule
	}
}

// containsAny checks if s contains any of the given substrings (case-insensitive).
func containsAny(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}
