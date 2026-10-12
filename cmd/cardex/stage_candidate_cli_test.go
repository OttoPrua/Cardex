package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func stageCandidateBin(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("CARDEX_CANDIDATE_BIN")); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("CARDEX_CANDIDATE_BIN=%s: %v", p, err)
		}
		return p
	}
	p := filepath.Join("..", "..", ".cardex-stage", "cardex")
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("stage candidate CLI not built")
	}
	return p
}

func TestStageCandidateCLIThreeConsumers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX candidate CLI fixtures")
	}
	bin := stageCandidateBin(t)
	for i := 0; i < 2; i++ {
		t.Run("ingest-done-closed", func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			wf := initTestWorkflow(t, root, dir)
			writer := doneClosedWriter(t, root, wf)
			commit, tree := commitOwnedSourceChange(t, dir)
			cmd := exec.Command(bin, "workflow", "freeze-candidate", wf.ID, "-root", root,
				"-commit", commit, "-tree", tree, "-changed-paths", "internal/auth")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("freeze: %v\n%s", err, out)
			}
			wf = reloadWF(t, root, wf.ID)
			writer, err = loadTask(root, writer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if writer.Status != statusDone {
				t.Fatalf("freeze rewrote done writer to %s", writer.Status)
			}
			receipt := writeDoneClosedReviewReceipt(t, dir, wf, writer)
			ing := exec.Command(bin, "workflow", "ingest-external-review", wf.ID, "-root", root, "-receipt", receipt)
			ingOut, err := ing.CombinedOutput()
			if err != nil {
				t.Fatalf("ingest: %v\n%s", err, ingOut)
			}
			if !strings.Contains(string(ingOut), "method=external_independent_local") {
				t.Fatalf("ingest output: %s", ingOut)
			}
			fresh, err := loadTask(root, writer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Status != statusDone {
				t.Fatalf("ingest rewrote done writer to %s", fresh.Status)
			}
		})
		t.Run("run-id-persist-reject", func(t *testing.T) {
			root := testRoot(t)
			t.Cleanup(func() { killLockPID(root) })
			countPath := filepath.Join(t.TempDir(), "calls")
			cfg := runTaskCfg(t, fakeClaudeStealLockBin(t, root, countPath))
			cfg.DrainRescanSec = 1
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			tk := newTask(root, cfg, typeSequence, "candidate-persist-reject", t.TempDir(), []string{"p"}, 5)
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "run", "-root", root, tk.ID)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("candidate run-ID persist-reject must be nonzero, stdout=%q", out)
			}
			if strings.Contains(string(out), "完成") || strings.Contains(strings.ToLower(string(out)), "done") {
				t.Fatalf("candidate stdout reported done: %s", out)
			}
			got, lerr := loadTask(root, tk.ID)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if got.Status == statusDone {
				t.Fatalf("disk status=%s", got.Status)
			}
			raw, _ := os.ReadFile(countPath)
			if strings.TrimSpace(string(raw)) != "1" {
				t.Fatalf("provider count=%q", raw)
			}
		})
		t.Run("max1-lock-loss-no-replay", func(t *testing.T) {
			root := testRoot(t)
			t.Cleanup(func() { killLockPID(root) })
			countPath := filepath.Join(t.TempDir(), "calls")
			cfg := runTaskCfg(t, fakeClaudeDeadOwnerLockBin(t, root, countPath))
			cfg.DrainRescanSec = 1
			if i == 1 {
				cfg.MaxAttempts = 1
			}
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			tk := newTask(root, cfg, typeSequence, "candidate-max1-lock-loss", t.TempDir(), []string{"p"}, 5)
			if i == 0 {
				tk.MaxAttempts = 1
			}
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			first := exec.Command(bin, "run", "-root", root, tk.ID)
			out, err := first.CombinedOutput()
			if err == nil {
				t.Fatalf("candidate max1 lock-loss must be nonzero, stdout=%q", out)
			}
			if strings.Contains(string(out), "完成") || strings.Contains(strings.ToLower(string(out)), "done") {
				t.Fatalf("candidate stdout reported done: %s", out)
			}
			raw, _ := os.ReadFile(countPath)
			if strings.TrimSpace(string(raw)) != "1" {
				t.Fatalf("first provider count=%q", raw)
			}
			got, lerr := loadTask(root, tk.ID)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if got.Status == statusDone {
				t.Fatalf("disk status=%s", got.Status)
			}
			oldAttempt := got.ActiveAttemptID
			second := exec.Command(bin, "run", "-root", root, "-quiet", tk.ID)
			out2, err2 := second.CombinedOutput()
			if err2 == nil {
				t.Fatalf("second candidate run must not exit 0, stdout=%q", out2)
			}
			if strings.Contains(string(out2), "完成") || strings.Contains(strings.ToLower(string(out2)), "done") {
				t.Fatalf("second stdout reported done: %s", out2)
			}
			raw, _ = os.ReadFile(countPath)
			if strings.TrimSpace(string(raw)) != "1" {
				t.Fatalf("second run invoked provider again, count=%q", raw)
			}
			after, aerr := loadTask(root, tk.ID)
			if aerr != nil {
				t.Fatal(aerr)
			}
			if after.Status == statusDone {
				t.Fatalf("replay disk status=%s", after.Status)
			}
			if oldAttempt != "" {
				if _, err := loadAttempt(root, tk.ID, oldAttempt); err != nil {
					t.Fatalf("old attempt evidence missing: %v", err)
				}
			}
		})
	}
}

func TestStageCandidateCLIUnboundStartNoReplay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	bin := stageCandidateBin(t)
	root := testRoot(t)
	count := filepath.Join(t.TempDir(), "calls")
	cfg := runTaskCfg(t, countingClaudeBin(t, count, mkOKResultJSON("unbound-should-not-call")))
	cfg.MaxAttempts = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	tk := newTask(root, cfg, typeSequence, "unknown start seam", t.TempDir(), []string{"p"}, 5)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	id := seedReservedOnlyAttempt(t, root, tk)
	rec, err := loadAttempt(root, tk.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	rec.InvokeStep = &step // durable Start intent; no PID binding survived
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(attemptPath(root, tk.ID, id))
	out, err := exec.Command(bin, "run", "-root", root, "-quiet", tk.ID).CombinedOutput()
	if err == nil {
		t.Fatalf("unbound Start was replayed: %s", out)
	}
	if _, err := os.Stat(count); !os.IsNotExist(err) {
		t.Fatal("unknown Start invoked fake provider again")
	}
	after, _ := os.ReadFile(attemptPath(root, tk.ID, id))
	if string(before) != string(after) {
		t.Fatal("original unknown attempt was rewritten")
	}
	t.Logf("binary=%s unknown_start provider_calls=0 original_attempt_unchanged=true", bin)
}

func TestStageCandidateCLISuccessfulStepRestart(t *testing.T) {
	if root := os.Getenv("CARDEX_TEST_STEP_RESTART_ROOT"); root != "" {
		cfg, err := loadConfig(root)
		if err != nil {
			os.Exit(87)
		}
		tk, err := loadTask(root, os.Getenv("CARDEX_TEST_STEP_RESTART_TASK"))
		if err != nil {
			os.Exit(87)
		}
		// Exit after the actual first step success has been durably persisted.
		admissionBetweenStepsHook = func() { os.Exit(88) }
		_ = runTask(context.Background(), root, cfg, tk, false)
		os.Exit(89)
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	bin := stageCandidateBin(t)
	root := testRoot(t)
	count := filepath.Join(t.TempDir(), "calls")
	cfg := runTaskCfg(t, countingClaudeBin(t, count, mkOKResultJSON("step-restart")))
	cfg.MaxAttempts = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	tk := newTask(root, cfg, typeSequence, "durable step restart", t.TempDir(), []string{"one", "two"}, 5)
	tk.MaxAttempts = 1
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	first := exec.Command(os.Args[0], "-test.run=^TestStageCandidateCLISuccessfulStepRestart$")
	first.Env = append(os.Environ(), "CARDEX_TEST_STEP_RESTART_ROOT="+root, "CARDEX_TEST_STEP_RESTART_TASK="+tk.ID)
	out, err := first.CombinedOutput()
	if first.ProcessState == nil || first.ProcessState.ExitCode() != 88 {
		t.Fatalf("first-step process: %v %s", err, out)
	}
	checkpoint, err := loadTask(root, tk.ID)
	if err != nil || checkpoint.Step != 1 || providerCallCount(t, count) != 1 {
		t.Fatalf("first-step checkpoint: %+v %v", checkpoint, err)
	}
	oldID := checkpoint.ActiveAttemptID
	out, err = exec.Command(bin, "run", "-root", root, "-quiet", tk.ID).CombinedOutput()
	if err != nil {
		t.Fatalf("second independent process: %v %s", err, out)
	}
	finished, err := loadTask(root, tk.ID)
	if err != nil || finished.Status != statusDone || finished.Step != 2 || providerCallCount(t, count) != 2 {
		t.Fatalf("restart result: %+v %v", finished, err)
	}
	if _, err := loadAttempt(root, tk.ID, oldID); err != nil {
		t.Fatal("prior successful attempt missing")
	}
	t.Logf("binary=%s persisted_step=1 independent_restart=done2/2 provider_calls=2", bin)
}
