package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGrokStderrRetentionBeforeHarvest(t *testing.T) {
	for _, tc := range []struct{ name, mode, stdout string }{
		{"harvest-empty", harvestModeOn, ""},
		{"harvest-version", harvestModeOn, grokProcessVersionOnlyStdout},
		{"no-harvest", harvestModeOff, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testRoot(t)
			stderr := "launcher failed: synthetic diagnostic\nAuthorization: Bearer synthetic-secret\n"
			bin, calls := fakeGrokBuildCountedStderrFile(t, tc.stdout, stderr, 1)
			cfg := policyTestConfig()
			cfg.GrokBuildBin, cfg.HarvestMode = bin, tc.mode
			isolateGrokLifecycleHome(t, cfg)
			work := t.TempDir()
			useHarvestScript(t, harvestScript{status: "?? reference.txt\n", diff: "diff --git a/reference.txt b/reference.txt\n"}, nil)
			if err := os.WriteFile(filepath.Join(work, "reference.txt"), []byte("pre-existing reference\n"), 0600); err != nil {
				t.Fatal(err)
			}
			task := ownerBackendGrokTask(t, root, cfg, work)
			if err := saveTask(root, task); err != nil {
				t.Fatal(err)
			}
			if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
				t.Fatal(err)
			}
			got, err := loadTask(root, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != statusHeld || got.Attempts != 0 || countProductCalls(t, calls) != 1 || got.PreferRunner != grokBuildRunnerName {
				t.Fatalf("retention changed execution policy: %+v", got)
			}
			if tc.mode == harvestModeOn && (got.Verdict != harvestVerdictNeedsReview || got.Harvest == nil) {
				t.Fatalf("fixture missed harvest early return: %+v", got)
			}
			files, err := filepath.Glob(filepath.Join(logsDir(root), "grok-stderr-*.json"))
			if err != nil || len(files) != 1 {
				t.Fatalf("missing private stderr evidence before harvest: files=%v err=%v", files, err)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var evidence map[string]any
			if err := json.Unmarshal(data, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence["task_id"] != task.ID || evidence["attempt_id"] == "" || evidence["attempt_id"] == nil || evidence["stderr_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(stderr))) || evidence["stderr_bytes"] != float64(len(stderr)) {
				t.Fatalf("incorrect evidence identity: %s", data)
			}
			excerpt, _ := evidence["excerpt"].(string)
			if !strings.Contains(excerpt, "launcher failed") || strings.Contains(excerpt, "synthetic-secret") || evidence["excerpt_format"] != "redacted_bounded_excerpt" {
				t.Fatalf("incorrect diagnostic excerpt: %s", data)
			}
			info, err := os.Stat(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if !lifecycleProbeModeValid(info) {
				t.Fatal("stderr artifact is not private")
			}
			log, _ := os.ReadFile(taskLogPath(root, task.ID))
			if !strings.Contains(string(log), filepath.Base(files[0])) || !strings.Contains(string(log), evidence["stderr_sha256"].(string)) || strings.Contains(string(log), "synthetic-secret") || strings.Contains(string(log), "launcher failed") {
				t.Fatalf("task log must retain only evidence metadata: %s", log)
			}
			assertNoGrokTerminalLeak(t, root, task.ID, "synthetic-secret", "launcher failed")
		})
	}
}

func TestGrokStderrRetentionBoundsAndIdentity(t *testing.T) {
	root := testRoot(t)
	task := &Task{ID: "stderr-fixture", ActiveAttemptID: "attempt-one", Step: 2, Attempts: 1}
	raw := "api_key=synthetic-key\n" + strings.Repeat(strings.Repeat("x", 900)+"\n", 20) + "excluded-tail"
	for _, attempt := range []string{"attempt-one", "attempt-two"} {
		task.ActiveAttemptID = attempt
		if err := retainGrokBuildStderr(root, task, raw); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(logsDir(root), "grok-stderr-*.json"))
	if len(files) != 2 {
		t.Fatalf("attempt evidence overwritten: %v", files)
	}
	seen := map[string]bool{}
	for _, path := range files {
		data, _ := os.ReadFile(path)
		var evidence map[string]any
		if err := json.Unmarshal(data, &evidence); err != nil {
			t.Fatal(err)
		}
		excerpt := evidence["excerpt"].(string)
		if len(excerpt) > grokBuildProcessStderrMaxBytes || strings.Count(excerpt, "\n") > grokBuildProcessStderrMaxLines || strings.Contains(excerpt, "synthetic-key") || strings.Contains(excerpt, "excluded-tail") {
			t.Fatalf("excerpt bounds/redaction failed: %q", excerpt)
		}
		if evidence["stderr_bytes"] != float64(len(raw)) || evidence["stderr_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) || evidence["step"] != float64(2) || evidence["attempt"] != float64(1) {
			t.Fatalf("original metadata replaced by excerpt metadata: %s", data)
		}
		seen[evidence["attempt_id"].(string)] = true
	}
	if !seen["attempt-one"] || !seen["attempt-two"] {
		t.Fatalf("lost attempt identity: %v", seen)
	}
}

func TestGrokStderrRetentionWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions fixture; private file ACL has platform tests")
	}
	root := testRoot(t)
	task := &Task{ID: "stderr-fixture", ActiveAttemptID: "attempt-one", Status: statusHeld}
	lg, err := openTaskLog(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	lg.Close()
	if err := os.Chmod(logsDir(root), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(logsDir(root), 0700) })
	err = retainGrokBuildStderr(root, task, "private diagnostic")
	if err == nil {
		t.Fatal("evidence write failure must be visible")
	}
	log, _ := os.ReadFile(taskLogPath(root, task.ID))
	if !strings.Contains(string(log), `"capture_status":"failed"`) || !strings.Contains(string(log), `"stderr_bytes":18`) || strings.Contains(string(log), "private diagnostic") {
		t.Fatalf("missing safe failure metadata: %s", log)
	}
	if task.Status != statusHeld || task.Attempts != 0 {
		t.Fatal("diagnostic persistence changed execution state")
	}
}

func TestGrokStderrRetentionRedactsCredentialShapes(t *testing.T) {
	for _, tc := range []struct{ name, raw, secret string }{
		{"bare-xai", "context xai-synthetic-credential123", "xai-synthetic-credential123"},
		{"bare-sk", "key sk-synthetic-credential123", "sk-synthetic-credential123"},
		{"passphrase", `passphrase="two secret words"`, "two secret words"},
		{"json-token", `{"token":"synthetic-token"}`, "synthetic-token"},
		{"flag-token", "invalid option --token=synthetic-token", "synthetic-token"},
		{"url-token", "https://example.invalid/?access_token=synthetic-token", "synthetic-token"},
		{"unix-credential-path", "permission denied /Users/fixture/.grokcreds/private-auth", "/Users/fixture/.grokcreds/private-auth"},
		{"windows-credential-path", `permission denied C:\Users\fixture\.grokcreds\private-auth`, `C:\Users\fixture\.grokcreds\private-auth`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testRoot(t)
			task := &Task{ID: "stderr-fixture", ActiveAttemptID: "attempt-one"}
			if err := retainGrokBuildStderr(root, task, tc.raw); err != nil {
				t.Fatal(err)
			}
			files, _ := filepath.Glob(filepath.Join(logsDir(root), "grok-stderr-*.json"))
			if len(files) != 1 {
				t.Fatal("missing private evidence")
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var evidence map[string]any
			if err := json.Unmarshal(data, &evidence); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(evidence["excerpt"].(string), tc.secret) {
				t.Fatal("credential retained in excerpt")
			}
			if evidence["stderr_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(tc.raw))) {
				t.Fatal("original hash changed")
			}
		})
	}
}
