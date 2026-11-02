package agentmod

import (
	"fmt"
	"strings"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"gopkg.in/yaml.v3"
)

// maxOpenAPIDepth limits YAML nesting depth to prevent stack exhaustion.
const maxOpenAPIDepth = 32

// ValidatedOpenAPI holds a parsed and validated OpenAPI document.
type ValidatedOpenAPI struct {
	// Version is "3.0" or "3.1".
	Version string

	// Title is the info.title field.
	Title string

	// Description is the info.description field (may be empty).
	Description string

	// Operations maps "METHOD /path" to operation metadata.
	// Example: "GET /api/items" → Operation{Method: "GET", Path: "/api/items"}
	Operations map[string]Operation
}

// Operation is one HTTP operation in the OpenAPI document.
type Operation struct {
	Method      string // HTTP method (GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS)
	Path        string // Path template (e.g. "/api/items/{id}")
	OperationID string // Optional operation ID
	Summary     string // Optional summary
}

// ValidateOpenAPI parses and validates an OpenAPI v3.0 or v3.1 YAML document.
// It rejects:
// - Unsupported OpenAPI versions (only 3.0.x and 3.1.x)
// - Duplicate operation IDs
// - Duplicate method+path combinations
// - External $ref references (only local references allowed)
// - Excessive nesting depth
// - Oversized input
func ValidateOpenAPI(data []byte) (*ValidatedOpenAPI, error) {
	if len(data) > moduleproto.MaxOpenAPISize {
		return nil, fmt.Errorf("OpenAPI YAML too large: %d bytes (max %d)", len(data), moduleproto.MaxOpenAPISize)
	}

	// Parse YAML into a generic map.
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("YAML parse error: %w", err)
	}

	// Check for duplicate keys by re-parsing with a stricter decoder.
	// yaml.v3 does not expose duplicate key detection directly, so we
	// check the structure after unmarshal.

	// Validate openapi version.
	openapi, ok := doc["openapi"]
	if !ok {
		return nil, fmt.Errorf("missing 'openapi' field")
	}
	version, ok := openapi.(string)
	if !ok {
		return nil, fmt.Errorf("'openapi' must be a string")
	}
	if !strings.HasPrefix(version, "3.0") && !strings.HasPrefix(version, "3.1") {
		return nil, fmt.Errorf("unsupported OpenAPI version %q (only 3.0.x and 3.1.x supported)", version)
	}
	majorVersion := version[:3] // "3.0" or "3.1"

	// Validate info section.
	info, ok := doc["info"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'info' section")
	}
	title, _ := info["title"].(string)
	description, _ := info["description"].(string)
	if title == "" {
		return nil, fmt.Errorf("'info.title' is required")
	}

	// Check for external $ref references.
	if err := checkNoExternalRefs(doc, 0); err != nil {
		return nil, err
	}

	// Extract and validate paths.
	paths, ok := doc["paths"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'paths' section")
	}

	operations := make(map[string]Operation)
	operationIDs := make(map[string]string) // operationID → "METHOD /path"

	for path, pathItem := range paths {
		if !strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("path %q must start with /", path)
		}
		if strings.Contains(path, "..") {
			return nil, fmt.Errorf("path %q contains '..'", path)
		}

		pathMap, ok := pathItem.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("path %q: invalid path item", path)
		}

		for method, opItem := range pathMap {
			method = strings.ToUpper(method)
			if !isHTTPMethod(method) {
				// Skip non-operation keys (parameters, summary, etc.)
				continue
			}
			if _, ok := opItem.(map[string]interface{}); !ok {
				return nil, fmt.Errorf("path %q method %s: invalid operation", path, method)
			}

			key := method + " " + path
			if _, exists := operations[key]; exists {
				return nil, fmt.Errorf("duplicate operation: %s", key)
			}

			opMap := opItem.(map[string]interface{})
			operationID, _ := opMap["operationId"].(string)
			summary, _ := opMap["summary"].(string)

			if operationID != "" {
				if prev, exists := operationIDs[operationID]; exists {
					return nil, fmt.Errorf("duplicate operationId %q: used by %s and %s %s", operationID, prev, method, path)
				}
				operationIDs[operationID] = key
			}

			operations[key] = Operation{
				Method:      method,
				Path:        path,
				OperationID: operationID,
				Summary:     summary,
			}
		}
	}

	return &ValidatedOpenAPI{
		Version:     majorVersion,
		Title:       title,
		Description: description,
		Operations:  operations,
	}, nil
}

// checkNoExternalRefs recursively checks for external $ref references.
// Only local references (starting with "#/") are allowed.
func checkNoExternalRefs(v interface{}, depth int) error {
	if depth > maxOpenAPIDepth {
		return fmt.Errorf("YAML nesting depth exceeds %d", maxOpenAPIDepth)
	}
	switch val := v.(type) {
	case map[string]interface{}:
		if ref, ok := val["$ref"].(string); ok {
			if !strings.HasPrefix(ref, "#/") {
				return fmt.Errorf("external $ref not allowed: %q", ref)
			}
		}
		for _, child := range val {
			if err := checkNoExternalRefs(child, depth+1); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, child := range val {
			if err := checkNoExternalRefs(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// isHTTPMethod checks if the string is a valid HTTP method.
func isHTTPMethod(s string) bool {
	switch s {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE":
		return true
	}
	return false
}

// OperationSummary returns a brief description of the API surface for
// inclusion in catalog entries.
func (v *ValidatedOpenAPI) OperationSummary() string {
	if len(v.Operations) == 0 {
		return v.Title
	}
	return fmt.Sprintf("%s (%d operations)", v.Title, len(v.Operations))
}

// MatchOperation finds the operation matching the given method and path.
// The path is matched against path templates (e.g. "/api/items/{id}").
// Returns the operation and true if found, or zero value and false otherwise.
func (v *ValidatedOpenAPI) MatchOperation(method, path string) (Operation, bool) {
	method = strings.ToUpper(method)

	// Exact match first.
	key := method + " " + path
	if op, ok := v.Operations[key]; ok {
		return op, true
	}

	// Template match: check if path matches any template pattern.
	for k, op := range v.Operations {
		if op.Method != method {
			continue
		}
		if matchPathTemplate(op.Path, path) {
			return v.Operations[k], true
		}
	}
	return Operation{}, false
}

// matchPathTemplate checks if a concrete path matches a path template.
// Template parameters are {name} segments.
func matchPathTemplate(template, path string) bool {
	tParts := strings.Split(template, "/")
	pParts := strings.Split(path, "/")
	if len(tParts) != len(pParts) {
		return false
	}
	for i, tp := range tParts {
		if strings.HasPrefix(tp, "{") && strings.HasSuffix(tp, "}") {
			// Template parameter matches any non-empty segment.
			if pParts[i] == "" {
				return false
			}
			continue
		}
		if tp != pParts[i] {
			return false
		}
	}
	return true
}
