// Package shardstore stores objects on a local filesystem under a
// two-level SHA-256 shard tree. Every object lives at
// <root>/<b0>/<b1>/<digest>, where digest is the lowercase hexadecimal
// SHA-256 of the exact key bytes and b0, b1 are its first two byte
// pairs. The layout spreads one flat namespace over 65,536 leaf
// directories, and the digest keeps every caller key out of path
// construction.
package shardstore

import (
	"crypto/sha256"
	"encoding/hex"
)

// EmptyKeyError reports an empty key. The store accepts every key with
// at least one byte and rejects the empty string before any filesystem
// access.
type EmptyKeyError struct{}

// Error implements the error interface.
func (e *EmptyKeyError) Error() string { return "shardstore: key is empty" }

// ResolvePath returns the shard directory and object path for a key.
// The paths are slash-separated relative paths, identical on every
// operating system. Callers convert them with filepath.FromSlash
// before a filesystem call. An empty key returns *EmptyKeyError.
func ResolvePath(key string) (shardDir, objectPath string, err error) {
	shardDir, name, err := resolveKey(key)
	if err != nil {
		return "", "", err
	}
	return shardDir, shardDir + "/" + name, nil
}

// resolveKey returns the shard directory and the object file name for
// a key. The name is the full digest, never the caller key.
func resolveKey(key string) (shardDir, name string, err error) {
	if key == "" {
		return "", "", &EmptyKeyError{}
	}
	sum := sha256.Sum256([]byte(key))
	name = hex.EncodeToString(sum[:])
	return name[0:2] + "/" + name[2:4], name, nil
}
