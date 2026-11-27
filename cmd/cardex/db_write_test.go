package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/store"
)

func TestCLIWritePublishesVerifiedPreWriteBackup(t *testing.T) {
	home := t.TempDir()
	backupName := "backups # verified"
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"backups # verified"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := Main(append([]string{"--home", home}, args...), &stdout, &stderr)
		return code, stderr.String()
	}
	if code, stderr := run("project", "add", "--id", "sample", "--repo", t.TempDir(), "--test-cmd", "first"); code != 0 {
		t.Fatalf("initial add: %d %q", code, stderr)
	}
	if code, stderr := run("project", "show", "sample"); code != 0 {
		t.Fatalf("read: %d %q", code, stderr)
	}
	backups := filepath.Join(home, backupName)
	if _, err := os.Stat(backups); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new database or read created backup directory: %v", err)
	}
	if runtime.GOOS == "windows" {
		if err := os.Mkdir(backups, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if code, stderr := run("project", "set", "--id", "sample", "--test-cmd", "second"); code != 0 {
		t.Fatalf("owner write: %d %q", code, stderr)
	}
	entries, err := os.ReadDir(backups)
	if err != nil || len(entries) != 1 {
		t.Fatalf("backup entries: %v %v", entries, err)
	}
	first := filepath.Join(backups, entries[0].Name())
	checkBefore := func(path, expect string) {
		t.Helper()
		if err := store.Inspect(path, func(db *store.Store) {
			got, err := db.GetProject(context.Background(), "sample")
			if err != nil || got.TestCmd != expect {
				t.Fatalf("snapshot wanted %q, got %q: %v", expect, got.TestCmd, err)
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	checkBefore(first, "first")
	original, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if code, stderr := run("project", "set", "--id", "sample", "--test-cmd", "third"); code != 0 {
		t.Fatalf("second write: %d %q", code, stderr)
	}
	entries, err = os.ReadDir(backups)
	if err != nil || len(entries) != 2 {
		t.Fatalf("backup entries: %v %v", entries, err)
	}
	after, err := os.ReadFile(first)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("prior backup was replaced: %v", err)
	}
	for _, entry := range entries {
		if path := filepath.Join(backups, entry.Name()); path != first {
			checkBefore(path, "second")
		}
	}
}

func TestCLIWriteFailsBeforeMutationWhenBackupDestinationUnavailable(t *testing.T) {
	home := t.TempDir()
	db, err := openStore(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	occupied := filepath.Join(home, "occupied")
	if err := os.WriteFile(occupied, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"occupied"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"--home", home, "project", "add", "--id", "not-written", "--repo", t.TempDir()}
	if code := Main(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "backup before database write") || stdout.Len() != 0 {
		t.Fatalf("unexpected success or diagnostic: %d %q %q", code, stdout.String(), stderr.String())
	}
	if rows, err := db.ListProjects(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("database changed after blocked backup: %v %v", rows, err)
	}
	if got, err := os.ReadFile(occupied); err != nil || string(got) != "untouched" {
		t.Fatalf("backup destination changed: %q %v", got, err)
	}
}
