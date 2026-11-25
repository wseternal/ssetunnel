package sdk

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// ObjectStore stores and fetches module-scoped binary objects. The server
// backs each object with a file in its local object storage root. Modules
// reach the store through the agent IPC endpoint, and the agent overwrites
// the module ID with the authenticated caller identity.
//
// A missing object returns an error that satisfies errors.Is(err,
// ErrObjectNotFound). Get returns a non-nil empty slice for a found empty
// object. Put replaces an existing object and has last-writer-wins
// semantics.
type ObjectStore interface {
	// Put stores data under key in the moduleID namespace.
	Put(ctx context.Context, moduleID, key string, data []byte) error

	// Get returns the object stored under key in the moduleID namespace.
	Get(ctx context.Context, moduleID, key string) ([]byte, error)
}

const (
	// MaxObjectSize caps one object at 4 MiB.
	MaxObjectSize = 4 << 20

	// MaxObjectKeyLen caps one object key at 128 bytes.
	MaxObjectKeyLen = 128
)

// ErrObjectNotFound reports a missing object.
var ErrObjectNotFound = errors.New("object not found")

// objectKeyRegexp matches a flat object key: one leading alphanumeric
// character, then alphanumeric characters, dots, underscores, or hyphens.
var objectKeyRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateObjectKey reports whether key is a valid flat object key. Keys
// contain 1 to 128 ASCII bytes. They must not contain path separators,
// whitespace, control bytes, or traversal forms.
func ValidateObjectKey(key string) error {
	if key == "" {
		return errors.New("object key is empty")
	}
	if len(key) > MaxObjectKeyLen {
		return fmt.Errorf("object key too long: %d bytes (max %d)", len(key), MaxObjectKeyLen)
	}
	if !objectKeyRegexp.MatchString(key) {
		return errors.New("object key must match [A-Za-z0-9][A-Za-z0-9._-]*")
	}
	return nil
}
