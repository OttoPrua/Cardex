package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecideHarvest(t *testing.T) {
	t.Parallel()
	exit0 := 0
	exit1 := 1
	cases := []struct {
		name    string
		facts   harvestFacts
		verdict string
		reason  string
	}{
		{
			name:    "read-only report is done",
			facts:   harvestFacts{ReadOnly: true, ReportNonEmpty: true, Attempts: 0, MaxAttempts: 3},
			verdict: harvestVerdictDone, reason: harvestReasonReadOnlyText,
		},
		{
			name:    "read-only final text is done",
			facts:   harvestFacts{ReadOnly: true, FinalTextNonEmpty: true, Attempts: 2, MaxAttempts: 3},
			verdict: harvestVerdictDone, reason: harvestReasonReadOnlyText,
		},
		{
			name:    "read-only silence retries",
			facts:   harvestFacts{ReadOnly: true, Attempts: 0, MaxAttempts: 3},
			verdict: harvestVerdictRetry, reason: harvestReasonNoOutput,
		},
		{
			name:    "read-only silence after last retry is attention",
			facts:   harvestFacts{ReadOnly: true, Attempts: 2, MaxAttempts: 3},
			verdict: harvestVerdictAttention, reason: harvestReasonNoOutputAfterRetry,
		},
		{
			name:    "writer verify zero is done",
			facts:   harvestFacts{HasChanges: true, VerifyConfigured: true, VerifyExit: &exit0, Attempts: 0, MaxAttempts: 3},
			verdict: harvestVerdictDone, reason: harvestReasonVerifyPassed,
		},
		{
			name:    "writer verify nonzero needs review",
			facts:   harvestFacts{HasChanges: true, VerifyConfigured: true, VerifyExit: &exit1},
			verdict: harvestVerdictNeedsReview, reason: harvestReasonVerifyFailed,
		},
		{
			name:    "writer changes without verify need review",
			facts:   harvestFacts{HasChanges: true},
			verdict: harvestVerdictNeedsReview, reason: harvestReasonUnverifiedOutput,
		},
		{
			name:    "writer commits without verify need review",
			facts:   harvestFacts{NewCommits: 2},
			verdict: harvestVerdictNeedsReview, reason: harvestReasonUnverifiedOutput,
		},
		{
			name:    "writer report without changes needs review",
			facts:   harvestFacts{ReportNonEmpty: true, Attempts: 0, MaxAttempts: 3},
			verdict: harvestVerdictNeedsReview, reason: harvestReasonReportNoChanges,
		},
		{
			name:    "writer final text without changes needs review",
			facts:   harvestFacts{FinalTextNonEmpty: true},
			verdict: harvestVerdictNeedsReview, reason: harvestReasonReportNoChanges,
		},
		{
			name:    "writer silence retries",
			facts:   harvestFacts{Attempts: 1, MaxAttempts: 3},
			verdict: harvestVerdictRetry, reason: harvestReasonNoOutput,
		},
		{
			name:    "writer silence exhausted is attention",
			facts:   harvestFacts{Attempts: 2, MaxAttempts: 3},
			verdict: harvestVerdictAttention, reason: harvestReasonNoOutputAfterRetry,
		},
		{
			name:    "read-only changes without text do not count as output",
			facts:   harvestFacts{ReadOnly: true, HasChanges: true, NewCommits: 1, Attempts: 0, MaxAttempts: 3},
			verdict: harvestVerdictRetry, reason: harvestReasonNoOutput,
		},
		{
			name:    "verify configured without output does not fail verify",
			facts:   harvestFacts{VerifyConfigured: true, Attempts: 0, MaxAttempts: 2},
			verdict: harvestVerdictRetry, reason: harvestReasonNoOutput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotV, gotR := decideHarvest(tc.facts)
			if gotV != tc.verdict || gotR != tc.reason {
				t.Fatalf("decideHarvest() = %s/%s, want %s/%s", gotV, gotR, tc.verdict, tc.reason)
			}
		})
	}
}

func TestHarvestModeNormalizes(t *testing.T) {
	t.Parallel()
	if harvestMode(nil) != harvestModeOff {
		t.Fatal("nil config")
	}
	if harvestMode(&Config{}) != harvestModeOff || harvestMode(&Config{HarvestMode: "off"}) != harvestModeOff {
		t.Fatal("off")
	}
	if harvestMode(&Config{HarvestMode: " DRY-RUN "}) != harvestModeDryRun {
		t.Fatal("dry-run")
	}
	if harvestMode(&Config{HarvestMode: "on"}) != harvestModeOn {
		t.Fatal("on")
	}
	if harvestMode(&Config{HarvestMode: "sometimes"}) != harvestModeOff {
		t.Fatal("unknown must stay off")
	}
}

func TestNativeDoneGateHarvestSkipsObservationHeuristics(t *testing.T) {
	t.Parallel()
	facts := nativeDoneFacts{
		OpenTools:           true,
		ObservationComplete: false,
		Prompt:              "write `missing.go` and add tests",
		ResultText:          "waiting for an id\nIdle turn_ended",
		WorktreeHasDiff:     false,
	}
	if got := nativeDoneHoldReasonMode(nil, facts, false); got != nativeDoneHoldOpenTools {
		t.Fatalf("off mode = %q", got)
	}
	if got := nativeDoneHoldReasonMode(nil, facts, true); got != "" {
		t.Fatalf("harvest mode on must skip observation heuristics, got %q", got)
	}
	facts.ProcessAlive = true
	if got := nativeDoneHoldReasonMode(nil, facts, true); got != nativeDoneHoldFakeExecuting {
		t.Fatalf("live process = %q", got)
	}
	facts.ProcessAlive = false
	facts.ProcessResidue = true
	if got := nativeDoneHoldReasonMode(nil, facts, true); got != nativeDoneHoldProcessResidue {
		t.Fatalf("residue = %q", got)
	}
}

type harvestScript struct {
	status     string
	numstat    string
	log        string
	diff       string
	verifyExit int
	verifyOut  string
}

func (s harvestScript) exec(_ context.Context, dir, name string, args ...string) ([]byte, int, error) {
	if name == "sh" {
		return []byte(s.verifyOut), s.verifyExit, nil
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "rev-parse"):
		return []byte("true\n"), 0, nil
	case strings.Contains(joined, "status"):
		return []byte(s.status), 0, nil
	case strings.Contains(joined, "numstat"):
		return []byte(s.numstat), 0, nil
	case strings.Contains(joined, "log"):
		return []byte(s.log), 0, nil
	case strings.Contains(joined, "diff"):
		return []byte(s.diff), 0, nil
	default:
		return nil, 1, nil
	}
}

func useHarvestScript(t *testing.T, script harvestScript, read func(string) ([]byte, error)) {
	t.Helper()
	harvestExecMu.Lock()
	prevExec, prevRead := harvestExecFn, harvestReadFn
	harvestExecFn = script.exec
	if read != nil {
		harvestReadFn = read
	}
	harvestExecMu.Unlock()
	t.Cleanup(func() {
		harvestExecMu.Lock()
		harvestExecFn, harvestReadFn = prevExec, prevRead
		harvestExecMu.Unlock()
	})
}

func TestHarvestCollectsFactsFromSeam(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cardex"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := "report says the design is ready\n"
	if err := os.WriteFile(filepath.Join(dir, ".cardex", "REPORT.md"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useHarvestScript(t, harvestScript{
		status:  "?? hello.txt\n M tracked.go\n",
		numstat: "3\t1\ttracked.go\n",
		log:     "abc1234 first\n",
	}, nil)
	task := &Task{
		ID: "card1", Type: typeSequence, Dir: dir, Verify: "",
		BaseCommit: "base", Attempts: 1, MaxAttempts: 3,
	}
	root := t.TempDir()
	ev, verdict, reason := harvestAttempt(root, &Config{MaxAttempts: 3}, task, "grok-build", &claudeResult{IsError: true, Result: "synthesized failure"}, errorsNewForTest("boom"))
	if verdict != harvestVerdictNeedsReview || reason != harvestReasonUnverifiedOutput {
		t.Fatalf("verdict %s/%s", verdict, reason)
	}
	if len(ev.ChangedFiles) != 2 || ev.Insertions != 5 || ev.Deletions != 1 {
		t.Fatalf("facts files=%v +%d -%d (untracked lines count, binary would be 0)", ev.ChangedFiles, ev.Insertions, ev.Deletions)
	}
	if len(ev.NewCommits) != 1 || ev.NewCommits[0] != "abc1234 first" {
		t.Fatalf("commits %+v", ev.NewCommits)
	}
	if ev.ReportExcerpt != strings.TrimSpace(report) || ev.FinalText != "" {
		t.Fatalf("report %q final %q; error results must not become final text", ev.ReportExcerpt, ev.FinalText)
	}
	if ev.VerifyExit != nil {
		t.Fatal("verify must not run without a configured command")
	}
	okRes := &claudeResult{Result: "all good"}
	ev, verdict, reason = harvestAttempt(root, &Config{MaxAttempts: 3}, &Task{
		ID: "card2", Type: typeReview, Dir: dir, Attempts: 0,
	}, "grok-build", okRes, nil)
	if verdict != harvestVerdictDone || reason != harvestReasonReadOnlyText || ev.FinalText != "all good" {
		t.Fatalf("read-only got %s/%s final %q", verdict, reason, ev.FinalText)
	}
}

func errorsNewForTest(msg string) error { return &harvestTestErr{msg} }

type harvestTestErr struct{ msg string }

func (e *harvestTestErr) Error() string { return e.msg }

func TestHarvestPatchWriterIncludesUntrackedText(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useHarvestScript(t, harvestScript{
		status: "?? new.txt\n",
		diff:   "diff --git a/old.go b/old.go\n+line\n",
	}, nil)
	root := t.TempDir()
	task := &Task{ID: "patchcard", Type: typeSequence, Dir: dir, Attempts: 0, BaseCommit: "HEAD"}
	ev, verdict, reason := harvestAttempt(root, &Config{MaxAttempts: 3}, task, "codex", nil, nil)
	if verdict != harvestVerdictNeedsReview || reason != harvestReasonUnverifiedOutput {
		t.Fatalf("got %s/%s", verdict, reason)
	}
	if ev.PatchPath == "" {
		t.Fatal("expected patch path")
	}
	body, err := os.ReadFile(ev.PatchPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "diff --git a/old.go b/old.go") {
		t.Fatalf("missing tracked diff: %s", text)
	}
	if !strings.Contains(text, "# cardex-harvest untracked: new.txt") || !strings.Contains(text, "alpha") {
		t.Fatalf("missing untracked section: %s", text)
	}
	if !strings.HasSuffix(ev.PatchPath, filepath.Join("harvest", "patchcard", "attempt-0.patch")) {
		t.Fatalf("patch path %s", ev.PatchPath)
	}
}

func TestHarvestVerifyRunsOnlyWithOutput(t *testing.T) {
	dir := t.TempDir()
	var shCalls int
	useHarvestScript(t, harvestScript{status: "?? a.go\n", verifyExit: 0, verifyOut: "PASS\n"}, nil)
	harvestExecMu.Lock()
	inner := harvestExecFn
	harvestExecFn = func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
		if name == "sh" {
			shCalls++
		}
		return inner(ctx, dir, name, args...)
	}
	harvestExecMu.Unlock()
	root := t.TempDir()
	ev, verdict, reason := harvestAttempt(root, &Config{MaxAttempts: 3}, &Task{
		ID: "v", Type: typeSequence, Dir: dir, Verify: "go test ./...", Attempts: 0,
	}, "grok-build", nil, nil)
	if verdict != harvestVerdictDone || reason != harvestReasonVerifyPassed {
		t.Fatalf("got %s/%s", verdict, reason)
	}
	if ev.VerifyExit == nil || *ev.VerifyExit != 0 || !strings.Contains(ev.VerifyTail, "PASS") {
		t.Fatalf("verify evidence %+v", ev)
	}
	if shCalls != 1 {
		t.Fatalf("sh calls %d", shCalls)
	}
	shCalls = 0
	_, verdict, reason = harvestAttempt(root, &Config{MaxAttempts: 3}, &Task{
		ID: "silent", Type: typeSequence, Dir: dir, Verify: "go test ./...", Attempts: 0,
	}, "grok-build", nil, nil)
	// status porcelain still reports a change because the script is shared.
	// A second script with empty status must not run verify.
	useHarvestScript(t, harvestScript{status: "", verifyExit: 7}, nil)
	_, verdict, reason = harvestAttempt(root, &Config{MaxAttempts: 3}, &Task{
		ID: "silent2", Type: typeSequence, Dir: dir, Verify: "go test ./...", Attempts: 0, MaxAttempts: 3,
	}, "grok-build", nil, nil)
	if verdict != harvestVerdictRetry || reason != harvestReasonNoOutput {
		t.Fatalf("no output got %s/%s", verdict, reason)
	}
}

func TestGrokLifecycleProbePermissionContinues(t *testing.T) {
	bin, productCalls := fakeGrokBuildCounted(t, `{"type":"end","stopReason":"end_turn"}`, "", 0)
	cfg := grokBuildTestConfig(t, bin)
	stateDir := filepath.Join(cfg.grokLifecycleHome, ".grok")
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
	task := &Task{ID: uniqueTaskID("grok-probe-perm"), Type: typeSequence, Dir: t.TempDir(), PreferRunner: grokBuildRunnerName}
	root := admitDirectInvoke(t, "", task)
	task.LastProviderPreflight = &ProviderPreflightReadback{Runner: grokBuildRunnerName, State: providerReady}
	if _, _, err := invokeGrokBuild(context.Background(), root, cfg, task, "harmless prompt"); err != nil {
		t.Fatalf("permission-class probe failure must not fail the attempt: %v", err)
	}
	if n := countProductCalls(t, productCalls); n != 1 {
		t.Fatalf("provider calls=%d", n)
	}
}

func TestRunTaskGrokHarvestOnNeedsReviewWhenWorktreeChanged(t *testing.T) {
	root := testRoot(t)
	bin, _ := fakeGrokBuildCounted(t, grokTerminalSemanticMissingEndPayload(), "", 1)
	cfg := policyTestConfig()
	cfg.GrokBuildBin = bin
	cfg.MaxAttempts = 3
	cfg.HarvestMode = harvestModeOn
	isolateGrokLifecycleHome(t, cfg)
	work := t.TempDir()
	useHarvestScript(t, harvestScript{status: "?? hello.txt\n", diff: "diff --git a/hello.txt b/hello.txt\n"}, nil)
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := newTask(root, cfg, typeSequence, "harvest needs review", work, []string{"implement"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	task.GrokModel = "grok-4.6"
	task.GrokEffort = "xhigh"
	task.ReviewAfter = false
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusHeld {
		t.Fatalf("status %s last %q", got.Status, got.LastError)
	}
	if got.Verdict != harvestVerdictNeedsReview {
		t.Fatalf("verdict %q", got.Verdict)
	}
	if got.Harvest == nil || len(got.Harvest.ChangedFiles) == 0 {
		t.Fatalf("harvest %+v", got.Harvest)
	}
	if !strings.HasPrefix(got.LastError, "harvest needs_review") {
		t.Fatalf("last error %q", got.LastError)
	}
	if got.Attempts != 0 {
		t.Fatalf("attempts changed: %d", got.Attempts)
	}
}

func TestRunTaskGrokHarvestOnVerifyPassIsDone(t *testing.T) {
	root := testRoot(t)
	bin, _ := fakeGrokBuildCounted(t, grokTerminalSemanticMissingEndPayload(), "", 1)
	cfg := policyTestConfig()
	cfg.GrokBuildBin = bin
	cfg.MaxAttempts = 3
	cfg.HarvestMode = harvestModeOn
	isolateGrokLifecycleHome(t, cfg)
	work := t.TempDir()
	useHarvestScript(t, harvestScript{status: "?? hello.txt\n", verifyExit: 0, verifyOut: "ok\n"}, nil)
	task := newTask(root, cfg, typeSequence, "harvest verify", work, []string{"implement"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	task.GrokModel = "grok-4.6"
	task.GrokEffort = "xhigh"
	task.ReviewAfter = false
	task.Verify = "true"
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusDone {
		t.Fatalf("status %s last %q verdict %q", got.Status, got.LastError, got.Verdict)
	}
	if got.Verdict != harvestVerdictDone || got.Harvest == nil || got.Harvest.Reason != harvestReasonVerifyPassed {
		t.Fatalf("harvest %+v verdict %q", got.Harvest, got.Verdict)
	}
}

func TestRunTaskGrokHarvestOffKeepsUnknownOutcomeHold(t *testing.T) {
	root := testRoot(t)
	bin, _ := fakeGrokBuildCounted(t, grokTerminalSemanticMissingEndPayload(), "", 1)
	cfg := policyTestConfig()
	cfg.GrokBuildBin = bin
	cfg.MaxAttempts = 3
	cfg.HarvestMode = harvestModeOff
	isolateGrokLifecycleHome(t, cfg)
	task := newTask(root, cfg, typeSequence, "harvest off", t.TempDir(), []string{"implement"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	task.GrokModel = "grok-4.6"
	task.GrokEffort = "xhigh"
	task.ReviewAfter = false
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusHeld || !strings.Contains(got.LastError, "Grok terminal unknown outcome held") {
		t.Fatalf("status %s last %q", got.Status, got.LastError)
	}
	if got.Verdict != "" || got.Harvest != nil {
		t.Fatalf("off mode must not store harvest: %+v %q", got.Harvest, got.Verdict)
	}
}

func TestGitRealCodexWritableRootsLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	mainRepo := filepath.Join(base, "main")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(mainRepo, "init")
	git(mainRepo, "config", "user.email", "cardex@example.com")
	git(mainRepo, "config", "user.name", "cardex")
	if err := os.WriteFile(filepath.Join(mainRepo, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(mainRepo, "add", "f.txt")
	git(mainRepo, "commit", "-m", "init")
	wt := filepath.Join(base, "wt")
	git(mainRepo, "worktree", "add", "-b", "side", wt)
	roots := codexWritableRoots(wt)
	if len(roots) < 3 {
		t.Fatalf("linked worktree should expose .git file, git-dir, and common dir: %v", roots)
	}
	if !sameGitPath(roots[0], filepath.Join(wt, ".git")) {
		t.Fatalf("first root should stay <dir>/.git, got %v", roots)
	}
	var common, gitDir bool
	for _, r := range roots[1:] {
		if sameGitPath(r, filepath.Join(mainRepo, ".git")) {
			common = true
		}
		if strings.Contains(filepath.ToSlash(r), "/worktrees/") {
			gitDir = true
		}
	}
	if !common || !gitDir {
		t.Fatalf("roots %v", roots)
	}
	arg := codexWritableRootsArg(wt)
	if !strings.Contains(arg, `sandbox_workspace_write.writable_roots=`) || !strings.Contains(arg, filepath.Join(wt, ".git")) {
		t.Fatalf("arg %s", arg)
	}
}
