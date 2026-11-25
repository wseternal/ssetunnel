package server

import (
	"strings"
	"testing"

	"github.com/wseternal/ssetunnel/sdk"
)

func TestValidateObjectKey(t *testing.T) {
	valid := []string{
		"a",
		"blob.bin",
		"A-Z_0.9",
		strings.Repeat("k", sdk.MaxObjectKeyLen),
	}
	for _, key := range valid {
		if err := sdk.ValidateObjectKey(key); err != nil {
			t.Fatalf("ValidateObjectKey(%q) = %v, want nil", key, err)
		}
	}

	invalid := []struct {
		key  string
		note string
	}{
		{"", "empty"},
		{strings.Repeat("k", sdk.MaxObjectKeyLen+1), "too long"},
		{".hidden", "leading dot"},
		{"_leading", "leading underscore"},
		{"../escape", "traversal"},
		{"a/b", "path separator"},
		{`a\b`, "backslash"},
		{"a b", "space"},
		{"a\tb", "control byte"},
		{"-lead", "leading hyphen"},
		{"ключ", "non-ASCII"},
	}
	for _, c := range invalid {
		if err := sdk.ValidateObjectKey(c.key); err == nil {
			t.Fatalf("ValidateObjectKey(%q) accepted a %s key", c.key, c.note)
		}
	}
}

func TestValidateObjectModuleID(t *testing.T) {
	valid := []string{
		"com.example.store",
		"io.github.some-module",
		"a.b",
	}
	for _, id := range valid {
		if err := validateObjectModuleID(id); err != nil {
			t.Fatalf("validateObjectModuleID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []struct {
		id   string
		note string
	}{
		{"", "empty"},
		{"com", "single label"},
		{"Com.Example", "uppercase"},
		{"com.example../escape", "traversal"},
		{"com.example/store", "path separator"},
		{strings.Repeat("a", sdk.MaxModuleIDLen+1-len(".b")) + ".b", "too long"},
	}
	for _, c := range invalid {
		if err := validateObjectModuleID(c.id); err == nil {
			t.Fatalf("validateObjectModuleID(%q) accepted a %s ID", c.id, c.note)
		}
	}
}

func TestObjectFileNameIsStableAndFlat(t *testing.T) {
	key := "blob.bin"
	first := objectFileName(key)
	second := objectFileName(key)
	if first != second {
		t.Fatal("object file name is not stable")
	}
	if first != objectFileName("blob.bin") {
		t.Fatal("object file name is not deterministic")
	}
	if strings.ContainsAny(first, "/\\") {
		t.Fatalf("object file name %q contains a path separator", first)
	}
	if len(first) != 64 {
		t.Fatalf("object file name length = %d, want 64", len(first))
	}
	if first == objectFileName("blob.bin2") {
		t.Fatal("different keys map to the same file name")
	}
}
