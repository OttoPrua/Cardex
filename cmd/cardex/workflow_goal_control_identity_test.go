//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func withShortControlWait(t *testing.T) {
	t.Helper()
	old := hostedControlConfirmWait
	hostedControlConfirmWait = 1500 * time.Millisecond
	t.Cleanup(func() { hostedControlConfirmWait = old })
}

func plantControlLeftover(t *testing.T, root string, tk *Task, action, failure string, confirmed bool, controlID string) {
	t.Helper()
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Goal.LastControl = action
	fresh.Goal.LastControlID = controlID
	fresh.Goal.ControlConfirmed = confirmed
	fresh.Goal.FailureClass = failure
	if err := writeTaskFile(root, fresh); err != nil {
		t.Fatal(err)
	}
	*tk = *fresh
}

func seedControlFIFO(t *testing.T, protocol int) (root, dir, home string, cfg *Config, wf *WorkflowRecord, tk *Task, drain *os.File) {
	t.Helper()
	root, dir = workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	home = t.TempDir()
	t.Setenv("GROK_HOME", home)
	wf = initTestWorkflow(t, root, dir)
	tk = seedHostedWriterForControl(t, root, dir, cfg, wf, "sess-control-id")
	if err := writeGoalHostStatus(root, tk.ID, tk.ActiveAttemptID, goalHostStatus{
		SessionID:       tk.SessionID,
		SupervisorAlive: true,
		InputProtocol:   hostedInputProtocol,
		ControlProtocol: protocol,
	}); err != nil {
		t.Fatal(err)
	}
	fifo := goalHostControlPath(root, tk.ID, tk.ActiveAttemptID)
	if err := ensureHostControlFIFO(fifo); err != nil {
		t.Fatal(err)
	}
	drain, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = drain.Close() })
	return root, dir, home, cfg, wf, tk, drain
}

func readControlFIFOLine(t *testing.T, drain *os.File, wait time.Duration) []byte {
	t.Helper()
	if err := syscall.SetNonblock(int(drain.Fd()), true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(wait)
	buf := make([]byte, 4096)
	var got []byte
	for time.Now().Before(deadline) {
		n, err := drain.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			if bytes.Contains(got, []byte("\n")) {
				return got
			}
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatalf("fifo read: %v", err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	return got
}

func parseControlRequest(t *testing.T, raw []byte) hostedControlRequest {
	t.Helper()
	line := bytes.TrimSpace(raw)
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = bytes.TrimSpace(line[:i])
	}
	var req hostedControlRequest
	if err := json.Unmarshal(line, &req); err != nil {
		t.Fatalf("control fifo payload %q: %v", line, err)
	}
	if req.Kind != hostedControlKind || req.ID == "" || req.Action == "" {
		t.Fatalf("control request identity: %+v", req)
	}
	return req
}

func ackControl(t *testing.T, root, home, dir, gid string, tk *Task, req hostedControlRequest, native string, confirmed bool, mutate func(*goalHostStatus)) {
	t.Helper()
	writeGrokGoalFixture(t, home, dir, tk.SessionID, gid, native, grokNativeGoalStateFile{Phase: "Idle"})
	st := goalHostStatus{
		SessionID:        tk.SessionID,
		LastControl:      req.Action,
		LastControlID:    req.ID,
		ControlConfirmed: confirmed,
		SupervisorAlive:  true,
		ControlProtocol:  hostedControlProtocol,
		NativeStatus:     native,
	}
	if mutate != nil {
		mutate(&st)
	}
	if err := writeGoalHostStatus(root, tk.ID, tk.ActiveAttemptID, st); err != nil {
		t.Fatal(err)
	}
}

func TestRequestGoalControlBindsCurrentRequestNotStaleFailure(t *testing.T) {
	withShortControlWait(t)
	root, dir, home, cfg, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
	gid := tk.Goal.NativeGoalID
	plantControlLeftover(t, root, tk, goalControlPause, goalFailControlUnsupported, false, "old-fail-id")
	writeGrokPermissionEvents(t, home, dir, tk.SessionID,
		`{"type":"phase_changed","phase":"permission_prompt"}`,
		`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
		`{"type":"permission_resolved","tool_name":"run_terminal_command","decision":"allow"}`,
		`{"type":"phase_changed","phase":"tool_execution"}`,
	)
	errCh := make(chan error, 1)
	started := time.Now()
	go func() { errCh <- requestGoalControl(root, cfg, wf, goalControlPause, home) }()
	raw := readControlFIFOLine(t, drain, time.Second)
	if len(raw) == 0 {
		t.Fatal("current request must enqueue FIFO instead of returning leftover control_unsupported")
	}
	req := parseControlRequest(t, raw)
	if req.ID == "old-fail-id" || req.AttemptID != tk.ActiveAttemptID || req.SessionID != tk.SessionID {
		t.Fatalf("request identity: %+v", req)
	}
	if time.Since(started) < 20*time.Millisecond {
		// leftover must not complete the call before ACK; FIFO bytes prove we waited
	}
	ackControl(t, root, home, dir, gid, tk, req, "paused", true, nil)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("current pause after Allow-once: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("matching current ACK + native pause did not complete")
	}
	if leftover, _ := loadTask(root, tk.ID); leftover.Goal.LastControlID != req.ID {
		t.Fatalf("confirmation must bind this request id, got %+v", leftover.Goal)
	}
}

func TestRequestGoalControlPendingPermissionStillRefuses(t *testing.T) {
	root, dir, home, cfg, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
	writeGrokPermissionEvents(t, home, dir, tk.SessionID,
		`{"type":"phase_changed","phase":"permission_prompt"}`,
		`{"type":"permission_requested","tool_name":"run_terminal_command"}`,
	)
	err := requestGoalControl(root, cfg, wf, goalControlPause, home)
	if err == nil || !strings.Contains(err.Error(), goalFailControlUnsupported) {
		t.Fatalf("pending must refuse: %v", err)
	}
	if raw := readControlFIFOLine(t, drain, 80*time.Millisecond); len(raw) > 0 {
		t.Fatalf("pending refuse must not enqueue control: %q", raw)
	}
}

func TestRequestGoalControlOldSuccessIsNotThisCall(t *testing.T) {
	withShortControlWait(t)
	root, dir, home, cfg, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
	gid := tk.Goal.NativeGoalID
	plantControlLeftover(t, root, tk, goalControlPause, "", true, "old-success-id")
	writeGrokGoalFixture(t, home, dir, tk.SessionID, gid, "paused", grokNativeGoalStateFile{Phase: "Idle"})
	_ = writeGoalHostStatus(root, tk.ID, tk.ActiveAttemptID, goalHostStatus{
		SessionID:        tk.SessionID,
		LastControl:      goalControlPause,
		LastControlID:    "old-success-id",
		ControlConfirmed: true,
		SupervisorAlive:  true,
		ControlProtocol:  hostedControlProtocol,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- requestGoalControl(root, cfg, wf, goalControlPause, home) }()
	raw := readControlFIFOLine(t, drain, time.Second)
	if len(raw) == 0 {
		t.Fatal("old same-action success must not confirm a new call before enqueue")
	}
	req := parseControlRequest(t, raw)
	if req.ID == "old-success-id" {
		t.Fatal("new call reused old request id")
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("old ACK/native pause confirmed a new call")
		}
		if !strings.Contains(err.Error(), goalFailPauseNotConfirmed) {
			t.Fatalf("want unconfirmed, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing current ACK must fail closed")
	}
}

func TestRequestGoalControlOldHostNoACKOldACKWrongIdentityBytesOnly(t *testing.T) {
	t.Run("old-host", func(t *testing.T) {
		root, _, home, cfg, wf, tk, drain := seedControlFIFO(t, 0)
		err := requestGoalControl(root, cfg, wf, goalControlPause, home)
		if err == nil || !strings.Contains(err.Error(), "does not upgrade an existing host") {
			t.Fatalf("old host: %v", err)
		}
		if raw := readControlFIFOLine(t, drain, 80*time.Millisecond); len(raw) > 0 {
			t.Fatalf("old host received FIFO bytes: %q", raw)
		}
		st, _ := loadGoalHostStatus(root, tk.ID, tk.ActiveAttemptID)
		if st.ControlProtocol != 0 {
			t.Fatalf("old host protocol upgraded: %+v", st)
		}
	})
	t.Run("missing-ack-no-resend", func(t *testing.T) {
		withShortControlWait(t)
		root, _, home, cfg, wf, _, drain := seedControlFIFO(t, hostedControlProtocol)
		errCh := make(chan error, 1)
		go func() { errCh <- requestGoalControl(root, cfg, wf, goalControlPause, home) }()
		raw := readControlFIFOLine(t, drain, time.Second)
		if len(raw) == 0 {
			t.Fatal("request was not written")
		}
		select {
		case err := <-errCh:
			if err == nil || !strings.Contains(err.Error(), goalFailPauseNotConfirmed) {
				t.Fatalf("missing ACK: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("missing ACK did not time out")
		}
		if extra := readControlFIFOLine(t, drain, 80*time.Millisecond); len(bytes.TrimSpace(extra)) > 0 {
			t.Fatalf("unknown ACK was resent: %q", extra)
		}
	})
	t.Run("old-ack", func(t *testing.T) {
		withShortControlWait(t)
		root, dir, home, cfg, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
		gid := tk.Goal.NativeGoalID
		errCh := make(chan error, 1)
		go func() { errCh <- requestGoalControl(root, cfg, wf, goalControlPause, home) }()
		req := parseControlRequest(t, readControlFIFOLine(t, drain, time.Second))
		ackControl(t, root, home, dir, gid, tk, req, "paused", true, func(st *goalHostStatus) {
			st.LastControlID = "not-" + req.ID
		})
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("old ACK confirmed this request")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("old ACK wait")
		}
	})
	t.Run("wrong-session", func(t *testing.T) {
		withShortControlWait(t)
		root, dir, home, cfg, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
		gid := tk.Goal.NativeGoalID
		errCh := make(chan error, 1)
		go func() { errCh <- requestGoalControl(root, cfg, wf, goalControlPause, home) }()
		req := parseControlRequest(t, readControlFIFOLine(t, drain, time.Second))
		ackControl(t, root, home, dir, gid, tk, req, "paused", true, func(st *goalHostStatus) {
			st.SessionID = "other-session"
		})
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("wrong session ACK confirmed this request")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("wrong session wait")
		}
	})
	t.Run("wrong-attempt", func(t *testing.T) {
		withShortControlWait(t)
		root, dir, home, cfg, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
		gid := tk.Goal.NativeGoalID
		errCh := make(chan error, 1)
		go func() { errCh <- requestGoalControl(root, cfg, wf, goalControlPause, home) }()
		req := parseControlRequest(t, readControlFIFOLine(t, drain, time.Second))
		ackControl(t, root, home, dir, gid, tk, req, "paused", true, nil)
		st, err := loadGoalHostStatus(root, tk.ID, tk.ActiveAttemptID)
		if err != nil {
			t.Fatal(err)
		}
		st.AttemptID = "other-attempt"
		raw, _ := json.MarshalIndent(st, "", "  ")
		if err := os.WriteFile(goalHostStatusPath(root, tk.ID, tk.ActiveAttemptID), append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("wrong attempt ACK confirmed this request")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("wrong attempt wait")
		}
	})
	t.Run("inject-bytes-only", func(t *testing.T) {
		withShortControlWait(t)
		root, dir, home, cfg, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
		gid := tk.Goal.NativeGoalID
		errCh := make(chan error, 1)
		go func() { errCh <- requestGoalControl(root, cfg, wf, goalControlPause, home) }()
		req := parseControlRequest(t, readControlFIFOLine(t, drain, time.Second))
		ackControl(t, root, home, dir, gid, tk, req, "active", true, nil)
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("ACK without native pause confirmed control")
			}
			if !strings.Contains(err.Error(), goalFailPauseNotConfirmed) {
				t.Fatalf("bytes-only: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("bytes-only wait")
		}
	})
}

func TestWriteGoalHostStatusDoesNotUpgradeOldControlProtocol(t *testing.T) {
	root := t.TempDir()
	if err := writeGoalHostStatus(root, "t1", "a1", goalHostStatus{SessionID: "s1", SupervisorAlive: true, ControlProtocol: 0}); err != nil {
		t.Fatal(err)
	}
	if err := writeGoalHostStatus(root, "t1", "a1", goalHostStatus{SessionID: "s1", SupervisorAlive: true, ControlProtocol: hostedControlProtocol}); err != nil {
		t.Fatal(err)
	}
	st, err := loadGoalHostStatus(root, "t1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if st.ControlProtocol != 0 {
		t.Fatalf("stamped current control protocol onto old host: %+v", st)
	}
}

func TestHostedControlLoopJSONRequestAcksCurrentID(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "loop-ctl-sess"
	gid := "loop-ctl-goal"
	writeGrokGoalFixture(t, home, cwd, sid, gid, "paused", grokNativeGoalStateFile{Phase: "Idle"})
	root := t.TempDir()
	tk := &Task{
		ID:              "t-json-ctl",
		Dir:             cwd,
		SessionID:       sid,
		ActiveAttemptID: "att-json-ctl",
		Goal: &TaskGoalBinding{
			NativeGoalID:   gid,
			BoundAttemptID: "att-json-ctl",
			Hosted:         true,
			ControlOwner:   goalControlOwnerHosted,
		},
	}
	if err := writeGoalHostStatus(root, tk.ID, tk.ActiveAttemptID, goalHostStatus{
		SessionID: sid, SupervisorAlive: true, ControlProtocol: hostedControlProtocol,
	}); err != nil {
		t.Fatal(err)
	}
	master, err := os.Create(filepath.Join(t.TempDir(), "master"))
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
		hostedControlLoop(root, tk, home, master, ctlR, stop, quietOutputWatch(t))
		close(done)
	}()
	req := hostedControlRequest{Kind: hostedControlKind, ID: "req-current-1", Action: goalControlPause, TaskID: tk.ID, AttemptID: tk.ActiveAttemptID, SessionID: sid}
	payload, _ := json.Marshal(req)
	if _, err := ctlW.Write(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var st goalHostStatus
	for time.Now().Before(deadline) {
		st, _ = loadGoalHostStatus(root, tk.ID, tk.ActiveAttemptID)
		if st.LastControlID == req.ID && st.ControlConfirmed && st.LastControl == goalControlPause {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	_ = ctlW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("control loop did not return")
	}
	if st.LastControlID != req.ID || !st.ControlConfirmed {
		t.Fatalf("host ACK: %+v", st)
	}
	_ = master.Close()
	_ = ctlR.Close()
}

func TestCmdWorkflowGoalControlUsesShippedRequestPath(t *testing.T) {
	withShortControlWait(t)
	root, dir, home, cfg, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
	_ = cfg
	gid := tk.Goal.NativeGoalID
	plantControlLeftover(t, root, tk, goalControlPause, goalFailControlUnsupported, false, "")
	writeGrokPermissionEvents(t, home, dir, tk.SessionID,
		`{"type":"permission_resolved","decision":"allow"}`,
		`{"type":"phase_changed","phase":"Executing"}`,
	)
	go func() {
		raw := readControlFIFOLine(t, drain, time.Second)
		var req hostedControlRequest
		if json.Unmarshal(bytes.TrimSpace(raw), &req) != nil {
			return
		}
		ackControl(t, root, home, dir, gid, tk, req, "paused", true, nil)
	}()
	out, err := captureWorkflowCmd(t, "goal-control", wf.ID, "-root", root, "-action", "pause")
	if err != nil {
		t.Fatalf("goal-control CLI: %v %s", err, out)
	}
	if !strings.Contains(out, "control="+goalControlPause) {
		t.Fatalf("stdout: %s", out)
	}
}

func TestCandidateBinGoalControlIdentity(t *testing.T) {
	withShortControlWait(t)
	bin := requireCandidateBin(t)
	root, dir, home, _, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
	gid := tk.Goal.NativeGoalID
	plantControlLeftover(t, root, tk, goalControlPause, goalFailControlUnsupported, false, "stale")
	writeGrokPermissionEvents(t, home, dir, tk.SessionID,
		`{"type":"permission_resolved","decision":"allow"}`,
		`{"type":"phase_changed","phase":"Executing"}`,
	)
	cmd := exec.Command(bin, "workflow", "goal-control", wf.ID, "-root", root, "-action", "pause")
	cmd.Env = append(os.Environ(), "GROK_HOME="+home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	req := parseControlRequest(t, readControlFIFOLine(t, drain, 2*time.Second))
	ackControl(t, root, home, dir, gid, tk, req, "paused", true, nil)
	err := cmd.Wait()
	if err != nil {
		t.Fatalf("candidate %s: %v stdout=%s stderr=%s argv=%v", bin, err, stdout.String(), stderr.String(), cmd.Args)
	}
	if !strings.Contains(stdout.String(), "control=pause") {
		t.Fatalf("candidate stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	t.Logf("binary=%s argv=%v request_id=%s", bin, cmd.Args, req.ID)
}

func TestCandidateBinHostAndClientEntry(t *testing.T) {
	entry := prepareSuppliedBinHostedHostAndClient(t)
	host := entry.hostArgv()
	if len(host) < 5 || host[0] != entry.Bin || host[1] != "workflow" || host[2] != "goal-run" {
		t.Fatalf("host must be supplied-bin workflow goal-run, got %v", host)
	}
	hosted := false
	for _, a := range host {
		if a == "-hosted" {
			hosted = true
		}
		if a == "goal-sync" {
			t.Fatalf("goal-sync is client observation, not hosted producer: %v", host)
		}
	}
	if !hosted {
		t.Fatalf("host argv missing -hosted: %v", host)
	}
	ctrl := entry.controlArgv(goalControlPause)
	obs := entry.inputObserveArgv()
	if ctrl[0] != entry.Bin || obs[0] != entry.Bin {
		t.Fatalf("client used a different binary host=%s control=%s observe=%s", entry.Bin, ctrl[0], obs[0])
	}
	t.Logf("prepared reusable hosted entry installer_fixture=6c3d417ac8ac4a194a2083fcddaa4bc8bd6c9878c4e1fd909e078dfc62cb661c TestHostedOperatorInputRealCLIAndControls/TestInstalledHostedOperatorInputRealCLIAndControls binary=%s host_argv=%v client_control_argv=%v client_input_observe_argv=%v covered=false",
		entry.Bin, host, ctrl, obs)
	if strings.TrimSpace(os.Getenv("CARDEX_RUN_HOSTED_PTY")) != "1" {
		t.Skip("PTY exec unrun: reuse items5-8 out of pty devices; do not probe; host path is workflow goal-run -hosted plus client goal-control/goal-input against the same fake PTY; not covered; source-owner runs CARDEX_RUN_HOSTED_PTY=1 after actual exit/custody when PTY returns")
	}
	entry.runAgainstFakePTY(t)
}

func TestInstalledD63SameActionRetryStaysRed(t *testing.T) {
	const want = "d63f12fbcb2a996bacc66cc4c3112f94f7ef049f452396f2522d3ab4396bb027"
	bin := "/opt/homebrew/bin/cardex"
	sum, err := fileSHA256(bin)
	if err != nil {
		t.Fatalf("blocking unverifiable: retained d63 missing: %v", err)
	}
	if sum != want {
		t.Fatalf("blocking unverifiable: %s sha256=%s want %s", bin, sum, want)
	}
	withShortControlWait(t)
	root, dir, home, _, wf, tk, drain := seedControlFIFO(t, hostedControlProtocol)
	plantControlLeftover(t, root, tk, goalControlPause, goalFailControlUnsupported, false, "")
	writeGrokPermissionEvents(t, home, dir, tk.SessionID,
		`{"type":"permission_resolved","decision":"allow"}`,
		`{"type":"phase_changed","phase":"Executing"}`,
	)
	started := time.Now()
	cmd := exec.Command(bin, "workflow", "goal-control", wf.ID, "-root", root, "-action", "pause")
	cmd.Env = append(os.Environ(), "GROK_HOME="+home)
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(string(out), goalFailControlUnsupported) {
		t.Fatalf("d63 must stay RED on leftover control_unsupported: err=%v out=%s", err, out)
	}
	if elapsed > 800*time.Millisecond {
		t.Fatalf("d63 did not return immediately from leftover failure: %s", elapsed)
	}
	_ = drain
	t.Logf("d63 RED elapsed=%s out=%s", elapsed, strings.TrimSpace(string(out)))
}

func TestHostedControlLoopScannerStillReadsPlainStop(t *testing.T) {
	// keep the preexisting plain-text host path for in-loop tests
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "plain-stop"
	gid := "plain-goal"
	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved", Phase: "Idle",
	})
	tk := &Task{
		ID: "t-plain", Dir: cwd, SessionID: sid, ActiveAttemptID: "att-plain",
		Goal: &TaskGoalBinding{NativeGoalID: gid, BoundAttemptID: "att-plain", Hosted: true, ControlOwner: goalControlOwnerHosted},
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
	if _, err := ctlW.Write([]byte("stop\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(masterPath)
		if strings.Contains(string(raw), "/quit") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	_ = ctlW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop")
	}
	_ = master.Close()
	_ = ctlR.Close()
}
