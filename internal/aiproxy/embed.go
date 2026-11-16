package aiproxy

import _ "embed"

// OpenAPIYAML is the OpenAPI specification for the AI automation proxy.
//
//go:embed openapi.yaml
var OpenAPIYAML []byte
