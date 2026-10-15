package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func buildGrokPermissionCandidate(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("CARDEX_CANDIDATE_BIN")); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("CARDEX_CANDIDATE_BIN=%s: %v", p, err)
		}
		return p
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	pkg := filepath.Dir(file)
	out := filepath.Join(t.TempDir(), "cardex")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = pkg
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v\n%s", err, data)
	}
	return out
}

func TestGrokPermissionCandidateCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake native transport")
	}
	bin := buildGrokPermissionCandidate(t)
	for i := 0; i < 2; i++ {
		t.Run("consumer", func(t *testing.T) {
			runGrokPermissionCandidateOnce(t, bin)
		})
	}
}

func runGrokPermissionCandidateOnce(t *testing.T, cardexBin string) {
	t.Helper()
	root := testRoot(t)
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := grokPermissionPayload()
	grokBin, _, _ := fakeGrokBuild(t, payload, "", 0)
	effect := filepath.Join(filepath.Dir(grokBin), "effect.txt")
	writeGrokPermissionSpec(t, grokBin, map[string]any{
		"requests": []any{
			map[string]any{
				"toolCallId": "call-outside",
				"toolName":   "run_terminal_command",
				"rawInput":   map[string]any{"command": "git cat-file -p HEAD", "read_only": false},
			},
		},
		"effectFile": effect,
	})
	cfg := grokBuildTestConfig(t, grokBin)
	cfg.MaxAttempts = 1
	cfg.StepTimeoutMin = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	task := newTask(root, cfg, typeReview, "candidate grok permission", t.TempDir(), []string{"review the candidate"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	task.GrokModel = "grok-4.6"
	task.GrokEffort = "xhigh"
	task.MaxAttempts = 1
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	run := exec.Command(cardexBin, "run", "-root", root, "-quiet", task.ID)
	run.Env = env
	run.Dir = task.Dir
	var runBuf strings.Builder
	run.Stdout = &runBuf
	run.Stderr = &runBuf
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run.Wait() }()

	var pending []grokPermissionPending
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		list := exec.Command(cardexBin, "grok-permission", "list", "-root", root, "-json", task.ID)
		list.Env = env
		out, err := list.CombinedOutput()
		if err == nil {
			if json.Unmarshal(out, &pending) == nil && len(pending) > 0 {
				if pending[0].RequestID != "" && pending[0].OperationDigest != "" && pending[0].ToolCallID != "" {
					break
				}
			}
		}
		select {
		case err := <-done:
			t.Fatalf("run exited before pending appeared: %v", err)
		default:
		}
		time.Sleep(40 * time.Millisecond)
		pending = nil
	}
	if len(pending) == 0 {
		t.Fatal("candidate pending identity missing")
	}
	ans := exec.Command(cardexBin, "grok-permission", "answer",
		"-root", root,
		"-request", pending[0].RequestID,
		"-decision", "reject",
		"-session", pending[0].SessionID,
		"-attempt", pending[0].AttemptID,
		"-tool-call", pending[0].ToolCallID,
		"-digest", pending[0].OperationDigest,
		task.ID)
	ans.Env = env
	if out, err := ans.CombinedOutput(); err != nil {
		t.Fatalf("answer: %v\n%s", err, out)
	}
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		_ = run.Process.Kill()
		t.Fatalf("candidate run did not reach terminal\n%s", runBuf.String())
	}
	if _, err := os.Stat(effect); !os.IsNotExist(err) {
		t.Fatalf("rejected call executed fake tool effect\n%s", runBuf.String())
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == statusDone || got.Status == statusQueued {
		t.Fatalf("rejected call retried, completed, or requeued: %+v\n%s", got, runBuf.String())
	}
	if got.Attempts > 1 {
		t.Fatalf("rejected call retried attempts=%d last=%s\n%s", got.Attempts, got.LastError, runBuf.String())
	}
	if strings.Contains(strings.ToLower(got.LastError+" "+runBuf.String()), "quota") ||
		strings.Contains(got.LastError+" "+runBuf.String(), "429") {
		t.Fatalf("reject must not look like quota: last=%s\n%s", got.LastError, runBuf.String())
	}
}
