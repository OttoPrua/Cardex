package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCardImportRefusesMutationWhenConfiguredBackupFails(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "not-a-directory"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"not-a-directory"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = WriteCardImport(context.Background(), path, func(context.Context, *Store) error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "backup before card import") || called {
		t.Fatalf("write=%t err=%v", called, err)
	}
}
