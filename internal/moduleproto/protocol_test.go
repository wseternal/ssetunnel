package moduleproto

import (
	"testing"
	"time"
)

func TestMarshalUnmarshalSnapshotEntries(t *testing.T) {
	entries := []SnapshotModuleEntry{
		{
			ModuleID:       "com.example.module1",
			ServiceName:    "service-one",
			ServiceVersion: "1.0.0",
			Revision:       "rev-1",
			OpenAPIYAML:    []byte("openapi: 3.0.0\ninfo:\n  title: Service One\n"),
			Health:         NewHealthEntry("healthy", "", time.Now()),
		},
		{
			ModuleID:       "com.example.module2",
			ServiceName:    "service-two",
			ServiceVersion: "2.1.0",
			OpenAPIYAML:    []byte("openapi: 3.1.0\ninfo:\n  title: Service Two\n"),
			Health:         NewHealthEntry("degraded", "high latency", time.Now()),
			UI: []UIEntry{
				{
					Kind:          "provider",
					RemoteName:    "com.example.module2",
					PageURL:       "/modules/module2",
					PackageDigest: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				},
			},
		},
	}

	data, err := MarshalSnapshotEntries(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("marshal produced empty output")
	}

	got, err := UnmarshalSnapshotEntries(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for i, g := range got {
		if g.ModuleID != entries[i].ModuleID {
			t.Errorf("entry[%d].ModuleID = %q, want %q", i, g.ModuleID, entries[i].ModuleID)
		}
		if g.ServiceVersion != entries[i].ServiceVersion {
			t.Errorf("entry[%d].ServiceVersion = %q, want %q", i, g.ServiceVersion, entries[i].ServiceVersion)
		}
		if string(g.OpenAPIYAML) != string(entries[i].OpenAPIYAML) {
			t.Errorf("entry[%d].OpenAPIYAML mismatch", i)
		}
	}
}

func TestUnmarshalSnapshotEntriesEmpty(t *testing.T) {
	got, err := UnmarshalSnapshotEntries(nil)
	if err != nil {
		t.Fatalf("unmarshal nil: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestUnmarshalSnapshotEntriesMalformed(t *testing.T) {
	_, err := UnmarshalSnapshotEntries([]byte(`{invalid json`))
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestErrorFormat(t *testing.T) {
	e := &Error{Code: ErrModuleNotFound, Message: "module com.example.gone is not registered"}
	got := e.Error()
	if got != "module_not_found: module com.example.gone is not registered" {
		t.Errorf("Error() = %q", got)
	}
}

func TestNewHealthEntry(t *testing.T) {
	now := time.Now()
	h := NewHealthEntry("healthy", "all good", now)
	if h.Status != "healthy" {
		t.Errorf("Status = %q, want healthy", h.Status)
	}
	if h.Message != "all good" {
		t.Errorf("Message = %q, want \"all good\"", h.Message)
	}
	if h.CheckedAt != now.UnixMilli() {
		t.Errorf("CheckedAt = %d, want %d", h.CheckedAt, now.UnixMilli())
	}
}

func TestLimitsConstants(t *testing.T) {
	// Verify key limits are reasonable.
	if MaxModulesPerAgent < 1 {
		t.Error("MaxModulesPerAgent must be positive")
	}
	if MaxOpenAPISize < 1024 {
		t.Error("MaxOpenAPISize must be at least 1 KiB")
	}
	if SnapshotChunkSize < 1024 {
		t.Error("SnapshotChunkSize must be at least 1 KiB")
	}
	if MaxPendingOpens < 1 {
		t.Error("MaxPendingOpens must be positive")
	}
	if EstablishmentDeadline < 1 {
		t.Error("EstablishmentDeadline must be at least 1 second")
	}
}
