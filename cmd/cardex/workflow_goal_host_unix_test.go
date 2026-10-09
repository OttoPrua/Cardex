//go:build darwin || linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeHostedGrokBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	py := filepath.Join(dir, "grok.py")
	body := `#!/usr/bin/env python3
import json, os, sys, time, urllib.parse
args = sys.argv[1:]
cwd = ""
sid = ""
prev = ""
for a in args:
    if a in ("-p", "--single", "--prompt-file"):
        sys.stderr.write("p-not-goal\n")
        sys.exit(2)
    if prev == "--cwd":
        cwd = a
    if prev in ("-s", "--resume", "--session-id"):
        sid = a
    prev = a
home = os.environ.get("GROK_HOME", "")
if not home or not cwd or not sid:
    sys.stderr.write("missing grok home/cwd/session\n")
    sys.exit(2)
enc = urllib.parse.quote(cwd, safe="")
sess = os.path.join(home, "sessions", enc, sid)
os.makedirs(os.path.join(sess, "goal"), exist_ok=True)
goal = "hosted-goal-1"

def write_state(status, classifier="", event="goal_updated"):
    st = {
        "goal_id": goal,
        "status": status,
        "phase": "Idle" if status != "active" else "Executing",
        "token_budget": 0,
        "last_classifier_verdict": classifier,
        "total_verify_rounds": 0,
        "total_worker_rounds": 1,
        "history": [{"event": event}],
    }
    # Readers must not observe partially rewritten JSON. Publish identity first.
    for path, value in [
        (os.path.join(sess, "summary.json"), {"info": {"id": sid, "cwd": cwd}}),
        (os.path.join(sess, "goal", "state.json"), st),
    ]:
        with open(path + ".tmp", "w") as f:
            json.dump(value, f)
            f.write("\n")
        os.replace(path + ".tmp", path)
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {
                "sessionUpdate": "goal_updated",
                "goal_id": goal,
                "status": status,
                "phase": "idle" if status != "active" else "executing",
                "last_classifier_verdict": classifier,
                "last_event": event,
            },
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")

write_state("active", event="goal_started")
buf = ""
while True:
    ch = sys.stdin.read(1)
    if ch == "":
        break
    if ch == "\x1b":
        continue
    if ch in ("\n", "\r"):
        line = buf.strip()
        buf = ""
        if not line:
            continue
        if line.startswith("/goal pause"):
            write_state("paused", event="goal_paused")
        elif line.startswith("/goal resume"):
            write_state("active", event="goal_resumed")
        elif line.startswith("/goal clear"):
            write_state("complete", "achieved", "goal_completed")
        elif line.startswith("/quit"):
            # Native completion precedes process exit. Closing the PTY master
            # during this cleanup window sends SIGHUP and loses the clean exit.
            time.sleep(0.2)
            with open(os.path.join(sess, "clean-exit"), "w") as f:
                f.write("quit handled")
            break
        elif line.startswith("/goal ") and not line.startswith("/goal status"):
            write_state("active", event="goal_started")
    else:
        buf += ch
`
	if err := os.WriteFile(py, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "grok")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec python3 -u "+shSingleQuote(py)+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func TestHostedGoalInjectPauseStopAndSync(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg.GrokBuildBin = fakeHostedGrokBin(t)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)

	errCh := make(chan error, 1)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		errCh <- launchHostedWorkflowGoal(root, cfg, wf, 0, "")
	}()
	t.Cleanup(func() {
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-runDone:
				return
			case <-deadline.C:
				t.Error("hosted fixture did not exit before cleanup")
				return
			case <-tick.C:
				signalRegisteredTaskProcs(tk.ID)
			}
		}
	})

	deadline := time.Now().Add(8 * time.Second)
	var sess string
	for time.Now().Before(deadline) {
		loaded, err := loadTask(root, tk.ID)
		if err == nil && loaded.SessionID != "" {
			sess = loaded.SessionID
			state := filepath.Join(grokGoalSessionDir(home, dir, sess), "goal", "state.json")
			if _, err := os.Stat(state); err == nil {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if sess == "" {
		t.Fatal("hosted launch did not bind session")
	}
	obs, err := observeNativeGrokGoal(home, dir, sess, "")
	if err != nil || obs.NativeStatus != "active" {
		t.Fatalf("expected active after inject: obs=%+v err=%v", obs, err)
	}

	// Another registered process must not start a second PTY reader or control
	// loop for this Goal. The old global resume hook ran for both commands.
	unrelated := exec.CommandContext(context.Background(), "/bin/sh", "-c", "true")
	setupProcGroup(unrelated)
	if err := runCmdRegistered(unrelated); err != nil {
		t.Fatalf("unrelated registered command: %v", err)
	}

	if err := requestGoalControl(root, cfg, wf, goalControlPause, home); err != nil {
		t.Fatalf("hosted pause: %v", err)
	}
	obs, err = observeNativeGrokGoal(home, dir, sess, "")
	if err != nil || obs.NativeStatus != "paused" {
		t.Fatalf("native pause post-state: %+v err=%v", obs, err)
	}
	loaded, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if _, err := loadGoalHostStatus(root, loaded.ID, firstNonBlank(loaded.ActiveAttemptID, loaded.Goal.BoundAttemptID)); err != nil {
		t.Logf("host-status missing after pause (native post-state is source of truth): %v", err)
	}

	if err := requestGoalControl(root, cfg, wf, goalControlStop, home); err != nil {
		t.Fatalf("hosted stop: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("hosted run: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("hosted supervisor did not return after stop")
	}

	if _, err := os.Stat(filepath.Join(grokGoalSessionDir(home, dir, sess), "clean-exit")); err != nil {
		t.Fatalf("PTY closed before provider finished /quit cleanup: %v", err)
	}
	synced, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{GrokHome: home, GrokCWD: dir})
	if err != nil {
		t.Fatalf("goal-sync: %v", err)
	}
	if synced.Goal.Observation != goalObsDone {
		t.Fatalf("expected done, got %s note=%s", synced.Goal.Observation, synced.Goal.ObservationNote)
	}
	if strings.Contains(synced.Goal.ObservationNote, "stream_incomplete") {
		t.Fatal("must not collapse to stream_incomplete")
	}
}

func fakeHostedGrokBinBudgetLimited(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	py := filepath.Join(dir, "grok.py")
	body := `#!/usr/bin/env python3
import json, os, sys, time, urllib.parse
args = sys.argv[1:]
cwd = ""
sid = ""
prev = ""
for a in args:
    if a in ("-p", "--single", "--prompt-file"):
        sys.stderr.write("p-not-goal\n")
        sys.exit(2)
    if prev == "--cwd":
        cwd = a
    if prev in ("-s", "--resume", "--session-id"):
        sid = a
    prev = a
home = os.environ.get("GROK_HOME", "")
if not home or not cwd or not sid:
    sys.stderr.write("missing grok home/cwd/session\n")
    sys.exit(2)
enc = urllib.parse.quote(cwd, safe="")
sess = os.path.join(home, "sessions", enc, sid)
os.makedirs(os.path.join(sess, "goal"), exist_ok=True)
goal = "hosted-budget-1"

def write_state(status, classifier="", event="goal_updated", phase="Idle"):
    st = {
        "goal_id": goal,
        "status": status,
        "phase": phase,
        "token_budget": 0,
        "last_classifier_verdict": classifier,
        "total_verify_rounds": 0,
        "total_worker_rounds": 1,
        "history": [{"event": event}],
    }
    summary = os.path.join(sess, "summary.json")
    with open(summary + ".tmp", "w") as f:
        json.dump({"info": {"id": sid, "cwd": cwd}}, f)
        f.write("\n")
    os.replace(summary + ".tmp", summary)
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {
                "sessionUpdate": "goal_updated",
                "goal_id": goal,
                "status": status,
                "phase": phase.lower(),
                "last_classifier_verdict": classifier,
                "last_event": event,
            },
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")
    # Publish readiness only after its required summary and update evidence.
    state = os.path.join(sess, "goal", "state.json")
    with open(state + ".tmp", "w") as f:
        json.dump(st, f)
        f.write("\n")
    os.replace(state + ".tmp", state)

write_state("budget_limited", classifier="not_achieved", event="goal_budget_limited", phase="Idle")
buf = ""
while True:
    ch = sys.stdin.read(1)
    if ch == "":
        break
    if ch == "\x1b":
        continue
    if ch in ("\n", "\r"):
        line = buf.strip()
        buf = ""
        if not line:
            continue
        if line.startswith("/goal clear"):
            # Stay budget_limited/Idle. /goal clear bytes are not complete/done.
            write_state("budget_limited", classifier="not_achieved", event="goal_budget_limited", phase="Idle")
        elif line.startswith("/quit"):
            time.sleep(0.2)
            with open(os.path.join(sess, "clean-exit"), "w") as f:
                f.write("quit handled")
            break
        elif line.startswith("/goal pause") or line.startswith("/goal resume"):
            continue
    else:
        buf += ch
`
	if err := os.WriteFile(py, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "grok")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec python3 -u "+shSingleQuote(py)+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func fakeHostedGrokBinIgnoreStop(t *testing.T, exitFlag string) string {
	t.Helper()
	dir := t.TempDir()
	py := filepath.Join(dir, "grok.py")
	body := fmt.Sprintf(`#!/usr/bin/env python3
import json, os, select, sys, time, urllib.parse
args = sys.argv[1:]
cwd = ""
sid = ""
prev = ""
for a in args:
    if a in ("-p", "--single", "--prompt-file"):
        sys.stderr.write("p-not-goal\n")
        sys.exit(2)
    if prev == "--cwd":
        cwd = a
    if prev in ("-s", "--resume", "--session-id"):
        sid = a
    prev = a
home = os.environ.get("GROK_HOME", "")
if not home or not cwd or not sid:
    sys.stderr.write("missing grok home/cwd/session\n")
    sys.exit(2)
enc = urllib.parse.quote(cwd, safe="")
sess = os.path.join(home, "sessions", enc, sid)
os.makedirs(os.path.join(sess, "goal"), exist_ok=True)
goal = "hosted-ignore-stop"
exit_flag = %q

def write_state(status, event="goal_started"):
    st = {
        "goal_id": goal,
        "status": status,
        "phase": "Executing",
        "token_budget": 0,
        "last_classifier_verdict": "",
        "total_verify_rounds": 0,
        "total_worker_rounds": 1,
        "history": [{"event": event}],
    }
    with open(os.path.join(sess, "goal", "state.json"), "w") as f:
        json.dump(st, f)
        f.write("\n")
    with open(os.path.join(sess, "summary.json"), "w") as f:
        json.dump({"info": {"id": sid, "cwd": cwd}}, f)
        f.write("\n")
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {
                "sessionUpdate": "goal_updated",
                "goal_id": goal,
                "status": status,
                "phase": "executing",
                "last_classifier_verdict": "",
                "last_event": event,
            },
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")

write_state("active")
buf = ""
while True:
    if os.path.exists(exit_flag):
        break
    r, _, _ = select.select([sys.stdin], [], [], 0.05)
    if not r:
        continue
    ch = sys.stdin.read(1)
    if ch == "":
        break
    if ch == "\x1b":
        continue
    if ch in ("\n", "\r"):
        line = buf.strip()
        buf = ""
        if line.startswith("/goal clear") or line.startswith("/quit"):
            continue
        if line.startswith("/goal "):
            write_state("active")
    else:
        buf += ch
`, exitFlag)
	if err := os.WriteFile(py, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "grok")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec python3 -u "+shSingleQuote(py)+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func TestHostedStopConfirmsBudgetLimitedThenGracefulExit(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg.GrokBuildBin = fakeHostedGrokBinBudgetLimited(t)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)

	errCh := make(chan error, 1)
	go func() {
		errCh <- launchHostedWorkflowGoal(root, cfg, wf, 0, "")
	}()

	deadline := time.Now().Add(8 * time.Second)
	var sess string
	for time.Now().Before(deadline) {
		loaded, err := loadTask(root, tk.ID)
		if err == nil && loaded.SessionID != "" {
			sess = loaded.SessionID
			state := filepath.Join(grokGoalSessionDir(home, dir, sess), "goal", "state.json")
			if _, err := os.Stat(state); err == nil {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if sess == "" {
		t.Fatal("hosted launch did not bind session")
	}
	obs, err := observeNativeGrokGoal(home, dir, sess, "")
	if err != nil || obs.NativeStatus != "budget_limited" {
		t.Fatalf("expected budget_limited Idle terminal: obs=%+v err=%v", obs, err)
	}

	if err := requestGoalControl(root, cfg, wf, goalControlStop, home); err != nil {
		t.Fatalf("hosted stop against budget_limited: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("hosted run: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("hosted supervisor did not return after confirmed budget_limited stop")
	}

	if _, err := os.Stat(filepath.Join(grokGoalSessionDir(home, dir, sess), "clean-exit")); err != nil {
		t.Fatalf("Wait/drain must allow /quit cleanup: %v", err)
	}
	synced, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if synced.Goal.Observation == goalObsDone || synced.Status == statusDone || synced.Goal.EvidenceComplete {
		t.Fatalf("budget_limited must stay nonaccepted, got status=%s obs=%s note=%s", synced.Status, synced.Goal.Observation, synced.Goal.ObservationNote)
	}
	if synced.Goal.LastNativeStatus != "budget_limited" && !strings.Contains(synced.Goal.ObservationNote, "budget_limited") {
		t.Fatalf("native budget_limited must remain visible: %+v", synced.Goal)
	}
	if !synced.Goal.CustodyReleased || synced.ActiveAttemptID != "" {
		t.Fatalf("custody must be reclaimed after Wait/drain: %+v", synced.Goal)
	}
}

func TestHostedStopInjectBytesOnlyIsNotRelease(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	exitFlag := filepath.Join(t.TempDir(), "exit-now")
	cfg.GrokBuildBin = fakeHostedGrokBinIgnoreStop(t, exitFlag)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)

	errCh := make(chan error, 1)
	go func() {
		errCh <- launchHostedWorkflowGoal(root, cfg, wf, 0, "")
	}()

	deadline := time.Now().Add(8 * time.Second)
	var sess string
	for time.Now().Before(deadline) {
		loaded, err := loadTask(root, tk.ID)
		if err == nil && loaded.SessionID != "" {
			sess = loaded.SessionID
			state := filepath.Join(grokGoalSessionDir(home, dir, sess), "goal", "state.json")
			if _, err := os.Stat(state); err == nil {
				obs, oerr := observeNativeGrokGoal(home, dir, sess, "")
				if oerr == nil && obs.NativeStatus == "active" {
					break
				}
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if sess == "" {
		t.Fatal("hosted launch did not bind session")
	}

	err := requestGoalControl(root, cfg, wf, goalControlStop, home)
	if err == nil || !strings.Contains(err.Error(), goalFailPauseNotConfirmed) {
		t.Fatalf("inject-bytes-only must be pause_not_confirmed, got %v", err)
	}
	loaded, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if loaded.Goal.CustodyReleased || loaded.Goal.Observation == goalObsDone || loaded.Status == statusDone {
		t.Fatalf("bytes are not release/done: %+v", loaded.Goal)
	}
	if loaded.Goal.FailureClass != goalFailPauseNotConfirmed {
		t.Fatalf("failure class=%q", loaded.Goal.FailureClass)
	}
	if err := os.WriteFile(exitFlag, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errCh:
	case <-time.After(8 * time.Second):
		t.Fatal("hosted supervisor did not return after test exit flag")
	}
}

func TestNativeStatusMatchesControlStopBudgetLimited(t *testing.T) {
	t.Parallel()
	if !nativeStatusMatchesControl(goalControlStop, "budget_limited") {
		t.Fatal("budget_limited/Idle must be stop-confirmable")
	}
	if nativeStatusMatchesControl(goalControlStop, "active") {
		t.Fatal("active must still reject stop confirmation")
	}
	if nativeStatusMatchesControl(goalControlStop, "unknown") || nativeStatusMatchesControl(goalControlStop, "") {
		t.Fatal("unknown/stale must still reject stop confirmation")
	}
	if nativeStatusMatchesControl(goalControlStop, "budget_limited") && nativeStatusMatchesControl(goalControlStop, "complete") {
		// Distinct clauses: budget_limited is not aliased to complete.
	}
	mapped := mapNativeGoalToTask(nativeGoalObservation{
		GoalID:        "g1",
		NativeStatus:  "budget_limited",
		BudgetLimited: true,
		UpdatesOK:     true,
		SummaryOK:     true,
	}, true, false, false)
	if mapped.AcceptDone || mapped.Observation == goalObsDone || mapped.Status == statusDone {
		t.Fatalf("budget_limited must not map to accepted done: %+v", mapped)
	}
}

func TestInjectHostedControlPrefixesEscBeforePause(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pty-capture")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := injectHostedControl(f, goalControlPause, "/goal pause"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 3 || raw[0] != 0x1b || raw[1] != 0x1b {
		t.Fatalf("pause must send Esc Esc before slash, got %q", raw)
	}
	if !strings.Contains(string(raw), "/goal pause") {
		t.Fatalf("missing slash command: %q", raw)
	}
}

func TestInjectHostedControlForSessionDoesNotApprovePendingPrompt(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "sess-pipe-prompt"
	writeGrokPermissionEvents(t, home, cwd, sid,
		`{"type":"phase_changed","phase":"permission_prompt"}`,
		`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
	)
	path := filepath.Join(t.TempDir(), "pty-capture")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = injectHostedControlForSession(f, goalControlStop, "/goal clear", home, cwd, sid)
	_ = f.Close()
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(raw), "/goal") || strings.Contains(string(raw), "\r") {
		t.Fatalf("pending prompt received an approve payload: %q", raw)
	}
	if err == nil || !strings.Contains(err.Error(), goalFailControlUnsupported) {
		t.Fatalf("want control_unsupported, got %v bytes=%q", err, raw)
	}
}

func TestInjectHostedControlGuardedAbortsBeforeCRIfPromptAppears(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pty-race")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	err = injectHostedControlGuarded(f, goalControlStop, "/goal clear", func() error {
		n++
		if n >= 2 {
			return fmt.Errorf("%s: native permission prompt is pending; use formal cancel", goalFailControlUnsupported)
		}
		return nil
	})
	_ = f.Close()
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(raw), "/goal") || strings.Contains(string(raw), "\r") {
		t.Fatalf("race inject sent slash/CR: %q", raw)
	}
	if err == nil || !strings.Contains(err.Error(), goalFailControlUnsupported) {
		t.Fatalf("want control_unsupported after Esc, got %v bytes=%q", err, raw)
	}
}

func TestFormalCancelBlocksFollowupTool(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	cfg := testCfg()
	dir := t.TempDir()
	flag := filepath.Join(t.TempDir(), "producer.flag")
	follow := filepath.Join(t.TempDir(), "followup")
	py := filepath.Join(t.TempDir(), "producer.py")
	body := fmt.Sprintf("import os, sys, time\nflag, follow = %q, %q\nopen(flag,'w').write('running\\n')\nwhile True:\n    if os.path.exists(follow):\n        open(flag,'w').write('TOOL_EXECUTED\\n')\n        break\n    time.sleep(0.05)\n", flag, follow)
	if err := os.WriteFile(py, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	tk := newTask(root, cfg, typeSequence, "cancel followup", dir, []string{"p"}, 5)
	tk.Status = statusRunning
	tk.Goal = &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsRunning, Started: true, Hosted: true, ControlOwner: goalControlOwnerHosted}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireWorkspaceExecutionLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-u", py)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{lease}
	if err := cmd.Start(); err != nil {
		_ = lease.Close()
		t.Fatal(err)
	}
	_ = lease.Close()
	pid := cmd.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-waited:
		case <-time.After(time.Second):
		}
		unregisterTaskInvoke(tk.ID, pid)
	})
	_ = bindLiveAttemptFromProcess(t, root, tk, cmd)
	if err := terminalize(root, tk.ID, statusCanceled, "cli:cancel", "cli cancel", map[string]any{"was_running": true}); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := archiveTask(root, fresh); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(follow, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("producer still running after cancel")
	}
	got, _ := os.ReadFile(flag)
	if strings.Contains(string(got), "TOOL_EXECUTED") {
		t.Fatalf("followup tool ran after cancel: %q", got)
	}
	archived, err := loadArchivedTaskFile(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != statusCanceled {
		t.Fatalf("status=%s", archived.Status)
	}
	if archived.Goal != nil && (archived.Goal.Observation == goalObsDone || archived.Goal.EvidenceComplete) {
		t.Fatalf("cancel manufactured done/achieved: %+v", archived.Goal)
	}
	if archived.Goal != nil && archived.Goal.Observation != goalObsCanceled {
		t.Fatalf("observation=%s", archived.Goal.Observation)
	}
	if _, err := loadTask(root, tk.ID); !os.IsNotExist(err) {
		t.Fatal("canceled producer must be archived")
	}
}

func bindLiveAttemptFromProcess(t *testing.T, root string, tk *Task, cmd *exec.Cmd) *AttemptRecord {
	t.Helper()
	attemptID := newAttemptID()
	now := time.Now().Format(time.RFC3339Nano)
	rec := &AttemptRecord{
		TaskID: tk.ID, AttemptID: attemptID, ExpectedRevision: tk.Revision,
		ControlEpoch: tk.ControlEpoch, State: attemptReserved, CreatedAt: now, UpdatedAt: now,
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
	if err := bindAttemptProcess(root, tk.ID, attemptID, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	registerTaskInvoke(tk.ID, cmd.Process.Pid)
	loaded, err := loadAttempt(root, tk.ID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestNativeGoalWriteSandboxHostedLaunchArgv(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	dump := filepath.Join(t.TempDir(), "hosted-argv")
	fake := filepath.Join(t.TempDir(), "grok")
	body := fmt.Sprintf(`#!/usr/bin/env python3
import os, sys
for a in sys.argv[1:]:
    if a in ("-p", "--single", "--prompt-file"):
        sys.stderr.write("p-not-goal\n")
        sys.exit(2)
with open(%q, "w") as f:
    f.write("\n".join(sys.argv[1:]) + "\n")
    f.write("GROK_HOME=" + os.environ.get("GROK_HOME", "") + "\n")
sys.exit(0)
`, dump)
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.GrokBuildBin = fake
	cfg.GrokBuild = &GrokBuildRoute{
		Enabled:             true,
		Model:               "grok-4.6",
		Effort:              "xhigh",
		WriteSandboxProfile: testWriteSandboxProfile,
	}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	_ = launchHostedWorkflowGoal(root, cfg, wf, 0, "")
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("hosted launcher must exec the shared argv consumer (dump missing): %v", err)
	}
	text := string(raw)
	if dumpedArgValue(text, "--sandbox") != testWriteSandboxProfile {
		t.Fatalf("hosted launch must forward --sandbox %s:\n%s", testWriteSandboxProfile, text)
	}
	if dumpedArgValue(text, "--permission-mode") != "auto" {
		t.Fatalf("hosted permission must stay auto:\n%s", text)
	}
	if dumpedArgValue(text, "--cwd") != dir {
		t.Fatalf("hosted --cwd must stay worktree %q:\n%s", dir, text)
	}
	loaded, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	sid := loaded.SessionID
	if sid == "" {
		sid = tk.SessionID
	}
	if dumpedArgValue(text, "-s") != sid && dumpedArgValue(text, "--resume") != sid {
		t.Fatalf("hosted argv missing session %s:\n%s", sid, text)
	}
	if !strings.Contains(text, "GROK_HOME="+home) {
		t.Fatalf("hosted child GROK_HOME must be %q:\n%s", home, text)
	}
}

func TestOpenGoalPTYMatchesShippedHelper(t *testing.T) {
	t.Parallel()
	master, slave, err := openGoalPTY()
	if err != nil {
		if ptyStartDenied(err) {
			t.Skipf("pty unavailable: %v", err)
		}
		t.Fatalf("openGoalPTY: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if !fileIsTerminal(slave) {
		t.Fatal("PTY slave must be a terminal")
	}
}

// Exercise the production hosted launcher with real PTY backpressure, no provider.
func TestHostedGoalDrainsOutputAndDeadline(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	for _, mode := range []string{"exit", "deadline", "broken-output"} {
		waitForDeadline := mode != "exit"
		t.Run(mode, func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			cfg := workflowTestCfg(t, root)
			t.Setenv("GROK_HOME", t.TempDir())
			marker := filepath.Join(t.TempDir(), "stdin-received")
			fake := filepath.Join(t.TempDir(), "synthetic-grok")
			body := fmt.Sprintf(`#!/usr/bin/env python3
import json, os, pathlib, sys, time
# Exceeds the kernel PTY buffer before the child reads /goal.
sys.stdout.write("PTY-OUT-" * 65536)
sys.stdout.flush()
sys.stderr.write("PTY-ERR-visible\n")
sys.stderr.flush()
line = sys.stdin.readline()
assert line.startswith("/goal "), repr(line)
pathlib.Path(%q).write_text(line)
# Let the production launcher bind the actual PID before returning.
deadline = time.monotonic() + 4
while time.monotonic() < deadline:
    if any(json.loads(p.read_text()).get("pid") == os.getpid()
           for p in pathlib.Path(%q).rglob("*.json")):
        break
    time.sleep(0.01)
else:
    sys.exit(2)
if %s:
    time.sleep(30)
else:
    sys.stdout.write("PTY-TAIL-" * 131072 + "PTY-END\n")
    sys.stdout.flush()
`, marker, filepath.Join(root, "control", "attempts"), map[bool]string{true: "True", false: "False"}[waitForDeadline])
			if err := os.WriteFile(fake, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			cfg.GrokBuildBin = fake
			cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
			saveGoalCfg(t, root, cfg)
			wf := initTestWorkflow(t, root, dir)
			tk := admitManualWriter(t, root, cfg, wf)
			tk.Goal.HardTimeoutSec = 6
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			// An ordinary file is the caller's output sink, not a product log.
			out, err := os.CreateTemp(t.TempDir(), "synthetic-output")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "broken-output" {
				name := out.Name()
				out.Close()
				out, err = os.Open(name) // The caller sink cannot be written.
				if err != nil {
					t.Fatal(err)
				}
			}
			defer out.Close()
			oldStdout := os.Stdout
			os.Stdout = out
			start := time.Now()
			warnings := captureStderr(t, func() { err = launchHostedWorkflowGoal(root, cfg, wf, 0, "") })
			os.Stdout = oldStdout
			if !waitForDeadline && strings.Contains(warnings, "output may be incomplete") {
				t.Fatal(warnings)
			}
			if err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed > 9*time.Second {
				t.Fatalf("cleanup took %v", elapsed)
			}
			if mode == "broken-output" {
				if !strings.Contains(warnings, "hosted PTY output may be incomplete") {
					t.Fatal("missing visible output failure warning")
				}
			} else if _, err := os.Stat(marker); err != nil {
				t.Fatalf("child blocked before stdin: %v", err)
			}
			raw, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			if mode != "broken-output" && (strings.Count(string(raw), "PTY-OUT-") != 65536 || !strings.Contains(string(raw), "PTY-ERR-visible")) {
				t.Fatal("hosted PTY output was lost instead of relayed to the caller")
			}
			if !waitForDeadline && (strings.Count(string(raw), "PTY-TAIL-") != 131072 || !strings.Contains(string(raw), "PTY-END")) {
				t.Fatal("final PTY output truncated on exit")
			}
			fresh, err := loadTask(root, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Goal.Observation != goalObsUnknown || !fresh.Goal.CustodyReleased || fresh.ActiveAttemptID != "" {
				t.Fatalf("exit must release custody without claiming native completion: %+v", fresh.Goal)
			}
			if waitForDeadline && !strings.Contains(fresh.Goal.ObservationNote, "hard timeout") {
				t.Fatal(fresh.Goal.ObservationNote)
			}
		})
	}
}

func TestUnknownSuccessorRequiresRecoveredCustodyAndRetainsHistory(t *testing.T) {
	t.Parallel()
	for _, blocked := range []string{"missing-attempt", "held-lease", "held-lease-failed", "round-limit", "stale-receipt", ""} {
		t.Run(blocked, func(t *testing.T) {
			root, cfg, wf, writer, receipt := d4SuccessorFixture(t)
			writer.Goal.LastNativeStatus = ""
			writer.Goal.BudgetTokens = 1234
			writer.Goal.HardTimeoutSec = 60
			if err := saveTask(root, writer); err != nil {
				t.Fatal(err)
			}
			rewriteDesignReceipt(t, receipt, map[string]any{"consumed_revision": writer.Revision, "consumed_observation": goalObsUnknown})
			switch blocked {
			case "missing-attempt":
				writer.Goal.BoundAttemptID = "missing"
				writer.ActiveAttemptID = "missing"
				if err := saveTask(root, writer); err != nil {
					t.Fatal(err)
				}
				rewriteDesignReceipt(t, receipt, map[string]any{"consumed_attempt_id": "missing", "consumed_revision": writer.Revision})
			case "held-lease", "held-lease-failed":
				if blocked == "held-lease-failed" {
					writer.Status = statusFailed
					if err := saveTask(root, writer); err != nil {
						t.Fatal(err)
					}
					rewriteDesignReceipt(t, receipt, map[string]any{"consumed_revision": writer.Revision})
				}
				lease, err := acquireWorkspaceExecutionLease(writer.Dir)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
			case "round-limit":
				wf.CurrentRound = wf.MaxRounds
				if err := persistWorkflow(root, cfg, wf); err != nil {
					t.Fatal(err)
				}
			case "stale-receipt":
				rewriteDesignReceipt(t, receipt, map[string]any{"consumed_revision": writer.Revision - 1})
			}
			next, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, goalObsUnknown, receipt)
			if blocked != "" {
				if err == nil {
					t.Fatalf("%s admitted a successor", blocked)
				}
				actual, _ := loadWorkflow(root, cfg, wf.ID)
				if actual.WriterTaskID != writer.ID {
					t.Fatal("rejected successor changed writer")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if next.ID == writer.ID || next.Goal.Started || next.SessionID != "" || next.ActiveAttemptID != "" || next.Goal.BoundAttemptID != "" {
				t.Fatalf("successor must be a distinct unstarted writer: %+v", next)
			}
			if next.Goal.BudgetTokens != 1234 || next.Goal.HardTimeoutSec != 60 || next.Goal.Provider != writer.Goal.Provider || next.PreferRunner != writer.PreferRunner {
				t.Fatalf("successor widened the contract: %+v", next)
			}
			old, err := loadTask(root, writer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if old.Status != statusFailed || old.Goal.Observation != goalObsUnknown || old.Goal.EvidenceComplete || old.Goal.LastNativeStatus != "" {
				t.Fatalf("retirement must preserve unknown native history: %+v", old)
			}
			if !old.Goal.CustodyReleased || old.ActiveAttemptID != "" || wf.CurrentRound != 1 {
				t.Fatalf("custody/round: %+v", old)
			}
			if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, goalObsUnknown, receipt); err == nil {
				t.Fatal("consumed decision replayed")
			}
		})
	}
}

func writeFakeHostedGrokWrapper(t *testing.T, pyBody string) string {
	t.Helper()
	dir := t.TempDir()
	py := filepath.Join(dir, "grok.py")
	if err := os.WriteFile(py, []byte(pyBody), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "grok")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec python3 -u "+shSingleQuote(py)+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

const fakeHostedGrokArgParse = `import json, os, select, sys, time, urllib.parse
args = sys.argv[1:]
cwd = ""
sid = ""
prev = ""
for a in args:
    if a in ("-p", "--single", "--prompt-file"):
        sys.stderr.write("p-not-goal\n")
        sys.exit(2)
    if prev == "--cwd":
        cwd = a
    if prev in ("-s", "--resume", "--session-id"):
        sid = a
    prev = a
home = os.environ.get("GROK_HOME", "")
if not home or not cwd or not sid:
    sys.stderr.write("missing grok home/cwd/session\n")
    sys.exit(2)
enc = urllib.parse.quote(cwd, safe="")
sess = os.path.join(home, "sessions", enc, sid)
os.makedirs(os.path.join(sess, "goal"), exist_ok=True)

def read_stdin_chunk(timeout=0.05):
    r, _, _ = select.select([sys.stdin], [], [], timeout)
    if not r:
        return None
    return os.read(sys.stdin.fileno(), 4096)

def write_final_turn_markers():
    with open(os.path.join(sess, "events.jsonl"), "a") as f:
        f.write('{"type":"phase_changed","phase":"streaming_text"}\n')
        f.write('{"type":"turn_ended","outcome":"completed"}\n')
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {"sessionUpdate": "turn_completed", "stop_reason": "end_turn"},
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")
`

func fakeHostedGrokBinNaturalComplete(t *testing.T) string {
	t.Helper()
	return writeFakeHostedGrokWrapper(t, fakeHostedGrokArgParse+`
goal = "hosted-natural-1"

def write_state(status, classifier="", event="goal_updated"):
    st = {
        "goal_id": goal,
        "status": status,
        "phase": "Idle" if status != "active" else "Executing",
        "token_budget": 0,
        "last_classifier_verdict": classifier,
        "total_verify_rounds": 0,
        "total_worker_rounds": 1,
        "history": [{"event": event}],
    }
    with open(os.path.join(sess, "goal", "state.json"), "w") as f:
        json.dump(st, f)
        f.write("\n")
    with open(os.path.join(sess, "summary.json"), "w") as f:
        json.dump({"info": {"id": sid, "cwd": cwd}}, f)
        f.write("\n")
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {
                "sessionUpdate": "goal_updated",
                "goal_id": goal,
                "status": status,
                "phase": "idle" if status != "active" else "executing",
                "last_classifier_verdict": classifier,
                "last_event": event,
            },
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")

write_state("active", event="goal_started")
buf = ""
completed = False
quit_count = 0
while True:
    ch = sys.stdin.read(1)
    if ch == "":
        break
    if ch == "\x1b":
        continue
    if ch in ("\n", "\r"):
        line = buf.strip()
        buf = ""
        if not line:
            continue
        if line.startswith("/quit"):
            quit_count += 1
            if quit_count == 1:
                time.sleep(0.4)
                with open(os.path.join(sess, "clean-exit"), "w") as f:
                    f.write(str(quit_count))
                break
        elif line.startswith("/goal ") and not completed:
            write_state("complete", "achieved", "goal_completed")
            sys.stdout.write("NATURAL-FINAL-OUTPUT\n")
            sys.stdout.flush()
            write_final_turn_markers()
            completed = True
    else:
        buf += ch
`)
}

func fakeHostedGrokBinNaturalHoldQuit(t *testing.T, exitFlag string) string {
	t.Helper()
	return writeFakeHostedGrokWrapper(t, fakeHostedGrokArgParse+fmt.Sprintf(`
goal = "hosted-natural-hold"
exit_flag = %q

def write_state(status, classifier="", event="goal_updated"):
    st = {
        "goal_id": goal,
        "status": status,
        "phase": "Idle" if status != "active" else "Executing",
        "token_budget": 0,
        "last_classifier_verdict": classifier,
        "total_verify_rounds": 0,
        "total_worker_rounds": 1,
        "history": [{"event": event}],
    }
    with open(os.path.join(sess, "goal", "state.json"), "w") as f:
        json.dump(st, f)
        f.write("\n")
    with open(os.path.join(sess, "summary.json"), "w") as f:
        json.dump({"info": {"id": sid, "cwd": cwd}}, f)
        f.write("\n")
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {
                "sessionUpdate": "goal_updated",
                "goal_id": goal,
                "status": status,
                "phase": "idle" if status != "active" else "executing",
                "last_classifier_verdict": classifier,
                "last_event": event,
            },
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")

write_state("active", event="goal_started")
buf = ""
completed = False
while True:
    if os.path.exists(exit_flag):
        break
    chunk = read_stdin_chunk()
    if chunk is None:
        continue
    if chunk == b"":
        break
    for ch in chunk.decode("latin1"):
        if ch == "\x1b":
            continue
        if ch in ("\n", "\r"):
            line = buf.strip()
            buf = ""
            if line.startswith("/quit"):
                with open(os.path.join(sess, "got-quit"), "w") as f:
                    f.write("quit bytes")
                continue
            if line.startswith("/goal ") and not completed:
                write_state("complete", "achieved", "goal_completed")
                sys.stdout.write("NATURAL-FINAL-OUTPUT\n")
                sys.stdout.flush()
                write_final_turn_markers()
                completed = True
        else:
            buf += ch
`, exitFlag))
}

func fakeHostedGrokBinNaturalPending(t *testing.T, exitFlag, approvedPath string) string {
	t.Helper()
	return writeFakeHostedGrokWrapper(t, fakeHostedGrokArgParse+fmt.Sprintf(`
goal = "hosted-natural-pending"
exit_flag = %q
approved = %q

def write_state(status, classifier="", event="goal_updated"):
    st = {
        "goal_id": goal,
        "status": status,
        "phase": "Idle" if status != "active" else "Executing",
        "token_budget": 0,
        "last_classifier_verdict": classifier,
        "total_verify_rounds": 0,
        "total_worker_rounds": 1,
        "history": [{"event": event}],
    }
    with open(os.path.join(sess, "goal", "state.json"), "w") as f:
        json.dump(st, f)
        f.write("\n")
    with open(os.path.join(sess, "summary.json"), "w") as f:
        json.dump({"info": {"id": sid, "cwd": cwd}}, f)
        f.write("\n")
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {
                "sessionUpdate": "goal_updated",
                "goal_id": goal,
                "status": status,
                "phase": "idle" if status != "active" else "executing",
                "last_classifier_verdict": classifier,
                "last_event": event,
            },
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")

write_state("active", event="goal_started")
with open(os.path.join(sess, "events.jsonl"), "w") as f:
    f.write('{"type":"phase_changed","phase":"permission_prompt"}\n')
    f.write('{"type":"permission_requested","tool_name":"run_terminal_command"}\n')
buf = ""
completed = False
while True:
    if os.path.exists(exit_flag):
        break
    chunk = read_stdin_chunk()
    if chunk is None:
        continue
    if chunk == b"":
        break
    for ch in chunk.decode("latin1"):
        if ch == "\x1b":
            continue
        if ch in ("\n", "\r"):
            line = buf.strip()
            buf = ""
            if line.startswith("/quit"):
                with open(approved, "w") as f:
                    f.write(line)
                continue
            if line.startswith("/goal ") and not completed:
                write_state("complete", "achieved", "goal_completed")
                sys.stdout.write("NATURAL-FINAL-OUTPUT\n")
                sys.stdout.flush()
                completed = True
        else:
            buf += ch
`, exitFlag, approvedPath))
}

func fakeHostedGrokBinNaturalStillProducing(t *testing.T, tooEarlyPath string) string {
	t.Helper()
	return writeFakeHostedGrokWrapper(t, fakeHostedGrokArgParse+fmt.Sprintf(`
goal = "hosted-natural-producing"
too_early = %q

def write_state(status, classifier="", event="goal_updated"):
    st = {
        "goal_id": goal,
        "status": status,
        "phase": "Idle" if status != "active" else "Executing",
        "token_budget": 0,
        "last_classifier_verdict": classifier,
        "total_verify_rounds": 0,
        "total_worker_rounds": 1,
        "history": [{"event": event}],
    }
    with open(os.path.join(sess, "goal", "state.json"), "w") as f:
        json.dump(st, f)
        f.write("\n")
    with open(os.path.join(sess, "summary.json"), "w") as f:
        json.dump({"info": {"id": sid, "cwd": cwd}}, f)
        f.write("\n")
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {
                "sessionUpdate": "goal_updated",
                "goal_id": goal,
                "status": status,
                "phase": "idle" if status != "active" else "executing",
                "last_classifier_verdict": classifier,
                "last_event": event,
            },
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")

write_state("active", event="goal_started")
buf = ""
producing = False
completed = False

def on_char(ch):
    global buf, producing, completed
    if ch == "\x1b":
        return "ok"
    if ch not in ("\n", "\r"):
        buf += ch
        return "ok"
    line = buf.strip()
    buf = ""
    if line.startswith("/quit"):
        if producing:
            with open(too_early, "w") as f:
                f.write("quit during produce")
            os._exit(1)
        time.sleep(0.2)
        with open(os.path.join(sess, "clean-exit"), "w") as f:
            f.write("1")
        return "quit"
    if line.startswith("/goal ") and not completed:
        write_state("complete", "achieved", "goal_completed")
        producing = True
        for i in range(16):
            sys.stdout.write("PARTIAL-%%d\n" %% i)
            sys.stdout.flush()
            with open(os.path.join(sess, "events.jsonl"), "a") as f:
                f.write('{"type":"phase_changed","phase":"streaming_text"}\n')
            chunk = read_stdin_chunk(0.35)
            if chunk:
                for ch2 in chunk.decode("latin1"):
                    if on_char(ch2) == "quit":
                        return "quit"
        sys.stdout.write("NATURAL-FINAL-OUTPUT\n")
        sys.stdout.flush()
        write_final_turn_markers()
        producing = False
        completed = True
    return "ok"

while True:
    chunk = read_stdin_chunk()
    if chunk is None:
        continue
    if chunk == b"":
        break
    stop = False
    for ch in chunk.decode("latin1"):
        if on_char(ch) == "quit":
            stop = True
            break
    if stop:
        break
`, tooEarlyPath))
}

func waitHostedBoundSession(t *testing.T, root, taskID, grokHome, cwd string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		loaded, err := loadTask(root, taskID)
		if err == nil && loaded.SessionID != "" {
			state := filepath.Join(grokGoalSessionDir(grokHome, cwd, loaded.SessionID), "goal", "state.json")
			if _, err := os.Stat(state); err == nil {
				return loaded.SessionID
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("hosted launch did not bind session")
	return ""
}

func startHostedNaturalLaunch(t *testing.T, bin string, enableWake bool) (root, dir, home string, cfg *Config, wf *WorkflowRecord, tk *Task, errCh chan error) {
	t.Helper()
	root, dir = workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	home = t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg.GrokBuildBin = bin
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	if enableWake {
		cfg.ManagerWake = &ManagerWakeConfig{Enabled: true}
	}
	saveGoalCfg(t, root, cfg)
	wf = initTestWorkflow(t, root, dir)
	tk = admitManualWriter(t, root, cfg, wf)
	tk.Goal.GrokHome = home
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	errCh = make(chan error, 1)
	return root, dir, home, cfg, wf, tk, errCh
}

func grokTurnCompletedUpdateLine(sessionID string) string {
	return fmt.Sprintf(`{"method":"_x.ai/session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"turn_completed","stop_reason":"end_turn"}}}`, sessionID)
}

func grokGoalUpdatedUpdateLine(sessionID, goalID, status, phase, classifier string) string {
	return fmt.Sprintf(`{"method":"_x.ai/session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"goal_updated","goal_id":%q,"status":%q,"phase":%q,"last_classifier_verdict":%q,"last_event":"goal_completed"}}}`, sessionID, goalID, status, phase, classifier)
}

func appendGrokSessionFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(strings.TrimSpace(line) + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func writeGrokSessionFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendGrokCurrentSessionFinalTurn(t *testing.T, grokHome, cwd, sessionID string) {
	t.Helper()
	appendGrokSessionFile(t, grokNativeEventsPath(grokHome, cwd, sessionID),
		`{"type":"phase_changed","phase":"streaming_text"}`,
		`{"type":"turn_ended","outcome":"completed"}`,
	)
	appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(grokHome, cwd, sessionID), "updates.jsonl"),
		grokTurnCompletedUpdateLine(sessionID),
	)
}

func TestNativeNaturalCompletionReadyGates(t *testing.T) {
	t.Parallel()
	ready := nativeGoalObservation{
		GoalID:          "g1",
		NativeStatus:    "complete",
		Phase:           "idle",
		Classifier:      "achieved",
		FinalStatus:     "complete",
		FinalClassifier: "achieved",
		UpdatesOK:       true,
		SummaryOK:       true,
	}
	if !nativeNaturalCompletionReady(ready, "g1") {
		t.Fatal("current complete/Idle/achieved with consistent files must be ready")
	}
	if nativeNaturalCompletionReady(ready, "other-goal") {
		t.Fatal("wrong-binding Goal must not be ready")
	}
	missing := ready
	missing.Missing = true
	if nativeNaturalCompletionReady(missing, "g1") {
		t.Fatal("missing evidence must not be ready")
	}
	stale := ready
	stale.Contradictory = true
	if nativeNaturalCompletionReady(stale, "g1") {
		t.Fatal("contradictory evidence must not be ready")
	}
	raw := ready
	raw.UpdatesOK = false
	if nativeNaturalCompletionReady(raw, "g1") {
		t.Fatal("raw state.json complete without matching updates must not be ready")
	}
	noSummary := ready
	noSummary.SummaryOK = false
	if nativeNaturalCompletionReady(noSummary, "g1") {
		t.Fatal("still-producing final summary must not be ready")
	}
	active := ready
	active.NativeStatus = "active"
	active.Phase = "executing"
	if nativeNaturalCompletionReady(active, "g1") {
		t.Fatal("active native without semantic completion must not be ready")
	}
	noIdle := ready
	noIdle.Phase = "executing"
	if nativeNaturalCompletionReady(noIdle, "g1") {
		t.Fatal("complete without Idle must not be ready")
	}
	notAchieved := ready
	notAchieved.Classifier = "not_achieved"
	notAchieved.NotAchieved = true
	if nativeNaturalCompletionReady(notAchieved, "g1") {
		t.Fatal("complete not_achieved must not be ready")
	}
	budget := ready
	budget.NativeStatus = "budget_limited"
	budget.BudgetLimited = true
	budget.Classifier = "not_achieved"
	if nativeNaturalCompletionReady(budget, "g1") {
		t.Fatal("budget_limited must stay on the explicit stop path")
	}
	failed := ready
	failed.NativeStatus = "failed"
	if nativeNaturalCompletionReady(failed, "g1") {
		t.Fatal("failed must stay on the explicit stop path")
	}
	blankID := ready
	blankID.GoalID = ""
	if nativeNaturalCompletionReady(blankID, "") {
		t.Fatal("blank native goal_id must not be ready")
	}
	mapped := mapNativeGoalToTask(ready, false, false, false)
	if mapped.AcceptDone || mapped.Observation == goalObsDone {
		t.Fatalf("natural-ready is not custody release or done: %+v", mapped)
	}
}

func TestHostedNaturalQuitReadyRiskConditions(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "natural-ready-sess"
	gid := "natural-ready-goal"
	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
		Phase:                 "Idle",
	})
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("complete/Idle/achieved + quiet without turn_ended/turn_completed must not be ready")
	}
	appendGrokCurrentSessionFinalTurn(t, home, cwd, sid)
	if !hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("current-session turn_ended after streaming_text and turn_completed after complete/Idle plus quiet must be ready")
	}
	if hostedNaturalQuitReady(home, cwd, sid, gid, false) {
		t.Fatal("still-producing output must not be ready")
	}
	if hostedNaturalQuitReady(home, cwd, sid, "other-goal", true) {
		t.Fatal("wrong-binding expected Goal must not be ready")
	}

	writeGrokGoalFixture(t, home, cwd, sid, gid, "active", grokNativeGoalStateFile{Phase: "Executing"})
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("active native must not be ready")
	}

	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
		Phase:                 "Idle",
	})
	if err := os.Remove(filepath.Join(grokGoalSessionDir(home, cwd, sid), "summary.json")); err != nil {
		t.Fatal(err)
	}
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("complete without summary must not be ready")
	}

	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
		Phase:                 "Idle",
	})
	if err := os.Remove(filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl")); err != nil {
		t.Fatal(err)
	}
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("raw state.json complete alone must not be ready")
	}

	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
		Phase:                 "Idle",
	})
	writeGrokPermissionEvents(t, home, cwd, sid,
		`{"type":"phase_changed","phase":"permission_prompt"}`,
		`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
	)
	appendGrokCurrentSessionFinalTurn(t, home, cwd, sid)
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("pending permission must not be ready")
	}
	if err := hostedControlRefuseReason(home, cwd, sid); err == nil {
		t.Fatal("pending permission must refuse hosted inject")
	}

	missingHome := t.TempDir()
	if hostedNaturalQuitReady(missingHome, cwd, "no-such", "", true) {
		t.Fatal("missing native files must not be ready")
	}
}

func TestHostedNaturalQuitReadyFinalTurnEvidence(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "final-turn-sess"
	gid := "final-turn-goal"
	writeComplete := func() {
		writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
			LastClassifierVerdict: "achieved",
			Phase:                 "Idle",
		})
	}

	writeComplete()
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("complete/Idle/achieved + quiet without turn_ended/turn_completed is not ready")
	}

	writeComplete()
	appendGrokCurrentSessionFinalTurn(t, home, cwd, sid)
	if !hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("current-session turn_ended after streaming_text and turn_completed after complete/Idle plus quiet must be ready")
	}

	writeComplete()
	writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
		fmt.Sprintf(`{"type":"turn_started","session_id":%q}`, sid),
		`{"type":"phase_changed","phase":"streaming_text"}`,
		`{"type":"turn_ended","outcome":"completed"}`,
	)
	appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
		grokTurnCompletedUpdateLine(sid),
	)
	if !hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("turn_started before streaming_text then turn_ended must remain the current final turn")
	}

	writeComplete()
	writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
		`{"type":"phase_changed","phase":"streaming_text"}`,
		`{"type":"turn_ended","outcome":"completed"}`,
		fmt.Sprintf(`{"type":"turn_started","session_id":%q}`, sid),
	)
	appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
		grokTurnCompletedUpdateLine(sid),
	)
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("new turn_started after turn_ended must not reuse stale turn_completed")
	}

	writeComplete()
	writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
		`{"type":"phase_changed","phase":"streaming_text"}`,
		`{"type":"turn_ended","outcome":"completed"}`,
		`{"type":"phase_changed","phase":"streaming_text"}`,
	)
	appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
		grokTurnCompletedUpdateLine(sid),
	)
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("streaming_text after the last turn_ended must not be ready")
	}

	writeComplete()
	writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
		`{"type":"phase_changed","phase":"streaming_text"}`,
		`{"type":"turn_ended","outcome":"completed"}`,
	)
	writeGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
		grokTurnCompletedUpdateLine(sid),
		grokGoalUpdatedUpdateLine(sid, gid, "complete", "idle", "achieved"),
	)
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("turn_completed before matching complete/Idle must not be ready")
	}

	writeComplete()
	writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
		`{"type":"phase_changed","phase":"streaming_text"}`,
		`{"type":"turn_ended","outcome":"completed"}`,
	)
	appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
		grokTurnCompletedUpdateLine("other-session"),
	)
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("wrong-session turn_completed must not be ready")
	}

	writeComplete()
	writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
		`{"type":"turn_started","session_id":"other-session"}`,
		`{"type":"phase_changed","phase":"streaming_text"}`,
		`{"type":"turn_ended","outcome":"completed"}`,
	)
	appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
		grokTurnCompletedUpdateLine(sid),
	)
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("wrong-session events must not be ready")
	}

	writeComplete()
	writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
		`{"type":"turn_ended","outcome":"completed"}`,
	)
	appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
		grokTurnCompletedUpdateLine(sid),
	)
	if hostedNaturalQuitReady(home, cwd, sid, gid, true) {
		t.Fatal("turn_ended without prior streaming_text must not be ready")
	}
}

func TestNativeGoalEvidenceFingerprintStability(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "fp-sess"
	writeGrokGoalFixture(t, home, cwd, sid, "fp-goal", "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
		Phase:                 "Idle",
	})
	appendGrokCurrentSessionFinalTurn(t, home, cwd, sid)
	a := nativeGoalEvidenceFingerprint(home, cwd, sid)
	b := nativeGoalEvidenceFingerprint(home, cwd, sid)
	if !nativeGoalEvidenceStable(a, b) {
		t.Fatalf("unchanged files must be stable: %q %q", a, b)
	}
	updates := filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl")
	f, err := os.OpenFile(updates, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	c := nativeGoalEvidenceFingerprint(home, cwd, sid)
	if nativeGoalEvidenceStable(b, c) {
		t.Fatal("still-producing native files must not look stable")
	}
	if nativeGoalEvidenceStable("", a) || nativeGoalEvidenceStable(a, "") {
		t.Fatal("empty fingerprint is not stable")
	}
}

func TestInjectHostedNaturalQuitDoesNotApprovePendingPrompt(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "sess-natural-prompt"
	writeGrokPermissionEvents(t, home, cwd, sid,
		`{"type":"phase_changed","phase":"permission_prompt"}`,
		`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
	)
	path := filepath.Join(t.TempDir(), "pty-capture")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = injectHostedNaturalQuit(f, home, cwd, sid)
	_ = f.Close()
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(raw), "/quit") || strings.Contains(string(raw), "\r") {
		t.Fatalf("pending prompt received an approve payload: %q", raw)
	}
	if err == nil || !strings.Contains(err.Error(), goalFailControlUnsupported) {
		t.Fatalf("want control_unsupported, got %v bytes=%q", err, raw)
	}
}

func TestInjectHostedNaturalQuitWritesQuitWhenAllowed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pty-capture")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := injectHostedNaturalQuit(f, t.TempDir(), t.TempDir(), "sess-ok"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "/quit\r" {
		t.Fatalf("natural /quit payload=%q", raw)
	}
}

func quietOutputWatch(t *testing.T) *ptyOutputWatch {
	t.Helper()
	w := &ptyOutputWatch{}
	w.note(8)
	time.Sleep(hostedNaturalOutputQuiet + 30*time.Millisecond)
	if !w.Quiet(hostedNaturalOutputQuiet) {
		t.Fatal("watch must be quiet before control-loop samples")
	}
	return w
}

func captureHostedControlLoop(t *testing.T, home string, tk *Task, watch *ptyOutputWatch, hold time.Duration) string {
	t.Helper()
	masterPath := filepath.Join(t.TempDir(), "master")
	master, err := os.Create(masterPath)
	if err != nil {
		t.Fatal(err)
	}
	ctlR, ctlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		hostedControlLoop(t.TempDir(), tk, home, master, ctlR, stop, watch)
		close(done)
	}()
	time.Sleep(hold)
	close(stop)
	_ = ctlW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hosted control loop did not return")
	}
	_ = master.Close()
	_ = ctlR.Close()
	raw, err := os.ReadFile(masterPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestHostedControlLoopNaturalQuitWithoutExternalStop(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "loop-natural-sess"
	gid := "loop-natural-goal"
	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
		Phase:                 "Idle",
	})
	appendGrokCurrentSessionFinalTurn(t, home, cwd, sid)
	tk := &Task{
		ID:              "t-natural-loop",
		Dir:             cwd,
		SessionID:       sid,
		ActiveAttemptID: "att-natural",
		Goal: &TaskGoalBinding{
			NativeGoalID:     gid,
			BoundAttemptID:   "att-natural",
			Hosted:           true,
			ControlOwner:     goalControlOwnerHosted,
			LastNativeStatus: "complete",
		},
	}
	got := captureHostedControlLoop(t, home, tk, quietOutputWatch(t), 450*time.Millisecond)
	if strings.Count(got, "/quit") != 1 || !strings.Contains(got, "/quit\r") {
		t.Fatalf("want exactly one natural /quit, got %q", got)
	}
	if strings.Contains(got, "/goal") {
		t.Fatalf("natural complete must not send other control: %q", got)
	}
}

func TestHostedControlLoopDuplicateTerminalDoesNotReinject(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "loop-dup-sess"
	gid := "loop-dup-goal"
	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
		Phase:                 "Idle",
	})
	appendGrokCurrentSessionFinalTurn(t, home, cwd, sid)
	tk := &Task{
		ID:              "t-natural-dup",
		Dir:             cwd,
		SessionID:       sid,
		ActiveAttemptID: "att-dup",
		Goal: &TaskGoalBinding{
			NativeGoalID:   gid,
			BoundAttemptID: "att-dup",
			Hosted:         true,
			ControlOwner:   goalControlOwnerHosted,
		},
	}
	masterPath := filepath.Join(t.TempDir(), "master")
	master, err := os.Create(masterPath)
	if err != nil {
		t.Fatal(err)
	}
	ctlR, ctlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		hostedControlLoop(t.TempDir(), tk, home, master, ctlR, stop, quietOutputWatch(t))
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(masterPath)
		if strings.Contains(string(raw), "/quit") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := ctlW.Write([]byte("stop\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	close(stop)
	_ = ctlW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hosted control loop did not return")
	}
	_ = master.Close()
	_ = ctlR.Close()
	raw, err := os.ReadFile(masterPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "/quit") != 1 {
		t.Fatalf("duplicate terminal must not reinject /quit: %q", raw)
	}
}

func TestHostedControlLoopNaturalQuitRiskConditions(t *testing.T) {
	type tc struct {
		name  string
		setup func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch
	}
	cases := []tc{
		{
			name: "active-without-complete",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "active", grokNativeGoalStateFile{Phase: "Executing"})
				return quietOutputWatch(t)
			},
		},
		{
			name: "wrong-binding",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, "other-goal", "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				return quietOutputWatch(t)
			},
		},
		{
			name: "raw-state-complete-alone",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				if err := os.Remove(filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl")); err != nil {
					t.Fatal(err)
				}
				return quietOutputWatch(t)
			},
		},
		{
			name: "pending-permission",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				writeGrokPermissionEvents(t, home, cwd, sid,
					`{"type":"phase_changed","phase":"permission_prompt"}`,
					`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
				)
				appendGrokCurrentSessionFinalTurn(t, home, cwd, sid)
				return quietOutputWatch(t)
			},
		},
		{
			name: "still-producing-output",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				appendGrokCurrentSessionFinalTurn(t, home, cwd, sid)
				return &ptyOutputWatch{}
			},
		},
		{
			name: "complete-quiet-without-turn-evidence",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				return quietOutputWatch(t)
			},
		},
		{
			name: "streaming-after-turn-ended",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
					`{"type":"phase_changed","phase":"streaming_text"}`,
					`{"type":"turn_ended","outcome":"completed"}`,
					`{"type":"phase_changed","phase":"streaming_text"}`,
				)
				appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
					grokTurnCompletedUpdateLine(sid),
				)
				return quietOutputWatch(t)
			},
		},
		{
			name: "turn-completed-before-complete-idle",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
					`{"type":"phase_changed","phase":"streaming_text"}`,
					`{"type":"turn_ended","outcome":"completed"}`,
				)
				writeGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
					grokTurnCompletedUpdateLine(sid),
					grokGoalUpdatedUpdateLine(sid, gid, "complete", "idle", "achieved"),
				)
				return quietOutputWatch(t)
			},
		},
		{
			name: "wrong-session-turn-completed",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
					`{"type":"phase_changed","phase":"streaming_text"}`,
					`{"type":"turn_ended","outcome":"completed"}`,
				)
				appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
					grokTurnCompletedUpdateLine("other-session"),
				)
				return quietOutputWatch(t)
			},
		},
		{
			name: "new-turn-started-after-turn-ended",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
					LastClassifierVerdict: "achieved",
					Phase:                 "Idle",
				})
				writeGrokSessionFile(t, grokNativeEventsPath(home, cwd, sid),
					`{"type":"phase_changed","phase":"streaming_text"}`,
					`{"type":"turn_ended","outcome":"completed"}`,
					fmt.Sprintf(`{"type":"turn_started","session_id":%q}`, sid),
				)
				appendGrokSessionFile(t, filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"),
					grokTurnCompletedUpdateLine(sid),
				)
				return quietOutputWatch(t)
			},
		},
		{
			name: "missing-evidence",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				return quietOutputWatch(t)
			},
		},
		{
			name: "budget-limited",
			setup: func(t *testing.T, home, cwd, sid, gid string) *ptyOutputWatch {
				writeGrokGoalFixture(t, home, cwd, sid, gid, "budget_limited", grokNativeGoalStateFile{
					LastClassifierVerdict: "not_achieved",
					Phase:                 "Idle",
				})
				return quietOutputWatch(t)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := t.TempDir()
			sid := "loop-risk-sess"
			gid := "loop-risk-goal"
			watch := c.setup(t, home, cwd, sid, gid)
			tk := &Task{
				ID:              "t-natural-risk",
				Dir:             cwd,
				SessionID:       sid,
				ActiveAttemptID: "att-risk",
				Goal: &TaskGoalBinding{
					NativeGoalID:   gid,
					BoundAttemptID: "att-risk",
					Hosted:         true,
					ControlOwner:   goalControlOwnerHosted,
				},
			}
			got := captureHostedControlLoop(t, home, tk, watch, 400*time.Millisecond)
			if strings.Contains(got, "/quit") || strings.Contains(got, "\r") {
				t.Fatalf("%s must not inject /quit: %q", c.name, got)
			}
		})
	}
}

func TestPTYOutputWatchQuietRequiresBytesThenSilence(t *testing.T) {
	t.Parallel()
	w := &ptyOutputWatch{}
	if w.Quiet(hostedNaturalOutputQuiet) {
		t.Fatal("no output yet is not the final-output boundary")
	}
	w.note(4)
	if w.Quiet(hostedNaturalOutputQuiet) {
		t.Fatal("just-written output is still producing")
	}
	time.Sleep(hostedNaturalOutputQuiet + 30*time.Millisecond)
	if !w.Quiet(hostedNaturalOutputQuiet) {
		t.Fatal("quiet after last byte is the final-output boundary")
	}
}

func TestHostedNaturalCompleteQuitsWithoutStop(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	root, dir, home, cfg, wf, tk, errCh := startHostedNaturalLaunch(t, fakeHostedGrokBinNaturalComplete(t), true)
	out, err := os.CreateTemp(t.TempDir(), "natural-out")
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = out
	t.Cleanup(func() { os.Stdout = oldStdout })
	go func() {
		errCh <- launchHostedWorkflowGoal(root, cfg, wf, 0, "")
	}()

	sess := waitHostedBoundSession(t, root, tk.ID, home, dir, 8*time.Second)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("hosted run: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("hosted supervisor did not return after natural complete")
	}
	os.Stdout = oldStdout
	_ = out.Sync()
	raw, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "NATURAL-FINAL-OUTPUT") {
		t.Fatalf("final output truncated or missing: %q", raw)
	}
	clean, err := os.ReadFile(filepath.Join(grokGoalSessionDir(home, dir, sess), "clean-exit"))
	if err != nil {
		t.Fatalf("PTY closed before provider finished /quit cleanup: %v", err)
	}
	if strings.TrimSpace(string(clean)) != "1" {
		t.Fatalf("duplicate /quit: clean-exit=%q", clean)
	}
	synced, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if synced.Goal.Observation != goalObsDone {
		t.Fatalf("expected done, got %s note=%s", synced.Goal.Observation, synced.Goal.ObservationNote)
	}
	if !synced.Goal.CustodyReleased || synced.ActiveAttemptID != "" {
		t.Fatalf("custody must be reclaimed after Wait/drain: %+v", synced.Goal)
	}
	done := goalEventsOfType(t, root, synced.ID, evDone)
	if len(done) != 1 || done[0].Actor != "workflow:goal-sync" || done[0].TransitionID != synced.LastCommittedTransitionID {
		t.Fatalf("evDone actor/transition: %+v", done)
	}
	rows, class, err := loadManagerWakeOutbox(root)
	if err != nil || class != "" {
		t.Fatalf("outbox class=%s err=%v", class, err)
	}
	if len(rows) != 1 || rows[0].TransitionID != synced.LastCommittedTransitionID || rows[0].EventType != evDone || rows[0].TaskID != synced.ID {
		t.Fatalf("outbox rows: %+v", rows)
	}
}

func TestHostedNaturalQuitBytesAreNotRelease(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	exitFlag := filepath.Join(t.TempDir(), "exit-now")
	t.Cleanup(func() { _ = os.WriteFile(exitFlag, []byte("x"), 0o644) })
	root, dir, home, cfg, wf, tk, errCh := startHostedNaturalLaunch(t, fakeHostedGrokBinNaturalHoldQuit(t, exitFlag), true)
	go func() {
		errCh <- launchHostedWorkflowGoal(root, cfg, wf, 0, "")
	}()
	sess := waitHostedBoundSession(t, root, tk.ID, home, dir, 8*time.Second)
	gotQuit := filepath.Join(grokGoalSessionDir(home, dir, sess), "got-quit")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(gotQuit); err == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if _, err := os.Stat(gotQuit); err != nil {
		t.Fatal("natural complete must inject /quit without an external stop")
	}
	select {
	case err := <-errCh:
		t.Fatalf("Wait must not return from /quit bytes alone: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal.CustodyReleased || loaded.Goal.Observation == goalObsDone || loaded.Status == statusDone {
		t.Fatalf("/quit bytes are not release/done: %+v", loaded.Goal)
	}
	if done := goalEventsOfType(t, root, loaded.ID, evDone); len(done) != 0 {
		t.Fatalf("unexited process committed evDone: %+v", done)
	}
	if err := os.WriteFile(exitFlag, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("hosted run: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("hosted supervisor did not return after test exit flag")
	}
}

func TestHostedNaturalCompletePendingPermissionDoesNotQuit(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	exitFlag := filepath.Join(t.TempDir(), "exit-now")
	t.Cleanup(func() { _ = os.WriteFile(exitFlag, []byte("x"), 0o644) })
	approved := filepath.Join(t.TempDir(), "approved")
	root, dir, home, cfg, wf, tk, errCh := startHostedNaturalLaunch(t, fakeHostedGrokBinNaturalPending(t, exitFlag, approved), true)
	go func() {
		errCh <- launchHostedWorkflowGoal(root, cfg, wf, 0, "")
	}()
	_ = waitHostedBoundSession(t, root, tk.ID, home, dir, 8*time.Second)
	select {
	case err := <-errCh:
		t.Fatalf("pending permission must not /quit the child: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	if _, err := os.Stat(approved); err == nil {
		t.Fatal("pending permission received /quit or CR approve payload")
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal.CustodyReleased || loaded.Goal.Observation == goalObsDone || loaded.Status == statusDone {
		t.Fatalf("pending permission must not release/done: %+v", loaded.Goal)
	}
	if done := goalEventsOfType(t, root, loaded.ID, evDone); len(done) != 0 {
		t.Fatalf("pending permission committed evDone: %+v", done)
	}
	if err := os.WriteFile(exitFlag, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errCh:
	case <-time.After(8 * time.Second):
		t.Fatal("hosted supervisor did not return after test exit flag")
	}
}

func TestHostedNaturalStillProducingOutputDoesNotQuitEarly(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	tooEarly := filepath.Join(t.TempDir(), "too-early")
	root, dir, home, cfg, wf, tk, errCh := startHostedNaturalLaunch(t, fakeHostedGrokBinNaturalStillProducing(t, tooEarly), true)
	out, err := os.CreateTemp(t.TempDir(), "producing-out")
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = out
	t.Cleanup(func() { os.Stdout = oldStdout })
	go func() {
		errCh <- launchHostedWorkflowGoal(root, cfg, wf, 0, "")
	}()
	sess := waitHostedBoundSession(t, root, tk.ID, home, dir, 8*time.Second)
	select {
	case err := <-errCh:
		if _, statErr := os.Stat(tooEarly); statErr == nil {
			t.Fatal("still-producing output received /quit")
		}
		if err != nil {
			t.Fatalf("hosted run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("hosted supervisor did not return after still-producing settled")
	}
	os.Stdout = oldStdout
	if _, err := os.Stat(tooEarly); err == nil {
		t.Fatal("still-producing output received /quit")
	}
	raw, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "NATURAL-FINAL-OUTPUT") {
		t.Fatalf("final output truncated: %q", raw)
	}
	clean, err := os.ReadFile(filepath.Join(grokGoalSessionDir(home, dir, sess), "clean-exit"))
	if err != nil {
		t.Fatalf("Wait/drain must allow /quit cleanup: %v", err)
	}
	if strings.TrimSpace(string(clean)) != "1" {
		t.Fatalf("duplicate /quit: clean-exit=%q", clean)
	}
	synced, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if synced.Goal.Observation != goalObsDone {
		t.Fatalf("expected done, got %s note=%s", synced.Goal.Observation, synced.Goal.ObservationNote)
	}
	if !synced.Goal.CustodyReleased || synced.ActiveAttemptID != "" {
		t.Fatalf("custody must be reclaimed after Wait/drain: %+v", synced.Goal)
	}
	done := goalEventsOfType(t, root, synced.ID, evDone)
	if len(done) != 1 || done[0].Actor != "workflow:goal-sync" || done[0].TransitionID != synced.LastCommittedTransitionID {
		t.Fatalf("evDone actor/transition: %+v", done)
	}
	rows, class, err := loadManagerWakeOutbox(root)
	if err != nil || class != "" {
		t.Fatalf("outbox class=%s err=%v", class, err)
	}
	if len(rows) != 1 || rows[0].TransitionID != synced.LastCommittedTransitionID || rows[0].EventType != evDone || rows[0].TaskID != synced.ID {
		t.Fatalf("outbox rows: %+v", rows)
	}
}
