package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestMain drops CARDEX_MANAGED_EXECUTOR before any test runs. CLI tests in
// this package open a temporary store and act as an external management
// session; a parent executor may export the marker, and inheriting it turns
// those writes into the nested-card refusal. TestManagedExecutorCardWrite
// sets the marker itself for the refusal cases.
func TestMain(m *testing.M) {
	if code, handled := runLiveSnapshotWorker(os.Args[1:]); handled {
		os.Exit(code)
	}
	if code, handled := snapshotTestManifest(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Unsetenv("CARDEX_MANAGED_EXECUTOR")
	os.Exit(m.Run())
}

func TestManagedExecutorCardWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_MANAGED_EXECUTOR", "1")

	var stdout, stderr bytes.Buffer
	code := Main([]string{"--home", home, "version"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "cardex") {
		t.Fatalf("version inside managed executor: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--home", home, "card"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "card —") {
		t.Fatalf("card help: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--home", home, "card", "add", "--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "card add") || !strings.Contains(stdout.String(), "create a card") {
		t.Fatalf("card add help: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--home", home, "card", "cancel", "--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "card cancel") {
		t.Fatalf("card cancel help: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}

	writes := []struct {
		name string
		args []string
	}{
		{"plain add", []string{"--home", home, "card", "add", "--id", "c-nest", "--objective", "o", "--title", "nested", "--tier", "T2"}},
		{"title help", []string{"--home", home, "card", "add", "--id", "c-help", "--objective", "o", "--title", "help", "--tier", "T2"}},
		{"title --help", []string{"--home", home, "card", "add", "--id", "c-flag", "--objective", "o", "--title", "--help", "--tier", "T2"}},
		{"title=--help", []string{"--home", home, "card", "add", "--id", "c-eq", "--objective", "o", "--title=--help", "--tier", "T2"}},
		{"cancel id help", []string{"--home", home, "card", "cancel", "help"}},
		{"merged note --help", []string{"--home", home, "card", "merged", "--note", "--help", "c-missing"}},
	}
	for _, write := range writes {
		stdout.Reset()
		stderr.Reset()
		code = Main(write.args, &stdout, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "CardeX-managed executor") || !strings.Contains(stderr.String(), "management session") {
			t.Fatalf("%s: code %d stdout %q stderr %q", write.name, code, stdout.String(), stderr.String())
		}
	}
	for _, argv := range [][]string{{"host", "a-synthetic"}, {"serve"}} {
		stdout.Reset()
		stderr.Reset()
		code = Main(append([]string{"--home", home}, argv...), &stdout, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "cannot start a CardeX scheduler or host") {
			t.Fatalf("nested runtime %v: code %d stdout %q stderr %q", argv, code, stdout.String(), stderr.String())
		}
	}
	if ids := cardIDs(t, home); len(ids) != 0 {
		t.Fatalf("managed card writes created %v", ids)
	}

	t.Setenv("CARDEX_MANAGED_EXECUTOR", "")
	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--home", home, "card", "add", "--id", "c-ext", "--objective", "o", "--title", "external", "--tier", "T2"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "c-ext") {
		t.Fatalf("external card add: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}

	t.Setenv("CARDEX_MANAGED_EXECUTOR", "1")
	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--home", home, "card", "list"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "c-ext") || strings.Contains(stdout.String(), "c-nest") {
		t.Fatalf("managed card list: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--home", home, "card", "show", "c-ext"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "c-ext") {
		t.Fatalf("managed card show: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func cardIDs(t *testing.T, home string) []string {
	t.Helper()
	db, err := openStore(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	items, err := db.ListCards(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}
