package shardstore

import (
	"errors"
	"fmt"
	"io/fs"
)

// ShardStats reports object distribution over the populated leaves of a
// shard tree. AverageObjectsPerLeaf is zero when no leaf holds an object.
type ShardStats struct {
	LeafDirectories       int
	Objects               int
	MinObjectsPerLeaf     int
	MaxObjectsPerLeaf     int
	AverageObjectsPerLeaf float64
}

// ShardInspector reads distribution statistics from one store.
type ShardInspector struct {
	store *Store
}

// NewShardInspector returns an inspector for a store.
func NewShardInspector(store *Store) *ShardInspector {
	return &ShardInspector{store: store}
}

// Inspect walks the shard tree and returns its statistics. Inspect holds
// the store lock for the full scan, so it sees a stable state of calls
// through the store. A separate process can still change the tree, because
// the root belongs to the operator. An empty tree returns zero values.
// A closed store returns ErrStoreClosed.
//
// The scan holds the write lock, so every Get, Put, Exists, and Delete
// through the same store waits for it. Call Inspect off the request path.
func (i *ShardInspector) Inspect() (ShardStats, error) {
	if i.store == nil {
		return ShardStats{}, errors.New("shardstore: inspector has no store")
	}
	store := i.store
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.root == nil {
		return ShardStats{}, ErrStoreClosed
	}
	return inspectShardTree(store.root.FS())
}

// inspectShardTree counts objects per valid level-two leaf. It reads each
// directory once. It skips branches that are not two lowercase hexadecimal
// characters at either level, so temporary and malformed entries never
// change the result.
func inspectShardTree(fsys fs.FS) (ShardStats, error) {
	var stats ShardStats

	firstLevel, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return ShardStats{}, fmt.Errorf("shardstore: read root: %w", err)
	}
	for _, b0 := range firstLevel {
		if !isHexDirectory(b0, 2) {
			continue
		}
		secondLevel, err := fs.ReadDir(fsys, b0.Name())
		if err != nil {
			return ShardStats{}, fmt.Errorf("shardstore: read shard %s: %w", b0.Name(), err)
		}
		for _, b1 := range secondLevel {
			if !isHexDirectory(b1, 2) {
				continue
			}
			leaf := b0.Name() + "/" + b1.Name()
			count, err := countLeafObjects(fsys, leaf, b0.Name()+b1.Name())
			if err != nil {
				return ShardStats{}, err
			}
			if count == 0 {
				continue
			}
			stats.LeafDirectories++
			stats.Objects += count
			if stats.LeafDirectories == 1 || count < stats.MinObjectsPerLeaf {
				stats.MinObjectsPerLeaf = count
			}
			if count > stats.MaxObjectsPerLeaf {
				stats.MaxObjectsPerLeaf = count
			}
		}
	}

	if stats.LeafDirectories > 0 {
		stats.AverageObjectsPerLeaf = float64(stats.Objects) / float64(stats.LeafDirectories)
	}
	return stats, nil
}

// countLeafObjects counts the valid regular objects in one leaf. A valid
// object name has 64 lowercase hexadecimal characters, and its first two
// characters match the two shard directory names.
func countLeafObjects(fsys fs.FS, leaf, prefix string) (int, error) {
	entries, err := fs.ReadDir(fsys, leaf)
	if err != nil {
		return 0, fmt.Errorf("shardstore: read leaf %s: %w", leaf, err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if len(name) != 64 || !isLowerHex(name) {
			continue
		}
		if name[0:2] != prefix[0:2] || name[2:4] != prefix[2:4] {
			continue
		}
		count++
	}
	return count, nil
}

// isHexDirectory reports a directory whose name has n lowercase
// hexadecimal characters.
func isHexDirectory(entry fs.DirEntry, n int) bool {
	if !entry.IsDir() {
		return false
	}
	name := entry.Name()
	return len(name) == n && isLowerHex(name)
}

// isLowerHex reports a non-empty lowercase hexadecimal string.
func isLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
