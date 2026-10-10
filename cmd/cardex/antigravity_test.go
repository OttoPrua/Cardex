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
)

const (
	agySavedEmptySuccessDenied = `{"conversation_id":"00e9d3ad-5d49-4105-9538-dded61e5c758","status":"SUCCESS","response":"","duration_seconds":3.817977,"num_turns":1,"usage":{"input_tokens":7729,"output_tokens":253,"thinking_tokens":0,"cache_read_tokens":13084,"total_tokens":7982},"denied_actions":[{"action":"command","display_name":"RunCommand"}]}`
	agySecretSentinel          = "SECRET_SENTINEL_agy_r01_leak"
	agyTestModel               = "claude-opus-4-6-thinking"
)

func TestAntigravityThinkingModelOmitsEffort(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig("")
	cfg.Antigravity = &AntigravityRoute{Enabled: true, Effort: "high"}
	argv := antigravityArgs(cfg, &Task{Type: typeSequence}, "claude-opus-4-6-thinking", "harmless")
	args := "\n" + strings.Join(argv, "\n") + "\n"
	if strings.Contains(args, "\n--effort\n") {
		t.Fatalf("thinking-encoded model must not receive --effort: %v", argv)
	}
	if !strings.Contains(args, "\n--model\nclaude-opus-4-6-thinking\n") {
		t.Fatalf("actual advertised Opus route missing from argv: %v", argv)
	}
}

func TestParseAntigravityJSONFailsClosedWithoutTerminal(t *testing.T) {
	t.Parallel()
	missing := parseAntigravityJSON([]byte(`{}`))
	if missing == nil || !missing.IsError || !missing.ObservationComplete {
		t.Fatalf("valid JSON without terminal must be a complete fail-closed observation: %+v", missing)
	}
	if missing.Subtype != antigravitySubtypeUnknownTerminal {
		t.Fatalf("cover-all missing_terminal must not swallow unknown: %+v", missing)
	}
	invalid := parseAntigravityJSON([]byte(`not-json`))
	if invalid == nil || !invalid.IsError || invalid.ObservationComplete {
		t.Fatalf("invalid JSON must remain an incomplete fail-closed observation: %+v", invalid)
	}
}

func TestEnqueueEmittedRunnerAntigravity(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	cfg := defaultConfig("")
	cfg.AntigravityBin = "agy"
	cfg.Antigravity = &AntigravityRoute{Enabled: true, Effort: "high"}
	parent := newTask(root, cfg, typeCoordinate, "父", t.TempDir(), []string{"p"}, 1)
	result := "```json\n{\"tasks\":[{\"title\":\"填充\",\"type\":\"sequence\",\"runner\":\"agy\",\"agy_model\":\"claude-opus-4-6-thinking\",\"prompts\":[\"做事\"]}]}\n```"
	ids, err := enqueueEmitted(root, cfg, parent, result)
	if err != nil || len(ids) != 1 {
		t.Fatalf("emit 应入队 1 张 agy 卡: ids=%v err=%v", ids, err)
	}
	nt, err := loadTask(root, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if nt.PreferRunner != antigravityRunnerName || nt.AgyModel != "claude-opus-4-6-thinking" || !nt.RunnerExplicit {
		t.Fatalf("emit 的 agy runner/model 应随卡: %+v", nt)
	}
}

func TestR01EmptySuccessWithDeniedAction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake AGY provider")
	}
	data, err := os.ReadFile(filepath.Join("testdata", "agy_print_empty_success_denied.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != agySavedEmptySuccessDenied {
		t.Fatalf("testdata drifted from the frozen print payload")
	}
	parsed := parseAntigravityJSON([]byte(agySavedEmptySuccessDenied))
	if parsed == nil || !parsed.IsError || parsed.Subtype != antigravitySubtypeNeedsInteraction {
		t.Fatalf("empty SUCCESS+denied must need interaction, got %+v", parsed)
	}
	if parsed.SessionID != "00e9d3ad-5d49-4105-9538-dded61e5c758" {
		t.Fatalf("conversation_id missing: %+v", parsed)
	}
	root, cfg, task, _ := setupAntigravityRun(t, agySavedEmptySuccessDenied, "", 0, false)
	before := task.Attempts
	got := runAntigravityTask(t, root, cfg, task)
	if got.Status == statusDone {
		t.Fatalf("empty SUCCESS+denied must not complete: %+v", got)
	}
	if got.Status != statusHeld {
		t.Fatalf("status=%s last=%q", got.Status, got.LastError)
	}
	if got.Attempts != before {
		t.Fatalf("must not consume a new attempt: before=%d after=%d", before, got.Attempts)
	}
	if !strings.Contains(got.LastError, "交互") && !strings.Contains(got.LastError, "needs_interaction") {
		t.Fatalf("interaction reason missing: %q", got.LastError)
	}
}

func TestR01TerminalPrecedence(t *testing.T) {
	t.Parallel()
	bodyError := parseAntigravityJSON([]byte(`{"status":"SUCCESS","response":"looks done","error":"quota exceeded"}`))
	if bodyError == nil || !bodyError.IsError || bodyError.Subtype != antigravitySubtypeQuota {
		t.Fatalf("body+error must not be success: %+v", bodyError)
	}
	bodyDenied := parseAntigravityJSON([]byte(`{"response":"partial","denied_actions":[{"action":"command","display_name":"RunCommand"}]}`))
	if bodyDenied == nil || !bodyDenied.IsError {
		t.Fatalf("body+denied without SUCCESS terminal must not be success: %+v", bodyDenied)
	}
	truncated := parseAntigravityJSON([]byte(`{"status":"SUCCESS","response":"hi"`))
	if truncated == nil || !truncated.IsError || truncated.ObservationComplete {
		t.Fatalf("truncated JSON must not be success: %+v", truncated)
	}
	assistant := parseAntigravityJSON([]byte("{\"role\":\"assistant\",\"content\":\"done\"}\n{\"role\":\"assistant\",\"text\":\"also done\"}\n"))
	if assistant == nil || !assistant.IsError {
		t.Fatalf("last assistant without terminal must not be success: %+v", assistant)
	}
	ok := parseAntigravityJSON([]byte(`{"status":"SUCCESS","response":"exact-success-body","conversation_id":"sess-ok","num_turns":1,"usage":{"input_tokens":3,"output_tokens":2}}`))
	if ok == nil || ok.IsError || ok.Result != "exact-success-body" || ok.TerminalEvents != 1 {
		t.Fatalf("exact success structure must enter result handling: %+v", ok)
	}
	attachAntigravityInvokeFacts(ok, &Task{Type: typeSequence},
		"no output produced because headless mode cannot prompt for command permission and soft-denied it", nil)
	obs := observationFromAntigravity(&Task{Type: typeSequence, Dir: t.TempDir()}, ok, nil)
	if obs.NeedsInteraction || !obs.SuccessStructure {
		t.Fatalf("stderr cannot-prompt must not override a SUCCESS body: %+v diag=%+v", obs, ok.AntigravityDiagnostics)
	}
	unknown := parseAntigravityJSON([]byte(`{}`))
	attachAntigravityInvokeFacts(unknown, &Task{Type: typeSequence}, "", nil)
	uobs := observationFromAntigravity(&Task{Type: typeSequence, Dir: t.TempDir()}, unknown, nil)
	udec := decideExecutionOutcome(uobs, executionAttemptFacts{Attempts: 0, MaxAttempts: 3})
	if udec.Kind != executionDecisionUnknown || strings.Contains(udec.Reason, "no_input") {
		t.Fatalf("unknown terminal must not be reported as capability: obs=%+v dec=%+v", uobs, udec)
	}
	if runtime.GOOS == "windows" {
		return
	}
	root, cfg, task, _ := setupAntigravityRun(t, `{"status":"SUCCESS","response":"exact-success-body","conversation_id":"sess-ok","num_turns":1,"usage":{"input_tokens":3,"output_tokens":2}}`, "", 0, true)
	got := runAntigravityTask(t, root, cfg, task)
	if got.Status != statusDone {
		t.Fatalf("exact success must complete via existing result/verify path: status=%s last=%q", got.Status, got.LastError)
	}
	if got.SessionID != "sess-ok" {
		t.Fatalf("session_id=%q", got.SessionID)
	}
}

func TestR01ArgumentsRespectMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake AGY provider")
	}
	t.Parallel()
	payload := `{"status":"SUCCESS","response":"argv-ok"}`
	bin, _, argsDump := fakeAntigravityCounted(t, payload, "", 0)
	cfg := antigravityTestConfig(t, bin)
	planTask := readyAntigravityTask(t, typeReview, agyTestModel)
	root := admitDirectInvoke(t, "", planTask)
	res, _, err := invokeAntigravity(context.Background(), cfg, planTask, "harmless plan prompt")
	if err != nil && res == nil {
		t.Fatalf("plan invoke: %v", err)
	}
	argv := readArgvDump(t, argsDump)
	if !strings.Contains(argv, "--mode\nplan\n") {
		t.Fatalf("plan argv missing --mode plan:\n%s", argv)
	}
	if strings.Contains(argv, "--disable-slash-commands") {
		t.Fatalf("plan must not pair with --disable-slash-commands:\n%s", argv)
	}
	if strings.Contains(argv, "只读") || strings.Contains(strings.ToLower(argv), "read-only") {
		t.Fatalf("argv must not claim read-only in prompt text:\n%s", argv)
	}
	if res.AntigravityDiagnostics == nil || res.AntigravityDiagnostics.CapabilityReason != antigravityCapabilityUnknownVer {
		t.Fatalf("unknown version must be a concrete reason, got %+v", res.AntigravityDiagnostics)
	}

	seqTask := readyAntigravityTask(t, typeSequence, agyTestModel)
	admitDirectInvoke(t, root, seqTask)
	res2, _, err := invokeAntigravity(context.Background(), cfg, seqTask, "harmless seq prompt")
	if err != nil && res2 == nil {
		t.Fatalf("seq invoke: %v", err)
	}
	argv2 := readArgvDump(t, argsDump)
	if !strings.Contains(argv2, "--mode\naccept-edits\n") {
		t.Fatalf("sequence argv missing accept-edits:\n%s", argv2)
	}
	if strings.Contains(argv2, "--mode\nplan\n") && strings.Contains(argv2, "--disable-slash-commands") {
		t.Fatalf("plan must not combine with disabling argv:\n%s", argv2)
	}
	if res2.AntigravityDiagnostics == nil || res2.AntigravityDiagnostics.CapabilityReason != antigravityCapabilityNoInput {
		t.Fatalf("headless without input capability must be concrete, got %+v", res2.AntigravityDiagnostics)
	}
}

func TestR01DiagnosticProjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake AGY provider")
	}
	root, cfg, task, _ := setupAntigravityRun(t, agySavedEmptySuccessDenied, agySecretSentinel, 0, false)
	got := runAntigravityTask(t, root, cfg, task)
	assertNoSecretLeak(t, root, got, agySecretSentinel)
	if got.SessionID != "00e9d3ad-5d49-4105-9538-dded61e5c758" {
		t.Fatalf("conversation_id must be recorded when present: %q", got.SessionID)
	}
	if got.RawUsage == nil || got.RawUsage.Last == nil || got.RawUsage.Last.Fields.InputTokens == nil || *got.RawUsage.Last.Fields.InputTokens != 7729 {
		t.Fatalf("present usage must be written: %+v", got.RawUsage)
	}
	if got.LastRouteAttempt != nil && got.LastRouteAttempt.RequestedModel != agyTestModel {
		t.Fatalf("requested route must not be overwritten by provider echo: %+v", got.LastRouteAttempt)
	}
	diag := got.LastRouteAttempt.AntigravityDiagnostics
	if diag == nil || diag.StatusCategory != antigravityStatusSuccess || !containsDeniedCategory(diag, antigravityDeniedCommand) {
		t.Fatalf("denied facts must be finite categories: %+v", diag)
	}

	root2, cfg2, task2, _ := setupAntigravityRun(t, `{"status":"SUCCESS","response":"no-usage"}`, agySecretSentinel, 0, true)
	got2 := runAntigravityTask(t, root2, cfg2, task2)
	assertNoSecretLeak(t, root2, got2, agySecretSentinel)
	if got2.SessionID != "" {
		t.Fatalf("missing conversation_id must stay unknown, got %q", got2.SessionID)
	}
	if got2.RawUsage != nil && got2.RawUsage.Last != nil && got2.RawUsage.Last.Fields.InputTokens != nil {
		t.Fatalf("missing usage must not be written as zero: %+v", got2.RawUsage.Last.Fields)
	}

	const statusSentinel = "SECRET_STATUS_SENTINEL"
	const deniedSentinel = "SECRET_DENIED_ACTION"
	const displaySentinel = "SECRET_DENIED_DISPLAY"
	payload := `{"status":"` + statusSentinel + `","response":"","denied_actions":[{"action":"` + deniedSentinel + `","display_name":"` + displaySentinel + `"}]}`
	root3, cfg3, task3, _ := setupAntigravityRun(t, payload, "", 0, false)
	got3 := runAntigravityTask(t, root3, cfg3, task3)
	for _, s := range []string{statusSentinel, deniedSentinel, displaySentinel} {
		assertNoSecretLeak(t, root3, got3, s)
	}
	if d := got3.LastRouteAttempt.AntigravityDiagnostics; d == nil || d.StatusCategory != antigravityStatusUnknown ||
		!containsDeniedCategory(d, antigravityDeniedUnknown) {
		t.Fatalf("raw status/denied must project to unknown category: %+v", got3.LastRouteAttempt)
	}
}

func TestR01OptionalDeniedDoesNotBlockVerifiedResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake AGY provider")
	}
	optionalPayload := `{"status":"SUCCESS","response":"necessary-artifacts-ready","denied_actions":[{"action":"web_search","display_name":"WebSearch"}]}`
	root, cfg, task, _ := setupAntigravityRun(t, optionalPayload, "", 0, true)
	if err := os.WriteFile(filepath.Join(task.Dir, "required.txt"), []byte("artifact\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task.Verify = "test -f required.txt"
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	got := runAntigravityTask(t, root, cfg, task)
	if got.Status != statusDone {
		t.Fatalf("optional denied must not extra-hold a verified result: status=%s last=%q", got.Status, got.LastError)
	}
	if got.LastRouteAttempt == nil || got.LastRouteAttempt.AntigravityDiagnostics == nil ||
		!containsDeniedCategory(got.LastRouteAttempt.AntigravityDiagnostics, antigravityDeniedWebSearch) ||
		got.LastRouteAttempt.AntigravityDiagnostics.DeniedCount < 1 {
		t.Fatalf("denied category/count missing: %+v", got.LastRouteAttempt)
	}
	if got.Harvest == nil || got.Harvest.VerifyExit == nil || *got.Harvest.VerifyExit != 0 {
		t.Fatalf("existing verify evidence missing: %+v", got.Harvest)
	}

	bin := buildCardexCLI(t)
	t.Run("cli-web-search-unverified", func(t *testing.T) {
		cliRoot, work, _ := writeAntigravityCLIRoot(t, optionalPayload, "", 0)
		id := addAntigravityCLITask(t, bin, cliRoot, work, "cli-web-search-unverified", false)
		runOut, _ := exec.Command(bin, "run", "-root", cliRoot, id).CombinedOutput()
		got := mustLoadTaskAnywhere(t, cliRoot, id)
		if got.Status == statusDone {
			t.Fatalf("web_search denied without actual validation must not be done: %+v\n%s", got, runOut)
		}
		if got.LastRouteAttempt == nil || got.LastRouteAttempt.AntigravityDiagnostics == nil ||
			!containsDeniedCategory(got.LastRouteAttempt.AntigravityDiagnostics, antigravityDeniedWebSearch) ||
			got.LastRouteAttempt.AntigravityDiagnostics.DeniedCount < 1 {
			t.Fatalf("web_search denial must remain visible: %+v\n%s", got.LastRouteAttempt, runOut)
		}
		assertEventTypes(t, cliRoot, id, true, evHeld)
		assertEventTypes(t, cliRoot, id, false, evDone)
	})
	commandPayload := `{"status":"SUCCESS","response":"work landed despite command denial","denied_actions":[{"action":"command","display_name":"RunCommand"}]}`
	t.Run("cli-command-verified", func(t *testing.T) {
		cliRoot, work, _ := writeAntigravityCLIRoot(t, commandPayload, "", 0)
		if err := os.WriteFile(filepath.Join(work, "required.txt"), []byte("artifact\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		id := addAntigravityCLITask(t, bin, cliRoot, work, "cli-command-verified", false)
		tk := mustLoadTaskAnywhere(t, cliRoot, id)
		tk.Verify = "test -f required.txt"
		tk.VerifyOnSuccess = true
		if err := saveTask(cliRoot, tk); err != nil {
			t.Fatal(err)
		}
		runOut, err := exec.Command(bin, "run", "-root", cliRoot, id).CombinedOutput()
		if err != nil {
			t.Fatalf("command-verified run: %v\n%s", err, runOut)
		}
		got := mustLoadTaskAnywhere(t, cliRoot, id)
		if got.Status != statusDone {
			t.Fatalf("verified artifact plus irrelevant command denial must not extra-hold: status=%s last=%q\n%s", got.Status, got.LastError, runOut)
		}
		if got.LastRouteAttempt == nil || got.LastRouteAttempt.AntigravityDiagnostics == nil ||
			!containsDeniedCategory(got.LastRouteAttempt.AntigravityDiagnostics, antigravityDeniedCommand) ||
			got.LastRouteAttempt.AntigravityDiagnostics.DeniedCount < 1 {
			t.Fatalf("command denial must remain visible: %+v", got.LastRouteAttempt)
		}
		if got.Harvest == nil || got.Harvest.VerifyExit == nil || *got.Harvest.VerifyExit != 0 {
			t.Fatalf("existing verify evidence missing: %+v", got.Harvest)
		}
		assertEventTypes(t, cliRoot, id, true, evDone)
		assertEventTypes(t, cliRoot, id, false, evHeld)
	})
}

func TestR05SingleStateWriter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake AGY CLI consumer")
	}
	bin := buildCardexCLI(t)
	payload := `{"status":"SUCCESS","response":"agy-cli-complete","conversation_id":"sess-cli","num_turns":1,"usage":{"input_tokens":5,"output_tokens":2}}`
	t.Run("happy-twice", func(t *testing.T) {
		root, work, productCalls := writeAntigravityCLIRoot(t, payload, "", 0)
		id := addAntigravityCLITask(t, bin, root, work, "cli-happy", true)
		run1, err1 := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
		if err1 != nil {
			t.Fatalf("first run: %v\n%s", err1, run1)
		}
		got := mustLoadTaskAnywhere(t, root, id)
		if got.Status != statusDone {
			t.Fatalf("first terminal status=%s last=%q\n%s", got.Status, got.LastError, run1)
		}
		if countProductCalls(t, productCalls) != 1 {
			t.Fatalf("first run product calls=%d", countProductCalls(t, productCalls))
		}
		run2, _ := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
		got2 := mustLoadTaskAnywhere(t, root, id)
		if got2.Status != statusDone {
			t.Fatalf("replay changed terminal status=%s last=%q\n%s", got2.Status, got2.LastError, run2)
		}
		if countProductCalls(t, productCalls) != 1 {
			t.Fatalf("duplicate observation started another provider call: %d\n%s", countProductCalls(t, productCalls), run2)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		root, work, productCalls := writeAntigravityCLIRoot(t, payload, "", 0)
		id := addAntigravityCLITask(t, bin, root, work, "cli-cancel", true)
		if out, err := exec.Command(bin, "cancel", "-root", root, id).CombinedOutput(); err != nil {
			t.Fatalf("cancel: %v\n%s", err, out)
		}
		runOut, _ := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
		got := mustLoadTaskAnywhere(t, root, id)
		if got.Status == statusDone {
			t.Fatalf("canceled card completed: %+v\n%s", got, runOut)
		}
		if countProductCalls(t, productCalls) != 0 {
			t.Fatalf("cancel still invoked provider: %d", countProductCalls(t, productCalls))
		}
		assertEventTypes(t, root, id, true, evCanceled)
		assertEventTypes(t, root, id, false, evDone)
	})
	t.Run("unknown", func(t *testing.T) {
		root, work, _ := writeAntigravityCLIRoot(t, `{}`, "", 0)
		id := addAntigravityCLITask(t, bin, root, work, "cli-unknown", true)
		runOut, _ := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
		got := mustLoadTaskAnywhere(t, root, id)
		if got.Status == statusDone {
			t.Fatalf("unknown terminal completed: %+v\n%s", got, runOut)
		}
		if got.Status != statusHeld {
			t.Fatalf("unknown status=%s last=%q\n%s", got.Status, got.LastError, runOut)
		}
		if strings.Contains(got.LastError, antigravityCapabilityNoInput) {
			t.Fatalf("unknown terminal masked by capability reason: %q\n%s", got.LastError, runOut)
		}
	})
	t.Run("leakage", func(t *testing.T) {
		root, work, _ := writeAntigravityCLIRoot(t, payload, agySecretSentinel, 0)
		id := addAntigravityCLITask(t, bin, root, work, "cli-leak", true)
		if out, err := exec.Command(bin, "run", "-root", root, id).CombinedOutput(); err != nil {
			t.Fatalf("leak run: %v\n%s", err, out)
		}
		got := mustLoadTaskAnywhere(t, root, id)
		assertNoSecretLeak(t, root, got, agySecretSentinel)
	})
}

func antigravityTestConfig(t *testing.T, bin string) *Config {
	t.Helper()
	cfg := defaultConfig("/usr/bin/true")
	cfg.AntigravityBin = bin
	cfg.Antigravity = &AntigravityRoute{Enabled: true, Effort: "high"}
	cfg.StepTimeoutMin = 1
	cfg.MaxAttempts = 3
	cfg.RetryBackoffMin = 1
	cfg.CooldownMarginSec = 0
	cfg.HarvestMode = harvestModeOff
	cfg.OwnerRoutingEnforced = false
	return cfg
}

func readyAntigravityTask(t *testing.T, typ, model string) *Task {
	t.Helper()
	task := &Task{
		ID: uniqueTaskID("agy-" + typ), Type: typ, Dir: t.TempDir(),
		PreferRunner: antigravityRunnerName, RunnerExplicit: true, AgyModel: model,
		LastProviderPreflight: &ProviderPreflightReadback{Runner: antigravityRunnerName, State: providerReady, SelectedModel: model},
	}
	return task
}

func fakeAntigravityCounted(t *testing.T, payload, stderr string, exitCode int) (bin, productCalls, argsDump string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "agy")
	productCalls = filepath.Join(dir, "product.calls")
	argsDump = filepath.Join(dir, "args.dump")
	payloadPath := filepath.Join(dir, "result.json")
	if err := os.WriteFile(payloadPath, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = 'models' ]; then\n" +
		"    printf '%s\\n' 'claude-opus-4-6-thinking\tOpus thinking'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"done\n" +
		"printf 'product\\n' >> " + shSingleQuote(productCalls) + "\n" +
		"printf '%s\\n' \"$@\" > " + shSingleQuote(argsDump) + "\n" +
		"cat " + shSingleQuote(payloadPath) + "\n"
	if stderr != "" {
		script += "printf '%s\\n' " + shSingleQuote(stderr) + " >&2\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, productCalls, argsDump
}

func setupAntigravityRun(t *testing.T, payload, stderr string, exitCode int, verify bool) (root string, cfg *Config, task *Task, productCalls string) {
	t.Helper()
	bin, productCalls, _ := fakeAntigravityCounted(t, payload, stderr, exitCode)
	cfg = antigravityTestConfig(t, bin)
	root = testRoot(t)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	task = newTask(root, cfg, typeSequence, "agy fixture", work, []string{"synthetic agy task"}, 1)
	task.PreferRunner = antigravityRunnerName
	task.RunnerExplicit = true
	task.AgyModel = agyTestModel
	task.ReviewAfter = false
	if verify {
		task.Verify = "true"
		task.VerifyOnSuccess = true
	}
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	return root, cfg, task, productCalls
}

func runAntigravityTask(t *testing.T, root string, cfg *Config, task *Task) *Task {
	t.Helper()
	withSchedulerLock(t, root)
	if err := runTaskVia(context.Background(), root, cfg, task, antigravityRunnerName); err != nil {
		t.Fatalf("runTaskVia: %v", err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		got, err = findTaskAnywhere(root, task.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func readArgvDump(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertEventTypes(t *testing.T, root, id string, want bool, types ...string) {
	t.Helper()
	events, _, err := loadTaskEvents(root, id)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.Type] = true
	}
	for _, typ := range types {
		if seen[typ] != want {
			t.Fatalf("event %s want present=%v events=%v", typ, want, eventTypeList(events))
		}
	}
}

func eventTypeList(events []TaskEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}

func containsDeniedCategory(d *antigravityDiagnostics, cat string) bool {
	if d == nil {
		return false
	}
	for _, c := range d.DeniedCategories {
		if c == cat {
			return true
		}
	}
	return false
}

func assertNoSecretLeak(t *testing.T, root string, got *Task, sentinel string) {
	t.Helper()
	raw, err := os.ReadFile(taskPath(root, got.ID))
	if err != nil {
		raw, err = os.ReadFile(filepath.Join(archiveDir(root), got.ID+".json"))
	}
	if err == nil && strings.Contains(string(raw), sentinel) {
		t.Fatalf("task JSON leaked sentinel")
	}
	if ev, err := os.ReadFile(eventsPath(root, got.ID)); err == nil && strings.Contains(string(ev), sentinel) {
		t.Fatalf("events leaked sentinel")
	}
	if lg, err := os.ReadFile(taskLogPath(root, got.ID)); err == nil && strings.Contains(string(lg), sentinel) {
		t.Fatalf("log leaked sentinel")
	}
	if strings.Contains(got.LastError, sentinel) || strings.Contains(got.LastSummary, sentinel) {
		t.Fatalf("task fields leaked sentinel: last=%q summary=%q", got.LastError, got.LastSummary)
	}
}

func writeAntigravityCLIRoot(t *testing.T, payload, stderr string, exitCode int) (root, work, productCalls string) {
	t.Helper()
	agy, productCalls, _ := fakeAntigravityCounted(t, payload, stderr, exitCode)
	root = t.TempDir()
	work = t.TempDir()
	for _, d := range []string{"tasks", "archive", "logs", "events", "templates"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := antigravityTestConfig(t, agy)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	return root, work, productCalls
}

func addAntigravityCLITask(t *testing.T, bin, root, work, title string, verify bool) string {
	t.Helper()
	args := []string{"add", "-root", root, "-runner", antigravityRunnerName, "-agy-model", agyTestModel,
		"-dir", work, "-title", title, "-review-after=false"}
	if verify {
		args = append(args, "-verify", "true", "-verify-on-success")
	}
	args = append(args, "synthetic agy cli task")
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("cardex add: %v\n%s", err, out)
	}
	return onlyTaskID(t, root)
}

func mustLoadTaskAnywhere(t *testing.T, root, id string) *Task {
	t.Helper()
	got, err := findTaskAnywhere(root, id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
