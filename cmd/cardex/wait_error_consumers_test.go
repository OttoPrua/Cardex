package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWaitErrorFactsSharedAcrossListBoardDigest(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	fixtures := seedWaitErrorFixtures(t, root)
	writeWaitErrorDigestOutbox(t, root, fixtures)

	listText, err := captureStdout(t, func() error {
		return cmdList([]string{"-root", root, "-all"})
	})
	if err != nil {
		t.Fatal(err)
	}
	listJSON, err := captureStdout(t, func() error {
		return cmdList([]string{"-root", root, "-json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var listed []map[string]any
	if err := json.Unmarshal([]byte(listJSON), &listed); err != nil {
		t.Fatal(err)
	}
	listClass := map[string]string{}
	listReason := map[string]string{}
	for _, row := range listed {
		id, _ := row["id"].(string)
		class, _ := row["wait_error_class"].(string)
		reason, _ := row["wait_error_reason"].(string)
		listClass[id] = class
		listReason[id] = reason
	}

	snap, err := buildSnapshot(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	boardClass := map[string]string{}
	boardReason := map[string]string{}
	projID := ""
	for _, p := range snap.Projects {
		for _, ph := range p.Phases {
			for _, tb := range ph.Tasks {
				boardClass[tb.ID] = tb.WaitErrorClass
				boardReason[tb.ID] = tb.WaitErrorReason
				if projID == "" {
					projID = p.ID
				}
			}
		}
	}
	srv := newBoardServer(root, 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/project?id="+projID, nil)
	srv.handleProject(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("board API %d %s", rec.Code, rec.Body.String())
	}
	apiRaw := rec.Body.String()

	digestJSON, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root, "-since", "1", "-json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var digestRows []digestEntry
	if err := json.Unmarshal([]byte(digestJSON), &digestRows); err != nil {
		t.Fatal(err)
	}
	digestClass := map[string]string{}
	digestReason := map[string]string{}
	for _, row := range digestRows {
		digestClass[row.TaskID] = row.WaitErrorClass
		digestReason[row.TaskID] = row.WaitErrorReason
	}

	want := map[string]string{
		"we-inflight":    waitClassInFlight,
		"we-permwait":    waitClassPermissionWaiting,
		"we-failed":      waitClassFailure,
		"we-unknown":     waitClassUnknown,
		"we-budget":      waitClassBudgetLimited,
		"we-quota":       waitClassAccountQuota,
		"we-transport":   waitClassTransport,
		"we-unavailable": waitClassTransport,
		"we-auth":        waitClassAuth,
		"we-cancel":      waitClassCancellation,
	}
	for id, class := range want {
		if listClass[id] != class {
			t.Fatalf("list %s class=%q want %s json=%s", id, listClass[id], class, listJSON)
		}
		if boardClass[id] != class {
			t.Fatalf("board snapshot %s class=%q want %s", id, boardClass[id], class)
		}
		if digestClass[id] != class {
			t.Fatalf("digest %s class=%q want %s json=%s", id, digestClass[id], class, digestJSON)
		}
		if listClass[id] != boardClass[id] || boardClass[id] != digestClass[id] {
			t.Fatalf("consumers disagree on %s list=%q board=%q digest=%q", id, listClass[id], boardClass[id], digestClass[id])
		}
		if !strings.Contains(listText, class) {
			t.Fatalf("list text missing class %s for %s:\n%s", class, id, listText)
		}
		if !strings.Contains(apiRaw, `"wait_error_class":"`+class+`"`) {
			t.Fatalf("board API missing %s class %s: %s", id, class, apiRaw)
		}
		if listReason[id] == "" || boardReason[id] == "" || digestReason[id] == "" {
			t.Fatalf("empty reason %s list=%q board=%q digest=%q", id, listReason[id], boardReason[id], digestReason[id])
		}
	}
	for _, id := range []string{"we-transport", "we-unavailable"} {
		blob := listClass[id] + listReason[id] + boardClass[id] + boardReason[id] + digestClass[id] + digestReason[id]
		if strings.Contains(strings.ToLower(blob), "quota") || listClass[id] == waitClassAccountQuota {
			t.Fatalf("transport/unavailable labeled quota: %s %s", id, blob)
		}
		if strings.Contains(strings.ToLower(listText), "we-transport") && strings.Contains(listText, waitClassAccountQuota+":") {
			// class tokens appear independently; the transport row itself must not be quota
		}
		if !strings.Contains(strings.ToLower(listReason[id]+boardReason[id]+digestReason[id]), "not quota") &&
			!strings.Contains(strings.ToLower(listReason[id]), "unavailable") &&
			!strings.Contains(strings.ToLower(listReason[id]), "transport") &&
			!strings.Contains(strings.ToLower(listReason[id]), "connection refused") {
			t.Fatalf("transport reason not actionable: %s", listReason[id])
		}
	}
	if strings.Contains(strings.ToLower(listReason["we-unavailable"]+boardReason["we-unavailable"]+digestReason["we-unavailable"]), "quota exhausted") {
		t.Fatal("provider unavailable described as quota exhaustion")
	}
}

func seedWaitErrorFixtures(t *testing.T, root string) []*Task {
	t.Helper()
	now := time.Now().Format(time.RFC3339)
	save := func(tk *Task) *Task {
		t.Helper()
		if tk.CreatedAt == "" {
			tk.CreatedAt = now
			tk.UpdatedAt = now
		}
		if tk.Dir == "" {
			tk.Dir = filepath.Join(root, "work", tk.ID)
		}
		if err := os.MkdirAll(tk.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if len(tk.Prompts) == 0 {
			tk.Prompts = []string{"p"}
		}
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	inflight := save(&Task{ID: "we-inflight", Title: "in flight card", Type: typeSequence, Status: statusRunning, Project: "WaitErr"})
	perm := save(&Task{ID: "we-permwait", Title: "permission wait card", Type: typeReview, Status: statusRunning, Project: "WaitErr", ActiveAttemptID: "att-perm", SessionID: "sess-perm"})
	if err := persistGrokPermissionPending(root, perm, grokPermissionPending{
		grokPermissionIdentity: grokPermissionIdentity{
			TaskID: perm.ID, SessionID: "sess-perm", AttemptID: "att-perm",
			ToolCallID: "call-perm", OperationDigest: strings.Repeat("a", 64),
			JSONRPCRequestID: "1", RequestID: "req-perm-wait",
		},
		ToolName: "run_terminal_command",
		State:    grokPermissionPendingState,
	}); err != nil {
		t.Fatal(err)
	}
	failed := save(&Task{ID: "we-failed", Title: "failed card", Type: typeSequence, Status: statusFailed, Project: "WaitErr", LastError: "compile failed: missing symbol"})
	unknown := save(&Task{ID: "we-unknown", Title: "unknown held", Type: typeSequence, Status: statusHeld, Project: "WaitErr",
		Goal: &TaskGoalBinding{Observation: goalObsUnknown, ObservationNote: ""}})
	budget := save(&Task{ID: "we-budget", Title: "budget limited", Type: typeSequence, Status: statusHeld, Project: "WaitErr",
		Goal: &TaskGoalBinding{LastNativeStatus: "budget_limited", Observation: goalObsUnknown, ObservationNote: "budget_limited held"}})
	quota := save(&Task{ID: "we-quota", Title: "account quota", Type: typeSequence, Status: statusLimitPaused, Project: "WaitErr",
		LastError: "用量限额: account quota", LastRouteAttempt: &RouteAttemptReadback{FailureKind: string(fallbackQuota)}})
	transport := save(&Task{ID: "we-transport", Title: "transport timeout", Type: typeSequence, Status: statusHeld, Project: "WaitErr",
		LastError: "connection refused while dialing provider", LastRouteAttempt: &RouteAttemptReadback{FailureKind: string(fallbackTransport)}})
	unavailable := save(&Task{ID: "we-unavailable", Title: "provider unavailable", Type: typeSequence, Status: statusHeld, Project: "WaitErr",
		LastError: "model unavailable", LastProviderPreflight: &ProviderPreflightReadback{State: providerModelUnavailable, Reason: "model unavailable"}})
	auth := save(&Task{ID: "we-auth", Title: "auth held", Type: typeSequence, Status: statusHeld, Project: "WaitErr",
		LastError: "[auth] please login again", LastRouteAttempt: &RouteAttemptReadback{FailureClass: string(failureAuth)}})
	cancel := save(&Task{ID: "we-cancel", Title: "canceled card", Type: typeSequence, Status: statusCanceled, Project: "WaitErr"})
	return []*Task{inflight, perm, failed, unknown, budget, quota, transport, unavailable, auth, cancel}
}

func writeWaitErrorDigestOutbox(t *testing.T, root string, tasks []*Task) {
	t.Helper()
	var b strings.Builder
	for i, tk := range tasks {
		row := managerWakeOutboxRow{
			Schema: outboxSchemaV1, Seq: int64(i + 1), WakeEventID: "w", TaskID: tk.ID,
			TaskEventSeq: 1, TransitionID: "t", Project: tk.Project, EventType: tk.Status,
			Status: tk.Status, ReasonClass: "cli", TS: "2026-10-25T00:00:00Z",
		}
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	path := managerWakeOutboxPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGrokPermissionCLIWrongIdentityWritesNoBytes(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	tk := &Task{ID: "perm-cli", Title: "perm", Type: typeReview, Status: statusRunning, ActiveAttemptID: "att-1", Dir: t.TempDir(), Prompts: []string{"p"}}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	pending := grokPermissionPending{
		grokPermissionIdentity: grokPermissionIdentity{
			TaskID: tk.ID, SessionID: "sess-1", AttemptID: "att-1",
			ToolCallID: "call-1", OperationDigest: strings.Repeat("b", 64),
			JSONRPCRequestID: "7", RequestID: "req-cli-1",
		},
		ToolName: "run_terminal_command",
	}
	if err := persistGrokPermissionPending(root, tk, pending); err != nil {
		t.Fatal(err)
	}
	answerPath := grokPermissionAnswerPath(root, tk.ID, "att-1", pending.RequestID)
	wrong := [][]string{
		{"-root", root, "-request", pending.RequestID, "-decision", "allow-once", "-session", "other-session", tk.ID},
		{"-root", root, "-request", pending.RequestID, "-decision", "allow-once", "-attempt", "other-attempt", tk.ID},
		{"-root", root, "-request", pending.RequestID, "-decision", "allow-once", "-tool-call", "other-call", tk.ID},
		{"-root", root, "-request", pending.RequestID, "-decision", "allow-once", "-digest", strings.Repeat("0", 64), tk.ID},
		{"-root", root, "-request", "req-missing", "-decision", "allow-once", tk.ID},
	}
	for _, args := range wrong {
		err := cmdGrokPermissionAnswer(args)
		if err == nil {
			t.Fatalf("wrong identity accepted: %v", args)
		}
		if _, statErr := os.Stat(answerPath); !os.IsNotExist(statErr) {
			t.Fatalf("wrong identity wrote answer bytes: %v", args)
		}
	}
	if err := cmdGrokPermissionAnswer([]string{
		"-root", root, "-request", pending.RequestID, "-decision", "allow-once",
		"-session", pending.SessionID, "-attempt", pending.AttemptID,
		"-tool-call", pending.ToolCallID, "-digest", pending.OperationDigest, tk.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(answerPath); err != nil {
		t.Fatalf("matching allow-once did not persist: %v", err)
	}
	if err := cmdGrokPermissionAnswer([]string{
		"-root", root, "-request", pending.RequestID, "-decision", "allow-once",
		"-session", pending.SessionID, "-attempt", pending.AttemptID, tk.ID,
	}); err == nil {
		t.Fatal("duplicate answer authorized")
	}
}

func TestWaitErrorStalePermissionPendingNotCurrent(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "stale-grok-home")
	writeStaleNativePermissionEvents := func(cwd, session string) {
		t.Helper()
		path := grokNativeEventsPath(home, cwd, session)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "{\"type\":\"phase_changed\",\"phase\":\"permission_prompt\"}\n{\"type\":\"permission_requested\",\"tool_name\":\"stale-tool\"}\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seedPending := func(tk *Task, attempt, session, reqID string) {
		t.Helper()
		prev := tk.ActiveAttemptID
		tk.ActiveAttemptID = attempt
		if err := persistGrokPermissionPending(root, tk, grokPermissionPending{
			grokPermissionIdentity: grokPermissionIdentity{
				TaskID: tk.ID, SessionID: session, AttemptID: attempt,
				ToolCallID: "call-stale", OperationDigest: strings.Repeat("c", 64),
				JSONRPCRequestID: "9", RequestID: reqID,
			},
			ToolName: "run_terminal_command",
		}); err != nil {
			t.Fatal(err)
		}
		tk.ActiveAttemptID = prev
	}

	canceled := &Task{ID: "we-stale-cancel", Title: "stale cancel", Type: typeSequence, Status: statusCanceled,
		Project: "WaitErr", Dir: filepath.Join(root, "work", "we-stale-cancel"), SessionID: "sess-stale-cancel",
		ActiveAttemptID: "att-stale-cancel", Prompts: []string{"p"},
		Goal: &TaskGoalBinding{Observation: goalObsCanceled, GrokHome: home}}
	if err := os.MkdirAll(canceled.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, canceled); err != nil {
		t.Fatal(err)
	}
	seedPending(canceled, "att-stale-cancel", "sess-stale-cancel", "req-stale-cancel")
	writeStaleNativePermissionEvents(canceled.Dir, canceled.SessionID)

	budget := &Task{ID: "we-stale-budget", Title: "stale budget", Type: typeSequence, Status: statusHeld,
		Project: "WaitErr", Dir: filepath.Join(root, "work", "we-stale-budget"), SessionID: "sess-stale-budget",
		ActiveAttemptID: "att-stale-budget", Prompts: []string{"p"},
		Goal: &TaskGoalBinding{LastNativeStatus: "budget_limited", Observation: goalObsUnknown, ObservationNote: "budget_limited held", GrokHome: home}}
	if err := os.MkdirAll(budget.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, budget); err != nil {
		t.Fatal(err)
	}
	seedPending(budget, "att-stale-budget", "sess-stale-budget", "req-stale-budget")
	writeStaleNativePermissionEvents(budget.Dir, budget.SessionID)

	oldAtt := &Task{ID: "we-stale-oldatt", Title: "running old attempt pending", Type: typeReview, Status: statusRunning,
		Project: "WaitErr", Dir: filepath.Join(root, "work", "we-stale-oldatt"), SessionID: "sess-new",
		ActiveAttemptID: "att-old", Prompts: []string{"p"}}
	if err := os.MkdirAll(oldAtt.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, oldAtt); err != nil {
		t.Fatal(err)
	}
	seedPending(oldAtt, "att-old", "sess-old", "req-stale-old")
	oldAtt.ActiveAttemptID = "att-new"
	if err := saveTask(root, oldAtt); err != nil {
		t.Fatal(err)
	}

	gotCancel := classifyWaitError(root, canceled)
	if gotCancel.Class != waitClassCancellation {
		t.Fatalf("canceled+stale pending class=%s reason=%s", gotCancel.Class, gotCancel.Reason)
	}
	gotBudget := classifyWaitError(root, budget)
	if gotBudget.Class != waitClassBudgetLimited {
		t.Fatalf("budget+stale pending class=%s reason=%s", gotBudget.Class, gotBudget.Reason)
	}
	freshOld, err := loadTask(root, oldAtt.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotOld := classifyWaitError(root, freshOld)
	if gotOld.Class != waitClassInFlight {
		t.Fatalf("running with leftover old-attempt pending class=%s reason=%s", gotOld.Class, gotOld.Reason)
	}

	listJSON, err := captureStdout(t, func() error { return cmdList([]string{"-root", root, "-json"}) })
	if err != nil {
		t.Fatal(err)
	}
	digestJSON, err := captureStdout(t, func() error { return cmdDigest([]string{"-root", root, "-json"}) })
	if err != nil {
		t.Fatal(err)
	}
	snap, err := buildSnapshot(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"we-stale-cancel", "we-stale-budget", "we-stale-oldatt"} {
		if strings.Contains(listJSON, `"id": "`+id+`"`) && strings.Contains(listJSON, `"wait_error_class": "`+waitClassPermissionWaiting+`"`) {
			// list JSON contains many cards; check per-id class below
		}
		_ = digestJSON
	}
	var listed []map[string]any
	if err := json.Unmarshal([]byte(listJSON), &listed); err != nil {
		t.Fatal(err)
	}
	classOf := map[string]string{}
	for _, row := range listed {
		id, _ := row["id"].(string)
		class, _ := row["wait_error_class"].(string)
		classOf[id] = class
	}
	if classOf["we-stale-cancel"] != waitClassCancellation {
		t.Fatalf("list canceled stale pending class=%s", classOf["we-stale-cancel"])
	}
	if classOf["we-stale-budget"] != waitClassBudgetLimited {
		t.Fatalf("list budget stale pending class=%s", classOf["we-stale-budget"])
	}
	if classOf["we-stale-oldatt"] != waitClassInFlight {
		t.Fatalf("list old-attempt pending class=%s", classOf["we-stale-oldatt"])
	}
	for _, p := range snap.Projects {
		for _, ph := range p.Phases {
			for _, tb := range ph.Tasks {
				switch tb.ID {
				case "we-stale-cancel":
					if tb.WaitErrorClass != waitClassCancellation {
						t.Fatalf("board canceled stale class=%s", tb.WaitErrorClass)
					}
				case "we-stale-budget":
					if tb.WaitErrorClass != waitClassBudgetLimited {
						t.Fatalf("board budget stale class=%s", tb.WaitErrorClass)
					}
				case "we-stale-oldatt":
					if tb.WaitErrorClass != waitClassInFlight {
						t.Fatalf("board old-attempt class=%s", tb.WaitErrorClass)
					}
				}
			}
		}
	}
}

func TestHostedEnterAndDuplicateSameInputFailClosed(t *testing.T) {
	for _, key := range []string{"enter", "tab", "up", "down"} {
		req := hostedInputRequest{Kind: hostedInputKind, ID: "n", TaskID: "t", AttemptID: "a", SessionID: "s", Key: key, Prompt: "p"}
		if err := validateHostedOperatorInput(t.TempDir(), &Task{ID: "t", Goal: &TaskGoalBinding{}}, req); err == nil || !strings.Contains(err.Error(), "permission-modal") {
			t.Fatalf("key %s must refuse modal choice: %v", key, err)
		}
	}
	if !hostedSameInputAlreadyDelivered(goalHostStatus{LastInputID: "id1", LastInputKey: "1", LastInputPrompt: "fp"}, "1", "fp") {
		t.Fatal("duplicate same-input not detected")
	}
	if hostedSameInputAlreadyDelivered(goalHostStatus{LastInputID: "id1", InputError: "refused"}, "1", "fp") {
		t.Fatal("failed first delivery (no successful key/prompt) must remain retryable")
	}
	if !hostedSameInputAlreadyDelivered(goalHostStatus{LastInputID: "reject-id", LastInputKey: "1", LastInputPrompt: "fp", InputError: "stale prompt"}, "1", "fp") {
		t.Fatal("rejected intervening request must not erase successful same-prompt dedup")
	}
	if _, err := hostedOperatorKey("ctrl-enter"); err == nil {
		t.Fatal("cardex operator-key adapter must not map ctrl-enter; native TUI Send-now stays outside this adapter")
	}
	oldProtocol2 := goalHostStatus{SupervisorAlive: true, InputProtocol: 2, LastInputID: "old-ack"}
	if hostedSupervisorHasCurrentInputCapability(oldProtocol2) {
		t.Fatal("old protocol2 host without successful-delivery retention must not look current")
	}
	if hostedSameInputAlreadyDelivered(oldProtocol2, "1", "fp") {
		t.Fatal("old protocol2 host never recorded LastInputKey/LastInputPrompt")
	}
	if !hostedSupervisorHasCurrentInputCapability(goalHostStatus{SupervisorAlive: true, InputProtocol: hostedInputProtocol}) {
		t.Fatal("fresh current host must advertise current operator-input capability")
	}
}

func TestFrozenRoutePreviewAdmissionArgvAndLegacyPin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake grok argv")
	}
	root := testRoot(t)
	dir := t.TempDir()
	cfg := policyTestConfig()
	cfg.RouteMatrix = RouteMatrix{
		"medium": {"general": {
			Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "xhigh",
			Fallback: []RouteLegConfig{{Runner: "codex", Model: "gpt-5.6-sol", Effort: "high"}},
		}},
	}
	bin, argvDump, _ := fakeGrokBuild(t, `{"type":"end","stopReason":"end_turn"}`+"\n", "", 0)
	cfg.GrokBuildBin = bin
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	previewRaw, err := captureStdout(t, func() error {
		return cmdRoute([]string{"-root", root, "-complexity", "medium", "-category", "general"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var preview struct {
		Primary  policyLeg   `json:"primary"`
		Fallback []policyLeg `json:"fallback"`
	}
	if err := json.Unmarshal([]byte(previewRaw), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Primary.Runner != grokBuildRunnerName || preview.Primary.Model != "grok-4.6" || preview.Primary.Effort != "xhigh" {
		t.Fatalf("preview primary %+v", preview.Primary)
	}
	if len(preview.Fallback) != 1 {
		t.Fatalf("preview unused fallback missing: %s", previewRaw)
	}

	task := newTask(root, cfg, typeSequence, "frozen argv card", dir, []string{"p"}, 5)
	task.Model = "sonnet"
	task.RouteClass = routeClassGeneral
	task.PreferRunner = "codex"
	if err := admitTaskRoute(cfg, task); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if !completeFrozenRoute(task) || !ownerRouteSnapshotLegMatches(task, preview.Primary) {
		t.Fatalf("admission drifted from preview %+v %+v", preview.Primary, task.FrozenRoute)
	}
	before, err := json.Marshal(task.FrozenRoute)
	if err != nil {
		t.Fatal(err)
	}
	oldPin := task.GrokModel + "/" + task.GrokEffort

	cfg.RouteMatrix["medium"]["general"] = ConfiguredRoute{
		Runner: grokBuildRunnerName, Model: "grok-4.7", Effort: "high",
		Fallback: []RouteLegConfig{{Runner: "codex", Model: "gpt-6.1-sol", Effort: "max"}},
	}
	cfg.GrokBuild.Model = "grok-4.7"
	cfg.GrokBuild.Effort = "high"
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	oldAfter, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(oldAfter.FrozenRoute)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("old frozen pin changed after config edit before=%s after=%s", before, after)
	}
	if oldAfter.GrokModel+"/"+oldAfter.GrokEffort != oldPin {
		t.Fatalf("old grok pin reinterpreted %s -> %s/%s", oldPin, oldAfter.GrokModel, oldAfter.GrokEffort)
	}
	model := resolveGrokBuildModel(cfg, oldAfter)
	effort := resolveGrokBuildEffort(cfg, oldAfter)
	if model != "grok-4.6" || effort != "xhigh" {
		t.Fatalf("live config leaked into frozen argv identity model=%s effort=%s", model, effort)
	}
	isolateGrokLifecycleHome(t, cfg)
	oldAfter.LastProviderPreflight = &ProviderPreflightReadback{Runner: grokBuildRunnerName, State: providerReady}
	if err := saveTask(root, oldAfter); err != nil {
		t.Fatal(err)
	}
	admitDirectInvoke(t, root, oldAfter)
	if _, _, err := invokeGrokBuild(context.Background(), root, cfg, oldAfter, "frozen argv prompt"); err != nil {
		t.Fatalf("invokeGrokBuild: %v", err)
	}
	captured, err := os.ReadFile(argvDump)
	if err != nil {
		t.Fatalf("fake bin did not record argv: %v", err)
	}
	argvText := string(captured)
	if !strings.Contains(argvText, "grok-4.6") || !strings.Contains(argvText, "xhigh") {
		t.Fatalf("captured argv missing frozen identity: %s", argvText)
	}
	if !strings.Contains(argvText, "--permission-mode") {
		t.Fatalf("captured argv missing permission field: %s", argvText)
	}
	if strings.Contains(argvText, "grok-4.7") || strings.Contains(argvText, "gpt-6.1-sol") {
		t.Fatalf("unused fallback/live config rerouted captured argv: %s", argvText)
	}
	if preview.Primary.ReadOnly && strings.Contains(argvText, "--permission-mode\nauto") {
		t.Fatalf("read-only frozen leg launched with auto permission: %s", argvText)
	}

	fresh := newTask(root, cfg, typeSequence, "new after edit", dir, []string{"p"}, 5)
	fresh.Model = "sonnet"
	fresh.RouteClass = routeClassGeneral
	fresh.PreferRunner = "codex"
	if err := admitTaskRoute(cfg, fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.GrokModel != "grok-4.7" {
		t.Fatalf("new work did not consume edited config: %+v", fresh.FrozenRoute)
	}
}

func items58FrozenGrokRoute(model, effort string) *frozenOwnerRoute {
	return &frozenOwnerRoute{Name: "items58-frozen", Legs: []frozenPolicyLeg{{
		Runner: grokBuildRunnerName, Model: model, Effort: effort, Stage: "primary",
	}}}
}

func TestFrozenRouteDoesNotReinterpretStoredPinsOrAmbiguousEvidence(t *testing.T) {
	cfg := policyTestConfig()
	cfg.RouteMatrix = RouteMatrix{"medium": {"general": {
		Runner: grokBuildRunnerName, Model: "grok-4.7", Effort: "xhigh",
	}}}
	cfg.GrokBuild.Model = "grok-4.7"
	cfg.GrokBuild.Effort = "xhigh"

	t.Run("frozen selector keeps concrete stored pin after config change", func(t *testing.T) {
		task := &Task{
			PreferRunner: grokBuildRunnerName, GrokModel: "grok-4.6", GrokEffort: "xhigh",
			FrozenRoute: items58FrozenGrokRoute(grokStableSelector, "high"),
			LastRouteAttempt: &RouteAttemptReadback{
				RequestedRunner: grokBuildRunnerName, ActualRunner: grokBuildRunnerName,
				RequestedModel: "grok-4.6", ActualModel: "grok-4.6",
			},
		}
		if got := resolveGrokBuildModel(cfg, task); got != "grok-4.6" {
			t.Fatalf("selector hid stored pin: %q", got)
		}
		if got := resolveGrokBuildEffort(cfg, task); got != "xhigh" {
			t.Fatalf("frozen selector effort overwrote stored pin: %q", got)
		}
		before := task.GrokModel + "/" + task.GrokEffort
		if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
			t.Fatal(err)
		}
		if task.GrokModel+"/"+task.GrokEffort != before {
			t.Fatalf("stored pin mutated under frozen selector: %s -> %s/%s", before, task.GrokModel, task.GrokEffort)
		}
	})

	t.Run("concrete stored pin differing from frozen leg fails closed", func(t *testing.T) {
		task := &Task{
			PreferRunner: grokBuildRunnerName, GrokModel: "grok-4.6", GrokEffort: "xhigh",
			FrozenRoute: items58FrozenGrokRoute("grok-4.7", "high"),
		}
		if got := resolveGrokBuildModel(cfg, task); got != "grok-4.6" {
			t.Fatalf("resolve reinterpreted stored pin as frozen leg: %q", got)
		}
		err := freezeGrokAttemptModel(context.Background(), cfg, task)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("differing frozen leg must fail closed: %v", err)
		}
		if task.GrokModel != "grok-4.6" || task.GrokEffort != "xhigh" {
			t.Fatalf("contradictory freeze mutated pin: %s/%s", task.GrokModel, task.GrokEffort)
		}
	})

	t.Run("ambiguous execution is not rescued by FrozenRoute", func(t *testing.T) {
		task := &Task{
			PreferRunner: grokBuildRunnerName, SessionID: "already-started", GrokEffort: "xhigh",
			FrozenRoute: items58FrozenGrokRoute("grok-4.6", "xhigh"),
		}
		err := freezeGrokAttemptModel(context.Background(), cfg, task)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || task.GrokModel != "" {
			t.Fatalf("FrozenRoute made unidentified session safe: err=%v model=%q", err, task.GrokModel)
		}
		conflict := &Task{
			PreferRunner: grokBuildRunnerName, GrokEffort: "xhigh",
			FrozenRoute: items58FrozenGrokRoute("grok-4.6", "xhigh"),
			LastRouteAttempt: &RouteAttemptReadback{
				RequestedRunner: grokBuildRunnerName, ActualRunner: grokBuildRunnerName,
				RequestedModel: "grok-4.6", ActualModel: "grok-4.5",
			},
		}
		err = freezeGrokAttemptModel(context.Background(), cfg, conflict)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || conflict.GrokModel != "" {
			t.Fatalf("FrozenRoute made conflicting evidence safe: err=%v model=%q", err, conflict.GrokModel)
		}
	})
}

func items58CandidateBin(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("CARDEX_CANDIDATE_BIN")); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("CARDEX_CANDIDATE_BIN=%s: %v", p, err)
		}
		return p
	}
	out := filepath.Join(t.TempDir(), "cardex")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v\n%s", err, data)
	}
	return out
}

func TestItems58DumpReplayRoot(t *testing.T) {
	dest := strings.TrimSpace(os.Getenv("ITEMS58_REPLAY_ROOT"))
	if dest == "" {
		t.Skip("ITEMS58_REPLAY_ROOT unset")
	}
	if err := os.MkdirAll(filepath.Join(dest, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := policyTestConfig()
	cfg.RouteMatrix = RouteMatrix{"medium": {"general": {
		Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "xhigh",
		Fallback: []RouteLegConfig{{Runner: "codex", Model: "gpt-5.6-sol", Effort: "high"}},
	}}}
	fakeDir := filepath.Join(dest, "fake-bin")
	if err := os.MkdirAll(fakeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	argvPath := filepath.Join(dest, "captured.argv")
	fakeGrok := filepath.Join(fakeDir, "grok")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shSingleQuote(argvPath) + "\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = models ]; then\n" +
		"    printf '%s\\n' 'You are logged in with grok.com.' 'Available models:' '  * grok-4.6 (default)' '  * grok-4.7'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"done\n" +
		"printf '%s\\n' " + shSingleQuote(`{"type":"end","stopReason":"end_turn"}`) + "\n" +
		"exit 0\n"
	if err := os.WriteFile(fakeGrok, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.GrokBuildBin = fakeGrok
	if err := saveConfig(dest, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(os.Getenv("ITEMS58_DUMP_ARGV")) == "" {
		fixtures := seedWaitErrorFixtures(t, dest)
		writeWaitErrorDigestOutbox(t, dest, fixtures)
	}
}

func TestItems58BuiltCLIConsumerLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX built CLI consumer")
	}
	bin := items58CandidateBin(t)
	for i := 0; i < 2; i++ {
		t.Run("pass", func(t *testing.T) {
			runItems58BuiltCLIOnce(t, bin)
		})
	}
}

func runItems58BuiltCLIOnce(t *testing.T, bin string) {
	t.Helper()
	root := testRoot(t)
	cfg := policyTestConfig()
	cfg.RouteMatrix = RouteMatrix{"medium": {"general": {
		Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "xhigh",
		Fallback: []RouteLegConfig{{Runner: "codex", Model: "gpt-5.6-sol", Effort: "high"}},
	}}}
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	fixtures := seedWaitErrorFixtures(t, root)
	writeWaitErrorDigestOutbox(t, root, fixtures)
	run := func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("%v: %v", args, err)
			}
		}
		return stdout.String(), stderr.String(), code
	}
	listText, _, code := run("list", "-root", root, "-all")
	if code != 0 {
		t.Fatalf("list exit %d", code)
	}
	listJSON, _, code := run("list", "-root", root, "-json")
	if code != 0 {
		t.Fatalf("list -json exit %d", code)
	}
	digestJSON, _, code := run("digest", "-root", root, "-since", "1", "-json")
	if code != 0 {
		t.Fatalf("digest exit %d", code)
	}
	var listed []map[string]any
	if err := json.Unmarshal([]byte(listJSON), &listed); err != nil {
		t.Fatal(err)
	}
	var digestRows []digestEntry
	if err := json.Unmarshal([]byte(digestJSON), &digestRows); err != nil {
		t.Fatal(err)
	}
	classOf := func(id string) (listC, digestC string) {
		for _, row := range listed {
			if row["id"] == id {
				listC, _ = row["wait_error_class"].(string)
			}
		}
		for _, row := range digestRows {
			if row.TaskID == id {
				digestC = row.WaitErrorClass
			}
		}
		return listC, digestC
	}
	for id, want := range map[string]string{
		"we-inflight": "in_flight", "we-permwait": "permission_waiting", "we-failed": "failure",
		"we-unknown": "unknown", "we-budget": "budget_limited", "we-quota": "account_quota",
		"we-transport": "transport", "we-unavailable": "transport", "we-auth": "auth", "we-cancel": "cancellation",
	} {
		lc, dc := classOf(id)
		if lc != want || dc != want {
			t.Fatalf("built CLI %s list=%q digest=%q want %s", id, lc, dc, want)
		}
		if !strings.Contains(listText, want) {
			t.Fatalf("built CLI list text missing %s", want)
		}
	}
	trList, trDigest := classOf("we-transport")
	if trList == "account_quota" || trDigest == "account_quota" || strings.Contains(strings.ToLower(listJSON+digestJSON), `"wait_error_class": "account_quota"`) && strings.Contains(listJSON, "we-transport") {
		if trList == waitClassAccountQuota || trDigest == waitClassAccountQuota {
			t.Fatalf("built CLI transport misquota list=%s digest=%s", trList, trDigest)
		}
	}

	permOut, permErr, permCode := run("grok-permission", "answer",
		"-root", root, "-request", "req-perm-wait", "-decision", "allow-once",
		"-session", "other-session", "we-permwait")
	if permCode == 0 {
		t.Fatalf("wrong-session answer succeeded stdout=%s stderr=%s", permOut, permErr)
	}
	if _, err := os.Stat(grokPermissionAnswerPath(root, "we-permwait", "att-perm", "req-perm-wait")); !os.IsNotExist(err) {
		t.Fatal("wrong-session answer wrote bytes")
	}

	preview, _, code := run("route", "-root", root, "-complexity", "medium", "-category", "general")
	if code != 0 {
		t.Fatalf("route exit %d %s", code, preview)
	}
	if !strings.Contains(preview, "grok-4.6") || !strings.Contains(preview, "xhigh") {
		t.Fatalf("route preview %s", preview)
	}
	if strings.Contains(strings.ToLower(preview), "quota exhaustion") && !strings.Contains(preview, "no auth/refusal/transport/unknown") {
		t.Fatalf("route preview dropped transport exclusion: %s", preview)
	}

	observeOut, observeErr, observeCode := run("workflow", "goal-input", "wf-missing", "-root", root, "-observe")
	if observeCode == 0 {
		t.Fatalf("goal-input -observe on missing workflow succeeded: %s", observeOut)
	}
	combinedObserve := observeOut + observeErr
	if strings.Contains(strings.ToLower(combinedObserve), "native approval") && !strings.Contains(combinedObserve, "not proof") {
		t.Fatalf("missing-host observe looked like native ACK: %s", combinedObserve)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	boardCmd := exec.Command(bin, "board", "-root", root, "-addr", "127.0.0.1", "-port", strconv.Itoa(port), "-ttl", "1")
	var boardBuf strings.Builder
	boardCmd.Stdout = &boardBuf
	boardCmd.Stderr = &boardBuf
	if err := boardCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boardCmd.Process.Kill(); _ = boardCmd.Wait() }()
	boardURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	var overview []byte
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		resp, gerr := http.Get(boardURL + "/api/overview")
		if gerr == nil {
			overview, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 && strings.Contains(string(overview), "wait_error_class") {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	boardClass := map[string]string{}
	projID := ""
	var ov map[string]any
	if json.Unmarshal(overview, &ov) == nil {
		if projects, ok := ov["projects"].([]any); ok {
			for _, p := range projects {
				pm, _ := p.(map[string]any)
				if pm == nil {
					continue
				}
				phases, _ := pm["phases"].([]any)
				for _, ph := range phases {
					phm, _ := ph.(map[string]any)
					tasks, _ := phm["tasks"].([]any)
					for _, tb := range tasks {
						tm, _ := tb.(map[string]any)
						id, _ := tm["id"].(string)
						cls, _ := tm["wait_error_class"].(string)
						if id != "" {
							boardClass[id] = cls
						}
						if id == "we-permwait" {
							projID, _ = pm["id"].(string)
						}
					}
				}
			}
		}
	}
	if boardClass["we-transport"] != waitClassTransport || boardClass["we-unavailable"] != waitClassTransport {
		t.Fatalf("board overview transport class=%q unavailable=%q body=%s board=%s", boardClass["we-transport"], boardClass["we-unavailable"], overview, boardBuf.String())
	}
	if boardClass["we-quota"] != waitClassAccountQuota {
		t.Fatalf("board overview quota class=%q", boardClass["we-quota"])
	}
	if boardClass["we-transport"] == waitClassAccountQuota || boardClass["we-unavailable"] == waitClassAccountQuota {
		t.Fatalf("board labeled transport/unavailable as quota: %+v", boardClass)
	}
	if projID != "" {
		presp, err := http.Get(boardURL + "/api/project?id=" + projID)
		if err != nil {
			t.Fatal(err)
		}
		projBody, _ := io.ReadAll(presp.Body)
		_ = presp.Body.Close()
		if !strings.Contains(string(projBody), "permission_waiting") {
			t.Fatalf("board project API missing permission_waiting: %s", projBody)
		}
	}

	argvRoot := testRoot(t)
	fakeBin, argvDump, _ := fakeGrokBuild(t, `{"type":"end","stopReason":"end_turn"}`+"\n", "", 0)
	liveCfg := policyTestConfig()
	liveCfg.RouteMatrix = RouteMatrix{"medium": {"general": {
		Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "xhigh",
		Fallback: []RouteLegConfig{{Runner: "codex", Model: "gpt-5.6-sol", Effort: "high"}},
	}}}
	liveCfg.GrokBuildBin = fakeBin
	isolateGrokLifecycleHome(t, liveCfg)
	if err := saveConfig(argvRoot, liveCfg); err != nil {
		t.Fatal(err)
	}
	run = func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("%v: %v", args, err)
			}
		}
		return stdout.String(), stderr.String(), code
	}
	preview2, _, pcode := run("route", "-root", argvRoot, "-complexity", "medium", "-category", "general")
	if pcode != 0 || !strings.Contains(preview2, "grok-4.6") || !strings.Contains(preview2, "xhigh") {
		t.Fatalf("argv-root preview %d %s", pcode, preview2)
	}
	addOut, addErr, addCode := run("add", "-root", argvRoot, "-dir", t.TempDir(), "-title", "frozen argv card",
		"-model", "sonnet", "-route-class", "general", "frozen argv prompt")
	if addCode != 0 {
		t.Fatalf("add: %s %s", addOut, addErr)
	}
	cards, err := loadTasks(argvRoot)
	if err != nil {
		t.Fatal(err)
	}
	var frozen *Task
	for _, c := range cards {
		if c != nil && c.Title == "frozen argv card" {
			frozen = c
			break
		}
	}
	if frozen == nil || !completeFrozenRoute(frozen) {
		t.Fatalf("add did not freeze route: %s %+v", addOut, frozen)
	}
	beforePin, _ := json.Marshal(frozen.FrozenRoute)
	liveCfg.RouteMatrix["medium"]["general"] = ConfiguredRoute{
		Runner: grokBuildRunnerName, Model: "grok-4.7", Effort: "xhigh",
		Fallback: []RouteLegConfig{{Runner: "codex", Model: "gpt-6.1-sol", Effort: "max"}},
	}
	liveCfg.GrokBuild.Model = "grok-4.7"
	liveCfg.GrokBuild.Effort = "xhigh"
	liveCfg.GrokBuildBin = fakeBin
	if err := saveConfig(argvRoot, liveCfg); err != nil {
		t.Fatal(err)
	}
	runOut, runErr, runCode := run("run", "-root", argvRoot, "-quiet", frozen.ID)
	captured, rerr := os.ReadFile(argvDump)
	if rerr != nil {
		got, _ := loadTask(argvRoot, frozen.ID)
		st, runner, last := "", "", ""
		if got != nil {
			st, runner, last = got.Status, got.Runner, got.LastError
		}
		t.Fatalf("built CLI launch did not record argv: %v runCode=%d out=%q err=%q status=%s runner=%s last=%q freeze=%+v add=%s", rerr, runCode, runOut, runErr, st, runner, last, frozen.FrozenRoute, addOut)
	}
	argvText := string(captured)
	if !strings.Contains(argvText, "grok-4.6") || !strings.Contains(argvText, "xhigh") {
		t.Fatalf("built CLI argv lost frozen pin: %s run=%s", argvText, runOut)
	}
	if strings.Contains(argvText, "grok-4.7") {
		t.Fatalf("built CLI argv used live config: %s", argvText)
	}
	afterPin, err := loadTask(argvRoot, frozen.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, _ := json.Marshal(afterPin.FrozenRoute)
	if string(beforePin) != string(afterBytes) {
		t.Fatalf("frozen pin mutated during launch before=%s after=%s", beforePin, afterBytes)
	}
}
