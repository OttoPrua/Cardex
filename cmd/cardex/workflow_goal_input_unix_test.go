//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	for _, key := range []string{"", "0", "10", "ctrl-o", "ctrl-enter", "always-approve", "/goal resume", "1\n"} {
		if _, err := hostedOperatorKey(key); err == nil {
			t.Fatalf("accepted %q", key)
		}
	}
}

func TestHostedOldProtocol2HostRefusesSuccessfulDeliveryOperation(t *testing.T) {
	root := testRoot(t)
	dir := t.TempDir()
	cfg := testCfg()
	tk := newTask(root, cfg, typeSequence, "old protocol2 host", dir, []string{"p"}, 5)
	tk.Status = statusRunning
	tk.SessionID = "sess-old-p2"
	tk.FrozenRoute = &frozenOwnerRoute{Name: "pinned", Legs: []frozenPolicyLeg{{
		Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "xhigh", Stage: "primary",
	}}}
	beforePin, err := json.Marshal(tk.FrozenRoute)
	if err != nil {
		t.Fatal(err)
	}
	tk.Goal = &TaskGoalBinding{
		Hosted: true, ControlOwner: goalControlOwnerHosted, Observation: goalObsRunning,
		HardTimeoutSec: 3600, WriterMode: goalWriterManual,
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	plantLiveAttempt(t, root, tk)
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Goal.BoundAttemptID = fresh.ActiveAttemptID
	if err := saveTask(root, fresh); err != nil {
		t.Fatal(err)
	}
	if err := writeGoalHostStatus(root, fresh.ID, fresh.ActiveAttemptID, goalHostStatus{
		SessionID: fresh.SessionID, SupervisorAlive: true, InputProtocol: 2,
		LastInputID: "old-protocol2-ack",
	}); err != nil {
		t.Fatal(err)
	}
	fifo := goalHostControlPath(root, fresh.ID, fresh.ActiveAttemptID)
	if err := ensureHostControlFIFO(fifo); err != nil {
		t.Fatal(err)
	}
	drain, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer drain.Close()

	err = requestHostedOperatorInput(root, fresh.ID, fresh.ActiveAttemptID, fresh.SessionID, "1", "fp")
	if err == nil || !strings.Contains(err.Error(), "does not upgrade an existing host") {
		t.Fatalf("old protocol2 host must refuse current send: %v", err)
	}

	if err := syscall.SetNonblock(int(drain.Fd()), true); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, _ := drain.Read(buf)
	if n > 0 {
		t.Fatalf("old protocol2 host received FIFO bytes: %q", buf[:n])
	}

	st, err := loadGoalHostStatus(root, fresh.ID, fresh.ActiveAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if st.InputProtocol != 2 || st.LastInputKey != "" || st.LastInputPrompt != "" {
		t.Fatalf("old host status mutated: %+v", st)
	}
	if err := writeGoalHostStatus(root, fresh.ID, fresh.ActiveAttemptID, goalHostStatus{
		SessionID: fresh.SessionID, SupervisorAlive: true, InputProtocol: hostedInputProtocol,
	}); err != nil {
		t.Fatal(err)
	}
	afterWrite, err := loadGoalHostStatus(root, fresh.ID, fresh.ActiveAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if afterWrite.InputProtocol != 2 {
		t.Fatalf("new CLI advertised current protocol onto old running host: %+v", afterWrite)
	}

	afterTask, err := loadTask(root, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterPin, err := json.Marshal(afterTask.FrozenRoute)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforePin) != string(afterPin) {
		t.Fatalf("old FrozenRoute mutated before=%s after=%s", beforePin, afterPin)
	}
}

func TestHostedRejectedInterveningRequestKeepsSuccessfulDedup(t *testing.T) {
	root := testRoot(t)
	taskID, attemptID, sessionID := "t-dedup", "att-1", "sess-1"
	if err := writeGoalHostStatus(root, taskID, attemptID, goalHostStatus{
		TaskID: taskID, AttemptID: attemptID, SessionID: sessionID,
		SupervisorAlive: true, InputProtocol: hostedInputProtocol,
		LastInputID: "success-id", LastInputKey: "1", LastInputPrompt: "fp",
	}); err != nil {
		t.Fatal(err)
	}
	handleHostedOperatorInput(root, taskID, attemptID, sessionID, nil, hostedInputRequest{
		Kind: hostedInputKind, ID: "reject-id", TaskID: taskID, AttemptID: attemptID,
		SessionID: sessionID, Key: "2", Prompt: "fp",
	})
	got, err := loadGoalHostStatus(root, taskID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastInputID != "reject-id" || got.InputError == "" {
		t.Fatalf("rejected request must ACK with InputError: %+v", got)
	}
	if got.LastInputKey != "1" || got.LastInputPrompt != "fp" {
		t.Fatalf("successful key/prompt cleared on rejection: %+v", got)
	}
	if !hostedSameInputAlreadyDelivered(got, "1", "fp") {
		t.Fatal("rejected intervening request allowed same-prompt resend")
	}
	if hostedSameInputAlreadyDelivered(got, "2", "fp") {
		t.Fatal("rejected key 2 must not count as delivered")
	}
}

type hostedObserveView struct {
	Task          string `json:"task"`
	Root          string `json:"root"`
	Workflow      string `json:"workflow"`
	Attempt       string `json:"attempt"`
	Session       string `json:"session"`
	Prompt        string `json:"prompt"`
	InputProtocol int    `json:"input_protocol"`
	InputCapable  bool   `json:"input_capable"`
	Command       string `json:"command"`
}

type hostedFIFOWatch struct {
	mu  sync.Mutex
	got []byte
}

func (w *hostedFIFOWatch) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.got...)
}

func (w *hostedFIFOWatch) add(p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.got = append(w.got, p...)
}

type hostedPlantedMember struct {
	task   *Task
	watch  *hostedFIFOWatch
	prompt string
	fifo   *os.File
	label  string
}

func hostedGoalInputTestCLI(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("CARDEX_CANDIDATE_BIN")); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("CARDEX_CANDIDATE_BIN=%s: %v", p, err)
		}
		return p
	}
	cli := filepath.Join(t.TempDir(), "cardex")
	build := exec.Command("go", "build", "-o", cli, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real CLI: %v %s", err, out)
	}
	return cli
}

func runHostedGoalInputCLI(t *testing.T, cli string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(cli, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("%v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), code
}

func items6GreenOutDir(t *testing.T) string {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("ITEMS6_GREEN_OUT"))
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func items6GreenFile(t *testing.T, name, body string) {
	t.Helper()
	dir := items6GreenOutDir(t)
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func items6GreenLog(t *testing.T, name, command, stdout, stderr string, code int) {
	t.Helper()
	if items6GreenOutDir(t) == "" {
		return
	}
	items6GreenFile(t, name+".cmd", command+"\n")
	items6GreenFile(t, name+".stdout", stdout)
	items6GreenFile(t, name+".stderr", stderr)
	items6GreenFile(t, name+".exit", fmt.Sprintf("%d\n", code))
}

func items6GreenBytes(t *testing.T, name string, writer, nativeB *hostedPlantedMember) {
	t.Helper()
	items6GreenFile(t, name, fmt.Sprintf("A=%d B=%d A_task=%s B_task=%s\n",
		len(writer.watch.snapshot()), len(nativeB.watch.snapshot()), writer.task.ID, nativeB.task.ID))
}

func startHostedFIFOAck(t *testing.T, root string, m *hostedPlantedMember) {
	t.Helper()
	go func() {
		scanner := bufio.NewScanner(m.fifo)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			m.watch.add(append(line, '\n'))
			var req hostedInputRequest
			if json.Unmarshal(line, &req) != nil || req.ID == "" {
				continue
			}
			st, err := loadGoalHostStatus(root, m.task.ID, attemptIDForHostedTask(m.task))
			if err != nil {
				continue
			}
			st.SessionID = m.task.SessionID
			st.SupervisorAlive = true
			st.LastInputID = req.ID
			st.LastInputKey = req.Key
			st.LastInputPrompt = req.Prompt
			st.InputError = ""
			_ = writeGoalHostStatus(root, m.task.ID, attemptIDForHostedTask(m.task), st)
		}
	}()
}

func plantHostedOperatorMember(t *testing.T, root string, cfg *Config, wfID, workDir, label, session, tool string, protocol int) *hostedPlantedMember {
	t.Helper()
	dir := filepath.Join(workDir, "member-"+label)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	tk := newTask(root, cfg, typeSequence, "hosted "+label, dir, []string{"p"}, 5)
	tk.Status = statusRunning
	tk.SessionID = session
	tk.WorkflowID = wfID
	tk.Goal = &TaskGoalBinding{
		Hosted: true, ControlOwner: goalControlOwnerHosted, Observation: goalObsRunning,
		HardTimeoutSec: 3600, WriterMode: goalWriterManual, GrokHome: home,
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	plantLiveAttempt(t, root, tk)
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Goal.BoundAttemptID = fresh.ActiveAttemptID
	if err := saveTask(root, fresh); err != nil {
		t.Fatal(err)
	}
	if protocol == 0 {
		protocol = hostedInputProtocol
	}
	if err := writeGoalHostStatus(root, fresh.ID, fresh.ActiveAttemptID, goalHostStatus{
		TaskID: fresh.ID, AttemptID: fresh.ActiveAttemptID, SessionID: fresh.SessionID,
		SupervisorAlive: true, InputProtocol: protocol,
	}); err != nil {
		t.Fatal(err)
	}
	fifoPath := goalHostControlPath(root, fresh.ID, fresh.ActiveAttemptID)
	if err := ensureHostControlFIFO(fifoPath); err != nil {
		t.Fatal(err)
	}
	fifo, err := os.OpenFile(fifoPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fifo.Close() })
	events := grokNativeEventsPath(home, dir, session)
	if err := os.MkdirAll(filepath.Dir(events), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"phase_changed\",\"phase\":\"permission_prompt\"}\n{\"type\":\"permission_requested\",\"tool_name\":\"" + tool + "\"}\n"
	if err := os.WriteFile(events, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fp, err := hostedPendingPermissionFingerprint(home, dir, session)
	if err != nil {
		t.Fatal(err)
	}
	m := &hostedPlantedMember{task: fresh, watch: &hostedFIFOWatch{}, prompt: fp, fifo: fifo, label: label}
	startHostedFIFOAck(t, root, m)
	return m
}

func waitHostedFIFOBytes(t *testing.T, w *hostedFIFOWatch, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b := w.snapshot(); len(b) > 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("FIFO received no bytes")
	return nil
}

func parseHostedObserve(t *testing.T, stdout string) hostedObserveView {
	t.Helper()
	var view hostedObserveView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("observe JSON: %v %s", err, stdout)
	}
	return view
}

func execCopiedGoalInput(t *testing.T, cli, command, key string) (argv []string, stdout, stderr string, code int) {
	t.Helper()
	fields := strings.Fields(command)
	if len(fields) < 3 {
		t.Fatalf("copyable command too short: %q", command)
	}
	fields[0] = cli
	replaced := false
	for i, f := range fields {
		if f == "KEY" {
			fields[i] = key
			replaced = true
		}
	}
	if !replaced {
		t.Fatalf("copyable command missing KEY: %q", command)
	}
	stdout, stderr, code = runHostedGoalInputCLI(t, fields[0], fields[1:]...)
	return fields, stdout, stderr, code
}

func assertFIFOUnchanged(t *testing.T, label string, w *hostedFIFOWatch, before []byte) {
	t.Helper()
	time.Sleep(40 * time.Millisecond)
	after := w.snapshot()
	if !bytes.Equal(after, before) {
		t.Fatalf("%s FIFO gained bytes before=%q after=%q", label, before, after)
	}
}

func TestHostedExactTaskSelectorCLIBindsSameMember(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	cli := hostedGoalInputTestCLI(t)

	writer := plantHostedOperatorMember(t, root, cfg, wf.ID, dir, "writer-a", "sess-writer-a", "fake-compile-a", hostedInputProtocol)
	nativeB := plantHostedOperatorMember(t, root, cfg, wf.ID, dir, "native-b", "sess-native-b", "fake-compile-b", hostedInputProtocol)
	terminal := plantHostedOperatorMember(t, root, cfg, wf.ID, dir, "terminal", "sess-terminal", "fake-compile-t", hostedInputProtocol)
	expired := plantHostedOperatorMember(t, root, cfg, wf.ID, dir, "expired", "sess-expired", "fake-compile-e", hostedInputProtocol)
	oldhost := plantHostedOperatorMember(t, root, cfg, wf.ID, dir, "oldhost", "sess-oldhost", "fake-compile-o", 2)
	nonmember := plantHostedOperatorMember(t, root, cfg, wf.ID, dir, "nonmember", "sess-nonmember", "fake-compile-n", hostedInputProtocol)
	foreign := plantHostedOperatorMember(t, root, cfg, "wf-foreign-aaaa", dir, "foreign", "sess-foreign", "fake-compile-f", hostedInputProtocol)

	termTask, err := loadTask(root, terminal.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	termTask.Status = statusCanceled
	termTask.Goal.Observation = goalObsCanceled
	if err := saveTask(root, termTask); err != nil {
		t.Fatal(err)
	}
	terminal.task = termTask

	expTask, err := loadTask(root, expired.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := loadAttempt(root, expTask.ID, expTask.ActiveAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	rec.CreatedAt = time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	expTask.Goal.HardTimeoutSec = 1
	if err := saveTask(root, expTask); err != nil {
		t.Fatal(err)
	}
	expired.task = expTask

	wf.WriterTaskID = writer.task.ID
	wf.NativeGoalTaskIDs = []string{nativeB.task.ID, terminal.task.ID, expired.task.ID, oldhost.task.ID}
	if err := saveWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.WriterTaskID != writer.task.ID {
		t.Fatalf("writer rewritten at plant: %s", wf.WriterTaskID)
	}

	watches := []*hostedPlantedMember{writer, nativeB, terminal, expired, oldhost, nonmember, foreign}
	snapshotAll := func() map[string][]byte {
		out := map[string][]byte{}
		for _, m := range watches {
			out[m.label] = m.watch.snapshot()
		}
		return out
	}
	assertAllUnchanged := func(t *testing.T, before map[string][]byte) {
		t.Helper()
		time.Sleep(40 * time.Millisecond)
		for _, m := range watches {
			if !bytes.Equal(m.watch.snapshot(), before[m.label]) {
				t.Fatalf("%s FIFO changed during refuse: before=%q after=%q", m.label, before[m.label], m.watch.snapshot())
			}
		}
	}

	observeBOut, observeBErr, observeBCode := runHostedGoalInputCLI(t, cli, "workflow", "goal-input", wf.ID, "-root", root, "-task", nativeB.task.ID, "-observe")
	items6GreenLog(t, "observe-b", strings.Join([]string{cli, "workflow", "goal-input", wf.ID, "-root", root, "-task", nativeB.task.ID, "-observe"}, " "), observeBOut, observeBErr, observeBCode)
	if observeBCode != 0 {
		t.Fatalf("observe -task B: exit %d stdout=%s stderr=%s", observeBCode, observeBOut, observeBErr)
	}
	viewB := parseHostedObserve(t, observeBOut)
	if viewB.Task != nativeB.task.ID || viewB.Workflow != wf.ID || viewB.Root != root {
		t.Fatalf("observe B identity: %+v", viewB)
	}
	if viewB.Attempt != nativeB.task.ActiveAttemptID || viewB.Session != nativeB.task.SessionID || viewB.Prompt != nativeB.prompt {
		t.Fatalf("observe B current identity: %+v want attempt=%s session=%s prompt=%s", viewB, nativeB.task.ActiveAttemptID, nativeB.task.SessionID, nativeB.prompt)
	}
	if viewB.InputProtocol != hostedInputProtocol || !viewB.InputCapable {
		t.Fatalf("observe B protocol: %+v", viewB)
	}
	if !strings.Contains(viewB.Command, "-task "+nativeB.task.ID) || !strings.Contains(viewB.Command, viewB.Prompt) {
		t.Fatalf("copyable B command missing selected identity: %s", viewB.Command)
	}
	if strings.Contains(viewB.Command, writer.task.ID) || strings.Contains(viewB.Command, writer.task.ActiveAttemptID) || strings.Contains(viewB.Command, writer.task.SessionID) {
		t.Fatalf("selecting B silently named A: %s", viewB.Command)
	}
	if viewB.Task == writer.task.ID || viewB.Attempt == writer.task.ActiveAttemptID || viewB.Session == writer.task.SessionID || viewB.Prompt == writer.prompt {
		t.Fatalf("selecting B returned A observe JSON: %+v", viewB)
	}

	beforeBSend := snapshotAll()
	sendBArgv, sendBOut, sendBErr, sendBCode := execCopiedGoalInput(t, cli, viewB.Command, "1")
	items6GreenLog(t, "send-b-copied", strings.Join(sendBArgv, " "), sendBOut, sendBErr, sendBCode)
	if sendBCode != 0 || !strings.Contains(sendBOut, "not proof of native approval") || !strings.Contains(sendBOut, "task="+nativeB.task.ID) {
		t.Fatalf("copied B send: exit %d stdout=%s stderr=%s", sendBCode, sendBOut, sendBErr)
	}
	if strings.Contains(sendBOut, "task="+writer.task.ID) {
		t.Fatalf("copied B send acknowledged A: %s", sendBOut)
	}
	bBytes := waitHostedFIFOBytes(t, nativeB.watch, 3*time.Second)
	var bReq hostedInputRequest
	if err := json.Unmarshal(bytes.TrimSpace(bBytes), &bReq); err != nil {
		t.Fatalf("B FIFO JSON: %v %q", err, bBytes)
	}
	if bReq.TaskID != nativeB.task.ID || bReq.AttemptID != nativeB.task.ActiveAttemptID || bReq.SessionID != nativeB.task.SessionID || bReq.Key != "1" || bReq.Prompt != nativeB.prompt || bReq.ID == "" {
		t.Fatalf("B FIFO request %+v", bReq)
	}
	items6GreenFile(t, "send-b-copied.fifo.json", string(bytes.TrimSpace(bBytes))+"\n")
	items6GreenFile(t, "send-b-copied.request-id", bReq.ID+"\n")
	items6GreenBytes(t, "bytes-after-b-send.txt", writer, nativeB)
	assertFIFOUnchanged(t, "writer-a after B send", writer.watch, beforeBSend["writer-a"])
	for _, m := range []*hostedPlantedMember{terminal, expired, oldhost, nonmember, foreign} {
		assertFIFOUnchanged(t, m.label+" after B send", m.watch, beforeBSend[m.label])
	}

	observeDefOut, observeDefErr, observeDefCode := runHostedGoalInputCLI(t, cli, "workflow", "goal-input", wf.ID, "-root", root, "-observe")
	items6GreenLog(t, "observe-default", strings.Join([]string{cli, "workflow", "goal-input", wf.ID, "-root", root, "-observe"}, " "), observeDefOut, observeDefErr, observeDefCode)
	if observeDefCode != 0 {
		t.Fatalf("default observe: exit %d stdout=%s stderr=%s", observeDefCode, observeDefOut, observeDefErr)
	}
	viewA := parseHostedObserve(t, observeDefOut)
	if viewA.Task != writer.task.ID || viewA.Attempt != writer.task.ActiveAttemptID || viewA.Session != writer.task.SessionID || viewA.Prompt != writer.prompt {
		t.Fatalf("default observe left writer A: %+v", viewA)
	}
	if strings.Contains(viewA.Command, "-task") || strings.Contains(viewA.Command, nativeB.task.ID) {
		t.Fatalf("default copyable command selected a sibling: %s", viewA.Command)
	}

	beforeDefaultSend := snapshotAll()
	sendAArgv, sendAOut, sendAErr, sendACode := execCopiedGoalInput(t, cli, viewA.Command, "1")
	items6GreenLog(t, "send-default-copied", strings.Join(sendAArgv, " "), sendAOut, sendAErr, sendACode)
	if sendACode != 0 || !strings.Contains(sendAOut, "not proof of native approval") || !strings.Contains(sendAOut, "task="+writer.task.ID) {
		t.Fatalf("default copied send: exit %d stdout=%s stderr=%s", sendACode, sendAOut, sendAErr)
	}
	aBytes := waitHostedFIFOBytes(t, writer.watch, 3*time.Second)
	var aReq hostedInputRequest
	if err := json.Unmarshal(bytes.TrimSpace(aBytes), &aReq); err != nil {
		t.Fatalf("A FIFO JSON: %v %q", err, aBytes)
	}
	if aReq.TaskID != writer.task.ID || aReq.Key != "1" || aReq.Prompt != writer.prompt || aReq.ID == "" {
		t.Fatalf("A FIFO request %+v", aReq)
	}
	items6GreenFile(t, "send-default-copied.fifo.json", string(bytes.TrimSpace(aBytes))+"\n")
	items6GreenFile(t, "send-default-copied.request-id", aReq.ID+"\n")
	items6GreenBytes(t, "bytes-after-default-send.txt", writer, nativeB)
	assertFIFOUnchanged(t, "native-b after default send", nativeB.watch, beforeDefaultSend["native-b"])

	refuse := func(name string, args []string) {
		t.Helper()
		before := snapshotAll()
		stdout, stderr, code := runHostedGoalInputCLI(t, cli, args...)
		items6GreenLog(t, name, strings.Join(append([]string{cli}, args...), " "), stdout, stderr, code)
		if code == 0 {
			t.Fatalf("%s succeeded: %s %s", name, stdout, stderr)
		}
		assertAllUnchanged(t, before)
		items6GreenBytes(t, name+".bytes", writer, nativeB)
	}

	refuse("refuse-cross-a-identity-on-b", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", nativeB.task.ID, "-attempt", writer.task.ActiveAttemptID, "-session", writer.task.SessionID, "-prompt", nativeB.prompt, "-key", "1"})
	refuse("refuse-cross-b-identity-on-a", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", writer.task.ID, "-attempt", nativeB.task.ActiveAttemptID, "-session", nativeB.task.SessionID, "-prompt", writer.prompt, "-key", "1"})
	refuse("refuse-foreign", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", foreign.task.ID, "-attempt", foreign.task.ActiveAttemptID, "-session", foreign.task.SessionID, "-prompt", foreign.prompt, "-key", "1"})
	refuse("refuse-nonmember", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", nonmember.task.ID, "-attempt", nonmember.task.ActiveAttemptID, "-session", nonmember.task.SessionID, "-prompt", nonmember.prompt, "-key", "1"})
	refuse("refuse-unknown", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", "task-does-not-exist", "-attempt", nativeB.task.ActiveAttemptID, "-session", nativeB.task.SessionID, "-prompt", nativeB.prompt, "-key", "1"})
	refuse("refuse-terminal", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", terminal.task.ID, "-attempt", terminal.task.ActiveAttemptID, "-session", terminal.task.SessionID, "-prompt", terminal.prompt, "-key", "1"})
	refuse("refuse-expired", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", expired.task.ID, "-attempt", expired.task.ActiveAttemptID, "-session", expired.task.SessionID, "-prompt", expired.prompt, "-key", "1"})
	refuse("refuse-oldhost", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", oldhost.task.ID, "-attempt", oldhost.task.ActiveAttemptID, "-session", oldhost.task.SessionID, "-prompt", oldhost.prompt, "-key", "1"})
	refuse("refuse-wrong-attempt", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", nativeB.task.ID, "-attempt", "stale-attempt", "-session", nativeB.task.SessionID, "-prompt", nativeB.prompt, "-key", "1"})
	refuse("refuse-wrong-session", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", nativeB.task.ID, "-attempt", nativeB.task.ActiveAttemptID, "-session", "other-session", "-prompt", nativeB.prompt, "-key", "1"})
	refuse("refuse-duplicate-b", []string{"workflow", "goal-input", wf.ID, "-root", root, "-task", nativeB.task.ID, "-attempt", nativeB.task.ActiveAttemptID, "-session", nativeB.task.SessionID, "-prompt", nativeB.prompt, "-key", "1"})
	items6GreenBytes(t, "bytes-after-refusals.txt", writer, nativeB)

	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.WriterTaskID != writer.task.ID {
		t.Fatalf("selector rewrote WriterTaskID to %s", disk.WriterTaskID)
	}
}
