package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	expected, capErr := captureNamedRepairExpected(root, wf, named)
	ch := make(chan struct {
		task *Task
		err  error
	}, 1)
	go func() {
		if capErr != nil {
			ch <- struct {
				task *Task
				err  error
			}{nil, capErr}
			return
		}
		task, err := admitKnownDirectionRepair(root, cfg, wf, named, expected)
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

func rewriteValidReviewFindings(t *testing.T, root, id, body string) {
	t.Helper()
	review, err := loadTask(root, id)
	if err != nil || review == nil || review.ReviewOutput == nil {
		t.Fatalf("rewrite findings: %v %+v", err, review)
	}
	trimmed := strings.TrimSpace(body)
	header := "--- RESULT ---\n"
	if err := os.WriteFile(taskLogPath(root, id), []byte(header+trimmed+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	review.ReviewOutput.Offset = int64(len(header))
	review.ReviewOutput.Bytes = int64(len(trimmed))
	review.ReviewOutput.SHA256 = sha256Hex(trimmed)
	if err := writeTaskFile(root, review); err != nil {
		t.Fatal(err)
	}
}

func replaceWithValidFailedSameName(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, old *Task) *Task {
	t.Helper()
	if old == nil {
		t.Fatal("missing original member")
	}
	clone := newTask(root, cfg, old.Type, old.Title, old.Dir, append([]string(nil), old.Prompts...), old.Priority)
	clone.Project = old.Project
	clone.WorkflowID = wf.ID
	clone.DependsOn = append([]string(nil), old.DependsOn...)
	clone.WriteDomain = inheritWriteDomain(old.WriteDomain)
	clone.SessionStage = old.SessionStage
	clone.SessionRole = old.SessionRole
	clone.FixRound = old.FixRound
	clone.Status = statusFailed
	clone.LastError = "different current " + old.SessionStage + " failure, NOT original target " + old.ID
	inheritRepairEffectLimits(clone, old)
	if err := saveTask(root, clone); err != nil {
		t.Fatal(err)
	}
	ids := append([]string(nil), wf.DirectionTaskIDs...)
	replaced := false
	for i, id := range ids {
		if id == old.ID {
			ids[i] = clone.ID
			replaced = true
		}
	}
	if !replaced {
		t.Fatalf("old member %s not in %v", old.ID, ids)
	}
	wf.DirectionTaskIDs = ids
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	if clone.ID == old.ID {
		t.Fatal("replacement reused TaskID")
	}
	if !namedDirectionIsSoleRepairableFailure(root, wf, clone.SessionStage) {
		t.Fatal("replacement must remain a valid failed ordinary same-name member")
	}
	return clone
}

func TestOptInRepairPreLockExpectedSameNameReplacementRefuses(t *testing.T) {
	root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
	expected, err := captureNamedRepairExpected(root, wf, beta.SessionStage)
	if err != nil {
		t.Fatal(err)
	}
	if expected.TargetID != beta.ID || expected.FailureIdentity == "" {
		t.Fatalf("expected scalars: %+v original=%s", expected, beta.ID)
	}
	replacement := replaceWithValidFailedSameName(t, root, cfg, wf, beta)
	if replacement.ID == expected.TargetID {
		t.Fatal("replacement TaskID matched pre-lock expected")
	}
	before := snapRepairState(t, root, cfg, wf.ID)
	got, err := admitKnownDirectionRepair(root, cfg, wf, beta.SessionStage, expected)
	if err == nil || got != nil {
		t.Fatalf("same-name replacement admitted: %+v %v", got, err)
	}
	assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
}

func TestOptInRepairPreLockExpectedChangedFindingsRefuses(t *testing.T) {
	root, _, cfg, wf, members := readyDisjointAcceptFixture(t)
	beta := members[1]
	if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 1}); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("outer findings/coverage must pass before capture")
	}
	expected, err := captureNamedRepairExpected(root, wf, beta.SessionStage)
	if err != nil {
		t.Fatal(err)
	}
	before := snapRepairState(t, root, cfg, wf.ID)
	rewriteValidReviewFindings(t, root, acc.ID, verdictJSON("concerns", []string{"different valid beta finding"}, nil))
	if !namedDirectionRepairableFromCurrentFindings(root, wf, beta.SessionStage) {
		t.Fatal("changed findings must remain a valid current source")
	}
	got, err := admitKnownDirectionRepair(root, cfg, wf, beta.SessionStage, expected)
	if err == nil || got != nil {
		t.Fatalf("changed valid findings admitted: %+v %v", got, err)
	}
	assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
}

func collidingFindingsConcat(p0 []string) string {
	var b strings.Builder
	for i, p := range p0 {
		fmt.Fprintf(&b, "P0-%d=%s\n", i+1, p)
	}
	return b.String()
}

func TestOptInRepairPreLockExpectedCollidingFindingsRefuses(t *testing.T) {
	first := []string{"x\nP0-2=y"}
	second := []string{"x", "y"}
	if collidingFindingsConcat(first) != collidingFindingsConcat(second) {
		t.Fatal("counterexample must collide under newline-concat P0 encoding")
	}
	root, _, cfg, wf, members := readyDisjointAcceptFixture(t)
	beta := members[1]
	if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 1}); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := loadTask(root, wf.AcceptanceTaskID)
	if err != nil {
		t.Fatal(err)
	}
	acc.Status = statusDone
	if err := writeTaskFile(root, acc); err != nil {
		t.Fatal(err)
	}
	writeReviewLog(t, root, acc.ID, verdictJSON("concerns", first, nil))
	if !namedDirectionRepairableFromCurrentFindings(root, wf, beta.SessionStage) || !currentAcceptanceCoverageHolds(root, wf) {
		t.Fatal("outer findings/coverage must pass before capture")
	}
	expected, err := captureNamedRepairExpected(root, wf, beta.SessionStage)
	if err != nil {
		t.Fatal(err)
	}
	before := snapRepairState(t, root, cfg, wf.ID)
	rewriteValidReviewFindings(t, root, acc.ID, verdictJSON("concerns", second, nil))
	if !namedDirectionRepairableFromCurrentFindings(root, wf, beta.SessionStage) {
		t.Fatal("colliding-array findings must remain a valid current source")
	}
	gotID := namedRepairFindingsIdentity(root, wf)
	if gotID == "" || gotID == expected.FindingsIdentity {
		t.Fatalf("JSON findings identity collided first=%q second=%q", expected.FindingsIdentity, gotID)
	}
	got, err := admitKnownDirectionRepair(root, cfg, wf, beta.SessionStage, expected)
	if err == nil || got != nil {
		t.Fatalf("newline-colliding valid findings admitted: %+v %v", got, err)
	}
	assertNoRepairMutation(t, before, snapRepairState(t, root, cfg, wf.ID))
}

func TestOptInRepairPreLockExpectedChangedCoverageRefuses(t *testing.T) {
	root, dir, cfg, wf, members := readyDisjointAcceptFixture(t)
	beta := members[1]
	if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 1}); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("outer findings/coverage must pass before capture")
	}
	expected, err := captureNamedRepairExpected(root, wf, beta.SessionStage)
	if err != nil {
		t.Fatal(err)
	}
	before := snapRepairState(t, root, cfg, wf.ID)
	mustWriteFile(t, filepath.Join(dir, "internal", "billing", "extra.go"), "package billing\n")
	workflowGit(t, dir, "add", "-A")
	workflowGit(t, dir, "commit", "-q", "-m", "changed valid coverage after pre-lock capture")
	fresh, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	cand := readLocalSourceCandidate(dir)
	fresh.Candidate = cand
	acc, err = loadTask(root, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	acc.ReviewCandidate = &WorkflowCandidate{
		Commit:       cand.Commit,
		Tree:         cand.Tree,
		Branch:       cand.Branch,
		ChangedPaths: append([]string(nil), cand.ChangedPaths...),
	}
	if acc.ReviewOutput != nil {
		acc.ReviewOutput.CandidateCommit = cand.Commit
		acc.ReviewOutput.CandidateTree = cand.Tree
	}
	if err := writeTaskFile(root, acc); err != nil {
		t.Fatal(err)
	}
	fp, err := acceptanceFingerprint(root, fresh)
	if err != nil {
		t.Fatal(err)
	}
	fresh.AcceptanceCoverage = fp
	if err := persistWorkflow(root, cfg, fresh); err != nil {
		t.Fatal(err)
	}
	wf = fresh
	if !namedDirectionRepairableFromCurrentFindings(root, wf, beta.SessionStage) || !currentAcceptanceCoverageHolds(root, wf) {
		t.Fatal("changed coverage must remain a valid current source")
	}
	got, err := admitKnownDirectionRepair(root, cfg, wf, beta.SessionStage, expected)
	if err == nil || got != nil {
		t.Fatalf("changed valid coverage admitted: %+v %v", got, err)
	}
	after := snapRepairState(t, root, cfg, wf.ID)
	if after.cardCount != before.cardCount || after.round != before.round || after.members != before.members {
		t.Fatalf("changed coverage mutated repair state before=%+v after=%+v", before, after)
	}
	if after.acceptance != before.acceptance {
		t.Fatalf("changed coverage created a new card: %s -> %s", before.acceptance, after.acceptance)
	}
}

func TestOptInRepairPreLockExpectedUnchangedAdmitsExactlyOne(t *testing.T) {
	root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
	expected, err := captureNamedRepairExpected(root, wf, beta.SessionStage)
	if err != nil {
		t.Fatal(err)
	}
	first, err := admitKnownDirectionRepair(root, cfg, wf, beta.SessionStage, expected)
	if err != nil || first == nil || first.ID == "" || first.ID == beta.ID {
		t.Fatalf("unchanged expected must admit one repair: %+v %v", first, err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.CurrentRound != 1 {
		t.Fatalf("round=%d", wf.CurrentRound)
	}
	mid := snapRepairState(t, root, cfg, wf.ID)
	dup, err := admitKnownDirectionRepair(root, cfg, wf, beta.SessionStage, expected)
	if err == nil || dup != nil {
		t.Fatalf("duplicate with same expected admitted: %+v %v", dup, err)
	}
	assertNoRepairMutation(t, mid, snapRepairState(t, root, cfg, wf.ID))
	fresh, capErr := captureNamedRepairExpected(root, wf, beta.SessionStage)
	if capErr != nil {
		return
	}
	again, err := admitKnownDirectionRepair(root, cfg, wf, beta.SessionStage, fresh)
	if err == nil || again != nil {
		t.Fatalf("fresh capture after one repair admitted another: %+v %v", again, err)
	}
	assertNoRepairMutation(t, mid, snapRepairState(t, root, cfg, wf.ID))
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

type candidateCLIExec struct {
	Argv       []string `json:"argv"`
	Exit       int      `json:"exit"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	Binary     string   `json:"binary"`
	BinaryHash string   `json:"binary_hash"`
}

func (r candidateCLIExec) text() string { return r.Stdout + r.Stderr }

func execCandidateCLI(t *testing.T, bin, hash string, args ...string) candidateCLIExec {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("%v: %v\nstdout=%s\nstderr=%s", args, err, stdout.String(), stderr.String())
		}
	}
	return candidateCLIExec{
		Argv:       append([]string{bin}, append([]string(nil), args...)...),
		Exit:       code,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		Binary:     bin,
		BinaryHash: hash,
	}
}

func seedAlphaArtifactIfMissing(t *testing.T, path string) bool {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		return false
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("items11-prelock-intent-finish-20261025 labeled initial alpha artifact seed\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return true
}

func TestCandidateBinNonpassReviewRepairPassStop(t *testing.T) {
	bin := requireCandidateBin(t)
	binHash, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	root, dir, cfg, wf, members := readyDisjointAcceptFixture(t)
	alpha, beta := members[0], members[1]
	alphaAttempts, alphaArtifact := alpha.Attempts, taskLogPath(root, alpha.ID)
	alphaSeeded := seedAlphaArtifactIfMissing(t, alphaArtifact)
	alphaBefore, err := fileSHA256(alphaArtifact)
	if err != nil {
		t.Fatal(err)
	}
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
	var executed []candidateCLIExec
	runCLI := func(args ...string) candidateCLIExec {
		t.Helper()
		got := execCandidateCLI(t, bin, binHash, args...)
		executed = append(executed, got)
		return got
	}
	first := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1")
	if first.Exit != 0 || !strings.Contains(first.text(), "acceptance_created=1") {
		t.Fatalf("unified acceptance: code=%d %s", first.Exit, first.text())
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	accID := wf.AcceptanceTaskID
	if accID == "" {
		t.Fatal("missing acceptance")
	}
	beforeFake := providerCallCount(t, count)
	reviewOut := runCLI("run", "-root", root, "-quiet", accID)
	if reviewOut.Exit != 0 {
		t.Fatalf("fake reviewer: code=%d %s", reviewOut.Exit, reviewOut.text())
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
	repairOut := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1",
		"-repair-direction", beta.SessionStage)
	if repairOut.Exit != 0 || !strings.Contains(repairOut.text(), "repair_created=1") {
		t.Fatalf("beta-only repair: code=%d %s", repairOut.Exit, repairOut.text())
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
	ran := runCLI("run", "-once", "-root", root, "-quiet", repairID)
	if ran.Exit != 0 {
		t.Fatalf("fake repair run: code=%d %s", ran.Exit, ran.text())
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
	freeze := runCLI("workflow", "freeze-candidate", wf.ID, "-root", root, "-commit", commit, "-tree", tree)
	if freeze.Exit != 0 {
		t.Fatalf("freeze: code=%d %s", freeze.Exit, freeze.text())
	}
	acc2Out := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1")
	if acc2Out.Exit != 0 || !strings.Contains(acc2Out.text(), "acceptance_created=1") {
		t.Fatalf("new unified acceptance: code=%d %s", acc2Out.Exit, acc2Out.text())
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	newAcc := wf.AcceptanceTaskID
	if newAcc == "" || newAcc == accID {
		t.Fatalf("expected new acceptance, got %s old=%s", newAcc, accID)
	}
	beforePass := providerCallCount(t, count)
	passRun := runCLI("run", "-root", root, "-quiet", newAcc)
	if passRun.Exit != 0 {
		t.Fatalf("pass reviewer: code=%d %s", passRun.Exit, passRun.text())
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
	stopOut := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1")
	if stopOut.Exit != 0 || !strings.Contains(stopOut.text(), "goal_accepted=true") {
		t.Fatalf("pass stop: code=%d %s", stopOut.Exit, stopOut.text())
	}
	fakeAfterPass := providerCallCount(t, count)
	again := runCLI("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1")
	if !strings.Contains(again.text(), "repair_created=0") {
		t.Fatalf("idempotent: code=%d %s", again.Exit, again.text())
	}
	if strings.Contains(again.text(), "repair_created=1") {
		t.Fatalf("extra repair: %s", again.text())
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
	alphaAfter, err := fileSHA256(alphaArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if alphaAfter != alphaBefore {
		t.Fatalf("alpha artifact bytes changed %s -> %s path=%s", alphaBefore, alphaAfter, alphaArtifact)
	}
	if providerCallCount(t, count) != 3 {
		t.Fatalf("fake executions=%d want 3", providerCallCount(t, count))
	}
	t.Logf("manual Git combination labeled; binary=%s fake=%d argv=workflow advance / run -once alpha_seeded=%t", bin, providerCallCount(t, count), alphaSeeded)
	dumpItems11JSON(t, "nonpass-chain.json", map[string]any{
		"binary":                       bin,
		"binary_hash":                  binHash,
		"cli_executions":               executed,
		"acceptance_created":           first,
		"reviewer":                     reviewOut,
		"repair_created":               repairOut,
		"repair_run":                   ran,
		"freeze":                       freeze,
		"new_acceptance":               acc2Out,
		"pass_review":                  passRun,
		"stop":                         stopOut,
		"idempotent":                   again,
		"fake_executions":              providerCallCount(t, count),
		"raw_review_output_nonpass":    acc.ReviewOutput,
		"raw_review_result_nonpass":    raw,
		"raw_fix_output":               ran,
		"raw_review_output_pass":       passAcc.ReviewOutput,
		"raw_review_result_pass":       passRaw,
		"alpha_id":                     alpha.ID,
		"alpha_attempts":               alphaAttempts,
		"alpha_artifact_path":          alphaArtifact,
		"alpha_artifact_seeded":        alphaSeeded,
		"alpha_artifact_sha256_before": alphaBefore,
		"alpha_artifact_sha256_after":  alphaAfter,
		"review_verdict":               v.Verdict,
		"review_p0":                    v.P0,
		"review_p1":                    v.P1,
		"pass_verdict":                 passV.Verdict,
		"pass_p0_present":              passV.p0Present,
		"pass_p1_present":              passV.p1Present,
		"pass_p0":                      passV.P0,
		"pass_p1":                      passV.P1,
		"real_provider_calls":          0,
		"manual_git_combination":       "labeled",
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

func items11ScratchDir() string {
	return strings.TrimSpace(os.Getenv("CARDEX_ITEMS11_SCRATCH"))
}

func writeItems11File(t *testing.T, name, body string) {
	t.Helper()
	dir := items11ScratchDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lsofBin() string {
	for _, p := range []string{"lsof", "/usr/sbin/lsof", "/usr/bin/lsof"} {
		if abs, err := exec.LookPath(p); err == nil {
			return abs
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func pollCLIOpenLock(t *testing.T, pid int, lockPath string) (out string, ok bool, unavailable string) {
	t.Helper()
	bin := lsofBin()
	if bin == "" {
		return "", false, "lsof binary not found on PATH or /usr/sbin/lsof"
	}
	deadline := time.Now().Add(8 * time.Second)
	var last string
	pidStr := strconv.Itoa(pid)
	for time.Now().Before(deadline) {
		cmd := exec.Command(bin, "-nP", "-a", "-p", pidStr, "--", lockPath)
		raw, err := cmd.CombinedOutput()
		last = string(raw)
		if err == nil && (strings.Contains(last, lockPath) || strings.Contains(last, filepath.Base(lockPath))) {
			return last, true, ""
		}
		wide := exec.Command(bin, "-nP", "--", lockPath)
		wideRaw, _ := wide.CombinedOutput()
		last = last + "\n" + string(wideRaw)
		if strings.Contains(string(wideRaw), pidStr) {
			return last, true, ""
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last, false, ""
}

type lockFDRun struct {
	Argv             []string
	Exit             int
	Stdout           string
	Stderr           string
	Binary           string
	BinaryHash       string
	LockPath         string
	Lsof             string
	LsofOK           bool
	LsofUnavailable  string
	OriginalTargetID string
	ReplacementID    string
	MembersBefore    string
	MembersAfter     string
	MembersAtWait    string
	RoundBefore      int
	RoundAfter       int
	RoundAtWait      int
	CardsBefore      int
	CardsAfter       int
	CardsAtWait      int
	Root             string
	WorkflowID       string
	RepairDirection  string
}

func dumpLockFDRun(t *testing.T, name string, run lockFDRun) {
	t.Helper()
	writeItems11File(t, name+".cmd", strings.Join(run.Argv, " ")+"\n")
	writeItems11File(t, name+".exit", strconv.Itoa(run.Exit)+"\n")
	writeItems11File(t, name+".stdout", run.Stdout)
	writeItems11File(t, name+".stderr", run.Stderr)
	dumpItems11JSON(t, name+".json", map[string]any{
		"argv":               run.Argv,
		"exit":               run.Exit,
		"stdout":             run.Stdout,
		"stderr":             run.Stderr,
		"binary":             run.Binary,
		"binary_hash":        run.BinaryHash,
		"lock_path":          run.LockPath,
		"lsof":               run.Lsof,
		"lsof_ok":            run.LsofOK,
		"lsof_unavailable":   run.LsofUnavailable,
		"original_target_id": run.OriginalTargetID,
		"replacement_id":     run.ReplacementID,
		"members_before":     run.MembersBefore,
		"members_at_wait":    run.MembersAtWait,
		"members_after":      run.MembersAfter,
		"round_before":       run.RoundBefore,
		"round_at_wait":      run.RoundAtWait,
		"round_after":        run.RoundAfter,
		"cards_before":       run.CardsBefore,
		"cards_at_wait":      run.CardsAtWait,
		"cards_after":        run.CardsAfter,
	})
}

func runSameNameLockFD(t *testing.T, bin string, replace bool) lockFDRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	hash, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	root, _, cfg, wf, _, beta := failedBetaOwnWorkspace(t)
	lockPath := controlLockPath(root, "workflow-admit:"+wf.ID)
	before := snapRepairState(t, root, cfg, wf.ID)
	release := holdWorkflowAdmitLock(t, root, wf.ID)
	defer release()
	args := []string{"workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1",
		"-repair-direction", beta.SessionStage}
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	lsofOut, lsofOK, lsofUnavail := pollCLIOpenLock(t, cmd.Process.Pid, lockPath)
	if lsofUnavail != "" {
		writeItems11File(t, "lsof-unavailable.log", lsofUnavail+"\n")
	}
	replacementID := ""
	atWait := before
	if replace {
		if !lsofOK && lsofUnavail == "" {
			writeItems11File(t, "lsof-unavailable.log", "lsof ran but did not observe CLI open fd on "+lockPath+"\nlast=\n"+lsofOut+"\n")
		}
		if lsofOK || lsofUnavail != "" {
			rep := replaceWithValidFailedSameName(t, root, cfg, wf, beta)
			replacementID = rep.ID
			atWait = snapRepairState(t, root, cfg, wf.ID)
		} else {
			cmd.Process.Kill()
			<-waited
			t.Fatalf("lsof did not show CLI pid %d on %s\n%s", cmd.Process.Pid, lockPath, lsofOut)
		}
	}
	release()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		<-waited
		t.Fatal("CLI timed out waiting on workflow-admit lock")
	}
	exit := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("CLI wait: %v stdout=%s stderr=%s", waitErr, stdout.String(), stderr.String())
		}
	}
	after := snapRepairState(t, root, cfg, wf.ID)
	run := lockFDRun{
		Argv:             append([]string{bin}, args...),
		Exit:             exit,
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		Binary:           bin,
		BinaryHash:       hash,
		LockPath:         lockPath,
		Lsof:             lsofOut,
		LsofOK:           lsofOK,
		LsofUnavailable:  lsofUnavail,
		OriginalTargetID: beta.ID,
		ReplacementID:    replacementID,
		MembersBefore:    before.members,
		MembersAtWait:    atWait.members,
		MembersAfter:     after.members,
		RoundBefore:      before.round,
		RoundAtWait:      atWait.round,
		RoundAfter:       after.round,
		CardsBefore:      before.cardCount,
		CardsAtWait:      atWait.cardCount,
		CardsAfter:       after.cardCount,
		Root:             root,
		WorkflowID:       wf.ID,
		RepairDirection:  beta.SessionStage,
	}
	return run
}

func TestCandidateBinSameNameReplacementUnderLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	bin := requireCandidateBin(t)
	first := runSameNameLockFD(t, bin, true)
	dumpLockFDRun(t, "lockfd-current-1", first)
	text := first.Stdout + first.Stderr
	if first.Exit == 0 && strings.Contains(text, "repair_created=1") {
		t.Fatalf("current CLI admitted same-name replacement: exit=%d %s", first.Exit, text)
	}
	if !strings.Contains(text, "repair_created=0") {
		t.Fatalf("current CLI must refuse with repair_created=0: exit=%d %s", first.Exit, text)
	}
	if first.CardsAfter != first.CardsAtWait || first.RoundAfter != first.RoundAtWait || first.MembersAfter != first.MembersAtWait {
		t.Fatalf("current CLI mutated under same-name replacement: %+v", first)
	}
	second := runSameNameLockFD(t, bin, true)
	dumpLockFDRun(t, "lockfd-current-2", second)
	text2 := second.Stdout + second.Stderr
	if second.Exit == 0 && strings.Contains(text2, "repair_created=1") {
		t.Fatalf("current CLI second admitted same-name replacement: exit=%d %s", second.Exit, text2)
	}
	if !strings.Contains(text2, "repair_created=0") {
		t.Fatalf("current CLI second must refuse: exit=%d %s", second.Exit, text2)
	}
	if second.CardsAfter != second.CardsAtWait || second.RoundAfter != second.RoundAtWait || second.MembersAfter != second.MembersAtWait {
		t.Fatalf("current CLI second mutated: %+v", second)
	}
}

func TestCandidateBinUnchangedLockWaitAdmitsExactlyOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	bin := requireCandidateBin(t)
	first := runSameNameLockFD(t, bin, false)
	dumpLockFDRun(t, "lockfd-unchanged-1", first)
	text := first.Stdout + first.Stderr
	if first.Exit != 0 || !strings.Contains(text, "repair_created=1") {
		t.Fatalf("unchanged lock wait must admit one: exit=%d %s", first.Exit, text)
	}
	if first.RoundAfter != first.RoundBefore+1 || first.CardsAfter != first.CardsBefore+1 {
		t.Fatalf("unchanged lock wait missing repair mutation: %+v", first)
	}
	hash, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	second := execCandidateCLI(t, bin, hash, "workflow", "advance", first.WorkflowID, "-root", first.Root, "-opt-in", "-max-actions", "1",
		"-repair-direction", first.RepairDirection)
	dumpLockFDRun(t, "lockfd-unchanged-2", lockFDRun{
		Argv: second.Argv, Exit: second.Exit, Stdout: second.Stdout, Stderr: second.Stderr,
		Binary: bin, BinaryHash: hash, Root: first.Root, WorkflowID: first.WorkflowID,
		RepairDirection: first.RepairDirection,
	})
	if !strings.Contains(second.text(), "repair_created=0") {
		t.Fatalf("unchanged lock wait duplicate must be 0: %s", second.text())
	}
}

func retainedRedBinPath() string {
	if p := strings.TrimSpace(os.Getenv("CARDEX_RETAINED_RED_BIN")); p != "" {
		return p
	}
	return filepath.Join("..", "..", ".cardex-stage", "closeout-20261024",
		"items11-repair-custody-fix-20261025", "outputs", "items11-repair-custody-fix-20261025-cardex")
}

func TestRetainedRedBinSameNameReplacementStillAdmits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	bin := retainedRedBinPath()
	st, err := os.Stat(bin)
	if err != nil || st.IsDir() {
		t.Skip("retained RED binary not present")
	}
	sum, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	if sum != "3b118cfa208a9db4d1544520521d38f19fd4ee62c08d57bcf1a807913a03c41a" {
		t.Fatalf("retained RED sha256=%s", sum)
	}
	run := runSameNameLockFD(t, bin, true)
	dumpLockFDRun(t, "lockfd-retained-red", run)
	text := run.Stdout + run.Stderr
	if run.Exit != 0 || !strings.Contains(text, "repair_created=1") || strings.Contains(text, "held=true") {
		t.Fatalf("retained RED must still admit same-name replacement: exit=%d %s", run.Exit, text)
	}
	if !strings.Contains(text, "held=false") {
		t.Fatalf("retained RED held flag: %s", text)
	}
	if run.CardsAfter != run.CardsAtWait+1 || run.RoundAfter != run.RoundAtWait+1 {
		t.Fatalf("retained RED did not create repair: %+v", run)
	}
}
