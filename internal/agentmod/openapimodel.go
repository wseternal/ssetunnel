package agentmod

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"gopkg.in/yaml.v3"
)

// maxAPISchemaDepth bounds how deep a schema tree is expanded for the
// console invocation UI. Schemas nested deeper are truncated with
// APISchema.Truncated set, and the UI falls back to a free-form JSON editor.
const maxAPISchemaDepth = 6

// APIOperation is a UI-facing description of one callable module operation.
// It is derived from the module's OpenAPI document and is intentionally
// presentation-oriented: it carries just enough structure for a form
// generator, not the whole specification.
type APIOperation struct {
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	OperationID string          `json:"operation_id,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  []APIParameter  `json:"parameters"`
	RequestBody *APIRequestBody `json:"request_body,omitempty"`
}

// APIParameter describes one path, query, header, or cookie parameter.
type APIParameter struct {
	Name        string     `json:"name"`
	In          string     `json:"in"`
	Description string     `json:"description,omitempty"`
	Required    bool       `json:"required"`
	Schema      *APISchema `json:"schema,omitempty"`
}

// APIRequestBody describes the request body accepted by an operation.
type APIRequestBody struct {
	Required    bool       `json:"required"`
	ContentType string     `json:"content_type,omitempty"`
	Schema      *APISchema `json:"schema,omitempty"`
}

// APISchema is a flattened JSON-schema subset. References are resolved
// eagerly; recursion is reported via Recursive instead of expanded again.
type APISchema struct {
	Type        string        `json:"type,omitempty"`
	Format      string        `json:"format,omitempty"`
	Ref         string        `json:"ref,omitempty"`
	Description string        `json:"description,omitempty"`
	Nullable    bool          `json:"nullable,omitempty"`
	Enum        []interface{} `json:"enum,omitempty"`
	Default     interface{}   `json:"default,omitempty"`
	Example     interface{}   `json:"example,omitempty"`
	Properties  []APIProperty `json:"properties,omitempty"`
	Items       *APISchema    `json:"items,omitempty"`
	Truncated   bool          `json:"truncated,omitempty"`
	Recursive   bool          `json:"recursive,omitempty"`
}

// APIProperty is one named property of an object schema.
type APIProperty struct {
	Name     string     `json:"name"`
	Required bool       `json:"required"`
	Schema   *APISchema `json:"schema"`
}

// BuildOperationModels derives form-friendly operation models from an
// OpenAPI YAML document. The document is expected to have passed
// ValidateOpenAPI already; this function does not re-validate it beyond the
// structural minimum needed to walk it.
//
// Output order is deterministic: paths sorted lexicographically, then methods
// in canonical order, so repeated calls render the same form order.
func BuildOperationModels(data []byte) (string, []APIOperation, error) {
	if len(data) > moduleproto.MaxOpenAPISize {
		return "", nil, fmt.Errorf("OpenAPI YAML too large: %d bytes (max %d)", len(data), moduleproto.MaxOpenAPISize)
	}

	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", nil, fmt.Errorf("YAML parse error: %w", err)
	}

	info, _ := doc["info"].(map[string]interface{})
	title, _ := info["title"].(string)

	paths, ok := doc["paths"].(map[string]interface{})
	if !ok {
		return title, nil, fmt.Errorf("missing or invalid 'paths' section")
	}

	pathKeys := make([]string, 0, len(paths))
	for path := range paths {
		pathKeys = append(pathKeys, path)
	}
	sort.Strings(pathKeys)

	operations := make([]APIOperation, 0, len(paths))
	for _, path := range pathKeys {
		pathMap, ok := paths[path].(map[string]interface{})
		if !ok {
			continue
		}
		// A path item may itself be a reference (OpenAPI 3.1 pathItems).
		if ref, ok := pathMap["$ref"].(string); ok {
			if target, _ := resolveRef(doc, ref); target != nil {
				pathMap = target
			}
		}

		// Parameters declared on the path item apply to every operation
		// under it, unless the operation overrides the same name+location.
		common := parseParameters(doc, pathMap["parameters"])

		methodKeys := make([]string, 0, len(pathMap))
		for key := range pathMap {
			if isHTTPMethod(strings.ToUpper(key)) {
				methodKeys = append(methodKeys, key)
			}
		}
		sort.Slice(methodKeys, func(i, j int) bool {
			return methodRank(strings.ToUpper(methodKeys[i])) < methodRank(strings.ToUpper(methodKeys[j]))
		})

		for _, key := range methodKeys {
			opMap, ok := pathMap[key].(map[string]interface{})
			if !ok {
				continue
			}
			method := strings.ToUpper(key)
			op := APIOperation{
				Method:      method,
				Path:        path,
				OperationID: stringField(opMap, "operationId"),
				Summary:     stringField(opMap, "summary"),
				Description: stringField(opMap, "description"),
			}
			op.Parameters = mergeParameters(common, parseParameters(doc, opMap["parameters"]))
			op.RequestBody = parseRequestBody(doc, opMap["requestBody"])
			operations = append(operations, op)
		}
	}

	return title, operations, nil
}

// methodRank orders methods so the UI lists the most common ones first.
func methodRank(method string) int {
	switch method {
	case "GET":
		return 0
	case "POST":
		return 1
	case "PUT":
		return 2
	case "PATCH":
		return 3
	case "DELETE":
		return 4
	default:
		return 5
	}
}

func stringField(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// schemaType reads "type" in both spec dialects: OpenAPI 3.0 uses a string,
// while 3.1 allows an array such as ["string", "null"].
func schemaType(m map[string]interface{}) string {
	switch v := m["type"].(type) {
	case string:
		return v
	case []interface{}:
		for _, entry := range v {
			if s, ok := entry.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

// hasNullType reports whether a 3.1 type array includes "null".
func hasNullType(raw interface{}) bool {
	list, ok := raw.([]interface{})
	if !ok {
		return false
	}
	for _, entry := range list {
		// yaml.v3 decodes the literal `null` in a type array as a nil entry.
		if entry == nil {
			return true
		}
		if s, ok := entry.(string); ok && s == "null" {
			return true
		}
	}
	return false
}

func boolField(m map[string]interface{}, key string) bool {
	b, _ := m[key].(bool)
	return b
}

// parseParameters converts an OpenAPI parameters array into models.
func parseParameters(doc map[string]interface{}, raw interface{}) []APIParameter {
	list, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	params := make([]APIParameter, 0, len(list))
	for _, entry := range list {
		m, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		// A parameter entry may itself be a $ref to components.parameters.
		if ref, ok := m["$ref"].(string); ok {
			if target, _ := resolveRef(doc, ref); target != nil {
				m = target
			}
		}
		name := stringField(m, "name")
		in := stringField(m, "in")
		if name == "" || in == "" {
			continue
		}
		param := APIParameter{
			Name:        name,
			In:          in,
			Description: stringField(m, "description"),
			Schema:      buildSchema(doc, m["schema"], nil, 0),
		}
		// Path parameters are always required by the specification.
		param.Required = in == "path" || boolField(m, "required")
		params = append(params, param)
	}
	return params
}

// mergeParameters applies operation-level parameters over path-level ones.
// Entries with the same name and location are replaced in place so the
// resulting order stays stable.
func mergeParameters(common, own []APIParameter) []APIParameter {
	out := make([]APIParameter, 0, len(common)+len(own))
	out = append(out, common...)
	for _, p := range own {
		replaced := false
		for i := range out {
			if out[i].Name == p.Name && out[i].In == p.In {
				out[i] = p
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, p)
		}
	}
	if out == nil {
		return []APIParameter{}
	}
	return out
}

// parseRequestBody picks a single media type, preferring JSON.
func parseRequestBody(doc map[string]interface{}, raw interface{}) *APIRequestBody {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	body := &APIRequestBody{Required: boolField(m, "required")}
	content, ok := m["content"].(map[string]interface{})
	if !ok || len(content) == 0 {
		return body
	}

	mediaTypes := make([]string, 0, len(content))
	for mt := range content {
		mediaTypes = append(mediaTypes, mt)
	}
	sort.Strings(mediaTypes)
	chosen := mediaTypes[0]
	for _, mt := range mediaTypes {
		if mt == "application/json" || strings.HasSuffix(mt, "+json") {
			chosen = mt
			break
		}
	}

	body.ContentType = chosen
	if media, ok := content[chosen].(map[string]interface{}); ok {
		body.Schema = buildSchema(doc, media["schema"], nil, 0)
	}
	return body
}

// resolveRef resolves a local reference like "#/components/schemas/Foo".
func resolveRef(doc map[string]interface{}, ref string) (map[string]interface{}, string) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, ""
	}
	pointer := strings.TrimPrefix(ref, "#/")
	pointer = strings.ReplaceAll(pointer, "~1", "/")
	pointer = strings.ReplaceAll(pointer, "~0", "~")
	segments := strings.Split(pointer, "/")

	var current interface{} = doc
	for _, seg := range segments {
		if unescaped, err := url.PathUnescape(seg); err == nil {
			seg = unescaped
		}
		switch node := current.(type) {
		case map[string]interface{}:
			child, ok := node[seg]
			if !ok {
				return nil, ""
			}
			current = child
		case []interface{}:
			return nil, "" // numeric array pointers are not used by module specs
		default:
			return nil, ""
		}
	}
	target, ok := current.(map[string]interface{})
	if !ok {
		return nil, ""
	}
	name := segments[len(segments)-1]
	return target, name
}

// buildSchema converts a raw schema node into APISchema. seen tracks the
// $ref chain on the current branch so cycles terminate instead of expanding.
func buildSchema(doc map[string]interface{}, raw interface{}, seen map[string]bool, depth int) *APISchema {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}

	if ref, ok := m["$ref"].(string); ok {
		name := ref
		if target, resolvedName := resolveRef(doc, ref); target != nil {
			if resolvedName != "" {
				name = resolvedName
			}
			if seen[name] {
				return &APISchema{Type: "object", Ref: name, Recursive: true}
			}
			if depth >= maxAPISchemaDepth {
				return &APISchema{Type: "object", Ref: name, Truncated: true}
			}
			next := make(map[string]bool, len(seen)+1)
			for k, v := range seen {
				next[k] = v
			}
			next[name] = true
			schema := buildSchema(doc, target, next, depth+1)
			if schema == nil {
				schema = &APISchema{Type: "object"}
			}
			if schema.Ref == "" {
				schema.Ref = name
			}
			return schema
		}
		return &APISchema{Type: "object", Ref: name}
	}

	if depth >= maxAPISchemaDepth {
		return &APISchema{Type: "object", Truncated: true}
	}

	// allOf is common in generated specs: merge members into one object so
	// the form can render every property instead of hiding half of them.
	if members, ok := m["allOf"].([]interface{}); ok {
		m = mergeAllOf(doc, m, members)
	}

	schema := &APISchema{
		Type:        schemaType(m),
		Format:      stringField(m, "format"),
		Description: stringField(m, "description"),
		Nullable:    boolField(m, "nullable") || hasNullType(m["type"]),
	}
	if enum, ok := m["enum"].([]interface{}); ok {
		schema.Enum = enum
	}
	if v, ok := m["default"]; ok {
		schema.Default = v
	}
	if v, ok := m["example"]; ok {
		schema.Example = v
	}

	if props, ok := m["properties"].(map[string]interface{}); ok {
		required := map[string]bool{}
		if list, ok := m["required"].([]interface{}); ok {
			for _, entry := range list {
				if name, ok := entry.(string); ok {
					required[name] = true
				}
			}
		}
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			schema.Properties = append(schema.Properties, APIProperty{
				Name:     name,
				Required: required[name],
				Schema:   buildSchema(doc, props[name], seen, depth+1),
			})
		}
	}

	if _, ok := m["items"]; ok {
		schema.Items = buildSchema(doc, m["items"], seen, depth+1)
	}

	// Infer the type when the spec relies on structural keywords alone.
	if schema.Type == "" {
		switch {
		case len(schema.Properties) > 0:
			schema.Type = "object"
		case schema.Items != nil:
			schema.Type = "array"
		case len(schema.Enum) > 0:
			schema.Type = "string"
		default:
			schema.Type = "object"
		}
	}

	return schema
}

// mergeAllOf flattens allOf members over the parent node.
func mergeAllOf(doc map[string]interface{}, parent map[string]interface{}, members []interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(parent)+1)
	for k, v := range parent {
		merged[k] = v
	}
	delete(merged, "allOf")

	for _, member := range members {
		memberMap, ok := member.(map[string]interface{})
		if !ok {
			continue
		}
		if ref, ok := memberMap["$ref"].(string); ok {
			if target, _ := resolveRef(doc, ref); target != nil {
				memberMap = target
			}
		}
		for k, v := range memberMap {
			if k == "$ref" || k == "allOf" {
				continue
			}
			if k == "properties" {
				existing, _ := merged["properties"].(map[string]interface{})
				incoming, _ := v.(map[string]interface{})
				if existing == nil {
					existing = make(map[string]interface{}, len(incoming))
					merged["properties"] = existing
				}
				for name, prop := range incoming {
					if _, present := existing[name]; !present {
						existing[name] = prop
					}
				}
				continue
			}
			if k == "required" {
				existing, _ := merged["required"].([]interface{})
				incoming, _ := v.([]interface{})
				seen := make(map[string]bool, len(existing))
				for _, entry := range existing {
					if name, ok := entry.(string); ok {
						seen[name] = true
					}
				}
				combined := existing
				for _, entry := range incoming {
					name, ok := entry.(string)
					if !ok || seen[name] {
						continue
					}
					seen[name] = true
					combined = append(combined, entry)
				}
				merged["required"] = combined
				continue
			}
			if _, present := merged[k]; !present {
				merged[k] = v
			}
		}
	}
	return merged
}
