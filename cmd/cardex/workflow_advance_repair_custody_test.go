package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type repairStateSnap struct {
	cardCount  int
	round      int
	members    string
	acceptance string
	writer     string
	status     string
}

func snapRepairState(t *testing.T, root string, cfg *Config, wfID string) repairStateSnap {
	t.Helper()
	wf, err := loadWorkflow(root, cfg, wfID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	return repairStateSnap{
		cardCount:  len(tasks),
		round:      wf.CurrentRound,
		members:    strings.Join(wf.DirectionTaskIDs, ","),
		acceptance: wf.AcceptanceTaskID,
		writer:     wf.WriterTaskID,
		status:     wf.Status,
	}
}

func failedBetaOwnWorkspace(t *testing.T) (root, dir string, cfg *Config, wf *WorkflowRecord, alpha, beta *Task) {
	t.Helper()
	root, dir, cfg, wf, members := readyDisjointAcceptFixture(t)
	if len(members) != 2 {
		t.Fatalf("members=%d", len(members))
	}
	alpha, beta = members[0], members[1]
	betaDir := t.TempDir()
	beta.Dir = betaDir
	beta.Status = statusFailed
	beta.LastError = "synthetic known beta failure, kernel writer lease may still be held"
	if err := writeTaskFile(root, beta); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, beta.ID)
	if err != nil {
		t.Fatal(err)
	}
	beta = loaded
	has, err := taskHasAttemptRecord(root, beta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("fixture beta must have no attempt record")
	}
	return root, dir, cfg, wf, alpha, beta
}

func holdWorkflowAdmitLock(t *testing.T, root, wfID string) (release func()) {
	t.Helper()
	held := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := withTaskControlLock(root, "workflow-admit:"+wfID, func() error {
			close(held)
			<-stop
			return nil
		}); err != nil {
			t.Errorf("workflow-admit lock: %v", err)
		}
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout acquiring workflow-admit lock")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

func goAdmitRepair(root string, cfg *Config, wf *WorkflowRecord, named string) <-chan struct {
	task *Task
	err  error
} {
	ch := make(chan struct {
		task *Task
		err  error
	}, 1)
	go func() {
		task, err := admitKnownDirectionRepair(root, cfg, wf, named)
		ch <- struct {
			task *Task
			err  error
		}{task, err}
	}()
	return ch
}

func goAdvanceRepair(root string, cfg *Config, wf *WorkflowRecord, named string) <-chan struct {
	res *workflowAdvanceResult
	err error
} {
	ch := make(chan struct {
		res *workflowAdvanceResult
		err error
	}, 1)
	go func() {
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
			OptIn: true, MaxActions: 1, RepairDirection: named,
		})
		ch <- struct {
			res *workflowAdvanceResult
			err error
		}{res, err}
	}()
	return ch
}

func waitLockedCaller(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
}

func assertNoRepairMutation(t *testing.T, before, after repairStateSnap) {
	t.Helper()
	if after != before {
		t.Fatalf("repair mutated state before=%+v after=%+v", before, after)
	}
}

func TestOptInRepairHeldKernelLeaseRefusesThenAdmitsAfterRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	root, _, cfg, wf, alpha, beta := failedBetaOwnWorkspace(t)
	lease := holdExclusiveWorkspaceLease(t, beta.Dir)
	if !taskHasLiveWriterProof(root, beta) {
		t.Fatal("exclusive kernel lease must count as live writer proof")
	}
	before := snapRepairState(t, root, cfg, wf.ID)
	res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
	})
	if err == nil || res == nil || res.RepairCreated != 0 || res.AcceptanceCreated != 0 || !res.Held {
		t.Fatalf("held lease must refuse: %+v %v", res, err)
	}
	after := snapRepairState(t, root, cfg, wf.ID)
	assertNoRepairMutation(t, before, after)
	if !workspaceLeaseHeld(beta.Dir) {
		t.Fatal("fixture lease released by repair refuse")
	}
	has, err := taskHasAttemptRecord(root, beta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("refused repair fabricated an attempt on never-started beta")
	}

	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	lease = nil
	if workspaceLeaseHeld(beta.Dir) {
		t.Fatal("lease still held after fixture owner release")
	}
	betaFresh, err := loadTask(root, beta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if betaFresh.Status != statusFailed || betaFresh.LastError == "" {
		t.Fatalf("failure changed after lease release: %+v", betaFresh)
	}
	first, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
	})
	if err != nil {
		t.Fatalf("released valid failure: %v %+v", err, first)
	}
	if first.RepairCreated != 1 || first.RepairTaskID == "" || first.RepairTaskID == beta.ID {
		t.Fatalf("expected exactly one repair: %+v", first)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.CurrentRound != 1 {
		t.Fatalf("round=%d", wf.CurrentRound)
	}
	if !containsAllIDs(wf.DirectionTaskIDs, []string{alpha.ID, first.RepairTaskID}) {
		t.Fatalf("members=%v", wf.DirectionTaskIDs)
	}
	if containsAllIDs(wf.DirectionTaskIDs, []string{beta.ID}) && len(wf.DirectionTaskIDs) == 2 &&
		(wf.DirectionTaskIDs[0] == beta.ID || wf.DirectionTaskIDs[1] == beta.ID) {
		t.Fatalf("failed beta still current: %v", wf.DirectionTaskIDs)
	}
	mid := snapRepairState(t, root, cfg, wf.ID)
	again, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
	})
	if again != nil && again.RepairCreated != 0 {
		t.Fatalf("duplicate repair: %+v %v", again, err)
	}
	assertNoRepairMutation(t, mid, snapRepairState(t, root, cfg, wf.ID))
	has, err = taskHasAttemptRecord(root, beta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("successful repair fabricated an attempt on the never-started failed member")
	}
}

func TestOptInRepairLockStaleStateRefuses(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
		if !namedDirectionIsSoleRepairableFailure(root, wf, beta.SessionStage) {
			t.Fatal("outer filter must pass before lock")
		}
		before := snapRepairState(t, root, cfg, wf.ID)
		release := holdWorkflowAdmitLock(t, root, wf.ID)
		defer release()
		ch := goAdmitRepair(root, cfg, wf, beta.SessionStage)
		waitLockedCaller(t)
		beta.SessionStage = "gamma"
		if err := writeTaskFile(root, beta); err != nil {
			t.Fatal(err)
		}
		release()
		got := <-ch
		if got.err == nil || got.task != nil {
			t.Fatalf("stale identity admitted: %+v %v", got.task, got.err)
		}
		assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
	})
	t.Run("status", func(t *testing.T) {
		root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
		if !namedDirectionIsSoleRepairableFailure(root, wf, beta.SessionStage) {
			t.Fatal("outer filter must pass before lock")
		}
		before := snapRepairState(t, root, cfg, wf.ID)
		release := holdWorkflowAdmitLock(t, root, wf.ID)
		defer release()
		ch := goAdvanceRepair(root, cfg, wf, beta.SessionStage)
		waitLockedCaller(t)
		beta.Status = statusDone
		if err := writeTaskFile(root, beta); err != nil {
			t.Fatal(err)
		}
		release()
		got := <-ch
		if got.err == nil || (got.res != nil && got.res.RepairCreated != 0) {
			t.Fatalf("stale status admitted: %+v %v", got.res, got.err)
		}
		assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
	})
	t.Run("findings", func(t *testing.T) {
		root, _, cfg, wf, members := readyDisjointAcceptFixture(t)
		beta := members[1]
		if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 1}); err != nil {
			t.Fatal(err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		acc, err := loadTask(root, wf.AcceptanceTaskID)
		if err != nil {
			t.Fatal(err)
		}
		acc.Status = statusDone
		if err := writeTaskFile(root, acc); err != nil {
			t.Fatal(err)
		}
		writeReviewLog(t, root, acc.ID, verdictJSON("concerns", []string{"beta billing coverage hole"}, nil))
		if !namedDirectionRepairableFromCurrentFindings(root, wf, beta.SessionStage) || !currentAcceptanceCoverageHolds(root, wf) {
			t.Fatal("outer findings/coverage must pass before lock")
		}
		before := snapRepairState(t, root, cfg, wf.ID)
		release := holdWorkflowAdmitLock(t, root, wf.ID)
		defer release()
		ch := goAdmitRepair(root, cfg, wf, beta.SessionStage)
		waitLockedCaller(t)
		acc.Status = statusQueued
		acc.ReviewOutput = nil
		if err := writeTaskFile(root, acc); err != nil {
			t.Fatal(err)
		}
		release()
		got := <-ch
		if got.err == nil || got.task != nil {
			t.Fatalf("stale findings admitted: %+v %v", got.task, got.err)
		}
		assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
	})
	t.Run("coverage", func(t *testing.T) {
		root, dir, cfg, wf, members := readyDisjointAcceptFixture(t)
		beta := members[1]
		if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 1}); err != nil {
			t.Fatal(err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		acc, err := loadTask(root, wf.AcceptanceTaskID)
		if err != nil {
			t.Fatal(err)
		}
		acc.Status = statusDone
		if err := writeTaskFile(root, acc); err != nil {
			t.Fatal(err)
		}
		writeReviewLog(t, root, acc.ID, verdictJSON("concerns", []string{"beta billing coverage hole"}, nil))
		if !namedDirectionRepairableFromCurrentFindings(root, wf, beta.SessionStage) || !currentAcceptanceCoverageHolds(root, wf) {
			t.Fatal("outer findings/coverage must pass before lock")
		}
		before := snapRepairState(t, root, cfg, wf.ID)
		release := holdWorkflowAdmitLock(t, root, wf.ID)
		defer release()
		ch := goAdmitRepair(root, cfg, wf, beta.SessionStage)
		waitLockedCaller(t)
		mustWriteFile(t, filepath.Join(dir, "internal", "billing", "extra.go"), "package billing\n")
		workflowGit(t, dir, "add", "-A")
		workflowGit(t, dir, "commit", "-q", "-m", "stale coverage under lock")
		fresh, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		fresh.Candidate = readLocalSourceCandidate(dir)
		if err := persistWorkflow(root, cfg, fresh); err != nil {
			t.Fatal(err)
		}
		release()
		got := <-ch
		if got.err == nil || got.task != nil {
			t.Fatalf("stale coverage admitted: %+v %v", got.task, got.err)
		}
		after := snapRepairState(t, root, cfg, wf.ID)
		if after.cardCount != before.cardCount || after.round != before.round || after.members != before.members {
			t.Fatalf("stale coverage mutated repair state before=%+v after=%+v", before, after)
		}
		if after.acceptance != before.acceptance {
			t.Fatalf("stale coverage created a new card: %s -> %s", before.acceptance, after.acceptance)
		}
	})
	t.Run("lease-reappearance", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("workspace flock is POSIX")
		}
		root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
		if !namedDirectionIsSoleRepairableFailure(root, wf, beta.SessionStage) {
			t.Fatal("outer filter must pass before lock")
		}
		if taskHasLiveWriterProof(root, beta) {
			t.Fatal("lease must be absent before reappearance")
		}
		before := snapRepairState(t, root, cfg, wf.ID)
		release := holdWorkflowAdmitLock(t, root, wf.ID)
		defer release()
		ch := goAdvanceRepair(root, cfg, wf, beta.SessionStage)
		waitLockedCaller(t)
		_ = holdExclusiveWorkspaceLease(t, beta.Dir)
		release()
		got := <-ch
		if got.err == nil || (got.res != nil && got.res.RepairCreated != 0) {
			t.Fatalf("lease reappearance admitted: %+v %v", got.res, got.err)
		}
		assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
	})
}

func TestOptInRepairStalePIDAndDisjointOccupancyStillOperate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	t.Run("stale-pid", func(t *testing.T) {
		root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
		plantStaleAttemptPID(t, root, beta.ID, beta.Dir, 424242)
		if taskHasLiveWriterProof(root, beta) {
			t.Fatal("stale nonzero PID must not be live writer proof")
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
			OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
		})
		if err != nil || res.RepairCreated != 1 {
			t.Fatalf("stale PID blocked legitimate repair: %+v %v", res, err)
		}
	})
	t.Run("disjoint-occupancy", func(t *testing.T) {
		root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
		other := t.TempDir()
		_ = holdExclusiveWorkspaceLease(t, other)
		extra := newTask(root, cfg, typeSequence, "unrelated occupant", other, []string{"other"}, 5)
		if err := saveTask(root, extra); err != nil {
			t.Fatal(err)
		}
		if !workspaceLeaseHeld(other) {
			t.Fatal("unrelated occupancy not held")
		}
		if taskHasLiveWriterProof(root, beta) {
			t.Fatal("disjoint occupancy leaked onto current target")
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
			OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
		})
		if err != nil || res.RepairCreated != 1 {
			t.Fatalf("disjoint occupancy blocked current-target repair: %+v %v", res, err)
		}
	})
}

func TestOptInRepairNeverStartedFailedNeedsNoFabricatedAttempt(t *testing.T) {
	root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
	has, err := taskHasAttemptRecord(root, beta.ID)
	if err != nil || has {
		t.Fatalf("pre-repair attempt=%v %v", has, err)
	}
	res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
	})
	if err != nil || res.RepairCreated != 1 {
		t.Fatalf("never-started failed: %+v %v", res, err)
	}
	has, err = taskHasAttemptRecord(root, beta.ID)
	if err != nil || has {
		t.Fatalf("fabricated attempt/closeout on never-started failed member has=%v %v", has, err)
	}
	betaAfter, err := loadTask(root, beta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if betaAfter.ActiveAttemptID != "" || betaAfter.LastCommittedTransitionID != "" {
		t.Fatalf("fabricated closeout identity: attempt=%s trans=%s", betaAfter.ActiveAttemptID, betaAfter.LastCommittedTransitionID)
	}
}

func scriptedCountingClaudeBin(t *testing.T, counterPath string, payloads []string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake claude script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	quoted := shSingleQuote(counterPath)
	b.WriteString("n=0\n")
	b.WriteString("if [ -f " + quoted + " ]; then n=$(wc -c < " + quoted + " | tr -d '[:space:]'); fi\n")
	b.WriteString("printf x >> " + quoted + "\n")
	for i, payload := range payloads {
		switch {
		case i == 0:
			b.WriteString("if [ \"$n\" = \"0\" ]; then\n")
		case i == len(payloads)-1:
			b.WriteString("else\n")
		default:
			fmt.Fprintf(&b, "elif [ \"$n\" = \"%d\" ]; then\n", i)
		}
		b.WriteString("cat <<'JSON_EOF'\n" + payload + "\nJSON_EOF\n")
	}
	b.WriteString("fi\nexit 0\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mkReviewResultJSON(sessionID, verdict string, p0, p1 []string) string {
	body := verdictJSON(verdict, p0, p1)
	blob, _ := json.Marshal(map[string]any{
		"type":           "result",
		"result":         body,
		"session_id":     sessionID,
		"num_turns":      1,
		"total_cost_usd": 0.01,
		"duration_ms":    123,
		"is_error":       false,
	})
	return string(blob)
}

func pinWorkflowClaude(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, bin string) {
	t.Helper()
	cfg.ClaudeBin = bin
	cfg.StepTimeoutMin = 1
	cfg.DrainRescanSec = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	wf.WriterEngine = "claude"
	wf.ReviewerEngine = "claude"
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateBinRepairHeldLeaseThenRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	bin := requireCandidateBin(t)
	root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
	lease := holdExclusiveWorkspaceLease(t, beta.Dir)
	before := snapRepairState(t, root, cfg, wf.ID)
	run := func() (string, int) {
		t.Helper()
		cmd := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1",
			"-repair-direction", beta.SessionStage)
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("advance: %v %s", err, out)
			}
		}
		return string(out), code
	}
	first, _ := run()
	if !strings.Contains(first, "repair_created=0") {
		t.Fatalf("CLI held lease must refuse: %s", first)
	}
	assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
	second, _ := run()
	if !strings.Contains(second, "repair_created=0") {
		t.Fatalf("CLI held lease second: %s", second)
	}
	assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
	if !workspaceLeaseHeld(beta.Dir) {
		t.Fatal("CLI refuse released fixture lease")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	admit, code := run()
	if code != 0 || !strings.Contains(admit, "repair_created=1") {
		t.Fatalf("CLI after release: code=%d %s", code, admit)
	}
	dup, _ := run()
	if !strings.Contains(dup, "repair_created=0") {
		t.Fatalf("CLI duplicate: %s", dup)
	}
	t.Logf("binary=%s argv0-bound held-lease refuse then one repair", bin)
	dumpItems11JSON(t, "green-lease-cli.json", map[string]any{
		"binary": bin, "held_stdout": []string{first, second}, "after_release": admit, "duplicate": dup,
	})
}

func TestCandidateBinNonpassReviewRepairPassStop(t *testing.T) {
	bin := requireCandidateBin(t)
	root, dir, cfg, wf, members := readyDisjointAcceptFixture(t)
	alpha, beta := members[0], members[1]
	alphaAttempts, alphaArtifact := alpha.Attempts, taskLogPath(root, alpha.ID)
	count := filepath.Join(t.TempDir(), "calls")
	finding := "beta billing coverage hole"
	fake := scriptedCountingClaudeBin(t, count, []string{
		mkReviewResultJSON("review-nonpass", "concerns", []string{finding}, nil),
		mkOKResultJSON("beta-repair"),
		mkReviewResultJSON("review-pass", "pass", nil, nil),
	})
	pinWorkflowClaude(t, root, cfg, wf, fake)
	markDurablyDone(t, root, wf.DesignTaskID)
	markDurablyDone(t, root, alpha.ID)
	markDurablyDone(t, root, beta.ID)
	runCLI := func(args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
		}
		return string(out), code
	}
	first, code := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1")
	if code != 0 || !strings.Contains(first, "acceptance_created=1") {
		t.Fatalf("unified acceptance: code=%d %s", code, first)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	accID := wf.AcceptanceTaskID
	if accID == "" {
		t.Fatal("missing acceptance")
	}
	beforeFake := providerCallCount(t, count)
	reviewOut, code := runCLI("run", "-root", root, "-quiet", accID)
	if code != 0 {
		t.Fatalf("fake reviewer: code=%d %s", code, reviewOut)
	}
	if providerCallCount(t, count) != beforeFake+1 {
		t.Fatalf("reviewer fake=%d before=%d", providerCallCount(t, count), beforeFake)
	}
	acc, err := loadTask(root, accID)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Status == statusFailed {
		t.Fatal("must not set member status=failed as the nonpass proof")
	}
	if acc.ReviewOutput == nil {
		t.Fatal("fake reviewer must emit ReviewOutput; do not hand-mark review done")
	}
	raw := loadTaskResultForGate(root, acc)
	v := parseReviewVerdict(raw)
	if v == nil || (v.Verdict != "concerns" && v.Verdict != "block") || (len(v.P0) == 0 && len(v.P1) == 0) {
		t.Fatalf("ReviewOutput findings missing: raw=%q v=%+v", raw, v)
	}
	if !strings.Contains(strings.Join(v.P0, "\n"), finding) && !strings.Contains(strings.Join(v.P1, "\n"), finding) {
		t.Fatalf("specific finding missing: %+v", v)
	}
	repairOut, code := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1",
		"-repair-direction", beta.SessionStage)
	if code != 0 || !strings.Contains(repairOut, "repair_created=1") {
		t.Fatalf("beta-only repair: code=%d %s", code, repairOut)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	repairID := ""
	for _, id := range wf.DirectionTaskIDs {
		if id != alpha.ID && id != beta.ID {
			repairID = id
			break
		}
	}
	if repairID == "" {
		t.Fatalf("missing repair members=%v", wf.DirectionTaskIDs)
	}
	alphaMid, err := loadTask(root, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if alphaMid.ID != alpha.ID || alphaMid.Attempts != alphaAttempts || taskLogPath(root, alphaMid.ID) != alphaArtifact {
		t.Fatalf("alpha changed after repair admit: %+v", alphaMid)
	}
	beforeRepair := providerCallCount(t, count)
	ran, code := runCLI("run", "-once", "-root", root, "-quiet", repairID)
	if code != 0 {
		t.Fatalf("fake repair run: code=%d %s", code, ran)
	}
	if providerCallCount(t, count) != beforeRepair+1 {
		t.Fatalf("repair fake=%d before=%d", providerCallCount(t, count), beforeRepair)
	}
	alphaAfterRepair, err := loadTask(root, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if alphaAfterRepair.Attempts != alphaAttempts || alphaAfterRepair.ID != alpha.ID {
		t.Fatalf("alpha rerun: %+v", alphaAfterRepair)
	}
	mustWriteFile(t, filepath.Join(dir, "internal", "billing", "fix.go"), "package billing\n// manual Git combination of current candidate\n")
	workflowGit(t, dir, "add", "-A")
	workflowGit(t, dir, "commit", "-q", "-m", "manual Git combination of current candidate")
	commit := workflowGit(t, dir, "rev-parse", "HEAD")
	tree := workflowGit(t, dir, "rev-parse", "HEAD^{tree}")
	freeze, code := runCLI("workflow", "freeze-candidate", wf.ID, "-root", root, "-commit", commit, "-tree", tree)
	if code != 0 {
		t.Fatalf("freeze: code=%d %s", code, freeze)
	}
	acc2Out, code := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1")
	if code != 0 || !strings.Contains(acc2Out, "acceptance_created=1") {
		t.Fatalf("new unified acceptance: code=%d %s", code, acc2Out)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	newAcc := wf.AcceptanceTaskID
	if newAcc == "" || newAcc == accID {
		t.Fatalf("expected new acceptance, got %s old=%s", newAcc, accID)
	}
	beforePass := providerCallCount(t, count)
	passRun, code := runCLI("run", "-root", root, "-quiet", newAcc)
	if code != 0 {
		t.Fatalf("pass reviewer: code=%d %s", code, passRun)
	}
	if providerCallCount(t, count) != beforePass+1 {
		t.Fatalf("pass reviewer fake=%d before=%d", providerCallCount(t, count), beforePass)
	}
	passAcc, err := loadTask(root, newAcc)
	if err != nil {
		t.Fatal(err)
	}
	if passAcc.ReviewOutput == nil {
		t.Fatal("pass reviewer missing ReviewOutput")
	}
	passRaw := loadTaskResultForGate(root, passAcc)
	passV := parseReviewVerdict(passRaw)
	if !reviewVerdictIsAdmissiblePass(passV) || !passV.p0Present || !passV.p1Present || len(passV.P0) != 0 || len(passV.P1) != 0 {
		t.Fatalf("pass ReviewOutput: raw=%q v=%+v", passRaw, passV)
	}
	stopOut, code := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1")
	if code != 0 || !strings.Contains(stopOut, "goal_accepted=true") {
		t.Fatalf("pass stop: code=%d %s", code, stopOut)
	}
	fakeAfterPass := providerCallCount(t, count)
	again, code := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1")
	if !strings.Contains(again, "repair_created=0") {
		t.Fatalf("idempotent: code=%d %s", code, again)
	}
	if strings.Contains(again, "repair_created=1") {
		t.Fatalf("extra repair: %s", again)
	}
	if providerCallCount(t, count) != fakeAfterPass {
		t.Fatalf("idempotent extra provider call fake=%d", providerCallCount(t, count))
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	if wf.WriterTaskID != "" {
		t.Fatalf("next writer opened: %s", wf.WriterTaskID)
	}
	alphaFinal, err := loadTask(root, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if alphaFinal.ID != alpha.ID || alphaFinal.Attempts != alphaAttempts {
		t.Fatalf("alpha changed: %+v", alphaFinal)
	}
	t.Logf("manual Git combination labeled; binary=%s fake=%d argv=workflow advance / run -once", bin, providerCallCount(t, count))
	dumpItems11JSON(t, "nonpass-chain.json", map[string]any{
		"binary":                 bin,
		"acceptance_created":     first,
		"reviewer_stdout":        reviewOut,
		"repair_created":         repairOut,
		"repair_run":             ran,
		"new_acceptance":         acc2Out,
		"pass_review":            passRun,
		"stop":                   stopOut,
		"idempotent":             again,
		"fake_executions":        providerCallCount(t, count),
		"alpha_id":               alpha.ID,
		"alpha_attempts":         alphaAttempts,
		"review_verdict":         v.Verdict,
		"review_p0":              v.P0,
		"review_p1":              v.P1,
		"pass_verdict":           passV.Verdict,
		"pass_p0_present":        passV.p0Present,
		"pass_p1_present":        passV.p1Present,
		"pass_p0":                passV.P0,
		"pass_p1":                passV.P1,
		"real_provider_calls":    0,
		"manual_git_combination": "labeled",
	})
}

func dumpItems11JSON(t *testing.T, name string, body map[string]any) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("CARDEX_ITEMS11_SCRATCH"))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	blob, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(blob, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
