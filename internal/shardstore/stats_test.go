package shardstore_test

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wseternal/ssetunnel/internal/shardstore"
)

// writeShardFixture writes one regular file into a shard directory of the
// real root. The fixture builds exact digests, so the inspector sees
// controlled names.
func writeShardFixture(t *testing.T, root, shardDir, name string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(shardDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create fixture directory %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture %s/%s: %v", shardDir, name, err)
	}
}

func TestShardInspectorEmpty(t *testing.T) {
	store, _ := newTestStore(t)

	stats, err := shardstore.NewShardInspector(store).Inspect()
	if err != nil {
		t.Fatalf("inspect empty tree: %v", err)
	}
	if stats != (shardstore.ShardStats{}) {
		t.Errorf("empty tree stats = %+v, want all zero values", stats)
	}
}

func TestShardInspectorStats(t *testing.T) {
	root := t.TempDir()
	store, err := shardstore.NewStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	filler := strings.Repeat("0", 60)

	// Leaf 00/00 holds three valid objects.
	writeShardFixture(t, root, "00/00", "0000"+filler)
	writeShardFixture(t, root, "00/00", "0000"+strings.Repeat("1", 60))
	writeShardFixture(t, root, "00/00", "0000"+strings.Repeat("2", 60))

	// Leaf 00/01 holds two valid objects.
	writeShardFixture(t, root, "00/01", "0001"+strings.Repeat("3", 60))
	writeShardFixture(t, root, "00/01", "0001"+strings.Repeat("4", 60))

	// Leaf ff/ff holds one valid object.
	writeShardFixture(t, root, "ff/ff", "ffff"+strings.Repeat("5", 60))

	// An empty leaf must not count.
	if err := os.MkdirAll(filepath.Join(root, "ab", "cd"), 0o700); err != nil {
		t.Fatalf("create empty leaf: %v", err)
	}

	// Invalid files must not count.
	writeShardFixture(t, root, "00/00", "0000"+strings.Repeat("A", 60)) // uppercase
	writeShardFixture(t, root, "00/00", ".0000"+filler+".tmp-deadbeef") // temporary
	writeShardFixture(t, root, "00/00", "0001"+strings.Repeat("6", 60)) // prefix mismatch
	writeShardFixture(t, root, "00/00", "0000"+strings.Repeat("7", 59)) // short name
	writeShardFixture(t, root, "00/00", "0000"+strings.Repeat("g", 60)) // not hexadecimal

	// Invalid depths and names must not count.
	writeShardFixture(t, root, "00", "stray.txt")
	writeShardFixture(t, root, "", "stray.bin")
	if err := os.MkdirAll(filepath.Join(root, "zz", "aa"), 0o700); err != nil {
		t.Fatalf("create invalid branch: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "00", "00", "0000"+strings.Repeat("8", 60), "nested"), 0o700); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}

	stats, err := shardstore.NewShardInspector(store).Inspect()
	if err != nil {
		t.Fatalf("inspect fixture: %v", err)
	}
	if stats.LeafDirectories != 3 {
		t.Errorf("leaf directories = %d, want 3", stats.LeafDirectories)
	}
	if stats.Objects != 6 {
		t.Errorf("objects = %d, want 6", stats.Objects)
	}
	if stats.MinObjectsPerLeaf != 1 {
		t.Errorf("minimum objects per leaf = %d, want 1", stats.MinObjectsPerLeaf)
	}
	if stats.MaxObjectsPerLeaf != 3 {
		t.Errorf("maximum objects per leaf = %d, want 3", stats.MaxObjectsPerLeaf)
	}
	if stats.AverageObjectsPerLeaf != 2 {
		t.Errorf("average objects per leaf = %v, want 2", stats.AverageObjectsPerLeaf)
	}
}

func TestShardDistribution(t *testing.T) {
	store, root := newTestStore(t)

	const keyCount = 10000
	for i := 1; i <= keyCount; i++ {
		key := "user-" + strconv.Itoa(i)
		if err := store.Put(key, []byte(key)); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
	}

	stats, err := shardstore.NewShardInspector(store).Inspect()
	if err != nil {
		t.Fatalf("inspect distribution: %v", err)
	}
	if stats.Objects != keyCount {
		t.Errorf("objects = %d, want %d", stats.Objects, keyCount)
	}
	if stats.LeafDirectories < 2000 {
		t.Errorf("leaf directories = %d, want at least 2000", stats.LeafDirectories)
	}

	leaves, objects := assertShardLayout(t, root)
	if objects != keyCount {
		t.Errorf("independent walk found %d objects, want %d", objects, keyCount)
	}
	if leaves != stats.LeafDirectories {
		t.Errorf("independent walk found %d leaves, inspector reported %d", leaves, stats.LeafDirectories)
	}

	wantAverage := float64(stats.Objects) / float64(stats.LeafDirectories)
	if math.Abs(stats.AverageObjectsPerLeaf-wantAverage) > 1e-9 {
		t.Errorf("average = %v, want %v", stats.AverageObjectsPerLeaf, wantAverage)
	}
	if stats.MinObjectsPerLeaf < 1 {
		t.Errorf("minimum objects per leaf = %d, want at least 1", stats.MinObjectsPerLeaf)
	}
	if stats.MaxObjectsPerLeaf < stats.MinObjectsPerLeaf {
		t.Errorf("maximum %d is below minimum %d", stats.MaxObjectsPerLeaf, stats.MinObjectsPerLeaf)
	}
	t.Logf("distribution: %d objects over %d leaves (min %d, max %d, average %.3f)",
		stats.Objects, stats.LeafDirectories, stats.MinObjectsPerLeaf,
		stats.MaxObjectsPerLeaf, stats.AverageObjectsPerLeaf)
}

func TestShardDistributionDepthInvariant(t *testing.T) {
	store, root := newTestStore(t)

	for _, key := range []string{"user-1", "user-2", "../escape", "/leading"} {
		if err := store.Put(key, []byte(key)); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
	}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		if depth == 1 || depth == 2 {
			t.Errorf("object %q is at depth %d, want depth 3", rel, depth)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk root: %v", err)
	}
}

func TestShardInspectorClosedStore(t *testing.T) {
	store, _ := newTestStore(t)
	inspector := shardstore.NewShardInspector(store)

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := inspector.Inspect(); err == nil {
		t.Error("inspect after close returned no error")
	}
}
