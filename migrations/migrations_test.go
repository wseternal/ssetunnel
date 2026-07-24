package migrations_test

import (
	"testing"

	"github.com/wseternal/ssetunnel/migrations"
)

func TestMigrationsFS(t *testing.T) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	foundSQL := false
	foundSum := false
	for _, entry := range entries {
		if entry.Name() == "20260724000001_initial_schema.sql" {
			foundSQL = true
		}
		if entry.Name() == "atlas.sum" {
			foundSum = true
		}
	}

	if !foundSQL {
		t.Errorf("expected 20260724000001_initial_schema.sql in embedded migrations.FS")
	}
	if !foundSum {
		t.Errorf("expected atlas.sum in embedded migrations.FS")
	}
}
