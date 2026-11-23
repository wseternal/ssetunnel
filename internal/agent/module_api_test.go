package agent

import (
	"fmt"
	"testing"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
)

// TestContractForBypassesCacheWithoutRevision proves that a module which does
// not manage revisions is revalidated after its OpenAPI document changes.
// Caching an empty revision would keep serving the old contract because no
// revision change would ever invalidate it.
func TestContractForBypassesCacheWithoutRevision(t *testing.T) {
	s := &moduleAPIServer{}
	mod := &moduleproto.SnapshotModuleEntry{
		ModuleID:    "io.test.cache",
		OpenAPIYAML: []byte(openAPIWithPath("/first")),
	}

	first, err := s.contractFor(mod)
	if err != nil {
		t.Fatalf("first contract: %v", err)
	}
	if _, ok := first.validated.Operations["GET /first"]; !ok {
		t.Fatal("first contract does not contain GET /first")
	}

	mod.OpenAPIYAML = []byte(openAPIWithPath("/second"))
	second, err := s.contractFor(mod)
	if err != nil {
		t.Fatalf("second contract: %v", err)
	}
	if _, ok := second.validated.Operations["GET /second"]; !ok {
		t.Error("second contract does not contain GET /second")
	}
	if _, ok := second.validated.Operations["GET /first"]; ok {
		t.Error("second contract still contains the cached GET /first")
	}
}

func openAPIWithPath(path string) string {
	return fmt.Sprintf(`openapi: 3.0.3
info:
  title: Cache test
  version: 1.0.0
paths:
  %s:
    get:
      summary: test
`, path)
}
