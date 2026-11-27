package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestOpenForWriteCapturesCommittedWAL(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dbPath := filepath.Join(home, "cardex.db")
	source, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"snapshots"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep the writer open so committed pages still live in the WAL.
	if err := source.CreateProject(ctx, &model.Project{ID: "in-wal", RepoPath: "repo-before"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dbPath + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no active WAL: %v", err)
	}
	prepareTestBackupOnWindows(t, filepath.Join(home, "snapshots"))
	writer, err := OpenForWrite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.UpdateProject(ctx, &model.Project{ID: "in-wal", RepoPath: "repo-after"}); err != nil {
		t.Fatal(err)
	}
	copies := dbCopies(t, filepath.Join(home, "snapshots"))
	if len(copies) != 1 {
		t.Fatalf("wanted one copy, got %v", copies)
	}
	if err := checkBackupIntegrity(ctx, copies[0]); err != nil {
		t.Fatal(err)
	}
	if err := Inspect(copies[0], func(frozen *Store) {
		p, err := frozen.GetProject(ctx, "in-wal")
		if err != nil || p.RepoPath != "repo-before" {
			t.Fatalf("backup missing committed WAL value: %+v %v", p, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(copies[0] + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("verification created a sidecar: %v", err)
		}
	}
}

func TestPublishBackupRejectsCorruptionAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bad := filepath.Join(home, "bad.db")
	if err := os.WriteFile(bad, []byte(strings.Repeat("not a database", 128)), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "kept.db")
	if err := publishBackupWith(ctx, bad, dest, systemBackupPersistence()); err == nil || !strings.Contains(err.Error(), "verify SQLite backup") {
		t.Fatalf("bad database verdict %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt backup published: %v", err)
	}
	valid := filepath.Join(home, "valid.db")
	db, err := Open(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("older backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishBackupWith(ctx, valid, dest, systemBackupPersistence()); err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination verdict %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "older backup" {
		t.Fatalf("older backup changed: %q %v", got, err)
	}
}

func TestOpenForWriteReusesMigrationSnapshot(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	path := filepath.Join(home, "cardex.db")
	source, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", latestSchema); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"snapshots"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prepareTestBackupOnWindows(t, filepath.Join(home, "snapshots"))
	writer, err := OpenForWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if version, err := writer.SchemaVersion(ctx); err != nil || version != latestSchema {
		t.Fatalf("migrated schema version %d: %v", version, err)
	}
	copies := dbCopies(t, filepath.Join(home, "snapshots"))
	if len(copies) != 1 {
		t.Fatalf("one backup should protect both migrations and the owner write, got %v", copies)
	}
	if err := Inspect(copies[0], func(frozen *Store) {
		if version, err := frozen.SchemaVersion(ctx); err != nil || version != latestSchema-1 {
			t.Fatalf("backup must precede migration, found schema version %d: %v", version, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenForWriteKeepsDeleteDatabaseUnchangedWhenBackupFails(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertDelete := func(set bool) {
		t.Helper()
		raw, err := sql.Open("sqlite", sqliteURI(path))
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		query := "PRAGMA journal_mode"
		if set {
			query += "=DELETE"
		}
		var mode string
		if err := raw.QueryRow(query).Scan(&mode); err != nil || mode != "delete" {
			t.Fatalf("journal mode %q: %v", mode, err)
		}
	}
	assertDelete(true)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "not-a-directory"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"not-a-directory"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenForWrite(path); got != nil || err == nil || !strings.Contains(err.Error(), "backup before database write") {
		if got != nil {
			got.Close()
		}
		t.Fatalf("backup failure returned a writeable handle: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed backup changed the legacy database bytes: %v", err)
	}
	assertDelete(false)
}

func TestOpenForWriteRequiresDurableSnapshot(t *testing.T) {
	injected := errors.New("injected durability failure")
	for _, stage := range []string{"parent", "file", "published-name"} {
		t.Run(stage, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "cardex.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.CreateProject(context.Background(), &model.Project{ID: "sample", RepoPath: "before"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"missing/parents/backups"}`), 0o600); err != nil {
				t.Fatal(err)
			}

			ops := systemBackupPersistence()
			var steps []string
			prepareTestBackupOnWindows(t, filepath.Join(home, "missing", "parents", "backups"))
			prepare, sync, publish := ops.prepare, ops.sync, ops.publish
			ops.prepare = func(path string) error {
				steps = append(steps, "parent")
				if stage == "parent" {
					return injected
				}
				return prepare(path)
			}
			ops.sync = func(path string) error {
				steps = append(steps, "file")
				if stage == "file" {
					return injected
				}
				return sync(path)
			}
			ops.publish = func(staged, dest string) error {
				steps = append(steps, "published-name")
				if err := publish(staged, dest); err != nil {
					return err
				}
				// Even a valid visible name is insufficient if its
				// persistence did not report success to the operation.
				return injected
			}
			got, err := openWithSnapshot(path, true, func(db *Store, ctx context.Context, dir string) error {
				return db.backupWith(ctx, dir, ops)
			})
			if got != nil || !errors.Is(err, injected) {
				if got != nil {
					got.Close()
				}
				t.Fatalf("durability failure returned a writeable handle: %v", err)
			}
			want := map[string]string{
				"parent":         "parent",
				"file":           "parent,file",
				"published-name": "parent,file,published-name",
			}[stage]
			if trace := strings.Join(steps, ","); trace != want {
				t.Fatalf("durability order %s, expected %s", trace, want)
			}
			if stage == "published-name" {
				copies := dbCopies(t, filepath.Join(home, "missing", "parents", "backups"))
				if len(copies) != 1 {
					t.Fatalf("retain already-published verified copy: %v", copies)
				}
				if err := checkBackupIntegrity(context.Background(), copies[0]); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func prepareTestBackupOnWindows(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}
