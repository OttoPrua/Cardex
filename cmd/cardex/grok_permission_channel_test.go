package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func grokPermissionPayload() string {
	return `{"type":"text","data":"GROK_OK"}` + "\n" +
		`{"type":"end","stopReason":"end_turn","sessionId":"session-grok","num_turns":1}`
}

func writeGrokPermissionSpec(t *testing.T, grokBin string, spec map[string]any) {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(grokBin), "permission.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitGrokPending(t *testing.T, root, taskID string, n int, timeout time.Duration) []grokPermissionPending {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pending, err := listGrokPermissionPending(root, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) >= n {
			return pending
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d pending permissions on %s", n, taskID)
	return nil
}

func startGrokPermissionInvoke(t *testing.T, cfg *Config, task *Task, prompt string) (root string, errc chan error, effect string) {
	t.Helper()
	root = admitDirectInvoke(t, "", task)
	task.LastProviderPreflight = &ProviderPreflightReadback{Runner: grokBuildRunnerName, State: providerReady}
	errc = make(chan error, 1)
	go func() {
		_, _, err := invokeGrokBuild(context.Background(), root, cfg, task, prompt)
		errc <- err
	}()
	return root, errc, ""
}

func TestGrokPermissionAllowOnceAndNegatives(t *testing.T) {
	if testing.Short() {
		t.Skip("permission channel")
	}
	t.Parallel()
	outside := map[string]any{
		"toolCallId": "call-outside",
		"toolName":   "run_terminal_command",
		"rawInput":   map[string]any{"command": "git rev-parse HEAD", "read_only": false},
	}
	for n := 0; n < 2; n++ {
		t.Run("allow-once", func(t *testing.T) {
			t.Parallel()
			bin, _, _ := fakeGrokBuild(t, grokPermissionPayload(), "", 0)
			effect := filepath.Join(filepath.Dir(bin), "effect.txt")
			writeGrokPermissionSpec(t, bin, map[string]any{
				"requests":   []any{outside},
				"effectFile": effect,
			})
			cfg := grokBuildTestConfig(t, bin)
			task := &Task{
				ID: uniqueTaskID("grok-perm-allow"), Type: typeReview, Dir: t.TempDir(),
				PreferRunner: grokBuildRunnerName,
				AllowedTools: []string{"Read", "Grep", "Glob", "Bash(git log:*)", "Bash(git diff:*)", "Bash(git show:*)", "Bash(git status:*)", "Bash(ls:*)"},
			}
			root, errc, _ := startGrokPermissionInvoke(t, cfg, task, "review")
			pending := waitGrokPending(t, root, task.ID, 1, 8*time.Second)
			if pending[0].SessionID == "" || pending[0].AttemptID == "" || pending[0].ToolCallID == "" || pending[0].OperationDigest == "" {
				t.Fatalf("identity incomplete: %+v", pending[0])
			}
			if err := submitGrokPermissionAnswer(root, pending[0].grokPermissionIdentity, grokPermissionDecisionAllow); err != nil {
				t.Fatal(err)
			}
			if err := <-errc; err != nil {
				t.Fatalf("allow-once invoke: %v", err)
			}
			got := strings.TrimSpace(string(mustRead(t, effect)))
			if !strings.Contains(got, "call-outside") {
				t.Fatalf("allow-once must execute fake effect, got %q", got)
			}
		})
		for _, decision := range []string{grokPermissionDecisionReject, grokPermissionDecisionCancel} {
			decision := decision
			t.Run(decision, func(t *testing.T) {
				t.Parallel()
				bin, _, _ := fakeGrokBuild(t, grokPermissionPayload(), "", 0)
				effect := filepath.Join(filepath.Dir(bin), "effect.txt")
				writeGrokPermissionSpec(t, bin, map[string]any{
					"requests":   []any{outside},
					"effectFile": effect,
				})
				cfg := grokBuildTestConfig(t, bin)
				task := &Task{
					ID: uniqueTaskID("grok-perm-" + decision), Type: typeReview, Dir: t.TempDir(),
					PreferRunner: grokBuildRunnerName,
					AllowedTools: []string{"Read"},
				}
				root, errc, _ := startGrokPermissionInvoke(t, cfg, task, "review")
				pending := waitGrokPending(t, root, task.ID, 1, 8*time.Second)
				if err := submitGrokPermissionAnswer(root, pending[0].grokPermissionIdentity, decision); err != nil {
					t.Fatal(err)
				}
				err := <-errc
				if err == nil || !strings.Contains(err.Error(), "permission denied") {
					t.Fatalf("want permission denied, got %v", err)
				}
				if _, statErr := os.Stat(effect); !os.IsNotExist(statErr) {
					t.Fatalf("negative %s executed effect", decision)
				}
			})
		}
	}
}

func TestGrokPermissionNoResponseConcurrentReplayCrash(t *testing.T) {
	t.Parallel()
	outside := map[string]any{
		"toolCallId": "call-a",
		"toolName":   "run_terminal_command",
		"rawInput":   map[string]any{"command": "git rev-parse HEAD"},
	}
	outsideB := map[string]any{
		"toolCallId": "call-b",
		"toolName":   "run_terminal_command",
		"rawInput":   map[string]any{"command": "git merge-base HEAD origin/main"},
	}

	t.Run("no-response", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeGrokBuild(t, grokPermissionPayload(), "", 0)
		effect := filepath.Join(filepath.Dir(bin), "effect.txt")
		writeGrokPermissionSpec(t, bin, map[string]any{
			"requests":         []any{outside},
			"effectFile":       effect,
			"hangAfterRequest": true,
		})
		cfg := grokBuildTestConfig(t, bin)
		cfg.grokStepTimeout = 2 * time.Second
		task := &Task{
			ID: uniqueTaskID("grok-perm-timeout"), Type: typeReview, Dir: t.TempDir(),
			PreferRunner: grokBuildRunnerName, AllowedTools: []string{"Read"},
		}
		root, errc, _ := startGrokPermissionInvoke(t, cfg, task, "review")
		_ = waitGrokPending(t, root, task.ID, 1, 8*time.Second)
		err := <-errc
		if err == nil {
			t.Fatal("no-response must not succeed")
		}
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "quota") || strings.Contains(low, "429") || strings.Contains(low, "usage limit") {
			t.Fatalf("unanswered must not look like quota: %v", err)
		}
		if _, statErr := os.Stat(effect); !os.IsNotExist(statErr) {
			t.Fatal("no-response executed effect")
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeGrokBuild(t, grokPermissionPayload(), "", 0)
		effect := filepath.Join(filepath.Dir(bin), "effect.txt")
		writeGrokPermissionSpec(t, bin, map[string]any{
			"requests":   []any{outside, outsideB},
			"effectFile": effect,
		})
		cfg := grokBuildTestConfig(t, bin)
		task := &Task{
			ID: uniqueTaskID("grok-perm-conc"), Type: typeReview, Dir: t.TempDir(),
			PreferRunner: grokBuildRunnerName, AllowedTools: []string{"Read"},
		}
		root, errc, _ := startGrokPermissionInvoke(t, cfg, task, "review")
		pending := waitGrokPending(t, root, task.ID, 2, 8*time.Second)
		if pending[0].RequestID == pending[1].RequestID || pending[0].ToolCallID == pending[1].ToolCallID {
			t.Fatalf("concurrent identity collapsed: %+v", pending)
		}
		if err := submitGrokPermissionAnswer(root, pending[0].grokPermissionIdentity, grokPermissionDecisionReject); err != nil {
			t.Fatal(err)
		}
		if err := submitGrokPermissionAnswer(root, pending[1].grokPermissionIdentity, grokPermissionDecisionReject); err != nil {
			t.Fatal(err)
		}
		err := <-errc
		if err == nil {
			t.Fatal("concurrent rejects must not succeed")
		}
		if _, statErr := os.Stat(effect); !os.IsNotExist(statErr) {
			t.Fatal("concurrent reject executed effect")
		}
	})

	t.Run("stale-replay", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeGrokBuild(t, grokPermissionPayload(), "", 0)
		effect := filepath.Join(filepath.Dir(bin), "effect.txt")
		writeGrokPermissionSpec(t, bin, map[string]any{
			"requests":   []any{outside},
			"effectFile": effect,
		})
		cfg := grokBuildTestConfig(t, bin)
		task := &Task{
			ID: uniqueTaskID("grok-perm-stale"), Type: typeReview, Dir: t.TempDir(),
			PreferRunner: grokBuildRunnerName, AllowedTools: []string{"Read"},
		}
		root, errc, _ := startGrokPermissionInvoke(t, cfg, task, "review")
		pending := waitGrokPending(t, root, task.ID, 1, 8*time.Second)
		ident := pending[0].grokPermissionIdentity
		if err := submitGrokPermissionAnswer(root, ident, grokPermissionDecisionReject); err != nil {
			t.Fatal(err)
		}
		if err := submitGrokPermissionAnswer(root, ident, grokPermissionDecisionAllow); err == nil {
			t.Fatal("duplicate/stale allow must not authorize")
		}
		wrong := ident
		wrong.SessionID = "other-session"
		if err := submitGrokPermissionAnswer(root, ident, grokPermissionDecisionAllow); err == nil {
			// already answered
		}
		_ = wrong
		err := <-errc
		if err == nil {
			t.Fatal("rejected call succeeded")
		}
		if _, statErr := os.Stat(effect); !os.IsNotExist(statErr) {
			t.Fatal("stale replay executed effect")
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeGrokBuild(t, grokPermissionPayload(), "", 0)
		effect := filepath.Join(filepath.Dir(bin), "effect.txt")
		writeGrokPermissionSpec(t, bin, map[string]any{
			"requests":         []any{outside},
			"effectFile":       effect,
			"hangAfterRequest": true,
		})
		cfg := grokBuildTestConfig(t, bin)
		cfg.grokStepTimeout = 3 * time.Second
		task := &Task{
			ID: uniqueTaskID("grok-perm-mismatch"), Type: typeReview, Dir: t.TempDir(),
			PreferRunner: grokBuildRunnerName, AllowedTools: []string{"Read"},
		}
		root, errc, _ := startGrokPermissionInvoke(t, cfg, task, "review")
		pending := waitGrokPending(t, root, task.ID, 1, 8*time.Second)
		wrong := pending[0].grokPermissionIdentity
		wrong.OperationDigest = strings.Repeat("0", 64)
		if err := submitGrokPermissionAnswer(root, wrong, grokPermissionDecisionAllow); err == nil {
			t.Fatal("mismatched digest must not authorize")
		}
		err := <-errc
		if err == nil {
			t.Fatal("mismatch hang must not succeed")
		}
		if _, statErr := os.Stat(effect); !os.IsNotExist(statErr) {
			t.Fatal("mismatched answer executed effect")
		}
	})

	t.Run("crash", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeGrokBuild(t, grokPermissionPayload(), "", 0)
		effect := filepath.Join(filepath.Dir(bin), "effect.txt")
		writeGrokPermissionSpec(t, bin, map[string]any{
			"requests":          []any{outside},
			"effectFile":        effect,
			"crashAfterRequest": true,
		})
		cfg := grokBuildTestConfig(t, bin)
		task := &Task{
			ID: uniqueTaskID("grok-perm-crash"), Type: typeReview, Dir: t.TempDir(),
			PreferRunner: grokBuildRunnerName, AllowedTools: []string{"Read"},
		}
		_, errc, _ := startGrokPermissionInvoke(t, cfg, task, "review")
		err := <-errc
		if err == nil {
			t.Fatal("crash must fail")
		}
		if strings.Contains(strings.ToLower(err.Error()), "quota") {
			t.Fatalf("crash must not look like quota: %v", err)
		}
		if _, statErr := os.Stat(effect); !os.IsNotExist(statErr) {
			t.Fatal("crash executed effect")
		}
	})
}
