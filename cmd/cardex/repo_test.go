package main

import (
	"os"
	"path/filepath"
	"testing"
)

// testRepoPath locates repository files without changing the package working
// directory used by source scans, templates and testdata fixtures.
func testRepoPath(t *testing.T, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("cannot locate repository root for %s", rel)
		}
		dir = parent
	}
}
