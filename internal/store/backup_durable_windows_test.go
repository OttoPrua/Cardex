package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsBackupDirectoryMustBePrecreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing", "backup-dir")
	if err := prepareBackupDestination(dir); err == nil || !strings.Contains(err.Error(), "pre-create") {
		t.Fatalf("missing configured backup path: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created a directory without confirming durability: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepareBackupDestination(dir); err != nil {
		t.Fatal(err)
	}
}
