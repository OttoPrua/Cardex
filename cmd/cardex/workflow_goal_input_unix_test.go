//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeOperatorPromptGrok(t *testing.T) string {
	bin := fakeHostedGrokBin(t)
	p := filepath.Join(filepath.Dir(bin), "grok.py")
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(body), "import json, os, sys, time, urllib.parse", "import json, os, sys, time, urllib.parse, tty, fcntl, termios, struct", 1)
	s = strings.Replace(s, "buf = \"\"\nwhile True:", `tty.setraw(sys.stdin.fileno())
rows, cols, _, _ = struct.unpack("HHHH", fcntl.ioctl(sys.stdin, termios.TIOCGWINSZ, b"\0" * 8))
with open(os.path.join(sess, "screen-size"), "w") as f:
    f.write(str(rows) + "x" + str(cols))
pending = False
buf = ""
while True:`, 1)
	s = strings.Replace(s, `    if ch == "\x1b":`, `    if ch == "1" and pending:
        pending = False
        with open(os.path.join(sess, "allowed-once"), "w") as f:
            f.write("explicit operator key 1")
        with open(os.path.join(sess, "events.jsonl"), "a") as f:
            f.write(json.dumps({"type": "permission_resolved", "decision": "allow_once"}) + "\n")
            f.write(json.dumps({"type": "phase_changed", "phase": "Executing"}) + "\n")
        print("LOCAL TOOL ALLOWED ONCE", flush=True)
        continue
    if ch == "\x1b":`, 1)
	s = strings.Replace(s, `            write_state("active", event="goal_started")`, `            write_state("active", event="goal_started")
            pending = True
            with open(os.path.join(sess, "events.jsonl"), "a") as f:
                f.write(json.dumps({"type": "phase_changed", "phase": "permission_prompt"}) + "\n")
                f.write(json.dumps({"type": "permission_requested", "tool_name": "fake-local-compile"}) + "\n")
            print("PENDING fake-local-compile: 1 Allow once; 2 Deny", flush=True)`, 1)
	if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestHostedOperatorInputRealCLIAndControls(t *testing.T) {
	skipIfGoalPTYUnavailable(t)
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg.GrokBuildBin = fakeOperatorPromptGrok(t)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	cli := filepath.Join(t.TempDir(), "cardex")
	build := exec.Command("go", "build", "-o", cli, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real CLI: %v %s", err, out)
	}
	readOutput, writeOutput, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writeOutput
	defer func() { os.Stdout = originalStdout }()
	outputDone := make(chan string, 1)
	go func() { var b bytes.Buffer; _, _ = io.Copy(&b, readOutput); outputDone <- b.String() }()
	runDone := make(chan error, 1)
	go func() { runDone <- launchHostedWorkflowGoal(root, cfg, wf, 0, "") }()
	joined := false
	defer func() {
		if !joined {
			signalRegisteredTaskProcs(tk.ID)
			select {
			case <-runDone:
			case <-time.After(8 * time.Second):
				t.Error("fake hosted cleanup did not return")
			}
		}
		_ = writeOutput.Close()
		_ = readOutput.Close()
	}()
	var current *Task
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, _ = loadTask(root, tk.ID)
		if current != nil && current.SessionID != "" && nativePermissionPromptPending(grokNativeEventsPath(home, dir, current.SessionID)) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current == nil || !nativePermissionPromptPending(grokNativeEventsPath(home, dir, current.SessionID)) {
		t.Fatal("fake native prompt did not become pending")
	}
	sessDir := grokGoalSessionDir(home, dir, current.SessionID)
	if size, err := os.ReadFile(filepath.Join(sessDir, "screen-size")); err != nil || string(size) != "40x120" {
		t.Fatalf("usable native screen: %q %v", size, err)
	}
	if _, err := os.Stat(filepath.Join(sessDir, "allowed-once")); !os.IsNotExist(err) {
		t.Fatal("tool ran without operator choice")
	}
	// Goal controls keep refusing a pending permission; they must not implicitly
	// approve it. Operator input is a separately explicit normal command.
	if err := requestGoalControl(root, cfg, wf, goalControlPause, home); err == nil {
		t.Fatal("pause unexpectedly answered pending permission")
	}
	observe := func() string {
		out, err := exec.Command(cli, "workflow", "goal-input", wf.ID, "-root", root, "-observe").Output()
		if err != nil {
			t.Fatalf("observe actual pending prompt: %v %s", err, out)
		}
		var view struct {
			Prompt        string `json:"prompt"`
			Command       string `json:"command"`
			InputProtocol int    `json:"input_protocol"`
		}
		if err := json.Unmarshal(out, &view); err != nil || view.Prompt == "" || view.InputProtocol != hostedInputProtocol || !strings.Contains(view.Command, view.Prompt) {
			t.Fatalf("copyable pending identity: %+v %v %s", view, err, out)
		}
		return view.Prompt
	}
	prompt := observe()
	cliInput := func(attempt, session, key string) ([]byte, error) {
		return exec.Command(cli, "workflow", "goal-input", wf.ID, "-root", root, "-attempt", attempt, "-session", session, "-prompt", prompt, "-key", key).CombinedOutput()
	}
	// An installed new CLI cannot manufacture an input capability for an
	// already-running old supervisor. No key may reach that original child.
	statusPath := goalHostStatusPath(root, current.ID, current.ActiveAttemptID)
	statusBytes, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	var oldHost goalHostStatus
	if err := json.Unmarshal(statusBytes, &oldHost); err != nil {
		t.Fatal(err)
	}
	oldHost.InputProtocol = 0
	oldBytes, _ := json.Marshal(oldHost)
	if err := os.WriteFile(statusPath, oldBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := cliInput(current.ActiveAttemptID, current.SessionID, "1"); err == nil || !strings.Contains(string(out), "does not upgrade an existing host") {
		t.Fatalf("old supervisor must refuse without input: %v %s", err, out)
	}
	if err := os.WriteFile(statusPath, statusBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Task){
		func(v *Task) { v.Status = statusHeld },
		func(v *Task) { v.Goal.Observation = goalObsUnknown },
		func(v *Task) { v.Goal.External = true },
		func(v *Task) { v.Goal.CancelRequested = true },
		func(v *Task) { v.Goal.HardTimeoutSec = 0 },
	} {
		probe := *current
		binding := *current.Goal
		probe.Goal = &binding
		change(&probe)
		req := hostedInputRequest{Kind: hostedInputKind, ID: "negative", TaskID: current.ID, AttemptID: current.ActiveAttemptID, SessionID: current.SessionID, Key: "1", Prompt: prompt}
		if err := validateHostedOperatorInput(root, &probe, req); err == nil {
			t.Fatal("invalid owner/deadline accepted")
		}
	}
	for _, bad := range []struct{ attempt, session, key string }{
		{"stale-attempt", current.SessionID, "1"},
		{current.ActiveAttemptID, "other-session", "1"},
		{current.ActiveAttemptID, current.SessionID, "ctrl-o"},
		{current.ActiveAttemptID, current.SessionID, "/always-approve"},
	} {
		if out, err := cliInput(bad.attempt, bad.session, bad.key); err == nil {
			t.Fatalf("invalid input accepted: %+v %s", bad, out)
		}
	}
	if _, err := os.Stat(filepath.Join(sessDir, "allowed-once")); !os.IsNotExist(err) {
		t.Fatal("invalid input reached original PTY")
	}
	// Resolve the observed request before delivery, then introduce another
	// request with the same tool name. Neither is the observed original prompt.
	eventsPath := grokNativeEventsPath(home, dir, current.SessionID)
	appendEvents := func(lines string) {
		f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(lines)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	appendEvents("{\"type\":\"permission_resolved\"}\n{\"type\":\"phase_changed\",\"phase\":\"Executing\"}\n")
	if out, err := cliInput(current.ActiveAttemptID, current.SessionID, "1"); err == nil || !strings.Contains(string(out), "prompt changed or missing") {
		t.Fatalf("resolved prompt choice accepted: %v %s", err, out)
	}
	appendEvents("{\"type\":\"phase_changed\",\"phase\":\"permission_prompt\"}\n{\"type\":\"permission_requested\",\"tool_name\":\"fake-local-compile\"}\n")
	if out, err := cliInput(current.ActiveAttemptID, current.SessionID, "1"); err == nil || !strings.Contains(string(out), "prompt changed or missing") {
		t.Fatalf("old choice accepted by a later prompt: %v %s", err, out)
	}
	// The original owner repeats the fingerprint check at the write site,
	// covering a change after the CLI has already enqueued its request.
	stale := hostedInputRequest{Kind: hostedInputKind, ID: "stale-owner-delivery", TaskID: current.ID, AttemptID: current.ActiveAttemptID, SessionID: current.SessionID, Key: "1", Prompt: prompt}
	handleHostedOperatorInput(root, current.ID, current.ActiveAttemptID, current.SessionID, nil, stale)
	st, err := loadGoalHostStatus(root, current.ID, current.ActiveAttemptID)
	if err != nil || st.LastInputID != stale.ID || !strings.Contains(st.InputError, "prompt changed or missing") {
		t.Fatalf("owner did not recheck observed prompt before PTY write: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(sessDir, "allowed-once")); !os.IsNotExist(err) {
		t.Fatal("stale prompt input reached original fake tool")
	}
	newPrompt := observe()
	if newPrompt == prompt {
		t.Fatal("later identical tool reused prompt identity")
	}
	prompt = newPrompt
	out, err := cliInput(current.ActiveAttemptID, current.SessionID, "1")
	if err != nil || !strings.Contains(string(out), "not proof of native approval") {
		t.Fatalf("real input CLI: %v %s", err, out)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(sessDir, "allowed-once")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(sessDir, "allowed-once")); err != nil || string(b) != "explicit operator key 1" {
		t.Fatalf("fake tool response: %q %v", b, err)
	}
	afterInput, err := loadTask(root, current.ID)
	if err != nil || afterInput.ActiveAttemptID != current.ActiveAttemptID || afterInput.SessionID != current.SessionID ||
		afterInput.Goal.HardTimeoutSec != current.Goal.HardTimeoutSec || afterInput.Goal.BudgetTokens != current.Goal.BudgetTokens {
		t.Fatal("operator input changed original custody or budget/deadline")
	}
	if err := requestGoalControl(root, cfg, wf, goalControlPause, home); err != nil {
		t.Fatalf("pause after explicit response: %v", err)
	}
	if err := requestGoalControl(root, cfg, wf, goalControlResume, home); err != nil {
		t.Fatalf("resume compatibility: %v", err)
	}
	if err := requestGoalControl(root, cfg, wf, goalControlStop, home); err != nil {
		t.Fatalf("stop compatibility: %v", err)
	}
	select {
	case err := <-runDone:
		joined = true
		if err != nil {
			t.Fatalf("hosted closeout: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("fake hosted did not exit")
	}
	_ = writeOutput.Close()
	output := <-outputDone
	_ = readOutput.Close()
	if !strings.Contains(output, "PENDING fake-local-compile: 1 Allow once; 2 Deny") || !strings.Contains(output, "LOCAL TOOL ALLOWED ONCE") {
		t.Fatalf("normal output relay omitted actual prompt/response: %q", output)
	}
	t.Log("real CLI -> original control FIFO -> same fake PTY -> Allow once echo; full prompt and 40x120 screen observed; pause/resume/stop and clean exit pass")
}

func TestHostedOperatorKeysAreExplicit(t *testing.T) {
	for _, key := range []string{"1", "9", "enter", "escape", "tab", "up", "down", "ctrl-f"} {
		if _, err := hostedOperatorKey(key); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"", "0", "10", "ctrl-o", "always-approve", "/goal resume", "1\n"} {
		if _, err := hostedOperatorKey(key); err == nil {
			t.Fatalf("accepted %q", key)
		}
	}
}
