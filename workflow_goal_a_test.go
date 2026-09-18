package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyGoalLaunchErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("fork/exec grok-with-surge: inappropriate ioctl for device"), goalFailPTYIoctl},
		{errors.New("not a tty"), goalFailPTYMissing},
		{errors.New("operation not permitted"), goalFailPermission},
		{errors.New("CreateProcess Rejected: approval denied"), goalFailApprovalDenied},
		{errors.New("unknown flag: --budget"), goalFailBudgetFlagLayer},
		{errors.New("TUI is not a shell: $(cat"), goalFailTUINotShell},
		{errors.New("linked git common-dir"), goalFailInitGit},
	}
	for _, tc := range cases {
		if got := classifyGoalLaunchError(tc.err); got != tc.want {
			t.Fatalf("%q: got %s want %s", tc.err, got, tc.want)
		}
	}
	if !planningFailedUnknown("Planning failed") {
		t.Fatal("Planning failed must stay unknown")
	}
}

func TestCopyableGoalCommandIsNotShellAndBudgetIsGoalFlag(t *testing.T) {
	t.Parallel()
	cmd := copyableNativeGoalCommand("/abs/contract.txt", "abc", 12000)
	if strings.Contains(cmd, "$(cat") {
		t.Fatal("TUI must not be treated as a shell")
	}
	if !strings.HasPrefix(cmd, "/goal ") {
		t.Fatalf("native command is /goal, got %s", cmd)
	}
	if !strings.Contains(cmd, " --budget 12000") {
		t.Fatal("budget belongs on /goal, not grok argv")
	}
	args := manualGrokGoalArgs(nil, &Task{Dir: "/tmp/proj", SessionID: "s"}, "grok-4.6", "xhigh", "workspace", "auto", "/tmp/home")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--budget") {
		t.Fatalf("grok top-level must not take --budget: %s", joined)
	}
	if !containsArg(args, "--cwd") {
		t.Fatalf("missing --cwd: %s", joined)
	}
}

func containsArg(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func TestPlanningFailedMapsUnknownNotTimeout(t *testing.T) {
	t.Parallel()
	obs := nativeGoalObservation{NativeStatus: "paused", PauseMessage: "Planning failed", UpdatesOK: true}
	mapped := mapNativeGoalToTask(obs, false, false, false)
	if mapped.Observation != goalObsHeld {
		t.Fatalf("paused stays held, got %s", mapped.Observation)
	}
	if strings.Contains(strings.ToLower(mapped.Note), "timeout") {
		t.Fatalf("must not invent timeout: %s", mapped.Note)
	}
	if !strings.Contains(strings.ToLower(mapped.Note), "unspecified") && !strings.Contains(mapped.Note, "Planning failed") {
		t.Fatalf("planning failed must remain unspecified: %s", mapped.Note)
	}
}

func TestExternalObserveDoesNotInventAttempt(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	home := t.TempDir()
	sess := newGoalSessionID()
	goalID := "g-ext-1"
	writeGrokGoalFixture(t, home, dir, sess, goalID, "active", grokNativeGoalStateFile{Phase: "Executing"})
	if err := attachExternalGoalObservation(root, tk, home, dir, sess, goalID); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal.BoundAttemptID != "" || loaded.ActiveAttemptID != "" {
		t.Fatalf("external observe must not invent an attempt: %+v", loaded.Goal)
	}
	if loaded.Goal.ControlOwner != goalControlOwnerExternal || !loaded.Goal.External {
		t.Fatalf("must mark external observe-only: %+v", loaded.Goal)
	}
	if err := requestGoalControl(root, cfg, wf, goalControlPause, home); err == nil {
		t.Fatal("external observe must refuse takeover")
	}
}

func TestBindSessionRejectsHermesDisguisedAsCodexUUID(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	err := bindWorkflowSession(root, cfg, wf, PersistentSessionRef{
		Role: sessionRoleManager, Provider: sessionProvHermes, SessionType: sessionTypeHermes,
		SessionID: "01a03a2c-21db-7f90-a7f0-ccd8cabf966e",
	})
	if err == nil {
		t.Fatal("hermes must not accept a Codex UUID")
	}
	if err := bindWorkflowSession(root, cfg, wf, PersistentSessionRef{
		Role: sessionRoleManager, Provider: sessionProvHermes, SessionType: sessionTypeHermes,
		SessionID: "20260914_010203_abcdef", Profile: "yvonne",
	}); err != nil {
		t.Fatalf("hermes session id: %v", err)
	}
	if err := bindWorkflowSession(root, cfg, wf, PersistentSessionRef{
		Role: sessionRoleDesign, Provider: sessionProvCodex, SessionType: sessionTypeCodex,
		SessionID: "01a03a2c-21db-7f90-a7f0-ccd8cabf966e", OwnerEntry: true,
	}); err != nil {
		t.Fatalf("owner design entry: %v", err)
	}
	loaded, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OwnerDesignEntry == nil || loaded.OwnerDesignEntry.SessionID == loaded.ManagerSession.SessionID {
		t.Fatal("owner design entry and manager session must stay distinct")
	}
}

func TestDesignRequestTwoJudgmentsAndRecoverPointer(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	argv := filepath.Join(t.TempDir(), "argv")
	outDir := t.TempDir()
	cfg.GrokBuildBin = fakeGrokDesignBin(t, argv, outDir)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	sess := newGoalSessionID()
	t.Setenv("GROK_HOME", outDir)
	if err := bindWorkflowSession(root, cfg, wf, PersistentSessionRef{
		Role: sessionRoleDesign, Provider: sessionProvGrok, SessionType: sessionTypeGrok, SessionID: sess,
	}); err != nil {
		t.Fatal(err)
	}
	req1, err := submitWorkflowDesignRequest(root, cfg, wf, "judgment-one: keep original goal", "digest-a", "c1")
	if err != nil {
		t.Fatal(err)
	}
	reply1, err := collectWorkflowDesignReply(root, cfg, wf, req1.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	req2, err := submitWorkflowDesignRequest(root, cfg, wf, "judgment-two: continue from prior "+req1.RequestID, "digest-b", "c2")
	if err != nil {
		t.Fatal(err)
	}
	if req2.PriorRequestID != req1.RequestID {
		t.Fatalf("second judgment must reference first request, got %s", req2.PriorRequestID)
	}
	if req2.ContextVer != 2 {
		t.Fatalf("context version=%d", req2.ContextVer)
	}
	if req2.ReplyID == reply1.ReplyID {
		t.Fatal("second reply must have its own identity")
	}
	loaded, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DesignSession.SessionID != sess || loaded.DesignSession.RecoverPath == "" {
		t.Fatal("recover pointer missing")
	}
	if _, err := collectWorkflowDesignReply(root, cfg, loaded, ""); err != nil {
		t.Fatalf("recover via persisted pointer: %v", err)
	}
}

func fakeGrokDesignBin(t *testing.T, argvPath, grokHome string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\n"
	if argvPath != "" {
		script += "printf '%s\\n' \"$@\" > " + shSingleQuote(argvPath) + "\n"
	}
	script += "printf 'DESIGN_OK %s\\n' \"$*\"\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = grokHome
	return path
}

func TestHermesWakeRequiresAckNotLocalReceipt(t *testing.T) {
	root := t.TempDir()
	sub := ManagerWakeSubscription{
		ID: "yvonne-dev", Provider: "hermes", SessionType: sessionTypeHermes,
		ThreadID: "20260914_010203_abcdef", Profile: "yvonne", Enabled: true,
		TaskIDs: []string{"t0914-0001-aaaa"},
	}
	if _, class := closedManagerWakeSubscription(sub); class != "" {
		t.Fatalf("closed hermes sub: %s", class)
	}
	row := managerWakeOutboxRow{
		Schema: outboxSchemaV1, Seq: 1, WakeEventID: "wake-1", TaskID: "t0914-0001-aaaa",
		EventType: evDone, Status: statusDone, TS: time.Now().UTC().Format(time.RFC3339Nano),
	}
	msg := "cardex task=t0914-0001-aaaa wake=wake-1"
	managerWakeQueueTimeout = 200 * time.Millisecond
	t.Cleanup(func() { managerWakeQueueTimeout = time.Duration(managerWakeQueueTimeoutSec) * time.Second })
	if err := deliverHermesWake(root, sub, []managerWakeOutboxRow{row}, msg); err == nil {
		t.Fatal("local inbound without ack must not count as delivered")
	}
	managerWakeHermesAckHook = func(root string, sub ManagerWakeSubscription, ids []string) error {
		sum := sha256Hex(msg)
		for _, id := range ids {
			if err := writeHermesAckForTest(root, sub.ID, id, sum, ids); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() { managerWakeHermesAckHook = nil })
	if err := deliverHermesWake(root, sub, []managerWakeOutboxRow{row}, msg); err != nil {
		t.Fatalf("ack should confirm: %v", err)
	}
	raw, err := os.ReadFile(hermesInboundPath(root, sub.ID, "wake-1"))
	if err != nil {
		t.Fatal(err)
	}
	var inbound hermesInboundFile
	if json.Unmarshal(raw, &inbound) != nil || inbound.SessionID != sub.ThreadID || inbound.Profile != "yvonne" {
		t.Fatalf("inbound identity: %+v", inbound)
	}
}

func TestSlaveWriteHelperIsNotControlConfirmation(t *testing.T) {
	t.Parallel()
	st := goalHostStatus{LastInject: "/goal pause", ControlConfirmed: false, NativeStatus: "active"}
	if st.ControlConfirmed || nativeStatusMatchesControl(goalControlPause, st.NativeStatus) {
		t.Fatal("active after inject is not a confirmed pause")
	}
	if !nativeStatusMatchesControl(goalControlPause, "paused") {
		t.Fatal("native paused is confirmation")
	}
	if !nativeStatusMatchesControl(goalControlPause, "user_paused") {
		t.Fatal("native user_paused is confirmation")
	}
	mapped := mapNativeGoalToTask(nativeGoalObservation{NativeStatus: "user_paused", UpdatesOK: true}, false, false, false)
	if mapped.Status != statusHeld || mapped.Continuation != "same-session" {
		t.Fatalf("user_paused must be held same-session, got status=%s cont=%s", mapped.Status, mapped.Continuation)
	}
}
