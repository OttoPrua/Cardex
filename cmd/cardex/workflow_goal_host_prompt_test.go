package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func writeGrokPermissionEvents(t *testing.T, grokHome, cwd, sessionID string, lines ...string) string {
	t.Helper()
	dir := grokGoalSessionDir(grokHome, cwd, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := grokNativeEventsPath(grokHome, cwd, sessionID)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativePermissionPromptPendingFromEvents(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "sess-events"
	pending := writeGrokPermissionEvents(t, home, cwd, sid,
		`{"type":"tool_started","tool_name":"run_terminal_command"}`,
		`{"type":"phase_changed","phase":"permission_prompt"}`,
		`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
	)
	if !nativePermissionPromptPending(pending) {
		t.Fatal("unresolved permission_requested must be pending")
	}
	if err := hostedControlRefuseReason(home, cwd, sid); err == nil || !errors.Is(err, errGoalCapability) || !strings.Contains(err.Error(), goalFailControlUnsupported) {
		t.Fatalf("pending prompt must be control_unsupported: %v", err)
	}

	resolved := writeGrokPermissionEvents(t, home, cwd, sid,
		`{"type":"phase_changed","phase":"permission_prompt"}`,
		`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
		`{"type":"permission_resolved","tool_name":"run_terminal_command","decision":"allow"}`,
		`{"type":"phase_changed","phase":"tool_execution"}`,
	)
	if nativePermissionPromptPending(resolved) {
		t.Fatal("resolved permission must not stay pending")
	}
	if err := hostedControlRefuseReason(home, cwd, sid); err != nil {
		t.Fatalf("resolved prompt must allow hosted control: %v", err)
	}

	missing := grokNativeEventsPath(home, cwd, "no-such-session")
	if nativePermissionPromptPending(missing) {
		t.Fatal("missing events are not a pending prompt")
	}
}

func seedHostedWriterForControl(t *testing.T, root, dir string, cfg *Config, wf *WorkflowRecord, sessionID string) *Task {
	t.Helper()
	tk := admitManualWriter(t, root, cfg, wf)
	tk.SessionID = sessionID
	tk.Goal.Hosted = true
	tk.Goal.ControlOwner = goalControlOwnerHosted
	tk.Goal.NativeGoalID = "hosted-goal-prompt"
	tk.Goal.BoundAttemptID = "atprompt000000001"
	tk.ActiveAttemptID = "atprompt000000001"
	tk.Goal.Observation = goalObsRunning
	if err := writeTaskFile(root, tk); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestRequestGoalControlRefusesPendingPermissionPrompt(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	wf := initTestWorkflow(t, root, dir)
	tk := seedHostedWriterForControl(t, root, dir, cfg, wf, "sess-fakeprompt")
	writeGrokPermissionEvents(t, home, dir, tk.SessionID,
		`{"type":"phase_changed","phase":"permission_prompt"}`,
		`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
	)
	err := requestGoalControl(root, cfg, wf, goalControlStop, home)
	if err == nil {
		t.Fatal("pending permission prompt must refuse hosted stop")
	}
	if strings.Contains(err.Error(), "control owner not live") {
		t.Fatalf("must refuse the permission modal, not a missing fifo: %v", err)
	}
	if !errors.Is(err, errGoalCapability) && !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("want capability/unconfirmed, got %v", err)
	}
	if !strings.Contains(err.Error(), goalFailControlUnsupported) && !strings.Contains(err.Error(), goalFailPauseNotConfirmed) {
		t.Fatalf("want control_unsupported or pause_not_confirmed, got %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "permission") && !strings.Contains(strings.ToLower(err.Error()), "cancel") {
		t.Fatalf("refusal must name the permission/cancel bound: %v", err)
	}
	loaded, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if loaded.Goal.ControlConfirmed || loaded.Goal.Observation == goalObsDone || loaded.Status == statusDone {
		t.Fatalf("unconfirmed control claimed stop/done: %+v", loaded.Goal)
	}
	if loaded.Goal.FailureClass != goalFailControlUnsupported && loaded.Goal.FailureClass != goalFailPauseNotConfirmed {
		t.Fatalf("failure class=%q", loaded.Goal.FailureClass)
	}
}
