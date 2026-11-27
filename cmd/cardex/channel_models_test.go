package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

type previewWire struct {
	Before   []string `json:"before"`
	After    []string `json:"after"`
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	IfModels string   `json:"if_models"`
	DryRun   bool     `json:"dry_run"`
}

func TestChannelModelDelta(t *testing.T) {
	home := t.TempDir()
	call := func(args ...string) (int, string, string) {
		t.Helper()
		return runMain(t, append([]string{"--home", home, "--json", "channel", "set", "--id", "synthetic"}, args...)...)
	}
	expect := func(args ...string) previewWire {
		t.Helper()
		c, out, errText := call(args...)
		var w previewWire
		if c != 0 || errText != "" || json.Unmarshal([]byte(out), &w) != nil {
			t.Fatalf("code=%d out=%s err=%s", c, out, errText)
		}
		return w
	}
	models := func(want ...string) {
		t.Helper()
		db, err := store.Open(filepath.Join(home, "cardex.db"))
		if err != nil {
			t.Fatal(err)
		}
		ch, err := db.GetChannel(t.Context(), "synthetic")
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil || !reflect.DeepEqual(want, ch.Models) {
			t.Fatalf("models=%v want=%v err=%v", ch.Models, want, err)
		}
	}
	if c, _, e := call("--runner", "grok", "--model", "alpha", "--model", "beta"); c != 0 {
		t.Fatal(e)
	}
	path := filepath.Join(home, "cardex.db")
	base, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p := expect("--dry-run", "--add-model", "beta", "--add-model", "gamma", "--add-model", "gamma", "--remove-model", "alpha")
	if !p.DryRun || !reflect.DeepEqual(p.Before, []string{"alpha", "beta"}) || !reflect.DeepEqual(p.After, []string{"beta", "gamma"}) ||
		!reflect.DeepEqual(p.Added, []string{"gamma"}) || !reflect.DeepEqual(p.Removed, []string{"alpha"}) || len(p.IfModels) != 64 {
		t.Fatalf("%+v", p)
	}
	got, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(base, got) {
		t.Fatalf("preview changed DB: %v", err)
	}
	for _, s := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + s); !os.IsNotExist(err) {
			t.Fatalf("preview created sidecar %s: %v", s, err)
		}
	}
	for _, args := range [][]string{
		{"--dry-run", "--add-model", "gamma", "--remove-model", "gamma"},
		{"--dry-run", "--model", "replacement", "--add-model", "gamma"},
		{"--dry-run", "--add-model", "  "},
		{"--dry-run", "--remove-model", "unknown", "--add-model", "gamma"},
		{"--add-model", "gamma"},
		{"--if-models", p.IfModels, "--add-model", "gamma", "--concurrency", "10"},
	} {
		if c, _, e := call(args...); c == 0 || e == "" {
			t.Fatalf("invalid operation accepted: %v", args)
		}
	}
	// Legacy flag.Parse stops at positional operands. A misplaced positional
	// argument must not hide the explicit preview flag and cause a write.
	if c, out, e := call("--model", "accidental-write", "extra", "--dry-run"); c == 0 || out != "" || !strings.Contains(e, "exactly one channel id") {
		t.Fatalf("mistaken preview: %d %q %q", c, out, e)
	}
	if c, out, e := runMain(t, "--home", home, "channel", "set", "synthetic", "--dry-run", "--add-model", "gamma"); c != 0 || e != "" || !strings.Contains(out, "Preview only") {
		t.Fatalf("positional preview: %d %q %q", c, out, e)
	}
	models("alpha", "beta")
	if c, _, e := call("--model", "different"); c != 0 {
		t.Fatal(e)
	}
	if c, _, e := call("--if-models", p.IfModels, "--add-model", "gamma"); c == 0 || !strings.Contains(e, "changed") {
		t.Fatalf("stale preview accepted: %d %s", c, e)
	}
	models("different")
	p = expect("--dry-run", "--add-model", "gamma", "--remove-model", "different")
	applied := expect("--if-models", p.IfModels, "--add-model", "gamma", "--remove-model", "different")
	if applied.DryRun || !reflect.DeepEqual(applied.After, p.After) {
		t.Fatalf("applied=%+v preview=%+v", applied, p)
	}
	models("gamma")
	replacement := expect("--dry-run", "--model", "delta", "--model", "delta")
	if !reflect.DeepEqual(replacement.After, []string{"delta", "delta"}) {
		t.Fatalf("replacement=%+v", replacement)
	}
	models("gamma")
	if c, _, e := call("--model", "delta", "--model", "delta"); c != 0 {
		t.Fatal(e)
	}
	models("delta", "delta")
}

func TestChannelModelPreviewRefusesLiveAndNeverInitializes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	preview := func() (int, string, string) {
		return runMain(t, "--home", home, "channel", "set", "--id", "synthetic", "--dry-run", "--add-model", "new")
	}
	if c, _, _ := preview(); c == 0 {
		t.Fatal("preview accepted missing home")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("preview created home: %v", err)
	}
	home = t.TempDir()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ch := model.Channel{ID: "synthetic", Runner: model.RunnerGrok, Health: model.HealthOK, Models: []string{"existing"}}
	if err := db.CreateChannel(t.Context(), &ch); err != nil {
		t.Fatal(err)
	}
	if c, o, e := preview(); c == 0 || o != "" || !strings.Contains(e, "checkpointed") {
		t.Fatalf("live result: %d %q %q", c, o, e)
	}
}

func TestChannelModelPreviewRequiresWorkingBackupToApply(t *testing.T) {
	home := t.TempDir()
	call := func(args ...string) (int, string, string) {
		t.Helper()
		return runMain(t, append([]string{"--home", home, "--json", "channel", "set", "--id", "synthetic"}, args...)...)
	}
	if c, _, e := call("--runner", "grok", "--concurrency", "4", "--model", "alpha"); c != 0 {
		t.Fatal(e)
	}
	occupied := filepath.Join(home, "protected")
	if err := os.WriteFile(occupied, []byte("backup target exists as a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"protected"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var preview previewWire
	if c, out, e := call("--dry-run", "--add-model", "beta"); c != 0 || e != "" || json.Unmarshal([]byte(out), &preview) != nil {
		t.Fatalf("invalid preview: %d %q %q", c, out, e)
	}
	if c, out, e := call("--if-models", preview.IfModels, "--add-model", "beta"); c == 0 || out != "" || !strings.Contains(e, "backup before database write") {
		t.Fatalf("apply ignored unavailable backup: %d %q %q", c, out, e)
	}
	if got, err := os.ReadFile(occupied); err != nil || string(got) != "backup target exists as a file" {
		t.Fatalf("target changed: %q, %v", got, err)
	}
	state, err := store.PreviewChannelModels(t.Context(), filepath.Join(home, "cardex.db"), "synthetic")
	if err != nil || !reflect.DeepEqual(state.Models, []string{"alpha"}) {
		t.Fatalf("failed backup allowed edit: %v %v", state.Models, err)
	}
	if err := os.Remove(occupied); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(occupied, 0o700); err != nil {
		t.Fatal(err)
	}
	if c, _, e := call("--if-models", preview.IfModels, "--add-model", "beta"); c != 0 {
		t.Fatal(e)
	}
	entries, err := os.ReadDir(occupied)
	if err != nil || len(entries) != 1 {
		t.Fatalf("pre-write backup missing: %v %v", entries, err)
	}
	prior, err := store.PreviewChannelModels(t.Context(), filepath.Join(occupied, entries[0].Name()), "synthetic")
	if err != nil || !reflect.DeepEqual(prior.Models, []string{"alpha"}) {
		t.Fatalf("incorrect backup state: %v %v", prior.Models, err)
	}
	current, err := store.PreviewChannelModels(t.Context(), filepath.Join(home, "cardex.db"), "synthetic")
	if err != nil || !reflect.DeepEqual(current.Models, []string{"alpha", "beta"}) {
		t.Fatalf("incorrect model list: %v %v", current.Models, err)
	}
	if c, out, e := runMain(t, "--home", home, "--json", "channel", "show", "synthetic"); c != 0 || e != "" || !strings.Contains(out, `"concurrency": 4`) {
		t.Fatalf("edit touched unrelated fields: %d %q %q", c, out, e)
	}
}

func TestChannelModelPreviewTokenIsScopedToHomeAndProposedChange(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	call := func(h string, flags ...string) (int, string, string) {
		return runMain(t, append([]string{"--home", h, "--json", "channel", "set", "--id", "synthetic"}, flags...)...)
	}
	for _, h := range []string{home, other} {
		if code, _, errText := call(h, "--runner", "grok", "--model", "alpha"); code != 0 {
			t.Fatal(errText)
		}
	}
	var p previewWire
	if code, out, errText := call(home, "--dry-run", "--add-model", "beta"); code != 0 || json.Unmarshal([]byte(out), &p) != nil {
		t.Fatalf("preview failed: %d %s", code, errText)
	}
	for _, item := range []struct{ home, model string }{{other, "beta"}, {home, "gamma"}} {
		if code, out, errText := call(item.home, "--if-models", p.IfModels, "--add-model", item.model); code == 0 || out != "" || !strings.Contains(errText, "changed") {
			t.Fatalf("reused token: %d %q %q", code, out, errText)
		}
	}
	for _, h := range []string{home, other} {
		got, err := store.PreviewChannelModels(t.Context(), filepath.Join(h, "cardex.db"), "synthetic")
		if err != nil || !reflect.DeepEqual(got.Models, []string{"alpha"}) {
			t.Fatalf("token changed wrong list: %q %v", got.Models, err)
		}
	}
}

func TestChannelModelEditRefusesCompatibleBinding(t *testing.T) {
	home := t.TempDir()
	writeEngineConfig(t, home, `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","default_model":"model-1","credential":{"source":"env","name":"CARDEX_FIXTURE_VENDOR_A"}}}}`)
	call := func(args ...string) (int, string, string) {
		t.Helper()
		return runMain(t, append([]string{"--home", home, "--json", "channel", "set", "--id", "api-a"}, args...)...)
	}
	if code, out, errText := call("--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"); code != 0 {
		t.Fatalf("stage: %d %s %s", code, out, errText)
	}
	before := channelOf(t, home, "api-a")
	want := store.ErrCompatibleChannelModelEdit.Error()
	for _, args := range [][]string{
		{"--dry-run", "--add-model", "unauthorized-model"},
		{"--dry-run", "--model", "model-1"},
		{"--add-model", "unauthorized-model", "--if-models", strings.Repeat("a", 64)},
	} {
		if code, out, errText := call(args...); code == 0 || out != "" || !strings.Contains(errText, want) {
			t.Fatalf("restricted edit valid: %d %q %q", code, out, errText)
		}
	}
	withDB(t, home, func(db *store.Store) {
		if err := db.UpdateChannelModels(t.Context(), "api-a", []string{"unauthorized-model"}); !errors.Is(err, store.ErrCompatibleChannelModelEdit) {
			t.Fatalf("direct model write: %v", err)
		}
	})
	if got := channelOf(t, home, "api-a"); !reflect.DeepEqual(got, before) {
		t.Fatalf("API binding/model/health changed: before=%+v after=%+v", before, got)
	}
	if code, out, errText := call("--model", "opus"); code == 0 || !strings.Contains(errText, "tier alias") {
		t.Fatalf("API full path no longer validated: %d %q %q", code, out, errText)
	}
	// API1's dedicated validation path still allows a real model ID to be staged.
	if code, out, errText := call("--model", "model-2"); code != 0 {
		t.Fatalf("validated replace: %d %q %q", code, out, errText)
	}
	got := channelOf(t, home, "api-a")
	if got.EngineProfile == before.EngineProfile || got.Health != model.HealthDegraded || !reflect.DeepEqual(got.Models, []string{"model-2"}) {
		t.Fatalf("validated restaging: %+v", got)
	}
}
