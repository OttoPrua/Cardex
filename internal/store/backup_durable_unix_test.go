//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupAncestorSyncWhenAnotherWriterMadeDirectoryVisible(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "first")
	leaf := filepath.Join(first, "nested", "backups")
	visible := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	released := false
	defer func() {
		if !released {
			close(resume)
		}
	}()
	mkdir := func(path string) error { return os.Mkdir(path, 0o700) }
	go func() {
		done <- prepareBackupPath(leaf, func(path string) error {
			err := mkdir(path)
			if err == nil && path == first {
				// Writer A made its directory visible but has not called
				// sync on the directory entry's parent yet.
				close(visible)
				<-resume
			}
			return err
		}, syncBackupDir)
	}()
	select {
	case <-visible:
	case err := <-done:
		t.Fatalf("first writer did not create test directory: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first writer did not reach its pre-sync boundary")
	}

	// Writer B takes over while A is still stopped before syncing root.
	// A previous implementation would see first on disk and return early.
	bSynced := make(map[string]bool)
	if err := prepareBackupPath(leaf, mkdir, func(path string) error {
		bSynced[path] = true
		return syncBackupDir(path)
	}); err != nil {
		t.Fatal(err)
	}
	if !bSynced[root] || !bSynced[first] || !bSynced[filepath.Dir(leaf)] {
		t.Fatalf("second writer did not anchor all shared ancestor entries: %v", bSynced)
	}
	close(resume)
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first writer did not finish after the interleave")
	}
}

func TestBackupDirectoryAncestorsUseResolvedPath(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "physical")
	link := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	synced := map[string]bool{}
	if err := prepareBackupPath(filepath.Join(link, "backups"), func(path string) error {
		return os.Mkdir(path, 0o700)
	}, func(path string) error {
		synced[path] = true
		return syncBackupDir(path)
	}); err != nil {
		t.Fatal(err)
	}
	if !synced[real] || synced[link] {
		t.Fatalf("backups used an unanchored alias path: %v", synced)
	}
}
