package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptInFiniteAdvanceTwoDirectionsOneAcceptance(t *testing.T) {
	root, _, cfg, wf, members := readyAcceptFixture(t)
	if len(members) != 2 {
		t.Fatalf("members=%d", len(members))
	}
	alpha, beta := members[0].ID, members[1].ID
	res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 3})
	if err != nil {
		t.Fatalf("first advance: %v", err)
	}
	if res.AcceptanceCreated != 1 || res.RepairCreated != 0 || res.StopReason != "acceptance-created" {
		t.Fatalf("first advance: %+v", res)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := wf.AcceptanceTaskID
	if first == "" {
		t.Fatal("missing acceptance")
	}
	res2, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 3})
	if err != nil {
		t.Fatalf("second advance: %v", err)
	}
	if res2.AcceptanceCreated != 0 || res2.RepairCreated != 0 {
		t.Fatalf("duplicate acceptance/writer: %+v", res2)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID != first {
		t.Fatalf("second acceptance %s after %s", wf.AcceptanceTaskID, first)
	}
	n := 0
	all, _ := loadTasks(root)
	for _, task := range all {
		if task != nil && strings.Contains(task.Title, "overall acceptance") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("acceptance cards=%d", n)
	}
	if wf.WriterTaskID != "" {
		t.Fatalf("pass/idempotent opened a writer: %s", wf.WriterTaskID)
	}
	acc, err := loadTask(root, first)
	if err != nil {
		t.Fatal(err)
	}
	acc.Status = statusDone
	if err := writeTaskFile(root, acc); err != nil {
		t.Fatal(err)
	}
	writeReviewLog(t, root, first, verdictJSON("pass", nil, nil))
	res3, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 3})
	if err != nil {
		t.Fatalf("passing advance: %v", err)
	}
	if !res3.GoalAccepted || res3.AcceptanceCreated != 0 {
		t.Fatalf("pass: %+v", res3)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wf.GoalCompleted || wf.AcceptanceTaskID != first {
		t.Fatalf("accepted wf=%+v", wf)
	}
	if _, err := loadTask(root, alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTask(root, beta); err != nil {
		t.Fatal(err)
	}
}

func markOverallAcceptancePass(t *testing.T, root string, wf *WorkflowRecord) {
	t.Helper()
	acc, err := loadTask(root, wf.AcceptanceTaskID)
	if err != nil {
		t.Fatal(err)
	}
	acc.Status = statusDone
	if err := writeTaskFile(root, acc); err != nil {
		t.Fatal(err)
	}
	writeReviewLog(t, root, acc.ID, verdictJSON("pass", nil, nil))
}

func TestOptInAdvanceStaleGoalCompletedAfterChange(t *testing.T) {
	t.Run("paused-member", func(t *testing.T) {
		root, _, cfg, wf, members := readyAcceptFixture(t)
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2})
		if err != nil {
			t.Fatal(err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		markOverallAcceptancePass(t, root, wf)
		res, err = advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2})
		if err != nil || !res.GoalAccepted {
			t.Fatalf("genuine pass: %+v %v", res, err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		if !wf.GoalCompleted {
			t.Fatal("expected stored GoalCompleted")
		}
		members[0].Status = statusHeld
		if err := writeTaskFile(root, members[0]); err != nil {
			t.Fatal(err)
		}
		res, err = advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2})
		if res.StopReason == "already_complete" || res.GoalAccepted {
			t.Fatalf("paused member counted as current completion: %+v %v", res, err)
		}
		if err == nil || !res.Held {
			t.Fatalf("paused after pass: %+v %v", res, err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		if wf.GoalCompleted {
			t.Fatal("stored GoalCompleted survived paused member")
		}
	})
	t.Run("artifact-changed", func(t *testing.T) {
		root, dir, cfg, wf, _ := readyAcceptFixture(t)
		if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2}); err != nil {
			t.Fatal(err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		oldAcc := wf.AcceptanceTaskID
		markOverallAcceptancePass(t, root, wf)
		if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2}); err != nil {
			t.Fatal(err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		if !wf.GoalCompleted {
			t.Fatal("expected stored GoalCompleted")
		}
		mustWriteFile(t, filepath.Join(dir, "internal", "auth", "extra.go"), "package auth\n")
		workflowGit(t, dir, "add", "-A")
		workflowGit(t, dir, "commit", "-q", "-m", "post-pass change")
		wf.Candidate = readLocalSourceCandidate(dir)
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2})
		if res.StopReason == "already_complete" || res.GoalAccepted {
			t.Fatalf("changed artifact counted as current completion: %+v %v", res, err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		if wf.GoalCompleted {
			t.Fatal("stored GoalCompleted survived artifact change")
		}
		if wf.AcceptanceTaskID == oldAcc && res.AcceptanceCreated == 0 {
			t.Fatalf("changed coverage reused the prior pass card: %+v acc=%s", res, wf.AcceptanceTaskID)
		}
	})
}

func TestCandidateBinStaleGoalCompletedAfterPass(t *testing.T) {
	bin := requireCandidateBin(t)
	root, dir, cfg, wf, members := readyAcceptFixture(t)
	run := func(args ...string) (string, error) {
		t.Helper()
		out, err := exec.Command(bin, args...).CombinedOutput()
		return string(out), err
	}
	if out, err := run("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "2"); err != nil {
		t.Fatalf("first: %v %s", err, out)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	markOverallAcceptancePass(t, root, wf)
	passOut, err := run("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "2")
	if err != nil || !strings.Contains(passOut, "goal_accepted=true") {
		t.Fatalf("pass CLI: %v %s", err, passOut)
	}
	members[0].Status = statusHeld
	if err := writeTaskFile(root, members[0]); err != nil {
		t.Fatal(err)
	}
	pausedOut, err := run("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "2")
	if strings.Contains(pausedOut, "stop=already_complete") || strings.Contains(pausedOut, "goal_accepted=true") {
		t.Fatalf("CLI paused member still complete: %v %s", err, pausedOut)
	}
	if err == nil {
		t.Fatalf("CLI paused member must not succeed: %s", pausedOut)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	if wf.GoalCompleted {
		t.Fatal("CLI stored GoalCompleted survived pause")
	}

	root2, dir2, cfg2, wf2, _ := readyAcceptFixture(t)
	if out, err := run("workflow", "advance", wf2.ID, "-root", root2, "-opt-in", "-max-actions", "2"); err != nil {
		t.Fatalf("artifact first: %v %s", err, out)
	}
	wf2, _ = loadWorkflow(root2, cfg2, wf2.ID)
	markOverallAcceptancePass(t, root2, wf2)
	if out, err := run("workflow", "advance", wf2.ID, "-root", root2, "-opt-in", "-max-actions", "2"); err != nil {
		t.Fatalf("artifact pass: %v %s", err, out)
	}
	mustWriteFile(t, filepath.Join(dir2, "internal", "auth", "extra.go"), "package auth\n")
	workflowGit(t, dir2, "add", "-A")
	workflowGit(t, dir2, "commit", "-q", "-m", "post-pass change")
	wf2, _ = loadWorkflow(root2, cfg2, wf2.ID)
	wf2.Candidate = readLocalSourceCandidate(dir2)
	if err := persistWorkflow(root2, cfg2, wf2); err != nil {
		t.Fatal(err)
	}
	changedOut, err := run("workflow", "advance", wf2.ID, "-root", root2, "-opt-in", "-max-actions", "2")
	if strings.Contains(changedOut, "stop=already_complete") || strings.Contains(changedOut, "goal_accepted=true") {
		t.Fatalf("CLI changed artifact still complete: %v %s", err, changedOut)
	}
	wf2, _ = loadWorkflow(root2, cfg2, wf2.ID)
	if wf2.GoalCompleted {
		t.Fatal("CLI stored GoalCompleted survived artifact change")
	}
	_ = dir
}

func TestOptInKnownDirectionRepairLeavesUnaffectedUnrerun(t *testing.T) {
	root, _, cfg, wf, members := readyDisjointAcceptFixture(t)
	alpha, beta := members[0], members[1]
	beta.Status = statusFailed
	if err := writeTaskFile(root, beta); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(t.TempDir(), "calls")
	cfg.ClaudeBin = countingClaudeBin(t, count, mkOKResultJSON("beta-repair"))
	cfg.StepTimeoutMin = 1
	cfg.DrainRescanSec = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	wf.WriterEngine = "claude"
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 3, RepairDirection: beta.SessionStage,
	})
	if err != nil {
		t.Fatalf("repair admit: %v %+v", err, res)
	}
	if res.RepairCreated != 1 || res.RepairTaskID == "" || res.RepairTaskID == beta.ID {
		t.Fatalf("repair: %+v old=%s", res, beta.ID)
	}
	if _, err := loadTask(root, beta.ID); err != nil {
		t.Fatal("repair must preserve the previous product task")
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.DirectionTaskIDs[0] != alpha.ID && wf.DirectionTaskIDs[1] != alpha.ID {
		t.Fatalf("unaffected direction rerun/dropped: %v", wf.DirectionTaskIDs)
	}
	if wf.DirectionTaskIDs[0] == beta.ID || wf.DirectionTaskIDs[1] == beta.ID {
		t.Fatalf("failed direction still current: %v", wf.DirectionTaskIDs)
	}
	before := providerCallCount(t, count)
	repairTk, err := loadTask(root, res.RepairTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(repairTk.DependsOn) > 0 {
		design, derr := loadTask(root, repairTk.DependsOn[0])
		if derr != nil {
			t.Fatal(derr)
		}
		if err := persistTaskEvent(root, design, evDone, "test:done", statusDone, design.Step, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := tickFilterOpts(root, cfg, false, true, res.RepairTaskID, true); err != nil {
		t.Fatalf("execute named repair: %v runner=%s", err, repairTk.PreferRunner)
	}
	if providerCallCount(t, count) != before+1 {
		t.Fatalf("fake exec count=%d before=%d", providerCallCount(t, count), before)
	}
	alphaAfter, err := loadTask(root, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if alphaAfter.Status != statusDone || alphaAfter.Attempts != alpha.Attempts {
		t.Fatalf("unaffected rerun: %+v", alphaAfter)
	}
	over, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 2, RepairDirection: alpha.SessionStage + "," + beta.SessionStage,
	})
	if err == nil {
		t.Fatalf("overbroad repair admitted: %+v", over)
	}
}

func TestOptInAdvanceHeldCasesAndNoOptIn(t *testing.T) {
	t.Run("no-opt-in", func(t *testing.T) {
		root, _, cfg, wf, _ := readyAcceptFixture(t)
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{MaxActions: 3})
		if err == nil || res.StopReason != "no-opt-in" || !res.Held {
			t.Fatalf("no-opt-in: %+v %v", res, err)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		if wf.AcceptanceTaskID != "" {
			t.Fatal("no-opt-in created acceptance")
		}
	})
	t.Run("max-rounds-is-not-opt-in", func(t *testing.T) {
		root, _, cfg, wf, _ := readyAcceptFixture(t)
		if wf.MaxRounds < 1 {
			t.Fatal("fixture max-rounds")
		}
		_, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{MaxActions: wf.MaxRounds})
		if err == nil {
			t.Fatal("inferred opt-in from max-rounds")
		}
	})
	t.Run("paused", func(t *testing.T) {
		root, _, cfg, wf, members := readyAcceptFixture(t)
		members[0].Status = statusHeld
		if err := writeTaskFile(root, members[0]); err != nil {
			t.Fatal(err)
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 3})
		if err == nil || !res.Held {
			t.Fatalf("paused: %+v %v", res, err)
		}
	})
	t.Run("unknown-member", func(t *testing.T) {
		root, _, cfg, wf, _ := readyAcceptFixture(t)
		wf.DirectionTaskIDs = append(wf.DirectionTaskIDs, "missing-direction")
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 3})
		if err == nil || !res.Held {
			t.Fatalf("unknown: %+v %v", res, err)
		}
	})
	t.Run("failed-without-named-repair", func(t *testing.T) {
		root, _, cfg, wf, members := readyAcceptFixture(t)
		members[1].Status = statusFailed
		if err := writeTaskFile(root, members[1]); err != nil {
			t.Fatal(err)
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 3})
		if err == nil || !res.Held {
			t.Fatalf("failed: %+v %v", res, err)
		}
	})
	t.Run("missing-artifact", func(t *testing.T) {
		root, _, cfg, wf, members := readyAcceptFixture(t)
		if err := os.Remove(taskPath(root, members[0].ID)); err != nil {
			t.Fatal(err)
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 3})
		if err == nil || !res.Held {
			t.Fatalf("missing: %+v %v", res, err)
		}
	})
	t.Run("stale-coverage", func(t *testing.T) {
		root, dir, cfg, wf, _ := readyAcceptFixture(t)
		if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2}); err != nil {
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
		writeReviewLog(t, root, acc.ID, verdictJSON("pass", nil, nil))
		mustWriteFile(t, filepath.Join(dir, "internal", "auth", "extra.go"), "package auth\n")
		workflowGit(t, dir, "add", "-A")
		workflowGit(t, dir, "commit", "-q", "-m", "stale")
		wf.Candidate = readLocalSourceCandidate(dir)
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2})
		if err != nil {
			t.Fatalf("stale coverage should create a new card, not pass old: %v", err)
		}
		if res.GoalAccepted {
			t.Fatalf("stale coverage accepted: %+v", res)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		if wf.GoalCompleted {
			t.Fatal("stale coverage marked goal complete")
		}
	})
	t.Run("custody-not-released", func(t *testing.T) {
		root, dir, cfg, wf, _ := readyAcceptFixture(t)
		tk := newTask(root, cfg, typeSequence, "native still producing", dir, []string{"g"}, 5)
		tk.WorkflowID = wf.ID
		tk.Status = statusDone
		tk.SessionID = "sess-open"
		tk.Goal = &TaskGoalBinding{
			WriterMode: goalWriterManual, NativeGoalID: "g-open", Observation: goalObsDone,
			EvidenceComplete: true, CustodyReleased: false,
		}
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		wf.NativeGoalTaskIDs = append(wf.NativeGoalTaskIDs, tk.ID)
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2})
		if err == nil || !res.Held {
			t.Fatalf("custody: %+v %v", res, err)
		}
	})
	t.Run("sealed-goal-no-successor", func(t *testing.T) {
		root, dir, cfg, wf, members := readyAcceptFixture(t)
		members[1].Status = statusFailed
		if err := writeTaskFile(root, members[1]); err != nil {
			t.Fatal(err)
		}
		tk := newTask(root, cfg, typeSequence, "native sealed", dir, []string{"g"}, 5)
		tk.WorkflowID = wf.ID
		tk.Status = statusFailed
		tk.SessionStage = members[1].SessionStage
		tk.Goal = &TaskGoalBinding{
			WriterMode: goalWriterManual, Scope: members[1].SessionStage,
			Observation: goalObsFailed, FailureClass: "budget_limited",
			ObservationNote: "native sealed producer", ExtraRoundAuthorized: true,
		}
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		wf.NativeGoalTaskIDs = []string{tk.ID}
		wf.DirectionTaskIDs = []string{members[0].ID, tk.ID}
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		writersBefore := wf.WriterTaskID
		res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
			OptIn: true, MaxActions: 2, RepairDirection: members[1].SessionStage,
		})
		if err == nil {
			t.Fatalf("sealed repair: %+v", res)
		}
		wf, _ = loadWorkflow(root, cfg, wf.ID)
		if wf.WriterTaskID != writersBefore {
			t.Fatalf("auto successor/writer: %s -> %s", writersBefore, wf.WriterTaskID)
		}
	})
	t.Run("needs-new-design", func(t *testing.T) {
		root, _, cfg, wf, members := readyAcceptFixture(t)
		members[1].Status = statusFailed
		if err := writeTaskFile(root, members[1]); err != nil {
			t.Fatal(err)
		}
		wf.DesignTaskID = ""
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		_, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
			OptIn: true, MaxActions: 2, RepairDirection: members[1].SessionStage,
		})
		if err == nil || !strings.Contains(err.Error(), "needs-new-design") {
			t.Fatalf("needs-new-design: %v", err)
		}
	})
}

func TestTickDoesNotAdvanceWorkflow(t *testing.T) {
	root, _, cfg, wf, _ := readyAcceptFixture(t)
	if err := tick(root, cfg, false, true); err != nil {
		t.Fatalf("tick: %v", err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID != "" || wf.GoalCompleted {
		t.Fatalf("tick advanced workflow: %+v", wf)
	}
}

func TestOrdinaryAddRemainsOneDirectCard(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := cmdAdd([]string{"-root", root, "-dir", dir, "-title", "simple add", "do one thing"}); err != nil {
		t.Fatal(err)
	}
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ordinary add cards=%d", len(tasks))
	}
	if tasks[0].Goal != nil {
		t.Fatal("ordinary add became a Goal/fanout card")
	}
}

func TestCandidateBinOptInAdvance(t *testing.T) {
	bin := requireCandidateBin(t)
	root, _, cfg, wf, _ := readyAcceptFixture(t)
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	first := run("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "3")
	if !strings.Contains(first, "acceptance_created=1") {
		t.Fatalf("first: %s", first)
	}
	again := run("workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "3")
	if !strings.Contains(again, "acceptance_created=0") {
		t.Fatalf("again: %s", again)
	}
	refuse, err := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-max-actions", "3").CombinedOutput()
	if err == nil || !strings.Contains(string(refuse), "opt-in") {
		t.Fatalf("no-opt-in CLI: %v %s", err, refuse)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	t.Logf("binary=%s argv=workflow advance acceptance=%s", bin, wf.AcceptanceTaskID)
}

func TestOptInReadyRepairDirectionDoesNotReplaceDoneMembers(t *testing.T) {
	root, _, cfg, wf, members := readyAcceptFixture(t)
	if len(members) != 2 {
		t.Fatalf("members=%d", len(members))
	}
	alpha, beta := members[0], members[1]
	res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
	})
	if err != nil {
		t.Fatalf("ready + repair-direction: %v %+v", err, res)
	}
	if res.AcceptanceCreated != 1 || res.RepairCreated != 0 || res.RepairTaskID != "" {
		t.Fatalf("must create one acceptance and no repair: %+v", res)
	}
	if res.ActionsUsed != 1 || res.StopReason != "acceptance-created" {
		t.Fatalf("cap-1 must stop after acceptance: %+v", res)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.DirectionTaskIDs[0] != alpha.ID || wf.DirectionTaskIDs[1] != beta.ID {
		t.Fatalf("member IDs changed: %v want %s,%s", wf.DirectionTaskIDs, alpha.ID, beta.ID)
	}
	again, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 2, RepairDirection: beta.SessionStage,
	})
	if err != nil {
		t.Fatalf("queued review + repair-direction: %v %+v", err, again)
	}
	if again.AcceptanceCreated != 0 || again.RepairCreated != 0 {
		t.Fatalf("queued review must not admit repair: %+v", again)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	if wf.DirectionTaskIDs[0] != alpha.ID || wf.DirectionTaskIDs[1] != beta.ID {
		t.Fatalf("second call changed members: %v", wf.DirectionTaskIDs)
	}
}

func TestOptInRepairFromCompletedNonPassFindings(t *testing.T) {
	root, _, cfg, wf, members := readyDisjointAcceptFixture(t)
	alpha, beta := members[0], members[1]
	beta.PermissionMode = "acceptEdits"
	beta.SkipPermissions = false
	beta.AllowedTools = []string{"read", "edit"}
	beta.MaxAttempts = 2
	beta.Effort = "high"
	beta.EffortExplicit = true
	if err := writeTaskFile(root, beta); err != nil {
		t.Fatal(err)
	}
	if _, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 1}); err != nil {
		t.Fatal(err)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	finding := "beta billing coverage hole"
	acc, err := loadTask(root, wf.AcceptanceTaskID)
	if err != nil {
		t.Fatal(err)
	}
	acc.Status = statusDone
	if err := writeTaskFile(root, acc); err != nil {
		t.Fatal(err)
	}
	writeReviewLog(t, root, acc.ID, verdictJSON("concerns", []string{finding}, nil))
	res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
	})
	if err != nil {
		t.Fatalf("findings repair: %v %+v", err, res)
	}
	if res.RepairCreated != 1 || res.AcceptanceCreated != 0 || res.RepairTaskID == "" || res.RepairTaskID == beta.ID {
		t.Fatalf("expected named repair from findings: %+v", res)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	if wf.DirectionTaskIDs[0] != alpha.ID && wf.DirectionTaskIDs[1] != alpha.ID {
		t.Fatalf("unaffected dropped: %v", wf.DirectionTaskIDs)
	}
	repair, err := loadTask(root, res.RepairTaskID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Join(repair.Prompts, "\n")
	if !strings.Contains(prompt, finding) || !strings.Contains(prompt, "acceptance_verdict=concerns") {
		t.Fatalf("repair prompt missing findings:\n%s", prompt)
	}
	if repair.PermissionMode != "acceptEdits" || repair.MaxAttempts != 2 || repair.Effort != "high" {
		t.Fatalf("frozen limits dropped: mode=%s attempts=%d effort=%s", repair.PermissionMode, repair.MaxAttempts, repair.Effort)
	}
	if got := strings.Join(repair.AllowedTools, ","); got != "read,edit" {
		t.Fatalf("allowed tools=%s", got)
	}
	if repair.SessionStage != beta.SessionStage {
		t.Fatalf("scope widened: %s", repair.SessionStage)
	}
}

func TestOptInOrdinaryDoneLiveWriterHolds(t *testing.T) {
	root, _, cfg, wf, members := readyAcceptFixture(t)
	occupant := members[0]
	occupant.Dir = t.TempDir()
	if err := writeTaskFile(root, occupant); err != nil {
		t.Fatal(err)
	}
	plantLiveAttempt(t, root, occupant)
	if !taskHasLiveWriterProof(root, occupant) {
		t.Fatal("planted live producer not visible")
	}
	res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{OptIn: true, MaxActions: 2})
	if err == nil || !res.Held || res.AcceptanceCreated != 0 || res.RepairCreated != 0 || res.GoalAccepted {
		t.Fatalf("live writer must hold: %+v %v", res, err)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	if wf.AcceptanceTaskID != "" || wf.GoalCompleted {
		t.Fatalf("live writer created acceptance: %+v", wf)
	}
}

func TestOptInRepairPreservesDependsOn(t *testing.T) {
	root, dir, cfg, wf, members := readyDisjointAcceptFixture(t)
	alpha, beta := members[0], members[1]
	prereq := newTask(root, cfg, typeSequence, "blocked extra prereq", dir, []string{"wait"}, 5)
	prereq.WorkflowID = wf.ID
	if err := saveTask(root, prereq); err != nil {
		t.Fatal(err)
	}
	beta.Status = statusFailed
	beta.DependsOn = append(append([]string(nil), beta.DependsOn...), prereq.ID)
	beta.LastError = "beta terminal fail"
	if err := writeTaskFile(root, beta); err != nil {
		t.Fatal(err)
	}
	res, err := advanceOptInWorkflow(root, cfg, wf, workflowAdvanceOpts{
		OptIn: true, MaxActions: 1, RepairDirection: beta.SessionStage,
	})
	if err != nil {
		t.Fatalf("repair: %v %+v", err, res)
	}
	repair, err := loadTask(root, res.RepairTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAllIDs(repair.DependsOn, beta.DependsOn) || !containsAllIDs(repair.DependsOn, []string{wf.DesignTaskID, prereq.ID}) {
		t.Fatalf("repair DependsOn=%v lost original/design/prereq", repair.DependsOn)
	}
	prompt := strings.Join(repair.Prompts, "\n")
	if !strings.Contains(prompt, "beta terminal fail") {
		t.Fatalf("repair prompt missing member failure:\n%s", prompt)
	}
	if wf.DirectionTaskIDs[0] != alpha.ID && wf.DirectionTaskIDs[1] != alpha.ID {
		t.Fatalf("unaffected dropped: %v", wf.DirectionTaskIDs)
	}
}

func containsAllIDs(have, want []string) bool {
	set := map[string]bool{}
	for _, id := range have {
		set[id] = true
	}
	for _, id := range want {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if !set[id] {
			return false
		}
	}
	return true
}

func TestCandidateBinReadyRepairDirectionNoRepair(t *testing.T) {
	bin := requireCandidateBin(t)
	root, _, cfg, wf, members := readyAcceptFixture(t)
	alpha, beta := members[0].ID, members[1].ID
	out, err := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1",
		"-repair-direction", members[1].SessionStage).CombinedOutput()
	if err != nil {
		t.Fatalf("CLI ready repair-direction: %v %s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "acceptance_created=1") || !strings.Contains(text, "repair_created=0") {
		t.Fatalf("CLI must create one acceptance and no repair: %s", text)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	if wf.DirectionTaskIDs[0] != alpha || wf.DirectionTaskIDs[1] != beta {
		t.Fatalf("CLI changed members: %v", wf.DirectionTaskIDs)
	}
	again, err := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1",
		"-repair-direction", members[1].SessionStage).CombinedOutput()
	if err != nil {
		t.Fatalf("CLI queued: %v %s", err, again)
	}
	if !strings.Contains(string(again), "repair_created=0") {
		t.Fatalf("CLI queued review admitted repair: %s", again)
	}
}

func markDurablyDone(t *testing.T, root string, id string) {
	t.Helper()
	tk, err := loadTask(root, id)
	if err != nil || tk == nil {
		t.Fatalf("load %s: %v", id, err)
	}
	if tk.Status != statusDone {
		tk.Status = statusDone
		if err := writeTaskFile(root, tk); err != nil {
			t.Fatal(err)
		}
		tk, err = loadTask(root, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := persistTaskEvent(root, tk, evDone, "test:done", statusDone, tk.Step, nil); err != nil {
		t.Fatal(err)
	}
	tk, err = loadTask(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if !taskDurablyDone(root, tk) {
		t.Fatalf("%s not durably done status=%s trans=%s", id, tk.Status, tk.LastCommittedTransitionID)
	}
}

func TestCandidateBinFailNamedRepairBlockedThenExecReaccept(t *testing.T) {
	bin := requireCandidateBin(t)
	root, dir, cfg, wf, members := readyDisjointAcceptFixture(t)
	alpha, beta := members[0], members[1]
	prereq := newTask(root, cfg, typeSequence, "blocked extra prereq", dir, []string{"wait"}, 5)
	prereq.WorkflowID = wf.ID
	prereq.Status = statusDone
	if err := saveTask(root, prereq); err != nil {
		t.Fatal(err)
	}
	beta.Status = statusFailed
	beta.DependsOn = append(append([]string(nil), beta.DependsOn...), prereq.ID)
	if err := writeTaskFile(root, beta); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(t.TempDir(), "calls")
	cfg.ClaudeBin = countingClaudeBin(t, count, mkOKResultJSON("beta-repair"))
	cfg.StepTimeoutMin = 1
	cfg.DrainRescanSec = 1
	wf.WriterEngine = "claude"
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	admit, err := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1",
		"-repair-direction", beta.SessionStage).CombinedOutput()
	if err != nil {
		t.Fatalf("CLI admit repair: %v %s", err, admit)
	}
	if !strings.Contains(string(admit), "repair_created=1") {
		t.Fatalf("CLI repair admit: %s", admit)
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
		t.Fatalf("missing repair id members=%v", wf.DirectionTaskIDs)
	}
	repair, err := loadTask(root, repairID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAllIDs(repair.DependsOn, []string{wf.DesignTaskID, prereq.ID}) {
		t.Fatalf("CLI repair DependsOn=%v", repair.DependsOn)
	}
	before := providerCallCount(t, count)
	blocked, err := exec.Command(bin, "run", "-once", "-root", root, "-quiet", repairID).CombinedOutput()
	if err == nil {
		t.Fatalf("blocked extra prereq must prevent early exec: %s", blocked)
	}
	if providerCallCount(t, count) != before {
		t.Fatalf("repair executed early fake=%d", providerCallCount(t, count))
	}
	if taskDurablyDone(root, prereq) {
		t.Fatal("status=done extra prereq was already durable before the closeout event")
	}
	markDurablyDone(t, root, wf.DesignTaskID)
	markDurablyDone(t, root, prereq.ID)
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !dagAllowsTask(root, tasks, repairID) {
		repair, _ = loadTask(root, repairID)
		t.Fatalf("dag still blocked after durable prereq deps=%v", repair.DependsOn)
	}
	ran, err := exec.Command(bin, "run", "-once", "-root", root, "-quiet", repairID).CombinedOutput()
	if err != nil {
		t.Fatalf("CLI fake exec repair: %v %s runner=%s", err, ran, repair.PreferRunner)
	}
	if providerCallCount(t, count) != before+1 {
		t.Fatalf("fake exec count=%d before=%d out=%s", providerCallCount(t, count), before, ran)
	}
	alphaAfter, err := loadTask(root, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if alphaAfter.Status != statusDone || alphaAfter.Attempts != alpha.Attempts {
		t.Fatalf("unaffected rerun: %+v", alphaAfter)
	}
	repairDone, err := loadTask(root, repairID)
	if err != nil {
		t.Fatal(err)
	}
	if repairDone.Status != statusDone {
		t.Fatalf("repair not done after fake exec: %s", repairDone.Status)
	}
	accOut, err := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("CLI reaccept: %v %s", err, accOut)
	}
	if !strings.Contains(string(accOut), "acceptance_created=1") {
		t.Fatalf("reaccept: %s", accOut)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	markOverallAcceptancePass(t, root, wf)
	passOut, err := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("CLI pass: %v %s", err, passOut)
	}
	if !strings.Contains(string(passOut), "goal_accepted=true") {
		t.Fatalf("pass stop: %s", passOut)
	}
	if providerCallCount(t, count) != before+1 {
		t.Fatalf("pass increased fake exec: %d", providerCallCount(t, count))
	}
	t.Logf("manual Git/fake review seeding labeled; binary=%s argv=workflow advance / run -once fake=%d", bin, providerCallCount(t, count))
}

func TestCandidateBinTwoDirectionFakeExecPassStop(t *testing.T) {
	bin := requireCandidateBin(t)
	root, dir, cfg, wf, members := queuedDisjointFanoutFixture(t)
	count := filepath.Join(t.TempDir(), "calls")
	cfg.ClaudeBin = countingClaudeBin(t, count, mkOKResultJSON("dir-exec"))
	cfg.StepTimeoutMin = 1
	cfg.DrainRescanSec = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	design, err := loadTask(root, wf.DesignTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistTaskEvent(root, design, evDone, "test:done", statusDone, design.Step, nil); err != nil {
		t.Fatal(err)
	}
	before := providerCallCount(t, count)
	for _, m := range members {
		m.PreferRunner = "claude"
		m.RunnerExplicit = true
		if err := writeTaskFile(root, m); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, "run", "-once", "-root", root, "-quiet", m.ID).CombinedOutput()
		if err != nil {
			t.Fatalf("fake exec %s: %v %s", m.SessionStage, err, out)
		}
	}
	if providerCallCount(t, count) != before+2 {
		t.Fatalf("two fake directions=%d", providerCallCount(t, count))
	}
	commit := workflowGit(t, dir, "rev-parse", "HEAD")
	tree := workflowGit(t, dir, "rev-parse", "HEAD^{tree}")
	freeze, err := exec.Command(bin, "workflow", "freeze-candidate", wf.ID, "-root", root, "-commit", commit, "-tree", tree).CombinedOutput()
	if err != nil {
		t.Fatalf("freeze: %v %s", err, freeze)
	}
	first, err := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("accept: %v %s", err, first)
	}
	if !strings.Contains(string(first), "acceptance_created=1") {
		t.Fatalf("accept: %s", first)
	}
	wf, _ = loadWorkflow(root, cfg, wf.ID)
	markOverallAcceptancePass(t, root, wf)
	pass, err := exec.Command(bin, "workflow", "advance", wf.ID, "-root", root, "-opt-in", "-max-actions", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("pass: %v %s", err, pass)
	}
	if !strings.Contains(string(pass), "goal_accepted=true") {
		t.Fatalf("pass stop: %s", pass)
	}
	if providerCallCount(t, count) != before+2 {
		t.Fatalf("pass replayed writers fake=%d", providerCallCount(t, count))
	}
	t.Logf("manual Git/fake review seeding labeled; binary=%s fake=%d", bin, providerCallCount(t, count))
}

func queuedDisjointFanoutFixture(t *testing.T) (root, dir string, cfg *Config, wf *WorkflowRecord, members []*Task) {
	t.Helper()
	root, dir, cfg, wf, members = readyDisjointAcceptFixture(t)
	for i, m := range members {
		m.Status = statusQueued
		if err := writeTaskFile(root, m); err != nil {
			t.Fatal(err)
		}
		loaded, err := loadTask(root, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		members[i] = loaded
	}
	return root, dir, cfg, wf, members
}
