package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseReviewVerdictAdmissiblePassJSONContract(t *testing.T) {
	t.Parallel()
	admissible := func(raw string) bool {
		t.Helper()
		return reviewVerdictIsAdmissiblePass(parseReviewVerdict(raw))
	}
	if admissible(`{"verdict":"pass"}`) {
		t.Fatal("missing p0/p1 is not an admissible pass")
	}
	if admissible(`{"verdict":"pass","p0":null,"p1":null}`) {
		t.Fatal("null p0/p1 is not an admissible pass")
	}
	if admissible(`{"verdict":"pass","p0":"x","p1":[]}`) {
		t.Fatal("wrong-type p0 is not an admissible pass")
	}
	if admissible(`{"verdict":"pass","p0":[],"p1":1}`) {
		t.Fatal("wrong-type p1 is not an admissible pass")
	}
	if admissible("报告\n```json\n{\"verdict\":\"pass\"}\n```\n") {
		t.Fatal("fenced pass without arrays is not admissible")
	}
	if !admissible(`{"verdict":"pass","p0":[],"p1":[]}`) {
		t.Fatal("complete empty-array pass must be admissible")
	}
	if !admissible("报告正文\n```json\n{\"verdict\":\"pass\",\"p0\":[],\"p1\":[],\"p2\":[],\"summary\":\"ok\"}\n```\n") {
		t.Fatal("fenced complete empty-array pass must be admissible")
	}
	if !admissible(`{"verdict":"pass","p0":[],"p1":[],"summary":"usage ignored"}`) {
		t.Fatal("optional summary must not become a new gate")
	}
	if admissible(verdictJSON("pass", []string{"open"}, nil)) {
		t.Fatal("non-empty p0 is not an admissible pass")
	}
	if admissible(verdictJSON("concerns", nil, nil)) {
		t.Fatal("concerns is not a pass")
	}
	if admissible(verdictJSON("block", nil, nil)) {
		t.Fatal("block is not a pass")
	}
	if admissible(`{"verdict":"ACCEPT","p0":[],"p1":[]}`) {
		t.Fatal("unknown vocabulary is not a pass")
	}
	if reviewVerdictIsAdmissiblePass(&reviewVerdict{Verdict: "pass"}) {
		t.Fatal("Go zero-value pass is not JSON presence")
	}

	root, dir, cfg, wf, _ := readyAcceptFixture(t)
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	accID := wf.AcceptanceTaskID
	plantAcceptanceVerdict := func(body string) {
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
	for _, raw := range []string{
		`{"verdict":"pass"}`,
		`{"verdict":"pass","p0":null,"p1":null}`,
		`{"verdict":"pass","p0":"nope","p1":[]}`,
	} {
		plantAcceptanceVerdict("报告\n```json\n" + raw + "\n```\n")
		if err := cmdWorkflowAccept([]string{"-root", root, wf.ID, "-complete"}); err == nil {
			t.Fatalf("incomplete pass %s accepted the goal", raw)
		}
		wf, err = loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if wf.GoalCompleted {
			t.Fatalf("incomplete pass %s marked the goal done", raw)
		}
	}
	plantAcceptanceVerdict(verdictJSON("pass", nil, nil))
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID, "-complete"}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wf.GoalCompleted {
		t.Fatal("complete empty-array pass must accept the goal")
	}
	_ = dir
}

func lastStageSolReview(t *testing.T) (root string, cfg *Config, impl, sol *Task) {
	t.Helper()
	root = testRoot(t)
	cfg = policyTestConfig()
	impl = finalOwnerTask("opus", routeClassBackend, riskClassHigh)
	impl.ID = "last-stage-sol-lineage"
	impl.Dir = t.TempDir()
	impl.Title = "identity protocol change"
	impl.Status = statusHeld
	impl.Step = len(impl.Prompts)
	route, ok := resolveOwnerRoute(cfg, impl)
	if !ok || !pinOwnerPrimaryRoute(impl, route) {
		t.Fatalf("failed to pin high-risk route: %+v ok=%v", route, ok)
	}
	if err := saveTask(root, impl); err != nil {
		t.Fatal(err)
	}
	kimi, err := ensureReviewAfterTask(root, cfg, impl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if kimi == nil || kimi.ReviewPlanStage != reviewStageKimiSecondView {
		t.Fatalf("expected kimi second view: %+v", kimi)
	}
	kimiPass := "```json\n{\"verdict\":\"pass\",\"p0\":[],\"p1\":[],\"p2\":[],\"summary\":\"independent view complete\"}\n```"
	advancePlannedReview(root, cfg, kimi, kimiPass, nil)
	impl, err = findTaskAnywhere(root, impl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if impl.Status != statusHeld || reviewCompleted(impl, reviewStageSolMax) ||
		pendingRequiredReviewStage(impl) != reviewStageSolMax {
		t.Fatalf("Kimi must leave Sol pending: %+v", impl)
	}
	sol, err = existingPlannedReviewChild(root, impl.ID, reviewStageSolMax)
	if err != nil {
		t.Fatal(err)
	}
	if sol == nil {
		t.Fatal("mandatory Sol gate missing")
	}
	sol.AutomaticSolInvocations = 1
	return root, cfg, impl, sol
}

func lastStageKimiReview(t *testing.T) (root string, cfg *Config, impl, review *Task) {
	t.Helper()
	root = testRoot(t)
	cfg = policyTestConfig()
	impl = newTask(root, cfg, typeSequence, "ordinary bounded backend", t.TempDir(), []string{"implement bounded backend change"}, 5)
	impl.Model = "opus"
	impl.RouteClass = routeClassBackend
	impl.RiskClass = riskClassOrdinary
	impl.ID = "last-stage-kimi-lineage"
	for deterministicSolSample(impl.ID, 20) {
		impl.ID += "x"
	}
	route, ok := resolveOwnerRoute(cfg, impl)
	if !ok || route.Name != "opus_backend_ordinary" || !pinOwnerPrimaryRoute(impl, route) {
		t.Fatalf("ordinary backend route was not frozen: route=%+v ok=%v", route, ok)
	}
	impl.Status = statusHeld
	impl.Step = len(impl.Prompts)
	if err := saveTask(root, impl); err != nil {
		t.Fatal(err)
	}
	review, err := ensureReviewAfterTask(root, cfg, impl, nil)
	if err != nil || review == nil {
		t.Fatalf("Kimi adversarial review was not created: review=%+v err=%v", review, err)
	}
	if review.ReviewPlanStage != reviewStageKimiAdversarial {
		t.Fatalf("expected kimi adversarial last stage: %+v", review)
	}
	return root, cfg, impl, review
}

func assertOwnerReviewIncompletePassHeld(t *testing.T, root, implID, pendingStage string) *Task {
	t.Helper()
	got, err := findTaskAnywhere(root, implID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == statusDone {
		t.Fatalf("incomplete last-stage pass marked owner-review root done: %+v", got)
	}
	if got.Status != statusHeld {
		t.Fatalf("status=%s want held", got.Status)
	}
	if reviewCompleted(got, pendingStage) {
		t.Fatalf("incomplete pass completed %s: %+v", pendingStage, got)
	}
	if pendingRequiredReviewStage(got) != pendingStage {
		t.Fatalf("pending=%s want %s completed=%v", pendingRequiredReviewStage(got), pendingStage, got.CompletedReviews)
	}
	if !strings.Contains(got.LastError, "p0/p1") {
		t.Fatalf("LastError=%q want incomplete p0/p1", got.LastError)
	}
	return got
}

func TestAdvancePlannedReviewRejectsIncompleteJSONPass(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{name: "missing", raw: `{"verdict":"pass"}`},
		{name: "null", raw: `{"verdict":"pass","p0":null,"p1":null}`},
		{name: "wrong-type-p0", raw: `{"verdict":"pass","p0":"x","p1":[]}`},
		{name: "wrong-type-p1", raw: `{"verdict":"pass","p0":[],"p1":1}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("sol/"+tc.name, func(t *testing.T) {
			t.Parallel()
			root, cfg, impl, sol := lastStageSolReview(t)
			advancePlannedReview(root, cfg, sol, "```json\n"+tc.raw+"\n```", nil)
			assertOwnerReviewIncompletePassHeld(t, root, impl.ID, reviewStageSolMax)
		})
	}
	t.Run("sol/complete-empty-arrays", func(t *testing.T) {
		t.Parallel()
		root, cfg, impl, sol := lastStageSolReview(t)
		advancePlannedReview(root, cfg, sol, "```json\n{\"verdict\":\"pass\",\"p0\":[],\"p1\":[]}\n```", nil)
		got, err := findTaskAnywhere(root, impl.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != statusDone {
			t.Fatalf("complete empty-array last-stage pass must release the root: %+v", got)
		}
		if !reviewCompleted(got, reviewStageSolMax) {
			t.Fatalf("complete pass must close the Sol gate: %+v", got)
		}
	})
	t.Run("sol/postComplete-missing", func(t *testing.T) {
		t.Parallel()
		root, cfg, impl, sol := lastStageSolReview(t)
		sol.Status = statusDone
		sol.Step = len(sol.Prompts)
		postComplete(root, cfg, sol, &claudeResult{Result: "```json\n{\"verdict\":\"pass\"}\n```"}, nil)
		assertOwnerReviewIncompletePassHeld(t, root, impl.ID, reviewStageSolMax)
	})
	t.Run("kimi-last/missing", func(t *testing.T) {
		t.Parallel()
		root, cfg, impl, review := lastStageKimiReview(t)
		advancePlannedReview(root, cfg, review, "```json\n{\"verdict\":\"pass\"}\n```", nil)
		assertOwnerReviewIncompletePassHeld(t, root, impl.ID, reviewStageKimiAdversarial)
	})
	t.Run("kimi-last/complete-empty-arrays", func(t *testing.T) {
		t.Parallel()
		root, cfg, impl, review := lastStageKimiReview(t)
		advancePlannedReview(root, cfg, review, "```json\n{\"verdict\":\"pass\",\"p0\":[],\"p1\":[]}\n```", nil)
		got, err := findTaskAnywhere(root, impl.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != statusDone {
			t.Fatalf("complete empty-array last-stage Kimi pass must release the root: %+v", got)
		}
	})
}

func captureListOutput(t *testing.T, root string) string {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	cmdErr := cmdList([]string{"-root", root})
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out, _ := io.ReadAll(rOut)
	errOut, _ := io.ReadAll(rErr)
	if cmdErr != nil {
		t.Fatalf("list: %v\n%s%s", cmdErr, out, errOut)
	}
	return string(out) + string(errOut)
}

func TestMalformedTaskRecordDisplayVsAdmission(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	cfg.MaxParallel = 2
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}

	healthy := explicitTask(t, root, "healthy billing", dir, "billing-core", "billing-core-lineage", "billing", []string{"internal/billing"}, nil)
	brokenID := "broken-unknown-writer"
	if err := os.WriteFile(taskPath(root, brokenID), []byte(`{"status":"running",`), 0o600); err != nil {
		t.Fatal(err)
	}

	listed := captureListOutput(t, root)
	if !strings.Contains(listed, healthy.ID) {
		t.Fatalf("display must still list the healthy card:\n%s", listed)
	}
	if !strings.Contains(listed, "警告") && !strings.Contains(listed, brokenID) {
		t.Fatalf("display should warn about the broken sibling:\n%s", listed)
	}

	live := reconstructLiveWriterClaims(root)
	if !writerConflictsWithActive(healthy, live) {
		t.Fatal("unknown unreadable evidence must not be treated as absence on the admission path")
	}

	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })
	var launched []string
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		launched = append(launched, tk.ID)
		return nil
	}
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range launched {
		if id == healthy.ID {
			t.Fatal("tick launched a writer while a sibling record was unreadable with unknown scope")
		}
	}

	t.Run("scoped-unrelated", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		cfg.MaxParallel = 2
		argv := filepath.Join(t.TempDir(), "argv-unrelated")
		enableManualGrok(t, root, cfg, argv)
		auth := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
		writer := admitManualWriter(t, root, cfg, auth)
		if err := os.WriteFile(taskPath(root, writer.ID), []byte(`{"status":"running",`), 0o600); err != nil {
			t.Fatal(err)
		}
		billDir := extraWorkflowDir(t)
		bill := initNamedWorkflow(t, root, billDir, "billing", "internal/billing")
		billWriter := admitManualWriter(t, root, cfg, bill)
		headlessGoalStdin(t)
		if err := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", billWriter.ID, "-manual"}); err != nil {
			t.Fatalf("proven unrelated scope must not block a disjoint launch: %v", err)
		}
	})

	t.Run("overlap", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		cfg.MaxParallel = 2
		enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv-overlap"))
		auth := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
		writer := admitManualWriter(t, root, cfg, auth)
		if err := os.WriteFile(taskPath(root, writer.ID), []byte(`{"status":"running",`), 0o600); err != nil {
			t.Fatal(err)
		}
		other := initNamedWorkflow(t, root, extraWorkflowDir(t), "authz2", "internal/billing")
		second := admitManualWriter(t, root, cfg, other)
		second.WriteDomain = copyWriteDomain(auth.WriteDomain)
		if err := saveTask(root, second); err != nil {
			t.Fatal(err)
		}
		headlessGoalStdin(t)
		if err := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", second.ID, "-manual"}); err == nil {
			t.Fatal("overlapping proven scope of a damaged record must refuse launch")
		}
	})

	t.Run("fanout-unknown", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		design := doneDesign(t, root, cfg, wf, dir)
		if err := os.WriteFile(taskPath(root, "orphan-broken"), []byte(`{"status":"running",`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowFanout([]string{"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta"}); err == nil {
			t.Fatal("fanout must fail closed on unreadable sibling evidence")
		}
	})
}

func TestGoalLaunchReusesGoalRunAdmission(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	cfg.MaxParallel = 2
	hold := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(hold, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(hold)
	argvA := filepath.Join(t.TempDir(), "argv-a")
	argvB := filepath.Join(t.TempDir(), "argv-b")
	cfg.GrokBuildBin = fakeGrokGoalBinHold(t, argvA, hold)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh", MaxParallel: 8}
	saveGoalCfg(t, root, cfg)

	auth := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
	bill := initNamedWorkflow(t, root, extraWorkflowDir(t), "billing", "internal/billing")
	aWriter := admitManualWriter(t, root, cfg, auth)
	bWriter := admitManualWriter(t, root, cfg, bill)
	headlessGoalStdin(t)

	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errA = cmdWorkflowGoalLaunch([]string{"-root", root, "-task", aWriter.ID, "-manual"})
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(argvA); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Remove(hold)
			wg.Wait()
			t.Fatalf("first goal-launch did not start: %v", errA)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cfgB := *cfg
	cfgB.GrokBuildBin = fakeGrokGoalBinHold(t, argvB, hold)
	saveGoalCfg(t, root, &cfgB)
	wg.Add(1)
	go func() {
		defer wg.Done()
		errB = cmdWorkflowGoalLaunch([]string{"-root", root, "-task", bWriter.ID, "-manual"})
	}()
	deadline = time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(argvB); err == nil {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	os.Remove(hold)
	wg.Wait()
	if errA != nil {
		t.Fatalf("disjoint goal-launch A: %v", errA)
	}
	if errB != nil {
		t.Fatalf("disjoint proven directions must launch within max_parallel: %v", errB)
	}

	t.Run("overlap", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		cfg.MaxParallel = 2
		hold := filepath.Join(t.TempDir(), "hold2")
		if err := os.WriteFile(hold, []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(hold)
		argv := filepath.Join(t.TempDir(), "argv-c")
		cfg.GrokBuildBin = fakeGrokGoalBinHold(t, argv, hold)
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		saveGoalCfg(t, root, cfg)
		w1 := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
		w2 := initNamedWorkflow(t, root, extraWorkflowDir(t), "authz2", "internal/billing")
		first := admitManualWriter(t, root, cfg, w1)
		second := admitManualWriter(t, root, cfg, w2)
		second.WriteDomain = copyWriteDomain(w1.WriteDomain)
		if err := saveTask(root, second); err != nil {
			t.Fatal(err)
		}
		headlessGoalStdin(t)
		done := make(chan struct{})
		var firstErr error
		go func() {
			firstErr = cmdWorkflowGoalLaunch([]string{"-root", root, "-task", first.ID, "-manual"})
			close(done)
		}()
		deadline := time.Now().Add(8 * time.Second)
		for {
			if _, err := os.Stat(argv); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Remove(hold)
				<-done
				t.Fatalf("overlap first launch did not start: %v", firstErr)
			}
			time.Sleep(20 * time.Millisecond)
		}
		overlapErr := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", second.ID, "-manual"})
		os.Remove(hold)
		<-done
		if overlapErr == nil {
			t.Fatal("overlapping write domain must not goal-launch")
		}
		if !errors.Is(overlapErr, errAdmissionDenied) {
			t.Fatalf("overlap must be admission denied: %v", overlapErr)
		}
	})

	t.Run("reused-session", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		cfg.MaxParallel = 2
		enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv-sess"))
		w1 := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
		w2 := initNamedWorkflow(t, root, extraWorkflowDir(t), "billing", "internal/billing")
		a := admitManualWriter(t, root, cfg, w1)
		b := admitManualWriter(t, root, cfg, w2)
		a.SessionID = "shared-session-id"
		a.Status = statusRunning
		if err := saveTask(root, a); err != nil {
			t.Fatal(err)
		}
		b.SessionID = "shared-session-id"
		if err := saveTask(root, b); err != nil {
			t.Fatal(err)
		}
		headlessGoalStdin(t)
		if err := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", b.ID, "-manual"}); err == nil {
			t.Fatal("reused session must not launch a second writer")
		}
	})

	t.Run("unknown-custody", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv-unk"))
		wf := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
		tk := admitManualWriter(t, root, cfg, wf)
		if err := os.WriteFile(taskPath(root, "orphan-broken"), []byte(`{"status":"running",`), 0o600); err != nil {
			t.Fatal(err)
		}
		headlessGoalStdin(t)
		if err := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", tk.ID, "-manual"}); err == nil {
			t.Fatal("unknown custody of an unreadable sibling must refuse launch")
		}
	})

	t.Run("predecessor", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv-dep"))
		wf := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
		pred := newTask(root, cfg, typeSequence, "pred", dir, []string{"p"}, 5)
		pred.Status = statusQueued
		applyControlDefaults(pred)
		if err := saveTask(root, pred); err != nil {
			t.Fatal(err)
		}
		tk := admitManualWriter(t, root, cfg, wf)
		tk.DependsOn = []string{pred.ID}
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		headlessGoalStdin(t)
		if err := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", tk.ID, "-manual"}); err == nil {
			t.Fatal("missing DAG predecessor must not goal-launch")
		}
	})

	t.Run("capacity", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		cfg.MaxParallel = 1
		hold := filepath.Join(t.TempDir(), "hold-cap")
		if err := os.WriteFile(hold, []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(hold)
		argv := filepath.Join(t.TempDir(), "argv-cap")
		cfg.GrokBuildBin = fakeGrokGoalBinHold(t, argv, hold)
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh", MaxParallel: 8}
		saveGoalCfg(t, root, cfg)
		w1 := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
		w2 := initNamedWorkflow(t, root, extraWorkflowDir(t), "billing", "internal/billing")
		first := admitManualWriter(t, root, cfg, w1)
		second := admitManualWriter(t, root, cfg, w2)
		headlessGoalStdin(t)
		done := make(chan struct{})
		go func() {
			_ = cmdWorkflowGoalLaunch([]string{"-root", root, "-task", first.ID, "-manual"})
			close(done)
		}()
		deadline := time.Now().Add(8 * time.Second)
		for {
			if _, err := os.Stat(argv); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Remove(hold)
				<-done
				t.Fatal("capacity first launch did not start")
			}
			time.Sleep(20 * time.Millisecond)
		}
		capErr := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", second.ID, "-manual"})
		os.Remove(hold)
		<-done
		if capErr == nil {
			t.Fatal("max_parallel 1 must refuse a second launch")
		}
	})
}

func TestFanoutDirectionIdentitiesAndOverlap(t *testing.T) {
	t.Parallel()
	t.Run("omitted-retains-declared-authority", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		design := doneDesign(t, root, cfg, wf, dir)
		if err := cmdWorkflowFanout([]string{"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta"}); err != nil {
			t.Fatal(err)
		}
		wf, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(wf.DirectionTaskIDs) != 2 {
			t.Fatalf("directions=%v", wf.DirectionTaskIDs)
		}
		a, err := loadTask(root, wf.DirectionTaskIDs[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := loadTask(root, wf.DirectionTaskIDs[1])
		if err != nil {
			t.Fatal(err)
		}
		if a.WriteDomain == nil || b.WriteDomain == nil {
			t.Fatal("omitted scope must copy the workflow write domain")
		}
		if a.WriteDomain.ID != wf.WriteDomain.ID || b.WriteDomain.ID != wf.WriteDomain.ID {
			t.Fatalf("omitted scope changed domain id: %s %s want %s", a.WriteDomain.ID, b.WriteDomain.ID, wf.WriteDomain.ID)
		}
		if !sameStringSet(a.WriteDomain.Paths, wf.WriteDomain.Paths) {
			t.Fatalf("omitted scope narrowed paths: %v vs %v", a.WriteDomain.Paths, wf.WriteDomain.Paths)
		}
		if !writerClaimsConflict(writerClaimForTask(a), writerClaimForTask(b)) {
			t.Fatal("copied domain identity must keep overlap protection")
		}
	})

	t.Run("explicit-disjoint", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		design := doneDesign(t, root, cfg, wf, dir)
		if err := cmdWorkflowFanout([]string{
			"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta",
			"-direction-write-paths", "alpha=internal/auth;beta=internal/billing",
		}); err != nil {
			t.Fatal(err)
		}
		wf, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		a, err := loadTask(root, wf.DirectionTaskIDs[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := loadTask(root, wf.DirectionTaskIDs[1])
		if err != nil {
			t.Fatal(err)
		}
		if a.WriteDomain.ID == b.WriteDomain.ID || a.WriteDomain.Lineage == b.WriteDomain.Lineage {
			t.Fatalf("explicit disjoint scope must get independent identities: %+v %+v", a.WriteDomain, b.WriteDomain)
		}
		if writerClaimsConflict(writerClaimForTask(a), writerClaimForTask(b)) {
			t.Fatal("explicit disjoint paths must not serialize by copied domain id")
		}
	})

	t.Run("chinese-disjoint-names", func(t *testing.T) {
		byName := fanoutDirectionsByName(t, "认证,计费", "认证=internal/auth;计费=internal/billing")
		a, b := byName["认证"], byName["计费"]
		if a == nil || b == nil {
			t.Fatalf("missing 认证/计费 directions: %v", keysOfTasks(byName))
		}
		assertIndependentDisjointDirections(t, a, b)
		if a.WriteDomain.ID == "auth-tokens-lane" || b.WriteDomain.ID == "auth-tokens-lane" {
			t.Fatalf("认证/计费 must not copy auth-tokens-lane: %+v %+v", a.WriteDomain, b.WriteDomain)
		}
		if a.WriteDomain.Lineage == "auth-tokens-lane-lineage" || b.WriteDomain.Lineage == "auth-tokens-lane-lineage" {
			t.Fatalf("认证/计费 must not copy auth-tokens-lane-lineage: %+v %+v", a.WriteDomain, b.WriteDomain)
		}
	})

	t.Run("shared-ascii-slug", func(t *testing.T) {
		byName := fanoutDirectionsByName(t, "Auth,auth", "Auth=internal/auth;auth=internal/billing")
		a, b := byName["Auth"], byName["auth"]
		if a == nil || b == nil {
			t.Fatalf("missing Auth/auth directions: %v", keysOfTasks(byName))
		}
		assertIndependentDisjointDirections(t, a, b)
	})

	t.Run("shared-ascii-slug-true-overlap", func(t *testing.T) {
		byName := fanoutDirectionsByName(t, "Auth,auth", "Auth=internal/auth;auth=internal/auth")
		a, b := byName["Auth"], byName["auth"]
		if a == nil || b == nil {
			t.Fatalf("missing Auth/auth directions: %v", keysOfTasks(byName))
		}
		if !writerClaimsConflict(writerClaimForTask(a), writerClaimForTask(b)) {
			t.Fatal("true path overlap must still serialize when ASCII slugs match")
		}
	})

	t.Run("identity-stable-across-order", func(t *testing.T) {
		first := fanoutDirectionsByName(t, "认证,计费", "认证=internal/auth;计费=internal/billing")
		second := fanoutDirectionsByName(t, "计费,认证", "计费=internal/billing;认证=internal/auth")
		if first["认证"].WriteDomain.ID != second["认证"].WriteDomain.ID {
			t.Fatalf("认证 id changed with fanout order: %s vs %s", first["认证"].WriteDomain.ID, second["认证"].WriteDomain.ID)
		}
		if first["计费"].WriteDomain.ID != second["计费"].WriteDomain.ID {
			t.Fatalf("计费 id changed with fanout order: %s vs %s", first["计费"].WriteDomain.ID, second["计费"].WriteDomain.ID)
		}
	})

	t.Run("omitted-chinese-retains-authority", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		design := doneDesign(t, root, cfg, wf, dir)
		if err := cmdWorkflowFanout([]string{"-root", root, wf.ID, "-design", design.ID, "-directions", "认证,计费"}); err != nil {
			t.Fatal(err)
		}
		wf, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		byName := map[string]*Task{}
		for _, id := range wf.DirectionTaskIDs {
			tk, err := loadTask(root, id)
			if err != nil {
				t.Fatal(err)
			}
			byName[tk.SessionStage] = tk
		}
		a, b := byName["认证"], byName["计费"]
		if a == nil || b == nil {
			t.Fatalf("missing omitted-scope directions: %v", keysOfTasks(byName))
		}
		if a.WriteDomain == nil || b.WriteDomain == nil {
			t.Fatal("omitted scope must copy the workflow write domain")
		}
		if a.WriteDomain.ID != wf.WriteDomain.ID || b.WriteDomain.ID != wf.WriteDomain.ID {
			t.Fatalf("omitted chinese names changed domain id: %s %s want %s", a.WriteDomain.ID, b.WriteDomain.ID, wf.WriteDomain.ID)
		}
		if !writerClaimsConflict(writerClaimForTask(a), writerClaimForTask(b)) {
			t.Fatal("copied domain identity must keep overlap protection")
		}
	})

	t.Run("explicit-owner-domain-preserved", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv-owner-domain"))
		wf := initTestWorkflow(t, root, dir)
		if err := cmdWorkflowGoalDirection([]string{
			"-root", root, wf.ID, "-direction", "认证",
			"-write-domain-id", "auth-native-dep", "-write-paths", "internal/billing",
		}); err != nil {
			t.Fatal(err)
		}
		wf, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(wf.NativeGoalTaskIDs) == 0 {
			t.Fatal("goal-direction did not admit a native task")
		}
		tk, err := loadTask(root, wf.NativeGoalTaskIDs[len(wf.NativeGoalTaskIDs)-1])
		if err != nil {
			t.Fatal(err)
		}
		if tk.WriteDomain == nil || tk.WriteDomain.ID != "auth-native-dep" {
			t.Fatalf("explicit owner write domain rewritten: %+v", tk.WriteDomain)
		}
	})

	t.Run("true-overlap", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		design := doneDesign(t, root, cfg, wf, dir)
		if err := cmdWorkflowFanout([]string{
			"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta",
			"-direction-write-paths", "alpha=internal/auth;beta=internal/auth",
		}); err != nil {
			t.Fatal(err)
		}
		wf, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := loadTask(root, wf.DirectionTaskIDs[0])
		b, _ := loadTask(root, wf.DirectionTaskIDs[1])
		if !writerClaimsConflict(writerClaimForTask(a), writerClaimForTask(b)) {
			t.Fatal("true path overlap must still serialize")
		}
	})

	t.Run("resources", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		design := doneDesign(t, root, cfg, wf, dir)
		if err := cmdWorkflowFanout([]string{
			"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta",
			"-direction-write-paths", "alpha=internal/auth;beta=internal/billing",
			"-direction-write-resources", "alpha=database:shared.primary;beta=database:shared.primary",
		}); err != nil {
			t.Fatal(err)
		}
		wf, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := loadTask(root, wf.DirectionTaskIDs[0])
		b, _ := loadTask(root, wf.DirectionTaskIDs[1])
		if !writerClaimsConflict(writerClaimForTask(a), writerClaimForTask(b)) {
			t.Fatal("shared closed resource must serialize")
		}
	})

	t.Run("depends-on", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv-dep-fan"))
		wf := initTestWorkflow(t, root, dir)
		design := doneDesign(t, root, cfg, wf, dir)
		pred := newTask(root, cfg, typeSequence, "pred", dir, []string{"p"}, 5)
		pred.Status = statusQueued
		applyControlDefaults(pred)
		if err := saveTask(root, pred); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowFanout([]string{
			"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha",
			"-depends-on", pred.ID,
		}); err != nil {
			t.Fatal(err)
		}
		wf, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		tasks, err := loadTasks(root)
		if err != nil {
			t.Fatal(err)
		}
		if dagAllowsTask(root, tasks, wf.DirectionTaskIDs[0]) {
			t.Fatal("unmet depends_on must keep the direction unready")
		}
		if err := cmdWorkflowGoalDirection([]string{
			"-root", root, wf.ID, "-direction", "native-dep", "-depends-on", pred.ID,
			"-write-domain-id", "auth-native-dep", "-write-paths", "internal/billing",
		}); err != nil {
			t.Fatal(err)
		}
		wf, err = loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		nativeID := wf.NativeGoalTaskIDs[len(wf.NativeGoalTaskIDs)-1]
		headlessGoalStdin(t)
		if err := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", nativeID, "-manual"}); err == nil {
			t.Fatal("unmet depends_on must refuse goal-launch")
		}
	})
}

func doneDesign(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, dir string) *Task {
	t.Helper()
	design := newTask(root, cfg, typeSequence, "design card", dir, []string{"supplied design plan"}, 1)
	design.Status = statusDone
	design.WorkflowID = wf.ID
	if err := saveTask(root, design); err != nil {
		t.Fatal(err)
	}
	return design
}

func fanoutDirectionsByName(t *testing.T, directions, pathAssign string) map[string]*Task {
	t.Helper()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	design := doneDesign(t, root, cfg, wf, dir)
	args := []string{"-root", root, wf.ID, "-design", design.ID, "-directions", directions}
	if strings.TrimSpace(pathAssign) != "" {
		args = append(args, "-direction-write-paths", pathAssign)
	}
	if err := cmdWorkflowFanout(args); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*Task{}
	for _, id := range wf.DirectionTaskIDs {
		tk, err := loadTask(root, id)
		if err != nil {
			t.Fatal(err)
		}
		out[tk.SessionStage] = tk
	}
	return out
}

func assertIndependentDisjointDirections(t *testing.T, a, b *Task) {
	t.Helper()
	if a.WriteDomain == nil || b.WriteDomain == nil {
		t.Fatal("missing write domains")
	}
	if a.WriteDomain.ID == b.WriteDomain.ID || a.WriteDomain.Lineage == b.WriteDomain.Lineage {
		t.Fatalf("explicit disjoint scope must get independent identities: %+v %+v", a.WriteDomain, b.WriteDomain)
	}
	if writerClaimsConflict(writerClaimForTask(a), writerClaimForTask(b)) {
		t.Fatal("explicit disjoint paths must not serialize by copied domain id")
	}
}

func keysOfTasks(m map[string]*Task) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestErroneousAcceptanceAndCoverage(t *testing.T) {
	for _, status := range []string{statusQueued, statusRunning, statusFailed, statusHeld, statusLimitPaused} {
		status := status
		t.Run("member-"+status, func(t *testing.T) {
			root, _, cfg, wf, _ := readyAcceptFixture(t)
			task, err := loadTask(root, wf.DirectionTaskIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			task.Status = status
			if err := writeTaskFile(root, task); err != nil {
				t.Fatal(err)
			}
			if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err == nil {
				t.Fatalf("status %s created acceptance", status)
			}
			wf, err = loadWorkflow(root, cfg, wf.ID)
			if err != nil {
				t.Fatal(err)
			}
			if wf.GoalCompleted || wf.AcceptanceTaskID != "" {
				t.Fatalf("status %s accepted the goal: %+v", status, wf)
			}
		})
	}

	t.Run("unknown-member", func(t *testing.T) {
		root, _, cfg, wf, _ := readyAcceptFixture(t)
		wf.DirectionTaskIDs = append(wf.DirectionTaskIDs, "missing-direction")
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err == nil {
			t.Fatal("unknown member created acceptance")
		}
	})

	t.Run("duplicate-and-stale", func(t *testing.T) {
		root, dir, cfg, wf, _ := readyAcceptFixture(t)
		if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
			t.Fatal(err)
		}
		wf, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		first := wf.AcceptanceTaskID
		oldOut := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = w
		accErr := cmdWorkflowAccept([]string{"-root", root, wf.ID})
		_ = w.Close()
		os.Stdout = oldOut
		out, _ := io.ReadAll(r)
		if accErr != nil {
			t.Fatal(accErr)
		}
		if !bytes.Contains(out, []byte("acceptance_created=0")) || !bytes.Contains(out, []byte("existing="+first)) {
			t.Fatalf("duplicate accept output: %s", out)
		}
		wf, err = loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if wf.AcceptanceTaskID != first {
			t.Fatalf("duplicate accept created %s after %s", wf.AcceptanceTaskID, first)
		}
		mustWriteFile(t, filepath.Join(dir, "internal", "auth", "extra.go"), "package auth\n")
		workflowGit(t, dir, "add", "-A")
		workflowGit(t, dir, "commit", "-q", "-m", "stale")
		wf.Candidate = readLocalSourceCandidate(dir)
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowAccept([]string{"-root", root, wf.ID, "-complete"}); err == nil {
			t.Fatal("stale/mismatched coverage reused a prior pass")
		}
		wf, err = loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if wf.GoalCompleted {
			t.Fatal("stale coverage marked the goal done")
		}
		if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
			t.Fatal(err)
		}
		wf, err = loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if wf.AcceptanceTaskID == first {
			t.Fatal("changed coverage must not reuse the prior acceptance card")
		}
	})
}

func TestTwoDirectionComposedCandidateConsumerLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the candidate CLI")
	}
	bin := strings.TrimSpace(os.Getenv("CARDEX_CANDIDATE_BIN"))
	if bin == "" {
		bin = buildCardexCLI(t)
	}
	var seen []composedLoopIDs
	for i := 0; i < 2; i++ {
		ids := runComposedCandidateLoop(t, bin, i)
		if ids.kind != "ok" || ids.alpha == "" || ids.beta == "" || ids.commit == "" || ids.tree == "" || ids.acceptance == "" {
			t.Fatalf("loop %d observables: %+v", i+1, ids)
		}
		if !strings.Contains(ids.createdFirst, "acceptance_created=1") || !strings.Contains(ids.createdAgain, "acceptance_created=0") {
			t.Fatalf("loop %d accept output first=%q again=%q", i+1, ids.createdFirst, ids.createdAgain)
		}
		seen = append(seen, ids)
	}
	if seen[0].kind != seen[1].kind {
		t.Fatalf("loop observables diverged: %+v vs %+v", seen[0], seen[1])
	}
}

type composedLoopIDs struct {
	kind         string
	alpha        string
	beta         string
	commit       string
	tree         string
	acceptance   string
	createdFirst string
	createdAgain string
}

func (c composedLoopIDs) String() string {
	return c.kind + "|" + c.alpha + "|" + c.beta + "|" + c.commit + "|" + c.tree + "|" + c.acceptance
}

func runComposedCandidateLoop(t *testing.T, bin string, round int) composedLoopIDs {
	t.Helper()
	root, dir := workflowTestRoot(t)
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module example.com/mod\n\ngo 1.22\n")
	mustWriteFile(t, filepath.Join(dir, "internal", "auth", "token.go"), "package auth\n\nfunc Token() string { return \"base\" }\n")
	mustWriteFile(t, filepath.Join(dir, "internal", "billing", "bill.go"), "package billing\n\nfunc Charge() string { return \"base\" }\n")
	workflowGit(t, dir, "add", "-A")
	workflowGit(t, dir, "commit", "-q", "-m", "module fixture")

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	initOut := run("workflow", "init",
		"-root", root, "-mode", workflowModeSerial, "-module", "auth",
		"-goal-id", "auth-token-v1", "-goal", "ship an independently reviewed auth token vertical",
		"-dir", dir, "-terminal-criteria", "independent review pass with empty p0/p1; integration and live stay held",
		"-write-domain-id", "auth-tokens", "-write-domain-lineage", "auth-tokens-lineage",
		"-write-domain-component", "auth", "-write-paths", "internal/auth,internal/billing",
		"-engine", grokBuildRunnerName, "-max-rounds", "2")
	cfg := workflowTestCfg(t, root)
	wfs, err := loadWorkflows(root, cfg)
	if err != nil || len(wfs) == 0 {
		t.Fatalf("init: %v\n%s", err, initOut)
	}
	wf := wfs[0]
	design := doneDesign(t, root, cfg, wf, dir)

	fan := run("workflow", "fanout", "-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta",
		"-direction-write-paths", "alpha=internal/auth;beta=internal/billing")
	if !strings.Contains(fan, "acceptance_created=0") {
		t.Fatalf("fanout created acceptance: %s", fan)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.DirectionTaskIDs) != 2 {
		t.Fatalf("directions=%v", wf.DirectionTaskIDs)
	}
	alphaID, betaID := wf.DirectionTaskIDs[0], wf.DirectionTaskIDs[1]

	// Manual git composition in the fixture: each direction lands a distinct
	// change, then one commit contains both and exercises the combined interface.
	mustWriteFile(t, filepath.Join(dir, "internal", "auth", "token.go"),
		"package auth\n\nfunc Token() string { return \"alpha-token\" }\n")
	mustWriteFile(t, filepath.Join(dir, "internal", "billing", "bill.go"),
		"package billing\n\nimport \"example.com/mod/internal/auth\"\n\nfunc Charge() string { return auth.Token() + \"-charged\" }\n")
	mustWriteFile(t, filepath.Join(dir, "internal", "billing", "bill_test.go"),
		"package billing\n\nimport \"testing\"\n\nfunc TestChargeUsesAuthToken(t *testing.T) {\n\tif Charge() != \"alpha-token-charged\" {\n\t\tt.Fatalf(\"got %s\", Charge())\n\t}\n}\n")
	workflowGit(t, dir, "add", "-A")
	workflowGit(t, dir, "commit", "-q", "-m", "compose alpha+beta")
	test := exec.Command("go", "test", "./internal/billing")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("composed interface test: %v\n%s", err, out)
	}
	commit, tree := workflowHeadCandidate(t, dir)
	alphaSrc, _ := os.ReadFile(filepath.Join(dir, "internal", "auth", "token.go"))
	betaSrc, _ := os.ReadFile(filepath.Join(dir, "internal", "billing", "bill.go"))
	if !bytes.Contains(alphaSrc, []byte("alpha-token")) || !bytes.Contains(betaSrc, []byte("auth.Token")) {
		t.Fatal("composed tree missing both direction changes")
	}

	for _, id := range wf.DirectionTaskIDs {
		tk, err := loadTask(root, id)
		if err != nil {
			t.Fatal(err)
		}
		tk.Status = statusDone
		if err := writeTaskFile(root, tk); err != nil {
			t.Fatal(err)
		}
	}
	freeze := run("workflow", "freeze-candidate", wf.ID, "-root", root, "-commit", commit, "-tree", tree,
		"-changed-paths", "internal/auth,internal/billing")
	_ = freeze
	first := run("workflow", "accept", "-root", root, wf.ID)
	if !strings.Contains(first, "acceptance_created=1") {
		t.Fatalf("first accept: %s", first)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Candidate == nil || wf.Candidate.Commit != commit || wf.Candidate.Tree != tree {
		t.Fatalf("candidate=%+v want %s %s", wf.Candidate, commit, tree)
	}
	accID := wf.AcceptanceTaskID
	again := run("workflow", "accept", "-root", root, wf.ID)
	if !strings.Contains(again, "acceptance_created=0") || !strings.Contains(again, "existing="+accID) {
		t.Fatalf("second accept: %s", again)
	}
	if strings.Contains(again, "goal_accepted=true") {
		t.Fatalf("duplicate accept marked the goal done: %s", again)
	}
	completeRefuse := exec.Command(bin, "workflow", "accept", "-root", root, wf.ID, "-complete")
	refuseOut, refuseErr := completeRefuse.CombinedOutput()
	if refuseErr == nil {
		t.Fatalf("unfinished acceptance marked the goal done:\n%s", refuseOut)
	}
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
	writeReviewLog(t, root, accID, verdictJSON("pass", nil, nil))
	complete := run("workflow", "accept", "-root", root, wf.ID, "-complete")
	if !strings.Contains(complete, "goal_accepted=true") {
		t.Fatalf("admissible pass did not accept: %s", complete)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wf.GoalCompleted || wf.AcceptanceTaskID != accID {
		t.Fatalf("accepted wf=%+v", wf)
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
	_ = round
	ids := composedLoopIDs{
		kind:         "ok",
		alpha:        alphaID,
		beta:         betaID,
		commit:       commit,
		tree:         tree,
		acceptance:   accID,
		createdFirst: first,
		createdAgain: again,
	}
	t.Logf("composed-loop round=%d alpha=%s beta=%s commit=%s tree=%s acceptance=%s first=%q again=%q complete=%q",
		round+1, ids.alpha, ids.beta, ids.commit, ids.tree, ids.acceptance, strings.TrimSpace(ids.createdFirst), strings.TrimSpace(ids.createdAgain), strings.TrimSpace(complete))
	return ids
}
