package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRefusesV1Parent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeTaskJSON(t, dir)
	dbPath := filepath.Join(dir, "cardex.db")
	if _, err := Open(dbPath); err == nil || !strings.Contains(err.Error(), "import-legacy") {
		t.Fatalf("Open v1 parent: %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("Open created cardex.db in a v1 home")
	}
}

func TestOpenAllowsEmptyAndDBNextToTasks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	empty := t.TempDir()
	db, err := Open(filepath.Join(empty, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	opened, err := Open(filepath.Join(dir, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	writeTaskJSON(t, dir)
	again, err := Open(filepath.Join(dir, "cardex.db"))
	if err != nil {
		t.Fatalf("tasks plus cardex.db: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAllowsPathNamedCardex(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	dir := filepath.Join(root, ".cardex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTaskJSON(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks", "a.json"), []byte(`{"id":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}
