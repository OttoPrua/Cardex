package homeguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTasksJSONWithoutDBIsV1(t *testing.T) {
	dir := t.TempDir()
	writeTasksJSON(t, dir, "a.json", []byte(`{"id":"a"}`))
	err := Check(dir)
	if err == nil || !IsV1(err) || !strings.Contains(err.Error(), "import-legacy") || !strings.Contains(err.Error(), Usage) {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(err.Error(), "v1") {
		t.Fatalf("error %q", err.Error())
	}
}

func TestCardexDBClearsV1(t *testing.T) {
	dir := t.TempDir()
	writeTasksJSON(t, dir, "a.json", []byte(`{"id":"a"}`))
	if err := os.WriteFile(filepath.Join(dir, "cardex.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Check(dir); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyDirIsNotV1(t *testing.T) {
	if err := Check(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestTasksWithoutJSONIsNotV1(t *testing.T) {
	dir := t.TempDir()
	writeTasksJSON(t, dir, "readme.txt", []byte("not a record"))
	if err := Check(dir); err != nil {
		t.Fatal(err)
	}
}

func TestNestedJSONIsNotV1(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "tasks", "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "hidden.json"), []byte(`{"id":"h"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Check(dir); err != nil {
		t.Fatal(err)
	}
}

func TestChildOfV1DirIsNotRefused(t *testing.T) {
	dir := t.TempDir()
	writeTasksJSON(t, dir, "a.json", []byte(`{"id":"a"}`))
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Check(child); err != nil {
		t.Fatal(err)
	}
}

func TestMissingDirIsNotV1(t *testing.T) {
	if err := Check(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}

func writeTasksJSON(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks", name), body, 0o644); err != nil {
		t.Fatal(err)
	}
}
