package sdk

import (
	"net/http"
	"strings"
	"testing"
)

func TestManifestValidate(t *testing.T) {
	valid := Manifest{
		ModuleID:       "com.example.mymodule",
		ServiceName:    "my-service",
		ServiceVersion: "1.0.0",
		OpenAPIYAML:    []byte("openapi: 3.0.0\n"),
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid manifest: got error %v", err)
	}

	tests := []struct {
		name string
		mod  func(Manifest) Manifest
	}{
		{"empty module ID", func(m Manifest) Manifest { m.ModuleID = ""; return m }},
		{"bad module ID format", func(m Manifest) Manifest { m.ModuleID = "Bad-Name"; return m }},
		{"single-segment module ID", func(m Manifest) Manifest { m.ModuleID = "singlename"; return m }},
		{"empty service name", func(m Manifest) Manifest { m.ServiceName = ""; return m }},
		{"empty service version", func(m Manifest) Manifest { m.ServiceVersion = ""; return m }},
		{"empty openapi yaml", func(m Manifest) Manifest { m.OpenAPIYAML = nil; return m }},
		{
			"module ID too long",
			func(m Manifest) Manifest { m.ModuleID = "com." + strings.Repeat("a", 200); return m },
		},
		{
			"revision too long",
			func(m Manifest) Manifest { m.Revision = strings.Repeat("r", 100); return m },
		},
		{
			"openapi yaml too large",
			func(m Manifest) Manifest { m.OpenAPIYAML = make([]byte, MaxOpenAPISize+1); return m },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.mod(valid)
			if err := m.Validate(); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestModuleRefString(t *testing.T) {
	tests := []struct {
		ref  ModuleRef
		want string
	}{
		{ModuleRef{AgentID: "a", ModuleID: "m"}, "a/m"},
		{ModuleRef{AgentID: "a", ModuleID: "m", Epoch: "e1"}, "a/m@e1"},
		{ModuleRef{AgentID: "a", ModuleID: "m", Epoch: "e1", Revision: "r1"}, "a/m@e1:r1"},
		{ModuleRef{AgentID: "a", ModuleID: "m", Revision: "r1"}, "a/m:r1"},
	}
	for _, tt := range tests {
		got := tt.ref.String()
		if got != tt.want {
			t.Errorf("ModuleRef(%+v).String() = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestUIDescriptorValidate(t *testing.T) {
	valid := UIDescriptor{
		Kind:          UIProvider,
		RemoteName:    "com.example.mymodule",
		PageURL:       "/modules/mymodule",
		PackageDigest: strings.Repeat("a", 64),
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid descriptor: got error %v", err)
	}

	tests := []struct {
		name string
		mod  func(UIDescriptor) UIDescriptor
	}{
		{"invalid kind", func(d UIDescriptor) UIDescriptor { d.Kind = "unknown"; return d }},
		{"empty remote name", func(d UIDescriptor) UIDescriptor { d.RemoteName = ""; return d }},
		{"empty page URL", func(d UIDescriptor) UIDescriptor { d.PageURL = ""; return d }},
		{"page URL no leading slash", func(d UIDescriptor) UIDescriptor { d.PageURL = "modules/x"; return d }},
		{"provider without digest", func(d UIDescriptor) UIDescriptor { d.PackageDigest = ""; return d }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.mod(valid)
			if err := d.Validate(); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}

	// Consumer UI does not require a digest.
	consumer := UIDescriptor{Kind: UIConsumer, RemoteName: "cons", PageURL: "/cons"}
	if err := consumer.Validate(); err != nil {
		t.Errorf("consumer descriptor: got error %v", err)
	}
}

func TestAssetEntryValidate(t *testing.T) {
	valid := AssetEntry{
		Path:   "assets/index.js",
		Size:   1024,
		Digest: strings.Repeat("b", 64),
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid asset: got error %v", err)
	}

	tests := []struct {
		name string
		mod  func(AssetEntry) AssetEntry
	}{
		{"empty path", func(a AssetEntry) AssetEntry { a.Path = ""; return a }},
		{"absolute path", func(a AssetEntry) AssetEntry { a.Path = "/abs"; return a }},
		{"traversal path", func(a AssetEntry) AssetEntry { a.Path = "../escape"; return a }},
		{"zero size", func(a AssetEntry) AssetEntry { a.Size = 0; return a }},
		{"oversized asset", func(a AssetEntry) AssetEntry { a.Size = MaxAssetSize + 1; return a }},
		{"empty digest", func(a AssetEntry) AssetEntry { a.Digest = ""; return a }},
		{"short digest", func(a AssetEntry) AssetEntry { a.Digest = "abc"; return a }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.mod(valid)
			if err := a.Validate(); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestDigestAsset(t *testing.T) {
	d := DigestAsset([]byte("hello"))
	if len(d) != 64 {
		t.Errorf("DigestAsset length = %d, want 64", len(d))
	}
	// SHA-256 of "hello" is well-known.
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if d != want {
		t.Errorf("DigestAsset(\"hello\") = %s, want %s", d, want)
	}
}

func TestModuleErrorFormat(t *testing.T) {
	e := &ModuleError{Type: ErrTypeTransport, Title: "connection refused", Detail: "host unreachable"}
	got := e.Error()
	if !strings.Contains(got, "transport") || !strings.Contains(got, "connection refused") {
		t.Errorf("Error() = %q, missing type or title", got)
	}
	if !e.IsRetryable() {
		t.Error("transport error should be retryable")
	}
	if e.NeedsRediscovery() {
		t.Error("transport error should not need rediscovery")
	}

	re := &ModuleError{Type: ErrTypeRegistry, Title: "module not found"}
	if re.IsRetryable() {
		t.Error("registry error should not be retryable")
	}
	if !re.NeedsRediscovery() {
		t.Error("registry error should need rediscovery")
	}
}

func TestParseErrorResponse(t *testing.T) {
	// Nil response.
	e := ParseErrorResponse(nil)
	if e.Type != ErrTypeTransport {
		t.Errorf("nil response: got type %q, want transport", e.Type)
	}

	// Non-JSON response.
	resp := &http.Response{
		StatusCode: 500,
		Header:     http.Header{"X-Correlation-Id": []string{"abc123"}},
		Body:       http.NoBody,
	}
	e = ParseErrorResponse(resp)
	if e.Status != 500 {
		t.Errorf("status = %d, want 500", e.Status)
	}

	// RFC 7807 JSON response.
	body := strings.NewReader(`{"type":"registry/not-found","title":"Not Found","detail":"module gone","status":404}`)
	resp = &http.Response{
		StatusCode: 404,
		Header:     http.Header{"X-Correlation-Id": []string{"xyz"}},
		Body:       nopCloser{body},
	}
	e = ParseErrorResponse(resp)
	if e.Type != ErrTypeRegistry {
		t.Errorf("RFC 7807: got type %q, want registry", e.Type)
	}
	if e.Title != "Not Found" {
		t.Errorf("RFC 7807: got title %q, want \"Not Found\"", e.Title)
	}
	if e.CorrelationID != "xyz" {
		t.Errorf("correlation ID = %q, want \"xyz\"", e.CorrelationID)
	}
}

type nopCloser struct {
	*strings.Reader
}

func (nopCloser) Close() error { return nil }

func TestConsumerNewConsumer(t *testing.T) {
	c := NewConsumer("http://example.com/", "tok", nil)
	if c.serverURL != "http://example.com" {
		t.Errorf("serverURL = %q, want trailing slash trimmed", c.serverURL)
	}
	if c.httpClient == nil {
		t.Error("httpClient should not be nil")
	}
}
