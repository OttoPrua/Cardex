package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
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

func saveGoalCfg(t *testing.T, root string, cfg *Config) {
	t.Helper()
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
}

func enableManualGrok(t *testing.T, root string, cfg *Config, argvPath string) *Config {
	t.Helper()
	cfg.GrokBuildBin = fakeGrokGoalBin(t, argvPath)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	return cfg
}

func fakeGrokGoalBin(t *testing.T, argvPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\n"
	if argvPath != "" {
		script += "printf '%s\\n' \"$@\" > " + shSingleQuote(argvPath) + "\n"
	}
	script += `for a in "$@"; do
  case "$a" in
    -p|--single|--prompt-file) echo "p-not-goal" >&2; exit 2;;
  esac
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeGrokGoalBinHold(t *testing.T, argvPath, holdPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\n"
	if argvPath != "" {
		script += "printf '%s\\n' \"$@\" > " + shSingleQuote(argvPath) + "\n"
	}
	script += "while [ -f " + shSingleQuote(holdPath) + " ]; do sleep 0.05; done\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func spyGrokBin(t *testing.T, stamp string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grok")
	script := "#!/bin/sh\necho launched > " + shSingleQuote(stamp) + "\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeAstraDesignReceipt(t *testing.T) string {
	t.Helper()
	return writeAstraDesignReceiptWith(t, nil)
}

func writeAstraDesignReceiptWith(t *testing.T, extra map[string]any) string {
	t.Helper()
	input := filepath.Join(t.TempDir(), "design-input.txt")
	body := []byte("D2+D3 goal-mode-20260912 independent design input\n")
	if err := os.WriteFile(input, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])
	ro := true
	rec := map[string]any{
		"role":           goalDesignRole,
		"model":          "gpt-6-astra",
		"runner":         "astra",
		"actual_model":   "gpt-6-astra",
		"actual_runner":  "astra",
		"identity":       "Astra",
		"read_only":      ro,
		"status":         "completed",
		"input_identity": hexSum,
		"inputs":         []map[string]string{{"path": input, "sha256": hexSum}},
	}
	for k, v := range extra {
		rec[k] = v
	}
	if _, supplied := rec["result_path"]; !supplied {
		result := filepath.Join(t.TempDir(), "design.md")
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(result, append([]byte("Synthetic independent design fixture for these inputs:\n"), body...), 0o644); err != nil {
			t.Fatal(err)
		}
		rec["result_path"] = result
	}
	path := filepath.Join(t.TempDir(), "astra-design.json")
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func bindTestDesign(t *testing.T, root string, cfg *Config, wf *WorkflowRecord) {
	t.Helper()
	if err := bindInitialDesignProof(wf, designBindRequest{ReceiptPath: writeAstraDesignReceipt(t)}); err != nil {
		t.Fatalf("bind design proof: %v", err)
	}
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
}

func writeGrokGoalFixture(t *testing.T, grokHome, cwd, sessionID, goalID, status string, st grokNativeGoalStateFile) {
	t.Helper()
	sessDir := grokGoalSessionDir(grokHome, cwd, sessionID)
	if err := os.MkdirAll(filepath.Join(sessDir, "goal"), 0o755); err != nil {
		t.Fatal(err)
	}
	st.GoalID = goalID
	st.Status = status
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "goal", "state.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := json.MarshalIndent(grokSessionSummaryFile{Info: grokSessionSummaryInfo{ID: sessionID, CWD: cwd}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "summary.json"), append(sum, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{
		"method": "_x.ai/session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate":           "goal_updated",
				"goal_id":                 goalID,
				"status":                  status,
				"phase":                   "idle",
				"last_classifier_verdict": st.LastClassifierVerdict,
				"last_event":              "goal_completed",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "updates.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedExitedAttempt(t *testing.T, root string, tk *Task, attemptID string) {
	t.Helper()
	rec := &AttemptRecord{
		TaskID:           tk.ID,
		AttemptID:        attemptID,
		ExpectedRevision: tk.Revision,
		ControlEpoch:     tk.ControlEpoch,
		State:            attemptExited,
		CreatedAt:        time.Now().Format(time.RFC3339Nano),
		UpdatedAt:        time.Now().Format(time.RFC3339Nano),
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
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	*tk = *loaded
}

const residualTimeoutNote = "hard timeout fired; native budget is soft; no guessed success"

func cloneTaskJSON(t *testing.T, src *Task) *Task {
	t.Helper()
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	var dst Task
	if err := json.Unmarshal(raw, &dst); err != nil {
		t.Fatal(err)
	}
	return &dst
}

// mappedRunningMutation is the pre-fix normal-sync write: native active is
// mapped to running and BoundAttemptID is copied back onto ActiveAttemptID.
func mappedRunningMutation(t *testing.T, src *Task) *Task {
	t.Helper()
	next := cloneTaskJSON(t, src)
	next.Status = statusRunning
	if next.Goal != nil {
		next.Goal.Observation = goalObsRunning
		next.Goal.ObservationNote = "matched native active"
		next.Goal.LastNativeStatus = "active"
		next.Goal.SyncedRevision = next.Revision
		if next.ActiveAttemptID == "" && next.Goal.BoundAttemptID != "" {
			next.ActiveAttemptID = next.Goal.BoundAttemptID
		}
	}
	return next
}

func seedResidualActiveTimeoutGoal(t *testing.T) (root, dir, grokHome string, cfg *Config, wf *WorkflowRecord, tk *Task, before []byte) {
	t.Helper()
	root, dir = workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	wf = initTestWorkflow(t, root, dir)
	tk = admitManualWriter(t, root, cfg, wf)
	sessionID := "cafecafe-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-residual-timeout"
	attemptID := "ate7264293c6fb7e1a"
	grokHome = t.TempDir()
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	tk.Goal.Started = true
	tk.Goal.GrokHome = grokHome
	seedExitedAttempt(t, root, tk, attemptID)
	tk.Status = statusHeld
	tk.Goal.Observation = goalObsUnknown
	tk.Goal.ObservationNote = residualTimeoutNote
	tk.Goal.CustodyReleased = true
	tk.ActiveAttemptID = ""
	revokeScheduling(tk)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	*tk = *loaded
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "active", grokNativeGoalStateFile{Phase: "Executing"})
	before, err = os.ReadFile(taskPath(root, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	return root, dir, grokHome, cfg, wf, tk, before
}

func captureGoalSyncCLI(t *testing.T, root, wfID string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	cmdErr := cmdWorkflowGoalSync([]string{"-root", root, wfID})
	_ = w.Close()
	os.Stdout = old
	raw, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(raw), cmdErr
}

func assertResidualTimeoutUnchanged(t *testing.T, root string, tk *Task, before []byte, wantRev int64) {
	t.Helper()
	if tk.Status != statusHeld || tk.Goal == nil || tk.Goal.Observation != goalObsUnknown {
		t.Fatalf("want held/unknown, status=%s obs=%v", tk.Status, tk.Goal)
	}
	if !strings.Contains(tk.Goal.ObservationNote, "hard timeout") {
		t.Fatalf("timeout provenance missing: %q", tk.Goal.ObservationNote)
	}
	if tk.Status == statusRunning || tk.Goal.Observation == goalObsRunning {
		t.Fatalf("revived to running: status=%s obs=%s", tk.Status, tk.Goal.Observation)
	}
	if tk.Status == statusDone || tk.Status == statusFailed || tk.Goal.Observation == goalObsDone || tk.Goal.Observation == goalObsFailed {
		t.Fatalf("accepted terminal: status=%s obs=%s", tk.Status, tk.Goal.Observation)
	}
	if tk.ActiveAttemptID != "" {
		t.Fatalf("ActiveAttemptID revived: %q", tk.ActiveAttemptID)
	}
	if tk.Goal.Continuation != "" {
		t.Fatalf("invented continuation: %q", tk.Goal.Continuation)
	}
	if tk.Revision != wantRev {
		t.Fatalf("unnecessary revision change: got %d want %d", tk.Revision, wantRev)
	}
	assertNoGoalEvDone(t, root, tk)
	if failed := goalEventsOfType(t, root, tk.ID, evFailed); len(failed) != 0 {
		t.Fatalf("invented native-failed: %+v", failed)
	}
	after, err := os.ReadFile(taskPath(root, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("task JSON bytes changed\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// launchExitedManualGoal drives the shipped manual launcher with a fake grok
// executable so bindAttemptProcess records a real PID/start identity. Tests
// that need accepted done/failed must use this instead of stuffing a PID.
var (
	headlessGoalStdinOnce sync.Once
	headlessGoalStdinFile *os.File
)

func headlessGoalStdin(t *testing.T) *os.File {
	t.Helper()
	headlessGoalStdinOnce.Do(func() {
		r, w, err := os.Pipe()
		if err != nil {
			panic(err)
		}
		headlessGoalStdinFile = r
		goalLaunchStdin = r
		_ = w
	})
	return headlessGoalStdinFile
}

func runManualGoalLaunch(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, budget int64) error {
	t.Helper()
	headlessGoalStdin(t)
	return launchManualWorkflowGoal(root, cfg, wf, budget, "")
}

func launchExitedManualGoal(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task, goalID string) *Task {
	t.Helper()
	enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv"))
	if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
		t.Fatalf("production launch: %v", err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	*tk = *loaded
	if tk.SessionID == "" || tk.Goal == nil || !tk.Goal.Started {
		t.Fatalf("production launch must bind session and start: session=%q goal=%+v", tk.SessionID, tk.Goal)
	}
	rec, err := loadRequiredGoalAttempt(root, tk)
	if err != nil || !goalAttemptHasStartIdentity(rec) {
		t.Fatalf("production start identity missing: rec=%+v err=%v", rec, err)
	}
	if goalID != "" {
		tk.Goal.NativeGoalID = goalID
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		loaded, err = loadTask(root, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		*tk = *loaded
	}
	return tk
}

func admitManualWriter(t *testing.T, root string, cfg *Config, wf *WorkflowRecord) *Task {
	t.Helper()
	if wf.DesignLineage == nil || wf.DesignLineage.LatestValid == nil {
		bindTestDesign(t, root, cfg, wf)
	}
	tk, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterManual)
	if err != nil {
		t.Fatalf("admit manual writer: %v", err)
	}
	if tk.Goal == nil || tk.Goal.WriterMode != goalWriterManual {
		t.Fatalf("manual writer must carry Goal binding: %+v", tk.Goal)
	}
	return tk
}

func captureWorkflowShow(t *testing.T, root, id string) map[string]any {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	cmdErr := cmdWorkflowShow([]string{"-root", root, id})
	_ = w.Close()
	os.Stdout = old
	raw, readErr := io.ReadAll(r)
	_ = r.Close()
	if cmdErr != nil {
		t.Fatalf("workflow show: %v\n%s", cmdErr, raw)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("show JSON: %v\n%s", err, raw)
	}
	return m
}

func TestOrdinaryCardsWithoutGoalFieldsRemainEligible(t *testing.T) {
	t.Parallel()
	now := time.Now()
	queued := &Task{Status: statusQueued, CreatedAt: now.Format(time.RFC3339)}
	applyControlDefaults(queued)
	if !eligible(queued, now) {
		t.Fatal("ordinary queued card without Goal must remain eligible")
	}
	running := &Task{Status: statusRunning, CreatedAt: now.Format(time.RFC3339)}
	applyControlDefaults(running)
	if !eligible(running, now) {
		t.Fatal("ordinary running card without Goal must remain eligible (crash recovery)")
	}
	paused := &Task{Status: statusLimitPaused, CreatedAt: now.Format(time.RFC3339)}
	applyControlDefaults(paused)
	if !eligible(paused, now) {
		t.Fatal("ordinary limit_paused card without Goal must remain eligible for auto-resume")
	}
	if pickNext(&Config{ResumeFirst: true}, []*Task{queued, running, paused}, now) == nil {
		t.Fatal("pickNext must still select an ordinary card")
	}
}

func TestManualActivePausedUnknownGoalExcludedFromEligibleAndAutoResume(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ordinary := &Task{ID: "ord", Status: statusQueued, Priority: 1, CreatedAt: now.Add(-time.Hour).Format(time.RFC3339)}
	applyControlDefaults(ordinary)

	cases := []*Task{
		{ID: "g-run", Status: statusRunning, Goal: &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsRunning}},
		{ID: "g-pause", Status: statusLimitPaused, Goal: &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsHeld, Continuation: "same-session"}},
		{ID: "g-unk", Status: statusHeld, Goal: &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsUnknown, Started: true}},
		{ID: "g-active-q", Status: statusQueued, Goal: &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsUnstarted}},
	}
	for _, g := range cases {
		applyControlDefaults(g)
		if eligible(g, now) {
			t.Fatalf("Goal task %s observation=%s must not be eligible(); 反例注入: eligible 里删掉 goalBlocksOrdinaryDispatch", g.ID, g.Goal.Observation)
		}
	}
	picked := pickNext(&Config{ResumeFirst: true, TypeOrder: []string{typeSequence}}, append(cases, ordinary), now)
	if picked == nil || picked.ID != ordinary.ID {
		t.Fatalf("pickNext must skip Goal cards and take the ordinary one, got %+v", picked)
	}
}

func TestDuplicateGoalWriterAdmissionRejected(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	if _, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterManual); err == nil || !errors.Is(err, errGoalDesignProof) {
		if _, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterManual); err != nil && !errors.Is(err, errGoalDesignProof) {
			// first call may fail on missing design; bind then retry below
		}
	}
	bindTestDesign(t, root, cfg, wf)
	if _, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterManual); err != nil {
		t.Fatal(err)
	}
	if _, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterManual); !errors.Is(err, errWorkflowDuplicateRole) {
		t.Fatalf("second manual writer must be duplicate: %v", err)
	}
	if _, err := admitWorkflowWriter(root, cfg, wf, ""); !errors.Is(err, errWorkflowDuplicateRole) {
		t.Fatalf("ordinary writer while Goal writer is live must be duplicate: %v", err)
	}
}

func TestNativeWriterRefusedWhenAutomaticUnproven(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	_, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterNative)
	if !errors.Is(err, errGoalCapability) {
		t.Fatalf("grok-build native automatic must be refused: %v", err)
	}
	cap := nativeGoalCapability(grokBuildRunnerName)
	if cap.Level == goalCapSupported {
		t.Fatal("Grok must not be marked supported automatic")
	}
}

func TestGoalWriterRequiresIndependentDesignProof(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	_, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterManual)
	if !errors.Is(err, errGoalDesignProof) {
		t.Fatalf("Goal writer without design proof: %v", err)
	}
	if err := cmdWorkflowInit([]string{
		"-root", root, "-mode", workflowModeSerial, "-module", "plain",
		"-goal", "ordinary", "-dir", dir, "-terminal-criteria", "pass",
		"-write-domain-id", "plain-core", "-write-domain-lineage", "plain-core-lineage",
		"-write-domain-component", "plain", "-write-paths", "internal/billing",
		"-engine", grokBuildRunnerName,
		"-design-model", "gpt-6-astra", "-design-runner", grokBuildRunnerName,
	}); !errors.Is(err, errGoalDesignProof) {
		t.Fatalf("string-only Astra/grok-build must not be design proof: %v", err)
	}
	ordinary, err := admitWorkflowWriter(root, cfg, wf, "")
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Goal != nil {
		t.Fatal("ordinary writer must remain Goal-less")
	}
}

func TestGoalWriteDomainsOverlapFailClosedAndDisjointDoNot(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	a := admitManualWriter(t, root, cfg, wf)

	billing := copyWriteDomain(wf.WriteDomain)
	billing.ID = "billing-core"
	billing.Lineage = "billing-core-lineage"
	billing.Component = "billing"
	billing.Paths = []string{"internal/billing"}
	b := newTask(root, cfg, typeSequence, "billing goal", dir, []string{"p"}, 5)
	b.Goal = &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsRunning}
	b.WriteDomain = billing
	applyControlDefaults(b)

	if writerConflictsWithActive(b, []*Task{a}) {
		t.Fatal("disjoint Goal write domains must not conflict")
	}

	overlap := newTask(root, cfg, typeSequence, "overlap goal", dir, []string{"p"}, 5)
	overlap.Goal = &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsRunning}
	overlap.WriteDomain = copyWriteDomain(wf.WriteDomain)
	applyControlDefaults(overlap)
	if !writerConflictsWithActive(overlap, []*Task{a}) {
		t.Fatal("overlapping Goal write domains must fail closed; 反例注入: writerClaimsConflict 忽略 Goal 卡")
	}
}

func TestGoalSyncIdempotentAndRejectsStaleIdentities(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeee0001"
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	seedExitedAttempt(t, root, tk, "att-live")
	grokHome := t.TempDir()
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "active", grokNativeGoalStateFile{Phase: "Executing"})

	req := GoalSyncRequest{
		ExpectedRevision: tk.Revision,
		ExpectedSession:  sessionID,
		ExpectedGoalID:   goalID,
		ExpectedAttempt:  "att-live",
		GrokHome:         grokHome,
		GrokCWD:          dir,
	}
	got, err := syncWorkflowGoal(root, cfg, wf, req)
	if err != nil {
		t.Fatalf("sync active: %v", err)
	}
	if got.Status != statusRunning || got.Goal.Observation != goalObsRunning {
		t.Fatalf("active must map to running, status=%s obs=%s", got.Status, got.Goal.Observation)
	}

	again, err := syncWorkflowGoal(root, cfg, wf, req)
	if err != nil {
		t.Fatalf("duplicate sync on same revision must be idempotent: %v", err)
	}
	if again.Status != statusRunning || again.Goal.Observation != goalObsRunning {
		t.Fatalf("idempotent sync changed outcome: %+v", again.Goal)
	}

	stale := req
	stale.ExpectedRevision = req.ExpectedRevision - 1
	if _, err := syncWorkflowGoal(root, cfg, wf, stale); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("stale revision must be rejected: %v", err)
	}
	otherSess := req
	otherSess.ExpectedSession = "bbbbbbbb-bbbb-4ccc-8ddd-ffffffffffff"
	if _, err := syncWorkflowGoal(root, cfg, wf, otherSess); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("other session must be rejected: %v", err)
	}
	otherGoal := req
	otherGoal.ExpectedGoalID = "goal-other"
	if _, err := syncWorkflowGoal(root, cfg, wf, otherGoal); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("other goal_id must be rejected: %v", err)
	}
	oldAtt := req
	oldAtt.ExpectedAttempt = "att-old"
	if _, err := syncWorkflowGoal(root, cfg, wf, oldAtt); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("old attempt must be rejected: %v", err)
	}
}

func TestD3GenuineAchievedZeroRoundsAcceptedWithAttempt(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	goalID := "real-goal"
	launchExitedManualGoal(t, root, cfg, wf, tk, goalID)
	sessionID := tk.SessionID
	grokHome := t.TempDir()
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "complete", grokNativeGoalStateFile{
		Phase:                 "Idle",
		LastClassifierVerdict: "achieved",
		TotalVerifyRounds:     0,
		History:               []grokGoalEvent{{Event: "goal_completed"}},
	})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
		ExpectedAttempt: tk.Goal.BoundAttemptID, GrokHome: grokHome, GrokCWD: dir,
	})
	if err != nil {
		t.Fatalf("genuine complete: %v", err)
	}
	if got.Status != statusDone || got.Goal.Observation != goalObsDone {
		t.Fatalf("genuine achieved+zero rounds with attempt+custody must be done: status=%s obs=%s note=%s",
			got.Status, got.Goal.Observation, got.Goal.ObservationNote)
	}
	if got.LastCommittedTransitionID == "" || !taskDurablyDone(root, got) {
		t.Fatalf("accepted done must be a durable transition; id=%q durablyDone=%v",
			got.LastCommittedTransitionID, taskDurablyDone(root, got))
	}
}

func TestD3MissingAttemptMustNotBecomeDone(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.SessionID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	tk.Goal.NativeGoalID = "goal-a"
	tk.Goal.BoundAttemptID = "nonexistent-attempt"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	tk, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, tk.SessionID, tk.Goal.NativeGoalID, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved", TotalVerifyRounds: 0,
	})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, GrokHome: home, GrokCWD: dir,
		ExpectedSession: tk.SessionID, ExpectedGoalID: tk.Goal.NativeGoalID,
		ExpectedAttempt: "nonexistent-attempt",
	})
	if err == nil && got != nil && got.Status == statusDone {
		t.Fatalf("missing producer attempt accepted done; taskDurablyDone=%v transition=%q",
			taskDurablyDone(root, got), got.LastCommittedTransitionID)
	}
	if got != nil && (got.LastCommittedTransitionID != "" && taskDurablyDone(root, got)) {
		t.Fatal("missing attempt must not be durably done")
	}
}

func TestD3CorruptTailMustInvalidateNativeEvidence(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	gid := "goal-a"
	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved", TotalVerifyRounds: 0,
	})
	f, err := os.OpenFile(filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{truncated\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	obs, err := observeNativeGrokGoal(home, cwd, sid, gid)
	if err == nil && mapNativeGoalToTask(obs, true, false, false).AcceptDone {
		t.Fatal("truncated native stream accepted completed")
	}
}

func TestD3InventedClassifierAliasesDoNotProveDone(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	gid := "goal-alias"
	sessDir := grokGoalSessionDir(home, cwd, sid)
	if err := os.MkdirAll(filepath.Join(sessDir, "goal"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := map[string]any{
		"goal_id": gid, "status": "completed",
		"classifier_verdict": "pass", "verdict": "pass", "review_verdict": "pass",
		"evidence_complete": true, "total_verify_rounds": 1,
	}
	raw, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(sessDir, "goal", "state.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := json.Marshal(grokSessionSummaryFile{Info: grokSessionSummaryInfo{ID: sid, CWD: cwd}})
	_ = os.WriteFile(filepath.Join(sessDir, "summary.json"), sum, 0o644)
	line, _ := json.Marshal(map[string]any{
		"params": map[string]any{
			"update":    map[string]any{"sessionUpdate": "goal_updated"},
			"sessionId": sid,
			"goal_id":   gid,
		},
	})
	_ = os.WriteFile(filepath.Join(sessDir, "updates.jsonl"), append(line, '\n'), 0o644)
	obs, err := observeNativeGrokGoal(home, cwd, sid, gid)
	if err == nil && mapNativeGoalToTask(obs, true, false, false).AcceptDone {
		t.Fatalf("invented classifier_verdict/evidence_complete/params.goal_id must not prove done: %+v", obs)
	}

	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	launchExitedManualGoal(t, root, cfg, wf, tk, gid)
	writeGrokGoalFixture(t, home, dir, tk.SessionID, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved", TotalVerifyRounds: 4,
	})
	// verify_rounds>0 is extra, not required; genuine last_classifier_verdict still proves
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: tk.SessionID, ExpectedGoalID: gid,
		ExpectedAttempt: tk.Goal.BoundAttemptID, GrokHome: home, GrokCWD: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusDone {
		t.Fatalf("total_verify_rounds>0 must not block genuine achieved, got %s %s", got.Status, got.Goal.ObservationNote)
	}
}

func TestMissingNativeGoalIDDoesNotAuthorizeSyncOrAcceptance(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "s1missng-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = "goal-stored"
	seedExitedAttempt(t, root, tk, "att-missing-goal")
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, sessionID, "", "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
	})
	sessDir := grokGoalSessionDir(home, dir, sessionID)
	line, err := json.Marshal(map[string]any{
		"method": "_x.ai/session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate":           "goal_updated",
				"goal_id":                 "goal-stored",
				"status":                  "complete",
				"last_classifier_verdict": "achieved",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "updates.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: "goal-stored",
		ExpectedAttempt: tk.Goal.BoundAttemptID, GrokHome: home, GrokCWD: dir,
	})
	if err == nil || !errors.Is(err, errGoalSyncRejected) || !strings.Contains(err.Error(), "missing native goal_id") {
		t.Fatalf("blank state goal_id synced: err=%v status=%v", err, statusOf(got))
	}
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status == statusDone || fresh.Goal == nil || fresh.Goal.NativeGoalID != "goal-stored" {
		t.Fatalf("missing native goal_id changed identity or accepted done: status=%s goal=%+v", fresh.Status, fresh.Goal)
	}

	if err := cmdWorkflowGoalDirection([]string{"-root", root, wf.ID, "-direction", "native-a"}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.NativeGoalTaskIDs) != 1 {
		t.Fatalf("native directions = %v", wf.NativeGoalTaskIDs)
	}
	native, err := loadTask(root, wf.NativeGoalTaskIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	native.Status = statusDone
	if native.Goal == nil || strings.TrimSpace(native.Goal.NativeGoalID) != "" {
		t.Fatalf("admitted native direction must still lack NativeGoalID: %+v", native.Goal)
	}
	if err := writeTaskFile(root, native); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err == nil || !strings.Contains(err.Error(), "native goal_id") {
		t.Fatalf("done native card without goal id created acceptance: %v", err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID != "" || wf.GoalCompleted {
		t.Fatalf("missing NativeGoalID accepted the goal: acceptance=%s completed=%v", wf.AcceptanceTaskID, wf.GoalCompleted)
	}
}

func statusOf(t *Task) string {
	if t == nil {
		return "<nil>"
	}
	return t.Status
}

func TestD3BudgetLimitedNotAchievedIsNotComplete(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "s1sealed-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "s1-sealed"
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	seedExitedAttempt(t, root, tk, "att-s1")
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, sessionID, goalID, "budget_limited", grokNativeGoalStateFile{
		LastClassifierVerdict: "not_achieved", TotalVerifyRounds: 0,
	})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
		ExpectedAttempt: "att-s1", GrokHome: home, GrokCWD: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == statusDone || got.Goal.Observation == goalObsDone {
		t.Fatalf("S1 budget_limited/not_achieved must not be complete: %+v", got.Goal)
	}
}

func TestGoalSyncMapsNativeStatesAndDeniesIncompleteTerminals(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	goalID := "goal-map-1"
	grokHome := t.TempDir()

	prep := func(status string, st grokNativeGoalStateFile, attempt string) *Task {
		t.Helper()
		tk := admitManualWriter(t, root, cfg, wf)
		launchExitedManualGoal(t, root, cfg, wf, tk, goalID)
		writeGrokGoalFixture(t, grokHome, dir, tk.SessionID, goalID, status, st)
		got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
			ExpectedRevision: tk.Revision, ExpectedSession: tk.SessionID,
			ExpectedGoalID: goalID, ExpectedAttempt: tk.Goal.BoundAttemptID,
			GrokHome: grokHome, GrokCWD: dir,
		})
		if err != nil {
			t.Fatalf("sync %s: %v", status, err)
		}
		return got
	}

	paused := prep("paused", grokNativeGoalStateFile{}, "att-pause")
	if paused.Status != statusHeld || paused.Goal.Continuation != "same-session" {
		t.Fatalf("paused must be held with same-session continuation: status=%s cont=%s", paused.Status, paused.Goal.Continuation)
	}
	if err := terminalize(root, paused.ID, statusCanceled, "test", "clear", nil); err != nil {
		t.Fatal(err)
	}

	needs := prep("needs-input", grokNativeGoalStateFile{}, "att-needs")
	if needs.Status != statusHeld || needs.Goal.Continuation != "same-session" {
		t.Fatalf("needs-input must be held continuation: %+v", needs.Goal)
	}
	_ = terminalize(root, needs.ID, statusCanceled, "test", "clear", nil)

	failed := prep("failed", grokNativeGoalStateFile{}, "att-fail")
	if failed.Status != statusFailed || failed.Goal.Observation != goalObsFailed {
		t.Fatalf("failed with custody must be failed: status=%s obs=%s note=%s", failed.Status, failed.Goal.Observation, failed.Goal.ObservationNote)
	}
	if failed.LastCommittedTransitionID == "" || !strings.Contains(failed.LastCommittedTransitionID, "") && failed.Status == statusFailed {
		if failed.LastCommittedTransitionID == "" {
			t.Fatal("failed must go through durable transition")
		}
	}

	done := prep("complete", grokNativeGoalStateFile{LastClassifierVerdict: "achieved"}, "att-done")
	if done.Status != statusDone || done.Goal.Observation != goalObsDone {
		t.Fatalf("complete+achieved+released must be done: status=%s obs=%s note=%s",
			done.Status, done.Goal.Observation, done.Goal.ObservationNote)
	}
	if !taskDurablyDone(root, done) {
		t.Fatal("done must be visible to taskDurablyDone")
	}
}

func TestGoalSyncCompletedLiveLeaseIsNotDone(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "dddddddd-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-live-lease"
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	seedExitedAttempt(t, root, tk, "att-lease")
	doneCh := make(chan struct{})
	markTaskLeaseResidue(tk.ID, doneCh)
	t.Cleanup(func() { close(doneCh) })

	grokHome := t.TempDir()
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
	})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
		ExpectedAttempt: "att-lease", GrokHome: grokHome, GrokCWD: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == statusDone || got.Goal.Observation == goalObsDone {
		t.Fatalf("native completed with live lease must not be done: status=%s obs=%s", got.Status, got.Goal.Observation)
	}
	if got.Goal.Observation != goalObsUnknown {
		t.Fatalf("want unknown observation, got %s (%s)", got.Goal.Observation, got.Goal.ObservationNote)
	}
}

func TestGoalSyncMissingWrongAndFailOpenStayHeldUnknown(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	sessionID := "eeeeeeee-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-unknown"
	grokHome := t.TempDir()

	syncOne := func(name string, mutate func(sessDir string, tk *Task)) *Task {
		t.Helper()
		tk := admitManualWriter(t, root, cfg, wf)
		tk.SessionID = sessionID
		tk.Goal.NativeGoalID = goalID
		seedExitedAttempt(t, root, tk, "att-"+name)
		writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "complete", grokNativeGoalStateFile{
			LastClassifierVerdict: "achieved",
		})
		mutate(grokGoalSessionDir(grokHome, dir, sessionID), tk)
		got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
			ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
			ExpectedAttempt: tk.Goal.BoundAttemptID, GrokHome: grokHome, GrokCWD: dir,
		})
		if err != nil && !errors.Is(err, errGoalSyncRejected) {
			t.Fatalf("%s: %v", name, err)
		}
		if errors.Is(err, errGoalSyncRejected) {
			return tk
		}
		if got.Status == statusDone {
			t.Fatalf("%s accepted done: %+v", name, got.Goal)
		}
		if got.Goal.Observation != goalObsUnknown && got.Status != statusHeld {
			t.Fatalf("%s want held unknown, status=%s obs=%s", name, got.Status, got.Goal.Observation)
		}
		_ = terminalize(root, got.ID, statusCanceled, "test", "clear", nil)
		return got
	}

	syncOne("missing-state", func(sessDir string, _ *Task) {
		_ = os.Remove(filepath.Join(sessDir, "goal", "state.json"))
	})
	syncOne("missing-updates", func(sessDir string, _ *Task) {
		_ = os.Remove(filepath.Join(sessDir, "updates.jsonl"))
	})
	syncOne("fail-open-no-classifier", func(sessDir string, _ *Task) {
		st := grokNativeGoalStateFile{GoalID: goalID, Status: "complete", TotalVerifyRounds: 0}
		raw, _ := json.Marshal(st)
		_ = os.WriteFile(filepath.Join(sessDir, "goal", "state.json"), raw, 0o644)
		line, _ := json.Marshal(map[string]any{
			"params": map[string]any{
				"sessionId": sessionID,
				"update":    map[string]any{"sessionUpdate": "goal_updated", "goal_id": goalID, "status": "complete"},
			},
		})
		_ = os.WriteFile(filepath.Join(sessDir, "updates.jsonl"), append(line, '\n'), 0o644)
	})
	tk := admitManualWriter(t, root, cfg, wf)
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	seedExitedAttempt(t, root, tk, "att-wrong")
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
	})
	line, _ := json.Marshal(map[string]any{
		"params": map[string]any{
			"sessionId": "ffffffff-ffff-4fff-8fff-ffffffffffff",
			"update":    map[string]any{"sessionUpdate": "goal_updated", "goal_id": goalID, "status": "complete", "last_classifier_verdict": "achieved"},
		},
	})
	_ = os.WriteFile(filepath.Join(grokGoalSessionDir(grokHome, dir, sessionID), "updates.jsonl"), append(line, '\n'), 0o644)
	if _, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
		ExpectedAttempt: "att-wrong", GrokHome: grokHome, GrokCWD: dir,
	}); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("wrong-session updates must be rejected: %v", err)
	}
}

func TestCancelThenLateNativeCompletedIsNotDone(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "abababab-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-cancel-late"
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	tk.Goal.CancelRequested = true
	tk.Goal.StopEvidence = false
	tk.Goal.Observation = goalObsUnknown
	tk.Status = statusHeld
	revokeScheduling(tk)
	seedExitedAttempt(t, root, tk, "att-c")
	grokHome := t.TempDir()
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
	})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
		ExpectedAttempt: "att-c", GrokHome: grokHome, GrokCWD: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == statusDone || got.Status == statusCanceled && got.Goal.StopEvidence {
		t.Fatalf("late native completed without stop evidence must not be done/canceled: status=%s obs=%s stop=%v",
			got.Status, got.Goal.Observation, got.Goal.StopEvidence)
	}
	if got.Goal.Observation != goalObsUnknown {
		t.Fatalf("want unknown, got %s (%s)", got.Goal.Observation, got.Goal.ObservationNote)
	}
}

func TestGoalSyncDoesNotStartProvider(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	stamp := filepath.Join(t.TempDir(), "stamp")
	cfg.GrokBuildBin = spyGrokBin(t, stamp)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "12121212-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-no-launch"
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	seedExitedAttempt(t, root, tk, "att-n")
	grokHome := t.TempDir()
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "active", grokNativeGoalStateFile{})
	if _, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
		GrokHome: grokHome, GrokCWD: dir,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stamp); !os.IsNotExist(err) {
		t.Fatal("goal-sync must not start a provider process")
	}
}

func TestManualGoalLaunchBindsSessionBeforeEffectAndOmitsDashP(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	argvPath := filepath.Join(t.TempDir(), "argv")
	enableManualGrok(t, root, cfg, argvPath)
	wf := initTestWorkflow(t, root, dir)
	admitManualWriter(t, root, cfg, wf)

	withSchedulerLock(t, root)
	if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
		t.Fatalf("goal-run: %v", err)
	}
	tk, err := loadTask(root, wf.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.SessionID == "" {
		t.Fatal("session must be bound before launch")
	}
	if tk.GrokModel != "grok-4.6" {
		t.Fatalf("model must be frozen before launch, got %q", tk.GrokModel)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := string(raw)
	if strings.Contains(argv, "\n-p\n") || strings.Contains(argv, "--single") || strings.Contains(argv, "--prompt-file") {
		t.Fatalf("-p/--single/--prompt-file is not goal proof: %s", argv)
	}
	if !strings.Contains(argv, "-s\n"+tk.SessionID+"\n") && !strings.Contains(argv, "--resume\n"+tk.SessionID+"\n") {
		t.Fatalf("manual launch argv missing bound session %s:\n%s", tk.SessionID, argv)
	}
	if !strings.Contains(argv, "--model\ngrok-4.6\n") {
		t.Fatalf("manual launch argv missing frozen model:\n%s", argv)
	}
	if tk.Goal.Started && tk.Goal.Observation == goalObsUnstarted {
		t.Fatal("started launch must not remain unstarted")
	}
}

func TestManualGoalLaunchFreezesCatalogBeforeSessionArgv(t *testing.T) {
	t.Parallel()
	const want = "grok-4.7"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(want))
	bin, productArgs, modelsArgs := newGrokCatalogFake(t, catalog, 0, false)
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	cfg.GrokBuildBin = bin
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: grokStableSelector, Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	admitManualWriter(t, root, cfg, wf)
	withSchedulerLock(t, root)
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); err != nil {
		t.Fatalf("goal-run: %v", err)
	}
	tk, err := loadTask(root, wf.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.SessionID == "" || tk.GrokModel != want {
		t.Fatalf("session=%q model=%q", tk.SessionID, tk.GrokModel)
	}
	if countFileLines(modelsArgs+".calls") != 1 {
		t.Fatalf("catalog probe count=%d, want one probe before session bind", countFileLines(modelsArgs+".calls"))
	}
	raw, err := os.ReadFile(productArgs)
	if err != nil {
		t.Fatal(err)
	}
	argv := string(raw)
	if !strings.Contains(argv, "-s\n"+tk.SessionID+"\n") && !strings.Contains(argv, "--resume\n"+tk.SessionID+"\n") {
		t.Fatalf("stable launch argv missing bound session %s:\n%s", tk.SessionID, argv)
	}
	if !strings.Contains(argv, "--model\n"+want+"\n") || strings.Contains(argv, "--model\n"+grokStableSelector+"\n") {
		t.Fatalf("stable launch argv model:\n%s", argv)
	}
}

func TestManualGrokArgvPreservesPermissionTupleAndStageDigest(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	argvPath := filepath.Join(t.TempDir(), "argv")
	enableManualGrok(t, root, cfg, argvPath)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	if err := runManualGoalLaunch(t, root, cfg, wf, 350000); err != nil {
		t.Fatalf("goal-run: %v", err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := "\n" + string(raw)
	for _, want := range []string{"--sandbox", "--permission-mode", "--no-memory", "--no-subagents", "--disable-web-search"} {
		if !strings.Contains(argv, "\n"+want+"\n") && !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in argv:\n%s", want, raw)
		}
	}
	if !strings.Contains(argv, "workspace") || !strings.Contains(argv, "auto") {
		t.Fatalf("write-capable tuple must keep workspace/auto: %s", raw)
	}
	if !strings.Contains(string(raw), "--cwd") {
		t.Fatalf("manual launch must pass --cwd: %s", raw)
	}
	tk, err = loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(tk.Goal.InputDigest) == "" {
		t.Fatal("stage contract digest must be retained")
	}
	contractPath := filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt")
	body, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"outcome:", "acceptance:", "prompts:", "owned_paths:", "depends_on:", "stop:", "return_to_design:", "scope:", "budget_tokens:", "hard_timeout_seconds:", "latest_design_path:", "latest_design_digest:", "latest_design_input_identity:", "latest_design_decision:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stage contract missing %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "$(cat") {
		t.Fatal("frozen contract must not tell the TUI to use shell substitution")
	}
}

func TestCrashBeforeStartUnstartedCrashAfterStartUnknownBlocksRedispatch(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv"))
	wf := initTestWorkflow(t, root, dir)
	admitManualWriter(t, root, cfg, wf)
	withSchedulerLock(t, root)

	goalLaunchBeforeStartHook = func() error { return errAdmissionDenied }
	t.Cleanup(func() { goalLaunchBeforeStartHook = nil })
	err := runManualGoalLaunch(t, root, cfg, wf, 0)
	if !errors.Is(err, errGoalLaunchUnstarted) {
		t.Fatalf("crash before start: %v", err)
	}
	tk, err := loadTask(root, wf.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Goal.Started || tk.Goal.Observation != goalObsUnstarted {
		t.Fatalf("crash before start must stay unstarted: started=%v obs=%s", tk.Goal.Started, tk.Goal.Observation)
	}
	if _, err := admitWorkflowWriter(root, cfg, wf, ""); !errors.Is(err, errWorkflowDuplicateRole) {
		t.Fatalf("restart must not duplicate the writer: %v", err)
	}
	goalLaunchBeforeStartHook = nil
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); err != nil {
		t.Fatalf("unstarted must allow retry on the same writer: %v", err)
	}
	started, err := loadTask(root, wf.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !started.Goal.Started || started.Goal.Observation != goalObsUnknown {
		t.Fatalf("after start the goal is unknown until sync: started=%v obs=%s", started.Goal.Started, started.Goal.Observation)
	}
	if eligible(started, time.Now()) {
		t.Fatal("crash-after-start unknown must not be eligible")
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); !errors.Is(err, errGoalRedispatchBlocked) {
		t.Fatalf("crash after start must block redispatch: %v", err)
	}
	if _, err := admitWorkflowWriter(root, cfg, wf, ""); !errors.Is(err, errWorkflowDuplicateRole) {
		t.Fatalf("restart must not duplicate writer after crash-after-start: %v", err)
	}
}

func TestSameSessionResumePersistsControlEpochBeforeReserve(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv"))
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); err != nil {
		t.Fatal(err)
	}
	tk, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := tk.SessionID
	goalID := "goal-resume"
	tk.Goal.NativeGoalID = goalID
	tk.Goal.Observation = goalObsHeld
	tk.Goal.Continuation = "same-session"
	tk.Status = statusHeld
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	epochBefore, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); err != nil {
		t.Fatalf("same-session resume: %v", err)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SessionID != session {
		t.Fatalf("same-session UUID must be kept: %s vs %s", session, after.SessionID)
	}
	if after.Goal.NativeGoalID != goalID {
		t.Fatalf("goal id must be kept, got %s", after.Goal.NativeGoalID)
	}
	if after.ControlEpoch <= epochBefore.ControlEpoch {
		t.Fatalf("resume must persist a control-epoch transition before reserve: before=%d after=%d",
			epochBefore.ControlEpoch, after.ControlEpoch)
	}
}

func TestGoalWriterPublishedAtomicallyAgainstConcurrentConsumers(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	bindTestDesign(t, root, cfg, wf)
	wfID := wf.ID

	var mu sync.Mutex
	var violation string
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			tasks, err := loadTasks(root)
			if err != nil {
				continue
			}
			for _, tk := range tasks {
				if tk == nil || tk.WorkflowID != wfID || tk.IntegrationGate != nil {
					continue
				}
				if tk.Type == typeSequence && tk.Goal == nil {
					mu.Lock()
					violation = fmt.Sprintf("ordinary queued Goal-less card visible: %s status=%s", tk.ID, tk.Status)
					mu.Unlock()
					return
				}
			}
		}
	}()
	goalWriterPublishHook = func(tk *Task) {
		if tk == nil || tk.Goal == nil {
			mu.Lock()
			violation = "publish hook saw Goal-less writer"
			mu.Unlock()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { goalWriterPublishHook = nil })
	tk, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterManual)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if tk.Goal == nil {
		t.Fatal("first persisted writer must already carry Goal binding")
	}
	mu.Lock()
	v := violation
	mu.Unlock()
	if v != "" {
		t.Fatal(v)
	}
	disk, err := loadTask(root, tk.ID)
	if err != nil || disk.Goal == nil {
		t.Fatalf("disk task missing Goal: %v %+v", err, disk)
	}
}

func initNamedWorkflow(t *testing.T, root, dir, module, paths string) *WorkflowRecord {
	t.Helper()
	if err := cmdWorkflowInit([]string{
		"-root", root, "-mode", workflowModeSerial, "-module", module,
		"-goal-id", module + "-v1", "-goal", "ship " + module,
		"-dir", dir, "-terminal-criteria", "pass",
		"-write-domain-id", module + "-core", "-write-domain-lineage", module + "-core-lineage",
		"-write-domain-component", module, "-write-paths", paths,
		"-engine", grokBuildRunnerName, "-max-rounds", "2",
		"-design-receipt", writeAstraDesignReceipt(t),
	}); err != nil {
		t.Fatalf("init %s: %v", module, err)
	}
	cfg := workflowTestCfg(t, root)
	wfs, err := loadWorkflows(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, wf := range wfs {
		if wf.ModuleID == module {
			return wf
		}
	}
	t.Fatalf("workflow %s not found", module)
	return nil
}

func extraWorkflowDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "internal", "auth", "token.go"), "package auth\n")
	mustWriteFile(t, filepath.Join(dir, "internal", "billing", "bill.go"), "package billing\n")
	workflowGit(t, dir, "init", "-q")
	workflowGit(t, dir, "add", "-A")
	workflowGit(t, dir, "commit", "-q", "-m", "fixture")
	return dir
}

func TestGoalRunDisjointOverlapAndMissingPredecessor(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	cfg.MaxParallel = 2
	hold := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(hold, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(hold)
	argvA := filepath.Join(t.TempDir(), "argv-a")
	argvB := filepath.Join(t.TempDir(), "argv-b")
	cfg.GrokBuildBin = fakeGrokGoalBinHold(t, argvA, hold)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh", MaxParallel: 8}
	saveGoalCfg(t, root, cfg)

	auth := initNamedWorkflow(t, root, dir, "authz", "internal/auth")
	bill := initNamedWorkflow(t, root, extraWorkflowDir(t), "billing", "internal/billing")
	if _, err := admitWorkflowWriterMode(root, cfg, auth, "", goalWriterManual); err != nil {
		t.Fatal(err)
	}
	if _, err := admitWorkflowWriterMode(root, cfg, bill, "", goalWriterManual); err != nil {
		t.Fatal(err)
	}

	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errA = runManualGoalLaunch(t, root, cfg, auth, 0)
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(argvA); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Remove(hold)
			wg.Wait()
			t.Fatalf("first launch did not start: %v", errA)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cfgB := *cfg
	cfgB.GrokBuildBin = fakeGrokGoalBinHold(t, argvB, hold)
	wg.Add(1)
	go func() {
		defer wg.Done()
		errB = runManualGoalLaunch(t, root, &cfgB, bill, 0)
	}()
	deadline = time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(argvB); err == nil {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	os.Remove(hold)
	wg.Wait()
	if errA != nil {
		t.Fatalf("disjoint A: %v", errA)
	}
	if errB != nil {
		t.Fatalf("disjoint B within max_parallel must launch: %v", errB)
	}

	root2, dir2 := workflowTestRoot(t)
	cfg2 := workflowTestCfg(t, root2)
	cfg2.MaxParallel = 2
	hold2 := filepath.Join(t.TempDir(), "hold2")
	_ = os.WriteFile(hold2, []byte("1"), 0o644)
	defer os.Remove(hold2)
	argvC := filepath.Join(t.TempDir(), "argv-c")
	cfg2.GrokBuildBin = fakeGrokGoalBinHold(t, argvC, hold2)
	cfg2.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root2, cfg2)
	w1 := initNamedWorkflow(t, root2, dir2, "authz", "internal/auth")
	w2 := initNamedWorkflow(t, root2, dir2, "authz2", "internal/billing")
	if _, err := admitWorkflowWriterMode(root2, cfg2, w1, "", goalWriterManual); err != nil {
		t.Fatal(err)
	}
	tk2, err := admitWorkflowWriterMode(root2, cfg2, w2, "", goalWriterManual)
	if err != nil {
		t.Fatal(err)
	}
	w1t, err := loadTask(root2, w1.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if w1t.WriteDomain == nil {
		t.Fatal("first writer missing write domain")
	}
	tk2.WriteDomain = copyWriteDomain(*w1t.WriteDomain)
	if err := saveTask(root2, tk2); err != nil {
		t.Fatal(err)
	}
	var firstErr error
	done := make(chan struct{})
	go func() {
		firstErr = runManualGoalLaunch(t, root2, cfg2, w1, 0)
		close(done)
	}()
	deadline = time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(argvC); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Remove(hold2)
			<-done
			t.Fatalf("overlap first launch did not start: %v", firstErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	overlapErr := runManualGoalLaunch(t, root2, cfg2, w2, 0)
	os.Remove(hold2)
	<-done
	if overlapErr == nil {
		t.Fatal("overlapping write domain must not launch")
	}

	root3, dir3 := workflowTestRoot(t)
	cfg3 := workflowTestCfg(t, root3)
	enableManualGrok(t, root3, cfg3, filepath.Join(t.TempDir(), "argv-dep"))
	wf3 := initNamedWorkflow(t, root3, dir3, "authz", "internal/auth")
	pred := newTask(root3, cfg3, typeSequence, "pred", dir3, []string{"p"}, 5)
	pred.Status = statusQueued
	applyControlDefaults(pred)
	if err := saveTask(root3, pred); err != nil {
		t.Fatal(err)
	}
	tk3, err := admitWorkflowWriterMode(root3, cfg3, wf3, "", goalWriterManual)
	if err != nil {
		t.Fatal(err)
	}
	tk3.DependsOn = []string{pred.ID}
	if err := saveTask(root3, tk3); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root3, cfg3, wf3, 0); err == nil {
		t.Fatal("missing DAG predecessor must not launch")
	}
}

func TestFreshDesignNodeConsumesCandidateAndUsesWorkflowRounds(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	if err := cmdWorkflowInit([]string{
		"-root", root, "-mode", workflowModeSerial, "-module", "authz",
		"-goal-id", "authz-v1", "-select-goal", "ship authz",
		"-dir", dir, "-terminal-criteria", "pass",
		"-write-domain-id", "authz-tokens", "-write-domain-lineage", "authz-tokens-lineage",
		"-write-domain-component", "authz", "-write-paths", "internal/auth",
		"-engine", grokBuildRunnerName, "-max-rounds", "2",
		"-design-receipt", writeAstraDesignReceipt(t),
	}); err != nil {
		t.Fatal(err)
	}
	wfs, err := loadWorkflows(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var wf *WorkflowRecord
	for _, w := range wfs {
		if w.ModuleID == "authz" {
			wf = w
		}
	}
	if wf == nil || wf.DesignLineage == nil || wf.DesignLineage.Initial == nil {
		t.Fatal("init must bind the initial read-only design node")
	}
	if !wf.DesignLineage.Initial.ReadOnly || wf.DesignLineage.Initial.Role != goalDesignRole {
		t.Fatalf("design role must be read-only: %+v", wf.DesignLineage.Initial)
	}
	if wf.DesignLineage.Initial.ActualModel != "gpt-6-astra" {
		t.Fatalf("Astra independent design must be recorded, got %q", wf.DesignLineage.Initial.ActualModel)
	}

	wf, _ = runWorkflowToReview(t, root, wf, verdictJSON("concerns", nil, []string{"tighten"}))
	if err := ingestWorkflowReview(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	repairReceipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_commit": wf.Candidate.Commit,
		"consumed_tree":   wf.Candidate.Tree,
	})
	if err := bindWorkflowDesignRepair(root, cfg, wf, repairReceipt); err != nil {
		t.Fatal(err)
	}
	if wf.DesignLineage.RepairCount != 1 {
		t.Fatalf("repair count=%d", wf.DesignLineage.RepairCount)
	}
	latest := wf.DesignLineage.LatestValid
	if latest.ConsumedCommit != wf.Candidate.Commit || latest.ConsumedTree != wf.Candidate.Tree || latest.ConsumedReviewTask != wf.Review.TaskID {
		t.Fatalf("fresh design node must consume current candidate/results: %+v candidate=%+v review=%+v", latest, wf.Candidate, wf.Review)
	}
	if err := bindWorkflowDesignRepair(root, cfg, wf, repairReceipt); err != nil || wf.DesignLineage.RepairCount != 1 {
		t.Fatalf("replayed design bind must be a no-op: %v", err)
	}
	second := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_commit": wf.Candidate.Commit, "consumed_tree": wf.Candidate.Tree,
		"actual_model": "another-model", "identity": "independent-designer-2",
	})
	if err := bindWorkflowDesignRepair(root, cfg, wf, second); err != nil || wf.DesignLineage.RepairCount != 2 {
		t.Fatalf("second fresh design must use existing workflow limits, not a global once limit: %v", err)
	}
	wf.CurrentRound = wf.MaxRounds
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	if err := bindWorkflowDesignRepair(root, cfg, wf, second); !errors.Is(err, errWorkflowRoundsExceeded) {
		t.Fatalf("workflow round limit must remain hard: %v", err)
	}
}

func TestDesignResultConsumesNonacceptedAndRefusesDuplicate(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.Observation = goalObsUnknown
	tk.Goal.LastNativeStatus = "budget_limited"
	tk.Goal.ClassifierVerdict = "not_achieved"
	tk.Goal.ObservationNote = "budget_limited/not_achieved is a nonaccepted provider result"
	tk.Status = statusHeld
	revokeScheduling(tk)
	seedExitedAttempt(t, root, tk, "att-budget")

	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     tk.ID,
		"consumed_session_id":  tk.SessionID,
		"consumed_attempt_id":  tk.Goal.BoundAttemptID,
		"consumed_revision":    tk.Revision,
		"consumed_observation": "budget_limited",
	})
	next, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt)
	if err != nil {
		t.Fatalf("successor from budget_limited: %v", err)
	}
	if next == nil || next.Goal == nil {
		t.Fatal("successor must be Goal-bound")
	}
	if next.ID == tk.ID {
		t.Fatal("successor must be a new writer")
	}
	if next.Status == statusDone {
		t.Fatal("successor must not be manufactured done")
	}
	prev, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.Status == statusDone {
		t.Fatal("nonaccepted stage must not become done")
	}
	if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt); !errors.Is(err, errGoalDesignResult) {
		t.Fatalf("duplicate design result: %v", err)
	}

	root2, dir2 := workflowTestRoot(t)
	cfg2 := workflowTestCfg(t, root2)
	wf2 := initTestWorkflow(t, root2, dir2)
	paused := admitManualWriter(t, root2, cfg2, wf2)
	paused.Goal.Observation = goalObsHeld
	paused.Goal.Continuation = "same-session"
	paused.Status = statusHeld
	seedExitedAttempt(t, root2, paused, "att-p")
	pausedReceipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     paused.ID,
		"consumed_session_id":  paused.SessionID,
		"consumed_attempt_id":  paused.Goal.BoundAttemptID,
		"consumed_revision":    paused.Revision,
		"consumed_observation": "paused",
	})
	if _, err := applyWorkflowDesignResult(root2, cfg2, wf2, goalDecisionSuccessor, "paused", pausedReceipt); err == nil {
		t.Fatal("paused successor must be refused")
	}
	if _, err := applyWorkflowDesignResult(root2, cfg2, wf2, goalDecisionInput, "paused", pausedReceipt); err != nil {
		t.Fatalf("paused input: %v", err)
	}
	if _, err := applyWorkflowDesignResult(root2, cfg2, wf2, goalDecisionStop, "failed", pausedReceipt); err == nil {
		t.Fatal("mismatched observation must be refused")
	}
}

func TestNativeGoalCapabilityMatrixHonest(t *testing.T) {
	t.Parallel()
	caps := nativeGoalCapabilities()
	by := map[string]GoalCapability{}
	for _, c := range caps {
		by[c.Runner] = c
	}
	g := by[grokBuildRunnerName]
	if g.Level != goalCapManualOnly || !g.Verified || g.Reason == "" {
		t.Fatalf("Grok must be manual-only with verification/reason: %+v", g)
	}
	if !strings.Contains(g.Reason, "-p") {
		t.Fatal("Grok reason must disclose that -p is not goal proof")
	}
	low := strings.ToLower(g.Reason)
	if !strings.Contains(low, "complete") || !strings.Contains(low, "restart") {
		t.Fatalf("Grok reason must disclose proven complete+restart: %s", g.Reason)
	}
	if !strings.Contains(low, "pause") {
		t.Fatalf("Grok reason must disclose pause control limits: %s", g.Reason)
	}
	if !strings.Contains(low, "post-state") && !strings.Contains(low, "not pause") {
		t.Fatalf("Grok reason must not treat write/exit0 as pause: %s", g.Reason)
	}
	for _, name := range []string{kimiCLIRunnerName, "codex", "claude", cursorRunnerName, antigravityRunnerName, "opencode"} {
		c, ok := by[name]
		if !ok {
			t.Fatalf("%s must be present in the capability matrix", name)
		}
		if c.Level == goalCapSupported {
			t.Fatalf("%s must not be supported for unproven headless ongoing-goal: %+v", name, c)
		}
		if c.Level == goalCapUnsupported && !c.Rejected {
			t.Fatalf("%s native candidate must not be blanket unsupported: %+v", name, c)
		}
		if c.Reason == "" {
			t.Fatalf("%s needs a reason", name)
		}
	}
	if !strings.Contains(by[kimiCLIRunnerName].Reason, "goal.summary") {
		t.Fatalf("Kimi must disclose source-level goal.summary: %s", by[kimiCLIRunnerName].Reason)
	}
	if !by["gemini"].Rejected {
		t.Fatal("Gemini must stay rejected")
	}
	eng := nativeGoalCapability("kimi")
	if eng.Level == goalCapSupported {
		t.Fatalf("engine profiles must inherit executable limits, not fake supported: %+v", eng)
	}
}

func TestWorkflowShowReportsBoundGoalIdentity(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.SessionID = "99999999-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	tk.Goal.NativeGoalID = "goal-show"
	tk.Goal.BoundAttemptID = "att-show"
	tk.Goal.Observation = goalObsRunning
	tk.Status = statusRunning
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	writer, _ := buildWorkflowShowView(root, wf)
	if writer == nil {
		t.Fatal("show must project the writer")
	}
	if writer.SessionID != tk.SessionID || writer.GoalID != "goal-show" ||
		writer.AttemptID != "att-show" || writer.Status != statusRunning {
		t.Fatalf("show must print actual bound session/goal/attempt/status, got %+v", writer)
	}
	if writer.SessionID == "" || writer.GoalID == "pending" {
		t.Fatal("placeholder identities are not truthful")
	}
}

func TestOrdinaryWorkflowShowJSONTopLevelKeys(t *testing.T) {
	// Captures process-wide stdout; run before the parallel test group.
	root, dir := workflowTestRoot(t)
	wf := initTestWorkflow(t, root, dir)
	first := captureWorkflowShow(t, root, wf.ID)
	second := captureWorkflowShow(t, root, wf.ID)
	for i, m := range []map[string]any{first, second} {
		for _, key := range []string{"id", "goal", "status"} {
			if _, ok := m[key]; !ok {
				t.Fatalf("run %d missing top-level %s: %#v", i+1, key, m)
			}
		}
		if nested, ok := m["workflow"].(map[string]any); ok {
			if _, hasID := m["id"]; !hasID {
				t.Fatalf("wrapper-only shape: %#v", nested)
			}
		}
	}
	if first["id"] != second["id"] || first["goal"] != second["goal"] || first["status"] != second["status"] {
		t.Fatalf("show JSON must be stable: %#v vs %#v", first, second)
	}
}

// preFixNativeStatusMatchesControl is an immutable copy of HEAD stop matching
// (complete|failed|term only). budget_limited is excluded.
func preFixNativeStatusMatchesControl(action, native string) bool {
	st := normalizeNativeStatus(native)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case goalControlPause:
		return st == "paused" || st == "user_paused" || st == "needs-input"
	case goalControlResume:
		return st == "active"
	case goalControlStop:
		return st == "complete" || st == "failed" || st == "term"
	case goalControlStatus:
		return st != ""
	default:
		return false
	}
}

func TestPrefixtureStopMatcherExcludedBudgetLimited(t *testing.T) {
	t.Parallel()
	if preFixNativeStatusMatchesControl(goalControlStop, "budget_limited") {
		t.Fatal("pre-fix matcher treated budget_limited as complete/failed/term")
	}
	if !nativeStatusMatchesControl(goalControlStop, "budget_limited") {
		t.Fatal("current matcher must confirm budget_limited without aliasing it to complete")
	}
	if nativeStatusMatchesControl(goalControlStop, "active") || nativeStatusMatchesControl(goalControlStop, "unknown") {
		t.Fatal("active/unknown must still reject stop")
	}
	mapped := mapNativeGoalToTask(nativeGoalObservation{
		GoalID: "g1", NativeStatus: "budget_limited", BudgetLimited: true, UpdatesOK: true, SummaryOK: true,
	}, true, false, false)
	if mapped.AcceptDone || mapped.Observation == goalObsDone {
		t.Fatalf("budget_limited must stay nonaccepted: %+v", mapped)
	}
}

func TestConfirmHostedStopBudgetLimitedWithoutPTY(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	home := t.TempDir()
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	gid := "goal-budget-idle"
	writeGrokGoalFixture(t, home, dir, sid, gid, "budget_limited", grokNativeGoalStateFile{
		LastClassifierVerdict: "not_achieved",
		Phase:                 "Idle",
	})
	tk := &Task{
		ID:              "t-budget-stop",
		Dir:             dir,
		SessionID:       sid,
		ActiveAttemptID: "att-budget",
		Goal:            &TaskGoalBinding{NativeGoalID: gid, BoundAttemptID: "att-budget", LastNativeStatus: "budget_limited"},
	}
	if !waitHostedControlConfirmed(root, tk, home, goalControlStop, "/goal clear", time.Second) {
		t.Fatal("native budget_limited/Idle must confirm stop")
	}
	st, err := loadGoalHostStatus(root, tk.ID, "att-budget")
	if err != nil {
		t.Fatal(err)
	}
	if !st.ControlConfirmed || st.NativeStatus != "budget_limited" {
		t.Fatalf("stop confirmable without mapping to complete: %+v", st)
	}
	mapped := mapNativeGoalToTask(nativeGoalObservation{
		GoalID: gid, NativeStatus: "budget_limited", BudgetLimited: true, UpdatesOK: true, SummaryOK: true,
	}, true, false, false)
	if mapped.AcceptDone || mapped.Observation == goalObsDone {
		t.Fatalf("confirmed budget_limited stop is not accepted done: %+v", mapped)
	}

	writeGrokGoalFixture(t, home, dir, sid, gid, "active", grokNativeGoalStateFile{Phase: "Executing"})
	tk2 := &Task{
		ID:              "t-bytes-only",
		Dir:             dir,
		SessionID:       sid,
		ActiveAttemptID: "att-bytes",
		Goal:            &TaskGoalBinding{NativeGoalID: gid, BoundAttemptID: "att-bytes"},
	}
	confirmHostedControl(root, tk2, home, goalControlStop, "/goal clear")
	st2, err := loadGoalHostStatus(root, tk2.ID, "att-bytes")
	if err != nil {
		t.Fatal(err)
	}
	if st2.ControlConfirmed {
		t.Fatal("injecting /goal clear bytes against active is not stop confirmation")
	}
	if st2.FailureClass != goalFailPauseNotConfirmed {
		t.Fatalf("bytes-only want pause_not_confirmed, got %+v", st2)
	}

	writeGrokGoalFixture(t, home, dir, sid, gid, "budget_limited", grokNativeGoalStateFile{
		LastClassifierVerdict: "not_achieved",
		Phase:                 "Executing",
	})
	tk3 := &Task{
		ID:              "t-budget-executing",
		Dir:             dir,
		SessionID:       sid,
		ActiveAttemptID: "att-exec",
		Goal:            &TaskGoalBinding{NativeGoalID: gid, BoundAttemptID: "att-exec"},
	}
	confirmHostedControl(root, tk3, home, goalControlStop, "/goal clear")
	st3, err := loadGoalHostStatus(root, tk3.ID, "att-exec")
	if err != nil {
		t.Fatal(err)
	}
	if st3.ControlConfirmed {
		t.Fatal("budget_limited without Idle is not terminal stop confirmation")
	}
	if tk.Goal.CustodyReleased || tk3.Goal.CustodyReleased {
		t.Fatal("stop confirmation must not release custody; Wait does")
	}
	obs := nativeGoalObservation{GoalID: "other", NativeStatus: "budget_limited", Phase: "idle", BudgetLimited: true, UpdatesOK: true}
	if nativeStopConfirmed(obs, gid) {
		t.Fatal("other goal_id must not confirm this Goal")
	}
}

func TestGoalRunRequiresManualFlag(t *testing.T) {
	t.Parallel()
	if err := cmdWorkflowGoalRun([]string{"-root", t.TempDir(), "wf-nope"}); !errors.Is(err, errGoalManualRequired) {
		t.Fatalf("goal-run without -manual: %v", err)
	}
}

func TestWorkflowFlagsAfterPositionalID(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("goal-run", flag.ContinueOnError)
	manual := fs.Bool("manual", false, "")
	budget := fs.Int64("budget", 0, "")
	rootFlag := fs.String("root", "", "")
	sandbox := fs.String("sandbox", "", "")
	if err := parseWorkflowFlags(fs, []string{"wf-id", "-manual", "-budget", "12", "-root", "/tmp/r", "-sandbox", "linked-common-dir"}); err != nil {
		t.Fatal(err)
	}
	if fs.Arg(0) != "wf-id" || !*manual || *budget != 12 || *rootFlag != "/tmp/r" || *sandbox != "linked-common-dir" {
		t.Fatalf("id=%q manual=%v budget=%d root=%q sandbox=%q", fs.Arg(0), *manual, *budget, *rootFlag, *sandbox)
	}
}

func TestUnsupportedPermissionTupleRejectedBeforeLaunch(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	enableManualGrok(t, root, cfg, filepath.Join(t.TempDir(), "argv"))
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.PermissionMode = "danger-full-access"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); !errors.Is(err, errGoalUnsupportedTuple) {
		t.Fatalf("unsupported tuple must fail before effect: %v", err)
	}
}

func TestD3MissingFinalUpdateStatusRejected(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := t.TempDir()
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	gid := "goal-final"
	writeGrokGoalFixture(t, home, cwd, sid, gid, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
	})
	line, _ := json.Marshal(map[string]any{
		"params": map[string]any{
			"sessionId": sid,
			"update":    map[string]any{"sessionUpdate": "goal_updated", "goal_id": gid, "last_classifier_verdict": "achieved"},
		},
	})
	if err := os.WriteFile(filepath.Join(grokGoalSessionDir(home, cwd, sid), "updates.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	obs, err := observeNativeGrokGoal(home, cwd, sid, gid)
	if err == nil && mapNativeGoalToTask(obs, true, false, false).AcceptDone {
		t.Fatal("missing final update status must not prove done")
	}
}

func TestD3EmptySummaryCwdRejected(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	gid := "goal-cwd"
	tk.SessionID = sid
	tk.Goal.NativeGoalID = gid
	seedExitedAttempt(t, root, tk, "att-cwd")
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, sid, gid, "complete", grokNativeGoalStateFile{LastClassifierVerdict: "achieved"})
	sum, _ := json.Marshal(grokSessionSummaryFile{Info: grokSessionSummaryInfo{ID: sid, CWD: ""}})
	if err := os.WriteFile(filepath.Join(grokGoalSessionDir(home, dir, sid), "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sid, ExpectedGoalID: gid,
		ExpectedAttempt: "att-cwd", GrokHome: home, GrokCWD: dir,
	})
	if err == nil && got != nil && got.Status == statusDone {
		t.Fatal("empty summary cwd must not be accepted complete")
	}
}

func TestReservedAttemptCannotAcceptDone(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	gid := "goal-reserved"
	tk.SessionID = sid
	tk.Goal.NativeGoalID = gid
	rec := &AttemptRecord{
		TaskID: tk.ID, AttemptID: "att-res", ExpectedRevision: tk.Revision,
		ControlEpoch: tk.ControlEpoch, State: attemptReserved,
		CreatedAt: time.Now().Format(time.RFC3339Nano),
	}
	if err := writeAttempt(root, rec); err != nil {
		t.Fatal(err)
	}
	tk.ActiveAttemptID = "att-res"
	tk.Goal.BoundAttemptID = "att-res"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	tk, _ = loadTask(root, tk.ID)
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, sid, gid, "complete", grokNativeGoalStateFile{LastClassifierVerdict: "achieved"})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sid, ExpectedGoalID: gid,
		ExpectedAttempt: "att-res", GrokHome: home, GrokCWD: dir,
	})
	if err == nil && got != nil && got.Status == statusDone {
		t.Fatal("reserved never-started attempt must not become done")
	}
}

func TestDesignReceiptRejectsReadOnlyFalse(t *testing.T) {
	t.Parallel()
	path := writeAstraDesignReceiptWith(t, map[string]any{"read_only": false})
	if _, err := loadExternalDesignReceipt(path); err == nil {
		t.Fatal("read_only false must not be rewritten into acceptance")
	}
}

func TestDesignResultAcceptsCompletedStageWithFreshReceipt(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	gid := "goal-accept"
	launchExitedManualGoal(t, root, cfg, wf, tk, gid)
	sid := tk.SessionID
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, sid, gid, "complete", grokNativeGoalStateFile{LastClassifierVerdict: "achieved"})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sid, ExpectedGoalID: gid,
		ExpectedAttempt: tk.Goal.BoundAttemptID, GrokHome: home, GrokCWD: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusDone {
		t.Fatalf("setup done: %s %s", got.Status, got.Goal.ObservationNote)
	}
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     got.ID,
		"consumed_session_id":  got.SessionID,
		"consumed_attempt_id":  got.Goal.BoundAttemptID,
		"consumed_revision":    got.Revision,
		"consumed_observation": "complete",
	})
	out, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionAccept, "complete", receipt)
	if err != nil {
		t.Fatalf("accept completed stage: %v", err)
	}
	if out.Status != statusDone {
		t.Fatal("accept must not change done into another terminal")
	}
	wf2, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf2.EffectGates.Integration != effectGateHeld {
		t.Fatal("design accept must not release integration")
	}
}

func TestDesignResultKeepsActualModelDespiteDisplayLabel(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.Observation = goalObsUnknown
	tk.Goal.LastNativeStatus = "budget_limited"
	tk.Status = statusHeld
	revokeScheduling(tk)
	seedExitedAttempt(t, root, tk, "att-unqual")
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"actual_model":         "gpt-4",
		"identity":             "Astra",
		"consumed_task_id":     tk.ID,
		"consumed_session_id":  tk.SessionID,
		"consumed_attempt_id":  tk.Goal.BoundAttemptID,
		"consumed_revision":    tk.Revision,
		"consumed_observation": "budget_limited",
	})
	if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionStop, "budget_limited", receipt); err != nil {
		t.Fatal(err)
	}
	if wf.DesignLineage.LatestValid.ActualModel != "gpt-4" || tk.Status == statusDone {
		t.Fatal("display identity must not rewrite actual model or manufacture success")
	}
}

func TestCopyableNativeGoalIsLiteralPathDigest(t *testing.T) {
	// Captures process-wide stdout; run before the parallel test group.
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.BudgetTokens = 60000
	contract := frozenGoalStageContract(wf, tk)
	for _, want := range []string{"latest_design_path:", "latest_design_digest:", "latest_design_input_identity:", "latest_design_decision:"} {
		if !strings.Contains(contract, want) {
			t.Fatalf("initial frozen contract missing %s:\n%s", want, contract)
		}
	}
	digest := sha256Hex(contract)
	path := filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt")
	cmd := copyableNativeGoalCommand(path, digest, 60000)
	if strings.Contains(cmd, "$(cat") || strings.Contains(cmd, "`") {
		t.Fatalf("copyable command must be literal, got %q", cmd)
	}
	if !strings.Contains(cmd, path) || !strings.Contains(cmd, digest) || !strings.Contains(cmd, "--budget 60000") {
		t.Fatalf("copyable command missing path/digest/budget: %q", cmd)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	printManualGoalInstructions(root, wf, tk, contract, digest)
	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "$(cat") {
		t.Fatalf("printed instructions still use shell substitution:\n%s", text)
	}
	if !strings.Contains(text, cmd) {
		t.Fatalf("printed instructions must include the literal command:\n%s", text)
	}
}

func d4SuccessorFixture(t *testing.T) (string, *Config, *WorkflowRecord, *Task, string) {
	t.Helper()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.Observation = goalObsUnknown
	tk.Goal.LastNativeStatus = "budget_limited"
	tk.Status = statusHeld
	revokeScheduling(tk)
	seedExitedAttempt(t, root, tk, "att-budget")
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     tk.ID,
		"consumed_session_id":  tk.SessionID,
		"consumed_attempt_id":  tk.Goal.BoundAttemptID,
		"consumed_revision":    tk.Revision,
		"consumed_observation": "budget_limited",
	})
	return root, cfg, wf, tk, receipt
}

func TestD4SuccessorRetainsFreshDesignAndRound(t *testing.T) {
	t.Parallel()
	root, cfg, wf, _, receipt := d4SuccessorFixture(t)
	fresh, err := loadExternalDesignReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	before := wf.CurrentRound
	next, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.DesignLineage == nil || disk.DesignLineage.LatestValid == nil {
		t.Fatal("persisted workflow missing latest design")
	}
	if disk.DesignLineage.LatestValid.Digest != fresh.Digest {
		t.Errorf("fresh design lost: latest=%s wanted=%s", disk.DesignLineage.LatestValid.Digest, fresh.Digest)
	}
	if disk.CurrentRound <= before || next.FixRound <= before {
		t.Errorf("round not advanced: workflow=%d next=%d before=%d", disk.CurrentRound, next.FixRound, before)
	}
	contract := frozenGoalStageContract(disk, next)
	if !strings.Contains(contract, fresh.Digest) || !strings.Contains(contract, firstNonBlank(fresh.ResultPath, fresh.ReceiptPath)) {
		t.Fatalf("successor frozen contract must name the fresh design:\n%s", contract)
	}
	if !strings.Contains(contract, "latest_design_decision:") {
		t.Fatalf("successor frozen contract missing decision:\n%s", contract)
	}
}

func TestD4SuccessorHonorsExhaustedRound(t *testing.T) {
	t.Parallel()
	root, cfg, wf, _, receipt := d4SuccessorFixture(t)
	wf.CurrentRound = wf.MaxRounds
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	next, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt)
	if err == nil && next != nil {
		t.Fatalf("exhausted workflow still created successor %s round=%d max=%d", next.ID, wf.CurrentRound, wf.MaxRounds)
	}
	if next != nil {
		t.Fatalf("CurrentRound==MaxRounds must create no writer, got %s", next.ID)
	}
}

func TestGoalRepairRefusedTowardDesignResult(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	admitManualWriter(t, root, cfg, wf)
	wf.Review = &WorkflowReview{TaskID: "rev-1", Verdict: "block", Admissible: false}
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	_, err := admitWorkflowRepair(root, cfg, wf, "p1", "summary")
	if !errors.Is(err, errGoalDesignResult) {
		t.Fatalf("Goal repair must be refused: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "design-result") || !strings.Contains(err.Error(), "revise") {
		t.Fatalf("refusal must guide operator to design-result -decision revise: %v", err)
	}
}

func TestStaleDesignResultRevisionRefused(t *testing.T) {
	t.Parallel()
	root, cfg, wf, tk, _ := d4SuccessorFixture(t)
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     tk.ID,
		"consumed_session_id":  tk.SessionID,
		"consumed_attempt_id":  tk.Goal.BoundAttemptID,
		"consumed_revision":    tk.Revision + 99,
		"consumed_observation": "budget_limited",
	})
	if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt); !errors.Is(err, errGoalDesignResult) {
		t.Fatalf("stale revision must fail closed: %v", err)
	}
}

func TestConcurrentGoalRepairAndDesignResultFailClosed(t *testing.T) {
	t.Parallel()
	root, cfg, wf, tk, receipt := d4SuccessorFixture(t)
	wf.Review = &WorkflowReview{TaskID: "rev-1", Verdict: "block", Admissible: false}
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var repairErr, designErr error
	var next *Task
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, repairErr = admitWorkflowRepair(root, cfg, wf, "p1", "summary")
	}()
	go func() {
		defer wg.Done()
		next, designErr = applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt)
	}()
	wg.Wait()
	if repairErr == nil {
		t.Fatal("concurrent Goal repair must not create a writer")
	}
	if !errors.Is(repairErr, errGoalDesignResult) {
		t.Fatalf("Goal repair must fail closed: %v", repairErr)
	}
	if designErr != nil && next == nil {
		// serialized: design-result may lose if repair ran first and... repair refuses
		// without mutating, so design-result should still succeed.
		t.Fatalf("design-result should still admit after refused repair: %v", designErr)
	}
	if next != nil && next.ID == tk.ID {
		t.Fatal("successor must be a new writer")
	}
}

func TestCompletedDesignTaskPromptHashIsNotProof(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	review := newTask(root, cfg, typeReview, "design", dir, []string{"design the auth token vertical as Astra"}, 1)
	review.Status = statusDone
	review.CursorModel = "display-astra-not-actual"
	if err := saveTask(root, review); err != nil {
		t.Fatal(err)
	}
	_, err := loadCompletedDesignTask(root, review.ID)
	if !errors.Is(err, errGoalDesignProof) {
		t.Fatalf("prompt-hash design task must be refused: %v", err)
	}
}

func TestDesignArtifactDriftRefusedAtAdmission(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	result := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(result, []byte("independent astra design v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	receipt := writeAstraDesignReceiptWith(t, map[string]any{"result_path": result})
	wf := initTestWorkflow(t, root, dir)
	if err := bindInitialDesignProof(wf, designBindRequest{ReceiptPath: receipt}); err != nil {
		t.Fatal(err)
	}
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result, []byte("independent astra design DRIFTED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := admitWorkflowWriterMode(root, cfg, wf, "", goalWriterManual); !errors.Is(err, errGoalDesignProof) {
		t.Fatalf("artifact drift at admission must fail closed: %v", err)
	}
}

func TestExitedNeverStartedPID0CannotAcceptDone(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	gid := "goal-never-started"
	tk.SessionID = sid
	tk.Goal.NativeGoalID = gid
	seedExitedAttempt(t, root, tk, "att-zero")
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, sid, gid, "complete", grokNativeGoalStateFile{LastClassifierVerdict: "achieved"})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sid, ExpectedGoalID: gid,
		ExpectedAttempt: "att-zero", GrokHome: home, GrokCWD: dir,
	})
	if err == nil && got != nil && got.Status == statusDone {
		t.Fatal("PID 0 / never-started exited attempt must not become done")
	}
}

func TestInputDigestMismatchRejected(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	launchExitedManualGoal(t, root, cfg, wf, tk, "goal-digest")
	tk.Goal.InputDigest = "deadbeef"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	tk, _ = loadTask(root, tk.ID)
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, tk.SessionID, tk.Goal.NativeGoalID, "complete", grokNativeGoalStateFile{LastClassifierVerdict: "achieved"})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: tk.SessionID, ExpectedGoalID: tk.Goal.NativeGoalID,
		ExpectedAttempt: tk.Goal.BoundAttemptID, GrokHome: home, GrokCWD: dir,
	})
	if err == nil && got != nil && got.Status == statusDone {
		t.Fatal("InputDigest mismatch must not accept done")
	}
}

func TestFakeExecutableLaunchExitGoalSyncDoesNotStuffPID(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	launchExitedManualGoal(t, root, cfg, wf, tk, "goal-fake-exec")
	rec, err := loadRequiredGoalAttempt(root, tk)
	if err != nil {
		t.Fatal(err)
	}
	if rec.PID <= 0 || rec.StartIdentity == "" {
		t.Fatalf("fake-executable production path must bind real start identity, got pid=%d ident=%q", rec.PID, rec.StartIdentity)
	}
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, tk.SessionID, tk.Goal.NativeGoalID, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved", TotalVerifyRounds: 0,
	})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: tk.SessionID,
		ExpectedGoalID: tk.Goal.NativeGoalID, ExpectedAttempt: tk.Goal.BoundAttemptID,
		GrokHome: home, GrokCWD: dir,
	})
	if err != nil {
		t.Fatalf("goal-sync: %v", err)
	}
	if got.Status != statusDone || !taskDurablyDone(root, got) {
		t.Fatalf("fake-executable launch/exit/sync must accept done: status=%s obs=%s note=%s",
			got.Status, got.Goal.Observation, got.Goal.ObservationNote)
	}
	again, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: got.Revision, ExpectedSession: got.SessionID,
		ExpectedGoalID: got.Goal.NativeGoalID, ExpectedAttempt: got.Goal.BoundAttemptID,
		GrokHome: home, GrokCWD: dir,
	})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if again.Status != statusDone || again.ID != got.ID {
		t.Fatalf("second goal-sync must not redispatch: status=%s id=%s", again.Status, again.ID)
	}
}

// fakeGrokGoalTerminalBin is a provider stand-in that writes native session
// files and then returns. The directory encoding is the production cwd key so
// goal-sync reads the same tree the child wrote. Mode is complete, nonzero,
// timeout, or hold.
func fakeGrokGoalTerminalBin(t *testing.T, cwd, mode, status, classifier, readyPath, holdPath string) string {
	t.Helper()
	return fakeGrokGoalTerminalBinID(t, cwd, mode, status, classifier, readyPath, holdPath, "auto-goal-1")
}

func fakeGrokGoalTerminalBinID(t *testing.T, cwd, mode, status, classifier, readyPath, holdPath, goalID string) string {
	t.Helper()
	dir := t.TempDir()
	py := filepath.Join(dir, "grok.py")
	body := fmt.Sprintf(`#!/usr/bin/env python3
import json, os, sys, time
args = sys.argv[1:]
got_cwd = ""
sid = ""
prev = ""
for a in args:
    if a in ("-p", "--single", "--prompt-file"):
        sys.stderr.write("p-not-goal\n")
        sys.exit(2)
    if prev == "--cwd":
        got_cwd = a
    if prev in ("-s", "--resume", "--session-id"):
        sid = a
    prev = a
home = os.environ.get("GROK_HOME", "")
if not home or not got_cwd or not sid:
    sys.stderr.write("missing grok home/cwd/session\n")
    sys.exit(2)
enc = %q
sess = os.path.join(home, "sessions", enc, sid)
os.makedirs(os.path.join(sess, "goal"), exist_ok=True)
goal = %q
status = %q
classifier = %q
st = {
    "goal_id": goal,
    "status": status,
    "phase": "Executing" if status == "active" else "Idle",
    "token_budget": 0,
    "last_classifier_verdict": classifier,
    "total_verify_rounds": 0,
    "total_worker_rounds": 0,
    "history": [{"event": "goal_completed" if status == "complete" else "goal_updated"}],
}
with open(os.path.join(sess, "goal", "state.json"), "w") as f:
    json.dump(st, f)
    f.write("\n")
with open(os.path.join(sess, "summary.json"), "w") as f:
    json.dump({"info": {"id": sid, "cwd": got_cwd}}, f)
    f.write("\n")
line = {
    "method": "_x.ai/session/update",
    "params": {
        "sessionId": sid,
        "update": {
            "sessionUpdate": "goal_updated",
            "goal_id": goal,
            "status": status,
            "phase": "executing" if status == "active" else "idle",
            "last_classifier_verdict": classifier,
            "last_event": "goal_completed" if status == "complete" else "goal_updated",
        },
    },
}
with open(os.path.join(sess, "updates.jsonl"), "w") as f:
    f.write(json.dumps(line) + "\n")
ready = %q
hold = %q
mode = %q
if ready:
    with open(ready, "w") as f:
        f.write("ready\n")
if mode == "hold":
    while hold and os.path.exists(hold):
        time.sleep(0.05)
if mode == "timeout":
    time.sleep(30)
if mode == "nonzero":
    sys.exit(2)
sys.exit(0)
`, grokEncodeCwd(cwd), goalID, status, classifier, readyPath, holdPath, mode)
	if err := os.WriteFile(py, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "grok")
	script := "#!/bin/sh\nexec python3 -u " + shSingleQuote(py) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func goalEventsOfType(t *testing.T, root, taskID, evType string) []TaskEvent {
	t.Helper()
	events, _, err := loadTaskEvents(root, taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out []TaskEvent
	for _, ev := range events {
		if ev.Type == evType {
			out = append(out, ev)
		}
	}
	return out
}

func assertNoGoalEvDone(t *testing.T, root string, tk *Task) {
	t.Helper()
	if tk.Status == statusDone || tk.Status == statusCanceled {
		t.Fatalf("status=%s obs=%s note=%s", tk.Status, tk.Goal.Observation, tk.Goal.ObservationNote)
	}
	if done := goalEventsOfType(t, root, tk.ID, evDone); len(done) != 0 {
		t.Fatalf("evDone committed: %+v", done)
	}
	rows, class, err := loadManagerWakeOutbox(root)
	if err != nil || class != "" {
		t.Fatalf("outbox class=%s err=%v", class, err)
	}
	for _, row := range rows {
		if row.EventType == evDone {
			t.Fatalf("outbox has evDone: %+v", row)
		}
	}
}

func TestNativeDirectionReturnSyncsThatCardAndWaitsForSibling(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	if wf.WriterTaskID != "" {
		t.Fatalf("this fixture has no writer to route through, got %s", wf.WriterTaskID)
	}
	bindTestDesign(t, root, cfg, wf)
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.WriterTaskID != "" {
		t.Fatalf("design proof assigned a writer %s", wf.WriterTaskID)
	}
	if err := cmdWorkflowGoalDirection([]string{"-root", root, wf.ID, "-direction", "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowGoalDirection([]string{"-root", root, wf.ID, "-direction", "beta"}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.WriterTaskID != "" || len(wf.NativeGoalTaskIDs) != 2 {
		t.Fatalf("writer=%s natives=%v", wf.WriterTaskID, wf.NativeGoalTaskIDs)
	}
	alphaID, betaID := wf.NativeGoalTaskIDs[0], wf.NativeGoalTaskIDs[1]
	alpha, err := loadTask(root, alphaID)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := loadTask(root, betaID)
	if err != nil {
		t.Fatal(err)
	}
	if alpha.Goal == nil || alpha.Goal.Scope != "alpha" {
		beta, alpha = alpha, beta
		betaID, alphaID = alphaID, betaID
	}

	unrelated := newTask(root, cfg, typeSequence, "other workflow", dir, []string{"not this goal"}, 1)
	unrelated.WorkflowID = "wf-other-0000-abcd"
	if err := saveTask(root, unrelated); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowGoalSync([]string{"-root", root, wf.ID, "-task", unrelated.ID}); err == nil || !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("unrelated target synced: %v", err)
	}
	if err := cmdWorkflowGoalSync([]string{"-root", root, wf.ID}); err == nil || !strings.Contains(err.Error(), "no writer task") {
		t.Fatalf("omitting -task guessed a direction: %v", err)
	}

	launchDirection := func(id, goalID string) {
		t.Helper()
		task, err := loadTask(root, id)
		if err != nil {
			t.Fatal(err)
		}
		task.Goal.GrokHome = t.TempDir()
		if err := saveTask(root, task); err != nil {
			t.Fatal(err)
		}
		cfg = workflowTestCfg(t, root)
		cfg.GrokBuildBin = fakeGrokGoalTerminalBinID(t, dir, "complete", "complete", "achieved", "", "", goalID)
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		cfg.ManagerWake = &ManagerWakeConfig{Enabled: true}
		saveGoalCfg(t, root, cfg)
		headlessGoalStdin(t)
		if err := cmdWorkflowGoalLaunch([]string{"-root", root, "-task", id, "-manual"}); err != nil {
			t.Fatalf("normal return %s: %v", id, err)
		}
	}
	launchDirection(alphaID, "goal-alpha")
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.WriterTaskID != "" {
		t.Fatalf("sync rewrote WriterTaskID to %s", wf.WriterTaskID)
	}
	alpha, err = loadTask(root, alphaID)
	if err != nil {
		t.Fatal(err)
	}
	beta, err = loadTask(root, betaID)
	if err != nil {
		t.Fatal(err)
	}
	if alpha.Status != statusDone || alpha.Goal == nil || alpha.Goal.NativeGoalID != "goal-alpha" || alpha.Goal.Observation != goalObsDone {
		t.Fatalf("alpha was not the synced direction: status=%s goal=%+v", alpha.Status, alpha.Goal)
	}
	if beta.Status == statusDone || (beta.Goal != nil && beta.Goal.NativeGoalID != "") {
		t.Fatalf("sibling was synced early: status=%s goal=%+v", beta.Status, beta.Goal)
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err == nil || !strings.Contains(err.Error(), "未创建验收卡") {
		t.Fatalf("one verified direction created acceptance: %v", err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID != "" || wf.GoalCompleted || wf.WriterTaskID != "" {
		t.Fatalf("early acceptance=%s completed=%v writer=%s", wf.AcceptanceTaskID, wf.GoalCompleted, wf.WriterTaskID)
	}

	betaSession := beta.SessionID
	beta.SessionID = alpha.SessionID
	if err := saveTask(root, beta); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowGoalSync([]string{"-root", root, wf.ID, "-task", betaID}); err == nil || !strings.Contains(err.Error(), "shared session") {
		t.Fatalf("shared session synced: %v", err)
	}
	beta, err = loadTask(root, betaID)
	if err != nil {
		t.Fatal(err)
	}
	if beta.Status == statusDone {
		t.Fatal("shared session marked the sibling done")
	}
	beta.SessionID = betaSession
	beta.Goal.NativeGoalID = alpha.Goal.NativeGoalID
	if err := saveTask(root, beta); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowGoalSync([]string{"-root", root, wf.ID, "-task", betaID}); err == nil || !strings.Contains(err.Error(), "shared native goal_id") {
		t.Fatalf("shared native goal id synced: %v", err)
	}
	beta, err = loadTask(root, betaID)
	if err != nil {
		t.Fatal(err)
	}
	beta.Goal.NativeGoalID = ""
	beta.SessionID = betaSession
	if err := saveTask(root, beta); err != nil {
		t.Fatal(err)
	}

	launchDirection(betaID, "goal-beta")
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	alpha, err = loadTask(root, alphaID)
	if err != nil {
		t.Fatal(err)
	}
	beta, err = loadTask(root, betaID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.WriterTaskID != "" {
		t.Fatalf("second return rewrote WriterTaskID to %s", wf.WriterTaskID)
	}
	if alpha.Status != statusDone || alpha.Goal.NativeGoalID != "goal-alpha" {
		t.Fatalf("alpha changed after beta: status=%s goal=%s", alpha.Status, alpha.Goal.NativeGoalID)
	}
	if beta.Status != statusDone || beta.Goal == nil || beta.Goal.NativeGoalID != "goal-beta" || beta.SessionID == alpha.SessionID {
		t.Fatalf("beta was not synced on its own identity: status=%s goal=%+v session=%s alpha=%s", beta.Status, beta.Goal, beta.SessionID, alpha.SessionID)
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID == "" || wf.GoalCompleted {
		t.Fatalf("both verified directions must create one card without accepting the goal: id=%s completed=%v", wf.AcceptanceTaskID, wf.GoalCompleted)
	}
	accID := wf.AcceptanceTaskID
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID != accID || wf.GoalCompleted || wf.WriterTaskID != "" {
		t.Fatalf("second accept changed acceptance=%s completed=%v writer=%s", wf.AcceptanceTaskID, wf.GoalCompleted, wf.WriterTaskID)
	}
	if _, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{TaskID: betaID, ExpectedSession: alpha.SessionID}); err == nil || !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("sibling session was accepted for beta: %v", err)
	}
}

// TestGoalAutoSync drives the shipped manual launcher. Native files are written
// by the fake provider before it returns; the test does not call goal-sync itself.
func TestGoalAutoSync(t *testing.T) {
	t.Parallel()

	t.Run("NaturalCompletion", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		tk := admitManualWriter(t, root, cfg, wf)
		home := t.TempDir()
		tk.Goal.GrokHome = home
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		cfg.GrokBuildBin = fakeGrokGoalTerminalBin(t, dir, "complete", "complete", "achieved", "", "")
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		cfg.ManagerWake = &ManagerWakeConfig{Enabled: true}
		saveGoalCfg(t, root, cfg)

		if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
			t.Fatalf("natural return: %v", err)
		}
		got, err := loadTask(root, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != statusDone || got.Goal.Observation != goalObsDone {
			t.Fatalf("achieved return must be done: status=%s obs=%s note=%s", got.Status, got.Goal.Observation, got.Goal.ObservationNote)
		}
		if got.Goal.NativeGoalID != "auto-goal-1" {
			t.Fatalf("native goal id was not read from the session file: %q", got.Goal.NativeGoalID)
		}
		if !taskDurablyDone(root, got) || got.LastCommittedTransitionID == "" {
			t.Fatalf("done must be durable: id=%q durably=%v", got.LastCommittedTransitionID, taskDurablyDone(root, got))
		}
		done := goalEventsOfType(t, root, got.ID, evDone)
		if len(done) != 1 || done[0].Actor != "workflow:goal-sync" || done[0].TransitionID != got.LastCommittedTransitionID {
			t.Fatalf("evDone actor/transition: %+v", done)
		}
		rec, err := loadTransition(root, got.ID, got.LastCommittedTransitionID)
		if err != nil || rec == nil || rec.Actor != "workflow:goal-sync" || rec.EventType != evDone {
			t.Fatalf("transition actor: %+v err=%v", rec, err)
		}
		rows, class, err := loadManagerWakeOutbox(root)
		if err != nil || class != "" {
			t.Fatalf("outbox class=%s err=%v", class, err)
		}
		if len(rows) != 1 || rows[0].TransitionID != got.LastCommittedTransitionID || rows[0].EventType != evDone || rows[0].TaskID != got.ID {
			t.Fatalf("outbox rows: %+v", rows)
		}
		reloaded, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.GoalCompleted {
			t.Fatal("one native session complete must not accept the whole goal")
		}
		reloaded.DirectionTaskIDs = []string{got.ID, "t-missing-direction"}
		if err := saveWorkflow(root, cfg, reloaded); err != nil {
			t.Fatal(err)
		}
		if err := cmdWorkflowAccept([]string{"-root", root, reloaded.ID}); err == nil {
			t.Fatal("joined acceptance must wait for every required direction")
		}
		again, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if again.GoalCompleted {
			t.Fatal("refused joined acceptance must leave the goal unaccepted")
		}
	})

	t.Run("CancelWithoutStopEvidence", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		tk := admitManualWriter(t, root, cfg, wf)
		home := t.TempDir()
		tk.Goal.GrokHome = home
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		ready := filepath.Join(t.TempDir(), "ready")
		hold := filepath.Join(t.TempDir(), "hold")
		if err := os.WriteFile(hold, []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg.GrokBuildBin = fakeGrokGoalTerminalBin(t, dir, "hold", "complete", "achieved", ready, hold)
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		cfg.ManagerWake = &ManagerWakeConfig{Enabled: true}
		saveGoalCfg(t, root, cfg)

		setErr := make(chan error, 1)
		go func() {
			deadline := time.Now().Add(15 * time.Second)
			sawReady := false
			for time.Now().Before(deadline) {
				if _, err := os.Stat(ready); err == nil {
					sawReady = true
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !sawReady {
				_ = os.Remove(hold)
				setErr <- fmt.Errorf("provider did not reach the cancel window")
				return
			}
			if !acquireLock(root, 5*time.Second) {
				setErr <- fmt.Errorf("scheduler lock not available to record cancel")
				_ = os.Remove(hold)
				return
			}
			loaded, err := loadTask(root, tk.ID)
			if err != nil {
				releaseLock(root)
				setErr <- err
				_ = os.Remove(hold)
				return
			}
			if loaded.Goal == nil {
				releaseLock(root)
				setErr <- fmt.Errorf("goal binding missing while running")
				_ = os.Remove(hold)
				return
			}
			loaded.Goal.CancelRequested = true
			loaded.Goal.StopEvidence = false
			err = saveTask(root, loaded)
			releaseLock(root)
			_ = os.Remove(hold)
			setErr <- err
		}()

		launchErr := runManualGoalLaunch(t, root, cfg, wf, 12000)
		if err := <-setErr; err != nil {
			t.Fatalf("set cancel: %v (launch: %v)", err, launchErr)
		}
		if launchErr != nil {
			t.Fatalf("cancel return: %v", launchErr)
		}
		got, err := loadTask(root, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Goal.Observation != goalObsUnknown {
			t.Fatalf("cancel without stop evidence must stay unknown: status=%s obs=%s note=%s stop=%v",
				got.Status, got.Goal.Observation, got.Goal.ObservationNote, got.Goal.StopEvidence)
		}
		assertNoGoalEvDone(t, root, got)
	})

	t.Run("NonzeroExit", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		tk := admitManualWriter(t, root, cfg, wf)
		home := t.TempDir()
		tk.Goal.GrokHome = home
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		cfg.GrokBuildBin = fakeGrokGoalTerminalBin(t, dir, "nonzero", "complete", "achieved", "", "")
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		cfg.ManagerWake = &ManagerWakeConfig{Enabled: true}
		saveGoalCfg(t, root, cfg)

		err := runManualGoalLaunch(t, root, cfg, wf, 12000)
		if err == nil {
			t.Fatal("nonzero provider exit must surface")
		}
		got, lerr := loadTask(root, tk.ID)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if got.Status != statusHeld || got.Goal.Observation != goalObsUnknown {
			t.Fatalf("nonzero must stay held/unknown: status=%s obs=%s note=%s launch=%v",
				got.Status, got.Goal.Observation, got.Goal.ObservationNote, err)
		}
		assertNoGoalEvDone(t, root, got)
	})

	t.Run("Timeout", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		tk := admitManualWriter(t, root, cfg, wf)
		home := t.TempDir()
		tk.Goal.GrokHome = home
		tk.Goal.HardTimeoutSec = 2
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		cfg.GrokBuildBin = fakeGrokGoalTerminalBin(t, dir, "timeout", "complete", "achieved", "", "")
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		cfg.ManagerWake = &ManagerWakeConfig{Enabled: true}
		saveGoalCfg(t, root, cfg)

		err := runManualGoalLaunch(t, root, cfg, wf, 12000)
		if err != nil {
			t.Fatalf("timeout return must stay the existing nil launch error: %v", err)
		}
		got, lerr := loadTask(root, tk.ID)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if got.Status != statusHeld || got.Goal.Observation != goalObsUnknown {
			t.Fatalf("timeout must stay held/unknown: status=%s obs=%s note=%s",
				got.Status, got.Goal.Observation, got.Goal.ObservationNote)
		}
		assertNoGoalEvDone(t, root, got)
	})

	t.Run("ActiveStaysHeld", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		tk := admitManualWriter(t, root, cfg, wf)
		home := t.TempDir()
		tk.Goal.GrokHome = home
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		cfg.GrokBuildBin = fakeGrokGoalTerminalBin(t, dir, "complete", "active", "", "", "")
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		cfg.ManagerWake = &ManagerWakeConfig{Enabled: true}
		saveGoalCfg(t, root, cfg)

		if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
			t.Fatalf("active return: %v", err)
		}
		got, err := loadTask(root, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != statusHeld || got.Goal.Observation != goalObsUnknown || got.Goal.LastNativeStatus != "active" {
			t.Fatalf("active file after return must stay held/unknown: status=%s obs=%s native=%s note=%s",
				got.Status, got.Goal.Observation, got.Goal.LastNativeStatus, got.Goal.ObservationNote)
		}
		if got.Goal.Continuation != "" {
			t.Fatalf("active return must not keep a continuation: %q", got.Goal.Continuation)
		}
		assertNoGoalEvDone(t, root, got)
	})

	t.Run("PausedStaysNonAccepted", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		cfg := workflowTestCfg(t, root)
		wf := initTestWorkflow(t, root, dir)
		tk := admitManualWriter(t, root, cfg, wf)
		home := t.TempDir()
		tk.Goal.GrokHome = home
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		cfg.GrokBuildBin = fakeGrokGoalTerminalBin(t, dir, "complete", "paused", "", "", "")
		cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
		cfg.ManagerWake = &ManagerWakeConfig{Enabled: true}
		saveGoalCfg(t, root, cfg)

		if err := runManualGoalLaunch(t, root, cfg, wf, 12000); err != nil {
			t.Fatalf("paused return: %v", err)
		}
		got, err := loadTask(root, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == statusDone || got.Status == statusRunning || got.Goal.Observation == goalObsDone {
			t.Fatalf("paused file after return must not be done or running: status=%s obs=%s note=%s",
				got.Status, got.Goal.Observation, got.Goal.ObservationNote)
		}
		if got.Status != statusHeld || got.Goal.Continuation != "same-session" {
			t.Fatalf("paused return keeps sync's non-done held continuation: status=%s obs=%s cont=%s note=%s",
				got.Status, got.Goal.Observation, got.Goal.Continuation, got.Goal.ObservationNote)
		}
		assertNoGoalEvDone(t, root, got)
	})
}

func TestGoalSyncResidualActiveAfterTimeoutStaysHeldUnknown(t *testing.T) {
	root, dir, grokHome, cfg, wf, tk, before := seedResidualActiveTimeoutGoal(t)
	if tk.Status != statusHeld || tk.effectiveControlState() != controlRevoking || tk.ActiveAttemptID != "" {
		t.Fatalf("fixture must be held/revoking with empty ActiveAttemptID: status=%s control=%s active=%q",
			tk.Status, tk.effectiveControlState(), tk.ActiveAttemptID)
	}
	if !tk.Goal.CustodyReleased {
		t.Fatal("fixture must record released custody")
	}

	// Pre-fix / write-authority probe: current-revision mapped running write is
	// stale-refused and disk bytes stay unchanged. The guard is not loosened.
	probe := mappedRunningMutation(t, tk)
	if probe.Revision != tk.Revision {
		t.Fatalf("probe must keep current revision %d, got %d", tk.Revision, probe.Revision)
	}
	err := saveTask(root, probe)
	if !errors.Is(err, errStaleTaskWrite) {
		t.Fatalf("held/revoking -> running at current revision must be errStaleTaskWrite, got %v", err)
	}
	afterProbe, err := os.ReadFile(taskPath(root, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterProbe) != string(before) {
		t.Fatalf("stale running write mutated disk\nbefore:\n%s\nafter:\n%s", before, afterProbe)
	}

	req := GoalSyncRequest{
		ExpectedRevision: tk.Revision,
		ExpectedSession:  tk.SessionID,
		ExpectedGoalID:   tk.Goal.NativeGoalID,
		ExpectedAttempt:  tk.Goal.BoundAttemptID,
		GrokHome:         grokHome,
		GrokCWD:          dir,
	}
	got, err := syncWorkflowGoal(root, cfg, wf, req)
	if err != nil {
		t.Fatalf("shipped normal sync: %v", err)
	}
	assertResidualTimeoutUnchanged(t, root, got, before, tk.Revision)

	again, err := syncWorkflowGoal(root, cfg, wf, req)
	if err != nil {
		t.Fatalf("repeat sync: %v", err)
	}
	assertResidualTimeoutUnchanged(t, root, again, before, tk.Revision)

	out, cmdErr := captureGoalSyncCLI(t, root, wf.ID)
	if cmdErr != nil {
		t.Fatalf("cmdWorkflowGoalSync: %v\n%s", cmdErr, out)
	}
	if !strings.Contains(out, "status=held") || !strings.Contains(out, "observation=unknown") {
		t.Fatalf("CLI must print held/unknown, got %q", out)
	}
	if strings.Contains(out, "status=running") || strings.Contains(out, "status=done") || strings.Contains(out, "observation=running") {
		t.Fatalf("CLI revived residual-active timeout: %q", out)
	}
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertResidualTimeoutUnchanged(t, root, fresh, before, tk.Revision)

	if bin := strings.TrimSpace(os.Getenv("CARDEX_BIN")); bin != "" {
		run := func() string {
			t.Helper()
			cmd := exec.Command(bin, "workflow", "goal-sync", wf.ID, "-root", root)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("bin/cardex workflow goal-sync: %v\n%s", err, out)
			}
			s := string(out)
			t.Logf("bin/cardex workflow goal-sync: %s", strings.TrimSpace(s))
			if !strings.Contains(s, "status=held") || !strings.Contains(s, "observation=unknown") {
				t.Fatalf("bin/cardex must print held/unknown, got %q", s)
			}
			if strings.Contains(s, "status=running") || strings.Contains(s, "status=done") {
				t.Fatalf("bin/cardex revived residual-active timeout: %q", s)
			}
			return s
		}
		first := run()
		second := run()
		t.Logf("repeat bin/cardex stdout first=%q second=%q", strings.TrimSpace(first), strings.TrimSpace(second))
		fresh, err = loadTask(root, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertResidualTimeoutUnchanged(t, root, fresh, before, tk.Revision)
	}
}

func TestGoalSyncLiveNativeActiveStillRunning(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-live-active"
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	seedExitedAttempt(t, root, tk, "att-live")
	grokHome := t.TempDir()
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "active", grokNativeGoalStateFile{Phase: "Executing"})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
		ExpectedAttempt: "att-live", GrokHome: grokHome, GrokCWD: dir,
	})
	if err != nil {
		t.Fatalf("live native-active: %v", err)
	}
	if got.Status != statusRunning || got.Goal.Observation != goalObsRunning {
		t.Fatalf("live native-active producer must map to running, status=%s obs=%s", got.Status, got.Goal.Observation)
	}
}

func TestGoalSyncReleasedBudgetLimitedDoesNotReviveActiveAttempt(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "dddddddd-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-budget-released"
	attemptID := "atbudget000000001"
	grokHome := t.TempDir()
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	tk.Goal.GrokHome = grokHome
	seedExitedAttempt(t, root, tk, attemptID)
	tk.Status = statusHeld
	tk.Goal.Observation = goalObsUnknown
	tk.Goal.ObservationNote = "TUI returned; accept done only via goal-sync with native proof plus released custody"
	tk.Goal.CustodyReleased = true
	tk.ActiveAttemptID = ""
	revokeScheduling(tk)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	*tk = *loaded
	writeGrokGoalFixture(t, grokHome, dir, sessionID, goalID, "budget_limited", grokNativeGoalStateFile{
		LastClassifierVerdict: "not_achieved",
		Phase:                 "Idle",
	})
	got, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: tk.Revision,
		ExpectedSession:  sessionID,
		ExpectedGoalID:   goalID,
		ExpectedAttempt:  attemptID,
		GrokHome:         grokHome,
		GrokCWD:          dir,
	})
	if err != nil {
		t.Fatalf("budget_limited sync: %v", err)
	}
	if got.Goal == nil {
		t.Fatal("missing goal binding")
	}
	if got.Goal.Observation == goalObsDone || got.Status == statusDone || got.Goal.EvidenceComplete {
		t.Fatalf("budget_limited must stay nonaccepted: status=%s obs=%s note=%s", got.Status, got.Goal.Observation, got.Goal.ObservationNote)
	}
	if got.Goal.LastNativeStatus != "budget_limited" && !strings.Contains(got.Goal.ObservationNote, "budget_limited") {
		t.Fatalf("native budget_limited must remain visible: %+v", got.Goal)
	}
	if got.ActiveAttemptID != "" {
		t.Fatalf("custody-released budget_limited must not resurrect ActiveAttemptID: %q bound=%q", got.ActiveAttemptID, got.Goal.BoundAttemptID)
	}
	if got.Goal.BoundAttemptID != attemptID {
		t.Fatalf("BoundAttemptID must stay %s, got %q", attemptID, got.Goal.BoundAttemptID)
	}
	assertNoGoalEvDone(t, root, got)
}

func TestGoalSyncIsolationStaleAndMissingCustody(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	sessionID := "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	goalID := "goal-isolation"
	tk.SessionID = sessionID
	tk.Goal.NativeGoalID = goalID
	seedExitedAttempt(t, root, tk, "att-iso")
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, sessionID, goalID, "complete", grokNativeGoalStateFile{
		LastClassifierVerdict: "achieved",
	})
	req := GoalSyncRequest{
		ExpectedRevision: tk.Revision, ExpectedSession: sessionID, ExpectedGoalID: goalID,
		ExpectedAttempt: "att-iso", GrokHome: home, GrokCWD: dir,
	}

	staleRev := req
	staleRev.ExpectedRevision = req.ExpectedRevision - 1
	if _, err := syncWorkflowGoal(root, cfg, wf, staleRev); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("stale revision must reject: %v", err)
	}
	otherSess := req
	otherSess.ExpectedSession = "cccccccc-bbbb-4ccc-8ddd-ffffffffffff"
	if _, err := syncWorkflowGoal(root, cfg, wf, otherSess); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("other session must reject: %v", err)
	}
	otherGoal := req
	otherGoal.ExpectedGoalID = "goal-other"
	if _, err := syncWorkflowGoal(root, cfg, wf, otherGoal); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("other goal_id must reject: %v", err)
	}
	oldAtt := req
	oldAtt.ExpectedAttempt = "att-old"
	if _, err := syncWorkflowGoal(root, cfg, wf, oldAtt); !errors.Is(err, errGoalSyncRejected) {
		t.Fatalf("old attempt must reject: %v", err)
	}

	doneCh := make(chan struct{})
	markTaskLeaseResidue(tk.ID, doneCh)
	t.Cleanup(func() { close(doneCh) })
	got, err := syncWorkflowGoal(root, cfg, wf, req)
	if err != nil {
		t.Fatalf("live-lease complete: %v", err)
	}
	if got.Status == statusDone || got.Goal.Observation == goalObsDone {
		t.Fatalf("native complete without released custody must not be done: status=%s obs=%s", got.Status, got.Goal.Observation)
	}
	if got.Goal.Observation != goalObsUnknown {
		t.Fatalf("missing custody want unknown, got %s (%s)", got.Goal.Observation, got.Goal.ObservationNote)
	}
}

func TestGoalSyncConcurrentMappedRunningStaleGuard(t *testing.T) {
	root, _, _, _, _, tk, before := seedResidualActiveTimeoutGoal(t)
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			fresh, err := loadTask(root, tk.ID)
			if err != nil {
				errs <- err
				return
			}
			errs <- saveTask(root, mappedRunningMutation(t, fresh))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, errStaleTaskWrite) {
			t.Fatalf("concurrent mapped-running write must be errStaleTaskWrite, got %v", err)
		}
	}
	after, err := os.ReadFile(taskPath(root, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("concurrent stale running writes mutated disk\nbefore:\n%s\nafter:\n%s", before, after)
	}
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != statusHeld || fresh.Goal.Observation != goalObsUnknown || fresh.ActiveAttemptID != "" {
		t.Fatalf("concurrent probe revived task: status=%s obs=%s active=%q",
			fresh.Status, fresh.Goal.Observation, fresh.ActiveAttemptID)
	}
}

func assertGoalLaunchSharedConfigUnchanged(t *testing.T, root string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(configPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("shared config bytes must stay unchanged\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func assertGoalLaunchArgvAbsent(t *testing.T, argvPath string) {
	t.Helper()
	if _, err := os.Stat(argvPath); err == nil {
		raw, _ := os.ReadFile(argvPath)
		t.Fatalf("must not exec launcher, argv dump:\n%s", raw)
	}
}

func TestGoalLaunchOmitsBudgetAndSandboxDefaults(t *testing.T) {
	root, dir, argvPath, _, _, wf, tk, cfgBefore := setupInvocationSandboxLaunch(t)
	headlessGoalStdin(t)
	out, err := captureWorkflowCmd(t, "goal-launch", "-root", root, "-task", tk.ID, "-manual")
	if err != nil {
		t.Fatalf("omitted -budget/-sandbox with -manual must launch: %v\n%s", err, out)
	}
	assertGoalLaunchSharedConfigUnchanged(t, root, cfgBefore)
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("launcher must exec (argv dump missing): %v", err)
	}
	if dumpedArgValue(string(raw), "--sandbox") != grokBuildWriteSandboxDefault {
		t.Fatalf("omitted -sandbox must keep prior default --sandbox %s:\n%s", grokBuildWriteSandboxDefault, raw)
	}
	if dumpedHasFlag(string(raw), "--budget") {
		t.Fatalf("grok argv must not contain --budget:\n%s", raw)
	}
	contract, err := os.ReadFile(filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contract), "budget_tokens: 0 (soft)") {
		t.Fatalf("omitted -budget must freeze budget_tokens 0:\n%s", contract)
	}
	if !strings.Contains(out, "/goal") {
		t.Fatalf("printed instructions must include /goal:\n%s", out)
	}
	if strings.Contains(out, "--budget") {
		t.Fatalf("omitted -budget must not print --budget:\n%s", out)
	}
	if dumpedArgValue(string(raw), "--cwd") != dir {
		t.Fatalf("--cwd must stay worktree %q:\n%s", dir, raw)
	}
}

func TestGoalLaunchPositiveBudgetFreezesContractAndNativeGoal(t *testing.T) {
	root, _, argvPath, _, _, wf, tk, cfgBefore := setupInvocationSandboxLaunch(t)
	headlessGoalStdin(t)
	out, err := captureWorkflowCmd(t, "goal-launch", "-root", root, "-task", tk.ID, "-manual", "-budget", "12000")
	if err != nil {
		t.Fatalf("positive -budget must launch: %v\n%s", err, out)
	}
	assertGoalLaunchSharedConfigUnchanged(t, root, cfgBefore)
	contract, err := os.ReadFile(filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contract), "budget_tokens: 12000 (soft)") {
		t.Fatalf("contract must freeze budget_tokens 12000:\n%s", contract)
	}
	if !strings.Contains(out, "/goal") || !strings.Contains(out, "--budget 12000") {
		t.Fatalf("copyable command must contain /goal and --budget 12000:\n%s", out)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("launcher must exec (argv dump missing): %v", err)
	}
	if dumpedHasFlag(string(raw), "--budget") {
		t.Fatalf("grok argv must not contain --budget:\n%s", raw)
	}
}

func TestGoalLaunchRejectsBudgetMisuseBeforeExec(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
	}{
		{"negative", "-budget=-1"},
		{"overflow", "-budget=9223372036854775808"},
		{"non-numeric", "-budget=not-a-number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, argvPath, _, _, _, tk, cfgBefore := setupInvocationSandboxLaunch(t)
			headlessGoalStdin(t)
			err := cmdWorkflow([]string{"goal-launch", "-root", root, "-task", tk.ID, "-manual", tc.flag})
			if err == nil {
				t.Fatalf("%s must error before launch", tc.name)
			}
			assertGoalLaunchSharedConfigUnchanged(t, root, cfgBefore)
			assertGoalLaunchArgvAbsent(t, argvPath)
		})
	}
}

func TestGoalLaunchExistingSandboxProfile(t *testing.T) {
	root, dir, argvPath, _, _, _, tk, cfgBefore := setupInvocationSandboxLaunch(t)
	headlessGoalStdin(t)
	if err := cmdWorkflow([]string{"goal-launch", "-root", root, "-task", tk.ID, "-manual", "-sandbox", testInvocationSandboxProfile}); err != nil {
		t.Fatalf("existing applicable -sandbox must launch: %v", err)
	}
	assertGoalLaunchSharedConfigUnchanged(t, root, cfgBefore)
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("launcher must exec (argv dump missing): %v", err)
	}
	if dumpedArgValue(string(raw), "--sandbox") != testInvocationSandboxProfile {
		t.Fatalf("existing applicable profile must reach --sandbox %s:\n%s", testInvocationSandboxProfile, raw)
	}
	if dumpedArgValue(string(raw), "--cwd") != dir {
		t.Fatalf("--cwd must stay worktree %q:\n%s", dir, raw)
	}
}

func TestGoalLaunchSandboxSelectorRejectsBeforeExec(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"unknown-profile", "off", "workspace"} {
		name := name
		t.Run(name, func(t *testing.T) {
			root, _, argvPath, _, _, _, tk, cfgBefore := setupInvocationSandboxLaunch(t)
			headlessGoalStdin(t)
			err := cmdWorkflow([]string{"goal-launch", "-root", root, "-task", tk.ID, "-manual", "-sandbox", name})
			if err == nil {
				t.Fatalf("selector %q must reject before launch", name)
			}
			if !errors.Is(err, errGoalUnsupportedTuple) {
				t.Fatalf("selector %q want errGoalUnsupportedTuple, got %v", name, err)
			}
			assertGoalLaunchSharedConfigUnchanged(t, root, cfgBefore)
			assertGoalLaunchArgvAbsent(t, argvPath)
		})
	}
	t.Run("mismatched-git-grant", func(t *testing.T) {
		root, _, argvPath, home, _, _, tk, cfgBefore := setupInvocationSandboxLaunch(t)
		writeGrokSandboxTOML(t, filepath.Join(home, "sandbox.toml"), testInvocationSandboxProfile, "workspace", []string{filepath.Join(t.TempDir(), "other.git")})
		headlessGoalStdin(t)
		err := cmdWorkflow([]string{"goal-launch", "-root", root, "-task", tk.ID, "-manual", "-sandbox", testInvocationSandboxProfile})
		if err == nil {
			t.Fatal("mismatched git grant must reject before launch")
		}
		if !errors.Is(err, errGoalUnsupportedTuple) {
			t.Fatalf("want errGoalUnsupportedTuple, got %v", err)
		}
		assertGoalLaunchSharedConfigUnchanged(t, root, cfgBefore)
		assertGoalLaunchArgvAbsent(t, argvPath)
	})
}

func TestGoalLaunchUsageAndHostedManualMutex(t *testing.T) {
	err := cmdWorkflow([]string{"goal-launch"})
	if err == nil || !strings.Contains(err.Error(), "-budget") || !strings.Contains(err.Error(), "-sandbox") {
		t.Fatalf("usage must name -budget and -sandbox: %v", err)
	}
	err = cmdWorkflow([]string{"goal-launch", "-hosted", "-manual", "-task", "x"})
	if err == nil || !strings.Contains(err.Error(), "不能同时") {
		t.Fatalf("-hosted and -manual together must still error: %v", err)
	}

	old := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w
	helpErr := cmdWorkflow([]string{"goal-launch", "-h"})
	_ = w.Close()
	os.Stderr = old
	helpOut, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	text := string(helpOut)
	if helpErr == nil {
		t.Fatal("goal-launch -h must return a help error")
	}
	if !strings.Contains(text, "-budget") || !strings.Contains(text, "-sandbox") {
		t.Fatalf("help must name -budget and -sandbox:\n%s", text)
	}
}
