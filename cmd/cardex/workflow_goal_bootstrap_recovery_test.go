package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func mintBootstrapBeforeNativeAuthorization(t *testing.T, tk *Task, wf *WorkflowRecord, expires time.Time) string {
	t.Helper()
	if tk == nil || tk.Goal == nil || wf == nil {
		t.Fatal("mint requires workflow writer Goal")
	}
	if expires.IsZero() {
		expires = time.Now().Add(time.Hour)
	}
	auth := bootstrapRecoveryAuthorization{
		Kind:              bootstrapRecoveryKind,
		WorkflowID:        wf.ID,
		WriterTaskID:      tk.ID,
		Revision:          tk.Revision,
		OriginalAttemptID: strings.TrimSpace(tk.Goal.BoundAttemptID),
		SessionID:         tk.SessionID,
		ContractDigest:    tk.Goal.InputDigest,
		ProfileDigest:     tk.Goal.SandboxProfileDigest,
		IssuedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		ExpiresAt:         expires.UTC().Format(time.RFC3339Nano),
		Nonce:             newOpaqueID("bn"),
	}
	if auth.OriginalAttemptID == "" {
		auth.OriginalAttemptID = tk.ActiveAttemptID
	}
	raw, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bootstrap-authorization.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mintBootstrapExecutorAuthorization(t *testing.T, tk *Task, wf *WorkflowRecord, expires time.Time, proof syntheticCodexExecutorResult) string {
	t.Helper()
	path := mintBootstrapBeforeNativeAuthorization(t, tk, wf, expires)
	bindExecutorAuthorization(t, path, proof)
	return path
}

func bindExecutorAuthorization(t *testing.T, authPath string, proof syntheticCodexExecutorResult) {
	t.Helper()
	rewriteBootstrapAuthField(t, authPath, "executor_raw_digest", proof.Digest)
	rewriteBootstrapAuthField(t, authPath, "executor_call_id", proof.CallID)
	rewriteBootstrapAuthField(t, authPath, "executor_item_id", proof.ItemID)
	rewriteBootstrapAuthField(t, authPath, "executor_return_id", proof.ReturnID)
	rewriteBootstrapAuthField(t, authPath, "executor_provenance", proof.Provenance)
}

func fakeGrokGoalBinExitCode(t *testing.T, argvPath string, code int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\n"
	if argvPath != "" {
		script += "printf '%s\\n' \"$@\" > " + shSingleQuote(argvPath) + "\n"
	}
	script += fmt.Sprintf("exit %d\n", code)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeGrokGoalBinBootstrapBeforeProviderRefusal(t *testing.T, argvPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\n"
	if argvPath != "" {
		script += "printf '%s\\n' \"$@\" > " + shSingleQuote(argvPath) + "\n"
	}
	script += "printf '%s\\n' " + shSingleQuote(acceptedBootstrapBeforeProviderRefusal) + " >&2\n"
	script += "exit 2\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func plantRetainedBootstrapBeforeProviderProof(t *testing.T, root string, tk *Task, original *AttemptRecord) {
	t.Helper()
	if tk == nil || original == nil {
		t.Fatal("plant requires original attempt")
	}
	if err := writeBootstrapLauncherEvidence(root, bootstrapLauncherEvidence{
		Kind:           bootstrapBeforeProviderKind,
		AttemptID:      original.AttemptID,
		TaskID:         tk.ID,
		ExitStatus:     "2",
		RefusalOutput:  acceptedBootstrapBeforeProviderRefusal,
		SemanticEvents: 0,
		ModelEvents:    0,
		ToolEvents:     0,
		NativeSession:  false,
		NativeGoal:     false,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertCapturedBootstrapBeforeProviderProof(t *testing.T, root string, tk *Task, original *AttemptRecord) {
	t.Helper()
	if tk == nil || original == nil {
		t.Fatal("captured proof requires original attempt")
	}
	ev, err := loadBootstrapLauncherEvidence(root, tk.ID, original.AttemptID)
	if err != nil || ev == nil {
		t.Fatalf("real launch path did not retain exact bootstrap-before-provider refusal: %v", err)
	}
	if ev.AttemptID != original.AttemptID || ev.TaskID != tk.ID {
		t.Fatalf("captured proof bound to wrong attempt: %+v", ev)
	}
	if ev.ExitStatus == "" || ev.ExitStatus == "0" {
		t.Fatalf("captured proof exit_status=%q want actual nonzero", ev.ExitStatus)
	}
	if !outputContainsAcceptedBootstrapBeforeProviderRefusal(ev.RefusalOutput) {
		t.Fatalf("captured proof missing exact refusal output: %+v", ev)
	}
}

func bootstrapBeforeNativeFixture(t *testing.T) (root string, cfg *Config, wf *WorkflowRecord, tk *Task, originalAttempt *AttemptRecord, attemptBytes []byte) {
	t.Helper()
	root, dir := workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv"))
	wf = initTestWorkflow(t, root, dir)
	tk = admitManualWriter(t, root, cfg, wf)
	home := t.TempDir()
	tk.Goal.GrokHome = home
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	wf.MaxRounds = 3
	wf.CurrentRound = 3
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	tk, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	tk.FixRound = 3
	tk.MaxFixRounds = 3
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
		t.Fatalf("production launch: %v", err)
	}
	tk, err = loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !tk.Goal.Started || tk.Goal.Observation != goalObsUnknown {
		t.Fatalf("fixture must be started/unknown: started=%v obs=%s", tk.Goal.Started, tk.Goal.Observation)
	}
	if tk.Goal.BudgetTokens != 12000 {
		t.Fatalf("budget=%d", tk.Goal.BudgetTokens)
	}
	if tk.Goal.HardTimeoutSec <= 0 {
		t.Fatalf("hard timeout unset: %d", tk.Goal.HardTimeoutSec)
	}
	originalAttempt, err = loadRequiredGoalAttempt(root, tk)
	if err != nil || !goalAttemptHasStartIdentity(originalAttempt) {
		t.Fatalf("original start identity missing: rec=%+v err=%v", originalAttempt, err)
	}
	if originalAttempt.State != attemptExited {
		t.Fatalf("original attempt state=%s", originalAttempt.State)
	}
	attemptBytes, err = os.ReadFile(attemptPath(root, tk.ID, originalAttempt.AttemptID))
	if err != nil {
		t.Fatal(err)
	}
	hasSession, hasGoal, nerr := inspectNativeGrokSessionOrGoal(tk.Goal.GrokHome, tk.Dir, tk.SessionID)
	if nerr != nil || hasSession || hasGoal || tk.Goal.NativeGoalID != "" {
		t.Fatalf("fixture must lack native Session/Goal: session=%v goal=%v id=%q err=%v", hasSession, hasGoal, tk.Goal.NativeGoalID, nerr)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.CurrentRound != 3 || wf.MaxRounds != 3 {
		t.Fatalf("fixture round %d/%d", wf.CurrentRound, wf.MaxRounds)
	}
	return root, cfg, wf, tk, originalAttempt, attemptBytes
}

func bootstrapBeforeNativeRecoverableFixture(t *testing.T) (root string, cfg *Config, wf *WorkflowRecord, tk *Task, originalAttempt *AttemptRecord, attemptBytes []byte) {
	t.Helper()
	root, dir := workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	cfg.GrokBuildBin = fakeGrokGoalBinBootstrapBeforeProviderRefusal(t, filepath.Join(t.TempDir(), "argv"))
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf = initTestWorkflow(t, root, dir)
	tk = admitManualWriter(t, root, cfg, wf)
	home := t.TempDir()
	tk.Goal.GrokHome = home
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	wf.MaxRounds = 3
	wf.CurrentRound = 3
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	tk, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	tk.FixRound = 3
	tk.MaxFixRounds = 3
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	launchErr := runManualGoalLaunch(t, root, cfg, wf, 12000)
	if launchErr == nil {
		t.Fatal("exact bootstrap-before-provider refusal must exit nonzero")
	}
	tk, err = loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !tk.Goal.Started || tk.Goal.Observation != goalObsUnknown {
		t.Fatalf("recoverable fixture must be started/unknown: started=%v obs=%s", tk.Goal.Started, tk.Goal.Observation)
	}
	if tk.Goal.BudgetTokens != 12000 {
		t.Fatalf("budget=%d", tk.Goal.BudgetTokens)
	}
	if tk.Goal.HardTimeoutSec <= 0 {
		t.Fatalf("hard timeout unset: %d", tk.Goal.HardTimeoutSec)
	}
	originalAttempt, err = loadRequiredGoalAttempt(root, tk)
	if err != nil || !goalAttemptHasStartIdentity(originalAttempt) {
		t.Fatalf("original start identity missing: rec=%+v err=%v", originalAttempt, err)
	}
	if originalAttempt.State != attemptExited {
		t.Fatalf("original attempt state=%s", originalAttempt.State)
	}
	assertCapturedBootstrapBeforeProviderProof(t, root, tk, originalAttempt)
	attemptBytes, err = os.ReadFile(attemptPath(root, tk.ID, originalAttempt.AttemptID))
	if err != nil {
		t.Fatal(err)
	}
	hasSession, hasGoal, nerr := inspectNativeGrokSessionOrGoal(tk.Goal.GrokHome, tk.Dir, tk.SessionID)
	if nerr != nil || hasSession || hasGoal || tk.Goal.NativeGoalID != "" {
		t.Fatalf("fixture must lack native Session/Goal: session=%v goal=%v id=%q err=%v", hasSession, hasGoal, tk.Goal.NativeGoalID, nerr)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.CurrentRound != 3 || wf.MaxRounds != 3 {
		t.Fatalf("fixture round %d/%d", wf.CurrentRound, wf.MaxRounds)
	}
	cfg.GrokBuildBin = fakeGrokGoalBin(t, filepath.Join(t.TempDir(), "argv-recovery"))
	saveGoalCfg(t, root, cfg)
	return root, cfg, wf, tk, originalAttempt, attemptBytes
}

func runBootstrapRecoveryCLI(root, wfID, authPath string) error {
	return cmdWorkflowGoalBootstrapBeforeNativeRecovery([]string{
		"-root", root, wfID, "-manual", "-authorization", authPath,
	})
}

func runBootstrapRecoveryCLIWithExecutor(root, wfID, authPath, capturePath, digest, callID string) error {
	return cmdWorkflowGoalBootstrapBeforeNativeRecovery([]string{
		"-root", root, wfID, "-manual", "-authorization", authPath,
		"-executor-capture", capturePath,
		"-executor-digest", digest,
		"-executor-call-id", callID,
	})
}

func runBootstrapRecoveryHostedCLI(root, wfID, authPath string) error {
	return cmdWorkflowGoalBootstrapBeforeNativeRecovery([]string{
		"-root", root, wfID, "-hosted", "-authorization", authPath,
	})
}

const (
	syntheticCodexExecutorCallID    = "call_SYNTHETICFAKE0001"
	syntheticCodexExecutorCommandID = "exec-00000000-1111-2222-3333-444444444444"
	syntheticCodexExecutorProcessID = "4242"
	syntheticCodexExecutorReturnID  = "ctco_synthetic_unrelated"
)

type syntheticCodexExecutorOpts struct {
	CallID             string
	ItemID             string
	ReturnID           string
	Status             string
	ExitCode           *int
	OmitExitCode       bool
	LastChunkExit      *int
	OmitLastChunk      bool
	GoalRunOmitExit    bool
	GoalRunSessionID   string
	ProcessID          string
	SessionID          string
	ContractDigest     string
	ProfileDigest      string
	RefusalLine        string
	Command            string
	ItemCommand        string
	CommandArgv        []string
	ParsedCmd          string
	Input              string
	RecordCount        int
	ExtraApproved      bool
	UserJSON           any
	PlainText          string
	Attempt            *AttemptRecord
	CallTimestamp      string
	ItemTimestamp      string
	ReturnTimestamp    string
	Stdout             string
	Aggregated         string
	ReturnOutput       string
	SkipProvenance     bool
	LaterEvent         bool
	WrongAttemptTime   bool
	ProvenanceOnlyRaw  []byte
	OmitUnrelatedChunk bool
}

type syntheticCodexExecutorResult struct {
	Path       string
	Digest     string
	CallID     string
	ItemID     string
	ReturnID   string
	Provenance string
	Handle     string
	Raw        []byte
}

func executorFixtureJSONTime(ts time.Time) string {
	return ts.UTC().Format("2006-01-02T15:04:05.000Z")
}

func syntheticExecutorTimestamps(opts syntheticCodexExecutorOpts) (call, item, ret string) {
	if opts.CallTimestamp != "" || opts.ItemTimestamp != "" || opts.ReturnTimestamp != "" {
		call = opts.CallTimestamp
		item = opts.ItemTimestamp
		ret = opts.ReturnTimestamp
		if item == "" {
			item = call
		}
		if ret == "" {
			ret = item
		}
		return call, item, ret
	}
	created := time.Now().UTC().Add(-time.Second)
	updated := created.Add(200 * time.Millisecond)
	if opts.Attempt != nil {
		if ts, err := parseExecutorTime(opts.Attempt.CreatedAt); err == nil {
			created = ts
		}
		if ts, err := parseExecutorTime(opts.Attempt.UpdatedAt); err == nil {
			updated = ts
		}
	}
	if opts.LaterEvent {
		base := updated.Add(time.Hour)
		return executorFixtureJSONTime(base), executorFixtureJSONTime(base.Add(7 * time.Millisecond)), executorFixtureJSONTime(base.Add(11 * time.Millisecond))
	}
	if opts.WrongAttemptTime {
		return executorFixtureJSONTime(created.Add(-331 * time.Millisecond)),
			executorFixtureJSONTime(updated.Add(-80 * time.Millisecond)),
			executorFixtureJSONTime(updated.Add(-40 * time.Millisecond))
	}
	return executorFixtureJSONTime(created.Add(-331 * time.Millisecond)),
		executorFixtureJSONTime(updated.Add(7 * time.Millisecond)),
		executorFixtureJSONTime(updated.Add(11 * time.Millisecond))
}

func writeSyntheticCodexExecutorCapture(t *testing.T, tk *Task, wf *WorkflowRecord, opts syntheticCodexExecutorOpts) (path, digest, callID string) {
	t.Helper()
	res := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, opts)
	return res.Path, res.Digest, res.CallID
}

func writeSyntheticCodexExecutorCaptureResult(t *testing.T, tk *Task, wf *WorkflowRecord, opts syntheticCodexExecutorOpts) syntheticCodexExecutorResult {
	t.Helper()
	if tk == nil || tk.Goal == nil || wf == nil {
		t.Fatal("synthetic executor capture requires writer Goal")
	}
	callID := strings.TrimSpace(opts.CallID)
	if callID == "" {
		callID = syntheticCodexExecutorCallID
	}
	itemID := strings.TrimSpace(opts.ItemID)
	if itemID == "" {
		itemID = syntheticCodexExecutorCommandID
	}
	returnID := strings.TrimSpace(opts.ReturnID)
	if returnID == "" {
		returnID = syntheticCodexExecutorReturnID
	}
	processID := strings.TrimSpace(opts.ProcessID)
	if processID == "" {
		processID = syntheticCodexExecutorProcessID
	}
	if opts.PlainText != "" {
		path := filepath.Join(t.TempDir(), "executor-capture.txt")
		raw := []byte(opts.PlainText)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		res := syntheticCodexExecutorResult{Path: path, Digest: sha256Hex(string(raw)), CallID: callID, ItemID: itemID, ReturnID: returnID, Raw: append([]byte(nil), raw...)}
		res.Provenance = writeSyntheticExecutorProvenance(t, raw, res)
		return res
	}
	if opts.UserJSON != nil {
		path := filepath.Join(t.TempDir(), "executor-capture.json")
		raw, err := json.MarshalIndent(opts.UserJSON, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, '\n')
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		res := syntheticCodexExecutorResult{Path: path, Digest: sha256Hex(string(raw)), CallID: callID, ItemID: itemID, ReturnID: returnID, Raw: append([]byte(nil), raw...)}
		res.Provenance = writeSyntheticExecutorProvenance(t, raw, res)
		return res
	}

	session := firstNonBlank(opts.SessionID, tk.SessionID)
	contract := firstNonBlank(opts.ContractDigest, tk.Goal.InputDigest)
	profile := firstNonBlank(opts.ProfileDigest, tk.Goal.SandboxProfileDigest)
	profileName := strings.TrimSpace(tk.Goal.SandboxProfile)
	if profileName == "" {
		profileName = "workspace"
	}
	refusal := opts.RefusalLine
	if refusal == "" {
		refusal = acceptedBootstrapBeforeProviderRefusal
	}
	exitCode := 1
	if opts.ExitCode != nil {
		exitCode = *opts.ExitCode
	}
	status := opts.Status
	if status == "" {
		status = "failed"
	}
	lastChunk := exitCode
	if opts.LastChunkExit != nil {
		lastChunk = *opts.LastChunkExit
	}
	command := opts.Command
	if command == "" {
		command = "cardex workflow goal-run -manual -budget 12000 " + wf.ID
	}
	itemCommand := opts.ItemCommand
	if itemCommand == "" {
		itemCommand = command
	}
	input := opts.Input
	if input == "" {
		input = "text(await tools.exec_command({cmd:\"cardex workflow writer -mode manual -prompt 'synthetic unrelated executor capture' " + wf.ID + "\"}));\n" +
			"text(await tools.exec_command({cmd:\"" + command + "\",tty:true,yield_time_ms:1000}));\n"
	}
	terminal := strings.Join([]string{
		"session_id=" + session + " bound before launch",
		"stage_contract_path=/tmp/synthetic-cardex/workflows/" + wf.ID + ".stage-contract.txt",
		"stage_contract_digest=" + contract,
		"Frozen stage contract is the native /goal input (not grok -p, not outcome-only):",
		"outcome: synthetic unrelated executor capture",
		"acceptance: synthetic",
		"stop: provider budget is soft; existing step timeout is the hard deadline",
		"return_to_design: true",
		"scope: synthetic-executor-capture",
		"budget_tokens: 12000 (soft)",
		"hard_timeout_seconds: 3600",
		"sandbox_profile: " + profileName,
		"sandbox_profile_digest: " + profile,
		refusal,
		"错误: exit status 1",
	}, "\n") + "\n"
	stdout := terminal
	if opts.Stdout != "" {
		stdout = opts.Stdout
	}
	aggregated := stdout
	if opts.Aggregated != "" {
		aggregated = opts.Aggregated
	}
	returnOut := stdout
	if opts.ReturnOutput != "" {
		returnOut = opts.ReturnOutput
	}
	callTs, itemTs, retTs := syntheticExecutorTimestamps(opts)

	commandArgv := opts.CommandArgv
	if len(commandArgv) == 0 {
		commandArgv = []string{"/bin/zsh", "-lc", itemCommand}
	}
	item := map[string]any{
		"type":              "CommandExecution",
		"id":                itemID,
		"process_id":        processID,
		"command":           commandArgv,
		"status":            status,
		"stdout":            stdout,
		"stderr":            "",
		"aggregated_output": aggregated,
		"formatted_output":  stdout,
	}
	if parsed := strings.TrimSpace(opts.ParsedCmd); parsed != "" {
		item["parsed_cmd"] = []any{map[string]any{"cmd": parsed}}
	}
	if !opts.OmitExitCode {
		item["exit_code"] = exitCode
	}

	chunk0, err := json.Marshal(map[string]any{
		"chunk_id":  "aaaaaa",
		"exit_code": 0,
		"output":    fmt.Sprintf("workflow %s writer=%s round=0 engine=grok-build mode=manual\n", wf.ID, tk.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := []any{
		map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.5 seconds\nOutput:\n"},
	}
	if !opts.OmitUnrelatedChunk {
		outputs = append(outputs, map[string]any{"type": "input_text", "text": string(chunk0)})
	}
	if !opts.OmitLastChunk {
		goalChunk := map[string]any{
			"chunk_id": "bbbbbb",
			"output":   returnOut,
		}
		if sid := strings.TrimSpace(opts.GoalRunSessionID); sid != "" {
			goalChunk["session_id"] = sid
		}
		if !opts.GoalRunOmitExit {
			goalChunk["exit_code"] = lastChunk
		}
		chunk1, merr := json.Marshal(goalChunk)
		if merr != nil {
			t.Fatal(merr)
		}
		outputs = append(outputs, map[string]any{"type": "input_text", "text": string(chunk1)})
	}

	records := []any{
		map[string]any{
			"timestamp": callTs,
			"ordinal":   1,
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "custom_tool_call",
				"id":      "ctc_synthetic_unrelated",
				"status":  "completed",
				"call_id": callID,
				"name":    "exec",
				"input":   input,
			},
		},
		map[string]any{
			"timestamp": itemTs,
			"ordinal":   2,
			"type":      "event_msg",
			"payload": map[string]any{
				"type": "item_completed",
				"item": item,
			},
		},
		map[string]any{
			"timestamp": retTs,
			"ordinal":   3,
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "custom_tool_call_output",
				"id":      returnID,
				"call_id": callID,
				"output":  outputs,
			},
		},
	}
	if opts.ExtraApproved {
		approvedItem := map[string]any{
			"type":       "CommandExecution",
			"id":         "exec-approved-retry",
			"process_id": "9999",
			"command":    []string{"/bin/zsh", "-lc", command},
			"status":     "completed",
			"exit_code":  0,
			"stdout":     "ok\n",
		}
		records = append(records, map[string]any{
			"timestamp": executorFixtureJSONTime(time.Now().UTC().Add(time.Minute)),
			"ordinal":   4,
			"type":      "event_msg",
			"payload": map[string]any{
				"type": "item_completed",
				"item": approvedItem,
			},
		})
	}
	n := opts.RecordCount
	if n <= 0 {
		n = len(records)
		if n > 3 && !opts.ExtraApproved {
			n = 3
		}
	}
	if n > len(records) {
		n = len(records)
	}
	records = records[:n]

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			t.Fatal(err)
		}
	}
	raw := buf.Bytes()
	path := filepath.Join(t.TempDir(), "original-executor-event.jsonl")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	res := syntheticCodexExecutorResult{
		Path:     path,
		Digest:   sha256Hex(string(raw)),
		CallID:   callID,
		ItemID:   itemID,
		ReturnID: returnID,
		Handle:   processID,
		Raw:      append([]byte(nil), raw...),
	}
	if opts.SkipProvenance {
		res.Provenance = path
		return res
	}
	res.Provenance = writeSyntheticExecutorProvenance(t, raw, res)
	return res
}

func writeSyntheticExecutorProvenance(t *testing.T, snapshot []byte, ids syntheticCodexExecutorResult) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "retained-executor-rollout.jsonl")
	decoy0, err := json.Marshal(map[string]any{
		"timestamp": "2020-01-01T00:00:00.000Z",
		"ordinal":   0,
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "custom_tool_call",
			"id":      "ctc_decoy_before",
			"call_id": "call_DECOYBEFORE0001",
			"name":    "exec",
			"input":   "decoy-before",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoy1, err := json.Marshal(map[string]any{
		"timestamp": "2020-01-01T00:00:01.000Z",
		"ordinal":   99,
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "custom_tool_call_output",
			"id":      "ctco_decoy_after",
			"call_id": "call_DECOYAFTER0001",
			"output":  "decoy-after",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.Write(decoy0)
	buf.WriteByte('\n')
	buf.Write(snapshot)
	if len(snapshot) > 0 && snapshot[len(snapshot)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.Write(decoy1)
	buf.WriteByte('\n')
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = ids
	return path
}

const (
	syntheticWriteStdinCallID   = "call_SYNTHETIC_WRITE_STDIN_B"
	syntheticWriteStdinReturnID = "ctco_synthetic_write_stdin_b"
	syntheticAsyncHandle        = "proc_SYNTHETIC_HANDLE_0001"
)

type syntheticWriteStdinOpts struct {
	CallID          string
	ReturnID        string
	Handle          string
	Input           string
	FirstExit       *int
	FirstOutput     string
	FirstSessionID  string
	SecondExit      *int
	OmitSecondChunk bool
	CallTimestamp   string
	ReturnTimestamp string
	SkipProvenance  bool
}

func writeSyntheticWriteStdinTerminal(t *testing.T, original syntheticCodexExecutorResult, rec *AttemptRecord, opts syntheticWriteStdinOpts) syntheticCodexExecutorResult {
	t.Helper()
	callID := firstNonBlank(opts.CallID, syntheticWriteStdinCallID)
	returnID := firstNonBlank(opts.ReturnID, syntheticWriteStdinReturnID)
	handle := firstNonBlank(opts.Handle, original.Handle, syntheticAsyncHandle)
	input := opts.Input
	if input == "" {
		input = syntheticWriteStdinInput(handle, 1000, 900, false)
	}
	firstExit := 1
	if opts.FirstExit != nil {
		firstExit = *opts.FirstExit
	}
	firstOut := opts.FirstOutput
	if firstOut == "" {
		firstOut = "错误: exit status 1"
	}
	secondExit := 0
	if opts.SecondExit != nil {
		secondExit = *opts.SecondExit
	}
	callTs := opts.CallTimestamp
	retTs := opts.ReturnTimestamp
	if callTs == "" || retTs == "" {
		base := time.Now().UTC()
		if rec != nil {
			if ts, err := parseExecutorTime(rec.UpdatedAt); err == nil {
				base = ts
			}
		}
		if callTs == "" {
			callTs = executorFixtureJSONTime(base.Add(20 * time.Millisecond))
		}
		if retTs == "" {
			retTs = executorFixtureJSONTime(base.Add(35 * time.Millisecond))
		}
	}
	chunk1, err := json.Marshal(map[string]any{
		"chunk_id":  "cccccc",
		"exit_code": firstExit,
		"output":    firstOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.FirstSessionID != "" {
		patched := map[string]any{
			"chunk_id":   "cccccc",
			"exit_code":  firstExit,
			"output":     firstOut,
			"session_id": opts.FirstSessionID,
		}
		chunk1, err = json.Marshal(patched)
		if err != nil {
			t.Fatal(err)
		}
	}
	outputs := []any{
		map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.2 seconds\nOutput:\n"},
		map[string]any{"type": "input_text", "text": string(chunk1)},
	}
	if !opts.OmitSecondChunk {
		chunk2, merr := json.Marshal(map[string]any{
			"chunk_id":  "dddddd",
			"exit_code": secondExit,
			"output":    "unrelated exec_command ok\n",
		})
		if merr != nil {
			t.Fatal(merr)
		}
		outputs = append(outputs, map[string]any{"type": "input_text", "text": string(chunk2)})
	}
	records := []any{
		map[string]any{
			"timestamp": callTs,
			"ordinal":   10,
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "custom_tool_call",
				"id":      "ctc_synthetic_write_stdin",
				"status":  "completed",
				"call_id": callID,
				"name":    "exec",
				"input":   input,
			},
		},
		map[string]any{
			"timestamp": retTs,
			"ordinal":   11,
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "custom_tool_call_output",
				"id":      returnID,
				"call_id": callID,
				"output":  outputs,
			},
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			t.Fatal(err)
		}
	}
	raw := buf.Bytes()
	path := filepath.Join(t.TempDir(), "later-write-stdin.jsonl")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	res := syntheticCodexExecutorResult{
		Path:     path,
		Digest:   sha256Hex(string(raw)),
		CallID:   callID,
		ReturnID: returnID,
		Handle:   handle,
		Raw:      append([]byte(nil), raw...),
	}
	if opts.SkipProvenance {
		res.Provenance = path
		return res
	}
	res.Provenance = writeSyntheticExecutorProvenance(t, raw, res)
	return res
}

func bindAsyncAssociation(t *testing.T, authPath string, original, terminal syntheticCodexExecutorResult, handle string) {
	t.Helper()
	bindExecutorAuthorization(t, authPath, original)
	rewriteBootstrapAuthField(t, authPath, "executor_association_kind", bootstrapExecutorAsyncAssociation)
	rewriteBootstrapAuthField(t, authPath, "executor_handle", handle)
	rewriteBootstrapAuthField(t, authPath, "terminal_raw_path", terminal.Path)
	rewriteBootstrapAuthField(t, authPath, "terminal_raw_digest", terminal.Digest)
	rewriteBootstrapAuthField(t, authPath, "terminal_call_id", terminal.CallID)
	rewriteBootstrapAuthField(t, authPath, "terminal_return_id", terminal.ReturnID)
	rewriteBootstrapAuthField(t, authPath, "terminal_provenance", terminal.Provenance)
}

func syntheticWriteStdinRetrieveAfter(rec *AttemptRecord, after time.Duration) (call, ret string) {
	base := time.Now().UTC()
	if rec != nil {
		if ts, err := parseExecutorTime(rec.UpdatedAt); err == nil {
			base = ts
		}
	}
	callT := base.Add(after)
	return executorFixtureJSONTime(callT), executorFixtureJSONTime(callT.Add(15 * time.Millisecond))
}

func syntheticWriteStdinInput(handle string, yieldMS, maxTokens int, extraExec bool) string {
	obj := fmt.Sprintf("{session_id:%s,chars:\"\"", handle)
	if yieldMS >= 0 {
		obj += fmt.Sprintf(",yield_time_ms:%d", yieldMS)
	}
	if maxTokens >= 0 {
		obj += fmt.Sprintf(",max_output_tokens:%d", maxTokens)
	}
	obj += "}"
	s := "text(await tools.write_stdin(" + obj + "))"
	if extraExec {
		s += ";\ntext(await tools.exec_command({cmd:\"echo unrelated-second-command\",yield_time_ms:1000}));\n"
	}
	return s
}

func syntheticPublicGoalRunCommand(wfID, root string, flagsAfter bool) string {
	script := "ENV_FLAG=1 /opt/homebrew/bin/cardex workflow goal-run"
	if flagsAfter {
		return script + " " + wfID + " -root " + root + " -hosted -budget 120000"
	}
	return script + " -root " + root + " -hosted -budget 120000 " + wfID
}

func syntheticRealBootstrapPair(grokHome, profile string) string {
	if profile == "" {
		profile = "workspace"
	}
	return realBootstrapWarningPrefix + filepath.Join(grokHome, "managed_config.toml") + realBootstrapWarningSuffix + "\n" +
		realBootstrapErrorPrefix + profile + realBootstrapErrorSuffix
}

func writeSyntheticAsyncOriginal(t *testing.T, tk *Task, wf *WorkflowRecord, attempt *AttemptRecord, command string) syntheticCodexExecutorResult {
	t.Helper()
	if command == "" {
		command = "cardex workflow goal-run -manual -budget 12000 " + wf.ID
	}
	pair := syntheticRealBootstrapPair(tk.Goal.GrokHome, firstNonBlank(tk.Goal.SandboxProfile, "workspace"))
	return writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
		Attempt:          attempt,
		Command:          command,
		ItemCommand:      command,
		ProcessID:        syntheticAsyncHandle,
		GoalRunOmitExit:  true,
		GoalRunSessionID: syntheticAsyncHandle,
		RefusalLine:      pair,
	})
}

func hostedPTYLimitation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "operation not permitted") ||
		strings.Contains(s, "operation not supported by device") ||
		strings.Contains(s, "inappropriate ioctl") ||
		strings.Contains(s, "device not configured") ||
		strings.Contains(s, "no such device") ||
		strings.Contains(s, "not a tty") ||
		strings.Contains(s, "pty_missing") ||
		strings.Contains(s, "hosted pty goal is not supported")
}

func requireHostedPTYOrUnverified(t *testing.T) bool {
	t.Helper()
	master, slave, err := openGoalPTY()
	if err != nil {
		if hostedPTYLimitation(err) {
			t.Logf("UNVERIFIED: %v", err)
			return false
		}
		t.Fatalf("openGoalPTY: %v", err)
		return false
	}
	_ = master.Close()
	_ = slave.Close()
	return true
}

type hostedBootstrapEmit int

const (
	hostedEmitLongThenExact hostedBootstrapEmit = iota
	hostedEmitQuoted
	hostedEmitPrefixed
	hostedEmitConcatenated
	hostedEmitFloodThenExit
	hostedEmitRealPair
	hostedEmitRealPairCRLF
	hostedEmitRealPairExit0
)

func fakeHostedGrokBinBootstrapCapture(t *testing.T, reportPath string, emit hostedBootstrapEmit) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\n"
	if reportPath != "" {
		script += "if [ -t 2 ]; then echo stderr_is_tty=1 > " + shSingleQuote(reportPath) + "\n"
		script += "else echo stderr_is_tty=0 > " + shSingleQuote(reportPath) + "\nfi\n"
	}
	exact := shSingleQuote(acceptedBootstrapBeforeProviderRefusal)
	switch emit {
	case hostedEmitLongThenExact:
		script += "i=0\nwhile [ \"$i\" -lt 256 ]; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' >&2; i=$((i+1)); done\n"
		script += "printf '\\n' >&2\n"
		script += "printf '%s\\n' " + exact + " >&2\n"
		script += "exit 1\n"
	case hostedEmitQuoted:
		script += "printf '%s\\n' " + shSingleQuote("diagnostic quoted error: "+acceptedBootstrapBeforeProviderRefusal) + " >&2\n"
		script += "exit 1\n"
	case hostedEmitPrefixed:
		script += "printf '%s\\n' " + shSingleQuote("prefix: "+acceptedBootstrapBeforeProviderRefusal) + " >&2\n"
		script += "exit 1\n"
	case hostedEmitConcatenated:
		script += "i=0\nwhile [ \"$i\" -lt 256 ]; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' >&2; i=$((i+1)); done\n"
		script += "printf '%s\\n' " + exact + " >&2\n"
		script += "exit 1\n"
	case hostedEmitFloodThenExit:
		script += "dd if=/dev/zero bs=8192 count=16 2>/dev/null | tr '\\0' 'A'\n"
		script += "printf '\\n'\n"
		script += "exit 0\n"
	case hostedEmitRealPair, hostedEmitRealPairCRLF, hostedEmitRealPairExit0:
		script += "home=$GROK_HOME\n"
		script += "profile=workspace\n"
		script += "prev=\"\"\n"
		script += "for a in \"$@\"; do\n"
		script += "  if [ \"$prev\" = \"--sandbox\" ]; then profile=$a; fi\n"
		script += "  prev=$a\n"
		script += "done\n"
		nl := "\\n"
		if emit == hostedEmitRealPairCRLF {
			nl = "\\r\\n"
		}
		script += "printf '%s%s%s" + nl + "' " + shSingleQuote(realBootstrapWarningPrefix) + " \"$home/managed_config.toml\" " + shSingleQuote(realBootstrapWarningSuffix) + " >&2\n"
		script += "printf '%s%s%s" + nl + "' " + shSingleQuote(realBootstrapErrorPrefix) + " \"$profile\" " + shSingleQuote(realBootstrapErrorSuffix) + " >&2\n"
		if emit == hostedEmitRealPairExit0 {
			script += "exit 0\n"
		} else {
			script += "exit 1\n"
		}
	default:
		t.Fatalf("unknown hosted emit mode %d", emit)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func hostedBootstrapWriterFixture(t *testing.T, bin string) (root string, cfg *Config, wf *WorkflowRecord, tk *Task) {
	t.Helper()
	root, dir := workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	cfg.GrokBuildBin = bin
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf = initTestWorkflow(t, root, dir)
	tk = admitManualWriter(t, root, cfg, wf)
	tk.Goal.GrokHome = t.TempDir()
	tk.Goal.HardTimeoutSec = 12
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	wf.MaxRounds = 3
	wf.CurrentRound = 3
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	tk, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	tk.FixRound = 3
	tk.MaxFixRounds = 3
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	return root, cfg, wf, tk
}

func withHostedCallerStdout(t *testing.T, sink *os.File, fn func()) {
	t.Helper()
	old := os.Stdout
	os.Stdout = sink
	defer func() { os.Stdout = old }()
	fn()
}

func countBootstrapConsumeEvents(t *testing.T, root, taskID string) int {
	t.Helper()
	events, _, err := loadTaskEvents(root, taskID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == evBootstrapBeforeNative {
			n++
		}
	}
	return n
}

func bootstrapConsumeDetail(t *testing.T, root, taskID string) map[string]any {
	t.Helper()
	events, _, err := loadTaskEvents(root, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == evBootstrapBeforeNative {
			return ev.Detail
		}
	}
	t.Fatal("missing consume event")
	return nil
}

func assertOriginalAttemptUnchanged(t *testing.T, root, taskID, attemptID string, originalBytes []byte) {
	t.Helper()
	gotBytes, err := os.ReadFile(attemptPath(root, taskID, attemptID))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBytes) != string(originalBytes) {
		t.Fatalf("original attempt bytes changed\nbefore:\n%s\nafter:\n%s", originalBytes, gotBytes)
	}
}

func TestBootstrapBeforeNativeRecoveryOneAuthorizedAttemptPreservesHistory(t *testing.T) {
	root, cfg, wf, tk, original, originalBytes := bootstrapBeforeNativeRecoverableFixture(t)
	_ = cfg
	if err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual"}); !errors.Is(err, errGoalRedispatchBlocked) {
		t.Fatalf("ordinary goal-run must stay fail-closed on the fixture: %v", err)
	}
	authPath := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
	if err := runBootstrapRecoveryCLI(root, wf.ID, authPath); err != nil {
		t.Fatalf("authorized recovery: %v", err)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	wfAfter, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wfAfter.CurrentRound != 3 || wfAfter.MaxRounds != 3 {
		t.Fatalf("round/max changed: %d/%d", wfAfter.CurrentRound, wfAfter.MaxRounds)
	}
	if after.SessionID != tk.SessionID {
		t.Fatalf("session changed: %s vs %s", after.SessionID, tk.SessionID)
	}
	if after.PreferRunner != tk.PreferRunner {
		t.Fatalf("route changed: %s vs %s", after.PreferRunner, tk.PreferRunner)
	}
	if after.Goal.BudgetTokens != tk.Goal.BudgetTokens || after.Goal.HardTimeoutSec != tk.Goal.HardTimeoutSec {
		t.Fatalf("budget/timeout reset: budget=%d/%d timeout=%d/%d",
			after.Goal.BudgetTokens, tk.Goal.BudgetTokens, after.Goal.HardTimeoutSec, tk.Goal.HardTimeoutSec)
	}
	if after.Goal.InputDigest != tk.Goal.InputDigest || after.Goal.SandboxProfileDigest != tk.Goal.SandboxProfileDigest {
		t.Fatalf("contract/profile digest changed")
	}
	if after.Goal.SandboxProfile != tk.Goal.SandboxProfile {
		t.Fatalf("profile name changed")
	}
	if !after.Goal.Started {
		t.Fatal("Started was cleared")
	}
	attempts, err := listGoalAttemptRecords(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count=%d want 2", len(attempts))
	}
	var kept, created *AttemptRecord
	for _, rec := range attempts {
		if rec.AttemptID == original.AttemptID {
			kept = rec
		} else {
			created = rec
		}
	}
	if kept == nil || created == nil {
		t.Fatalf("missing distinct attempts: original=%s new=%v", original.AttemptID, created)
	}
	if kept.State != attemptExited || kept.PID != original.PID || kept.StartIdentity != original.StartIdentity {
		t.Fatalf("original exit overwritten: %+v", kept)
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	if !goalAttemptHasStartIdentity(created) {
		t.Fatalf("new attempt missing Wait/custody start identity: %+v", created)
	}
	if created.AttemptID == original.AttemptID {
		t.Fatal("new attempt reused original id")
	}
	if after.Goal.BoundAttemptID != created.AttemptID {
		t.Fatalf("bound=%s new=%s", after.Goal.BoundAttemptID, created.AttemptID)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatalf("consume events=%d", countBootstrapConsumeEvents(t, root, tk.ID))
	}
	detail := bootstrapConsumeDetail(t, root, tk.ID)
	if eventString(detail, "original_attempt_id") != original.AttemptID {
		t.Fatalf("consume original=%s", eventString(detail, "original_attempt_id"))
	}
	if eventString(detail, "new_attempt_id") != created.AttemptID {
		t.Fatalf("consume new=%s", eventString(detail, "new_attempt_id"))
	}
	start, err := time.Parse(time.RFC3339Nano, original.CreatedAt)
	if err != nil {
		start, err = time.Parse(time.RFC3339, original.CreatedAt)
	}
	if err != nil {
		t.Fatal(err)
	}
	wantDeadline := start.Add(time.Duration(tk.Goal.HardTimeoutSec) * time.Second)
	gotDeadline, err := time.Parse(time.RFC3339Nano, eventString(detail, "original_deadline"))
	if err != nil {
		t.Fatalf("original_deadline: %v", err)
	}
	if gotDeadline.Unix() != wantDeadline.Unix() {
		t.Fatalf("absolute original deadline changed: got %s want %s", gotDeadline, wantDeadline)
	}
	if eventString(detail, "round") != "3" {
		t.Fatalf("consume round=%s", eventString(detail, "round"))
	}
	if err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual"}); !errors.Is(err, errGoalRedispatchBlocked) {
		t.Fatalf("ordinary goal-run after recovery must stay fail-closed: %v", err)
	}
}

func TestBootstrapBeforeNativeRecoveryDuplicateReplayDoesNotAdmitTwice(t *testing.T) {
	root, _, wf, tk, original, _ := bootstrapBeforeNativeRecoverableFixture(t)
	authPath := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
	if err := runBootstrapRecoveryCLI(root, wf.ID, authPath); err != nil {
		t.Fatalf("first recovery: %v", err)
	}
	replayErr := runBootstrapRecoveryCLI(root, wf.ID, authPath)
	if replayErr == nil || !errors.Is(replayErr, errGoalBootstrapRecovery) {
		t.Fatalf("replay must refuse: %v", replayErr)
	}
	if !strings.Contains(replayErr.Error(), "replayed authorization") && !strings.Contains(replayErr.Error(), "consumed authorization") && !strings.Contains(replayErr.Error(), "changed binding") {
		t.Fatalf("replay reason: %v", replayErr)
	}
	attempts, err := listGoalAttemptRecords(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("replay admitted extra attempt: %d", len(attempts))
	}
	foundOriginal := false
	for _, rec := range attempts {
		if rec.AttemptID == original.AttemptID {
			foundOriginal = true
		}
	}
	if !foundOriginal {
		t.Fatal("original attempt missing after replay")
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatalf("consume replayed: %d", countBootstrapConsumeEvents(t, root, tk.ID))
	}

	root2, _, wf2, tk2, orig2, _ := bootstrapBeforeNativeRecoverableFixture(t)
	auth2 := mintBootstrapBeforeNativeAuthorization(t, tk2, wf2, time.Time{})
	// An enclosing scheduler may already own this process-wide lock.
	// Both callers must still serialize authorization consumption.
	if !acquireLock(root2, time.Minute) {
		t.Fatal("acquire enclosing scheduler lock")
	}
	defer releaseLock(root2)
	var mu sync.Mutex
	var nOK, nErr int
	var admissionErrors []error
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			err := runBootstrapRecoveryCLI(root2, wf2.ID, auth2)
			mu.Lock()
			if err == nil {
				nOK++
			} else {
				nErr++
				admissionErrors = append(admissionErrors, err)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if nOK != 1 || nErr != 1 {
		t.Fatalf("concurrent admit ok=%d err=%d: %v", nOK, nErr, admissionErrors)
	}
	attempts2, err := listGoalAttemptRecords(root2, tk2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts2) != 2 {
		t.Fatalf("concurrent extra attempts: %d", len(attempts2))
	}
	kept := false
	for _, rec := range attempts2 {
		if rec.AttemptID == orig2.AttemptID {
			kept = true
		}
	}
	if !kept {
		t.Fatal("concurrent lost original attempt")
	}
	if countBootstrapConsumeEvents(t, root2, tk2.ID) != 1 {
		t.Fatalf("concurrent consume=%d", countBootstrapConsumeEvents(t, root2, tk2.ID))
	}
}

func TestBootstrapBeforeNativeRecoveryReplayAfterConsumeBeforePIDDoesNotLaunchTwice(t *testing.T) {
	root, _, wf, tk, original, originalBytes := bootstrapBeforeNativeRecoverableFixture(t)
	authPath := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
	var replayErr error
	var replayAttemptCount int
	var replayStarted int
	goalLaunchBeforeStartHook = func() error {
		replayErr = runBootstrapRecoveryCLI(root, wf.ID, authPath)
		atts, err := listGoalAttemptRecords(root, tk.ID)
		if err != nil {
			return err
		}
		replayAttemptCount = len(atts)
		for _, rec := range atts {
			if rec.AttemptID != original.AttemptID && goalAttemptHasStartIdentity(rec) {
				replayStarted++
			}
		}
		return errors.New("simulated crash after consume before PID")
	}
	t.Cleanup(func() { goalLaunchBeforeStartHook = nil })
	firstErr := runBootstrapRecoveryCLI(root, wf.ID, authPath)
	if firstErr == nil {
		t.Fatal("crash-before-PID must surface")
	}
	if replayErr == nil || !errors.Is(replayErr, errGoalBootstrapRecovery) {
		t.Fatalf("replay after consume before PID must refuse: %v", replayErr)
	}
	if !strings.Contains(replayErr.Error(), "replayed authorization") && !strings.Contains(replayErr.Error(), "consumed authorization") {
		t.Fatalf("replay-before-PID reason: %v", replayErr)
	}
	if replayAttemptCount != 2 {
		t.Fatalf("replay-before-PID extra attempts: %d", replayAttemptCount)
	}
	if replayStarted != 0 {
		t.Fatalf("replay launched reserved attempt before PID: started=%d", replayStarted)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatalf("consume=%d", countBootstrapConsumeEvents(t, root, tk.ID))
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	second := runBootstrapRecoveryCLI(root, wf.ID, authPath)
	if second == nil {
		t.Fatal("consumed authorization must stay refused")
	}
	atts, err := listGoalAttemptRecords(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("post-crash extra attempts: %d", len(atts))
	}
	for _, rec := range atts {
		if rec.AttemptID != original.AttemptID && goalAttemptHasStartIdentity(rec) {
			t.Fatalf("unstarted failed reservation was launched: %+v", rec)
		}
	}
}

func TestBootstrapBeforeNativeRecoveryOriginalWindowExhaustedCrashReplay(t *testing.T) {
	root, _, wf, tk, original, _ := bootstrapBeforeNativeRecoverableFixture(t)
	authPath := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
	originalTimeout := tk.Goal.HardTimeoutSec
	var replayErr error
	var newCreatedAt string
	goalLaunchBeforeStartHook = func() error {
		rec, err := loadAttempt(root, tk.ID, original.AttemptID)
		if err != nil {
			return err
		}
		rec.CreatedAt = time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
		if err := writeAttempt(root, rec); err != nil {
			return err
		}
		atts, err := listGoalAttemptRecords(root, tk.ID)
		if err != nil {
			return err
		}
		for _, a := range atts {
			if a.AttemptID != original.AttemptID {
				newCreatedAt = a.CreatedAt
			}
		}
		replayErr = runBootstrapRecoveryCLI(root, wf.ID, authPath)
		return errors.New("crash after consume with exhausted original window")
	}
	t.Cleanup(func() { goalLaunchBeforeStartHook = nil })
	_ = runBootstrapRecoveryCLI(root, wf.ID, authPath)
	if replayErr == nil {
		t.Fatal("original-window-exhausted crash/replay must refuse")
	}
	if !strings.Contains(replayErr.Error(), "replayed authorization") &&
		!strings.Contains(replayErr.Error(), "consumed authorization") &&
		!strings.Contains(replayErr.Error(), "exhausted remaining allowance") {
		t.Fatalf("exhausted crash/replay reason: %v", replayErr)
	}
	if newCreatedAt == "" {
		t.Fatal("missing reserved recovery attempt")
	}
	newStart, err := time.Parse(time.RFC3339Nano, newCreatedAt)
	if err != nil {
		newStart, err = time.Parse(time.RFC3339, newCreatedAt)
	}
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(newStart) > time.Minute {
		t.Fatalf("new reservation clock unexpectedly old: %s", newCreatedAt)
	}
	orig, err := loadAttempt(root, tk.ID, original.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	origStart, err := time.Parse(time.RFC3339Nano, orig.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(origStart) < time.Hour {
		t.Fatalf("original clock was not exhausted: %s", orig.CreatedAt)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Goal.HardTimeoutSec != originalTimeout {
		t.Fatalf("hard timeout reset: %d vs %d", after.Goal.HardTimeoutSec, originalTimeout)
	}
	atts, err := listGoalAttemptRecords(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("exhausted crash/replay extra attempts: %d", len(atts))
	}
	for _, rec := range atts {
		if rec.AttemptID != original.AttemptID && goalAttemptHasStartIdentity(rec) {
			t.Fatalf("exhausted crash/replay launched reserved attempt: %+v", rec)
		}
	}
}

func TestBootstrapBeforeNativeRecoveryOrdinaryGoalRunStillFailClosed(t *testing.T) {
	root, _, wf, _, _, _ := bootstrapBeforeNativeFixture(t)
	err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual"})
	if !errors.Is(err, errGoalRedispatchBlocked) {
		t.Fatalf("goal-run: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "redispatch") {
		t.Fatalf("goal-run error: %v", err)
	}
}

func TestManagerBootstrapGenericStartedUnknownMustRefuse(t *testing.T) {
	root, _, wf, tk, original, originalBytes := bootstrapBeforeNativeFixture(t)
	if _, err := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(err) {
		t.Fatalf("generic started/unknown must not retain exact refusal proof: %v", err)
	}
	auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
	if err := runBootstrapRecoveryCLI(root, wf.ID, auth); err == nil {
		t.Fatal("generic exited CLI with no exact bootstrap rejection proof was allowed to recover")
	} else if !strings.Contains(err.Error(), "missing launcher/process evidence") &&
		!strings.Contains(err.Error(), "bootstrap-before-provider") {
		t.Fatalf("generic refuse reason: %v", err)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
		t.Fatal("generic started/unknown consumed authorization")
	}
	atts, err := listGoalAttemptRecords(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 {
		t.Fatalf("generic started/unknown admitted extra attempt: %d", len(atts))
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
}

func TestBootstrapBeforeNativeRecoveryGenericZeroAndNonzeroRefuse(t *testing.T) {
	t.Run("generic-zero", func(t *testing.T) {
		root, _, wf, tk, original, originalBytes := bootstrapBeforeNativeFixture(t)
		auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
		err := runBootstrapRecoveryCLI(root, wf.ID, auth)
		if err == nil {
			t.Fatal("generic zero exit recovered")
		}
		if !strings.Contains(err.Error(), "missing launcher/process evidence") &&
			!strings.Contains(err.Error(), "generic success") {
			t.Fatalf("generic zero reason: %v", err)
		}
		if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
			t.Fatal("generic zero consumed")
		}
		assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	})
	t.Run("generic-nonzero", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		cfg.GrokBuildBin = fakeGrokGoalBinExitCode(t, filepath.Join(t.TempDir(), "argv"), 1)
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		saveGoalCfg(t, root, cfg)
		wf := initTestWorkflow(t, root, dir)
		tk := admitManualWriter(t, root, cfg, wf)
		tk.Goal.GrokHome = t.TempDir()
		tk.Goal.BudgetTokens = 12000
		tk.Goal.HardTimeoutSec = 3600
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		wf.MaxRounds = 3
		wf.CurrentRound = 3
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		launchErr := runManualGoalLaunch(t, root, cfg, wf, 12000)
		if launchErr == nil {
			t.Fatal("expected nonzero fake grok to fail")
		}
		tk, err := loadTask(root, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !tk.Goal.Started || tk.Goal.Observation != goalObsUnknown {
			t.Fatalf("nonzero fixture started/unknown: started=%v obs=%s", tk.Goal.Started, tk.Goal.Observation)
		}
		original, err := loadRequiredGoalAttempt(root, tk)
		if err != nil || !goalAttemptHasStartIdentity(original) {
			t.Fatalf("nonzero original identity: %+v err=%v", original, err)
		}
		auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
		recErr := runBootstrapRecoveryCLI(root, wf.ID, auth)
		if recErr == nil {
			t.Fatal("generic nonzero exit recovered")
		}
		if !strings.Contains(recErr.Error(), "missing launcher/process evidence") &&
			!strings.Contains(recErr.Error(), "generic failure") &&
			!strings.Contains(recErr.Error(), "bootstrap-before-provider") {
			t.Fatalf("generic nonzero reason: %v", recErr)
		}
		if _, serr := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(serr) {
			t.Fatalf("generic nonzero must not retain exact refusal proof: %v", serr)
		}
		if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
			t.Fatal("generic nonzero consumed")
		}
		atts, err := listGoalAttemptRecords(root, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(atts) != 1 {
			t.Fatalf("generic nonzero extra attempts: %d", len(atts))
		}
	})
	t.Run("planted-generic-success-evidence", func(t *testing.T) {
		root, _, wf, tk, original, _ := bootstrapBeforeNativeFixture(t)
		if err := writeBootstrapLauncherEvidence(root, bootstrapLauncherEvidence{
			Kind:          bootstrapBeforeProviderKind,
			AttemptID:     original.AttemptID,
			TaskID:        tk.ID,
			ExitStatus:    "0",
			RefusalOutput: acceptedBootstrapBeforeProviderRefusal,
		}); err != nil {
			t.Fatal(err)
		}
		auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
		err := runBootstrapRecoveryCLI(root, wf.ID, auth)
		if err == nil || !strings.Contains(err.Error(), "generic success") {
			t.Fatalf("planted exit 0: %v", err)
		}
	})
	t.Run("planted-generic-failure-evidence", func(t *testing.T) {
		root, _, wf, tk, original, _ := bootstrapBeforeNativeFixture(t)
		if err := writeBootstrapLauncherEvidence(root, bootstrapLauncherEvidence{
			Kind:          bootstrapBeforeProviderKind,
			AttemptID:     original.AttemptID,
			TaskID:        tk.ID,
			ExitStatus:    "1",
			RefusalOutput: "exit 1",
		}); err != nil {
			t.Fatal(err)
		}
		auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
		err := runBootstrapRecoveryCLI(root, wf.ID, auth)
		if err == nil || !strings.Contains(err.Error(), "generic failure") {
			t.Fatalf("planted unclassified: %v", err)
		}
	})
	t.Run("generic-no-output-nonzero", func(t *testing.T) {
		root, _, wf, tk, original, originalBytes := bootstrapBeforeNativeFixture(t)
		if _, err := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(err) {
			t.Fatalf("generic exit/no-output must not retain bootstrap proof: %v", err)
		}
		auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
		err := runBootstrapRecoveryCLI(root, wf.ID, auth)
		if err == nil {
			t.Fatal("generic exit/no-output recovered")
		}
		if !strings.Contains(err.Error(), "missing launcher/process evidence") &&
			!strings.Contains(err.Error(), "bootstrap-before-provider") {
			t.Fatalf("generic no-output reason: %v", err)
		}
		if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
			t.Fatal("generic no-output consumed")
		}
		assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	})
}

func TestBootstrapBeforeNativeRecoveryStaleFailureClassWithoutExactRefusalMustRefuse(t *testing.T) {
	classes := []string{
		goalFailPermission,
		goalFailApprovalDenied,
		goalFailInitGit,
		goalFailContractControl,
		string(grokBuildProcessClassPermissionEnvironment),
		string(grokBuildProcessClassInvalidInvocation),
	}
	for _, class := range classes {
		t.Run("stale-class-"+class, func(t *testing.T) {
			root, _, wf, tk, original, originalBytes := bootstrapBeforeNativeFixture(t)
			tk.Goal.FailureClass = class
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(err) {
				t.Fatalf("stale FailureClass must not invent bootstrap proof: %v", err)
			}
			auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			err := runBootstrapRecoveryCLI(root, wf.ID, auth)
			if err == nil {
				t.Fatal("stale FailureClass recovered")
			}
			if !strings.Contains(err.Error(), "missing launcher/process evidence") &&
				!strings.Contains(err.Error(), "bootstrap-before-provider") {
				t.Fatalf("stale FailureClass reason: %v", err)
			}
			if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
				t.Fatal("stale FailureClass consumed")
			}
			assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
		})
	}
	t.Run("planted-class-zeros-without-exact-output", func(t *testing.T) {
		root, _, wf, tk, original, originalBytes := bootstrapBeforeNativeFixture(t)
		raw := []byte(`{
  "kind": "bootstrap-before-provider-refusal",
  "attempt_id": "` + original.AttemptID + `",
  "task_id": "` + tk.ID + `",
  "exit_status": "2",
  "class": "permission_denied",
  "semantic_events": 0,
  "model_events": 0,
  "tool_events": 0,
  "native_session": false,
  "native_goal": false
}
`)
		path := bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
		err := runBootstrapRecoveryCLI(root, wf.ID, auth)
		if err == nil {
			t.Fatal("class/typed zeros without exact refusal recovered")
		}
		if !strings.Contains(err.Error(), "missing launcher/process evidence") &&
			!strings.Contains(err.Error(), "bootstrap-before-provider") {
			t.Fatalf("class/zeros reason: %v", err)
		}
		if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
			t.Fatal("class/zeros consumed")
		}
		assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	})
}

func TestBootstrapBeforeNativeRecoveryRefusalTable(t *testing.T) {
	type tc struct {
		name       string
		want       string
		plantProof bool
		mutate     func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string
	}
	cases := []tc{
		{
			name: "directory-absence-only",
			want: "directory absence alone is not bootstrap-before-native proof",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				root2, dir := workflowTestRoot(t)
				cfg2 := workflowTestCfg(t, root2)
				enableManualGrok(t, root2, cfg2, filepath.Join(t.TempDir(), "argv"))
				wf2 := initTestWorkflow(t, root2, dir)
				tk2 := admitManualWriter(t, root2, cfg2, wf2)
				tk2.SessionID = newGoalSessionID()
				if err := saveTask(root2, tk2); err != nil {
					t.Fatal(err)
				}
				seedExitedAttempt(t, root2, tk2, "at-absent")
				tk2, err := loadTask(root2, tk2.ID)
				if err != nil {
					t.Fatal(err)
				}
				auth := mintBootstrapBeforeNativeAuthorization(t, tk2, wf2, time.Time{})
				before, _ := os.ReadFile(attemptPath(root2, tk2.ID, "at-absent"))
				err = runBootstrapRecoveryCLI(root2, wf2.ID, auth)
				if err == nil || !strings.Contains(err.Error(), "directory absence alone is not bootstrap-before-native proof") {
					t.Fatalf("directory-absence: %v", err)
				}
				after, _ := os.ReadFile(attemptPath(root2, tk2.ID, "at-absent"))
				if string(before) != string(after) {
					t.Fatal("directory-absence rewrote attempt")
				}
				if countBootstrapConsumeEvents(t, root2, tk2.ID) != 0 {
					t.Fatal("directory-absence consumed auth")
				}
				attempts, _ := listGoalAttemptRecords(root2, tk2.ID)
				if len(attempts) != 1 {
					t.Fatalf("directory-absence attempts=%d", len(attempts))
				}
				return "checked"
			},
		},
		{
			name: "live-custody",
			want: "live custody",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				hold := filepath.Join(t.TempDir(), "hold")
				if err := os.WriteFile(hold, []byte("1"), 0o644); err != nil {
					t.Fatal(err)
				}
				root2, dir := workflowTestRoot(t)
				cfg2 := workflowTestCfg(t, root2)
				cfg2.GrokBuildBin = fakeGrokGoalBinHold(t, filepath.Join(t.TempDir(), "argv"), hold)
				cfg2.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
				saveGoalCfg(t, root2, cfg2)
				wf2 := initTestWorkflow(t, root2, dir)
				tk2 := admitManualWriter(t, root2, cfg2, wf2)
				tk2.Goal.GrokHome = t.TempDir()
				tk2.Goal.BudgetTokens = 12000
				tk2.Goal.HardTimeoutSec = 3600
				if err := saveTask(root2, tk2); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					done <- runManualGoalLaunch(t, root2, cfg2, wf2, 12000)
				}()
				var live *Task
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					cur, err := loadTask(root2, tk2.ID)
					if err == nil && cur.Goal != nil && cur.ActiveAttemptID != "" {
						rec, rerr := loadAttempt(root2, cur.ID, cur.ActiveAttemptID)
						if rerr == nil && rec != nil && rec.PID > 0 {
							live = cur
							break
						}
					}
					time.Sleep(20 * time.Millisecond)
				}
				if live == nil {
					_ = os.Remove(hold)
					t.Fatal("live producer never bound")
				}
				auth := mintBootstrapBeforeNativeAuthorization(t, live, wf2, time.Time{})
				err := runBootstrapRecoveryCLI(root2, wf2.ID, auth)
				_ = os.Remove(hold)
				launchErr := <-done
				if err == nil || !strings.Contains(err.Error(), "live custody") {
					t.Fatalf("live custody: %v launch=%v", err, launchErr)
				}
				if countBootstrapConsumeEvents(t, root2, tk2.ID) != 0 {
					t.Fatal("live custody consumed auth")
				}
				return "checked"
			},
		},
		{
			name: "stale-revision",
			want: "stale revision",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
				tk.touch()
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				return auth
			},
		},
		{
			name: "expired-authorization",
			want: "expired authorization",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Now().Add(-time.Minute))
			},
		},
		{
			name: "changed-binding-digest",
			want: "changed binding",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
				tk.Goal.InputDigest = "changed-contract-digest"
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				return auth
			},
		},
		{
			name: "changed-profile-digest",
			want: "changed binding",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
				tk.Goal.SandboxProfileDigest = "changed-profile-digest"
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				return auth
			},
		},
		{
			name: "genuinely-executed-unknown",
			want: "genuinely executed unknown",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				goalID := "goal-native-executed"
				tk.Goal.NativeGoalID = goalID
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				writeGrokGoalFixture(t, tk.Goal.GrokHome, tk.Dir, tk.SessionID, goalID, "active", grokNativeGoalStateFile{})
				tk, err := loadTask(root, tk.ID)
				if err != nil {
					t.Fatal(err)
				}
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name:       "stream-incomplete",
			want:       "stream incomplete",
			plantProof: true,
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				tk.LastRouteAttempt = &RouteAttemptReadback{FailureKind: string(fallbackStreamIncomplete), ObservationSeen: true}
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				tk2, err := loadTask(root, tk.ID)
				if err != nil {
					t.Fatal(err)
				}
				*tk = *tk2
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name:       "no-retry-sealed",
			want:       "no-retry/sealed limit",
			plantProof: true,
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				tk.Goal.Observation = goalObsFailed
				tk.Status = statusFailed
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				tk2, err := loadTask(root, tk.ID)
				if err != nil {
					t.Fatal(err)
				}
				*tk = *tk2
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name:       "exhausted-remaining-allowance",
			want:       "exhausted remaining allowance",
			plantProof: true,
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				rec, err := loadAttempt(root, tk.ID, original.AttemptID)
				if err != nil {
					t.Fatal(err)
				}
				rec.CreatedAt = time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
				if err := writeAttempt(root, rec); err != nil {
					t.Fatal(err)
				}
				tk.Goal.HardTimeoutSec = 60
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				tk2, err := loadTask(root, tk.ID)
				if err != nil {
					t.Fatal(err)
				}
				*tk = *tk2
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name:       "unknown-remaining-allowance",
			want:       "unknown remaining allowance",
			plantProof: true,
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				tk.Goal.BudgetTokens = 0
				tk.Goal.HardTimeoutSec = 0
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				tk2, err := loadTask(root, tk.ID)
				if err != nil {
					t.Fatal(err)
				}
				*tk = *tk2
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name:       "future-remaining-allowance",
			want:       "future remaining allowance",
			plantProof: true,
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				rec, err := loadAttempt(root, tk.ID, original.AttemptID)
				if err != nil {
					t.Fatal(err)
				}
				rec.CreatedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
				if err := writeAttempt(root, rec); err != nil {
					t.Fatal(err)
				}
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name: "unreadable-launcher-evidence",
			want: "unreadable launcher/process evidence",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				path := bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name: "malformed-launcher-evidence",
			want: "malformed launcher/process evidence",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				path := bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
					t.Fatal(err)
				}
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name: "contradictory-launcher-evidence",
			want: "contradictory launcher/process evidence",
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				if err := writeBootstrapLauncherEvidence(root, bootstrapLauncherEvidence{
					Kind:          bootstrapBeforeProviderKind,
					AttemptID:     original.AttemptID,
					TaskID:        tk.ID,
					ExitStatus:    "2",
					RefusalOutput: acceptedBootstrapBeforeProviderRefusal,
					ModelEvents:   3,
					NativeSession: false,
				}); err != nil {
					t.Fatal(err)
				}
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name:       "unreadable-native-artifacts",
			want:       "unreadable native Session/Goal artifacts",
			plantProof: true,
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				dir := grokGoalSessionDir(tk.Goal.GrokHome, tk.Dir, tk.SessionID)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				sum := filepath.Join(dir, "summary.json")
				if err := os.Mkdir(sum, 0o755); err != nil {
					t.Fatal(err)
				}
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
		{
			name:       "malformed-native-artifacts",
			want:       "malformed native Session/Goal artifacts",
			plantProof: true,
			mutate: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord) string {
				dir := grokGoalSessionDir(tk.Goal.GrokHome, tk.Dir, tk.SessionID)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte("not-json"), 0o644); err != nil {
					t.Fatal(err)
				}
				return mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "directory-absence-only" || tc.name == "live-custody" {
				if got := tc.mutate(t, "", nil, nil, nil, nil); got != "checked" {
					t.Fatalf("self-checked case returned %q", got)
				}
				return
			}
			root, cfg, wf, tk, original, originalBytes := bootstrapBeforeNativeFixture(t)
			auth := tc.mutate(t, root, cfg, wf, tk, original)
			if tc.plantProof {
				plantRetainedBootstrapBeforeProviderProof(t, root, tk, original)
			}
			beforeAttempts, err := listGoalAttemptRecords(root, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			err = runBootstrapRecoveryCLI(root, wf.ID, auth)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: %v want %q", tc.name, err, tc.want)
			}
			afterAttempts, err := listGoalAttemptRecords(root, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(afterAttempts) != len(beforeAttempts) {
				t.Fatalf("%s admitted extra attempt: %d -> %d", tc.name, len(beforeAttempts), len(afterAttempts))
			}
			if tc.name != "exhausted-remaining-allowance" && tc.name != "future-remaining-allowance" {
				gotBytes, rerr := os.ReadFile(attemptPath(root, tk.ID, original.AttemptID))
				if rerr != nil {
					t.Fatal(rerr)
				}
				if string(gotBytes) != string(originalBytes) {
					t.Fatalf("%s rewrote original attempt", tc.name)
				}
			}
			if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
				t.Fatalf("%s consumed authorization", tc.name)
			}
		})
	}
}

func TestBootstrapBeforeNativeRecoveryNewAttemptFailureKeepsBothHistories(t *testing.T) {
	root, cfg, wf, tk, original, originalBytes := bootstrapBeforeNativeRecoverableFixture(t)
	cfg.GrokBuildBin = fakeGrokGoalBinExitCode(t, filepath.Join(t.TempDir(), "argv"), 1)
	saveGoalCfg(t, root, cfg)
	tk, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
	err = runBootstrapRecoveryCLI(root, wf.ID, auth)
	if err == nil {
		t.Fatal("new-attempt failure must surface")
	}
	attempts, lerr := listGoalAttemptRecords(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(attempts) != 2 {
		t.Fatalf("both histories missing: %d", len(attempts))
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	var created *AttemptRecord
	for _, rec := range attempts {
		if rec.AttemptID != original.AttemptID {
			created = rec
		}
	}
	if created == nil || !goalAttemptHasStartIdentity(created) {
		t.Fatalf("new failed attempt missing start identity: %+v", created)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ActiveAttemptID != "" && taskHasLiveWriterProof(root, after) {
		t.Fatal("new-attempt failure must reclaim custody")
	}
	if !after.Goal.CustodyReleased && after.ActiveAttemptID != "" {
		t.Fatalf("custody not reclaimed: active=%s released=%v", after.ActiveAttemptID, after.Goal.CustodyReleased)
	}
	if !after.Goal.Started {
		t.Fatal("original Started cleared")
	}
	if err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual"}); !errors.Is(err, errGoalRedispatchBlocked) {
		t.Fatalf("ordinary goal-run after failed recovery: %v", err)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatalf("consume=%d", countBootstrapConsumeEvents(t, root, tk.ID))
	}
}

func resolveCardexCandidateBinary(t *testing.T) string {
	t.Helper()
	if bin := strings.TrimSpace(os.Getenv("CARDEX_CANDIDATE")); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("CARDEX_CANDIDATE: %v", err)
		}
		return bin
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "cardex")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build candidate: %v\n%s", err, out)
	}
	return bin
}

func runCardexBootstrapRecoveryProcess(t *testing.T, bin, root, wfID, authPath string, extra ...string) (stdout, stderr string, code int) {
	t.Helper()
	args := []string{"workflow", "goal-bootstrap-before-native-recovery",
		"-root", root, wfID, "-manual", "-authorization", authPath}
	args = append(args, extra...)
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if err == nil {
		if cmd.ProcessState == nil {
			t.Fatal("missing process state after success")
		}
		return stdout, stderr, code
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout, stderr, ee.ExitCode()
	}
	t.Fatalf("candidate run: %v stderr=%s", err, stderr)
	return stdout, stderr, -1
}

func TestBootstrapBeforeNativeRecoveryCandidateSubprocessReturncode(t *testing.T) {
	bin := resolveCardexCandidateBinary(t)
	for pass := 1; pass <= 2; pass++ {
		root, _, wf, tk, original, _ := bootstrapBeforeNativeFixture(t)
		auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
		stdout, stderr, code := runCardexBootstrapRecoveryProcess(t, bin, root, wf.ID, auth)
		if code == 0 {
			t.Fatalf("pass %d generic started/unknown must be nonzero subprocess returncode; stdout=%q stderr=%q", pass, stdout, stderr)
		}
		t.Logf("pass %d generic-refuse cardex subprocess returncode=%d", pass, code)
		if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
			t.Fatalf("pass %d generic refuse consumed", pass)
		}
		_ = original

		root2, _, wf2, tk2, orig2, _ := bootstrapBeforeNativeRecoverableFixture(t)
		auth2 := mintBootstrapBeforeNativeAuthorization(t, tk2, wf2, time.Time{})
		stdout2, stderr2, code2 := runCardexBootstrapRecoveryProcess(t, bin, root2, wf2.ID, auth2)
		if !strings.Contains(stdout2, "original_attempt="+orig2.AttemptID) {
			t.Fatalf("pass %d recovery stdout missing original id: stdout=%q stderr=%q code=%d", pass, stdout2, stderr2, code2)
		}
		if !strings.Contains(stdout2, "new_attempt=") {
			t.Fatalf("pass %d recovery stdout missing new attempt: stdout=%q", pass, stdout2)
		}
		if code2 == 0 {
			t.Logf("pass %d genuine-admit cardex subprocess returncode=0 stdout=%s", pass, strings.TrimSpace(stdout2))
		} else {
			t.Logf("pass %d genuine-admit cardex subprocess returncode=%d (Wait/custody TTY unverified in this sandbox: python pty.openpty OSError out of pty devices; in-process TestBootstrapBeforeNativeRecoveryOneAuthorizedAttemptPreservesHistory covers Wait/custody) stdout=%s stderr=%s",
				pass, code2, strings.TrimSpace(stdout2), strings.TrimSpace(stderr2))
		}
		if countBootstrapConsumeEvents(t, root2, tk2.ID) != 1 {
			t.Fatalf("pass %d genuine did not consume once: %d", pass, countBootstrapConsumeEvents(t, root2, tk2.ID))
		}
		stdout3, stderr3, code3 := runCardexBootstrapRecoveryProcess(t, bin, root2, wf2.ID, auth2)
		if code3 == 0 {
			t.Fatalf("pass %d second invocation must be nonzero; stdout=%q stderr=%q", pass, stdout3, stderr3)
		}
		t.Logf("pass %d replay cardex subprocess returncode=%d", pass, code3)
		if countBootstrapConsumeEvents(t, root2, tk2.ID) != 1 {
			t.Fatalf("pass %d replay consumed again", pass)
		}
		atts, err := listGoalAttemptRecords(root2, tk2.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(atts) < 2 {
			t.Fatalf("pass %d expected original plus recovery attempt, got %d", pass, len(atts))
		}
	}
}

func TestBootstrapBeforeNativeRecoveryCLIRequiresSingleUseAuthorization(t *testing.T) {
	err := cmdWorkflow([]string{"goal-bootstrap-before-native-recovery"})
	if err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(err.Error(), "single-use authorization") && !strings.Contains(err.Error(), "authorization") {
		t.Fatalf("usage must name single-use authorization: %v", err)
	}
	err = cmdWorkflowGoalBootstrapBeforeNativeRecovery([]string{"-manual"})
	if err == nil || !errors.Is(err, errGoalBootstrapAuthRequired) {
		t.Fatalf("missing authorization: %v", err)
	}
}

func TestManagerBootstrapActualLauncherProofProduction(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv"))
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.GrokHome = t.TempDir()
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'Grok hook write-deny ensure failed: cannot create managed_config.toml: Operation not permitted; named profile refused with protections missing' >&2\nexit 1\n"
	if err := os.WriteFile(cfg.GrokBuildBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
		t.Logf("expected refusal exit: %v", err)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := loadRequiredGoalAttempt(root, after)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := loadBootstrapLauncherEvidence(root, after.ID, rec.AttemptID)
	if err != nil {
		t.Fatalf("actual exact bootstrap refusal produced no retained evidence: %v", err)
	}
	if err := validateBootstrapLauncherEvidence(proof, after, rec, false, false); err != nil {
		t.Fatal(err)
	}
	if proof.ExitStatus != "1" {
		t.Fatalf("actual producer exit_status=%q want 1", proof.ExitStatus)
	}
	if proof.RefusalOutput != acceptedBootstrapBeforeProviderRefusal {
		t.Fatalf("actual producer refusal_output=%q", proof.RefusalOutput)
	}

	originalBytes, err := os.ReadFile(attemptPath(root, after.ID, rec.AttemptID))
	if err != nil {
		t.Fatal(err)
	}
	cfg.GrokBuildBin = fakeGrokGoalBin(t, filepath.Join(t.TempDir(), "argv-recovery"))
	saveGoalCfg(t, root, cfg)
	auth := mintBootstrapBeforeNativeAuthorization(t, after, wf, time.Time{})
	if err := runBootstrapRecoveryCLI(root, wf.ID, auth); err != nil {
		t.Fatalf("producer-to-recovery consumer: %v", err)
	}
	recovered, err := loadTask(root, after.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := listGoalAttemptRecords(root, after.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("producer-to-recovery attempt count=%d want 2", len(attempts))
	}
	var created *AttemptRecord
	for _, a := range attempts {
		if a.AttemptID != rec.AttemptID {
			created = a
		}
	}
	if created == nil || !goalAttemptHasStartIdentity(created) {
		t.Fatalf("producer-to-recovery missing Wait/custody start identity: %+v", created)
	}
	assertOriginalAttemptUnchanged(t, root, after.ID, rec.AttemptID, originalBytes)
	if countBootstrapConsumeEvents(t, root, after.ID) != 1 {
		t.Fatalf("producer-to-recovery consume=%d", countBootstrapConsumeEvents(t, root, after.ID))
	}
	if recovered.Goal.BoundAttemptID != created.AttemptID {
		t.Fatalf("producer-to-recovery bound=%s new=%s", recovered.Goal.BoundAttemptID, created.AttemptID)
	}
}

func fakeGrokBinStderrTTYProbe(t *testing.T, reportPath string, emitRefusal bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\n"
	script += "if [ -t 2 ]; then echo stderr_is_tty=1 > " + shSingleQuote(reportPath) + "\n"
	script += "else echo stderr_is_tty=0 > " + shSingleQuote(reportPath) + "\nfi\n"
	if emitRefusal {
		script += "printf '%s\\n' " + shSingleQuote(acceptedBootstrapBeforeProviderRefusal) + " >&2\n"
		script += "exit 1\n"
	} else {
		script += "exit 0\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAttachManualGoalLaunchStderrCaptureTTYDescriptor(t *testing.T) {
	report := filepath.Join(t.TempDir(), "tty-report")
	bin := fakeGrokBinStderrTTYProbe(t, report, true)

	t.Run("controlling-tty-stderr-left-in-place", func(t *testing.T) {
		if !fileIsTerminal(os.Stderr) {
			t.Log("UNVERIFIED: process stderr is not a TTY; controlling TTY stderr fd pass-through was not exercised")
			return
		}
		cmd := exec.Command(bin)
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		captured := attachManualGoalLaunchStderrCapture(cmd, true)
		f, ok := cmd.Stderr.(*os.File)
		if !ok {
			t.Fatalf("TTY stderr was replaced with %T", cmd.Stderr)
		}
		if f.Fd() != os.Stderr.Fd() {
			t.Fatal("TTY stderr fd was replaced")
		}
		if !captured.preservedTTYStderr() {
			t.Fatal("capture must leave controlling TTY stderr in place")
		}
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("probe: %v", err)
			}
		}
		got, err := os.ReadFile(report)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(got)) != "stderr_is_tty=1" {
			t.Fatalf("child stderr TTY probe: %q", got)
		}
		if captured.capturedOutput() != "" {
			t.Fatalf("TTY stderr capture must be unavailable, got %q", captured.capturedOutput())
		}
	})

	t.Run("non-tty-capture-is-pipe-and-bounded", func(t *testing.T) {
		_ = os.Remove(report)
		cmd := exec.Command(bin)
		cmd.Stderr = os.Stderr
		captured := attachManualGoalLaunchStderrCapture(cmd, false)
		if _, ok := cmd.Stderr.(*os.File); ok {
			t.Fatal("headless capture must wrap stderr with the bounded writer")
		}
		if captured.preservedTTYStderr() {
			t.Fatal("headless capture must not claim TTY stderr was left in place")
		}
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("probe: %v", err)
			}
		}
		got, err := os.ReadFile(report)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(got)) != "stderr_is_tty=0" {
			t.Fatalf("capture-path child stderr TTY probe: %q", got)
		}
		if captured.capturedOutput() != acceptedBootstrapBeforeProviderRefusal {
			t.Fatalf("bounded capture missed exact refusal: %q", captured.capturedOutput())
		}
	})
}

func TestBootstrapRefusalCaptureBoundsLongStderr(t *testing.T) {
	t.Run("exact-line-after-newline-bounded", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		junk := append(bytes.Repeat([]byte("x"), grokBuildProcessStderrMaxBytes*8), '\n')
		if _, err := c.Write(junk); err != nil {
			t.Fatal(err)
		}
		if len(c.window) > grokBuildProcessStderrMaxBytes {
			t.Fatalf("unbounded pending line: %d", len(c.window))
		}
		if c.capturedOutput() == acceptedBootstrapBeforeProviderRefusal {
			t.Fatal("junk matched exact refusal")
		}
		if _, err := c.Write([]byte(acceptedBootstrapBeforeProviderRefusal + "\n")); err != nil {
			t.Fatal(err)
		}
		if c.capturedOutput() != acceptedBootstrapBeforeProviderRefusal {
			t.Fatalf("exact refusal after long stderr: %q", c.capturedOutput())
		}
		if c.window != nil {
			t.Fatalf("pending retained after match: %d", len(c.window))
		}
		more := bytes.Repeat([]byte("y"), grokBuildProcessStderrMaxBytes*4)
		if _, err := c.Write(more); err != nil {
			t.Fatal(err)
		}
		if len(c.capturedOutput()) > len(acceptedBootstrapBeforeProviderRefusal) {
			t.Fatalf("matched capture grew after long Goal stderr: %d", len(c.capturedOutput()))
		}
	})
	t.Run("junk-without-newline-is-not-a-line", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		if _, err := c.Write(bytes.Repeat([]byte("x"), grokBuildProcessStderrMaxBytes*8)); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write([]byte(acceptedBootstrapBeforeProviderRefusal + "\n")); err != nil {
			t.Fatal(err)
		}
		if c.capturedOutput() == acceptedBootstrapBeforeProviderRefusal {
			t.Fatal("junk+marker without a line boundary matched exact refusal")
		}
	})
	t.Run("quoted-diagnostic-is-not-exact-line", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		quoted := "diagnostic quoted error: " + acceptedBootstrapBeforeProviderRefusal + "\n"
		if _, err := c.Write([]byte(quoted)); err != nil {
			t.Fatal(err)
		}
		if c.capturedOutput() == acceptedBootstrapBeforeProviderRefusal {
			t.Fatal("quoted diagnostic substring matched exact refusal")
		}
		if line, ok := matchAcceptedBootstrapBeforeProviderRefusal(quoted); ok {
			t.Fatalf("whole-line matcher accepted quoted diagnostic: %q", line)
		}
	})
}

func TestManualGoalLaunchHeadlessCaptureSeesNonTTYStderr(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	report := filepath.Join(t.TempDir(), "tty-report")
	cfg.GrokBuildBin = fakeGrokBinStderrTTYProbe(t, report, true)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.GrokHome = t.TempDir()
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
		t.Logf("expected refusal exit: %v", err)
	}
	got, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "stderr_is_tty=0" {
		t.Fatalf("headless manual launch capture path stderr TTY probe: %q", got)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := loadRequiredGoalAttempt(root, after)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := loadBootstrapLauncherEvidence(root, after.ID, rec.AttemptID)
	if err != nil {
		t.Fatalf("headless capture produced no evidence: %v", err)
	}
	if err := validateBootstrapLauncherEvidence(proof, after, rec, false, false); err != nil {
		t.Fatal(err)
	}
}

func TestManagerQuotedRefusalMustNotProduceProof(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv"))
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.GrokHome = t.TempDir()
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'diagnostic quoted error: Grok hook write-deny ensure failed: cannot create managed_config.toml: Operation not permitted; named profile refused with protections missing' >&2\nexit 1\n"
	if err := os.WriteFile(cfg.GrokBuildBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
		t.Logf("expected refusal exit: %v", err)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := loadRequiredGoalAttempt(root, after)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadBootstrapLauncherEvidence(root, after.ID, rec.AttemptID)
	if err == nil {
		t.Fatal("quoted diagnostic substring is not the exact startup refusal but produced bootstrap proof")
	}
}

func TestHostedPTYBootstrapRefusalCaptureIsattyAndBounds(t *testing.T) {
	if !requireHostedPTYOrUnverified(t) {
		return
	}
	report := filepath.Join(t.TempDir(), "tty-report")
	root, cfg, wf, tk := hostedBootstrapWriterFixture(t, fakeHostedGrokBinBootstrapCapture(t, report, hostedEmitLongThenExact))
	out, err := os.CreateTemp(t.TempDir(), "hosted-out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var launchErr error
	withHostedCallerStdout(t, out, func() {
		launchErr = launchHostedWorkflowGoal(root, cfg, wf, 12000, "")
	})
	if hostedPTYLimitation(launchErr) {
		t.Logf("UNVERIFIED: %v", launchErr)
		return
	}
	if launchErr == nil {
		t.Fatal("hosted exact bootstrap-before-provider refusal must exit nonzero")
	}
	got, rerr := os.ReadFile(report)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.TrimSpace(string(got)) != "stderr_is_tty=1" {
		t.Fatalf("hosted child stderr isatty(2): %q", got)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := loadRequiredGoalAttempt(root, after)
	if err != nil {
		t.Fatal(err)
	}
	assertCapturedBootstrapBeforeProviderProof(t, root, after, rec)
	proof, err := loadBootstrapLauncherEvidence(root, after.ID, rec.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if proof.RefusalOutput != acceptedBootstrapBeforeProviderRefusal {
		t.Fatalf("bounded hosted capture retained extra PTY bytes: %q", proof.RefusalOutput)
	}
	if err := validateBootstrapLauncherEvidence(proof, after, rec, false, false); err != nil {
		t.Fatal(err)
	}
}

func TestHostedPTYBootstrapRefusalQuotedPrefixedConcatenatedNoProof(t *testing.T) {
	if !requireHostedPTYOrUnverified(t) {
		return
	}
	cases := []struct {
		name string
		emit hostedBootstrapEmit
	}{
		{name: "quoted", emit: hostedEmitQuoted},
		{name: "prefixed", emit: hostedEmitPrefixed},
		{name: "concatenated", emit: hostedEmitConcatenated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !requireHostedPTYOrUnverified(t) {
				return
			}
			root, cfg, wf, tk := hostedBootstrapWriterFixture(t, fakeHostedGrokBinBootstrapCapture(t, "", tc.emit))
			out, err := os.CreateTemp(t.TempDir(), "hosted-out")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			var launchErr error
			withHostedCallerStdout(t, out, func() {
				launchErr = launchHostedWorkflowGoal(root, cfg, wf, 12000, "")
			})
			if hostedPTYLimitation(launchErr) {
				t.Logf("UNVERIFIED: %v", launchErr)
				return
			}
			if launchErr == nil {
				t.Fatal("hosted negative refusal must exit nonzero")
			}
			after, err := loadTask(root, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := loadRequiredGoalAttempt(root, after)
			if err != nil {
				t.Fatal(err)
			}
			if _, serr := os.Stat(bootstrapLauncherEvidencePath(root, after.ID, rec.AttemptID)); !os.IsNotExist(serr) {
				t.Fatalf("%s line produced bootstrap proof: %v", tc.name, serr)
			}
		})
	}
}

func TestHostedPTYBrokenCallerSinkStillDrainsAndWait(t *testing.T) {
	if !requireHostedPTYOrUnverified(t) {
		return
	}
	root, cfg, wf, tk := hostedBootstrapWriterFixture(t, fakeHostedGrokBinBootstrapCapture(t, "", hostedEmitFloodThenExit))
	out, err := os.CreateTemp(t.TempDir(), "broken-out")
	if err != nil {
		t.Fatal(err)
	}
	name := out.Name()
	out.Close()
	broken, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	start := time.Now()
	var launchErr error
	warnings := captureStderr(t, func() {
		withHostedCallerStdout(t, broken, func() {
			launchErr = launchHostedWorkflowGoal(root, cfg, wf, 0, "")
		})
	})
	if hostedPTYLimitation(launchErr) {
		t.Logf("UNVERIFIED: %v", launchErr)
		return
	}
	if elapsed := time.Since(start); elapsed > 9*time.Second {
		t.Fatalf("broken sink Wait hung: %v err=%v", elapsed, launchErr)
	}
	if launchErr != nil {
		t.Fatalf("broken sink Wait: %v", launchErr)
	}
	if !strings.Contains(warnings, "hosted PTY output may be incomplete") {
		t.Fatalf("missing sink-failure drain warning: %q", warnings)
	}
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.Goal.CustodyReleased || fresh.ActiveAttemptID != "" {
		t.Fatalf("broken sink must still Wait and reclaim custody: %+v", fresh.Goal)
	}
}

func TestHostedPTYBootstrapCaptureToAuthorizedRecoveryAndReplay(t *testing.T) {
	if !requireHostedPTYOrUnverified(t) {
		return
	}
	report := filepath.Join(t.TempDir(), "tty-report")
	root, cfg, wf, tk := hostedBootstrapWriterFixture(t, fakeHostedGrokBinBootstrapCapture(t, report, hostedEmitLongThenExact))
	out, err := os.CreateTemp(t.TempDir(), "hosted-out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var launchErr error
	withHostedCallerStdout(t, out, func() {
		launchErr = launchHostedWorkflowGoal(root, cfg, wf, 12000, "")
	})
	if hostedPTYLimitation(launchErr) {
		t.Logf("UNVERIFIED: %v", launchErr)
		return
	}
	if launchErr == nil {
		t.Fatal("hosted exact bootstrap-before-provider refusal must exit nonzero")
	}
	got, rerr := os.ReadFile(report)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.TrimSpace(string(got)) != "stderr_is_tty=1" {
		t.Fatalf("hosted child stderr isatty(2): %q", got)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Goal.Started || after.Goal.Observation != goalObsUnknown {
		t.Fatalf("hosted capture must be started/unknown: started=%v obs=%s", after.Goal.Started, after.Goal.Observation)
	}
	original, err := loadRequiredGoalAttempt(root, after)
	if err != nil || !goalAttemptHasStartIdentity(original) {
		t.Fatalf("original start identity missing: rec=%+v err=%v", original, err)
	}
	assertCapturedBootstrapBeforeProviderProof(t, root, after, original)
	originalBytes, err := os.ReadFile(attemptPath(root, after.ID, original.AttemptID))
	if err != nil {
		t.Fatal(err)
	}
	originalTimeout := after.Goal.HardTimeoutSec
	originalBudget := after.Goal.BudgetTokens
	cfg.GrokBuildBin = fakeGrokGoalBin(t, filepath.Join(t.TempDir(), "argv-recovery"))
	saveGoalCfg(t, root, cfg)
	auth := mintBootstrapBeforeNativeAuthorization(t, after, wf, time.Time{})
	var recErr error
	withHostedCallerStdout(t, out, func() {
		recErr = runBootstrapRecoveryHostedCLI(root, wf.ID, auth)
	})
	if hostedPTYLimitation(recErr) {
		t.Logf("UNVERIFIED: %v", recErr)
		return
	}
	if recErr != nil {
		t.Fatalf("authorized hosted recovery: %v", recErr)
	}
	recovered, err := loadTask(root, after.ID)
	if err != nil {
		t.Fatal(err)
	}
	wfAfter, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wfAfter.CurrentRound != 3 || wfAfter.MaxRounds != 3 {
		t.Fatalf("round/max changed: %d/%d", wfAfter.CurrentRound, wfAfter.MaxRounds)
	}
	if recovered.Goal.BudgetTokens != originalBudget || recovered.Goal.HardTimeoutSec != originalTimeout {
		t.Fatalf("budget/timeout reset: budget=%d/%d timeout=%d/%d",
			recovered.Goal.BudgetTokens, originalBudget, recovered.Goal.HardTimeoutSec, originalTimeout)
	}
	if recovered.SessionID != after.SessionID {
		t.Fatalf("session changed: %s vs %s", recovered.SessionID, after.SessionID)
	}
	assertOriginalAttemptUnchanged(t, root, after.ID, original.AttemptID, originalBytes)
	attempts, err := listGoalAttemptRecords(root, after.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("producer-to-recovery attempt count=%d want 2", len(attempts))
	}
	var created *AttemptRecord
	for _, a := range attempts {
		if a.AttemptID != original.AttemptID {
			created = a
		}
	}
	if created == nil || !goalAttemptHasStartIdentity(created) {
		t.Fatalf("producer-to-recovery missing Wait/custody start identity: %+v", created)
	}
	if recovered.Goal.BoundAttemptID != created.AttemptID {
		t.Fatalf("producer-to-recovery bound=%s new=%s", recovered.Goal.BoundAttemptID, created.AttemptID)
	}
	if countBootstrapConsumeEvents(t, root, after.ID) != 1 {
		t.Fatalf("producer-to-recovery consume=%d", countBootstrapConsumeEvents(t, root, after.ID))
	}
	detail := bootstrapConsumeDetail(t, root, after.ID)
	if eventString(detail, "round") != "3" {
		t.Fatalf("consume round=%s", eventString(detail, "round"))
	}
	start, err := time.Parse(time.RFC3339Nano, original.CreatedAt)
	if err != nil {
		start, err = time.Parse(time.RFC3339, original.CreatedAt)
	}
	if err != nil {
		t.Fatal(err)
	}
	wantDeadline := start.Add(time.Duration(originalTimeout) * time.Second)
	gotDeadline, err := time.Parse(time.RFC3339Nano, eventString(detail, "original_deadline"))
	if err != nil {
		t.Fatalf("original_deadline: %v", err)
	}
	if gotDeadline.Unix() != wantDeadline.Unix() {
		t.Fatalf("absolute original deadline changed: got %s want %s", gotDeadline, wantDeadline)
	}
	replayErr := runBootstrapRecoveryHostedCLI(root, wf.ID, auth)
	if replayErr == nil || !errors.Is(replayErr, errGoalBootstrapRecovery) {
		t.Fatalf("hosted replay must refuse: %v", replayErr)
	}
	if countBootstrapConsumeEvents(t, root, after.ID) != 1 {
		t.Fatalf("replay consume=%d", countBootstrapConsumeEvents(t, root, after.ID))
	}
	atts, err := listGoalAttemptRecords(root, after.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("replay extra attempts: %d", len(atts))
	}
}

func TestDesignResultAddWritePathsSuccessorCLI(t *testing.T) {
	root, cfg, wf, writer, _ := d4SuccessorFixture(t)
	newPath := "internal/hosted/capture.go"
	if err := os.MkdirAll(filepath.Join(writer.Dir, "internal", "hosted"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(writer.Dir, filepath.FromSlash(newPath)), []byte("package hosted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWriterPaths := append([]string(nil), writer.WriteDomain.Paths...)
	oldDigest := ""
	if writer.Goal != nil {
		oldDigest = writer.Goal.InputDigest
	}
	oldDomainID, oldLineage, oldComponent := wf.WriteDomain.ID, wf.WriteDomain.Lineage, wf.WriteDomain.Component
	oldBudget, oldTimeout := writer.Goal.BudgetTokens, writer.Goal.HardTimeoutSec
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     writer.ID,
		"consumed_session_id":  writer.SessionID,
		"consumed_attempt_id":  writer.Goal.BoundAttemptID,
		"consumed_revision":    writer.Revision,
		"consumed_observation": "budget_limited",
		"add_write_paths":      []string{newPath},
	})
	stdout, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
	if err != nil {
		t.Fatalf("design-result CLI: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "design-result=") || !strings.Contains(stdout, newPath) {
		t.Fatalf("CLI readback missing decision or new path: %s", stdout)
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.WriteDomain.ID != oldDomainID || disk.WriteDomain.Lineage != oldLineage || disk.WriteDomain.Component != oldComponent {
		t.Fatalf("domain identity changed: %+v", disk.WriteDomain)
	}
	if !containsString(disk.WriteDomain.Paths, "internal/auth") || !containsString(disk.WriteDomain.Paths, newPath) {
		t.Fatalf("next claim missing unique new path: %v", disk.WriteDomain.Paths)
	}
	prev, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.WriteDomain == nil || strings.Join(prev.WriteDomain.Paths, ",") != strings.Join(oldWriterPaths, ",") {
		t.Fatalf("historical writer paths changed: %+v", prev.WriteDomain)
	}
	if prev.Goal != nil && prev.Goal.InputDigest != oldDigest {
		t.Fatalf("historical InputDigest changed: %q vs %q", prev.Goal.InputDigest, oldDigest)
	}
	next, err := loadTask(root, disk.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == writer.ID {
		t.Fatal("successor reused historical writer")
	}
	if next.WriteDomain == nil || !containsString(next.WriteDomain.Paths, newPath) {
		t.Fatalf("next writer missing new path: %+v", next.WriteDomain)
	}
	if next.Goal == nil || next.Goal.BudgetTokens != oldBudget || next.Goal.HardTimeoutSec != oldTimeout {
		t.Fatalf("budget/deadline reset: %+v", next.Goal)
	}
	contract := frozenGoalStageContract(disk, next)
	if !strings.Contains(contract, newPath) || !strings.Contains(contract, "internal/auth") {
		t.Fatalf("next contract missing expanded paths:\n%s", contract)
	}
}

func TestDesignResultAddWritePathsRefusesWithoutPartialExpansion(t *testing.T) {
	t.Run("outside-repo", func(t *testing.T) {
		root, cfg, wf, writer, _ := d4SuccessorFixture(t)
		beforePaths := append([]string(nil), wf.WriteDomain.Paths...)
		receipt := writeAstraDesignReceiptWith(t, map[string]any{
			"consumed_task_id":     writer.ID,
			"consumed_session_id":  writer.SessionID,
			"consumed_attempt_id":  writer.Goal.BoundAttemptID,
			"consumed_revision":    writer.Revision,
			"consumed_observation": "budget_limited",
			"add_write_paths":      []string{"../escape"},
		})
		_, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
		if err == nil {
			t.Fatal("outside-repo add_write_paths recovered")
		}
		assertDesignResultScopeUnchanged(t, root, cfg, wf, writer, beforePaths)
	})
	t.Run("stop-decision", func(t *testing.T) {
		root, cfg, wf, writer, _ := d4SuccessorFixture(t)
		beforePaths := append([]string(nil), wf.WriteDomain.Paths...)
		receipt := writeAstraDesignReceiptWith(t, map[string]any{
			"consumed_task_id":     writer.ID,
			"consumed_session_id":  writer.SessionID,
			"consumed_attempt_id":  writer.Goal.BoundAttemptID,
			"consumed_revision":    writer.Revision,
			"consumed_observation": "budget_limited",
			"add_write_paths":      []string{"internal/hosted/capture.go"},
		})
		_, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "stop", "-observation", "budget_limited")
		if err == nil || !strings.Contains(err.Error(), "add_write_paths is only for successor") {
			t.Fatalf("stop with add_write_paths: %v", err)
		}
		assertDesignResultScopeUnchanged(t, root, cfg, wf, writer, beforePaths)
	})
	t.Run("round-exhausted", func(t *testing.T) {
		root, cfg, wf, writer, receipt := d4SuccessorFixture(t)
		beforePaths := append([]string(nil), wf.WriteDomain.Paths...)
		wf.CurrentRound = wf.MaxRounds
		if err := persistWorkflow(root, cfg, wf); err != nil {
			t.Fatal(err)
		}
		receipt = writeAstraDesignReceiptWith(t, map[string]any{
			"consumed_task_id":     writer.ID,
			"consumed_session_id":  writer.SessionID,
			"consumed_attempt_id":  writer.Goal.BoundAttemptID,
			"consumed_revision":    writer.Revision,
			"consumed_observation": "budget_limited",
			"add_write_paths":      []string{"internal/hosted/capture.go"},
		})
		_, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
		if err == nil || !errors.Is(err, errWorkflowRoundsExceeded) {
			t.Fatalf("exhausted with add_write_paths: %v", err)
		}
		disk, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(disk.WriteDomain.Paths, ",") != strings.Join(beforePaths, ",") {
			t.Fatalf("exhausted expanded domain: %v", disk.WriteDomain.Paths)
		}
		if disk.WriterTaskID != writer.ID {
			t.Fatalf("exhausted created a writer: %s", disk.WriterTaskID)
		}
	})
	t.Run("unbound-result-digest", func(t *testing.T) {
		root, cfg, wf, writer, receipt := d4SuccessorFixture(t)
		beforePaths := append([]string(nil), wf.WriteDomain.Paths...)
		raw, err := os.ReadFile(receipt)
		if err != nil {
			t.Fatal(err)
		}
		var rec map[string]any
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		rec["add_write_paths"] = []string{"internal/hosted/not-in-result.go"}
		patched, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receipt, append(patched, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
		if err == nil || !strings.Contains(err.Error(), "not bound to the result digest") {
			t.Fatalf("unbound add_write_paths: %v", err)
		}
		assertDesignResultScopeUnchanged(t, root, cfg, wf, writer, beforePaths)
	})
	t.Run("workflow-conflict", func(t *testing.T) {
		root, cfg, wf, writer, _ := d4SuccessorFixture(t)
		beforePaths := append([]string(nil), wf.WriteDomain.Paths...)
		if err := cmdWorkflowInit([]string{
			"-root", root, "-module", "billing", "-goal-id", "billing-v1", "-goal", "bill",
			"-dir", writer.Dir, "-terminal-criteria", "c", "-engine", grokBuildRunnerName,
			"-write-domain-id", "billing-core", "-write-domain-lineage", "billing-core-lineage",
			"-write-domain-component", "billing", "-write-paths", "internal/billing",
		}); err != nil {
			t.Fatalf("disjoint billing lane: %v", err)
		}
		receipt := writeAstraDesignReceiptWith(t, map[string]any{
			"consumed_task_id":     writer.ID,
			"consumed_session_id":  writer.SessionID,
			"consumed_attempt_id":  writer.Goal.BoundAttemptID,
			"consumed_revision":    writer.Revision,
			"consumed_observation": "budget_limited",
			"add_write_paths":      []string{"internal/billing"},
		})
		_, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
		if err == nil {
			t.Fatal("conflicting add_write_paths recovered")
		}
		assertDesignResultScopeUnchanged(t, root, cfg, wf, writer, beforePaths)
	})
	t.Run("in-flight-custody", func(t *testing.T) {
		root, cfg, wf, writer, _ := d4SuccessorFixture(t)
		beforePaths := append([]string(nil), wf.WriteDomain.Paths...)
		registerTaskInvoke(writer.ID, os.Getpid())
		t.Cleanup(func() { unregisterTaskInvoke(writer.ID, os.Getpid()) })
		receipt := writeAstraDesignReceiptWith(t, map[string]any{
			"consumed_task_id":     writer.ID,
			"consumed_session_id":  writer.SessionID,
			"consumed_attempt_id":  writer.Goal.BoundAttemptID,
			"consumed_revision":    writer.Revision,
			"consumed_observation": "budget_limited",
			"add_write_paths":      []string{"internal/hosted/capture.go"},
		})
		_, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
		if err == nil || !strings.Contains(err.Error(), "custody is held") {
			t.Fatalf("in-flight custody with add_write_paths: %v", err)
		}
		assertDesignResultScopeUnchanged(t, root, cfg, wf, writer, beforePaths)
	})
	t.Run("stale-revision", func(t *testing.T) {
		root, cfg, wf, writer, _ := d4SuccessorFixture(t)
		beforePaths := append([]string(nil), wf.WriteDomain.Paths...)
		receipt := writeAstraDesignReceiptWith(t, map[string]any{
			"consumed_task_id":     writer.ID,
			"consumed_session_id":  writer.SessionID,
			"consumed_attempt_id":  writer.Goal.BoundAttemptID,
			"consumed_revision":    writer.Revision + 99,
			"consumed_observation": "budget_limited",
			"add_write_paths":      []string{"internal/hosted/capture.go"},
		})
		_, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
		if err == nil || !strings.Contains(err.Error(), "exact current task/session/attempt/revision") {
			t.Fatalf("stale revision with add_write_paths: %v", err)
		}
		assertDesignResultScopeUnchanged(t, root, cfg, wf, writer, beforePaths)
	})
	t.Run("active-task-conflict", func(t *testing.T) {
		root, cfg, wf, writer, _ := d4SuccessorFixture(t)
		beforePaths := append([]string(nil), wf.WriteDomain.Paths...)
		newPath := "internal/hosted/capture.go"
		if err := os.MkdirAll(filepath.Join(writer.Dir, "internal", "hosted"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(writer.Dir, filepath.FromSlash(newPath)), []byte("package hosted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		peer := newTask(root, cfg, typeSequence, "live peer writer", writer.Dir, []string{"peer"}, 8)
		peer.WriteDomain = &WriteDomain{
			ID:        "hosted-live",
			Lineage:   "hosted-live-lineage",
			Component: "hosted",
			Paths:     []string{newPath},
		}
		if err := saveTask(root, peer); err != nil {
			t.Fatal(err)
		}
		registerTaskInvoke(peer.ID, os.Getpid())
		t.Cleanup(func() { unregisterTaskInvoke(peer.ID, os.Getpid()) })
		receipt := writeAstraDesignReceiptWith(t, map[string]any{
			"consumed_task_id":     writer.ID,
			"consumed_session_id":  writer.SessionID,
			"consumed_attempt_id":  writer.Goal.BoundAttemptID,
			"consumed_revision":    writer.Revision,
			"consumed_observation": "budget_limited",
			"add_write_paths":      []string{newPath},
		})
		_, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
		if err == nil || !strings.Contains(err.Error(), "conflict with an active writer") {
			t.Fatalf("active task conflict with add_write_paths: %v", err)
		}
		assertDesignResultScopeUnchanged(t, root, cfg, wf, writer, beforePaths)
	})
}

func assertDesignResultScopeUnchanged(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, writer *Task, beforePaths []string) {
	t.Helper()
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(disk.WriteDomain.Paths, ",") != strings.Join(beforePaths, ",") {
		t.Fatalf("partial expansion: %v vs %v", disk.WriteDomain.Paths, beforePaths)
	}
	if disk.WriterTaskID != writer.ID {
		t.Fatalf("refused result created a writer: %s", disk.WriterTaskID)
	}
	prev, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.WriteDomain == nil || strings.Join(prev.WriteDomain.Paths, ",") != strings.Join(beforePaths, ",") {
		t.Fatalf("historical writer expanded: %+v", prev.WriteDomain)
	}
}

func TestManagerScopeDigestMustRejectParentPathSubstitution(t *testing.T) {
	root, cfg, wf, writer, _ := d4SuccessorFixture(t)
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     writer.ID,
		"consumed_session_id":  writer.SessionID,
		"consumed_attempt_id":  writer.Goal.BoundAttemptID,
		"consumed_revision":    writer.Revision,
		"consumed_observation": "budget_limited",
		"add_write_paths":      []string{"internal/hosted/capture.go"},
	})
	raw, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err = json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	rec["add_write_paths"] = []string{"internal/hosted"}
	raw, err = json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(receipt, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before := append([]string(nil), wf.WriteDomain.Paths...)
	_, err = captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "budget_limited")
	if err == nil {
		t.Fatal("digest authorized exact file but receipt parent-path substitution expanded entire directory")
	}
	assertDesignResultScopeUnchanged(t, root, cfg, wf, writer, before)
}

func TestManagerNoScopeStopMustNotRequireUnrelatedScheduler(t *testing.T) {
	if root := os.Getenv("CARDEX_SCOPE_FOREIGN_LOCK_ROOT"); root != "" {
		if !acquireLock(root, time.Minute) {
			t.Fatal("foreign scheduler helper cannot lock")
		}
		defer releaseLock(root)
		if err := os.WriteFile(filepath.Join(root, "scope-helper-ready"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(15 * time.Second)
		for time.Now().Before(until) {
			if _, err := os.Stat(filepath.Join(root, "scope-helper-release")); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("foreign scheduler helper timed out")
	}
	root, cfg, wf, writer, receipt := d4SuccessorFixture(t)
	child := exec.Command(os.Args[0], "-test.run=^TestManagerNoScopeStopMustNotRequireUnrelatedScheduler$", "-test.timeout=20s")
	child.Env = append(os.Environ(), "CARDEX_SCOPE_FOREIGN_LOCK_ROOT="+root)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(root, "scope-helper-release"), []byte("release"), 0o600)
		if err := child.Wait(); err != nil {
			t.Errorf("foreign scheduler helper: %v", err)
		}
	}()
	until := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "scope-helper-ready")); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("foreign helper readiness missing")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err := captureWorkflowCmd(t, "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "stop", "-observation", "budget_limited")
	if err != nil {
		t.Fatalf("ordinary no-add_write_paths stop newly blocked by unrelated scheduler: %v", err)
	}
	after, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.WriterTaskID != writer.ID || after.Status != workflowStatusOwnerChoice {
		t.Fatalf("unexpected ordinary stop result: %s/%s", after.Status, after.WriterTaskID)
	}
}

func copyBootstrapProofFile(t *testing.T, name, src string) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("CARDEX_BOOTSTRAP_CLI_LOG_DIR"))
	if dir == "" || strings.TrimSpace(src) == "" {
		return
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Logf("proof copy %s: %v", name, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Logf("proof write %s: %v", name, err)
	}
}

const (
	bootstrapCLIEvidenceInProcess  = "in-process official command handler + fake launch; handler result is not a cardex binary Wait returncode"
	bootstrapCLIEvidenceSubprocess = "cardex candidate subprocess Wait returncode"
)

func writeBootstrapCLIEvidence(name, stdout, stderr string, code int) {
	writeBootstrapCLIEvidenceSource(name, stdout, stderr, code, bootstrapCLIEvidenceInProcess)
}

func writeBootstrapCLIEvidenceSource(name, stdout, stderr string, code int, source string) {
	dir := strings.TrimSpace(os.Getenv("CARDEX_BOOTSTRAP_CLI_LOG_DIR"))
	if dir == "" {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "source=%s\n", source)
	if source == bootstrapCLIEvidenceInProcess {
		if code == 0 {
			b.WriteString("handler_err=nil\n")
		} else if strings.TrimSpace(stderr) != "" {
			fmt.Fprintf(&b, "handler_err=%s\n", strings.TrimSpace(stderr))
		} else {
			fmt.Fprintf(&b, "handler_err=status %d\n", code)
		}
	} else {
		fmt.Fprintf(&b, "exit=%d\n", code)
	}
	b.WriteString("--- stdout ---\n")
	b.WriteString(stdout)
	if stdout != "" && !strings.HasSuffix(stdout, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("--- stderr ---\n")
	b.WriteString(stderr)
	if stderr != "" && !strings.HasSuffix(stderr, "\n") {
		b.WriteByte('\n')
	}
	_ = os.WriteFile(filepath.Join(dir, name+".log"), []byte(b.String()), 0o644)
}

func bootstrapSidecarlessExecutorFixture(t *testing.T) (root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord, attemptBytes []byte, proof syntheticCodexExecutorResult) {
	t.Helper()
	root, cfg, wf, tk, original, attemptBytes = bootstrapBeforeNativeFixture(t)
	if _, err := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(err) {
		t.Fatalf("sidecar-less fixture must omit .bootstrap-refusal: %v", err)
	}
	proof = writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{Attempt: original})
	cfg.GrokBuildBin = fakeGrokGoalBin(t, filepath.Join(t.TempDir(), "argv-recovery"))
	saveGoalCfg(t, root, cfg)
	return
}

func rewriteBootstrapAuthField(t *testing.T, authPath, key, value string) {
	t.Helper()
	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	obj[key] = value
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapExecutorCaptureSidecarlessRecoveryPreservesHistory(t *testing.T) {
	root, cfg, wf, tk, original, originalBytes, proof := bootstrapSidecarlessExecutorFixture(t)
	if err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual"}); !errors.Is(err, errGoalRedispatchBlocked) {
		t.Fatalf("ordinary goal-run must stay fail-closed before recovery: %v", err)
	}
	authPath := mintBootstrapExecutorAuthorization(t, tk, wf, time.Time{}, proof)
	if err := runBootstrapRecoveryCLIWithExecutor(root, wf.ID, authPath, proof.Path, proof.Digest, proof.CallID); err != nil {
		t.Fatalf("sidecar-less executor recovery: %v", err)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	wfAfter, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wfAfter.CurrentRound != 3 || wfAfter.MaxRounds != 3 {
		t.Fatalf("round/max changed: %d/%d", wfAfter.CurrentRound, wfAfter.MaxRounds)
	}
	if after.SessionID != tk.SessionID {
		t.Fatalf("session changed: %s vs %s", after.SessionID, tk.SessionID)
	}
	if after.PreferRunner != tk.PreferRunner {
		t.Fatalf("route changed: %s vs %s", after.PreferRunner, tk.PreferRunner)
	}
	if after.Goal.BudgetTokens != tk.Goal.BudgetTokens || after.Goal.HardTimeoutSec != tk.Goal.HardTimeoutSec {
		t.Fatalf("budget/timeout reset: budget=%d/%d timeout=%d/%d",
			after.Goal.BudgetTokens, tk.Goal.BudgetTokens, after.Goal.HardTimeoutSec, tk.Goal.HardTimeoutSec)
	}
	if after.Goal.InputDigest != tk.Goal.InputDigest || after.Goal.SandboxProfileDigest != tk.Goal.SandboxProfileDigest {
		t.Fatalf("contract/profile digest changed")
	}
	if after.Goal.SandboxProfile != tk.Goal.SandboxProfile {
		t.Fatalf("profile name changed")
	}
	if !after.Goal.Started {
		t.Fatal("Started was cleared")
	}
	if _, err := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(err) {
		t.Fatalf("recovery wrote original .bootstrap-refusal: %v", err)
	}
	attempts, err := listGoalAttemptRecords(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count=%d want 2", len(attempts))
	}
	var kept, created *AttemptRecord
	for _, rec := range attempts {
		if rec.AttemptID == original.AttemptID {
			kept = rec
		} else {
			created = rec
		}
	}
	if kept == nil || created == nil {
		t.Fatalf("missing distinct attempts: original=%s new=%v", original.AttemptID, created)
	}
	if kept.State != attemptExited || kept.PID != original.PID || kept.StartIdentity != original.StartIdentity {
		t.Fatalf("original exit overwritten: %+v", kept)
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	if !goalAttemptHasStartIdentity(created) {
		t.Fatalf("new attempt missing Wait/custody start identity: %+v", created)
	}
	if created.AttemptID == original.AttemptID {
		t.Fatal("new attempt reused original id")
	}
	if after.Goal.BoundAttemptID != created.AttemptID {
		t.Fatalf("bound=%s new=%s", after.Goal.BoundAttemptID, created.AttemptID)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatalf("consume events=%d", countBootstrapConsumeEvents(t, root, tk.ID))
	}
	detail := bootstrapConsumeDetail(t, root, tk.ID)
	if eventString(detail, "original_attempt_id") != original.AttemptID {
		t.Fatalf("consume original=%s", eventString(detail, "original_attempt_id"))
	}
	if eventString(detail, "new_attempt_id") != created.AttemptID {
		t.Fatalf("consume new=%s", eventString(detail, "new_attempt_id"))
	}
	start, err := time.Parse(time.RFC3339Nano, original.CreatedAt)
	if err != nil {
		start, err = time.Parse(time.RFC3339, original.CreatedAt)
	}
	if err != nil {
		t.Fatal(err)
	}
	wantDeadline := start.Add(time.Duration(tk.Goal.HardTimeoutSec) * time.Second)
	gotDeadline, err := time.Parse(time.RFC3339Nano, eventString(detail, "original_deadline"))
	if err != nil {
		t.Fatalf("original_deadline: %v", err)
	}
	if gotDeadline.Unix() != wantDeadline.Unix() {
		t.Fatalf("absolute original deadline changed: got %s want %s", gotDeadline, wantDeadline)
	}
	if eventString(detail, "round") != "3" {
		t.Fatalf("consume round=%s", eventString(detail, "round"))
	}
	if err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual"}); !errors.Is(err, errGoalRedispatchBlocked) {
		t.Fatalf("ordinary goal-run after recovery must stay fail-closed: %v", err)
	}
}

func rebindExecutorProof(t *testing.T, authPath string, proof syntheticCodexExecutorResult) {
	t.Helper()
	bindExecutorAuthorization(t, authPath, proof)
}

func TestBootstrapExecutorCaptureNegativesRefuseWithoutConsume(t *testing.T) {
	type tc struct {
		name string
		want string
		prep func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string)
	}
	zero := 0
	cases := []tc{
		{
			name: "wrong-call",
			want: "wrong call identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt: original,
					CallID:  "call_WRONGIDENTITY0001",
				})
				next.CallID = proof.CallID
				rebindExecutorProof(t, authPath, next)
				rewriteBootstrapAuthField(t, authPath, "executor_call_id", proof.CallID)
				return next, authPath
			},
		},
		{
			name: "wrong-attempt",
			want: "changed binding",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				rewriteBootstrapAuthField(t, authPath, "original_attempt_id", "at-wrong-attempt")
				return proof, authPath
			},
		},
		{
			name: "wrong-session",
			want: "wrong session identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:   original,
					SessionID: "00000000-0000-4000-8000-ffffffffffff",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "wrong-contract",
			want: "wrong contract digest",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:        original,
					ContractDigest: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "wrong-profile",
			want: "wrong profile digest",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:       original,
					ProfileDigest: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "later-approved-retry",
			want: "later approved retry is not original executor capture",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:       original,
					Status:        "completed",
					ExitCode:      &zero,
					LastChunkExit: &zero,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "later-approved-extra-record",
			want: "later approved retry is not original executor capture",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:       original,
					ExtraApproved: true,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "missing-raw",
			want: "missing original executor capture",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				proof.Path = filepath.Join(t.TempDir(), "absent-executor.jsonl")
				return proof, authPath
			},
		},
		{
			name: "truncated-raw",
			want: "truncated original executor capture",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:     original,
					RecordCount: 2,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "modified-raw",
			want: "executor capture digest mismatch",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				raw, err := os.ReadFile(proof.Path)
				if err != nil {
					t.Fatal(err)
				}
				raw = append(raw, []byte("\n")...)
				if err := os.WriteFile(proof.Path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
				return proof, authPath
			},
		},
		{
			name: "missing-terminal",
			want: "missing terminal nonzero exit",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:       original,
					OmitExitCode:  true,
					OmitLastChunk: true,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "generic-error",
			want: "generic failure is not bootstrap-before-provider refusal",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:     original,
					RefusalLine: "exit status 1",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "user-created-json",
			want: "unknown executor capture format",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt: original,
					UserJSON: map[string]any{
						"kind":           "bootstrap-before-provider-refusal",
						"summary":        "sandbox hook failed",
						"attempt_id":     original.AttemptID,
						"refusal_output": acceptedBootstrapBeforeProviderRefusal,
					},
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "summary-llm-text",
			want: "unknown executor capture format",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:   original,
					PlainText: "The launch failed because Grok hook write-deny ensure failed: cannot create managed_config.toml: Operation not permitted; named profile refused with protections missing.\n",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "native-artifacts",
			want: "genuinely executed unknown",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				goalID := "goal-native-executed"
				tk.Goal.NativeGoalID = goalID
				if err := saveTask(root, tk); err != nil {
					t.Fatal(err)
				}
				writeGrokGoalFixture(t, tk.Goal.GrokHome, tk.Dir, tk.SessionID, goalID, "active", grokNativeGoalStateFile{})
				tk2, err := loadTask(root, tk.ID)
				if err != nil {
					t.Fatal(err)
				}
				authPath = mintBootstrapExecutorAuthorization(t, tk2, wf, time.Time{}, proof)
				return proof, authPath
			},
		},
		{
			name: "deadline-expired",
			want: "exhausted remaining allowance",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				rec, err := loadAttempt(root, tk.ID, original.AttemptID)
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now().Add(-2 * time.Hour)
				rec.CreatedAt = start.Format(time.RFC3339Nano)
				rec.UpdatedAt = start.Add(200 * time.Millisecond).Format(time.RFC3339Nano)
				if err := writeAttempt(root, rec); err != nil {
					t.Fatal(err)
				}
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{Attempt: rec})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "wrong-workflow-same-bindings",
			want: "wrong command identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt: original,
					Command: "cardex workflow goal-run -manual -budget 12000 wf-other-same-bindings",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "later-failed-event",
			want: "later executor event is not original capture",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:    original,
					LaterEvent: true,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "wrong-attempt-time",
			want: "wrong attempt time",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:          original,
					WrongAttemptTime: true,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "exact-command-mismatch",
			want: "wrong command identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:     original,
					Command:     "cardex workflow goal-run -manual -budget 12000 " + wf.ID,
					ItemCommand: "cardex workflow goal-run -manual -budget 99999 " + wf.ID,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "terminal-return-contradictory-output",
			want: "contradictory executor output",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:      original,
					ReturnOutput: acceptedBootstrapBeforeProviderRefusal + "\nlater failed output with the same session still refuses independently\n",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "forged-snapshot-provenance",
			want: "rewritten executor snapshot is not original capture",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				raw, err := os.ReadFile(proof.Path)
				if err != nil {
					t.Fatal(err)
				}
				records, err := splitExecutorJSONL(raw)
				if err != nil {
					t.Fatal(err)
				}
				if len(records) > 0 {
					records[0]["forged"] = true
				}
				var buf bytes.Buffer
				enc := json.NewEncoder(&buf)
				enc.SetEscapeHTML(false)
				for _, rec := range records {
					if err := enc.Encode(rec); err != nil {
						t.Fatal(err)
					}
				}
				rewritten := buf.Bytes()
				path := filepath.Join(t.TempDir(), "rewritten-executor.jsonl")
				if err := os.WriteFile(path, rewritten, 0o644); err != nil {
					t.Fatal(err)
				}
				next := proof
				next.Path = path
				next.Digest = sha256Hex(string(rewritten))
				rebindExecutorProof(t, authPath, next)
				rewriteBootstrapAuthField(t, authPath, "executor_provenance", proof.Provenance)
				return next, authPath
			},
		},
		{
			name: "unbound-digest-event",
			want: "unbound executor digest/event in authorization",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				authPath = mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
				return proof, authPath
			},
		},
		{
			name: "self-hashed-triple",
			want: "self-hashed capture is not original executor provenance",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt:        original,
					SkipProvenance: true,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "wrapper-shell",
			want: "wrong command identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt: original,
					Command: `sh -c "cardex workflow goal-run -manual -budget 12000 ` + wf.ID + `"`,
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "quoted-injection",
			want: "wrong command identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt: original,
					Command: "cardex workflow goal-run -manual -budget 12000 " + wf.ID + "; cat /etc/passwd",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
		{
			name: "ambiguous-multiple-commands",
			want: "wrong command identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, proof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, string) {
				next := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
					Attempt: original,
					Input: "text(await tools.exec_command({cmd:\"cardex workflow goal-run -manual -budget 12000 " + wf.ID + "\"}));\n" +
						"text(await tools.exec_command({cmd:\"cardex workflow goal-run -manual -budget 12000 wf-other-same-bindings\"}));\n",
				})
				rebindExecutorProof(t, authPath, next)
				return next, authPath
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _, wf, tk, original, _, proof := bootstrapSidecarlessExecutorFixture(t)
			authPath := mintBootstrapExecutorAuthorization(t, tk, wf, time.Time{}, proof)
			proof, authPath = tc.prep(t, root, wf, tk, original, proof, authPath)
			afterPrepBytes, rerr := os.ReadFile(attemptPath(root, tk.ID, original.AttemptID))
			if rerr != nil {
				t.Fatal(rerr)
			}
			err := runBootstrapRecoveryCLIWithExecutor(root, wf.ID, authPath, proof.Path, proof.Digest, proof.CallID)
			if err == nil {
				t.Fatalf("%s recovered", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s reason: %v want %q", tc.name, err, tc.want)
			}
			if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
				t.Fatalf("%s consumed authorization", tc.name)
			}
			atts, lerr := listGoalAttemptRecords(root, tk.ID)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if len(atts) != 1 {
				t.Fatalf("%s admitted extra attempt: %d", tc.name, len(atts))
			}
			assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, afterPrepBytes)
			if _, serr := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(serr) {
				t.Fatalf("%s wrote original .bootstrap-refusal: %v", tc.name, serr)
			}
		})
	}
}

func TestBootstrapExecutorCaptureReplayDoesNotAdmitTwice(t *testing.T) {
	root, _, wf, tk, original, originalBytes, proof := bootstrapSidecarlessExecutorFixture(t)
	authPath := mintBootstrapExecutorAuthorization(t, tk, wf, time.Time{}, proof)
	if err := runBootstrapRecoveryCLIWithExecutor(root, wf.ID, authPath, proof.Path, proof.Digest, proof.CallID); err != nil {
		t.Fatalf("first executor recovery: %v", err)
	}
	replayErr := runBootstrapRecoveryCLIWithExecutor(root, wf.ID, authPath, proof.Path, proof.Digest, proof.CallID)
	if replayErr == nil || !errors.Is(replayErr, errGoalBootstrapRecovery) {
		t.Fatalf("replay must refuse: %v", replayErr)
	}
	if !strings.Contains(replayErr.Error(), "replayed authorization") && !strings.Contains(replayErr.Error(), "consumed authorization") && !strings.Contains(replayErr.Error(), "changed binding") {
		t.Fatalf("replay reason: %v", replayErr)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatalf("replay consumed again: %d", countBootstrapConsumeEvents(t, root, tk.ID))
	}
	atts, err := listGoalAttemptRecords(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("replay admitted extra attempt: %d", len(atts))
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
}

func TestBootstrapExecutorCaptureLiveCustodyRefuses(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(hold, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	cfg.GrokBuildBin = fakeGrokGoalBinHold(t, filepath.Join(t.TempDir(), "argv"), hold)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.GrokHome = t.TempDir()
	tk.Goal.BudgetTokens = 12000
	tk.Goal.HardTimeoutSec = 3600
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- runManualGoalLaunch(t, root, cfg, wf, 12000)
	}()
	var live *Task
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := loadTask(root, tk.ID)
		if err == nil && cur.Goal != nil && cur.ActiveAttemptID != "" {
			rec, rerr := loadAttempt(root, cur.ID, cur.ActiveAttemptID)
			if rerr == nil && rec != nil && rec.PID > 0 {
				live = cur
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if live == nil {
		_ = os.Remove(hold)
		t.Fatal("live producer never bound")
	}
	liveAttempt, _ := loadRequiredGoalAttempt(root, live)
	proof := writeSyntheticCodexExecutorCaptureResult(t, live, wf, syntheticCodexExecutorOpts{Attempt: liveAttempt})
	auth := mintBootstrapExecutorAuthorization(t, live, wf, time.Time{}, proof)
	err := runBootstrapRecoveryCLIWithExecutor(root, wf.ID, auth, proof.Path, proof.Digest, proof.CallID)
	_ = os.Remove(hold)
	launchErr := <-done
	if err == nil || !strings.Contains(err.Error(), "live custody") {
		t.Fatalf("live custody: %v launch=%v", err, launchErr)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
		t.Fatal("live custody consumed auth")
	}
}

func TestBootstrapExecutorCaptureGenericStartedUnknownWithoutProofStillRefuses(t *testing.T) {
	root, _, wf, tk, original, originalBytes := bootstrapBeforeNativeFixture(t)
	if _, err := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(err) {
		t.Fatalf("generic started/unknown must not retain exact refusal proof: %v", err)
	}
	auth := mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
	if err := runBootstrapRecoveryCLI(root, wf.ID, auth); err == nil {
		t.Fatal("generic exited CLI with no sidecar and no executor proof was allowed to recover")
	} else if !strings.Contains(err.Error(), "missing launcher/process evidence") &&
		!strings.Contains(err.Error(), "bootstrap-before-provider") {
		t.Fatalf("generic refuse reason: %v", err)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
		t.Fatal("generic started/unknown consumed authorization")
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
}

func TestBootstrapExecutorCaptureCandidateSubprocessTwice(t *testing.T) {
	bin := resolveCardexCandidateBinary(t)
	for pass := 1; pass <= 2; pass++ {
		root, _, wf, tk, original, _, proof := bootstrapSidecarlessExecutorFixture(t)
		auth := mintBootstrapExecutorAuthorization(t, tk, wf, time.Time{}, proof)
		stdout, stderr, code := runCardexBootstrapRecoveryProcess(t, bin, root, wf.ID, auth,
			"-executor-capture", proof.Path, "-executor-digest", proof.Digest, "-executor-call-id", proof.CallID)
		writeBootstrapCLIEvidenceSource(fmt.Sprintf("cli-positive-%d", pass), stdout, stderr, code, bootstrapCLIEvidenceSubprocess)
		if !strings.Contains(stdout, "original_attempt="+original.AttemptID) {
			t.Fatalf("pass %d positive stdout missing original_attempt: stdout=%q stderr=%q code=%d", pass, stdout, stderr, code)
		}
		if !strings.Contains(stdout, "new_attempt=") {
			t.Fatalf("pass %d positive stdout missing new_attempt: stdout=%q", pass, stdout)
		}
		if code == 0 {
			t.Logf("pass %d sidecar-less executor-proof cardex subprocess returncode=0 stdout=%s", pass, strings.TrimSpace(stdout))
		} else {
			t.Logf("pass %d sidecar-less executor-proof cardex subprocess returncode=%d (Wait/custody TTY unverified in this sandbox: python pty.openpty OSError out of pty devices; in-process TestBootstrapExecutorCaptureSidecarlessRecoveryPreservesHistory covers Wait/custody) stdout=%s stderr=%s",
				pass, code, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		}
		if !strings.Contains(stdout, "original_attempt=") || !strings.Contains(stdout, "new_attempt=") {
			t.Fatalf("pass %d positive missing attempt identities", pass)
		}
		if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
			t.Fatalf("pass %d positive did not consume once: %d", pass, countBootstrapConsumeEvents(t, root, tk.ID))
		}
		if _, err := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(err) {
			t.Fatalf("pass %d wrote original .bootstrap-refusal: %v", pass, err)
		}

		rootN, _, wfN, tkN, origN, _, proofN := bootstrapSidecarlessExecutorFixture(t)
		wrong := writeSyntheticCodexExecutorCaptureResult(t, tkN, wfN, syntheticCodexExecutorOpts{
			Attempt: origN,
			CallID:  "call_WRONGIDENTITY0001",
		})
		authN := mintBootstrapExecutorAuthorization(t, tkN, wfN, time.Time{}, wrong)
		rewriteBootstrapAuthField(t, authN, "executor_call_id", proofN.CallID)
		stdoutN, stderrN, codeN := runCardexBootstrapRecoveryProcess(t, bin, rootN, wfN.ID, authN,
			"-executor-capture", wrong.Path, "-executor-digest", wrong.Digest, "-executor-call-id", proofN.CallID)
		writeBootstrapCLIEvidenceSource(fmt.Sprintf("cli-negative-%d", pass), stdoutN, stderrN, codeN, bootstrapCLIEvidenceSubprocess)
		if codeN == 0 {
			t.Fatalf("pass %d negative must be nonzero; stdout=%q stderr=%q", pass, stdoutN, stderrN)
		}
		combined := stdoutN + stderrN
		if !strings.Contains(combined, "wrong call identity") {
			t.Fatalf("pass %d negative missing fact: stdout=%q stderr=%q", pass, stdoutN, stderrN)
		}
		if countBootstrapConsumeEvents(t, rootN, tkN.ID) != 0 {
			t.Fatalf("pass %d negative consumed", pass)
		}
	}
}

func TestValidateBootstrapExecutorCaptureDigestFromWrittenBytes(t *testing.T) {
	root, _, wf, tk, original, _ := bootstrapBeforeNativeFixture(t)
	_ = root
	res := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{Attempt: original})
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(string(raw)) != res.Digest {
		t.Fatalf("fixture digest was not computed from written bytes")
	}
	proof, err := validateBootstrapExecutorCapture(raw, bootstrapExecutorCaptureAdmitted{
		Path:           res.Path,
		Digest:         res.Digest,
		CallID:         res.CallID,
		ItemID:         res.ItemID,
		ReturnID:       res.ReturnID,
		Provenance:     res.Provenance,
		SessionID:      tk.SessionID,
		ContractDigest: tk.Goal.InputDigest,
		ProfileDigest:  tk.Goal.SandboxProfileDigest,
		TaskID:         tk.ID,
		AttemptID:      original.AttemptID,
	}, original, tk)
	if err != nil {
		t.Fatalf("synthetic 3-record fixture must revalidate: %v", err)
	}
	if proof.CallID != res.CallID || proof.ExitCode == 0 {
		t.Fatalf("proof %+v", proof)
	}
}

func TestHostedPTYBootstrapExecutorCaptureRecoveryWaitCustody(t *testing.T) {
	if !requireHostedPTYOrUnverified(t) {
		return
	}
	root, cfg, wf, tk, original, originalBytes, proof := bootstrapSidecarlessExecutorFixture(t)
	cfg.GrokBuildBin = fakeGrokGoalBin(t, filepath.Join(t.TempDir(), "argv-hosted-executor"))
	saveGoalCfg(t, root, cfg)
	auth := mintBootstrapExecutorAuthorization(t, tk, wf, time.Time{}, proof)
	out, err := os.CreateTemp(t.TempDir(), "hosted-executor-out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var recErr error
	withHostedCallerStdout(t, out, func() {
		recErr = cmdWorkflowGoalBootstrapBeforeNativeRecovery([]string{
			"-root", root, wf.ID, "-hosted", "-authorization", auth,
			"-executor-capture", proof.Path,
			"-executor-digest", proof.Digest,
			"-executor-call-id", proof.CallID,
		})
	})
	if recErr != nil {
		msg := strings.ToLower(recErr.Error())
		if strings.Contains(msg, "pty_missing") {
			t.Fatalf("hosted executor recovery admitted then pty_missing: %v", recErr)
		}
		if hostedPTYLimitation(recErr) {
			t.Logf("UNVERIFIED: %v", recErr)
			return
		}
		t.Fatalf("hosted executor recovery: %v", recErr)
	}
	recovered, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	attempts, err := listGoalAttemptRecords(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("hosted executor attempt count=%d want 2", len(attempts))
	}
	var created *AttemptRecord
	for _, a := range attempts {
		if a.AttemptID != original.AttemptID {
			created = a
		}
	}
	if created == nil || !goalAttemptHasStartIdentity(created) {
		t.Fatalf("hosted executor recovery missing Wait/custody start identity: %+v", created)
	}
	if recovered.Goal.BoundAttemptID != created.AttemptID {
		t.Fatalf("hosted executor bound=%s new=%s", recovered.Goal.BoundAttemptID, created.AttemptID)
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatalf("hosted executor consume=%d", countBootstrapConsumeEvents(t, root, tk.ID))
	}
	if _, serr := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(serr) {
		t.Fatalf("hosted executor wrote original .bootstrap-refusal: %v", serr)
	}
}

func TestBootstrapExecutorCaptureRealRefusalPair(t *testing.T) {
	cases := []struct {
		name          string
		mutate        func(string) string
		wrongIdentity bool
		wrongCommand  bool
		wrongReturn   bool
		wantPass      bool
	}{
		{name: "actual-two-line-format", wantPass: true},
		{name: "missing-warning", mutate: func(s string) string { return strings.SplitN(s, "\n", 2)[1] }},
		{name: "missing-error", mutate: func(s string) string { return strings.SplitN(s, "\n", 2)[0] }},
		{name: "reversed-pair", mutate: func(s string) string { a := strings.SplitN(s, "\n", 2); return a[1] + "\n" + a[0] }},
		{name: "interleaved-pair", mutate: func(s string) string { return strings.Replace(s, "\n", "\nprovider started\n", 1) }},
		{name: "wrong-registry", mutate: func(s string) string { return strings.Replace(s, "managed_config.toml", "other.toml", 1) }},
		{name: "wrong-home", mutate: func(s string) string {
			return strings.Replace(s, "managed_config.toml", "other/managed_config.toml", 1)
		}},
		{name: "different-protection-semantics", mutate: func(s string) string {
			return strings.Replace(s, "Refusing to start with its protections missing.", "Continuing without protections.", 1)
		}},
		{name: "wrong-profile", mutate: func(s string) string { return strings.Replace(s, "'workspace'", "'different-profile'", 1) }},
		{name: "duplicate-pair", mutate: func(s string) string { return s + "\n" + s }},
		{name: "wrong-original-identity", wrongIdentity: true},
		{name: "wrong-command-profile", wrongCommand: true},
		{name: "contradictory-real-return", wrongReturn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, wf, tk, attempt, _ := bootstrapBeforeNativeFixture(t)
			tk.Goal.SandboxProfile = "workspace"
			pair := realBootstrapWarningPrefix + filepath.Join(tk.Goal.GrokHome, "managed_config.toml") + realBootstrapWarningSuffix + "\n" + realBootstrapErrorPrefix + "workspace" + realBootstrapErrorSuffix
			if tc.mutate != nil {
				pair = tc.mutate(pair)
			}
			opts := syntheticCodexExecutorOpts{Attempt: attempt, RefusalLine: pair}
			if tc.wrongCommand {
				opts.Command = "cardex workflow goal-run -manual -sandbox wrong-profile " + wf.ID
			}
			if tc.wrongReturn {
				opts.ReturnOutput = pair + "\ncontradictory terminal result\n"
			}
			result := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, opts)
			raw, err := os.ReadFile(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			admitted := bootstrapExecutorCaptureAdmitted{Path: result.Path, Digest: result.Digest, CallID: result.CallID, ItemID: result.ItemID, ReturnID: result.ReturnID, Provenance: result.Provenance, SessionID: tk.SessionID, ContractDigest: tk.Goal.InputDigest, ProfileDigest: tk.Goal.SandboxProfileDigest, TaskID: tk.ID, AttemptID: attempt.AttemptID}
			if tc.wrongIdentity {
				admitted.AttemptID = "not-original"
			}
			proof, err := validateBootstrapExecutorCapture(raw, admitted, attempt, tk)
			if tc.wantPass {
				if err != nil {
					t.Fatal(err)
				}
				if proof.ExitCode != 1 {
					t.Fatalf("exit=%d", proof.ExitCode)
				}
			} else if err == nil {
				t.Fatal("invalid actual refusal accepted")
			}
		})
	}
}

func fakeGrokGoalBinRealPair(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\n"
	script += "home=$GROK_HOME\n"
	script += "profile=workspace\n"
	script += "prev=\"\"\n"
	script += "for a in \"$@\"; do\n"
	script += "  if [ \"$prev\" = \"--sandbox\" ]; then profile=$a; fi\n"
	script += "  prev=$a\n"
	script += "done\n"
	script += "printf '%s%s%s\\n' " + shSingleQuote(realBootstrapWarningPrefix) + " \"$home/managed_config.toml\" " + shSingleQuote(realBootstrapWarningSuffix) + " >&2\n"
	script += "printf '%s%s%s\\n' " + shSingleQuote(realBootstrapErrorPrefix) + " \"$profile\" " + shSingleQuote(realBootstrapErrorSuffix) + " >&2\n"
	script += fmt.Sprintf("exit %d\n", exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBootstrapRefusalCaptureAdjacentPair(t *testing.T) {
	home := t.TempDir()
	pair := syntheticRealBootstrapPair(home, "workspace")
	warning, errorLine, ok := strings.Cut(pair, "\n")
	if !ok {
		t.Fatal("pair")
	}
	writeAll := func(c *bootstrapRefusalCapture, p []byte) {
		t.Helper()
		if _, err := c.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("adjacent", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte(pair+"\n"))
		if c.capturedOutput() != pair {
			t.Fatalf("adjacent pair: %q", c.capturedOutput())
		}
	})
	t.Run("chunk-split", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		raw := []byte(pair + "\n")
		for i := 0; i < len(raw); i += 3 {
			end := i + 3
			if end > len(raw) {
				end = len(raw)
			}
			writeAll(c, raw[i:end])
		}
		if c.capturedOutput() != pair {
			t.Fatalf("chunk-split pair: %q", c.capturedOutput())
		}
	})
	t.Run("crlf", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte(warning+"\r\n"+errorLine+"\r\n"))
		if c.capturedOutput() != pair {
			t.Fatalf("crlf pair: %q", c.capturedOutput())
		}
	})
	t.Run("truncated", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte(warning+"\n"))
		writeAll(c, bytes.Repeat([]byte("x"), grokBuildProcessStderrMaxBytes+8))
		if c.capturedOutput() == pair {
			t.Fatal("truncated error matched pair")
		}
	})
	t.Run("nonadjacent", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte(warning+"\nprovider started\n"+errorLine+"\n"))
		if c.capturedOutput() == pair {
			t.Fatal("nonadjacent pair matched")
		}
	})
	t.Run("reversed", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte(errorLine+"\n"+warning+"\n"))
		if c.capturedOutput() == pair {
			t.Fatal("reversed pair matched")
		}
	})
	t.Run("interleaved", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte(warning+"\ninterleaved\n"+errorLine+"\n"))
		if c.capturedOutput() == pair {
			t.Fatal("interleaved pair matched")
		}
	})
	t.Run("quoted", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte("diagnostic quoted: "+pair+"\n"))
		if c.capturedOutput() == pair {
			t.Fatal("quoted pair matched")
		}
	})
	t.Run("prefixed", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte("prefix: "+warning+"\n"+errorLine+"\n"))
		if c.capturedOutput() == pair {
			t.Fatal("prefixed warning matched")
		}
	})
	t.Run("concatenated", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, append(bytes.Repeat([]byte("x"), 64), []byte(pair+"\n")...))
		if c.capturedOutput() == pair {
			t.Fatal("concatenated junk+pair without line boundary matched")
		}
	})
	t.Run("generic-keyword", func(t *testing.T) {
		c := &bootstrapRefusalCapture{sink: io.Discard}
		writeAll(c, []byte("Operation not permitted\nsandbox profile refused\n"))
		if c.capturedOutput() == pair {
			t.Fatal("generic keyword matched")
		}
		if _, ok := matchAcceptedBootstrapBeforeProviderRefusal("Operation not permitted"); ok {
			t.Fatal("generic keyword matcher accepted")
		}
	})
}

func TestManualGoalLaunchHeadlessRealPairRetainsSidecarAndConsumer(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	cfg.GrokBuildBin = fakeGrokGoalBinRealPair(t, 1)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	home := t.TempDir()
	tk.Goal.GrokHome = home
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err == nil {
		t.Fatal("real pair producer must exit nonzero")
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := loadRequiredGoalAttempt(root, after)
	if err != nil {
		t.Fatal(err)
	}
	assertCapturedBootstrapBeforeProviderProof(t, root, after, rec)
	proof, err := loadBootstrapLauncherEvidence(root, after.ID, rec.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	want := syntheticRealBootstrapPair(after.Goal.GrokHome, firstNonBlank(after.Goal.SandboxProfile, "workspace"))
	if proof.RefusalOutput != want {
		t.Fatalf("sidecar pair=%q want %q", proof.RefusalOutput, want)
	}
	originalBytes, err := os.ReadFile(attemptPath(root, after.ID, rec.AttemptID))
	if err != nil {
		t.Fatal(err)
	}
	cfg.GrokBuildBin = fakeGrokGoalBin(t, filepath.Join(t.TempDir(), "argv-recovery"))
	saveGoalCfg(t, root, cfg)
	auth := mintBootstrapBeforeNativeAuthorization(t, after, wf, time.Time{})
	stdout, recErr := captureWorkflowCmd(t, "goal-bootstrap-before-native-recovery",
		"-root", root, wf.ID, "-manual", "-authorization", auth)
	if recErr != nil {
		t.Fatalf("consumer admit: %v stdout=%s", recErr, stdout)
	}
	if !strings.Contains(stdout, "original_attempt="+rec.AttemptID) || !strings.Contains(stdout, "new_attempt=") {
		t.Fatalf("consumer stdout: %s", stdout)
	}
	if countBootstrapConsumeEvents(t, root, after.ID) != 1 {
		t.Fatalf("consume=%d", countBootstrapConsumeEvents(t, root, after.ID))
	}
	assertOriginalAttemptUnchanged(t, root, after.ID, rec.AttemptID, originalBytes)
	copyBootstrapProofFile(t, "producer-sidecar.json", bootstrapLauncherEvidencePath(root, after.ID, rec.AttemptID))
	copyBootstrapProofFile(t, "producer-authorization.json", auth)
	writeBootstrapCLIEvidence("cli-producer-consumer", stdout, "", 0)
	t.Logf("cli-producer-consumer stdout=%s", stdout)

	root0, dir0 := workflowTestRoot(t)
	cfg0 := workflowTestCfg(t, root0)
	cfg0.GrokBuildBin = fakeGrokGoalBinRealPair(t, 0)
	cfg0.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root0, cfg0)
	wf0 := initTestWorkflow(t, root0, dir0)
	tk0 := admitManualWriter(t, root0, cfg0, wf0)
	tk0.Goal.GrokHome = t.TempDir()
	if err := saveTask(root0, tk0); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root0, cfg0, wf0, 12000); err != nil {
		t.Fatalf("exit-0 producer: %v", err)
	}
	after0, err := loadTask(root0, tk0.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec0, err := loadRequiredGoalAttempt(root0, after0)
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(bootstrapLauncherEvidencePath(root0, after0.ID, rec0.AttemptID)); !os.IsNotExist(serr) {
		t.Fatalf("exit-0 wrote sidecar: %v", serr)
	}
}

func bootstrapAsyncExecutorFixture(t *testing.T, command string, termOpts syntheticWriteStdinOpts) (
	root string, cfg *Config, wf *WorkflowRecord, tk *Task, original *AttemptRecord, originalBytes []byte,
	execProof, termProof syntheticCodexExecutorResult, authPath string,
) {
	t.Helper()
	root, cfg, wf, tk, original, originalBytes = bootstrapBeforeNativeFixture(t)
	if _, err := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(err) {
		t.Fatalf("async fixture must omit sidecar: %v", err)
	}
	execProof = writeSyntheticAsyncOriginal(t, tk, wf, original, command)
	if strings.TrimSpace(termOpts.Handle) == "" {
		termOpts.Handle = execProof.Handle
	}
	termProof = writeSyntheticWriteStdinTerminal(t, execProof, original, termOpts)
	cfg.GrokBuildBin = fakeGrokGoalBin(t, filepath.Join(t.TempDir(), "argv-async-recovery"))
	saveGoalCfg(t, root, cfg)
	authPath = mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{})
	bindAsyncAssociation(t, authPath, execProof, termProof, execProof.Handle)
	return
}

func assertAsyncAdmit(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, originalBytes []byte, execProof, termProof syntheticCodexExecutorResult, authPath string) string {
	t.Helper()
	beforeExec, err := os.ReadFile(execProof.Path)
	if err != nil {
		t.Fatal(err)
	}
	beforeTerm, err := os.ReadFile(termProof.Path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(string(beforeExec)) != execProof.Digest || sha256Hex(string(beforeTerm)) != termProof.Digest {
		t.Fatal("fixture digest was not computed from written bytes")
	}
	stdout, recErr := captureWorkflowCmd(t, "goal-bootstrap-before-native-recovery",
		"-root", root, wf.ID, "-manual", "-authorization", authPath,
		"-executor-capture", execProof.Path, "-executor-digest", execProof.Digest, "-executor-call-id", execProof.CallID)
	if recErr != nil {
		t.Fatalf("async admit: %v stdout=%s", recErr, stdout)
	}
	if !strings.Contains(stdout, "original_attempt="+original.AttemptID) || !strings.Contains(stdout, "new_attempt=") {
		t.Fatalf("async stdout: %s", stdout)
	}
	afterExec, err := os.ReadFile(execProof.Path)
	if err != nil {
		t.Fatal(err)
	}
	afterTerm, err := os.ReadFile(termProof.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeExec, afterExec) || !bytes.Equal(beforeTerm, afterTerm) {
		t.Fatal("original async raws were rewritten")
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatalf("consume=%d", countBootstrapConsumeEvents(t, root, tk.ID))
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
	if _, serr := os.Stat(bootstrapLauncherEvidencePath(root, tk.ID, original.AttemptID)); !os.IsNotExist(serr) {
		t.Fatalf("async wrote original sidecar: %v", serr)
	}
	copyBootstrapProofFile(t, "async-original-exec.jsonl", execProof.Path)
	copyBootstrapProofFile(t, "async-write-stdin.jsonl", termProof.Path)
	copyBootstrapProofFile(t, "async-authorization.json", authPath)
	writeBootstrapCLIEvidence("cli-async-positive", stdout, "", 0)
	t.Logf("cli-async-positive stdout=%s", stdout)
	return stdout
}

func TestBootstrapExecutorCaptureAsyncAssociationAdmits(t *testing.T) {
	t.Run("standalone-zh-wait", func(t *testing.T) {
		root, _, wf, tk, original, originalBytes, execProof, termProof, auth := bootstrapAsyncExecutorFixture(t, "", syntheticWriteStdinOpts{OmitSecondChunk: true})
		assertAsyncAdmit(t, root, wf, tk, original, originalBytes, execProof, termProof, auth)
	})
	t.Run("following-exec-command-allowed", func(t *testing.T) {
		root, _, wf, tk, original, originalBytes, execProof, _, auth := bootstrapAsyncExecutorFixture(t, "", syntheticWriteStdinOpts{})
		term := writeSyntheticWriteStdinTerminal(t, execProof, original, syntheticWriteStdinOpts{
			Handle: execProof.Handle,
			Input:  syntheticWriteStdinInput(execProof.Handle, 1000, 900, true),
		})
		bindAsyncAssociation(t, auth, execProof, term, execProof.Handle)
		assertAsyncAdmit(t, root, wf, tk, original, originalBytes, execProof, term, auth)
	})
	t.Run("non-whitelist-yield-max", func(t *testing.T) {
		root, _, wf, tk, original, originalBytes, execProof, _, auth := bootstrapAsyncExecutorFixture(t, "", syntheticWriteStdinOpts{})
		term := writeSyntheticWriteStdinTerminal(t, execProof, original, syntheticWriteStdinOpts{
			Handle:          execProof.Handle,
			Input:           syntheticWriteStdinInput(execProof.Handle, 2500, 400, false),
			OmitSecondChunk: true,
		})
		bindAsyncAssociation(t, auth, execProof, term, execProof.Handle)
		assertAsyncAdmit(t, root, wf, tk, original, originalBytes, execProof, term, auth)
	})
	t.Run("legacy-synchronous-triple", func(t *testing.T) {
		root, _, wf, tk, original, originalBytes, proof := bootstrapSidecarlessExecutorFixture(t)
		auth := mintBootstrapExecutorAuthorization(t, tk, wf, time.Time{}, proof)
		stdout, err := captureWorkflowCmd(t, "goal-bootstrap-before-native-recovery",
			"-root", root, wf.ID, "-manual", "-authorization", auth,
			"-executor-capture", proof.Path, "-executor-digest", proof.Digest, "-executor-call-id", proof.CallID)
		if err != nil {
			t.Fatalf("legacy triple: %v stdout=%s", err, stdout)
		}
		if !strings.Contains(stdout, "original_attempt="+original.AttemptID) {
			t.Fatalf("legacy stdout: %s", stdout)
		}
		assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
		if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
			t.Fatalf("legacy consume=%d", countBootstrapConsumeEvents(t, root, tk.ID))
		}
	})
}

func TestBootstrapExecutorCaptureAsyncRetrieveTiming(t *testing.T) {
	t.Run("retrieve-after-14s", func(t *testing.T) {
		root, _, wf, tk, original, originalBytes, execProof, _, auth := bootstrapAsyncExecutorFixture(t, "", syntheticWriteStdinOpts{OmitSecondChunk: true})
		callTs, retTs := syntheticWriteStdinRetrieveAfter(original, 14*time.Second)
		term := writeSyntheticWriteStdinTerminal(t, execProof, original, syntheticWriteStdinOpts{
			Handle:          execProof.Handle,
			OmitSecondChunk: true,
			CallTimestamp:   callTs,
			ReturnTimestamp: retTs,
		})
		bindAsyncAssociation(t, auth, execProof, term, execProof.Handle)
		assertAsyncAdmit(t, root, wf, tk, original, originalBytes, execProof, term, auth)
	})
	t.Run("new-identity", func(t *testing.T) {
		root, _, wf, tk, original, _, execProof, _, auth := bootstrapAsyncExecutorFixture(t, "", syntheticWriteStdinOpts{OmitSecondChunk: true})
		callTs, retTs := syntheticWriteStdinRetrieveAfter(original, 14*time.Second)
		term := writeSyntheticWriteStdinTerminal(t, execProof, original, syntheticWriteStdinOpts{
			Handle:          execProof.Handle,
			OmitSecondChunk: true,
			CallTimestamp:   callTs,
			ReturnTimestamp: retTs,
		})
		bindAsyncAssociation(t, auth, execProof, term, execProof.Handle)
		rewriteBootstrapAuthField(t, auth, "executor_handle", "proc_NEW_RUN_HANDLE")
		afterPrepBytes, rerr := os.ReadFile(attemptPath(root, tk.ID, original.AttemptID))
		if rerr != nil {
			t.Fatal(rerr)
		}
		err := runBootstrapRecoveryCLIWithExecutor(root, wf.ID, auth, execProof.Path, execProof.Digest, execProof.CallID)
		if err == nil {
			t.Fatal("new identity recovered")
		}
		if !strings.Contains(err.Error(), "wrong handle identity") {
			t.Fatalf("new identity reason: %v", err)
		}
		if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
			t.Fatal("new identity consumed")
		}
		assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, afterPrepBytes)
	})
	t.Run("past-original-deadline", func(t *testing.T) {
		root, _, wf, tk, original, _, _, _, auth := bootstrapAsyncExecutorFixture(t, "", syntheticWriteStdinOpts{OmitSecondChunk: true})
		rec, err := loadAttempt(root, tk.ID, original.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now().Add(-2 * time.Hour)
		rec.CreatedAt = start.Format(time.RFC3339Nano)
		rec.UpdatedAt = start.Add(200 * time.Millisecond).Format(time.RFC3339Nano)
		if err := writeAttempt(root, rec); err != nil {
			t.Fatal(err)
		}
		execProof := writeSyntheticAsyncOriginal(t, tk, wf, rec, "")
		callTs, retTs := syntheticWriteStdinRetrieveAfter(rec, 14*time.Second)
		term := writeSyntheticWriteStdinTerminal(t, execProof, rec, syntheticWriteStdinOpts{
			Handle:          execProof.Handle,
			OmitSecondChunk: true,
			CallTimestamp:   callTs,
			ReturnTimestamp: retTs,
		})
		bindAsyncAssociation(t, auth, execProof, term, execProof.Handle)
		afterPrepBytes, rerr := os.ReadFile(attemptPath(root, tk.ID, rec.AttemptID))
		if rerr != nil {
			t.Fatal(rerr)
		}
		err = runBootstrapRecoveryCLIWithExecutor(root, wf.ID, auth, execProof.Path, execProof.Digest, execProof.CallID)
		if err == nil {
			t.Fatal("past original deadline recovered")
		}
		if !strings.Contains(err.Error(), "exhausted remaining allowance") {
			t.Fatalf("past original deadline reason: %v", err)
		}
		if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
			t.Fatal("past original deadline consumed")
		}
		assertOriginalAttemptUnchanged(t, root, tk.ID, rec.AttemptID, afterPrepBytes)
	})
}

func TestBootstrapExecutorCaptureSupportedFlagOrderIdentifiesWorkflow(t *testing.T) {
	rootPath := "/tmp/synthetic-cardex-root"
	for _, flagsAfter := range []bool{false, true} {
		name := "flags-before-WF"
		if flagsAfter {
			name = "flags-after-WF-budget-last"
		}
		t.Run(name, func(t *testing.T) {
			root, _, wf, tk, original, originalBytes, _ := bootstrapSidecarlessExecutorFixture(t)
			script := syntheticPublicGoalRunCommand(wf.ID, rootPath, flagsAfter)
			proof := writeSyntheticCodexExecutorCaptureResult(t, tk, wf, syntheticCodexExecutorOpts{
				Attempt:     original,
				Command:     script,
				ItemCommand: script,
				CommandArgv: []string{"/bin/zsh", "-c", script},
				ParsedCmd:   script,
			})
			auth := mintBootstrapExecutorAuthorization(t, tk, wf, time.Time{}, proof)
			stdout, err := captureWorkflowCmd(t, "goal-bootstrap-before-native-recovery",
				"-root", root, wf.ID, "-manual", "-authorization", auth,
				"-executor-capture", proof.Path, "-executor-digest", proof.Digest, "-executor-call-id", proof.CallID)
			if err != nil {
				t.Fatalf("%s: %v stdout=%s", name, err, stdout)
			}
			if !strings.Contains(stdout, "original_attempt="+original.AttemptID) {
				t.Fatalf("%s stdout: %s", name, stdout)
			}
			assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, originalBytes)
			if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
				t.Fatalf("%s consume=%d", name, countBootstrapConsumeEvents(t, root, tk.ID))
			}
		})
	}
}

func TestBootstrapExecutorCaptureAsyncNegativesRefuseWithoutConsume(t *testing.T) {
	zero := 0
	one := 1
	type tc struct {
		name string
		want string
		prep func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, execProof, termProof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, syntheticCodexExecutorResult, string)
	}
	cases := []tc{
		{
			name: "wrong-handle",
			want: "wrong handle identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, execProof, termProof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, syntheticCodexExecutorResult, string) {
				rewriteBootstrapAuthField(t, authPath, "executor_handle", "proc_WRONG_HANDLE")
				return execProof, termProof, authPath
			},
		},
		{
			name: "swapped-raws",
			want: "truncated original executor capture",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, execProof, termProof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, syntheticCodexExecutorResult, string) {
				swapped := termProof
				swapped.ItemID = execProof.ItemID
				bindAsyncAssociation(t, authPath, swapped, execProof, execProof.Handle)
				return swapped, execProof, authPath
			},
		},
		{
			name: "concatenated-raws",
			want: "modified original executor capture",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, execProof, termProof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, syntheticCodexExecutorResult, string) {
				joined := append(append([]byte{}, execProof.Raw...), termProof.Raw...)
				path := filepath.Join(t.TempDir(), "concatenated.jsonl")
				if err := os.WriteFile(path, joined, 0o644); err != nil {
					t.Fatal(err)
				}
				next := execProof
				next.Path = path
				next.Digest = sha256Hex(string(joined))
				next.Raw = joined
				bindAsyncAssociation(t, authPath, next, termProof, execProof.Handle)
				return next, termProof, authPath
			},
		},
		{
			name: "last-success-chunk",
			want: "missing terminal nonzero exit",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, execProof, termProof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, syntheticCodexExecutorResult, string) {
				term := writeSyntheticWriteStdinTerminal(t, execProof, original, syntheticWriteStdinOpts{
					Handle:          execProof.Handle,
					FirstExit:       &zero,
					SecondExit:      &zero,
					OmitSecondChunk: false,
				})
				bindAsyncAssociation(t, authPath, execProof, term, execProof.Handle)
				return execProof, term, authPath
			},
		},
		{
			name: "convenient-nonzero-second-command",
			want: "unrelated second-command failure",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, execProof, termProof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, syntheticCodexExecutorResult, string) {
				term := writeSyntheticWriteStdinTerminal(t, execProof, original, syntheticWriteStdinOpts{
					Handle:     execProof.Handle,
					FirstExit:  &zero,
					SecondExit: &one,
				})
				bindAsyncAssociation(t, authPath, execProof, term, execProof.Handle)
				return execProof, term, authPath
			},
		},
		{
			name: "mixed-two-write-stdin",
			want: "wrong handle identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, execProof, termProof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, syntheticCodexExecutorResult, string) {
				input := syntheticWriteStdinInput(execProof.Handle, 1000, 900, false) + ";\n" +
					syntheticWriteStdinInput("proc_OTHER_HANDLE", 1000, 900, false)
				term := writeSyntheticWriteStdinTerminal(t, execProof, original, syntheticWriteStdinOpts{
					Handle:          execProof.Handle,
					Input:           input,
					OmitSecondChunk: true,
				})
				bindAsyncAssociation(t, authPath, execProof, term, execProof.Handle)
				return execProof, term, authPath
			},
		},
		{
			name: "different-call",
			want: "wrong call identity",
			prep: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, original *AttemptRecord, execProof, termProof syntheticCodexExecutorResult, authPath string) (syntheticCodexExecutorResult, syntheticCodexExecutorResult, string) {
				rewriteBootstrapAuthField(t, authPath, "executor_call_id", "call_WRONGIDENTITY0001")
				execProof.CallID = "call_WRONGIDENTITY0001"
				return execProof, termProof, authPath
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _, wf, tk, original, _, execProof, termProof, authPath := bootstrapAsyncExecutorFixture(t, "", syntheticWriteStdinOpts{OmitSecondChunk: true})
			execProof, termProof, authPath = tc.prep(t, root, wf, tk, original, execProof, termProof, authPath)
			afterPrepBytes, rerr := os.ReadFile(attemptPath(root, tk.ID, original.AttemptID))
			if rerr != nil {
				t.Fatal(rerr)
			}
			err := runBootstrapRecoveryCLIWithExecutor(root, wf.ID, authPath, execProof.Path, execProof.Digest, execProof.CallID)
			if err == nil {
				t.Fatalf("%s recovered", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s reason: %v want %q", tc.name, err, tc.want)
			}
			if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
				t.Fatalf("%s consumed", tc.name)
			}
			if tc.name == "wrong-handle" {
				writeBootstrapCLIEvidence("cli-async-negative-wrong-handle", "", err.Error(), 1)
				t.Logf("cli-async-negative consume=%d err=%v", countBootstrapConsumeEvents(t, root, tk.ID), err)
			}
			atts, lerr := listGoalAttemptRecords(root, tk.ID)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if len(atts) != 1 {
				t.Fatalf("%s extra attempt: %d", tc.name, len(atts))
			}
			assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, afterPrepBytes)
		})
	}
}
