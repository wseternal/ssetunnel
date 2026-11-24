package shardstore_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/wseternal/ssetunnel/internal/shardstore"
)

func TestResolvePath(t *testing.T) {
	tests := []struct {
		key    string
		shard  string
		object string
	}{
		{
			key:    "hello",
			shard:  "2c/f2",
			object: "2c/f2/2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		},
		{
			key:    "user-1",
			shard:  "c6/c2",
			object: "c6/c2/c6c289e49e9c05b2145860387b73bcb18df43fb09a1e4a4a9713c76c88bb541b",
		},
		{
			key:    "ключ",
			shard:  "1d/e3",
			object: "1d/e3/1de36a32af798da0c1ac9297603a320ed8fe567cf21c9177112a4ce914ebb8be",
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			shardDir, objectPath, err := shardstore.ResolvePath(tt.key)
			if err != nil {
				t.Fatalf("resolve %q: %v", tt.key, err)
			}
			if shardDir != tt.shard {
				t.Errorf("shard dir for %q = %q, want %q", tt.key, shardDir, tt.shard)
			}
			if objectPath != tt.object {
				t.Errorf("object path for %q = %q, want %q", tt.key, objectPath, tt.object)
			}
		})
	}
}

func TestResolvePathEmpty(t *testing.T) {
	shardDir, objectPath, err := shardstore.ResolvePath("")

	var emptyErr *shardstore.EmptyKeyError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("empty key error = %v, want *shardstore.EmptyKeyError", err)
	}
	if emptyErr.Error() == "" {
		t.Error("empty key error message is blank")
	}
	if shardDir != "" || objectPath != "" {
		t.Errorf("empty key returned paths %q and %q, want empty strings", shardDir, objectPath)
	}
}

func TestResolvePathShape(t *testing.T) {
	keys := []string{
		"a",
		"0",
		".",
		"..",
		"../escape",
		"/leading",
		"trailing/",
		"with space",
		`back\slash`,
		"КЛЮЧ",
		"\x00control",
		strings.Repeat("k", 4096),
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			shardDir, objectPath, err := shardstore.ResolvePath(key)
			if err != nil {
				t.Fatalf("resolve %q: %v", key, err)
			}

			shardParts := strings.Split(shardDir, "/")
			if len(shardParts) != 2 {
				t.Fatalf("shard dir for %q = %q, want two components", key, shardDir)
			}
			for _, part := range shardParts {
				if len(part) != 2 {
					t.Errorf("shard component %q has %d characters, want 2", part, len(part))
				}
				assertLowerHex(t, key, part)
			}

			name := strings.TrimPrefix(objectPath, shardDir+"/")
			if len(name) != 64 {
				t.Errorf("object name for %q has %d characters, want 64", key, len(name))
			}
			assertLowerHex(t, key, name)
			if !strings.HasPrefix(name, shardParts[0]+shardParts[1]) {
				t.Errorf("object name %q does not start with %q", name, shardParts[0]+shardParts[1])
			}

			shardAgain, objectAgain, err := shardstore.ResolvePath(key)
			if err != nil {
				t.Fatalf("resolve %q again: %v", key, err)
			}
			if shardAgain != shardDir || objectAgain != objectPath {
				t.Errorf("resolve %q is not repeatable: %q/%q then %q/%q", key, shardDir, objectPath, shardAgain, objectAgain)
			}
		})
	}
}

func assertLowerHex(t *testing.T, key, value string) {
	t.Helper()
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Errorf("value %q for key %q contains %q, want lowercase hexadecimal", value, key, r)
			return
		}
	}
}
