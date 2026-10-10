package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func r06SuccessPayload() string {
	return `{"type":"text","data":"GROK_OK"}` + "\n" + grok105PublicEnd
}

func r06QuotaPartialPayload() string {
	return grok105PublicUsage + "\n" + `{"type":"error","message":"HTTP 429: usage limit reached"}`
}

func writeGrokCLIRoot(t *testing.T, payload, stderr string, exitCode int) (root, work, productCalls string) {
	t.Helper()
	grok, productCalls := fakeGrokBuildCounted(t, payload, stderr, exitCode)
	root = t.TempDir()
	work = t.TempDir()
	for _, d := range []string{"tasks", "archive", "logs", "events", "templates"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := grokBuildTestConfig(t, grok)
	cfg.GrokBuild.OpusAdversarialReview = false
	cfg.HarvestMode = harvestModeOff
	cfg.OwnerRoutingEnforced = false
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	return root, work, productCalls
}

func addGrokCLITask(t *testing.T, bin, root, work, title string, verify bool) string {
	t.Helper()
	args := []string{"add", "-root", root, "-runner", grokBuildRunnerName,
		"-grok-model", "grok-4.6", "-grok-effort", "xhigh",
		"-dir", work, "-title", title, "-review-after=false"}
	if verify {
		args = append(args, "-verify", "true", "-verify-on-success")
	}
	args = append(args, "review only the current synthetic candidate")
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("cardex add: %v\n%s", err, out)
	}
	return onlyTaskID(t, root)
}

func r06AssertNoFakeSuccess(t *testing.T, got *Task) {
	t.Helper()
	if got.Status == statusDone {
		t.Fatalf("must not imply fake success: %+v", got)
	}
	if got.CodexModel != "" || got.FallbackReason != "" {
		t.Fatalf("must not queue a fallback writer: %+v", got)
	}
}

func TestR06GrokCommonOutcomeConsumer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake Grok CLI consumer")
	}
	bin := buildCardexCLI(t)
	t.Run("dispatch-verify-terminal", func(t *testing.T) {
		root, work, productCalls := writeGrokCLIRoot(t, r06SuccessPayload(), "", 0)
		id := addGrokCLITask(t, bin, root, work, "r06-cli-happy", true)
		run1, err1 := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
		if err1 != nil {
			t.Fatalf("first run: %v\n%s", err1, run1)
		}
		got := mustLoadTaskAnywhere(t, root, id)
		if got.Status != statusDone {
			t.Fatalf("first terminal status=%s last=%q\n%s", got.Status, got.LastError, run1)
		}
		if got.Harvest == nil || got.Harvest.VerifyExit == nil || *got.Harvest.VerifyExit != 0 {
			t.Fatalf("required verification missing: %+v\n%s", got.Harvest, run1)
		}
		if got.RawUsage == nil || got.RawUsage.Last == nil || got.RawUsage.Last.Fields.InputTokens == nil ||
			*got.RawUsage.Last.Fields.InputTokens != 34 {
			t.Fatalf("persisted usage missing: %+v", got.RawUsage)
		}
		events, _, err := loadTaskEvents(root, id)
		if err != nil {
			t.Fatal(err)
		}
		assertEventTypes(t, root, id, true, evDispatched, evDone)
		if countProductCalls(t, productCalls) != 1 {
			t.Fatalf("first run product calls=%d events=%v", countProductCalls(t, productCalls), eventTypeList(events))
		}
		run2, _ := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
		got2 := mustLoadTaskAnywhere(t, root, id)
		if got2.Status != statusDone {
			t.Fatalf("replay changed terminal status=%s last=%q\n%s", got2.Status, got2.LastError, run2)
		}
		if countProductCalls(t, productCalls) != 1 {
			t.Fatalf("second observation started another provider call: %d\n%s", countProductCalls(t, productCalls), run2)
		}
		if dump := strings.TrimSpace(os.Getenv("R06_CLI_DUMP")); dump != "" {
			if err := os.MkdirAll(dump, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dump, "run1.out"), run1, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dump, "run2.out"), run2, 0o644); err != nil {
				t.Fatal(err)
			}
			taskPath := taskPath(root, id)
			if _, err := os.Stat(taskPath); err != nil {
				taskPath = filepath.Join(archiveDir(root), id+".json")
			}
			taskRaw, err := os.ReadFile(taskPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dump, "task.json"), taskRaw, 0o644); err != nil {
				t.Fatal(err)
			}
			evRaw, err := os.ReadFile(eventsPath(root, id))
			if err != nil {
				evRaw, err = os.ReadFile(filepath.Join(archivedEventsDir(root), id+".jsonl"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dump, "events.jsonl"), evRaw, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dump, "product.calls"), []byte(strconv.Itoa(countProductCalls(t, productCalls))+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestR06ProcessTruthParity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake Grok process fixtures")
	}
	t.Run("nonzero-exit", func(t *testing.T) {
		root, got, productCalls := cprocRunHeld(t, "connection reset by peer\ncontext "+cprocBearerSecret, 7, false)
		cprocAssertTruthfulHold(t, root, got, productCalls, grokBuildProcessClassTransport, "7")
		r06AssertNoFakeSuccess(t, got)
		cprocAssertRootFree(t, root, cprocBearerSecret, "connection reset by peer")
	})
	t.Run("missing-terminal", func(t *testing.T) {
		root := testRoot(t)
		bin, productCalls := fakeGrokBuildCounted(t, grokTerminalSemanticMissingEndPayload(), "", 1)
		cfg := policyTestConfig()
		cfg.GrokBuildBin = bin
		cfg.MaxAttempts = 3
		isolateGrokLifecycleHome(t, cfg)
		task := ownerBackendGrokTask(t, root, cfg, t.TempDir())
		if err := saveTask(root, task); err != nil {
			t.Fatal(err)
		}
		if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
			t.Fatal(err)
		}
		assertGrokTerminalUnknownHeld(t, root, task, productCalls, 0, fallbackStreamIncomplete, 1, 1, 0, true)
		got := loadMust(t, root, task.ID)
		r06AssertNoFakeSuccess(t, got)
	})
	t.Run("cancel-after-output", func(t *testing.T) {
		root := testRoot(t)
		dir := t.TempDir()
		bin := filepath.Join(dir, "grok")
		productCalls := filepath.Join(dir, "product.calls")
		started := filepath.Join(dir, "started")
		payloadPath := filepath.Join(dir, "stdout.jsonl")
		if err := os.WriteFile(payloadPath, []byte(r06SuccessPayload()), 0o644); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\n" +
			"for arg in \"$@\"; do\n" +
			"  if [ \"$arg\" = 'models' ]; then\n" +
			"    printf '%s\\n' 'You are logged in with grok.com.' 'Available models:' '  * grok-4.6 (default)'\n" +
			"    exit 0\n" +
			"  fi\n" +
			"done\n" +
			"printf 'product\\n' >> " + shSingleQuote(productCalls) + "\n" +
			"cat " + shSingleQuote(payloadPath) + "\n" +
			"printf started > " + shSingleQuote(started) + "\n" +
			"exec sleep 20\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := grokBuildTestConfig(t, bin)
		cfg.GrokBuild.OpusAdversarialReview = false
		cfg.HarvestMode = harvestModeOff
		cfg.grokStepTimeout = 8 * time.Second
		isolateGrokLifecycleHome(t, cfg)
		task := newTask(root, cfg, typeSequence, "r06 cancel after output", t.TempDir(), []string{"review only the current synthetic candidate"}, 1)
		task.PreferRunner = grokBuildRunnerName
		task.RunnerExplicit = true
		task.GrokModel = "grok-4.6"
		task.GrokEffort = "xhigh"
		task.ReviewAfter = false
		if err := saveTask(root, task); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- runTaskVia(ctx, root, cfg, task, grokBuildRunnerName)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(started); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("provider never wrote stdout before cancel")
			}
			time.Sleep(20 * time.Millisecond)
		}
		fresh, err := loadTask(root, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		fresh.Status = statusCanceled
		if err := saveTask(root, fresh); err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case runErr := <-done:
			if runErr != nil {
				t.Fatalf("runTaskVia: %v", runErr)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("runTaskVia did not return after cancel")
		}
		got := mustLoadTaskAnywhere(t, root, task.ID)
		if got.Status == statusDone {
			t.Fatalf("cancel-after-output completed: %+v", got)
		}
		if got.Status != statusCanceled && got.Status != statusHeld {
			t.Fatalf("cancel-after-output status=%s last=%q", got.Status, got.LastError)
		}
		r06AssertNoFakeSuccess(t, got)
		if countProductCalls(t, productCalls) != 1 {
			t.Fatalf("product calls=%d", countProductCalls(t, productCalls))
		}
	})
	t.Run("timeout", func(t *testing.T) {
		root := testRoot(t)
		bin, productCalls := fakeGrokBuildCounted(t, `{"type":"system.version","version":"1.0.5"}`, "", 0)
		script, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		script = []byte(strings.TrimSuffix(string(script), "exit 0\n") + "exec sleep 20\n")
		if err := os.WriteFile(bin, script, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := grokBuildTestConfig(t, bin)
		cfg.GrokBuild.OpusAdversarialReview = false
		cfg.HarvestMode = harvestModeOff
		cfg.grokStepTimeout = 2 * time.Second
		isolateGrokLifecycleHome(t, cfg)
		task := newTask(root, cfg, typeSequence, "r06 timeout", t.TempDir(), []string{"review only the current synthetic candidate"}, 1)
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
		got := loadMust(t, root, task.ID)
		r06AssertNoFakeSuccess(t, got)
		if got.Attempts != 0 && got.Status == statusDone {
			t.Fatalf("timeout must not complete: %+v", got)
		}
		if countProductCalls(t, productCalls) != 1 {
			t.Fatalf("product calls=%d", countProductCalls(t, productCalls))
		}
	})
	t.Run("empty-complete-end-turn", func(t *testing.T) {
		root := testRoot(t)
		payload := strings.Replace(grok105PublicEnd, `"num_turns":3`, `"num_turns":0`, 1)
		bin, productCalls := fakeGrokBuildCounted(t, payload, "", 0)
		cfg := grokBuildTestConfig(t, bin)
		cfg.GrokBuild.OpusAdversarialReview = false
		cfg.HarvestMode = harvestModeOff
		cfg.OwnerRoutingEnforced = false
		isolateGrokLifecycleHome(t, cfg)
		task := newTask(root, cfg, typeSequence, "r06 empty complete end_turn", t.TempDir(), []string{"review only the current synthetic candidate"}, 1)
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
		got := loadMust(t, root, task.ID)
		if got.Status == statusDone {
			t.Fatalf("complete 0/0/0 empty end_turn must not persist done: %+v", got)
		}
		r06AssertNoFakeSuccess(t, got)
		if got.Attempts != 0 {
			t.Fatalf("empty complete end_turn consumed attempts: %+v", got)
		}
		if countProductCalls(t, productCalls) != 1 {
			t.Fatalf("product calls=%d", countProductCalls(t, productCalls))
		}
	})
	t.Run("residue-blocks-fallback", func(t *testing.T) {
		proof := policyFallbackProof{
			Before: &policyWorkspaceFingerprint{Root: "/r", Digest: "a"},
			After:  &policyWorkspaceFingerprint{Root: "/r", Digest: "a"},
			ObservationComplete: true, ProcessResidue: true,
		}
		if _, err := authorizePolicyFallback(proof); err == nil {
			t.Fatal("residue must not authorize fallback")
		}
		root, got, productCalls := cprocRunHeld(t, "connection refused\ncontext "+cprocAPIKeySecret, 1, false)
		cprocAssertTruthfulHold(t, root, got, productCalls, grokBuildProcessClassTransport, "1")
		r06AssertNoFakeSuccess(t, got)
	})
}

func TestR06PartialWorkRetained(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake Grok quota fixture")
	}
	root := testRoot(t)
	work := t.TempDir()
	source := filepath.Join(work, "partial-source.go")
	if err := os.WriteFile(source, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, productCalls := fakeGrokBuildCounted(t, r06QuotaPartialPayload(), "HTTP 429: usage limit reached", 1)
	cfg := grokBuildTestConfig(t, bin)
	cfg.GrokBuild.OpusAdversarialReview = false
	cfg.HarvestMode = harvestModeOff
	cfg.OwnerRoutingEnforced = false
	isolateGrokLifecycleHome(t, cfg)
	task := newTask(root, cfg, typeSequence, "r06 partial quota", work, []string{"review only the current synthetic candidate"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	task.GrokModel = "grok-4.6"
	task.GrokEffort = "xhigh"
	task.ReviewAfter = false
	task.Verify = "test -f partial-source.go"
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	got := loadMust(t, root, task.ID)
	r06AssertNoFakeSuccess(t, got)
	if got.Status != statusLimitPaused && got.Status != statusQueued && got.Status != statusHeld {
		t.Fatalf("quota/budget stop status=%s last=%q", got.Status, got.LastError)
	}
	body, err := os.ReadFile(source)
	if err != nil || string(body) != "package p\n" {
		t.Fatalf("source must survive quota stop: %s %v", body, err)
	}
	if got.Verify != "test -f partial-source.go" {
		t.Fatalf("verify check lost: %q", got.Verify)
	}
	if got.RawUsage == nil || got.RawUsage.Last == nil || got.RawUsage.Last.Fields.InputTokens == nil ||
		*got.RawUsage.Last.Fields.InputTokens != 21 {
		t.Fatalf("usage must survive quota stop: %+v", got.RawUsage)
	}
	if countProductCalls(t, productCalls) != 1 {
		t.Fatalf("product calls=%d", countProductCalls(t, productCalls))
	}
	if got.PreferRunner != grokBuildRunnerName {
		t.Fatalf("legal continuation lost the Grok pin: %+v", got)
	}
	if got.Status == statusLimitPaused && got.ResumeAtEpoch == 0 {
		t.Fatal("limit_paused continuation missing resume_at")
	}
}

func TestR06NoDuplicateBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake Grok counted consumer")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	dir := filepath.Dir(thisFile)
	runnerSrc, err := os.ReadFile(filepath.Join(dir, "runner.go"))
	if err != nil {
		t.Fatal(err)
	}
	runner := string(runnerSrc)
	deleted := []string{
		"GROK_PROCESS_HELD",
		"GROK_TERMINAL_HELD",
		"grok_zero_event_process_exit_held",
		"grok_terminal_unknown_outcome_held",
		"grok_build_auth_circuit_open",
		"1g) Grok Build",
		"grokBuildZeroEventProcessFailure(res, runErr)",
		"grokTerminalUnknownOutcome(res)",
	}
	for _, needle := range deleted {
		if strings.Contains(runner, needle) {
			t.Fatalf("deleted Grok result-judgment branch still live in runner.go: %s", needle)
		}
	}
	if !strings.Contains(runner, "applyGrokExecutionDecision") || !strings.Contains(runner, "applyGrokDeferredDisposition") {
		t.Fatal("replacement consumer applyGrokExecutionDecision/applyGrokDeferredDisposition missing from runner.go")
	}
	grokSrc, err := os.ReadFile(filepath.Join(dir, "grok.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(grokSrc), "func applyGrokExecutionDecision") ||
		!strings.Contains(string(grokSrc), "decideExecutionOutcome") ||
		!strings.Contains(string(grokSrc), "applyFailureDisposition") {
		t.Fatal("Grok adapter must map into decideExecutionOutcome and existing persist writers")
	}
	goalSrc, err := os.ReadFile(filepath.Join(dir, "workflow_goal.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(goalSrc), "func executionObservationFromNativeGoal") {
		t.Fatal("hosted Goal must keep the original native mapping; unused executionObservationFromNativeGoal must stay deleted")
	}
	if !strings.Contains(string(goalSrc), "func mapNativeGoalToTask") {
		t.Fatal("hosted Goal native state machine mapping must remain")
	}

	root := testRoot(t)
	bin, productCalls := fakeGrokBuildCounted(t, r06SuccessPayload(), "", 0)
	cfg := grokBuildTestConfig(t, bin)
	cfg.GrokBuild.OpusAdversarialReview = false
	cfg.HarvestMode = harvestModeOff
	isolateGrokLifecycleHome(t, cfg)
	task := newTask(root, cfg, typeSequence, "r06 one provider", t.TempDir(), []string{"review only the current synthetic candidate"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	task.GrokModel = "grok-4.6"
	task.GrokEffort = "xhigh"
	task.ReviewAfter = false
	task.Verify = "true"
	task.VerifyOnSuccess = true
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	got := loadMust(t, root, task.ID)
	if got.Status != statusDone {
		t.Fatalf("new card status=%s last=%q", got.Status, got.LastError)
	}
	if n := countProductCalls(t, productCalls); n != 1 {
		t.Fatalf("new card started %d provider entries, want 1", n)
	}
}

func r06PinnedGrokRun(t *testing.T, payload, stderr string, exitCode int) (got *Task, calls int) {
	t.Helper()
	root := testRoot(t)
	bin, productCalls := fakeGrokBuildCounted(t, payload, stderr, exitCode)
	cfg := grokBuildTestConfig(t, bin)
	cfg.GrokBuild.OpusAdversarialReview = false
	cfg.HarvestMode = harvestModeOff
	cfg.OwnerRoutingEnforced = false
	cfg.MaxAttempts = 3
	isolateGrokLifecycleHome(t, cfg)
	task := newTask(root, cfg, typeSequence, "r06 pinned grok", t.TempDir(), []string{"synthetic read-only review"}, 1)
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
	return loadMust(t, root, task.ID), countProductCalls(t, productCalls)
}

func TestR06UnknownDecisionPersistsHold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake Grok process fixtures")
	}
	got, calls := r06PinnedGrokRun(t, `{"type":"error","message":"context deadline exceeded"}`, "", 1)
	r06AssertNoFakeSuccess(t, got)
	if got.Status != statusHeld || got.Attempts != 0 {
		t.Fatalf("unknown no-terminal result must remain held without automatic retry: status=%s attempts=%d calls=%d error=%s",
			got.Status, got.Attempts, calls, got.LastError)
	}
	if calls != 1 {
		t.Fatalf("product calls=%d", calls)
	}
}

func TestR06TerminalFailureClassification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake Grok process fixtures")
	}
	for _, tc := range []struct {
		name, message, want string
	}{
		{"input_limit", "prompt is too long", statusFailed},
		{"permission", "permission denied", statusHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"type":"error","message":"` + tc.message + `"}` + "\n" + grok105PublicEnd
			got, calls := r06PinnedGrokRun(t, payload, "", 1)
			r06AssertNoFakeSuccess(t, got)
			if got.Status != tc.want {
				t.Fatalf("terminal failure classification: status=%s want=%s attempts=%d calls=%d error=%s",
					got.Status, tc.want, got.Attempts, calls, got.LastError)
			}
			if got.Attempts != 0 {
				t.Fatalf("terminal class must not consume attempts: %+v", got)
			}
			if calls != 1 {
				t.Fatalf("product calls=%d", calls)
			}
		})
	}
}
