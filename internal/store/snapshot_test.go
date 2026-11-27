package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func closedSnapshotHome(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func readSnapshotFixture(ctx context.Context, path, card string) (Snapshot, error) {
	if runtime.GOOS == "linux" {
		return ReadSnapshot(ctx, path, card)
	}
	return ReadCheckpointedCopy(ctx, path, card)
}

func withSnapshotFixtureView(ctx context.Context, path string, read func(context.Context, *sql.Tx) error) error {
	return withSnapshotViewPolicy(ctx, path, read, runtime.GOOS == "linux")
}

type stableEntry struct {
	mode os.FileMode
	mod  time.Time
	hash [32]byte
}

func snapshotManifest(t *testing.T, dir string) map[string]stableEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]stableEntry{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = stableEntry{info.Mode(), info.ModTime(), sha256.Sum256(raw)}
	}
	return out
}

func TestReadSnapshotCleanDoesNotMutate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "literal %20 # ?")
	// ? is not a legal Windows path component.
	if os.PathSeparator == '\\' {
		dir = filepath.Join(filepath.Dir(dir), "literal %20 #")
	}
	path := filepath.Join(dir, "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotManifest(t, dir)

	for range 2 {
		got, err := readSnapshotFixture(context.Background(), path, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Schema != latestSchema || got.Card != nil || got.Attempt != nil || len(got.Events) != 0 {
			t.Fatalf("unexpected snapshot: %+v", got)
		}
	}
	if after := snapshotManifest(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("snapshot changed the source: before %v, after %v", before, after)
	}
}

func TestReadSnapshotRefusesLiveWALAndMissingSHMWithoutMutation(t *testing.T) {
	ctx := context.Background()
	path := closedSnapshotHome(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`CREATE TABLE committed_probe (id INT); INSERT INTO committed_probe VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT count(*) FROM committed_probe`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("writer did not commit: %d, %v", n, err)
	}
	defer db.Close()
	before := snapshotManifest(t, filepath.Dir(path))
	if _, ok := before["cardex.db-wal"]; !ok {
		t.Fatal("fixture did not establish a live WAL")
	}
	got, err := readSnapshotFixture(ctx, path, "")
	if !errors.Is(err, ErrSnapshotUnavailable) || !reflect.DeepEqual(got, Snapshot{}) {
		t.Fatalf("live source yielded %+v, %v", got, err)
	}
	if after := snapshotManifest(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot changed live WAL/SHM or their database")
	}

	// Model a copied WAL whose -shm is absent. Do not remove live files.
	copyPath := closedSnapshotHome(t)
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath+"-wal", wal, 0o600); err != nil {
		t.Fatal(err)
	}
	before = snapshotManifest(t, filepath.Dir(copyPath))
	if _, err := readSnapshotFixture(ctx, copyPath, ""); !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("missing-SHM WAL yielded %v", err)
	}
	if after := snapshotManifest(t, filepath.Dir(copyPath)); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot changed source with a missing SHM")
	}
}

func TestReadSnapshotRefusesMissingAndBusyGuard(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing-home", "cardex.db")
	if _, err := readSnapshotFixture(ctx, missing, ""); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("missing home: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("snapshot created missing home: %v", err)
	}
	path := closedSnapshotHome(t)
	lock := path + ".lock"
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshotFixture(ctx, path, ""); !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("uncoordinated source: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("snapshot recreated a missing lock: %v", err)
	}

	unlock, err := lockExclusive(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := readSnapshotFixture(ctx, path, ""); !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("busy source: %v", err)
	}
}

func TestReadSnapshotRejectsInodeReplacementAfterRead(t *testing.T) {
	path := closedSnapshotHome(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".replacement"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	err = withSnapshotFixtureView(context.Background(), path, func(ctx context.Context, tx *sql.Tx) error {
		var schema int
		if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema); err != nil || schema != latestSchema {
			t.Fatalf("read pinned snapshot: %d, %v", schema, err)
		}
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
		original, err := os.ReadFile(path + ".original")
		if err != nil || !bytes.Equal(raw, original) {
			t.Fatalf("view unexpectedly changed old source: %v", err)
		}
		return nil
	})
	if !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("same-bytes/new-inode must not yield data: %v", err)
	}
}

func TestReadSnapshotRejectsWALAppearingDuringRead(t *testing.T) {
	path := closedSnapshotHome(t)
	var external *sql.DB
	err := withSnapshotFixtureView(context.Background(), path, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		var err error
		external, err = sql.Open("sqlite", sqliteURI(path)+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := external.Exec(`CREATE TABLE new_external_write(id INT)`); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if external != nil {
		defer external.Close()
	}
	if !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("uncoordinated WAL appearance must discard result: %v", err)
	}
}

func TestReadSnapshotRejectsOldSchemaWithoutMigration(t *testing.T) {
	path := closedSnapshotHome(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM schema_migrations WHERE version = ?`, latestSchema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotManifest(t, filepath.Dir(path))
	if _, err := readSnapshotFixture(context.Background(), path, ""); !errors.Is(err, ErrSnapshotSchema) {
		t.Fatalf("old schema: %v", err)
	}
	if after := snapshotManifest(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot changed the legacy source")
	}
}
