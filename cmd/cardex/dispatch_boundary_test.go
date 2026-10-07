package main

import (
	"strings"
	"testing"
)

func TestOrdinaryDirectIgnoresGoalPolicyAndAdmissionIsNotLaunch(t *testing.T) {
	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.ConfirmedDispatch = &ConfirmedDispatchPolicy{
		OnboardingDecision: "adopt",
		WorkMode:           workModeGoal,
	}
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := cmdAdd([]string{"-root", root, "-dir", work, "diagnose the check, change the code, and repair the author finding"}); err != nil {
		t.Fatalf("saved goal policy must not block ordinary direct submission: %v", err)
	}
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ordinary add wrote %d cards", len(tasks))
	}
	got := tasks[0]
	if got.Goal != nil {
		t.Fatalf("ordinary add became a native goal: %+v", got.Goal)
	}
	if got.Status != statusQueued {
		t.Fatalf("admitted status=%s", got.Status)
	}
	if got.DispatchDecision == nil || got.DispatchDecision.WorkMode != workModeDirect || !strings.Contains(got.DispatchDecision.Reason, "not native Goal") {
		t.Fatalf("decision=%+v", got.DispatchDecision)
	}

	before := len(tasks)
	err = cmdAdd([]string{"-root", root, "-dir", work, "-work-mode", workModeStaged, "staged work"})
	if err == nil || !strings.Contains(err.Error(), "cardex workflow") || !strings.Contains(err.Error(), "未写入本卡") {
		t.Fatalf("explicit staged = %v", err)
	}
	tasks, err = loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != before {
		t.Fatalf("rejected staged add wrote a card: %d", len(tasks))
	}

	if err := cmdAdd([]string{"-root", root, "-dir", work, "-hold", "predecessor stays held"}); err != nil {
		t.Fatal(err)
	}
	tasks, err = loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	var pred *Task
	for _, tk := range tasks {
		if tk != nil && tk.Status == statusHeld {
			pred = tk
		}
	}
	if pred == nil {
		t.Fatal("held predecessor was not admitted")
	}
	if err := cmdAdd([]string{"-root", root, "-dir", work, "-depends-on", pred.ID, "child waits"}); err != nil {
		t.Fatal(err)
	}
	child, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	var queued *Task
	for _, tk := range child {
		if tk != nil && tk.ID != pred.ID && tk.ID != got.ID && tk.Status == statusQueued {
			queued = tk
		}
	}
	if queued == nil {
		t.Fatal("dependent card was not admitted to the queue")
	}
	if !dagAllowsTask(root, []*Task{queued, pred}, queued.ID) {
		return
	}
	t.Fatal("queue admission must not be launch readiness while the predecessor is held")
}

func TestWorkflowFanoutAcceptAndRoundCommands(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir, "-max-rounds", "2")
	if err := cmdWorkflowGoalRound([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	round1, err := loadTask(root, wf.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	round1.Status = statusDone
	if err := writeTaskFile(root, round1); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowGoalRound([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	round2, err := loadTask(root, wf.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadTask(root, round1.ID); err != nil {
		t.Fatal("goal round dropped retained work")
	}
	if round1.ID == round2.ID {
		t.Fatal("second round reused the first card")
	}
	if wf.GoalCompleted {
		t.Fatal("opening a round accepted the goal")
	}

	design := newTask(root, cfg, typeSequence, "design card", dir, []string{"supplied design plan"}, 1)
	design.Status = statusDone
	design.WorkflowID = wf.ID
	if err := saveTask(root, design); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowFanout([]string{"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta"}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.DirectionTaskIDs) != 2 || wf.AcceptanceTaskID != "" {
		t.Fatalf("fanout directions=%v acceptance=%s", wf.DirectionTaskIDs, wf.AcceptanceTaskID)
	}
	first := append([]string{}, wf.DirectionTaskIDs...)
	if err := cmdWorkflowFanout([]string{"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta"}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(wf.DirectionTaskIDs, ",") != strings.Join(first, ",") {
		t.Fatalf("duplicate fanout created new cards: %v vs %v", wf.DirectionTaskIDs, first)
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err == nil || !strings.Contains(err.Error(), "未创建验收卡") {
		t.Fatalf("queued directions created acceptance: %v", err)
	}
	setStatus := func(status string) {
		t.Helper()
		task, err := loadTask(root, wf.DirectionTaskIDs[0])
		if err != nil {
			t.Fatal(err)
		}
		task.Status = status
		if err := writeTaskFile(root, task); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{statusHeld, statusFailed, statusCanceled} {
		setStatus(status)
		if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err == nil || !strings.Contains(err.Error(), "未创建验收卡") {
			t.Fatalf("status %s created acceptance: %v", status, err)
		}
	}
	missing := append([]string{}, wf.DirectionTaskIDs...)
	wf.DirectionTaskIDs = append(wf.DirectionTaskIDs, "missing-direction")
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err == nil || !strings.Contains(err.Error(), "未知") {
		t.Fatalf("unknown direction created acceptance: %v", err)
	}
	wf.DirectionTaskIDs = missing
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	for _, id := range wf.DirectionTaskIDs {
		task, err := loadTask(root, id)
		if err != nil {
			t.Fatal(err)
		}
		task.Status = statusDone
		if err := writeTaskFile(root, task); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID == "" || wf.GoalCompleted {
		t.Fatalf("done members must create one card without accepting: id=%s accepted=%v", wf.AcceptanceTaskID, wf.GoalCompleted)
	}
	accID := wf.AcceptanceTaskID
	commit, tree := workflowHeadCandidate(t, dir)
	if wf.Candidate == nil || wf.Candidate.Commit != commit || wf.Candidate.Tree != tree {
		t.Fatalf("acceptance candidate=%+v want %s %s", wf.Candidate, commit, tree)
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID != accID {
		t.Fatalf("duplicate accept created %s after %s", wf.AcceptanceTaskID, accID)
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID, "-complete"}); err == nil || wf.GoalCompleted {
		t.Fatal("unfinished acceptance marked the goal accepted")
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.GoalCompleted {
		t.Fatal("failed complete accepted the goal")
	}
	// A second evDone on an already committed terminal card is a no-op, so each
	// verdict starts from done with no review binding. writeReviewLog still has
	// to produce the bytes the gate reads.
	recordVerdict := func(body string) {
		t.Helper()
		acc, err := loadTask(root, accID)
		if err != nil {
			t.Fatal(err)
		}
		acc.Status = statusDone
		acc.ReviewOutput = nil
		acc.LastCommittedTransitionID = ""
		acc.ActiveAttemptID = ""
		if err := writeTaskFile(root, acc); err != nil {
			t.Fatal(err)
		}
		writeReviewLog(t, root, accID, body)
	}
	refuse := func(body, label string) {
		t.Helper()
		recordVerdict(body)
		if err := cmdWorkflowAccept([]string{"-root", root, wf.ID, "-complete"}); err == nil {
			t.Fatalf("%s verdict accepted the goal", label)
		}
		wf, err = loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if wf.GoalCompleted || wf.AcceptanceTaskID != accID {
			t.Fatalf("%s verdict changed acceptance id=%s accepted=%v", label, wf.AcceptanceTaskID, wf.GoalCompleted)
		}
	}
	refuse(verdictJSON("concerns", nil, []string{"p1"}), "concerns")
	refuse(verdictJSON("ACCEPT", nil, nil), "unknown")
	refuse(verdictJSON("block", []string{"p0"}, nil), "block")
	recordVerdict(verdictJSON("pass", nil, nil))
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID, "-complete"}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wf.GoalCompleted {
		t.Fatal("pass verdict on the current artifact did not accept the goal")
	}
	n := 0
	all, _ := loadTasks(root)
	for _, task := range all {
		if task != nil && strings.Contains(task.Title, "overall acceptance") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("acceptance cards = %d", n)
	}
	if err := cmdWorkflowGoalRound([]string{"-root", root, wf.ID}); err == nil || !strings.Contains(err.Error(), "不再开启下一轮") {
		t.Fatalf("round after acceptance = %v", err)
	}
	if _, err := loadTask(root, round1.ID); err != nil {
		t.Fatal(err)
	}

	limitRoot, limitDir := workflowTestRoot(t)
	limit := initTestWorkflow(t, limitRoot, limitDir, "-max-rounds", "1")
	if err := cmdWorkflowGoalRound([]string{"-root", limitRoot, limit.ID}); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowGoalRound([]string{"-root", limitRoot, limit.ID}); err == nil || !strings.Contains(err.Error(), "已达轮次上限") {
		t.Fatalf("max rounds = %v", err)
	}
	limit, err = loadWorkflow(limitRoot, workflowTestCfg(t, limitRoot), limit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if limit.GoalCompleted {
		t.Fatal("max rounds counted as success")
	}

	if err := cmdWorkflowGoalDirection([]string{"-root", root, wf.ID, "-direction", "native-a"}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.NativeGoalTaskIDs) != 1 {
		t.Fatalf("native directions = %v", wf.NativeGoalTaskIDs)
	}
	if wf.GoalCompleted || wf.AcceptanceTaskID != "" {
		t.Fatal("a new required direction kept a stale acceptance")
	}
	firstNative := wf.NativeGoalTaskIDs[0]
	if err := cmdWorkflowGoalDirection([]string{"-root", root, wf.ID, "-direction", "native-a"}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.NativeGoalTaskIDs) != 1 || wf.NativeGoalTaskIDs[0] != firstNative {
		t.Fatalf("duplicate direction = %v", wf.NativeGoalTaskIDs)
	}
	if wf.GoalCompleted {
		t.Fatal("reusing a direction accepted the goal")
	}
	if err := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", firstNative, "-hosted"}); err == nil {
		t.Fatal("goal-launch without grok enabled started a provider")
	}
	launched, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if launched.GoalCompleted {
		t.Fatal("refused goal-launch accepted the goal")
	}
}
