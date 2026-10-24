package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGoalLaunchLeaseOnlyCapacity drives shipped goal-launch/goal-run
// admission from a real isolated start state. Occupancy is a verified exclusive
// workspace lease, live attempt/process, or recovered live projection — never a
// hardcoded slot integer.
func TestGoalLaunchLeaseOnlyCapacity(t *testing.T) {
	t.Parallel()

	t.Run("healthy-lease-only-max-parallel-1", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 1)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertCapacityReached(t, err, 1)
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	t.Run("broken-lease-only-max-parallel-1", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 1)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		truncateTaskJSON(t, fx.root, fx.occupant.ID)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertCapacityReached(t, err, 1)
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	t.Run("goal-run-healthy-lease-only-max-parallel-1", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 1)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		err := runGoalWorkflow(t, fx.root, fx.candidateWF.ID)
		assertCapacityReached(t, err, 1)
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	t.Run("goal-run-broken-lease-only-max-parallel-1", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 1)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		truncateTaskJSON(t, fx.root, fx.occupant.ID)
		err := runGoalWorkflow(t, fx.root, fx.candidateWF.ID)
		assertCapacityReached(t, err, 1)
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	t.Run("max-parallel-2-disjoint-reaches-fake-launch", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 2)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertFakeLaunchBoundary(t, err, fx.argv)
	})

	t.Run("overlap-still-refuses", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 2)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		fx.candidate.WriteDomain = copyWriteDomain(fx.occupantWF.WriteDomain)
		if err := saveTask(fx.root, fx.candidate); err != nil {
			t.Fatal(err)
		}
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		if err == nil {
			t.Fatal("overlapping write domain must refuse")
		}
		if !errors.Is(err, errAdmissionDenied) {
			t.Fatalf("overlap must be admission denied: %v", err)
		}
		if strings.Contains(err.Error(), "max_parallel") {
			t.Fatalf("overlap must refuse as conflict, got %v", err)
		}
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	t.Run("unlocked-lease-not-a-slot", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 1)
		plantUnlockedLeaseFile(t, fx.occupant.Dir)
		if workspaceLeaseHeld(fx.occupant.Dir) {
			t.Fatal("unlocked lease file must not be liveness")
		}
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertFakeLaunchBoundary(t, err, fx.argv)
	})

	t.Run("admitted-directory-not-a-slot", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 1)
		if workspaceLeaseHeld(fx.occupant.Dir) {
			t.Fatal("mere occupant directory must not be a slot")
		}
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertFakeLaunchBoundary(t, err, fx.argv)
	})

	t.Run("duplicate-projections-count-once", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 2)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		plantDuplicateLeaseProjections(t, fx.root, fx.occupant.ID, fx.occupant.Dir)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertFakeLaunchBoundary(t, err, fx.argv)
	})

	t.Run("lone-pending-not-blocked", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		cfg.MaxParallel = 1
		argv := filepath.Join(t.TempDir(), "argv-lone")
		enableManualGrok(t, root, cfg, argv)
		if cfg.GrokBuild != nil {
			cfg.GrokBuild.MaxParallel = 8
		}
		saveGoalCfg(t, root, cfg)
		wf := initNamedWorkflow(t, root, dir, "lone", "internal/auth")
		tk := admitManualWriter(t, root, cfg, wf)
		err := launchGoalTask(t, root, tk.ID)
		assertFakeLaunchBoundary(t, err, argv)
	})

	t.Run("stale-pid-not-a-slot", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 1)
		plantStaleAttemptPID(t, fx.root, fx.occupant.ID, fx.occupant.Dir, 424242)
		truncateTaskJSON(t, fx.root, fx.occupant.ID)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertFakeLaunchBoundary(t, err, fx.argv)
	})

	t.Run("healthy-stale-pid-not-a-slot", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 1)
		plantStaleAttemptPID(t, fx.root, fx.occupant.ID, fx.occupant.Dir, 434343)
		fx.occupant.ActiveAttemptID = ""
		if err := saveTask(fx.root, fx.occupant); err != nil {
			t.Fatal(err)
		}
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertFakeLaunchBoundary(t, err, fx.argv)
	})

	t.Run("live-attempt-is-a-slot", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("workspace flock is POSIX")
		}
		fx := newLeaseCapacityFixture(t, 1)
		plantLiveAttempt(t, fx.root, fx.occupant)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertCapacityReached(t, err, 1)
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	t.Run("live-process-without-lease-is-a-slot", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX live pid")
		}
		fx := newLeaseCapacityFixture(t, 1)
		pid, cleanup := spawnLiveForeignPID(t)
		t.Cleanup(cleanup)
		registerTaskInvoke(fx.occupant.ID, pid)
		t.Cleanup(func() { unregisterTaskInvoke(fx.occupant.ID, pid) })
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertCapacityReached(t, err, 1)
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	t.Run("unrecoverable-malformed-refused", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 2)
		if err := os.WriteFile(taskPath(fx.root, "orphan-broken"), []byte(`{"status":"running",`), 0o600); err != nil {
			t.Fatal(err)
		}
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		if err == nil {
			t.Fatal("unknown unreadable ownership must refuse")
		}
		if !errors.Is(err, errAdmissionDenied) {
			t.Fatalf("unrecoverable must be admission denied: %v", err)
		}
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	t.Run("foreign-historical-attempt-does-not-merge-slots", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX live pid and workspace flock")
		}
		fx := newLeaseCapacityFixture(t, 2)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		otherDir := extraWorkflowDir(t)
		otherWF := initNamedWorkflow(t, fx.root, otherDir, "otherproc", "internal/other")
		other := admitManualWriter(t, fx.root, fx.cfg, otherWF)
		pid, cleanup := spawnLiveForeignPID(t)
		t.Cleanup(cleanup)
		registerTaskInvoke(other.ID, pid)
		t.Cleanup(func() { unregisterTaskInvoke(other.ID, pid) })
		plantStaleAttemptPID(t, fx.root, other.ID, fx.occupant.Dir, 616161)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertCapacityReached(t, err, 2)
		assertFakeGrokNotInvoked(t, fx.argv)
	})

	// Case (a): truncated B, independently live child, distinct recoverable
	// workspace B, dead historical attempt naming locked lease A.
	t.Run("truncated-b-dead-history-does-not-merge-slots", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 2)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		other := plantIndependentLiveWriter(t, fx, "othertrunc")
		wantDir := other.Dir
		plantStaleAttemptPID(t, fx.root, other.ID, fx.occupant.Dir, 616162)
		truncateTaskJSON(t, fx.root, other.ID)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertCapacityReached(t, err, 2)
		assertFakeGrokNotInvoked(t, fx.argv)
		assertRecoveredWriterScope(t, fx.root, other.ID, wantDir, fx.occupant.Dir)
	})

	t.Run("goal-run-truncated-b-dead-history-does-not-merge-slots", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 2)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		other := plantIndependentLiveWriter(t, fx, "othertrunr")
		wantDir := other.Dir
		plantStaleAttemptPID(t, fx.root, other.ID, fx.occupant.Dir, 616163)
		truncateTaskJSON(t, fx.root, other.ID)
		err := runGoalWorkflow(t, fx.root, fx.candidateWF.ID)
		assertCapacityReached(t, err, 2)
		assertFakeGrokNotInvoked(t, fx.argv)
		assertRecoveredWriterScope(t, fx.root, other.ID, wantDir, fx.occupant.Dir)
	})

	// Case (b): healthy B, independently live child, ActiveAttemptID points at
	// a dead record whose WorkspaceLeaseID is locked lease A.
	t.Run("healthy-b-dead-active-attempt-does-not-merge-slots", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 2)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		other := plantIndependentLiveWriter(t, fx, "otherdead")
		wantDir := other.Dir
		plantDeadActiveAttemptNamingLease(t, fx.root, other, fx.occupant.Dir)
		err := launchGoalTask(t, fx.root, fx.candidate.ID)
		assertCapacityReached(t, err, 2)
		assertFakeGrokNotInvoked(t, fx.argv)
		assertHealthyWriterScope(t, fx.root, other.ID, wantDir, fx.occupant.Dir)
	})

	t.Run("goal-run-healthy-b-dead-active-attempt-does-not-merge-slots", func(t *testing.T) {
		fx := newLeaseCapacityFixture(t, 2)
		holdExclusiveWorkspaceLease(t, fx.occupant.Dir)
		other := plantIndependentLiveWriter(t, fx, "otherdeadr")
		wantDir := other.Dir
		plantDeadActiveAttemptNamingLease(t, fx.root, other, fx.occupant.Dir)
		err := runGoalWorkflow(t, fx.root, fx.candidateWF.ID)
		assertCapacityReached(t, err, 2)
		assertFakeGrokNotInvoked(t, fx.argv)
		assertHealthyWriterScope(t, fx.root, other.ID, wantDir, fx.occupant.Dir)
	})
}

type leaseCapacityFixture struct {
	root         string
	cfg          *Config
	argv         string
	occupantWF   *WorkflowRecord
	candidateWF  *WorkflowRecord
	occupant     *Task
	candidate    *Task
}

func newLeaseCapacityFixture(t *testing.T, maxParallel int) *leaseCapacityFixture {
	t.Helper()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	cfg.MaxParallel = maxParallel
	argv := filepath.Join(t.TempDir(), "argv-lease-cap")
	enableManualGrok(t, root, cfg, argv)
	if cfg.GrokBuild != nil {
		cfg.GrokBuild.MaxParallel = 8
	}
	saveGoalCfg(t, root, cfg)
	occWF := initNamedWorkflow(t, root, dir, "existing", "internal/auth")
	candDir := extraWorkflowDir(t)
	candWF := initNamedWorkflow(t, root, candDir, "candidate", "internal/billing")
	occ := admitManualWriter(t, root, cfg, occWF)
	cand := admitManualWriter(t, root, cfg, candWF)
	occWF, err := loadWorkflow(root, cfg, occWF.ID)
	if err != nil {
		t.Fatal(err)
	}
	candWF, err = loadWorkflow(root, cfg, candWF.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &leaseCapacityFixture{
		root:        root,
		cfg:         cfg,
		argv:        argv,
		occupantWF:  occWF,
		candidateWF: candWF,
		occupant:    occ,
		candidate:   cand,
	}
}

func launchGoalTask(t *testing.T, root, taskID string) error {
	t.Helper()
	headlessGoalStdin(t)
	return cmdWorkflowGoalLaunch([]string{"-root", root, "-task", taskID, "-manual"})
}

func runGoalWorkflow(t *testing.T, root, wfID string) error {
	t.Helper()
	headlessGoalStdin(t)
	return cmdWorkflowGoalRun([]string{"-root", root, wfID, "-manual"})
}

func assertCapacityReached(t *testing.T, err error, maxPar int) {
	t.Helper()
	if err == nil {
		t.Fatal("expected max_parallel capacity refusal")
	}
	if !errors.Is(err, errAdmissionDenied) {
		t.Fatalf("want admission denied, got %v", err)
	}
	want := fmt.Sprintf("max_parallel %d reached", maxPar)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q in %v", want, err)
	}
	if strings.Contains(err.Error(), "pty_missing") {
		t.Fatalf("capacity refusal reached pty_missing: %v", err)
	}
}

func assertFakeLaunchBoundary(t *testing.T, err error, argvPath string) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected existing fake-launch boundary, got %v", err)
	}
	assertFakeGrokInvoked(t, argvPath)
}

func assertFakeGrokInvoked(t *testing.T, argvPath string) {
	t.Helper()
	if _, err := os.Stat(argvPath); err != nil {
		t.Fatalf("fake grok was not invoked: %v", err)
	}
}

func assertFakeGrokNotInvoked(t *testing.T, argvPath string) {
	t.Helper()
	if _, err := os.Stat(argvPath); !os.IsNotExist(err) {
		t.Fatal("fake grok launched before admission refusal")
	}
}

func holdExclusiveWorkspaceLease(t *testing.T, dir string) *os.File {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	f, err := acquireWorkspaceExecutionLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f != nil {
			_ = f.Close()
		}
	})
	if !workspaceLeaseHeld(dir) {
		t.Fatal("exclusive kernel workspace lease was not visible to occupancy")
	}
	return f
}

func plantUnlockedLeaseFile(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	f, err := acquireWorkspaceExecutionLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	path, err := workspaceExecutionLeasePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("released lockfile missing: %v", err)
	}
}

func truncateTaskJSON(t *testing.T, root, id string) {
	t.Helper()
	raw := []byte(`{"id":` + strconv.Quote(id) + `,"status":`)
	if err := os.WriteFile(taskPath(root, id), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func plantStaleAttemptPID(t *testing.T, root, taskID, workspace string, pid int) {
	t.Helper()
	rec := &AttemptRecord{
		TaskID:           taskID,
		AttemptID:        newAttemptID(),
		State:            attemptBound,
		PID:              pid,
		WorkspaceLeaseID: workspace,
		CreatedAt:        time.Now().Format(time.RFC3339Nano),
		UpdatedAt:        time.Now().Format(time.RFC3339Nano),
	}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
}

func plantDuplicateLeaseProjections(t *testing.T, root, occupantID, workspace string) {
	t.Helper()
	dupID := "dup-" + occupantID
	truncateTaskJSON(t, root, dupID)
	plantStaleAttemptPID(t, root, dupID, workspace, 515151)
	hist := &AttemptRecord{
		TaskID:           occupantID,
		AttemptID:        newAttemptID(),
		State:            attemptExited,
		PID:              525252,
		WorkspaceLeaseID: workspace,
		CreatedAt:        time.Now().Format(time.RFC3339Nano),
		UpdatedAt:        time.Now().Format(time.RFC3339Nano),
	}
	if err := writeAttempt(root, hist); err != nil {
		t.Fatal(err)
	}
}

func plantIndependentLiveWriter(t *testing.T, fx *leaseCapacityFixture, module string) *Task {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX live pid and workspace flock")
	}
	otherDir := extraWorkflowDir(t)
	otherWF := initNamedWorkflow(t, fx.root, otherDir, module, "internal/other")
	other := admitManualWriter(t, fx.root, fx.cfg, otherWF)
	pid, cleanup := spawnLiveForeignPID(t)
	t.Cleanup(cleanup)
	registerTaskInvoke(other.ID, pid)
	t.Cleanup(func() { unregisterTaskInvoke(other.ID, pid) })
	if strings.TrimSpace(other.Dir) == "" {
		t.Fatal("independent writer must keep a current workspace")
	}
	if canonicalWorkspaceID(other.Dir) == canonicalWorkspaceID(fx.occupant.Dir) {
		t.Fatal("independent writer workspace must stay distinct from lease A")
	}
	if workspaceLeaseHeld(other.Dir) {
		t.Fatal("case requires B live via process, not via B's own kernel lease")
	}
	return other
}

func plantDeadActiveAttemptNamingLease(t *testing.T, root string, occupant *Task, foreignLease string) {
	t.Helper()
	attemptID := newAttemptID()
	rec := &AttemptRecord{
		TaskID:           occupant.ID,
		AttemptID:        attemptID,
		State:            attemptBound,
		PID:              626262,
		WorkspaceLeaseID: foreignLease,
		CreatedAt:        time.Now().Format(time.RFC3339Nano),
		UpdatedAt:        time.Now().Format(time.RFC3339Nano),
	}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	occupant.ActiveAttemptID = attemptID
	if err := saveTask(root, occupant); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, occupant.ID)
	if err != nil {
		t.Fatal(err)
	}
	*occupant = *loaded
	live, got := liveAttempt(root, occupant)
	if live || attemptProducerAlive(got) {
		t.Fatal("planted ActiveAttempt must be dead")
	}
	if got == nil || strings.TrimSpace(got.WorkspaceLeaseID) == "" {
		t.Fatal("dead ActiveAttempt must still load with a lease id")
	}
}

func assertRecoveredWriterScope(t *testing.T, root, taskID, wantDir, occupantDir string) {
	t.Helper()
	claim := recoverBrokenTaskWriterClaim(root, taskID, brokenTaskWorkflowWriters(root))
	if claim == nil {
		t.Fatal("expected recoverable writer scope for truncated B")
	}
	got := canonicalWorkspaceID(claim.Dir)
	want := canonicalWorkspaceID(wantDir)
	if got == "" || got != want {
		t.Fatalf("recovered scope %q want %q", claim.Dir, wantDir)
	}
	if occ := canonicalWorkspaceID(occupantDir); occ != "" && got == occ {
		t.Fatalf("recovered scope overwritten by occupant lease %q", occupantDir)
	}
}

func assertHealthyWriterScope(t *testing.T, root, taskID, wantDir, occupantDir string) {
	t.Helper()
	loaded, err := loadTask(root, taskID)
	if err != nil {
		t.Fatal(err)
	}
	got := canonicalWorkspaceID(loaded.Dir)
	want := canonicalWorkspaceID(wantDir)
	if got == "" || got != want {
		t.Fatalf("healthy B dir %q want %q", loaded.Dir, wantDir)
	}
	if occ := canonicalWorkspaceID(occupantDir); occ != "" && got == occ {
		t.Fatalf("healthy B dir overwritten by occupant lease %q", occupantDir)
	}
	assertRecoveredWriterScope(t, root, taskID, wantDir, occupantDir)
}

func plantLiveAttempt(t *testing.T, root string, occupant *Task) {
	t.Helper()
	holdExclusiveWorkspaceLease(t, occupant.Dir)
	pid, cleanup := spawnLiveForeignPID(t)
	t.Cleanup(cleanup)
	ident, ok := processStartIdentity(pid)
	if !ok || ident == "" {
		t.Fatal("live process start identity missing")
	}
	attemptID := newAttemptID()
	rec := &AttemptRecord{
		TaskID:           occupant.ID,
		AttemptID:        attemptID,
		ExpectedRevision: occupant.Revision,
		ControlEpoch:     occupant.ControlEpoch,
		State:            attemptBound,
		PID:              pid,
		PGID:             attemptProcessPGID(pid),
		StartIdentity:    ident,
		WorkspaceLeaseID: canonicalWorkspaceID(occupant.Dir),
		CreatedAt:        time.Now().Format(time.RFC3339Nano),
		UpdatedAt:        time.Now().Format(time.RFC3339Nano),
	}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	occupant.ActiveAttemptID = attemptID
	if err := saveTask(root, occupant); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, occupant.ID)
	if err != nil {
		t.Fatal(err)
	}
	*occupant = *loaded
	live, got := liveAttempt(root, occupant)
	if !live && !attemptProducerAlive(got) && !taskHasLiveWriterProof(root, occupant) {
		t.Fatal("planted live attempt/lease was not visible")
	}
}
