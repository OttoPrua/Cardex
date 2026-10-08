package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const retainedSource = "package retained\n"

func persistLegalFailedCard(t *testing.T, root, title, session string) *Task {
	t.Helper()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "kept.go"), []byte(retainedSource), 0o644); err != nil {
		t.Fatal(err)
	}
	tk := newTask(root, testCfg(), typeSequence, title, work, []string{"do work", "next"}, 5)
	tk.Status = statusFailed
	tk.SessionID = session
	tk.MidStep = true
	tk.Step = 1
	tk.LastError = "provider crashed after partial work"
	tk.Attempts = 3
	tk.MaxAttempts = 8
	tk.CostUSD = 1.25
	tk.TurnsUsed = 9
	tk.Model = "opus"
	tk.GrokModel = "grok-4.6"
	tk.PreferRunner = grokBuildRunnerName
	markControlTerminal(tk)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func loadMust(t *testing.T, root, id string) *Task {
	t.Helper()
	got, err := loadTask(root, id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func taskFileBytes(t *testing.T, root, id string) []byte {
	t.Helper()
	raw, err := os.ReadFile(taskPath(root, id))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertRetainedWorkHistory(t *testing.T, before, after *Task) {
	t.Helper()
	if after.Dir != before.Dir {
		t.Fatalf("work dir changed: %s -> %s", before.Dir, after.Dir)
	}
	got, err := os.ReadFile(filepath.Join(after.Dir, "kept.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != retainedSource {
		t.Fatalf("source bytes changed: %q", got)
	}
	if after.LastError != before.LastError {
		t.Fatalf("failure history cleared: %q", after.LastError)
	}
	if after.CostUSD != before.CostUSD || after.TurnsUsed != before.TurnsUsed || after.MaxAttempts != before.MaxAttempts {
		t.Fatalf("budgets changed: cost=%v turns=%d max=%d", after.CostUSD, after.TurnsUsed, after.MaxAttempts)
	}
	if after.Model != before.Model || after.GrokModel != before.GrokModel || after.PreferRunner != before.PreferRunner {
		t.Fatalf("provider/model changed: model=%q grok=%q runner=%q", after.Model, after.GrokModel, after.PreferRunner)
	}
	if after.Step != before.Step {
		t.Fatalf("completed step reset: %d -> %d", before.Step, after.Step)
	}
}

const fakeClaudeArgvFile = ".cardex-fake-claude-argv"
const freshProviderSession = "sess-fresh-received"

func writeFakeClaudeArgvCapture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
case "$1" in --version|-v) echo 'claude 1.0'; exit 0;; esac
cat >/dev/null
printf '%s\n' "$@" > .cardex-fake-claude-argv
resume=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--resume" ]; then
    resume="$a"
  fi
  prev="$a"
done
if [ -n "$resume" ]; then
  sid="$resume"
else
  sid="sess-fresh-received"
fi
printf '{"type":"result","result":"done step","session_id":"%s","num_turns":1,"total_cost_usd":0.01,"duration_ms":10,"is_error":false}\n' "$sid"
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func persistLegalFailedClaudeCard(t *testing.T, root, title, session string) *Task {
	t.Helper()
	tk := persistLegalFailedCard(t, root, title, session)
	tk.PreferRunner = "claude"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func saveRunConfig(t *testing.T, root, claudeBin string) {
	t.Helper()
	cfg := defaultConfig(claudeBin)
	cfg.StepTimeoutMin = 1
	cfg.HarvestMode = harvestModeOff
	cfg.MaxParallel = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
}

func readCapturedArgv(t *testing.T, workDir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workDir, fakeClaudeArgvFile))
	if err != nil {
		t.Fatalf("fake claude argv missing in %s: %v", workDir, err)
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func argvHasResume(argv []string, session string) bool {
	for i, a := range argv {
		if a == "--resume" && i+1 < len(argv) && argv[i+1] == session {
			return true
		}
	}
	return false
}

func cardexRunCaptureArgv(t *testing.T, bin, root, id, workDir string) ([]string, *Task, string) {
	t.Helper()
	out, err := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
	if err != nil {
		t.Fatalf("cardex run %s: %v\n%s", id, err, out)
	}
	return readCapturedArgv(t, workDir), loadMust(t, root, id), string(out)
}

func assertRetainedSourceAndPins(t *testing.T, before, after *Task) {
	t.Helper()
	if after.Dir != before.Dir {
		t.Fatalf("work dir changed: %s -> %s", before.Dir, after.Dir)
	}
	got, err := os.ReadFile(filepath.Join(after.Dir, "kept.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != retainedSource {
		t.Fatalf("source bytes changed: %q", got)
	}
	if after.Model != before.Model || after.GrokModel != before.GrokModel || after.PreferRunner != before.PreferRunner {
		t.Fatalf("provider/model changed: model=%q grok=%q runner=%q", after.Model, after.GrokModel, after.PreferRunner)
	}
	if after.MaxAttempts != before.MaxAttempts {
		t.Fatalf("max attempts changed: %d -> %d", before.MaxAttempts, after.MaxAttempts)
	}
}

func TestRetryDefaultRetainsSessionOnFailedCard(t *testing.T) {
	root := testRoot(t)
	before := persistLegalFailedCard(t, root, "default retry keeps session", "sess-keep-1")
	if err := cmdSetStatus([]string{"-root", root, before.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	after := loadMust(t, root, before.ID)
	if after.Status != statusQueued {
		t.Fatalf("status=%s", after.Status)
	}
	if after.SessionID != "sess-keep-1" {
		t.Fatalf("default retry dropped session: %q", after.SessionID)
	}
	if after.MidStep != true {
		t.Fatal("default retry cleared mid-step continuation")
	}
	assertRetainedWorkHistory(t, before, after)
}

func TestRetryFreshClearsContinuationKeepsWork(t *testing.T) {
	root := testRoot(t)
	before := persistLegalFailedCard(t, root, "fresh retry new session", "sess-old-2")
	if err := cmdSetStatus([]string{"-root", root, "-fresh", before.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	after := loadMust(t, root, before.ID)
	if after.Status != statusQueued {
		t.Fatalf("status=%s", after.Status)
	}
	if after.SessionID != "" {
		t.Fatalf("fresh retry kept session %q", after.SessionID)
	}
	if after.MidStep {
		t.Fatal("fresh retry left mid-step resume state")
	}
	assertRetainedWorkHistory(t, before, after)
}

func TestRetryFreshFakeCLIResumeVsNoResume(t *testing.T) {
	root := testRoot(t)
	fake := writeFakeClaudeArgvCapture(t)
	saveRunConfig(t, root, fake)
	bin := buildCardexCLI(t)
	keep := persistLegalFailedClaudeCard(t, root, "resume consumer", "sess-resume")
	fresh := persistLegalFailedClaudeCard(t, root, "no-resume consumer", "sess-drop")
	if err := cmdSetStatus([]string{"-root", root, keep.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	if err := cmdSetStatus([]string{"-root", root, "-fresh", fresh.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	keepQueued := loadMust(t, root, keep.ID)
	freshQueued := loadMust(t, root, fresh.ID)
	if keepQueued.SessionID != "sess-resume" {
		t.Fatalf("default retry dropped session: %q", keepQueued.SessionID)
	}
	if freshQueued.SessionID != "" {
		t.Fatalf("fresh retry kept session %q", freshQueued.SessionID)
	}
	assertRetainedWorkHistory(t, keep, keepQueued)
	assertRetainedWorkHistory(t, fresh, freshQueued)

	keepArgv, keepAfter, keepOut := cardexRunCaptureArgv(t, bin, root, keep.ID, keep.Dir)
	if !argvHasResume(keepArgv, "sess-resume") {
		t.Fatalf("default cardex run argv lost old --resume: %v\n%s", keepArgv, keepOut)
	}
	assertRetainedSourceAndPins(t, keepQueued, keepAfter)

	freshArgv, freshAfter, freshOut := cardexRunCaptureArgv(t, bin, root, fresh.ID, fresh.Dir)
	if argvHasResume(freshArgv, "sess-drop") || containsString(freshArgv, "sess-drop") {
		t.Fatalf("fresh cardex run still resumed old session: %v\n%s", freshArgv, freshOut)
	}
	if containsString(freshArgv, "--resume") {
		t.Fatalf("fresh cardex run still --resume: %v\n%s", freshArgv, freshOut)
	}
	if freshAfter.SessionID != freshProviderSession {
		t.Fatalf("fresh did not receive new session: %q\n%s", freshAfter.SessionID, freshOut)
	}
	assertRetainedSourceAndPins(t, freshQueued, freshAfter)
}

func TestRetryFreshRefusesHeldIntegration(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	tk := persistLegalFailedCard(t, root, "held gated", "sess-held")
	tk.Status = statusHeld
	tk.IntegrationGate = &IntegrationGate{ReviewTaskID: "missing-review"}
	tk.LastError = "retry -fresh authorized; custody released; sealed lifted"
	markControlTerminal(tk)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	rec := &AttemptRecord{TaskID: tk.ID, AttemptID: "consumed", ControlEpoch: tk.ControlEpoch, State: attemptExited, CreatedAt: time.Now().Format(time.RFC3339Nano)}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	before := taskFileBytes(t, root, tk.ID)
	err := cmdSetStatus([]string{"-root", root, "-fresh", tk.ID}, "retry")
	if err == nil || !strings.Contains(err.Error(), "集成门仍 held") {
		t.Fatalf("held/integration: %v", err)
	}
	after := taskFileBytes(t, root, tk.ID)
	if string(after) != string(before) {
		t.Fatal("rejected held fresh retry mutated task bytes")
	}
	got := loadMust(t, root, tk.ID)
	if got.SessionID != "sess-held" {
		t.Fatalf("session force-cleared: %q", got.SessionID)
	}
}

func TestRetryFreshRefusesSealedNativeGoal(t *testing.T) {
	root := testRoot(t)
	tk := persistLegalFailedCard(t, root, "sealed goal", "sess-sealed")
	tk.Goal = &TaskGoalBinding{Observation: goalObsFailed, CustodyReleased: true}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	before := taskFileBytes(t, root, tk.ID)
	err := cmdSetStatus([]string{"-root", root, "-fresh", tk.ID}, "retry")
	if err == nil || !strings.Contains(err.Error(), "sealed/no-retry") {
		t.Fatalf("sealed: %v", err)
	}
	if string(taskFileBytes(t, root, tk.ID)) != string(before) {
		t.Fatal("sealed fresh retry mutated task")
	}
	if err := cmdSetStatus([]string{"-root", root, tk.ID}, "retry"); err != nil {
		t.Fatalf("default retry of failed card must stay legal: %v", err)
	}
	got := loadMust(t, root, tk.ID)
	if got.SessionID != "sess-sealed" {
		t.Fatalf("default retry dropped sealed session: %q", got.SessionID)
	}
}

func TestRetryFreshRefusesBoundNativeGoalIdentity(t *testing.T) {
	root := testRoot(t)
	tk := persistLegalFailedCard(t, root, "bound goal", "sess-bound")
	tk.Goal = &TaskGoalBinding{
		NativeGoalID:    "goal-bound",
		BoundAttemptID:  "att-bound",
		Started:         true,
		CustodyReleased: true,
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	before := taskFileBytes(t, root, tk.ID)
	err := cmdSetStatus([]string{"-root", root, "-fresh", tk.ID}, "retry")
	if err == nil || !strings.Contains(err.Error(), "身份已绑定") {
		t.Fatalf("bound identity: %v", err)
	}
	got := loadMust(t, root, tk.ID)
	if string(taskFileBytes(t, root, tk.ID)) != string(before) {
		t.Fatal("bound identity mutated")
	}
	if got.SessionID != "sess-bound" || got.Goal == nil || got.Goal.NativeGoalID != "goal-bound" || got.GrokModel != "grok-4.6" {
		t.Fatalf("force-cleared identity: %+v", got)
	}
}

func TestRetryFreshRefusesUnrecoveredCustody(t *testing.T) {
	root := testRoot(t)
	tk := persistLegalFailedCard(t, root, "custody held", "sess-custody")
	tk.Goal = &TaskGoalBinding{Started: true, CustodyReleased: false}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	before := taskFileBytes(t, root, tk.ID)
	err := cmdSetStatus([]string{"-root", root, "-fresh", tk.ID}, "retry")
	if err == nil || !strings.Contains(err.Error(), "custody 未回收") {
		t.Fatalf("custody: %v", err)
	}
	if string(taskFileBytes(t, root, tk.ID)) != string(before) {
		t.Fatal("custody refusal mutated task")
	}
}

func TestRetryFreshLastErrorIsNotPermission(t *testing.T) {
	root := testRoot(t)
	tk := persistLegalFailedCard(t, root, "lasterror trap", "sess-err")
	tk.Goal = &TaskGoalBinding{
		NativeGoalID:    "goal-trap",
		Started:         true,
		CustodyReleased: false,
		Observation:     goalObsFailed,
	}
	tk.LastError = "cardex retry -fresh is allowed; custody released; native Goal identity may be cleared"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	before := taskFileBytes(t, root, tk.ID)
	err := cmdSetStatus([]string{"-root", root, "-fresh", tk.ID}, "retry")
	if err == nil {
		t.Fatal("LastError text was treated as permission")
	}
	if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tk.LastError)) && !strings.Contains(err.Error(), "custody") && !strings.Contains(err.Error(), "sealed") && !strings.Contains(err.Error(), "身份") {
		t.Fatalf("error parsed LastError as policy: %v", err)
	}
	if string(taskFileBytes(t, root, tk.ID)) != string(before) {
		t.Fatal("LastError trap mutated task")
	}
}

func TestRetryFreshDoesNotChangeProviderOrForceClearUnsupportedIdentity(t *testing.T) {
	root := testRoot(t)
	tk := persistLegalFailedCard(t, root, "unsupported bound", "sess-unsup")
	tk.Goal = &TaskGoalBinding{NativeGoalID: "goal-unsup", BoundAttemptID: "att-unsup", Started: true, CustodyReleased: true}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	err := cmdSetStatus([]string{"-root", root, "-fresh", tk.ID}, "retry")
	if err == nil {
		t.Fatal("expected bound identity refusal")
	}
	got := loadMust(t, root, tk.ID)
	if got.PreferRunner != grokBuildRunnerName || got.GrokModel != "grok-4.6" || got.Model != "opus" {
		t.Fatalf("provider/model changed on refusal: %+v", got)
	}
	if got.Goal.NativeGoalID != "goal-unsup" || got.SessionID != "sess-unsup" {
		t.Fatalf("unsupported identity force-cleared: %+v", got.Goal)
	}

	ok := persistLegalFailedCard(t, root, "fresh keeps pins", "sess-pins")
	if err := cmdSetStatus([]string{"-root", root, "-fresh", ok.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	after := loadMust(t, root, ok.ID)
	if after.PreferRunner != grokBuildRunnerName || after.GrokModel != "grok-4.6" || after.Model != "opus" {
		t.Fatalf("successful fresh changed provider/model: %+v", after)
	}
}

func attachRetainedAttempt(t *testing.T, root string, tk *Task, rec *AttemptRecord) {
	t.Helper()
	if rec.CreatedAt == "" {
		now := time.Now().Format(time.RFC3339Nano)
		rec.CreatedAt, rec.UpdatedAt = now, now
	}
	rec.TaskID = tk.ID
	if rec.AttemptID == "" {
		rec.AttemptID = "at-retained"
	}
	rec.ControlEpoch = tk.ControlEpoch
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	tk.ActiveAttemptID = rec.AttemptID
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
}

func TestRetryFreshRefusesUnknownOrdinaryAttempt(t *testing.T) {
	root := testRoot(t)
	missing := persistLegalFailedCard(t, root, "missing attempt", "sess-missing")
	missing.ActiveAttemptID = "at-missing"
	if err := saveTask(root, missing); err != nil {
		t.Fatal(err)
	}
	before := taskFileBytes(t, root, missing.ID)
	err := cmdSetStatus([]string{"-root", root, "-fresh", missing.ID}, "retry")
	if err == nil || !strings.Contains(err.Error(), "去向未知") {
		t.Fatalf("missing attempt: %v", err)
	}
	if string(taskFileBytes(t, root, missing.ID)) != string(before) {
		t.Fatal("unknown missing attempt mutated task")
	}
	if err := cmdSetStatus([]string{"-root", root, missing.ID}, "retry"); err != nil {
		t.Fatalf("default retry must ignore unknown custody: %v", err)
	}
	if loadMust(t, root, missing.ID).SessionID != "sess-missing" {
		t.Fatal("default retry dropped session")
	}

	bound := persistLegalFailedCard(t, root, "bound unknown", "sess-bound-ord")
	attachRetainedAttempt(t, root, bound, &AttemptRecord{State: attemptBound, PID: 0})
	beforeBound := taskFileBytes(t, root, bound.ID)
	err = cmdSetStatus([]string{"-root", root, "-fresh", bound.ID}, "retry")
	if err == nil || !strings.Contains(err.Error(), "去向未知") {
		t.Fatalf("bound unknown: %v", err)
	}
	if string(taskFileBytes(t, root, bound.ID)) != string(beforeBound) {
		t.Fatal("bound unknown mutated task")
	}
}

func TestRetryFreshRefusesRunningStatus(t *testing.T) {
	root := testRoot(t)
	tk := newTask(root, testCfg(), typeSequence, "running", t.TempDir(), []string{"p"}, 5)
	tk.Status = statusRunning
	tk.SessionID = "sess-run"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	before := taskFileBytes(t, root, tk.ID)
	err := cmdSetStatus([]string{"-root", root, "-fresh", tk.ID}, "retry")
	if err == nil || !strings.Contains(err.Error(), "无需 retry") {
		t.Fatalf("running: %v", err)
	}
	if string(taskFileBytes(t, root, tk.ID)) != string(before) {
		t.Fatal("running fresh retry mutated task")
	}
}

func writeOutboxRow(t *testing.T, root string, seq int64, taskID, event string) {
	t.Helper()
	row := managerWakeOutboxRow{
		Schema: outboxSchemaV1, Seq: seq, WakeEventID: "w", TaskID: taskID,
		TaskEventSeq: 1, TransitionID: "t", EventType: event, Status: event,
		ReasonClass: "cli", TS: "2026-10-09T00:00:00Z",
	}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	path := managerWakeOutboxPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryLinkOneTempCLILoop(t *testing.T) {
	root := testRoot(t)
	fake := writeFakeClaudeArgvCapture(t)
	saveRunConfig(t, root, fake)
	bin := buildCardexCLI(t)
	work := t.TempDir()
	reqID := "req_0123456789abcdef"

	addOut, err := exec.Command(bin, "add", "-root", root, "-dir", work, "-title", "digest req card", "-req", reqID, "do the bounded work").CombinedOutput()
	if err != nil {
		t.Fatalf("add: %v\n%s", err, addOut)
	}
	listOut, err := exec.Command(bin, "list", "-root", root, "-json").CombinedOutput()
	if err != nil {
		t.Fatalf("list: %v\n%s", err, listOut)
	}
	var listed []Task
	if err := json.Unmarshal(listOut, &listed); err != nil {
		t.Fatalf("list json: %v\n%s", err, listOut)
	}
	if len(listed) != 1 || listed[0].Req != reqID {
		t.Fatalf("list req: %s", listOut)
	}
	writeOutboxRow(t, root, 1, listed[0].ID, "failed")
	digOut, err := exec.Command(bin, "digest", "-root", root, "-since", "1", "-json").CombinedOutput()
	if err != nil {
		t.Fatalf("digest: %v\n%s", err, digOut)
	}
	var entries []digestEntry
	if err := json.Unmarshal(digOut, &entries); err != nil {
		t.Fatalf("digest json: %v\n%s", err, digOut)
	}
	if len(entries) != 1 || entries[0].Req != reqID {
		t.Fatalf("digest req: %s", digOut)
	}

	keep := persistLegalFailedClaudeCard(t, root, "cli default retry", "sess-cli-keep")
	fresh := persistLegalFailedClaudeCard(t, root, "cli fresh retry", "sess-cli-fresh")
	retryOut, err := exec.Command(bin, "retry", "-root", root, keep.ID).CombinedOutput()
	if err != nil {
		t.Fatalf("retry: %v\n%s", err, retryOut)
	}
	freshRetryOut, err := exec.Command(bin, "retry", "-fresh", "-root", root, fresh.ID).CombinedOutput()
	if err != nil {
		t.Fatalf("retry -fresh: %v\n%s", err, freshRetryOut)
	}
	kept := loadMust(t, root, keep.ID)
	dropped := loadMust(t, root, fresh.ID)
	if kept.SessionID != "sess-cli-keep" {
		t.Fatalf("CLI default session=%q", kept.SessionID)
	}
	if dropped.SessionID != "" {
		t.Fatalf("CLI fresh session=%q", dropped.SessionID)
	}
	assertRetainedWorkHistory(t, keep, kept)
	assertRetainedWorkHistory(t, fresh, dropped)

	keepArgv, keepAfter, keepRun := cardexRunCaptureArgv(t, bin, root, keep.ID, keep.Dir)
	if !argvHasResume(keepArgv, "sess-cli-keep") {
		t.Fatalf("CLI default run argv lost old --resume: %v\n%s", keepArgv, keepRun)
	}
	assertRetainedSourceAndPins(t, kept, keepAfter)

	freshArgv, freshAfter, freshRun := cardexRunCaptureArgv(t, bin, root, fresh.ID, fresh.Dir)
	if argvHasResume(freshArgv, "sess-cli-fresh") || containsString(freshArgv, "sess-cli-fresh") {
		t.Fatalf("CLI fresh run still resumed old session: %v\n%s", freshArgv, freshRun)
	}
	if containsString(freshArgv, "--resume") {
		t.Fatalf("CLI fresh run still --resume: %v\n%s", freshArgv, freshRun)
	}
	if freshAfter.SessionID != freshProviderSession {
		t.Fatalf("CLI fresh did not receive new session: %q\n%s", freshAfter.SessionID, freshRun)
	}
	assertRetainedSourceAndPins(t, dropped, freshAfter)

	wrongTier := &Task{
		ID: "t-wrong-tier", Model: "opus", PreferRunner: grokBuildRunnerName, GrokModel: "ignored-task-field",
	}
	obs := observationFromProviderResult(root, wrongTier, &claudeResult{
		UsageSource:            usageSrcGrokBuildEnd,
		ObservedAssistantModel: "grok-4.6",
	})
	if obs.Model != "grok-4.6" || obs.ModelSource != usageModelSrcProviderReported {
		t.Fatalf("CLI-loop usage still used generic tier: %+v", obs)
	}
}
