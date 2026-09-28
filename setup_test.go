package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupFirstRunAndPreservesExistingConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new root")
	if err := setupWithInput([]string{"-root", root, "-runner", "codex", "-bin", "missing-codex-for-test"}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(root, cfg, typeSequence, "hello", t.TempDir(), []string{"hello"}, 0)
	if task.PreferRunner != "codex" || resolveCodexModel(cfg, task) != "" || resolveCodexReasoning(cfg, task) != "medium" {
		t.Fatalf("unexpected onboarding route: %+v", task)
	}
	if doctorNeedsClaude(cfg) {
		t.Fatal("Codex-only setup must not require Claude")
	}
	before, _ := os.ReadFile(configPath(root))
	if err := setupWithInput([]string{"-root", root, "-runner", "claude"}, strings.NewReader("")); err == nil {
		t.Fatal("existing config overwritten")
	}
	after, _ := os.ReadFile(configPath(root))
	if !bytes.Equal(before, after) {
		t.Fatal("existing config changed")
	}
}

func TestSetupInteractiveAndInvalidInput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new")
	if err := setupWithInput([]string{"-root", root}, strings.NewReader("\n")); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultRunner != "claude" || cfg.TypeDefaults[typeSequence].Model != "sonnet" {
		t.Fatal("wrong default")
	}
	root = filepath.Join(t.TempDir(), "untouched")
	for _, in := range []string{"", "invalid\n"} {
		if err := setupWithInput([]string{"-root", root}, strings.NewReader(in)); err == nil {
			t.Fatal("expected input error")
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("invalid setup created root")
	}
}
