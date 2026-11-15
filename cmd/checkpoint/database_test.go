package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseSelectionForNativeAndContainer(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "setup-state.json")
	if got := resolveDatabasePath("", marker); got != "/data/checkpoint.db" {
		t.Fatalf("container default: %s", got)
	}
	if err := os.WriteFile(marker, []byte(`{"format":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := resolveDatabasePath("", marker); got != "/var/lib/checkpoint/checkpoint.db" {
		t.Fatalf("native default: %s", got)
	}
	if got := resolveDatabasePath("/custom/instance.db", marker); got != "/custom/instance.db" {
		t.Fatalf("explicit database must take precedence: %s", got)
	}
}
