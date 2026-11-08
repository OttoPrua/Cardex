package positioning

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAllowsEmptyHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAllowsTasksWhenDBExists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	h, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks", "a.json"), []byte(`{"id":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err = Open(dir)
	if err != nil {
		t.Fatalf("tasks plus cardex.db: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAllowsDirectoryNamedCardex(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	dir := filepath.Join(root, ".cardex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}
