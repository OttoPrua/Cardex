package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

func TestCardImportRejectsUnactivatedStateBeforeBackup(t *testing.T) {
	for _, fixture := range []struct{ name, statement, reason string }{
		{"bare v15", "DROP TABLE tier_semantics_activation", "valid tier activation"},
		{"wrong v15", "UPDATE tier_semantics_activation SET convention='wrong'", "valid tier activation"},
		{"legacy v14", "DELETE FROM schema_migrations WHERE version=15", "requires schema"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			db := openDB(t)
			path := db.path
			home := filepath.Dir(path)
			if _, err := db.db.Exec(fixture.statement); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"snapshots"}`), 0600); err != nil {
				t.Fatal(err)
			}
			before := snapshotManifest(t, home)
			callback := func(context.Context, *Store) error { t.Fatal("unactivated batch reached callback"); return nil }
			if err := ReadCardImportState(ctx, path, callback); err == nil || !strings.Contains(err.Error(), fixture.reason) {
				t.Fatalf("preview accepted invalid state: %v", err)
			}
			if got := snapshotManifest(t, home); !reflect.DeepEqual(before, got) {
				t.Fatal("preview changed source")
			}
			if err := WriteCardImport(ctx, path, callback); err == nil || !strings.Contains(err.Error(), fixture.reason) {
				t.Fatalf("apply accepted invalid state: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, "snapshots")); !os.IsNotExist(err) {
				t.Fatalf("unactivated import reached backup: %v", err)
			}
		})
	}
}
