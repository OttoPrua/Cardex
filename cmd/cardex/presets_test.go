package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresetImportDispatchOverrideAndPreservation(t *testing.T) {
	root := t.TempDir()
	if err := setupWithInput([]string{"-root", root, "-runner", "codex", "-bin", "test-codex"}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile(configPath(root))
	var raw map[string]any
	_ = json.Unmarshal(config, &raw)
	raw["local_extension"] = map[string]any{"preserve": true}
	config, _ = json.Marshal(raw)
	if err := os.WriteFile(configPath(root), config, 0600); err != nil {
		t.Fatal(err)
	}
	imported := filepath.Join(t.TempDir(), "presets.json")
	if err := os.WriteFile(imported, []byte(`{"routine":{"runner":"codex","model":"test-model","effort":"high","review_after":true,"note":"catalog checked"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmdPresets([]string{"-root", root, "-file", imported}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(configPath(root))
	if !bytes.Contains(after, []byte("local_extension")) {
		t.Fatal("lost config extension")
	}
	backups, _ := filepath.Glob(filepath.Join(root, "config.before-presets-*.json"))
	if len(backups) != 1 {
		t.Fatal("missing backup")
	}
	backup, _ := os.ReadFile(backups[0])
	if !bytes.Equal(config, backup) {
		t.Fatal("backup changed")
	}
	work := t.TempDir()
	for _, override := range []bool{false, true} {
		args := []string{"-root", root, "-dir", work, "-preset", "routine", "-hold"}
		if override {
			args = append(args, "-codex-model", "manual-model", "-effort", "low", "-review-after=false")
		}
		args = append(args, "implement")
		if err := cmdAdd(args); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := loadTasks(root)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("cards=%d err=%v", len(tasks), err)
	}
	for _, tk := range tasks {
		if tk.PreferRunner != "codex" {
			t.Fatal("wrong runner")
		}
		if tk.CodexModel == "manual-model" {
			if tk.Effort != "low" || tk.ReviewAfter {
				t.Fatal("manual override lost")
			}
		} else if tk.CodexModel != "test-model" || tk.Effort != "high" || !tk.ReviewAfter {
			t.Fatalf("preset not applied: %+v", tk)
		}
	}
	if err := cmdAdd([]string{"-root", root, "-dir", work, "-preset", "routine", "-runner", "claude", "x"}); err == nil {
		t.Fatal("cross-runner override accepted")
	}
	cfg, _ := loadConfig(root)
	cfg.DispatchPresets["routine"] = DispatchPreset{Runner: "codex", Model: "replacement"}
	_ = saveConfig(root, cfg)
	again, _ := loadTasks(root)
	for _, tk := range again {
		if tk.CodexModel == "replacement" {
			t.Fatal("preset edit mutated admitted task")
		}
	}
}

func TestPresetInvalidImportDoesNotChangeConfig(t *testing.T) {
	root := testRoot(t)
	cfg := defaultConfig("claude")
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath(root))
	file := filepath.Join(t.TempDir(), "input.json")
	for _, body := range []string{`{"routine":{"runner":"codex"}}`, `{"routine":{"runner":"claude","skip_permissions":true}}`, `{"routine":{"runner":"claude"}} {}`, `null`} {
		_ = os.WriteFile(file, []byte(body), 0600)
		if err := cmdPresets([]string{"-root", root, "-file", file}); err == nil {
			t.Fatalf("invalid import accepted: %s", body)
		}
		after, _ := os.ReadFile(configPath(root))
		if !bytes.Equal(before, after) {
			t.Fatal("invalid import changed config")
		}
	}
}

func TestPresetNativeModelFlagMapping(t *testing.T) {
	for _, runner := range []string{"claude", "codex", "grok-build", "cursor", "kimi-cli", "agy", "opencode"} {
		t.Run(runner, func(t *testing.T) {
			root := testRoot(t)
			cfg := defaultConfig("claude")
			cfg.CodexBin = "codex"
			cfg.GrokBuildBin = "grok"
			cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "high"}
			cfg.CursorBin = "cursor-agent"
			cfg.KimiCLIBin = "kimi"
			cfg.AntigravityBin = "agy"
			cfg.Antigravity = &AntigravityRoute{Enabled: true, Effort: "high"}
			cfg.OpenCodeBin = "opencode"
			model := "native-model"
			if runner == "grok-build" {
				model = "grok-4.6"
			}
			cfg.DispatchPresets = map[string]DispatchPreset{"sample": {Runner: runner, Model: model}}
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			if err := cmdAdd([]string{"-root", root, "-dir", t.TempDir(), "-preset", "sample", "-hold", "inspect"}); err != nil {
				t.Fatal(err)
			}
			tasks, err := loadTasks(root)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks=%v err=%v", tasks, err)
			}
			task := tasks[0]
			got := ""
			switch runner {
			case "claude":
				got = task.Model
			case "codex":
				got = task.CodexModel
			case "grok-build":
				got = task.GrokModel
			case "cursor":
				got = task.CursorModel
			case "kimi-cli":
				got = task.KimiModel
			case "agy":
				got = task.AgyModel
			case "opencode":
				got = task.OpenCodeModel
			}
			if got != model || task.PreferRunner != runner {
				t.Fatalf("model=%s runner=%s", got, task.PreferRunner)
			}
		})
	}
}
