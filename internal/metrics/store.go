package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// Store persists metric samples, tuning decisions, and rolling window
// state to BadgerDB. Keys are lexicographically sorted so prefix scans
// yield chronological results within an agent.
type Store struct {
	db *badger.DB
}

// OpenStore opens (or creates) a BadgerDB at dir for metrics storage.
// It pre-creates the directory if missing and removes stale zero-byte
// memtable WAL files left behind by an unclean shutdown. BadgerDB v4
// treats empty .mem files as a fatal error (returns the internal sentinel
// z.NewFile which openMemTables does not handle), preventing the database
// from opening at all.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create metrics dir %s: %w", dir, err)
	}
	// Clean up stale zero-byte .mem files from a prior unclean shutdown.
	// BadgerDB's openMemTables does not handle the z.NewFile sentinel
	// ("Create a new file") returned for empty memtable WALs, causing
	// a fatal "while opening memtables" error.
	if err := cleanStaleMemTables(dir); err != nil {
		return nil, fmt.Errorf("clean stale memtables in %s: %w", dir, err)
	}
	opts := badger.DefaultOptions(dir)
	opts.Logger = nil          // silence badger logs
	opts.SyncWrites = false    // metrics are not critical enough for fsync
	opts.CompactL0OnClose = true
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open badger %s: %w", dir, err)
	}
	return &Store{db: db}, nil
}

// cleanStaleMemTables removes zero-byte .mem files from dir.
// BadgerDB v4 writes memtable WALs as NNNNN.mem. If the process is killed
// mid-creation, an empty .mem file may remain; on next open, BadgerDB
// returns the z.NewFile sentinel which openMemTables treats as fatal.
// Removing these files is safe: they contain no data.
func cleanStaleMemTables(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory may not exist yet on first run; let badger handle it.
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mem") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > 0 {
			continue
		}
		// Empty .mem file — safe to remove.
		if rmErr := os.Remove(filepath.Join(dir, e.Name())); rmErr != nil && !os.IsNotExist(rmErr) {
			return rmErr
		}
	}
	return nil
}

// Close closes the underlying BadgerDB.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// sampleKey returns the BadgerDB key for a metric sample:
// m:<agentID>:<unix_nano_zero_padded>
func sampleKey(agentID string, t time.Time) []byte {
	return []byte(fmt.Sprintf("m:%s:%020d", agentID, t.UnixNano()))
}

// samplePrefix returns the key prefix for scanning all samples of an agent.
func samplePrefix(agentID string) []byte {
	return []byte("m:" + agentID + ":")
}

// decisionKey returns the BadgerDB key for a tuning decision:
// t:<agentID>:<unix_nano_zero_padded>
func decisionKey(agentID string, t time.Time) []byte {
	return []byte(fmt.Sprintf("t:%s:%020d", agentID, t.UnixNano()))
}

// decisionPrefix returns the key prefix for scanning all decisions of an agent.
func decisionPrefix(agentID string) []byte {
	return []byte("t:" + agentID + ":")
}

// windowKey returns the BadgerDB key for a rolling window state.
func windowKey(agentID string) []byte {
	return []byte("w:" + agentID)
}

// WriteSamples persists a batch of metric samples in a single transaction.
func (s *Store) WriteSamples(samples []MetricSample) error {
	if s == nil || len(samples) == 0 {
		return nil
	}
	return s.db.Update(func(txn *badger.Txn) error {
		for _, sample := range samples {
			data, err := json.Marshal(sample)
			if err != nil {
				return fmt.Errorf("marshal sample: %w", err)
			}
			if err := txn.Set(sampleKey(sample.AgentID, sample.Timestamp), data); err != nil {
				return fmt.Errorf("set sample: %w", err)
			}
		}
		return nil
	})
}

// QuerySamples returns metric samples for an agent within [from, to].
func (s *Store) QuerySamples(agentID string, from, to time.Time) ([]MetricSample, error) {
	if s == nil {
		return nil, nil
	}
	var samples []MetricSample
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = samplePrefix(agentID)
		it := txn.NewIterator(opts)
		defer it.Close()

		// Seek to the first key >= from
		seekKey := sampleKey(agentID, from)
		for it.Seek(seekKey); it.Valid(); it.Next() {
			item := it.Item()
			// Stop if past the "to" bound
			key := string(item.Key())
			if !strings.HasPrefix(key, string(samplePrefix(agentID))) {
				break
			}
			// Parse timestamp from key to check upper bound
			ts := parseTimestampFromKey(key)
			if ts.After(to) {
				break
			}
			val, err := item.ValueCopy(nil)
			if err != nil {
				return fmt.Errorf("copy sample value: %w", err)
			}
			var sample MetricSample
			if err := json.Unmarshal(val, &sample); err != nil {
				return fmt.Errorf("unmarshal sample: %w", err)
			}
			samples = append(samples, sample)
		}
		return nil
	})
	return samples, err
}

// WriteDecision persists one tuning decision.
func (s *Store) WriteDecision(d TuningDecision) error {
	if s == nil {
		return nil
	}
	data, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("marshal decision: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(decisionKey(d.AgentID, d.Timestamp), data)
	})
}

// QueryDecisions returns up to `limit` recent tuning decisions for an agent
// (newest first). If agentID is empty, returns decisions for all agents.
func (s *Store) QueryDecisions(agentID string, limit int) ([]TuningDecision, error) {
	if s == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var decisions []TuningDecision
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = true
		if agentID != "" {
			opts.Prefix = decisionPrefix(agentID)
		} else {
			opts.Prefix = []byte("t:")
		}
		it := txn.NewIterator(opts)
		defer it.Close()

		// Seek to end of prefix
		seekKey := append(opts.Prefix, 0xFF)
		for it.Seek(seekKey); it.Valid() && len(decisions) < limit; it.Next() {
			item := it.Item()
			key := string(item.Key())
			if !strings.HasPrefix(key, string(opts.Prefix)) {
				break
			}
			val, err := item.ValueCopy(nil)
			if err != nil {
				return fmt.Errorf("copy decision value: %w", err)
			}
			var d TuningDecision
			if err := json.Unmarshal(val, &d); err != nil {
				return fmt.Errorf("unmarshal decision: %w", err)
			}
			decisions = append(decisions, d)
		}
		return nil
	})
	return decisions, err
}

// PruneOlderThan deletes all samples and decisions older than retention.
func (s *Store) PruneOlderThan(retention time.Duration) error {
	if s == nil {
		return nil
	}
	cutoff := time.Now().Add(-retention)
	return s.db.Update(func(txn *badger.Txn) error {
		// Prune samples: scan all m: keys
		if err := prunePrefix(txn, "m:", cutoff); err != nil {
			return err
		}
		// Prune decisions: scan all t: keys
		return prunePrefix(txn, "t:", cutoff)
	})
}

// prunePrefix deletes keys under prefix whose timestamp is before cutoff.
func prunePrefix(txn *badger.Txn, prefix string, cutoff time.Time) error {
	opts := badger.DefaultIteratorOptions
	opts.Prefix = []byte(prefix)
	opts.PrefetchValues = false // only need keys
	it := txn.NewIterator(opts)
	defer it.Close()

	for it.Seek([]byte(prefix)); it.Valid(); it.Next() {
		item := it.Item()
		key := string(item.Key())
		if !strings.HasPrefix(key, prefix) {
			break
		}
		ts := parseTimestampFromKey(key)
		if ts.IsZero() || ts.After(cutoff) {
			continue
		}
		if err := txn.Delete(item.KeyCopy(nil)); err != nil {
			return fmt.Errorf("delete expired key: %w", err)
		}
	}
	return nil
}

// WriteWindow persists the rolling window state for an agent.
func (s *Store) WriteWindow(agentID string, state []byte) error {
	if s == nil {
		return nil
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(windowKey(agentID), state)
	})
}

// ReadWindow reads the rolling window state for an agent.
// Returns nil, nil if no state exists.
func (s *Store) ReadWindow(agentID string) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(windowKey(agentID))
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get window: %w", err)
		}
		data, err = item.ValueCopy(nil)
		return err
	})
	return data, err
}

// parseTimestampFromKey extracts the unix nano timestamp from a key like
// "m:agentID:00000000001234567890" or "t:agentID:00000000001234567890".
func parseTimestampFromKey(key string) time.Time {
	// Find the last colon
	idx := strings.LastIndex(key, ":")
	if idx < 0 || idx+1 >= len(key) {
		return time.Time{}
	}
	tsStr := key[idx+1:]
	var nano int64
	for _, c := range tsStr {
		if c < '0' || c > '9' {
			return time.Time{}
		}
		nano = nano*10 + int64(c-'0')
	}
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}
