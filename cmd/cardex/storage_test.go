package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/roots"
)

func TestProjectScratchDirFlagRoundTrip(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	scratch := t.TempDir()
	var out, errb bytes.Buffer
	code := Main([]string{"--home", home, "--json", "project", "add", "--id", "p1", "--repo", repo, "--scratch-dir", scratch}, &out, &errb)
	if code != 0 {
		t.Fatalf("add %d %s", code, errb.String())
	}
	var project model.Project
	if err := json.Unmarshal(out.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.ScratchDir != scratch {
		t.Fatalf("add scratch %q", project.ScratchDir)
	}
	out.Reset()
	errb.Reset()
	if code := Main([]string{"--home", home, "--json", "project", "show", "p1"}, &out, &errb); code != 0 {
		t.Fatalf("show %d %s", code, errb.String())
	}
	var shown model.Project
	if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.ScratchDir != scratch {
		t.Fatalf("show scratch %q", shown.ScratchDir)
	}
	out.Reset()
	errb.Reset()
	if code := Main([]string{"--home", home, "--json", "project", "set", "--id", "p1", "--scratch-dir", ""}, &out, &errb); code != 0 {
		t.Fatalf("set %d %s", code, errb.String())
	}
	var cleared model.Project
	if err := json.Unmarshal(out.Bytes(), &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.ScratchDir != "" {
		t.Fatalf("cleared scratch %q", cleared.ScratchDir)
	}
	help := bytes.Buffer{}
	if code := Main([]string{"project", "help"}, &help, io.Discard); code != 0 || !strings.Contains(help.String(), "--scratch-dir") {
		t.Fatalf("group help %d\n%s", code, help.String())
	}
	help.Reset()
	if code := Main([]string{"project", "add", "--help"}, &help, io.Discard); code != 0 || !strings.Contains(help.String(), "scratch-dir") {
		t.Fatalf("add help %d\n%s", code, help.String())
	}
}

func TestDoctorReportsStorageAndDoesNotDelete(t *testing.T) {
	home := t.TempDir()
	backups := filepath.Join(t.TempDir(), "backups")
	attempts := filepath.Join(t.TempDir(), "attempts")
	scratch := t.TempDir()
	marker := filepath.Join(scratch, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(map[string]string{
		"backups_dir": backups, "attempts_dir": attempts, "scratch_dir": scratch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	projScratch := filepath.Join(t.TempDir(), "not-created")
	var errb bytes.Buffer
	if code := Main([]string{"--home", home, "project", "add", "--id", "p1", "--repo", repo, "--scratch-dir", projScratch}, io.Discard, &errb); code != 0 {
		t.Fatalf("project %d %s", code, errb.String())
	}
	before, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	prev := roots.FreeWarnBytes
	roots.FreeWarnBytes = ^uint64(0)
	t.Cleanup(func() { roots.FreeWarnBytes = prev })
	code, out, errText := callCLI(t, []string{"--home", home, "doctor"})
	if code != 0 {
		t.Fatalf("doctor %d\n%s\n%s", code, out, errText)
	}
	for name, path := range map[string]string{
		"backups": backups, "attempts": attempts, "scratch": scratch, "scratch:p1": projScratch,
	} {
		if !strings.Contains(out, "storage: name="+name+" path="+path+" volume=") {
			t.Fatalf("missing %s at %s\n%s", name, path, out)
		}
	}
	if !strings.Contains(out, "volume=") || !strings.Contains(out, "free=") {
		t.Fatalf("volume line missing\n%s", out)
	}
	if !strings.Contains(out, "below") {
		t.Fatalf("warning missing\n%s", out)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "keep" {
		t.Fatalf("marker %q %v", b, err)
	}
	for _, dir := range []string{backups, attempts, projScratch} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("doctor created %s: %v", dir, err)
		}
	}
	after, err := os.ReadDir(home)
	if err != nil || len(after) != len(before) {
		t.Fatalf("doctor changed home entries from %v to %v: %v", before, after, err)
	}
	for i := range before {
		if before[i].Name() != after[i].Name() {
			t.Fatalf("doctor replaced %s with %s", before[i].Name(), after[i].Name())
		}
	}
}

func TestDoctorDoesNotInitializeHomeOrDatabase(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-created")
	code, out, errText := callCLI(t, []string{"--home", home, "doctor"})
	if code != 0 || !strings.Contains(out, "storage: name=attempts") || !strings.Contains(out, "database cannot be inspected") {
		t.Fatalf("doctor %d\n%s\n%s", code, out, errText)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("doctor created home: %v", err)
	}
}
