package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func seedBoundAttempt(t *testing.T, root string, tk *Task, pid int, identity string) string {
	t.Helper()
	id := newAttemptID()
	now := time.Now().Format(time.RFC3339Nano)
	rec := &AttemptRecord{
		TaskID:           tk.ID,
		AttemptID:        id,
		ExpectedRevision: tk.Revision,
		ControlEpoch:     tk.ControlEpoch,
		State:            attemptBound,
		PID:              pid,
		PGID:             pid,
		StartIdentity:    identity,
		WorkspaceLeaseID: canonicalWorkspaceID(tk.Dir),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	tk.ActiveAttemptID = id
	tk.Status = statusRunning
	tk.touch()
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedReservedOnlyAttempt(t *testing.T, root string, tk *Task) string {
	t.Helper()
	id := newAttemptID()
	now := time.Now().Format(time.RFC3339Nano)
	rec := &AttemptRecord{
		TaskID:           tk.ID,
		AttemptID:        id,
		ExpectedRevision: tk.Revision,
		ControlEpoch:     tk.ControlEpoch,
		State:            attemptReserved,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	tk.ActiveAttemptID = id
	tk.Status = statusRunning
	tk.touch()
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	return id
}

func fakeClaudeDeadOwnerLockBin(t *testing.T, root, countPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	lock := lockPath(root)
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version|-v) echo 'claude 1.0'; exit 0;; esac\n" +
		"n=$(cat " + shSingleQuote(countPath) + " 2>/dev/null || echo 0)\n" +
		"n=$((n+1)); printf '%s' \"$n\" > " + shSingleQuote(countPath) + "\n" +
		"printf '{\"pid\":999999,\"at\":\"%s\"}\\n' \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\" > " + shSingleQuote(lock) + "\n" +
		"cat <<'JSON_EOF'\n" + mkOKResultJSON("sess-max1-dead-lock") + "\nJSON_EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAutomaticProviderReplayBlockedUsesAttemptRecords(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	cfg := testCfg()
	ws := t.TempDir()
	max1 := newTask(root, cfg, typeSequence, "max1 bound", ws, []string{"p"}, 5)
	max1.MaxAttempts = 1
	if err := saveTask(root, max1); err != nil {
		t.Fatal(err)
	}
	oldID := seedBoundAttempt(t, root, max1, 999999, "started-provider")
	loaded, err := loadTask(root, max1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !automaticProviderReplayBlocked(root, cfg, loaded) {
		t.Fatal("started max1 bound attempt must block automatic replay")
	}

	pre := newTask(root, cfg, typeSequence, "max1 reserved only", t.TempDir(), []string{"p"}, 5)
	pre.MaxAttempts = 1
	if err := saveTask(root, pre); err != nil {
		t.Fatal(err)
	}
	seedReservedOnlyAttempt(t, root, pre)
	preLoaded, err := loadTask(root, pre.ID)
	if err != nil {
		t.Fatal(err)
	}
	if automaticProviderReplayBlocked(root, cfg, preLoaded) {
		t.Fatal("reserved-only pre-provider failure must not count as started provider work")
	}

	unknown := newTask(root, cfg, typeSequence, "unknown leftover", t.TempDir(), []string{"p"}, 5)
	unknown.MaxAttempts = 3
	unknown.LastRouteAttempt = &RouteAttemptReadback{FailureKind: "unknown_outcome", FailureClass: string(failureUnknown)}
	if err := saveTask(root, unknown); err != nil {
		t.Fatal(err)
	}
	seedBoundAttempt(t, root, unknown, 999998, "unknown-started")
	unknownLoaded, err := loadTask(root, unknown.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !automaticProviderReplayBlocked(root, cfg, unknownLoaded) {
		t.Fatal("started unknown leftover with Attempts=0 must block automatic replay")
	}

	counted, err := loadAttempt(root, max1.ID, oldID)
	if err != nil || counted.AttemptID != oldID || counted.State != attemptBound {
		t.Fatalf("old attempt evidence missing: %+v err=%v", counted, err)
	}
}

func TestMax1StartedAttemptDoesNotRedispatchAfterDeadOwnerLockLoss(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX lock steal")
	}
	root := testRoot(t)
	t.Cleanup(func() { killLockPID(root) })
	countPath := filepath.Join(t.TempDir(), "calls")
	cfg := runTaskCfg(t, fakeClaudeDeadOwnerLockBin(t, root, countPath))
	cfg.DrainRescanSec = 1
	cfg.MaxParallel = 2
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	tk := newTask(root, cfg, typeSequence, "max1 lock-loss", t.TempDir(), []string{"p"}, 5)
	tk.MaxAttempts = 1
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}

	out, err := captureCmdRun(t, []string{"-root", root, tk.ID})
	if err == nil {
		t.Fatalf("first run after dead-owner lock overwrite must be nonzero, stdout=%q", out)
	}
	if strings.Contains(out, "完成") || strings.Contains(strings.ToLower(out), "done") {
		t.Fatalf("stdout reported done: %q", out)
	}
	raw, _ := os.ReadFile(countPath)
	if strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("first invoke count=%q want 1", raw)
	}
	afterFirst, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.Status == statusDone {
		t.Fatalf("disk status must stay non-done, got %s", afterFirst.Status)
	}
	firstAttempt := afterFirst.ActiveAttemptID
	if firstAttempt == "" {
		t.Fatal("started attempt evidence missing after first invoke")
	}

	sibling := newTask(root, cfg, typeSequence, "healthy sibling", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root, sibling); err != nil {
		t.Fatal(err)
	}
	cfg.ClaudeBin = fakeClaudeBin(t, mkOKResultJSON("sess-sibling"), "", 0)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}

	out2, err2 := captureCmdRun(t, []string{"-root", root, "-quiet"})
	if err2 != nil && !errors.Is(err2, errAutomaticReplayBlocked) && !errors.Is(err2, errRunIDNotDispatched) {
		t.Fatalf("drain after lock recovery: %v stdout=%q", err2, out2)
	}
	raw, _ = os.ReadFile(countPath)
	if strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("second dispatch invoked provider again, count=%q", raw)
	}
	got, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == statusDone {
		t.Fatalf("max1 leftover status=%s", got.Status)
	}
	if got.ActiveAttemptID != firstAttempt && firstAttempt != "" {
		rec, lerr := loadAttempt(root, tk.ID, firstAttempt)
		if lerr != nil || rec == nil {
			t.Fatalf("old attempt %s missing after replay: %v", firstAttempt, lerr)
		}
	}
	entries, err := os.ReadDir(attemptsDir(root, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		rec, lerr := loadAttempt(root, tk.ID, strings.TrimSuffix(e.Name(), ".json"))
		if lerr != nil {
			t.Fatal(lerr)
		}
		if attemptRecordStartedProvider(rec) && rec.ControlEpoch == got.ControlEpoch {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("started attempts in epoch=%d want 1", started)
	}
	sib, err := loadTask(root, sibling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sib.Status != statusDone {
		t.Fatalf("unrelated healthy card status=%s want done", sib.Status)
	}
}

func TestPreProviderReservedMax1StillDispatches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	root := testRoot(t)
	countPath := filepath.Join(t.TempDir(), "calls")
	cfg := runTaskCfg(t, countingClaudeBin(t, countPath, mkOKResultJSON("sess-pre")))
	cfg.DrainRescanSec = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	tk := newTask(root, cfg, typeSequence, "max1 pre-provider", t.TempDir(), []string{"p"}, 5)
	tk.MaxAttempts = 1
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	seedReservedOnlyAttempt(t, root, tk)
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	if providerCallCount(t, countPath) != 1 {
		t.Fatalf("pre-provider reserved attempt must still invoke once, got %d", providerCallCount(t, countPath))
	}
	got, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusDone {
		t.Fatalf("pre-provider recovery status=%s want done", got.Status)
	}
}

func TestExplicitRetryAfterMax1HoldAllowsNewEpoch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	root := testRoot(t)
	countPath := filepath.Join(t.TempDir(), "calls")
	cfg := runTaskCfg(t, countingClaudeBin(t, countPath, mkOKResultJSON("sess-retry")))
	cfg.DrainRescanSec = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	tk := newTask(root, cfg, typeSequence, "max1 explicit retry", t.TempDir(), []string{"p"}, 5)
	tk.MaxAttempts = 1
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := cmdSetStatus([]string{"-root", root, tk.ID}, "hold"); err != nil {
		t.Fatal(err)
	}
	held, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldID := newAttemptID()
	now := time.Now().Format(time.RFC3339Nano)
	if err := writeAttempt(root, &AttemptRecord{
		TaskID: held.ID, AttemptID: oldID, ControlEpoch: held.ControlEpoch,
		State: attemptBound, PID: 999999, StartIdentity: "started-then-held",
		WorkspaceLeaseID: canonicalWorkspaceID(held.Dir),
		CreatedAt:        now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := cmdSetStatus([]string{"-root", root, tk.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	retried, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != statusQueued || retried.ControlEpoch != held.ControlEpoch+1 {
		t.Fatalf("retry must open a new epoch: %+v", retried)
	}
	if automaticProviderReplayBlocked(root, cfg, retried) {
		t.Fatal("new control epoch must not inherit the old started-attempt gate")
	}
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	if providerCallCount(t, countPath) != 1 {
		t.Fatalf("authorized retry must invoke once, got %d", providerCallCount(t, countPath))
	}
	old, err := loadAttempt(root, tk.ID, oldID)
	if err != nil || old.AttemptID != oldID {
		t.Fatalf("old attempt evidence missing: %v", err)
	}
}

func TestWriterDeferralReportsObservedRunningStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX live-attempt fixture")
	}
	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.DrainRescanSec = 1
	cfg.ClaudeBin = fakeClaudeBin(t, mkOKResultJSON("sess-defer"), "", 0)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	live := newTask(root, cfg, typeSequence, "live running writer", t.TempDir(), []string{"p"}, 9)
	live.Status = statusRunning
	if err := saveTask(root, live); err != nil {
		t.Fatal(err)
	}
	cmd := bindLiveAttempt(t, root, live)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	freshLive, err := loadTask(root, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshLive.Status != statusRunning {
		t.Fatalf("live fixture status=%s", freshLive.Status)
	}
	healthy := newTask(root, cfg, typeSequence, "other workspace", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root, healthy); err != nil {
		t.Fatal(err)
	}
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	ev, ok := lastTaskEvent(root, live.ID)
	if !ok || ev.Type != evDeferred || ev.Detail["reason"] != "live_writer_proof" {
		t.Fatalf("live_writer_proof event: ok=%v ev=%+v", ok, ev)
	}
	if ev.Status != statusRunning {
		t.Fatalf("deferral status=%q want observed running", ev.Status)
	}
	if ev.Status == statusQueued {
		t.Fatal("deferral projected fake queued")
	}
	gotLive, err := loadTask(root, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotLive.Status == statusQueued {
		t.Fatal("live writer disk status mutated to queued")
	}
	gotHealthy, err := loadTask(root, healthy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotHealthy.Status != statusDone {
		t.Fatalf("unrelated healthy card status=%s want done", gotHealthy.Status)
	}
}

func TestDeadOwnerLockStillStealableAfterClassification(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	path := lockPath(root)
	info, _ := json.Marshal(lockInfo{PID: 999999, At: time.Now().Format(time.RFC3339)})
	if err := os.WriteFile(path, info, 0o644); err != nil {
		t.Fatal(err)
	}
	if classifyProcessObservation(999999) != processObservationDead {
		t.Fatalf("PID 999999 observation=%s want dead", classifyProcessObservation(999999))
	}
	if !staleLock(path, time.Hour) {
		t.Fatal("known-dead owner must remain recoverable")
	}
	if !acquireLock(root, time.Hour) {
		t.Fatal("dead owner lock must be stealable")
	}
	if !holdsSchedulerLock(root) {
		t.Fatal("recovery must create a new explicit owner")
	}
	releaseLock(root)
}

func TestSchedulerInvokeMarkerAtStartBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	for _, failure := range []string{"start-failed", "bind-write-failed"} {
		t.Run(failure, func(t *testing.T) {
			root := testRoot(t)
			withSchedulerLock(t, root)
			cfg := defaultConfig("claude")
			cfg.MaxAttempts = 1
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			tk := newTask(root, cfg, typeSequence, failure, t.TempDir(), []string{"p"}, 5)
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			if err := reserveDispatchAttempt(root, tk); err != nil {
				t.Fatal(err)
			}
			taskExecRoot.Store(tk.ID, root)
			t.Cleanup(func() { taskExecRoot.Delete(tk.ID); afterCmdStart = nil; attemptWriteHook = nil })
			cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
			setupProcGroup(cmd)
			if failure == "start-failed" {
				cmd.Dir = filepath.Join(t.TempDir(), "missing-dir")
			} else {
				afterCmdStart = func(_ *exec.Cmd) {
					rec, err := loadAttempt(root, tk.ID, tk.ActiveAttemptID)
					if err != nil || rec.InvokeStep == nil || *rec.InvokeStep != 0 {
						t.Error("Start crossed before durable invoke marker")
					}
					attemptWriteHook = func(rec *AttemptRecord) error {
						if rec.State == attemptBound {
							return errors.New("synthetic bind persistence failure")
						}
						return nil
					}
				}
			}
			launchErr := runCmdRegisteredForTaskWorkspace(cmd, tk.ID, tk.Dir)
			afterCmdStart, attemptWriteHook = nil, nil
			if launchErr == nil {
				t.Fatal("injected failure unexpectedly succeeded")
			}
			fresh, err := loadTask(root, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := loadAttempt(root, tk.ID, tk.ActiveAttemptID)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "start-failed" {
				if rec.InvokeStep != nil || automaticProviderReplayBlocked(root, cfg, fresh) {
					t.Fatal("definite Start failure consumed provider work")
				}
			} else {
				if rec.InvokeStep == nil || !automaticProviderReplayBlocked(root, cfg, fresh) {
					t.Fatalf("started but unbound attempt allowed replay: launch=%v record=%+v", launchErr, rec)
				}
				if err := reserveDispatchAttempt(root, fresh); !errors.Is(err, errAutomaticReplayBlocked) {
					t.Fatalf("second reservation: %v", err)
				}
			}
		})
	}
}
