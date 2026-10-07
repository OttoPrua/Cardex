//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeHistoricalV1(t *testing.T, root, id, status, repo, worktree string, domain WriteDomain) {
	t.Helper()
	if err := os.MkdirAll(workflowsDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	wf := WorkflowRecord{
		Schema:           workflowSchemaV1,
		ID:               id,
		Mode:             workflowModeSerial,
		ModuleID:         "historical-mod",
		GoalID:           "historical-goal",
		Goal:             "historical vanished worktree",
		Repo:             repo,
		Worktree:         worktree,
		WriteDomain:      domain,
		TerminalCriteria: "terminal",
		MaxRounds:        2,
		WriterEngine:     grokBuildRunnerName,
		ReviewerEngine:   grokBuildRunnerName,
		EffectGates: WorkflowEffectGates{
			Integration: effectGateHeld,
			Live:        effectGateHeld,
			Cutover:     effectGateHeld,
		},
		Status: status,
	}
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath(root, id), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalWorkflowDisappearedWorktreeLoad(t *testing.T) {
	t.Parallel()
	root, live := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	missing := filepath.Join(t.TempDir(), "never-existed-worktree")
	writingID := "wf0915-1218-ea8f05"
	exhaustedID := "wf0914-0254-a336d2"
	writingDomain := WriteDomain{
		ID: "desktop-calendar-repair-20260915", Lineage: "desktop-calendar-repair-20260915-lineage",
		Component: "desktop-calendar", Paths: []string{"src/calendar.go"},
		Resources: []ResourceClaim{{Kind: resourceDatabase, ID: "calendar.primary"}},
	}
	exhaustedDomain := WriteDomain{
		ID: "m-r7-t1-557d82f-20260914", Lineage: "m-r7-t1-557d82f-20260914-lineage",
		Component: "mobile", Paths: []string{"app/src/main.go"},
	}
	writeHistoricalV1(t, root, writingID, workflowStatusWriting, missing, missing, writingDomain)
	writeHistoricalV1(t, root, exhaustedID, workflowStatusExhausted, missing, missing, exhaustedDomain)

	writing, err := loadWorkflow(root, cfg, writingID)
	if err != nil {
		t.Fatalf("valid writing v1 with a disappeared worktree must still load: %v", err)
	}
	if writing.Status != workflowStatusWriting || !workflowClaimsActive(writing) {
		t.Fatalf("writing vanished record treated as terminal: status=%s active=%t", writing.Status, workflowClaimsActive(writing))
	}
	exhausted, err := loadWorkflow(root, cfg, exhaustedID)
	if err != nil {
		t.Fatalf("valid exhausted v1 with a disappeared worktree must still load: %v", err)
	}
	if exhausted.Status != workflowStatusExhausted || workflowClaimsActive(exhausted) {
		t.Fatalf("exhausted vanished record still claiming: status=%s active=%t", exhausted.Status, workflowClaimsActive(exhausted))
	}

	unknown := cmdWorkflowInit([]string{
		"-root", root, "-module", "billing", "-goal-id", "billing-v1", "-goal", "bill",
		"-dir", live, "-terminal-criteria", "c", "-engine", grokBuildRunnerName,
		"-write-domain-id", "billing-core", "-write-domain-lineage", "billing-core-lineage",
		"-write-domain-component", "billing", "-write-paths", "internal/billing",
	})
	if !errors.Is(unknown, errWorkflowWriteOverlap) || !strings.Contains(unknown.Error(), writingID) || !strings.Contains(unknown.Error(), "repository identity cannot be proven") {
		t.Fatalf("unproven writing custody must block new admission: %v", unknown)
	}

	exRoot, exLive := workflowTestRoot(t)
	exMissing := filepath.Join(t.TempDir(), "exhausted-gone")
	writeHistoricalV1(t, exRoot, exhaustedID, workflowStatusExhausted, exMissing, exMissing, exhaustedDomain)
	if err := cmdWorkflowInit([]string{
		"-root", exRoot, "-module", "billing", "-goal-id", "billing-v1", "-goal", "bill",
		"-dir", exLive, "-terminal-criteria", "c", "-engine", grokBuildRunnerName,
		"-write-domain-id", "billing-core", "-write-domain-lineage", "billing-core-lineage",
		"-write-domain-component", "billing", "-write-paths", "internal/billing",
	}); err != nil {
		t.Fatalf("exhausted vanished history must not stay active: %v", err)
	}

	escapeRoot := testRoot(t)
	if err := saveConfig(escapeRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	writeHistoricalV1(t, escapeRoot, "wf0915-1218-escape", workflowStatusWriting, missing, missing, WriteDomain{
		ID: "escape-claim", Lineage: "escape-claim-lineage", Component: "desktop-calendar",
		Paths: []string{"../secret"},
	})
	if _, err := loadWorkflow(escapeRoot, workflowTestCfg(t, escapeRoot), "wf0915-1218-escape"); !errors.Is(err, errWriteDomainTraversal) {
		t.Fatalf("historical load must still reject traversal: %v", err)
	}
}

func TestHistoricalOwnerChoiceOnTempCopies(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	cfg := workflowTestCfg(t, root)
	missing := filepath.Join(t.TempDir(), "never-existed-worktree")
	writingID := "wf0101-hist-write1"
	exhaustedID := "wf0101-hist-exh1"
	writingDomain := WriteDomain{
		ID: "hist-write-core", Lineage: "hist-write-core-lineage", Component: "desktop-calendar",
		Paths: []string{"src/calendar.go"},
	}
	exhaustedDomain := WriteDomain{
		ID: "hist-exh-core", Lineage: "hist-exh-core-lineage", Component: "mobile",
		Paths: []string{"app/src/main.go"},
	}
	writeHistoricalV1(t, root, writingID, workflowStatusWriting, missing, missing, writingDomain)
	writeHistoricalV1(t, root, exhaustedID, workflowStatusExhausted, missing, missing, exhaustedDomain)
	released := seedVanishedHeldReleasedWriter(t, root, writingID, missing)
	setHistoricalWriterTaskID(t, root, writingID, released.ID)
	setHistoricalWriterTaskID(t, root, exhaustedID, "t0101-hist-absent")

	for _, id := range []string{writingID, exhaustedID} {
		for pass := 1; pass <= 2; pass++ {
			if err := cmdWorkflowMark([]string{"-root", root, id, "-kind", "owner", "-summary", "synthetic owner recovery"}); err != nil {
				t.Fatalf("pass %d workflow mark -kind owner on %s: %v", pass, id, err)
			}
			marked, err := loadWorkflow(root, cfg, id)
			if err != nil {
				t.Fatal(err)
			}
			if marked.Status != workflowStatusOwnerChoice || workflowClaimsActive(marked) {
				t.Fatalf("pass %d %s status=%s active=%t", pass, id, marked.Status, workflowClaimsActive(marked))
			}
			if _, ok := provenWorkflowRepoRoot(marked); ok {
				t.Fatalf("%s persist invented a proven repo root", id)
			}
		}
	}

	unknownRoot := testRoot(t)
	if err := saveConfig(unknownRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	unknownID := "wf0101-hist-unk1"
	writeHistoricalV1(t, unknownRoot, unknownID, workflowStatusWriting, missing, missing, writingDomain)
	unknownTask := seedVanishedHeldUnknownWriter(t, unknownRoot, unknownID, missing)
	for pass := 1; pass <= 2; pass++ {
		if err := cmdWorkflowMark([]string{"-root", unknownRoot, unknownID, "-kind", "owner", "-summary", "unknown custody"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("pass %d unresolved unknown custody must reject owner mark: %v", pass, err)
		}
	}
	unkWF, err := loadWorkflow(unknownRoot, workflowTestCfg(t, unknownRoot), unknownID)
	if err != nil {
		t.Fatal(err)
	}
	if unkWF.Status != workflowStatusWriting || !workflowClaimsActive(unkWF) {
		t.Fatalf("unknown custody mark released the claim: status=%s active=%t", unkWF.Status, workflowClaimsActive(unkWF))
	}
	afterUnknown, err := loadTask(unknownRoot, unknownTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterUnknown.Goal == nil || afterUnknown.Goal.CustodyReleased || afterUnknown.Goal.Observation != goalObsUnknown {
		t.Fatalf("unknown custody was released: %+v", afterUnknown.Goal)
	}

	missingWriterRoot := testRoot(t)
	if err := saveConfig(missingWriterRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	missingWriterID := "wf0101-hist-miss1"
	writeHistoricalV1(t, missingWriterRoot, missingWriterID, workflowStatusWriting, missing, missing, writingDomain)
	setHistoricalWriterTaskID(t, missingWriterRoot, missingWriterID, "t0101-hist-missing")
	for pass := 1; pass <= 2; pass++ {
		if err := cmdWorkflowMark([]string{"-root", missingWriterRoot, missingWriterID, "-kind", "owner", "-summary", "missing writer"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("pass %d missing referenced writer must reject owner mark: %v", pass, err)
		}
	}
	missingWF, err := loadWorkflow(missingWriterRoot, workflowTestCfg(t, missingWriterRoot), missingWriterID)
	if err != nil {
		t.Fatal(err)
	}
	if missingWF.Status != workflowStatusWriting || !workflowClaimsActive(missingWF) {
		t.Fatalf("missing referenced writer released the claim: status=%s", missingWF.Status)
	}

	aloneRoot := testRoot(t)
	if err := saveConfig(aloneRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	aloneID := "wf0101-hist-alone"
	writeHistoricalV1(t, aloneRoot, aloneID, workflowStatusWriting, missing, missing, writingDomain)
	for pass := 1; pass <= 2; pass++ {
		if err := cmdWorkflowMark([]string{"-root", aloneRoot, aloneID, "-kind", "owner", "-summary", "missing root alone"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("pass %d missing root alone must stay held: %v", pass, err)
		}
	}
	alone, err := loadWorkflow(aloneRoot, workflowTestCfg(t, aloneRoot), aloneID)
	if err != nil {
		t.Fatal(err)
	}
	if alone.Status != workflowStatusWriting || !workflowClaimsActive(alone) {
		t.Fatalf("missing root alone released the claim: status=%s", alone.Status)
	}

	liveRoot, liveDir := workflowTestRoot(t)
	liveWF := initTestWorkflow(t, liveRoot, liveDir)
	running := admitManualWriter(t, liveRoot, workflowTestCfg(t, liveRoot), liveWF)
	running.Status = statusRunning
	if err := writeTaskFile(liveRoot, running); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowMark([]string{"-root", liveRoot, liveWF.ID, "-kind", "owner", "-summary", "running"}); err == nil || !errors.Is(err, errWorkflowDuplicateRole) {
		t.Fatalf("running dispatchable writer must block owner mark: %v", err)
	}

	heldLiveRoot, heldLiveDir := workflowTestRoot(t)
	heldLiveWF := initTestWorkflow(t, heldLiveRoot, heldLiveDir)
	heldLive := admitManualWriter(t, heldLiveRoot, workflowTestCfg(t, heldLiveRoot), heldLiveWF)
	liveCmd := bindLiveAttempt(t, heldLiveRoot, heldLive)
	defer func() { _ = liveCmd.Process.Kill(); _ = liveCmd.Wait() }()
	heldLive, err = loadTask(heldLiveRoot, heldLive.ID)
	if err != nil {
		t.Fatal(err)
	}
	heldLive.Status = statusHeld
	revokeScheduling(heldLive)
	if err := writeTaskFile(heldLiveRoot, heldLive); err != nil {
		t.Fatal(err)
	}
	if taskIsDispatchable(heldLive) {
		t.Fatal("held-but-live control must not be dispatchable")
	}
	if !taskHasLiveWriterProof(heldLiveRoot, heldLive) {
		t.Fatal("held-but-live control must keep live attempt proof")
	}
	if err := cmdWorkflowMark([]string{"-root", heldLiveRoot, heldLiveWF.ID, "-kind", "owner", "-summary", "held live"}); err == nil || !errors.Is(err, errWorkflowCustody) {
		t.Fatalf("held-but-live attempt must reject owner mark: %v", err)
	}
}

// Evidence: tlink-custody-recovery-20261004.json autonomous_window_recovery.
// Original directories are absent. Referenced writers are held/revoking with
// no active attempt and no lease. PID absence is not release authority.
// Copies only; these IDs are not production records.
func TestEvidenceHeldRevokingPidAbsenceDoesNotRelease(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-existed-worktree")
	domain := func(id, component, path string) WriteDomain {
		return WriteDomain{ID: id, Lineage: id + "-lineage", Component: component, Paths: []string{path}}
	}

	exhaustedRoot := testRoot(t)
	if err := saveConfig(exhaustedRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	writeHistoricalV1(t, exhaustedRoot, "wf0914-0254-a336d2", workflowStatusExhausted, missing, missing,
		domain("mobile-r7", "mobile", "app/src/main.go"))
	for pass := 1; pass <= 2; pass++ {
		if err := cmdWorkflowMark([]string{"-root", exhaustedRoot, "wf0914-0254-a336d2", "-kind", "owner", "-summary", "exhausted no writer"}); err != nil {
			t.Fatalf("pass %d exhausted record with no writer: %v", pass, err)
		}
		marked, err := loadWorkflow(exhaustedRoot, workflowTestCfg(t, exhaustedRoot), "wf0914-0254-a336d2")
		if err != nil {
			t.Fatal(err)
		}
		if marked.Status != workflowStatusOwnerChoice || workflowClaimsActive(marked) {
			t.Fatalf("pass %d exhausted no-writer status=%s active=%t", pass, marked.Status, workflowClaimsActive(marked))
		}
	}

	type heldCase struct {
		id     string
		status string
		taskID string
	}
	cases := []heldCase{
		{id: "wf0914-0306-3c09d6", status: workflowStatusExhausted, taskID: "t0914-0306-b689"},
		{id: "wf0915-1218-ea8f05", status: workflowStatusWriting, taskID: "t0915-2318-ae6c"},
		{id: "wf0915-2210-ec4731", status: workflowStatusWriting, taskID: "t0915-2318-dce5"},
	}
	for _, tc := range cases {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		writeHistoricalV1(t, root, tc.id, tc.status, missing, missing, domain(tc.id, "historical", "src/"+tc.taskID+".go"))
		task := seedHeldRevokingNoAttempt(t, root, tc.id, tc.taskID, missing)
		setHistoricalWriterTaskID(t, root, tc.id, task.ID)
		for pass := 1; pass <= 2; pass++ {
			err := cmdWorkflowMark([]string{"-root", root, tc.id, "-kind", "owner", "-summary", "pid absence is not release"})
			if err == nil || !errors.Is(err, errWorkflowCustody) || !strings.Contains(err.Error(), "pid absence is not release authority") {
				t.Fatalf("pass %d %s pid absence must not release: %v", pass, tc.id, err)
			}
			marked, loadErr := loadWorkflow(root, workflowTestCfg(t, root), tc.id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if marked.Status != tc.status {
				t.Fatalf("pass %d %s status changed to %s", pass, tc.id, marked.Status)
			}
			after, loadErr := loadTask(root, task.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if after.Status != statusHeld || after.effectiveControlState() != controlRevoking || after.ActiveAttemptID != "" {
				t.Fatalf("pass %d %s writer changed: status=%s control=%s attempt=%q", pass, tc.id, after.Status, after.effectiveControlState(), after.ActiveAttemptID)
			}
			if goalCustodyReleased(root, after) || recordedAttemptCloseout(root, after) {
				t.Fatalf("pass %d %s treated pid absence as closeout", pass, tc.id)
			}
		}
	}
}

func seedHeldRevokingNoAttempt(t *testing.T, root, wfID, taskID, missing string) *Task {
	t.Helper()
	cfg := workflowTestCfg(t, root)
	tk := newTask(root, cfg, typeSequence, "held revoking writer", missing, []string{"no attempt and no lease"}, 5)
	tk.ID = taskID
	tk.WorkflowID = wfID
	tk.Project = "historical-mod"
	tk.Status = statusHeld
	tk.PreferRunner = grokBuildRunnerName
	tk.ActiveAttemptID = ""
	tk.Goal = nil
	revokeScheduling(tk)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveAttemptID != "" || loaded.Status != statusHeld || loaded.effectiveControlState() != controlRevoking {
		t.Fatalf("seed %s lost held/revoking shape: status=%s control=%s attempt=%q", taskID, loaded.Status, loaded.effectiveControlState(), loaded.ActiveAttemptID)
	}
	if taskHasLiveWriterProof(root, loaded) {
		t.Fatalf("seed %s has live writer proof", taskID)
	}
	return loaded
}

func setHistoricalWriterTaskID(t *testing.T, root, wfID, taskID string) {
	t.Helper()
	path := workflowPath(root, wfID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["writer_task_id"] = taskID
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedVanishedHeldReleasedWriter(t *testing.T, root, wfID, missing string) *Task {
	t.Helper()
	cfg := workflowTestCfg(t, root)
	tk := newTask(root, cfg, typeSequence, "synthetic vanished writer", missing, []string{"synthetic held writer for vanished root"}, 5)
	tk.WorkflowID = wfID
	tk.Project = "historical-mod"
	tk.Status = statusHeld
	tk.PreferRunner = grokBuildRunnerName
	revokeScheduling(tk)
	tk.Goal = &TaskGoalBinding{
		WriterMode:      goalWriterManual,
		Observation:     goalObsUnknown,
		ObservationNote: "synthetic unknown observation with released custody",
		Started:         true,
		CustodyReleased: true,
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	bindRealExitedAttempt(t, root, tk)
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = statusHeld
	revokeScheduling(loaded)
	if loaded.Goal == nil {
		t.Fatal("goal binding dropped")
	}
	loaded.Goal.Observation = goalObsUnknown
	if err := writeTaskFile(root, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !goalCustodyReleased(root, loaded) {
		t.Fatal("synthetic released writer still has unresolved custody")
	}
	return loaded
}

func seedVanishedHeldUnknownWriter(t *testing.T, root, wfID, missing string) *Task {
	t.Helper()
	cfg := workflowTestCfg(t, root)
	tk := newTask(root, cfg, typeSequence, "synthetic unknown writer", missing, []string{"synthetic held writer with missing attempt evidence"}, 5)
	tk.WorkflowID = wfID
	tk.Project = "historical-mod"
	tk.Status = statusHeld
	tk.PreferRunner = grokBuildRunnerName
	revokeScheduling(tk)
	tk.Goal = &TaskGoalBinding{
		WriterMode:      goalWriterManual,
		Observation:     goalObsUnknown,
		Started:         true,
		BoundAttemptID:  "atmissing00000001",
		CustodyReleased: false,
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func bindRealExitedAttempt(t *testing.T, root string, tk *Task) {
	t.Helper()
	attemptID := newAttemptID()
	now := time.Now().Format(time.RFC3339Nano)
	rec := &AttemptRecord{
		TaskID:           tk.ID,
		AttemptID:        attemptID,
		ExpectedRevision: tk.Revision,
		ControlEpoch:     tk.ControlEpoch,
		State:            attemptReserved,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	tk.ActiveAttemptID = attemptID
	if tk.Goal != nil {
		tk.Goal.BoundAttemptID = attemptID
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := bindAttemptProcess(root, tk.ID, attemptID, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := closeAttemptRecord(root, tk.ID, attemptID, attemptExited); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	*tk = *loaded
	tk.ActiveAttemptID = ""
	if tk.Goal != nil {
		tk.Goal.BoundAttemptID = attemptID
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
}

func bindLiveAttempt(t *testing.T, root string, tk *Task) *exec.Cmd {
	t.Helper()
	attemptID := newAttemptID()
	now := time.Now().Format(time.RFC3339Nano)
	rec := &AttemptRecord{
		TaskID:           tk.ID,
		AttemptID:        attemptID,
		ExpectedRevision: tk.Revision,
		ControlEpoch:     tk.ControlEpoch,
		State:            attemptReserved,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	tk.ActiveAttemptID = attemptID
	if tk.Goal != nil {
		tk.Goal.BoundAttemptID = attemptID
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Dir = tk.Dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireWorkspaceExecutionLease(tk.Dir)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if lease != nil {
			_ = lease.Close()
		}
		unregisterTaskInvoke(tk.ID, cmd.Process.Pid)
	})
	if err := bindAttemptProcess(root, tk.ID, attemptID, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	registerTaskInvoke(tk.ID, cmd.Process.Pid)
	return cmd
}

// A held integration card minted for the DAG and never launched is not an
// unfinished run. Revoking, a started Goal, or a missing scheduling bit stays
// unresolved. The writer closeout does not stand in for the integration card.
func TestNeverStartedIntegrationPlaceholderDoesNotBlockOwnerMark(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-existed-worktree")
	domain := WriteDomain{
		ID: "hist-integ", Lineage: "hist-integ-lineage", Component: "historical",
		Paths: []string{"src/hold.go"},
	}

	exhaustedRoot := testRoot(t)
	if err := saveConfig(exhaustedRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	exhaustedID := "wf0101-hist-integ"
	writeHistoricalV1(t, exhaustedRoot, exhaustedID, workflowStatusExhausted, missing, missing, domain)
	only := seedNeverStartedIntegration(t, exhaustedRoot, exhaustedID, missing)
	setHistoricalIntegrationTaskID(t, exhaustedRoot, exhaustedID, only.ID)
	if workflowCardHasUnresolvedCustody(exhaustedRoot, only) {
		t.Fatal("never-started integration placeholder is unresolved")
	}
	for pass := 1; pass <= 2; pass++ {
		if err := cmdWorkflowMark([]string{"-root", exhaustedRoot, exhaustedID, "-kind", "owner", "-summary", "never-started integration"}); err != nil {
			t.Fatalf("pass %d never-started integration blocked owner mark: %v", pass, err)
		}
		marked, err := loadWorkflow(exhaustedRoot, workflowTestCfg(t, exhaustedRoot), exhaustedID)
		if err != nil {
			t.Fatal(err)
		}
		if marked.Status != workflowStatusOwnerChoice || marked.IntegrationTaskID != only.ID {
			t.Fatalf("pass %d status=%s integration=%s", pass, marked.Status, marked.IntegrationTaskID)
		}
		still, err := loadTask(exhaustedRoot, only.ID)
		if err != nil {
			t.Fatal(err)
		}
		if still.Status != statusHeld || still.ControlState != controlTerminal || still.Goal != nil {
			t.Fatalf("pass %d placeholder changed: status=%s control=%s", pass, still.Status, still.ControlState)
		}
	}

	pairedRoot := testRoot(t)
	if err := saveConfig(pairedRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	pairedID := "wf0101-hist-pair"
	writeHistoricalV1(t, pairedRoot, pairedID, workflowStatusWriting, missing, missing, domain)
	writer := seedVanishedHeldReleasedWriter(t, pairedRoot, pairedID, missing)
	setHistoricalWriterTaskID(t, pairedRoot, pairedID, writer.ID)
	paired := seedNeverStartedIntegration(t, pairedRoot, pairedID, missing)
	setHistoricalIntegrationTaskID(t, pairedRoot, pairedID, paired.ID)
	for pass := 1; pass <= 2; pass++ {
		if err := cmdWorkflowMark([]string{"-root", pairedRoot, pairedID, "-kind", "owner", "-summary", "writer closeout plus placeholder"}); err != nil {
			t.Fatalf("pass %d exited writer plus never-started integration: %v", pass, err)
		}
		marked, err := loadWorkflow(pairedRoot, workflowTestCfg(t, pairedRoot), pairedID)
		if err != nil {
			t.Fatal(err)
		}
		if marked.Status != workflowStatusOwnerChoice || marked.WriterTaskID != writer.ID || marked.IntegrationTaskID != paired.ID {
			t.Fatalf("pass %d dropped a referenced card: status=%s writer=%s integration=%s", pass, marked.Status, marked.WriterTaskID, marked.IntegrationTaskID)
		}
	}

	revokingRoot := testRoot(t)
	if err := saveConfig(revokingRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	revokingID := "wf0101-hist-revk"
	writeHistoricalV1(t, revokingRoot, revokingID, workflowStatusWriting, missing, missing, domain)
	revokingWriter := seedVanishedHeldReleasedWriter(t, revokingRoot, revokingID, missing)
	setHistoricalWriterTaskID(t, revokingRoot, revokingID, revokingWriter.ID)
	revokingCard := seedNeverStartedIntegration(t, revokingRoot, revokingID, missing)
	setHistoricalIntegrationTaskID(t, revokingRoot, revokingID, revokingCard.ID)
	revokeScheduling(revokingCard)
	if err := writeTaskFile(revokingRoot, revokingCard); err != nil {
		t.Fatal(err)
	}
	if neverStartedIntegrationPlaceholder(revokingRoot, revokingCard) {
		t.Fatal("revoking integration counted as never started")
	}
	if err := cmdWorkflowMark([]string{"-root", revokingRoot, revokingID, "-kind", "owner", "-summary", "revoking integration"}); err == nil || !errors.Is(err, errWorkflowCustody) || !strings.Contains(err.Error(), "pid absence is not release authority") {
		t.Fatalf("revoking integration must stay unresolved: %v", err)
	}
	revokingWF, err := loadWorkflow(revokingRoot, workflowTestCfg(t, revokingRoot), revokingID)
	if err != nil {
		t.Fatal(err)
	}
	if revokingWF.Status != workflowStatusWriting {
		t.Fatalf("revoking integration released the route: %s", revokingWF.Status)
	}

	startedRoot := testRoot(t)
	if err := saveConfig(startedRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	startedID := "wf0101-hist-start"
	writeHistoricalV1(t, startedRoot, startedID, workflowStatusWriting, missing, missing, domain)
	started := seedNeverStartedIntegration(t, startedRoot, startedID, missing)
	setHistoricalIntegrationTaskID(t, startedRoot, startedID, started.ID)
	started.Goal = &TaskGoalBinding{Started: true, BoundAttemptID: "atnever0000000001", CustodyReleased: false}
	started.ControlState = controlTerminal
	started.SchedulingEligible = boolPtr(false)
	if err := writeTaskFile(startedRoot, started); err != nil {
		t.Fatal(err)
	}
	if neverStartedIntegrationPlaceholder(startedRoot, started) {
		t.Fatal("started goal with no attempt file counted as never launched")
	}
	if err := cmdWorkflowMark([]string{"-root", startedRoot, startedID, "-kind", "owner", "-summary", "started without closeout"}); err == nil || !errors.Is(err, errWorkflowCustody) {
		t.Fatalf("started integration without a closeout must stay unresolved: %v", err)
	}

	omittedRoot := testRoot(t)
	if err := saveConfig(omittedRoot, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	omittedID := "wf0101-hist-elig"
	writeHistoricalV1(t, omittedRoot, omittedID, workflowStatusWriting, missing, missing, domain)
	omitted := seedNeverStartedIntegration(t, omittedRoot, omittedID, missing)
	setHistoricalIntegrationTaskID(t, omittedRoot, omittedID, omitted.ID)
	omitted.SchedulingEligible = nil
	omitted.ControlState = controlTerminal
	if err := writeTaskFile(omittedRoot, omitted); err != nil {
		t.Fatal(err)
	}
	if neverStartedIntegrationPlaceholder(omittedRoot, omitted) {
		t.Fatal("omitted scheduling bit counted as explicitly not runnable")
	}
	if err := cmdWorkflowMark([]string{"-root", omittedRoot, omittedID, "-kind", "owner", "-summary", "scheduling bit omitted"}); err == nil || !errors.Is(err, errWorkflowCustody) {
		t.Fatalf("held integration without an explicit scheduling bit must stay unresolved: %v", err)
	}

	productRoot, productDir := workflowTestRoot(t)
	product := initTestWorkflow(t, productRoot, productDir)
	productCard, err := loadTask(productRoot, product.IntegrationTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !neverStartedIntegrationPlaceholder(productRoot, productCard) {
		t.Fatalf("product held integration is not the never-started shape: status=%s control=%s eligible=%v goal=%v attempt=%q",
			productCard.Status, productCard.ControlState, productCard.SchedulingEligible, productCard.Goal != nil, productCard.ActiveAttemptID)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := cmdWorkflowMark([]string{"-root", productRoot, product.ID, "-kind", "owner", "-summary", "product placeholder"}); err != nil {
			t.Fatalf("pass %d product integration placeholder: %v", pass, err)
		}
	}
	productAfter, err := loadWorkflow(productRoot, workflowTestCfg(t, productRoot), product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if productAfter.Status != workflowStatusOwnerChoice || productAfter.IntegrationTaskID != productCard.ID {
		t.Fatalf("product route status=%s integration=%s", productAfter.Status, productAfter.IntegrationTaskID)
	}
}

func seedNeverStartedIntegration(t *testing.T, root, wfID, dir string) *Task {
	t.Helper()
	cfg := workflowTestCfg(t, root)
	tk := newTask(root, cfg, typeSequence, "held integration", dir, []string{"minted held, never launched"}, 5)
	tk.WorkflowID = wfID
	tk.Project = "historical-mod"
	tk.Status = statusHeld
	tk.ControlState = controlTerminal
	tk.SchedulingEligible = boolPtr(false)
	tk.ActiveAttemptID = ""
	tk.SessionID = ""
	tk.Goal = nil
	tk.IntegrationGate = &IntegrationGate{WorkflowID: wfID}
	tk.PreferRunner = grokBuildRunnerName
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !neverStartedIntegrationPlaceholder(root, loaded) {
		t.Fatalf("seed %s is not a never-started integration placeholder: status=%s control=%s", loaded.ID, loaded.Status, loaded.ControlState)
	}
	return loaded
}

func setHistoricalIntegrationTaskID(t *testing.T, root, wfID, taskID string) {
	t.Helper()
	path := workflowPath(root, wfID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["integration_task_id"] = taskID
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Evidence shape (temp copies only): canceled TLink writer archived at
// archive/<id>.json, bound original attempt exited, no live producer/lease.
// workflow mark -kind owner must consume that identity; spoofs stay writing.
func TestArchivedCanceledWriterOwnerMarkIdentity(t *testing.T) {
	archiveDomain := WriteDomain{
		ID: "hist-archive-core", Lineage: "hist-archive-core-lineage", Component: "historical",
		Paths: []string{"src/archive.go"},
	}

	t.Run("valid canceled archive enables owner choice without done", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "never-existed-worktree")
		wfID := "wf0105-arch-ok"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, missing, missing, archiveDomain)
		writer := seedCanceledArchivedWriter(t, root, wfID, missing)
		setHistoricalWriterTaskID(t, root, wfID, writer.ID)
		if _, err := loadTask(root, writer.ID); !os.IsNotExist(err) {
			t.Fatal("valid case must not keep a live task file")
		}
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "canceled archive closeout"}); err != nil {
			t.Fatalf("valid canceled archive owner mark: %v", err)
		}
		marked, err := loadWorkflow(root, workflowTestCfg(t, root), wfID)
		if err != nil {
			t.Fatal(err)
		}
		if marked.Status != workflowStatusOwnerChoice || workflowClaimsActive(marked) {
			t.Fatalf("status=%s active=%t", marked.Status, workflowClaimsActive(marked))
		}
		if marked.Status == workflowStatusWriting {
			t.Fatal("owner mark left the route writing")
		}
		archived, err := loadArchivedTaskFile(root, writer.ID)
		if err != nil {
			t.Fatal(err)
		}
		if archived.Status == statusDone || (archived.Goal != nil && archived.Goal.Observation == goalObsDone) {
			t.Fatalf("owner mark manufactured done: status=%s goal=%+v", archived.Status, archived.Goal)
		}
		if archived.Status != statusCanceled {
			t.Fatalf("archived writer status=%s", archived.Status)
		}
	})

	t.Run("missing archive stays fail-closed writing", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "never-existed-worktree")
		wfID := "wf0105-arch-miss"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, missing, missing, archiveDomain)
		setHistoricalWriterTaskID(t, root, wfID, "t0105-arch-missing")
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "missing"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("missing archive must reject: %v", err)
		}
		assertWritingClaimHeld(t, root, wfID)
	})

	t.Run("corrupt archive stays fail-closed writing", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "never-existed-worktree")
		wfID := "wf0105-arch-bad"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, missing, missing, archiveDomain)
		id := "t0105-arch-corrupt"
		setHistoricalWriterTaskID(t, root, wfID, id)
		if err := os.MkdirAll(archiveDir(root), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(archiveDir(root), id+".json"), []byte("{not-json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "corrupt"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("corrupt archive must reject: %v", err)
		}
		assertWritingClaimHeld(t, root, wfID)
	})

	t.Run("wrong workflow binding stays fail-closed writing", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "never-existed-worktree")
		wfID := "wf0105-arch-bind"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, missing, missing, archiveDomain)
		writer := seedCanceledArchivedWriter(t, root, "wf-other-binding", missing)
		setHistoricalWriterTaskID(t, root, wfID, writer.ID)
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "wrong binding"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("wrong binding must reject: %v", err)
		}
		assertWritingClaimHeld(t, root, wfID)
	})

	t.Run("still-live producer on archive stays fail-closed writing", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		wfID := "wf0105-arch-live"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, dir, dir, archiveDomain)
		writer := seedCanceledArchivedWriter(t, root, wfID, dir)
		setHistoricalWriterTaskID(t, root, wfID, writer.ID)
		lease, err := acquireWorkspaceExecutionLease(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if lease != nil {
				_ = lease.Close()
			}
		})
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "still live"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("still-live archive must reject: %v", err)
		}
		assertWritingClaimHeld(t, root, wfID)
	})

	t.Run("unresolved attempt stays fail-closed writing", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "never-existed-worktree")
		wfID := "wf0105-arch-unres"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, missing, missing, archiveDomain)
		cfg := workflowTestCfg(t, root)
		tk := newTask(root, cfg, typeSequence, "unresolved archive", missing, []string{"no closeout"}, 5)
		tk.WorkflowID = wfID
		tk.Status = statusCanceled
		markControlTerminal(tk)
		tk.Goal = &TaskGoalBinding{
			WriterMode:     goalWriterManual,
			Observation:    goalObsCanceled,
			Started:        true,
			BoundAttemptID: "atunresolved000001",
		}
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		if err := archiveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		setHistoricalWriterTaskID(t, root, wfID, tk.ID)
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "unresolved"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("unresolved archive attempt must reject: %v", err)
		}
		assertWritingClaimHeld(t, root, wfID)
	})

	t.Run("unreadable live does not fall back to archive", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "never-existed-worktree")
		wfID := "wf0105-arch-fb"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, missing, missing, archiveDomain)
		writer := seedCanceledArchivedWriter(t, root, wfID, missing)
		setHistoricalWriterTaskID(t, root, wfID, writer.ID)
		if err := os.MkdirAll(tasksDir(root), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(taskPath(root, writer.ID), []byte("{broken-live"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "live fallback"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("unreadable live must not fall back to archive: %v", err)
		}
		assertWritingClaimHeld(t, root, wfID)
	})

	t.Run("mismatched archive id stays fail-closed writing", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "never-existed-worktree")
		wfID := "wf0105-arch-id"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, missing, missing, archiveDomain)
		writer := seedCanceledArchivedWriter(t, root, wfID, missing)
		setHistoricalWriterTaskID(t, root, wfID, writer.ID)
		archived, err := loadArchivedTaskFile(root, writer.ID)
		if err != nil {
			t.Fatal(err)
		}
		archived.ID = "t0105-arch-other"
		raw, err := json.MarshalIndent(archived, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(archiveDir(root), writer.ID+".json"), append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "id mismatch"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("mismatched archive id must reject: %v", err)
		}
		assertWritingClaimHeld(t, root, wfID)
	})

	t.Run("live plus archive duplicates stay fail-closed writing", func(t *testing.T) {
		root := testRoot(t)
		if err := saveConfig(root, defaultConfig("claude")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "never-existed-worktree")
		wfID := "wf0105-arch-dup"
		writeHistoricalV1(t, root, wfID, workflowStatusWriting, missing, missing, archiveDomain)
		live := seedVanishedHeldReleasedWriter(t, root, wfID, missing)
		setHistoricalWriterTaskID(t, root, wfID, live.ID)
		dup := *live
		if err := writeArchivedTaskFile(root, &dup); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTask(root, live.ID); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowMark([]string{"-root", root, wfID, "-kind", "owner", "-summary", "duplicate"}); err == nil || !errors.Is(err, errWorkflowCustody) {
			t.Fatalf("live+archive duplicates must reject: %v", err)
		}
		assertWritingClaimHeld(t, root, wfID)
	})
}

func assertWritingClaimHeld(t *testing.T, root, wfID string) {
	t.Helper()
	marked, err := loadWorkflow(root, workflowTestCfg(t, root), wfID)
	if err != nil {
		t.Fatal(err)
	}
	if marked.Status != workflowStatusWriting || !workflowClaimsActive(marked) {
		t.Fatalf("claim released: status=%s active=%t", marked.Status, workflowClaimsActive(marked))
	}
}

func seedCanceledArchivedWriter(t *testing.T, root, wfID, dir string) *Task {
	t.Helper()
	tk := seedVanishedHeldReleasedWriter(t, root, wfID, dir)
	tk.Status = statusCanceled
	markControlTerminal(tk)
	if tk.Goal == nil {
		t.Fatal("released writer lost goal binding")
	}
	tk.Goal.Observation = goalObsCanceled
	tk.Goal.ObservationNote = "canceled with stop evidence"
	tk.Goal.CancelRequested = true
	tk.Goal.StopEvidence = true
	if err := writeTaskFile(root, tk); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !recordedAttemptCloseout(root, loaded) {
		t.Fatal("canceled archive seed missing original-attempt closeout")
	}
	if taskHasLiveWriterProof(root, loaded) {
		t.Fatal("canceled archive seed still has live writer proof")
	}
	if err := archiveTask(root, loaded); err != nil {
		t.Fatal(err)
	}
	archived, err := loadArchivedTaskFile(root, loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	return archived
}
