package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestProjectScratchDirRoundTrip(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	project := model.Project{
		ID:           "p1",
		RepoPath:     "/repo",
		WorktreeRoot: "/wt",
		ScratchDir:   "  /Volumes/scratch  ",
	}
	if err := db.CreateProject(ctx, &project); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetProject(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ScratchDir != "/Volumes/scratch" {
		t.Fatalf("scratch %q", got.ScratchDir)
	}
	got.ScratchDir = ""
	if err := db.UpdateProject(ctx, &got); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetProject(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ScratchDir != "" {
		t.Fatalf("cleared scratch %q", got.ScratchDir)
	}
}

func TestBackupRunsOnlyOnRemigration(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "cardex.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(home, "bak")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backups, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if copies := dbCopies(t, home); len(copies) != 0 {
		t.Fatalf("unset backups_dir wrote %v", copies)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"bak"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dropSchemaVersion(t, dbPath, 8)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	copies := dbCopies(t, backups)
	if len(copies) != 1 {
		t.Fatalf("copies %v", copies)
	}
	copied, err := os.ReadFile(copies[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != string(before) {
		t.Fatalf("copy len %d source len %d", len(copied), len(before))
	}
	still, err := os.ReadFile(dbPath)
	if err != nil || len(still) == 0 {
		t.Fatalf("source missing after backup: %v len %d", err, len(still))
	}
	if b, err := os.ReadFile(filepath.Join(backups, "keep.txt")); err != nil || string(b) != "keep" {
		t.Fatalf("sibling %q %v", b, err)
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if again := dbCopies(t, backups); len(again) != 1 {
		t.Fatalf("second open added copies %v", again)
	}
}

func dropSchemaVersion(t *testing.T, dbPath string, version int) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DELETE FROM schema_migrations WHERE version=?`, version); err != nil {
		t.Fatal(err)
	}
}

func dbCopies(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if strings.HasSuffix(name, "-cardex.db") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
