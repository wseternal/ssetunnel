package agentmod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageValidate(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mymodule")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	valid := Package{
		ModuleID:       "com.example.mymodule",
		ServiceName:    "my-service",
		ServiceVersion: "1.0.0",
		Executable:     exe,
		OpenAPIYAML:    []byte("openapi: 3.0.0\ninfo:\n  title: Test\npaths: {}\n"),
		PackageDir:     dir,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid package: got error %v", err)
	}

	tests := []struct {
		name string
		mod  func(Package) Package
	}{
		{"empty module ID", func(p Package) Package { p.ModuleID = ""; return p }},
		{"bad module ID format", func(p Package) Package { p.ModuleID = "Bad-Name"; return p }},
		{"empty service name", func(p Package) Package { p.ServiceName = ""; return p }},
		{"empty service version", func(p Package) Package { p.ServiceVersion = ""; return p }},
		{"bad semver", func(p Package) Package { p.ServiceVersion = "not-a-version"; return p }},
		{"empty executable", func(p Package) Package { p.Executable = ""; return p }},
		{"relative executable", func(p Package) Package { p.Executable = "relative/path"; return p }},
		{"exe outside package", func(p Package) Package { p.Executable = "/tmp/other"; return p }},
		{"empty openapi", func(p Package) Package { p.OpenAPIYAML = nil; return p }},
		{"empty package dir", func(p Package) Package { p.PackageDir = ""; return p }},
		{
			"module ID too long",
			func(p Package) Package { p.ModuleID = "com." + strings.Repeat("a", 200); return p },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.mod(valid)
			if err := p.Validate(); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestPackageValidateExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mymodule")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := Package{Executable: exe, PackageDir: dir}
	if err := p.ValidateExecutable(); err != nil {
		t.Errorf("valid executable: got error %v", err)
	}

	// Non-existent executable.
	p2 := Package{Executable: filepath.Join(dir, "nonexistent"), PackageDir: dir}
	if err := p2.ValidateExecutable(); err == nil {
		t.Error("expected error for non-existent executable")
	}

	// Non-executable file.
	noexec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(noexec, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	p3 := Package{Executable: noexec, PackageDir: dir}
	if err := p3.ValidateExecutable(); err == nil {
		t.Error("expected error for non-executable file")
	}
}

func TestIsSubPath(t *testing.T) {
	tests := []struct {
		parent, child string
		want          bool
	}{
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/../c", false},
		{"/a/b", "/a/c", false},
		{"/a/b", "/a", false},
	}
	for _, tt := range tests {
		got := isSubPath(tt.parent, tt.child)
		if got != tt.want {
			t.Errorf("isSubPath(%q, %q) = %v, want %v", tt.parent, tt.child, got, tt.want)
		}
	}
}

func TestValidateOpenAPI(t *testing.T) {
	valid := `
openapi: 3.0.3
info:
  title: Test API
  description: A test API
  version: 1.0.0
paths:
  /items:
    get:
      operationId: listItems
      summary: List items
    post:
      operationId: createItem
      summary: Create item
  /items/{id}:
    get:
      operationId: getItem
      summary: Get item by ID
`
	doc, err := ValidateOpenAPI([]byte(valid))
	if err != nil {
		t.Fatalf("valid OpenAPI: %v", err)
	}
	if doc.Version != "3.0" {
		t.Errorf("Version = %q, want 3.0", doc.Version)
	}
	if doc.Title != "Test API" {
		t.Errorf("Title = %q, want \"Test API\"", doc.Title)
	}
	if len(doc.Operations) != 3 {
		t.Errorf("Operations count = %d, want 3", len(doc.Operations))
	}

	// Test MatchOperation.
	op, ok := doc.MatchOperation("GET", "/items/123")
	if !ok {
		t.Error("MatchOperation(GET /items/123) = not found")
	} else if op.OperationID != "getItem" {
		t.Errorf("MatchOperation operationId = %q, want getItem", op.OperationID)
	}
}

func TestValidateOpenAPI31(t *testing.T) {
	data := `
openapi: 3.1.0
info:
  title: Test API v3.1
  version: 1.0.0
paths:
  /health:
    get:
      summary: Health check
`
	doc, err := ValidateOpenAPI([]byte(data))
	if err != nil {
		t.Fatalf("valid 3.1 OpenAPI: %v", err)
	}
	if doc.Version != "3.1" {
		t.Errorf("Version = %q, want 3.1", doc.Version)
	}
}

func TestValidateOpenAPIErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			"missing openapi field",
			"info:\n  title: Test\npaths: {}\n",
		},
		{
			"unsupported version",
			"openapi: 2.0\ninfo:\n  title: Test\npaths: {}\n",
		},
		{
			"missing info",
			"openapi: 3.0.0\npaths: {}\n",
		},
		{
			"missing title",
			"openapi: 3.0.0\ninfo:\n  version: 1.0.0\npaths: {}\n",
		},
		{
			"missing paths",
			"openapi: 3.0.0\ninfo:\n  title: Test\n  version: 1.0.0\n",
		},
		{
			"external ref",
			`openapi: 3.0.0
info:
  title: Test
  version: 1.0.0
paths:
  /items:
    get:
      responses:
        "200":
          $ref: "https://external.com/schema.json"
`,
		},
		{
			"duplicate operationId",
			`openapi: 3.0.0
info:
  title: Test
  version: 1.0.0
paths:
  /a:
    get:
      operationId: sameId
  /b:
    get:
      operationId: sameId
`,
		},
		{
			"path without leading slash",
			`openapi: 3.0.0
info:
  title: Test
  version: 1.0.0
paths:
  items:
    get:
      summary: List
`,
		},
		{
			"path with traversal",
			`openapi: 3.0.0
info:
  title: Test
  version: 1.0.0
paths:
  /items/../secret:
    get:
      summary: Secret
`,
		},
		{
			"invalid YAML",
			"openapi: 3.0.0\ninfo:\n  title: [invalid\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateOpenAPI([]byte(tt.yaml))
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestValidateOpenAPISizeLimit(t *testing.T) {
	// Create a YAML document just over the limit.
	big := make([]byte, 1<<20+1)
	for i := range big {
		big[i] = 'x'
	}
	_, err := ValidateOpenAPI(big)
	if err == nil {
		t.Error("expected error for oversized YAML")
	}
}

func TestMatchPathTemplate(t *testing.T) {
	tests := []struct {
		template, path string
		want           bool
	}{
		{"/items", "/items", true},
		{"/items/{id}", "/items/123", true},
		{"/items/{id}/sub", "/items/abc/sub", true},
		{"/items/{id}", "/items", false},
		{"/items/{id}", "/items/", false},
		{"/items", "/other", false},
		{"/a/{b}/{c}", "/a/1/2", true},
		{"/a/{b}/{c}", "/a/1", false},
	}
	for _, tt := range tests {
		got := matchPathTemplate(tt.template, tt.path)
		if got != tt.want {
			t.Errorf("matchPathTemplate(%q, %q) = %v, want %v", tt.template, tt.path, got, tt.want)
		}
	}
}
