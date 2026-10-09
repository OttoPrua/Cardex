package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// admitHostedGoalThenRelease drives the shipped admission reserve then
// releaseAdmission: freeze, acquire, admitManualGoalLaunchLocked(hosted),
// release. It does not open a PTY or call a paid provider.
func admitHostedGoalThenRelease(t *testing.T) (root string, cfg *Config, wf *WorkflowRecord, tk *Task) {
	t.Helper()
	root, dir := workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv"))
	wf = initTestWorkflow(t, root, dir)
	tk = admitManualWriter(t, root, cfg, wf)
	if err := freezeManualGoalModel(context.Background(), root, cfg, tk); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if !acquireLock(root, time.Hour) {
		t.Fatal("acquire admission lock")
	}
	if err := admitManualGoalLaunchLocked(root, cfg, wf, tk, 0, true); err != nil {
		releaseLock(root)
		t.Fatalf("admit hosted: %v", err)
	}
	if tk.ActiveAttemptID == "" || tk.Status != statusRunning || tk.Goal == nil || !tk.Goal.Hosted {
		releaseLock(root)
		t.Fatalf("admission must persist hosted active eligible metadata under ownership: status=%s attempt=%q hosted=%v", tk.Status, tk.ActiveAttemptID, tk.Goal != nil && tk.Goal.Hosted)
	}
	if tk.effectiveControlState() != controlEligible {
		releaseLock(root)
		t.Fatalf("admitted hosted task must stay eligible, got %s", tk.effectiveControlState())
	}
	releaseLock(root)
	if schedulerWriteAllowed(root) {
		t.Fatal("former owner must be fail-closed after releaseAdmission (schedulerOwned still remembers this root)")
	}
	return root, cfg, wf, tk
}

func TestHostedPostAdmissionMetadataDoesNotInvalidateProducer(t *testing.T) {
	root, cfg, wf, tk := admitHostedGoalThenRelease(t)

	err := persistHostedGoalSupervisorStart(root, tk)
	invalidated := producerInvalidated(root, tk)
	staleEvent := hasStaleWriteEvent(root, tk.ID, tk.ActiveAttemptID)
	if err != nil || invalidated || staleEvent {
		t.Fatalf("legitimate post-admission hosted metadata persist: err=%v producer_invalidated=%v stale_attempt_write_rejected=%v (want nil/false/false)",
			err, invalidated, staleEvent)
	}
	fresh, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if fresh.ActiveAttemptID == "" || !isActiveStatus(fresh.Status) || fresh.Goal == nil || !fresh.Goal.Hosted {
		t.Fatalf("task must remain admitted active hosted: status=%s attempt=%q goal=%+v", fresh.Status, fresh.ActiveAttemptID, fresh.Goal)
	}
	if _, serr := loadGoalHostStatus(root, fresh.ID, firstNonBlank(fresh.ActiveAttemptID, fresh.Goal.BoundAttemptID)); serr != nil {
		t.Fatalf("host-status: %v", serr)
	}

	finalErr := withWorkflowSchedulerLock(root, cfg, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return finalizeManualGoalLaunch(root, cfg, wf, tk, ctx, nil)
	})
	if producerInvalidated(root, tk) || errors.Is(finalErr, errSchedulerLockLost) || errors.Is(finalErr, errStaleTaskWrite) {
		t.Fatalf("finalization/goal-sync custody must not be blocked by stale CAS: %v", finalErr)
	}
	if finalErr != nil && !errors.Is(finalErr, errGoalLaunchUnstarted) {
		t.Fatalf("unexpected finalize without a live child: %v", finalErr)
	}
}

func TestHostedPostAdmissionOwnedFailurePersistDoesNotInvalidate(t *testing.T) {
	root, cfg, _, tk := admitHostedGoalThenRelease(t)
	tk.Goal.FailureClass = goalFailPTYMissing
	if err := persistHostedGoalOwned(root, cfg, tk); err != nil {
		t.Fatalf("owned failure-class persist after release: %v", err)
	}
	if producerInvalidated(root, tk) || hasStaleWriteEvent(root, tk.ID, tk.ActiveAttemptID) {
		t.Fatal("owned persist must not invalidate a valid producer or emit stale_attempt_write_rejected")
	}
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Goal == nil || fresh.Goal.FailureClass != goalFailPTYMissing {
		t.Fatalf("failure class must persist while owned: %+v", fresh.Goal)
	}
	if fresh.ActiveAttemptID == "" || !isActiveStatus(fresh.Status) {
		t.Fatalf("owned persist must keep the admitted active attempt: status=%s attempt=%q", fresh.Status, fresh.ActiveAttemptID)
	}
}

func bindExitedHostedAttempt(t *testing.T, root string, tk *Task) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := bindAttemptProcess(root, tk.ID, tk.ActiveAttemptID, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("bindAttemptProcess: %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func TestHostedPersistErrorAfterExitStillReclaimsCustody(t *testing.T) {
	root, cfg, wf, tk := admitHostedGoalThenRelease(t)
	bindExitedHostedAttempt(t, root, tk)
	if err := saveTask(root, tk); !errors.Is(err, errSchedulerLockLost) {
		t.Fatalf("former-owner active write must stay denied: %v", err)
	}
	persistErr := errors.New("hosted inject persist failed")
	runErr := errors.New("exit status 2")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := completeHostedGrokGoalAfterCmd(root, cfg, wf, tk, ctx, runErr, nil, persistErr, true)
	if errors.Is(err, errStaleTaskWrite) || errors.Is(err, errSchedulerLockLost) || errors.Is(err, errProducerInvalidated) {
		t.Fatalf("inject persist error must not skip finalize into stale denial: %v", err)
	}
	if err == nil {
		t.Fatal("nonzero exit must surface")
	}
	fresh, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if fresh.Status != statusHeld || fresh.Goal == nil || fresh.Goal.Observation != goalObsUnknown {
		t.Fatalf("actual exit must stay held/unknown: status=%s goal=%+v", fresh.Status, fresh.Goal)
	}
	if fresh.Goal.Observation == goalObsDone {
		t.Fatal("exit must not be accepted as done")
	}
	if !fresh.Goal.CustodyReleased || fresh.ActiveAttemptID != "" {
		t.Fatalf("custody must be reclaimed: custody=%v attempt=%q", fresh.Goal.CustodyReleased, fresh.ActiveAttemptID)
	}
	assertNoGoalEvDone(t, root, fresh)
}

func TestHostedPostAdmissionRawSaveStillDeniedAfterRelease(t *testing.T) {
	root, _, _, tk := admitHostedGoalThenRelease(t)
	tk.Goal.ObservationNote = "probe former-owner active write"
	err := saveTask(root, tk)
	if !errors.Is(err, errSchedulerLockLost) {
		t.Fatalf("genuine former-owner active save must stay denied: %v", err)
	}
	if !producerInvalidated(root, tk) {
		t.Fatal("genuine stale write must invalidate the producer")
	}
	if !hasStaleWriteEvent(root, tk.ID, tk.ActiveAttemptID) {
		t.Fatal("missing stale_attempt_write_rejected for genuine former-owner write")
	}
}

func TestHostedBudgetLimitedFinalizeSyncClearsActiveAttempt(t *testing.T) {
	root, cfg, wf, tk := admitHostedGoalThenRelease(t)
	bindExitedHostedAttempt(t, root, tk)
	home := t.TempDir()
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	gid := "goal-budget-finalize"
	tk.SessionID = sid
	tk.Goal.NativeGoalID = gid
	tk.Goal.GrokHome = home
	if err := persistHostedGoalOwned(root, cfg, tk); err != nil {
		t.Fatalf("persist session identity: %v", err)
	}
	writeGrokGoalFixture(t, home, tk.Dir, sid, gid, "budget_limited", grokNativeGoalStateFile{
		LastClassifierVerdict: "not_achieved",
		Phase:                 "Idle",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := completeHostedGrokGoalAfterCmd(root, cfg, wf, tk, ctx, nil, nil, nil, true)
	if errors.Is(err, errStaleTaskWrite) || errors.Is(err, errSchedulerLockLost) || errors.Is(err, errProducerInvalidated) {
		t.Fatalf("budget_limited finalize/sync must not be blocked by stale CAS: %v", err)
	}
	if err != nil {
		t.Fatalf("finalize/sync: %v", err)
	}
	fresh, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if fresh.Goal == nil {
		t.Fatal("missing goal binding")
	}
	if fresh.Goal.Observation == goalObsDone || fresh.Status == statusDone || fresh.Goal.EvidenceComplete {
		t.Fatalf("budget_limited must stay nonaccepted: status=%s obs=%s note=%s", fresh.Status, fresh.Goal.Observation, fresh.Goal.ObservationNote)
	}
	if fresh.Goal.LastNativeStatus != "budget_limited" && !strings.Contains(fresh.Goal.ObservationNote, "budget_limited") {
		t.Fatalf("sync must record native budget_limited: %+v", fresh.Goal)
	}
	if !fresh.Goal.CustodyReleased || fresh.ActiveAttemptID != "" {
		t.Fatalf("custody-released budget_limited must not resurrect ActiveAttemptID: custody=%v attempt=%q bound=%q",
			fresh.Goal.CustodyReleased, fresh.ActiveAttemptID, fresh.Goal.BoundAttemptID)
	}
	assertNoGoalEvDone(t, root, fresh)
}

func TestHostedPauseNotConfirmedOwnedPersist(t *testing.T) {
	root, cfg, _, tk := admitHostedGoalThenRelease(t)
	if err := persistHostedControlUnconfirmed(root, cfg, tk, goalControlStop); err != nil {
		t.Fatalf("owned pause_not_confirmed persist: %v", err)
	}
	if producerInvalidated(root, tk) || hasStaleWriteEvent(root, tk.ID, tk.ActiveAttemptID) {
		t.Fatal("owned failed-control persist must not invalidate a valid producer")
	}
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Goal == nil || fresh.Goal.FailureClass != goalFailPauseNotConfirmed {
		t.Fatalf("pause_not_confirmed must persist on the task: %+v", fresh.Goal)
	}
	if fresh.Goal.ControlConfirmed || fresh.Goal.CustodyReleased || fresh.Goal.Observation == goalObsDone || fresh.Status == statusDone {
		t.Fatalf("unconfirmed stop must not claim release/done: status=%s goal=%+v", fresh.Status, fresh.Goal)
	}
	if fresh.ActiveAttemptID == "" || !isActiveStatus(fresh.Status) {
		t.Fatalf("unconfirmed stop must keep the live writer: status=%s attempt=%q", fresh.Status, fresh.ActiveAttemptID)
	}
	st, serr := loadGoalHostStatus(root, fresh.ID, firstNonBlank(fresh.ActiveAttemptID, fresh.Goal.BoundAttemptID))
	if serr != nil {
		t.Fatalf("host-status: %v", serr)
	}
	if st.FailureClass != goalFailPauseNotConfirmed || st.ControlConfirmed {
		t.Fatalf("host-status must record unconfirmed stop: %+v", st)
	}
}
