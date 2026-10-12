package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	extraRoundAltBudgetTokens       = int64(250000)
	extraRoundAltHardTimeoutSeconds = int64(1800)
)

const extraRoundSyntheticSession = "00000000-0000-4000-8000-0000000000e1"

func extraRoundExhaustedFixture(t *testing.T) (root string, cfg *Config, wf *WorkflowRecord, tk *Task, receipt string) {
	t.Helper()
	root, dir := workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	wf = initTestWorkflow(t, root, dir, "-max-rounds", "1")
	tk = admitManualWriter(t, root, cfg, wf)
	tk.SessionID = extraRoundSyntheticSession
	tk.Goal.Observation = goalObsUnknown
	tk.Goal.LastNativeStatus = "budget_limited"
	tk.Goal.ObservationNote = "budget_limited held unknown fixture"
	tk.Goal.BudgetTokens = 12000
	tk.Goal.HardTimeoutSec = 3600
	tk.RawUsage = &taskRawUsage{Schema: "cardex.usage.v1", BoundIdentity: "old-writer-usage"}
	tk.Status = statusHeld
	revokeScheduling(tk)
	seedExitedAttempt(t, root, tk, "att-extra-round")
	wf.CurrentRound = wf.MaxRounds
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	*tk = *loaded
	receipt = writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     tk.ID,
		"consumed_session_id":  tk.SessionID,
		"consumed_attempt_id":  tk.Goal.BoundAttemptID,
		"consumed_revision":    tk.Revision,
		"consumed_observation": "budget_limited",
	})
	return root, cfg, wf, tk, receipt
}

func mintExtraRoundAuthorization(t *testing.T, wf *WorkflowRecord, tk *Task, receipt string, mutate func(*workflowExtraRoundGrant)) string {
	t.Helper()
	fresh, err := loadExternalDesignReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	grant := workflowExtraRoundGrant{
		Kind:               extraRoundKind,
		WorkflowID:         wf.ID,
		CurrentRound:       wf.CurrentRound,
		BaseMaxRounds:      wf.MaxRounds,
		PreviousTaskID:     tk.ID,
		PreviousSessionID:  tk.SessionID,
		PreviousAttemptID:  goalAttemptID(tk),
		PreviousRevision:   tk.Revision,
		DesignDigest:       fresh.Digest,
		HumanSource:        "synthetic-human-source",
		HumanMessage:       "synthetic-human-message",
		HumanEvidence:      "synthetic-human-evidence",
		BudgetTokens:       extraRoundBudgetTokens,
		HardTimeoutSeconds: extraRoundHardTimeoutSeconds,
		MaxAttempts:        extraRoundMaxAttempts,
		NoAutoRetry:        true,
		IssuedAt:           "2026-10-12T00:00:00Z",
		Nonce:              "xr-fake-" + tk.ID,
	}
	if mutate != nil {
		mutate(&grant)
	}
	raw, err := json.MarshalIndent(grant, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "extra-round-auth.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func snapshotOldWriterBytes(t *testing.T, root string, tk *Task) (taskBytes, attemptBytes []byte) {
	t.Helper()
	var err error
	taskBytes, err = os.ReadFile(taskPath(root, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	attemptBytes, err = os.ReadFile(attemptPath(root, tk.ID, goalAttemptID(tk)))
	if err != nil {
		t.Fatal(err)
	}
	return taskBytes, attemptBytes
}

func extraRoundDesignResultArgs(wfID, root, receipt, auth string) []string {
	return []string{
		"design-result", wfID,
		"-root", root,
		"-design-receipt", receipt,
		"-decision", "successor",
		"-observation", "budget_limited",
		"-extra-round-authorization", auth,
	}
}

func countExtraRoundWriters(t *testing.T, root, workflowID string) int {
	t.Helper()
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tk := range tasks {
		if tk != nil && tk.WorkflowID == workflowID && tk.Goal != nil && tk.Goal.ExtraRoundAuthorized {
			n++
		}
	}
	return n
}

func TestOrdinarySuccessorHonorsExhaustedOneOne(t *testing.T) {
	t.Parallel()
	root, cfg, wf, _, receipt := extraRoundExhaustedFixture(t)
	beforeID := wf.WriterTaskID
	next, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt)
	if err == nil && next != nil {
		t.Fatalf("exhausted 1/1 still created successor %s", next.ID)
	}
	if next != nil {
		t.Fatalf("CurrentRound==MaxRounds must create no writer, got %s", next.ID)
	}
	if !errors.Is(err, errWorkflowRoundsExceeded) {
		t.Fatalf("want rounds exhausted, got %v", err)
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.WriterTaskID != beforeID || disk.MaxRounds != 1 || extraRoundHasConsumption(disk) {
		t.Fatalf("ordinary exhaustion mutated identity: writer=%s max=%d extra=%v", disk.WriterTaskID, disk.MaxRounds, disk.ExtraRoundConsumptions)
	}
}

func TestExtraRoundAdmitsOneManualGoalWriter(t *testing.T) {
	t.Parallel()
	root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
	taskBefore, attemptBefore := snapshotOldWriterBytes(t, root, old)
	auth := mintExtraRoundAuthorization(t, wf, old, receipt, nil)
	next, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth)
	if err != nil {
		t.Fatalf("authorized extra-round: %v", err)
	}
	if next == nil || next.ID == old.ID {
		t.Fatal("extra-round must create a distinct writer")
	}
	if next.Goal == nil || next.Goal.WriterMode != goalWriterManual {
		t.Fatalf("new writer must be manual Goal: %+v", next.Goal)
	}
	if next.Goal.BudgetTokens != extraRoundBudgetTokens || next.Goal.HardTimeoutSec != extraRoundHardTimeoutSeconds {
		t.Fatalf("granted bounds missing: budget=%d timeout=%d", next.Goal.BudgetTokens, next.Goal.HardTimeoutSec)
	}
	if next.MaxAttempts != extraRoundMaxAttempts || next.AutomaticCodex || next.Goal.Observation != goalObsUnstarted {
		t.Fatalf("one-attempt unstarted no-auto: max_attempts=%d auto=%v obs=%s", next.MaxAttempts, next.AutomaticCodex, next.Goal.Observation)
	}
	if next.SessionID != "" || next.ActiveAttemptID != "" || next.Goal.BoundAttemptID != "" || next.Goal.NativeGoalID != "" || next.Goal.Started {
		t.Fatalf("new identity copied old native session: session=%q attempt=%q bound=%q goal=%q started=%v",
			next.SessionID, next.ActiveAttemptID, next.Goal.BoundAttemptID, next.Goal.NativeGoalID, next.Goal.Started)
	}
	if !next.Goal.ExtraRoundAuthorized {
		t.Fatal("new writer must carry extra-round freeze")
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.CurrentRound != 2 || disk.MaxRounds != 1 || disk.WriterTaskID != next.ID {
		t.Fatalf("round/max/writer: round=%d max=%d writer=%s", disk.CurrentRound, disk.MaxRounds, disk.WriterTaskID)
	}
	if !extraRoundHasConsumption(disk) {
		t.Fatal("consumption missing")
	}
	taskAfter, err := os.ReadFile(taskPath(root, old.ID))
	if err != nil {
		t.Fatal(err)
	}
	attemptAfter, err := os.ReadFile(attemptPath(root, old.ID, goalAttemptID(old)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(taskAfter, taskBefore) {
		t.Fatalf("old task bytes changed\nbefore:\n%s\nafter:\n%s", taskBefore, taskAfter)
	}
	if !bytes.Equal(attemptAfter, attemptBefore) {
		t.Fatalf("old attempt bytes changed")
	}
	held, err := loadTask(root, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held.Status != statusHeld || held.Goal.Observation != goalObsUnknown {
		t.Fatalf("old writer must stay held/unknown: status=%s obs=%s", held.Status, held.Goal.Observation)
	}
	if held.schedulingAllowed() {
		t.Fatal("old held task was requeued")
	}
	show := captureWorkflowShow(t, root, disk.ID)
	if got := intFromShow(t, show["max_rounds"]); got != 1 {
		t.Fatalf("show max_rounds=%v", show["max_rounds"])
	}
	if got := intFromShow(t, show["current_round"]); got != 2 {
		t.Fatalf("show current_round=%v", show["current_round"])
	}
	extra, ok := show["extra_round"].(map[string]any)
	if !ok {
		t.Fatalf("show missing extra_round: %v", show)
	}
	if intFromShow(t, extra["original_max_rounds"]) != 1 || intFromShow(t, extra["authorized_extra_round"]) != 2 {
		t.Fatalf("show extra_round=%v", extra)
	}
	consumed, ok := extra["consumed"].(map[string]any)
	if !ok || consumed["grant"] == nil {
		t.Fatalf("show missing consumed grant bindings: %v", extra)
	}
}

func intFromShow(t *testing.T, v any) int {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			t.Fatal(err)
		}
		return int(i)
	default:
		t.Fatalf("not a number: %T %v", v, v)
		return 0
	}
}

func TestExtraRoundRefusalMatrix(t *testing.T) {
	t.Parallel()
	type caseSpec struct {
		name   string
		mutate func(*workflowExtraRoundGrant)
		auth   func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, receipt string) string
		prep   func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task)
		dec    string
		obs    string
	}
	cases := []caseSpec{
		{name: "missing-file", auth: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, receipt string) string {
			return filepath.Join(t.TempDir(), "missing-extra-round.json")
		}},
		{name: "malformed-json", auth: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, receipt string) string {
			path := filepath.Join(t.TempDir(), "bad.json")
			if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "wrong-workflow", mutate: func(g *workflowExtraRoundGrant) { g.WorkflowID = "wf-fake-other" }},
		{name: "wrong-round", mutate: func(g *workflowExtraRoundGrant) { g.CurrentRound = 0 }},
		{name: "wrong-base-max", mutate: func(g *workflowExtraRoundGrant) { g.BaseMaxRounds = 9 }},
		{name: "wrong-task", mutate: func(g *workflowExtraRoundGrant) { g.PreviousTaskID = "t-fake-other" }},
		{name: "wrong-session", mutate: func(g *workflowExtraRoundGrant) { g.PreviousSessionID = "00000000-0000-4000-8000-0000000000aa" }},
		{name: "wrong-attempt", mutate: func(g *workflowExtraRoundGrant) { g.PreviousAttemptID = "att-other" }},
		{name: "wrong-revision", mutate: func(g *workflowExtraRoundGrant) { g.PreviousRevision = tkRevisionPlus(g.PreviousRevision) }},
		{name: "wrong-digest", mutate: func(g *workflowExtraRoundGrant) {
			g.DesignDigest = strings.Repeat("ab", 32)
		}},
		{name: "wrong-human-identity", mutate: func(g *workflowExtraRoundGrant) { g.HumanSource = "" }},
		{name: "drifted-human-message", mutate: func(g *workflowExtraRoundGrant) { g.HumanMessage = " \t " }},
		{name: "non-positive-budget", mutate: func(g *workflowExtraRoundGrant) { g.BudgetTokens = 0 }},
		{name: "negative-budget", mutate: func(g *workflowExtraRoundGrant) { g.BudgetTokens = -1 }},
		{name: "non-finite-budget", auth: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, receipt string) string {
			path := mintExtraRoundAuthorization(t, wf, tk, receipt, nil)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			patched := strings.Replace(string(raw), `"budget_tokens": 400000`, `"budget_tokens": 1e400`, 1)
			if patched == string(raw) {
				t.Fatal("failed to patch budget_tokens for non-finite case")
			}
			if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "non-positive-timeout", mutate: func(g *workflowExtraRoundGrant) { g.HardTimeoutSeconds = 0 }},
		{name: "non-finite-timeout", auth: func(t *testing.T, root string, wf *WorkflowRecord, tk *Task, receipt string) string {
			path := mintExtraRoundAuthorization(t, wf, tk, receipt, nil)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			patched := strings.Replace(string(raw), `"hard_timeout_seconds": 5400`, `"hard_timeout_seconds": 1e400`, 1)
			if patched == string(raw) {
				t.Fatal("failed to patch hard_timeout_seconds for non-finite case")
			}
			if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "non-representable-timeout", mutate: func(g *workflowExtraRoundGrant) { g.HardTimeoutSeconds = math.MaxInt64 }},
		{name: "wrong-attempts", mutate: func(g *workflowExtraRoundGrant) { g.MaxAttempts = 3 }},
		{name: "auto-retry", mutate: func(g *workflowExtraRoundGrant) { g.NoAutoRetry = false }},
		{name: "stale-design"},
		{name: "paused", obs: "paused", prep: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task) {
			tk.Goal.Observation = goalObsHeld
			tk.Goal.Continuation = "same-session"
			tk.Goal.LastNativeStatus = ""
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "needs-input", obs: "needs-input", prep: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task) {
			tk.Goal.Observation = goalObsHeld
			tk.Goal.Continuation = "same-session"
			tk.Goal.LastNativeStatus = "needs-input"
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unreclaimed-custody", prep: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task) {
			if err := os.Remove(attemptPath(root, tk.ID, goalAttemptID(tk))); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "active-dispatchable", prep: func(t *testing.T, root string, cfg *Config, wf *WorkflowRecord, tk *Task) {
			restoreScheduling(tk)
			tk.Status = statusQueued
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "revise-with-grant", dec: goalDecisionRevise},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, cfg, wf, tk, receipt := extraRoundExhaustedFixture(t)
			if tc.prep != nil {
				tc.prep(t, root, cfg, wf, tk)
				loaded, err := loadTask(root, tk.ID)
				if err != nil {
					t.Fatal(err)
				}
				*tk = *loaded
				if tc.obs == "paused" || tc.obs == "needs-input" {
					receipt = writeAstraDesignReceiptWith(t, map[string]any{
						"consumed_task_id":     tk.ID,
						"consumed_session_id":  tk.SessionID,
						"consumed_attempt_id":  tk.Goal.BoundAttemptID,
						"consumed_revision":    tk.Revision,
						"consumed_observation": tc.obs,
					})
				}
			}
			if tc.name == "stale-design" {
				fresh, err := loadExternalDesignReceipt(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if wf.DesignLineage == nil {
					wf.DesignLineage = &WorkflowDesignLineage{}
				}
				wf.DesignLineage.LastConsumedDigest = fresh.Digest
				wf.DesignLineage.LastConsumedTaskID = tk.ID
				wf.DesignLineage.LastConsumedRevision = tk.Revision
				if err := persistWorkflow(root, cfg, wf); err != nil {
					t.Fatal(err)
				}
			}
			beforeID := wf.WriterTaskID
			maxBefore := wf.MaxRounds
			auth := ""
			if tc.auth != nil {
				auth = tc.auth(t, root, wf, tk, receipt)
			} else {
				auth = mintExtraRoundAuthorization(t, wf, tk, receipt, tc.mutate)
			}
			dec := tc.dec
			if dec == "" {
				dec = goalDecisionSuccessor
			}
			obs := tc.obs
			if obs == "" {
				obs = "budget_limited"
			}
			next, err := applyWorkflowDesignResultAuth(root, cfg, wf, dec, obs, receipt, auth)
			if err == nil {
				t.Fatalf("expected refusal, admitted %v", next)
			}
			if next != nil {
				t.Fatalf("refusal created writer %s", next.ID)
			}
			disk, loadErr := loadWorkflow(root, cfg, wf.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if disk.WriterTaskID != beforeID || disk.MaxRounds != maxBefore {
				t.Fatalf("refusal mutated writer/max: writer=%s/%s max=%d/%d", disk.WriterTaskID, beforeID, disk.MaxRounds, maxBefore)
			}
			if extraRoundHasConsumption(disk) && tc.name != "already-consumed" {
				t.Fatalf("%s consumed grant on refusal", tc.name)
			}
			if countExtraRoundWriters(t, root, wf.ID) != 0 {
				t.Fatalf("%s left an extra-round writer", tc.name)
			}
		})
	}
}

func tkRevisionPlus(rev int64) int64 { return rev + 99 }

func TestExtraRoundReplayAndNoGrantAfterConsumption(t *testing.T) {
	t.Parallel()
	root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
	auth := mintExtraRoundAuthorization(t, wf, old, receipt, nil)
	next, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth); err == nil {
		t.Fatal("replayed grant must fail")
	}
	if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt); !errors.Is(err, errWorkflowRoundsExceeded) && err == nil {
		t.Fatalf("ordinary successor after extra-round: %v", err)
	}
	fresh2 := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id":     next.ID,
		"consumed_session_id":  next.SessionID,
		"consumed_attempt_id":  goalAttemptID(next),
		"consumed_revision":    next.Revision,
		"consumed_observation": "budget_limited",
	})
	if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionRevise, "budget_limited", fresh2); err == nil {
		t.Fatal("ordinary revise after extra-round must fail")
	}
	if _, err := admitWorkflowRepair(root, cfg, wf, "p1", "summary"); !errors.Is(err, errGoalDesignResult) {
		t.Fatalf("ordinary repair after extra-round: %v", err)
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.WriterTaskID != next.ID || disk.MaxRounds != 1 {
		t.Fatalf("replay mutated identity: writer=%s max=%d", disk.WriterTaskID, disk.MaxRounds)
	}
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	writers := 0
	for _, tk := range tasks {
		if tk != nil && tk.WorkflowID == wf.ID && tk.Goal != nil && tk.IntegrationGate == nil && tk.Type == typeSequence {
			writers++
		}
	}
	if writers != 2 {
		t.Fatalf("want original+one extra writer, got %d", writers)
	}
}

func TestExtraRoundConcurrentAdmissionOneWriter(t *testing.T) {
	root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
	auth := mintExtraRoundAuthorization(t, wf, old, receipt, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var admitted []*Task
	var errs []error
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			next, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			admitted = append(admitted, next)
		}()
	}
	wg.Wait()
	if len(admitted) != 1 {
		t.Fatalf("concurrent extra-round admitted %d writers errs=%v", len(admitted), errs)
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.WriterTaskID != admitted[0].ID || disk.CurrentRound != 2 || disk.MaxRounds != 1 {
		t.Fatalf("concurrent result: writer=%s round=%d max=%d", disk.WriterTaskID, disk.CurrentRound, disk.MaxRounds)
	}
}

func TestExtraRoundPublishFailureDoesNotLeaveExecutableWriter(t *testing.T) {
	root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
	auth := mintExtraRoundAuthorization(t, wf, old, receipt, nil)
	beforeID := wf.WriterTaskID
	taskBefore, attemptBefore := snapshotOldWriterBytes(t, root, old)
	goalWriterPublishHook = func(tk *Task) error {
		if tk != nil && tk.Goal != nil && tk.Goal.ExtraRoundAuthorized {
			return extraRoundRefused("forced publish failure")
		}
		return nil
	}
	t.Cleanup(func() { goalWriterPublishHook = nil })
	next, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth)
	if err == nil {
		t.Fatal("forced publish failure succeeded")
	}
	if next != nil {
		t.Fatalf("publish failure returned writer %s", next.ID)
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.WriterTaskID != beforeID || disk.MaxRounds != 1 || disk.CurrentRound != 1 {
		t.Fatalf("publish failure mutated identity: writer=%s round=%d/%d", disk.WriterTaskID, disk.CurrentRound, disk.MaxRounds)
	}
	if extraRoundHasConsumption(disk) {
		t.Fatal("pre-publication failure consumed an unused grant")
	}
	if countExtraRoundWriters(t, root, wf.ID) != 0 {
		t.Fatal("publish hook failure left an extra-round writer")
	}
	taskAfter, err := os.ReadFile(taskPath(root, beforeID))
	if err != nil {
		t.Fatal(err)
	}
	attemptAfter, err := os.ReadFile(attemptPath(root, beforeID, goalAttemptID(old)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(taskAfter, taskBefore) || !bytes.Equal(attemptAfter, attemptBefore) {
		t.Fatal("pre-publication failure rewrote old evidence")
	}

	goalWriterPublishHook = nil
	retried, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth)
	if err != nil {
		t.Fatalf("unused grant must remain retryable after pre-publication failure: %v", err)
	}
	if retried == nil || retried.ID == beforeID {
		t.Fatal("retry of unused grant must admit exactly one extra writer")
	}
	if countExtraRoundWriters(t, root, wf.ID) != 1 {
		t.Fatalf("retry admitted %d extra writers", countExtraRoundWriters(t, root, wf.ID))
	}
	if _, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth); err == nil {
		t.Fatal("successful extra-round must consume the grant")
	}
	if countExtraRoundWriters(t, root, wf.ID) != 1 {
		t.Fatal("post-success replay created another extra writer")
	}
}

func TestExtraRoundVisibleWriterPersistFailDoesNotDuplicate(t *testing.T) {
	root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
	auth := mintExtraRoundAuthorization(t, wf, old, receipt, nil)
	beforeID := wf.WriterTaskID
	taskBefore, attemptBefore := snapshotOldWriterBytes(t, root, old)
	wfID := wf.ID
	persistWorkflowAfterProgressHook = func(rec *WorkflowRecord) error {
		if rec != nil && rec.ID == wfID && extraRoundHasConsumption(rec) {
			return extraRoundRefused("forced persist failure after progress")
		}
		return nil
	}
	t.Cleanup(func() { persistWorkflowAfterProgressHook = nil })
	next, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth)
	if err == nil {
		t.Fatal("forced persist failure succeeded")
	}
	if next != nil {
		t.Fatalf("persist failure returned writer %s", next.ID)
	}
	if countExtraRoundWriters(t, root, wf.ID) != 1 {
		t.Fatalf("visible extra writer must remain after persist failure, got %d", countExtraRoundWriters(t, root, wf.ID))
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if extraRoundHasConsumption(disk) {
		t.Fatal("failed record write must not pretend consumption committed")
	}
	if disk.WriterTaskID != beforeID || disk.MaxRounds != 1 {
		t.Fatalf("persist failure mutated record identity: writer=%s max=%d", disk.WriterTaskID, disk.MaxRounds)
	}
	taskAfter, err := os.ReadFile(taskPath(root, beforeID))
	if err != nil {
		t.Fatal(err)
	}
	attemptAfter, err := os.ReadFile(attemptPath(root, beforeID, goalAttemptID(old)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(taskAfter, taskBefore) || !bytes.Equal(attemptAfter, attemptBefore) {
		t.Fatal("persist failure deleted or rewrote old evidence")
	}
	persistWorkflowAfterProgressHook = nil
	if _, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth); err == nil {
		t.Fatal("visible extra writer must not make the grant reusable")
	}
	if countExtraRoundWriters(t, root, wf.ID) != 1 {
		t.Fatalf("retry after visible persist-fail admitted %d extra writers", countExtraRoundWriters(t, root, wf.ID))
	}
}

func TestExtraRoundLoadAllowsConsumedTwoOverOne(t *testing.T) {
	t.Parallel()
	root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
	auth := mintExtraRoundAuthorization(t, wf, old, receipt, nil)
	if _, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth); err != nil {
		t.Fatal(err)
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.CurrentRound != 2 || disk.MaxRounds != 1 {
		t.Fatalf("loaded 2/1: %d/%d", disk.CurrentRound, disk.MaxRounds)
	}
	rawPath := workflowPath(root, wf.ID)
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	delete(obj, "extra_round_consumptions")
	delete(obj, "extra_round_grants")
	stripped, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rawPath, stripped, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWorkflow(root, cfg, wf.ID); err == nil {
		t.Fatal("2/1 without consumed grant must be malformed")
	}
}

func TestExtraRoundCLIAdmitAndReplayInProcessAndCandidate(t *testing.T) {
	run := func(t *testing.T, viaBinary bool) {
		t.Helper()
		root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
		auth := mintExtraRoundAuthorization(t, wf, old, receipt, nil)
		args := extraRoundDesignResultArgs(wf.ID, root, receipt, auth)
		var stdout, stderr string
		var err error
		if viaBinary {
			bin := resolveCardexCandidateBinary(t)
			stdout, stderr, err = runCardexWorkflowProcess(t, bin, args...)
			if err != nil {
				t.Fatalf("candidate admit: %v stderr=%s stdout=%s", err, stderr, stdout)
			}
		} else {
			stdout, err = captureWorkflowCmd(t, args...)
			if err != nil {
				t.Fatalf("in-process admit: %v stdout=%s", err, stdout)
			}
		}
		if !strings.Contains(stdout, "design-result=successor") {
			t.Fatalf("admit stdout missing successor: %s", stdout)
		}
		t.Logf("admit stdout:\n%s", stdout)
		if viaBinary {
			t.Logf("admit stderr:\n%s", stderr)
		}
		disk, loadErr := loadWorkflow(root, cfg, wf.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if disk.WriterTaskID == old.ID || disk.CurrentRound != 2 || disk.MaxRounds != 1 {
			t.Fatalf("admit state: writer=%s round=%d/%d", disk.WriterTaskID, disk.CurrentRound, disk.MaxRounds)
		}
		if !strings.Contains(stdout, disk.WriterTaskID) {
			t.Fatalf("admit stdout must print new writer id %s: %s", disk.WriterTaskID, stdout)
		}
		var replayOut, replayErrText string
		var replayErr error
		if viaBinary {
			bin := resolveCardexCandidateBinary(t)
			replayOut, replayErrText, replayErr = runCardexWorkflowProcess(t, bin, args...)
		} else {
			replayOut, replayErr = captureWorkflowCmd(t, args...)
		}
		t.Logf("replay stdout:\n%s", replayOut)
		if replayErrText != "" {
			t.Logf("replay stderr:\n%s", replayErrText)
		}
		if replayErr == nil {
			t.Fatal("identical extra-round invocation must fail")
		}
		t.Logf("replay error: %v", replayErr)
		show := captureWorkflowShow(t, root, wf.ID)
		showRaw, _ := json.MarshalIndent(show, "", "  ")
		t.Logf("workflow show JSON:\n%s", showRaw)
		if intFromShow(t, show["max_rounds"]) != 1 || intFromShow(t, show["current_round"]) != 2 {
			t.Fatalf("show after CLI: %v", show)
		}
		if show["extra_round"] == nil {
			t.Fatal("show missing extra_round")
		}
	}
	t.Run("in-process", func(t *testing.T) { run(t, false) })
	t.Run("candidate-binary", func(t *testing.T) { run(t, true) })
}

func runCardexWorkflowProcess(t *testing.T, bin string, workflowArgs ...string) (stdout, stderr string, err error) {
	t.Helper()
	args := append([]string{"workflow"}, workflowArgs...)
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func TestExtraRoundFakeLaunchBindsGrantedBounds(t *testing.T) {
	cases := []struct {
		name    string
		budget  int64
		timeout int64
	}{
		{name: "backend-example", budget: extraRoundBudgetTokens, timeout: extraRoundHardTimeoutSeconds},
		{name: "grant-owned", budget: extraRoundAltBudgetTokens, timeout: extraRoundAltHardTimeoutSeconds},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for pass := 1; pass <= 2; pass++ {
				pass := pass
				t.Run("root-"+strconv.Itoa(pass), func(t *testing.T) {
					root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
					auth := mintExtraRoundAuthorization(t, wf, old, receipt, func(g *workflowExtraRoundGrant) {
						g.BudgetTokens = tc.budget
						g.HardTimeoutSeconds = tc.timeout
					})
					next, err := applyWorkflowDesignResultAuth(root, cfg, wf, goalDecisionSuccessor, "budget_limited", receipt, auth)
					if err != nil {
						t.Fatal(err)
					}
					if next.Goal.BudgetTokens != tc.budget || next.Goal.HardTimeoutSec != tc.timeout {
						t.Fatalf("admit bound grant values budget=%d timeout=%d want %d/%d",
							next.Goal.BudgetTokens, next.Goal.HardTimeoutSec, tc.budget, tc.timeout)
					}
					argv := filepath.Join(t.TempDir(), "argv")
					enableManualGrok(t, root, cfg, argv)
					headlessGoalStdin(t)
					if err := launchManualWorkflowGoal(root, cfg, wf, tc.budget+1, ""); err == nil {
						t.Fatal("widening -budget must be refused")
					}
					oldStdout := os.Stdout
					r, w, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					os.Stdout = w
					launchErr := launchManualWorkflowGoal(root, cfg, wf, 0, "")
					_ = w.Close()
					os.Stdout = oldStdout
					out, _ := io.ReadAll(r)
					_ = r.Close()
					if launchErr != nil {
						t.Fatalf("pass %d fake launch: %v\n%s", pass, launchErr, out)
					}
					text := string(out)
					wantBudget := "--budget " + strconv.FormatInt(tc.budget, 10)
					wantTimeout := "hard_timeout_seconds: " + strconv.FormatInt(tc.timeout, 10)
					if !strings.Contains(text, wantBudget) {
						t.Fatalf("pass %d copyable /goal missing %s:\n%s", pass, wantBudget, text)
					}
					if !strings.Contains(text, wantTimeout) {
						t.Fatalf("pass %d contract missing %s:\n%s", pass, wantTimeout, text)
					}
					t.Logf("pass %d fake launch stdout:\n%s", pass, text)
					reloaded, err := loadTask(root, wf.WriterTaskID)
					if err != nil {
						t.Fatal(err)
					}
					if reloaded.Goal.BudgetTokens != tc.budget || reloaded.Goal.HardTimeoutSec != tc.timeout || reloaded.MaxAttempts != extraRoundMaxAttempts || reloaded.AutomaticCodex {
						t.Fatalf("launch mutated frozen bounds: budget=%d timeout=%d attempts=%d auto=%v",
							reloaded.Goal.BudgetTokens, reloaded.Goal.HardTimeoutSec, reloaded.MaxAttempts, reloaded.AutomaticCodex)
					}
				})
			}
		})
	}
}

func TestExtraRoundHelpDocumentsFlag(t *testing.T) {
	t.Parallel()
	err := cmdWorkflow(nil)
	if err == nil {
		t.Fatal("bare workflow must print usage")
	}
	msg := err.Error()
	if !strings.Contains(msg, "-extra-round-authorization") {
		t.Fatalf("usage missing extra-round flag: %s", msg)
	}
	if !strings.Contains(msg, "budget_tokens") || !strings.Contains(msg, "hard_timeout_seconds") {
		t.Fatalf("usage missing grant-owned bounds: %s", msg)
	}
}

func TestExtraRoundBudgetNeverRounds(t *testing.T) {
	for _, raw := range []string{"1.0000000000000001", "9007199254740995.0", "9223372036854775808", "1e400", "0", "-1", ""} {
		if got, err := extraRoundParsePositiveInt64(json.Number(raw), "budget_tokens"); err == nil {
			t.Errorf("invalid grant %q silently accepted as %d", raw, got)
		}
	}
	for _, want := range []int64{1, 400000, 9007199254740995, math.MaxInt64} {
		got, err := extraRoundParsePositiveInt64(json.Number(strconv.FormatInt(want, 10)), "budget_tokens")
		if err != nil || got != want {
			t.Errorf("exact integer %d became %d: %v", want, got, err)
		}
	}
}

func TestExtraRoundCandidateLaunchGrantedBounds(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real controlling-TTY command uses macOS script")
	}
	bin := resolveCardexCandidateBinary(t)
	for _, budget := range []int64{extraRoundBudgetTokens, extraRoundAltBudgetTokens} {
		t.Run(strconv.FormatInt(budget, 10), func(t *testing.T) {
			root, cfg, wf, old, receipt := extraRoundExhaustedFixture(t)
			taskBefore, attemptBefore := snapshotOldWriterBytes(t, root, old)
			timeout := extraRoundHardTimeoutSeconds
			if budget == extraRoundAltBudgetTokens {
				timeout = extraRoundAltHardTimeoutSeconds
			}
			auth := mintExtraRoundAuthorization(t, wf, old, receipt, func(g *workflowExtraRoundGrant) {
				g.BudgetTokens, g.HardTimeoutSeconds = budget, timeout
			})
			out, stderr, err := runCardexWorkflowProcess(t, bin, extraRoundDesignResultArgs(wf.ID, root, receipt, auth)...)
			if err != nil {
				t.Fatalf("binary admission: %v stdout=%s stderr=%s", err, out, stderr)
			}
			argv := filepath.Join(t.TempDir(), "argv")
			enableManualGrok(t, root, cfg, argv)
			out, stderr, err = runCardexWorkflowProcess(t, bin, "goal-run", wf.ID, "-root", root, "-manual", "-budget", strconv.FormatInt(budget+1, 10))
			if err == nil || !strings.Contains(stderr, "budget is frozen") {
				t.Fatalf("binary widened budget: %v stdout=%s stderr=%s", err, out, stderr)
			}
			if _, err := os.Stat(argv); !os.IsNotExist(err) {
				t.Fatal("refused budget launched a provider")
			}
			args := []string{bin, "workflow", "goal-run", wf.ID, "-root", root, "-manual"}
			for i, arg := range args {
				args[i] = shSingleQuote(arg)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "script", "-q", "/dev/null", "/bin/sh", "-c", strings.Join(args, " ")+"; code=$?; printf '\\nCARDEX_BINARY_EXIT=%s\\n' \"$code\"; exit \"$code\"")
			raw, err := cmd.CombinedOutput()
			if err != nil || !bytes.Contains(raw, []byte("CARDEX_BINARY_EXIT=0")) {
				t.Fatalf("binary fake launch: %v\n%s", err, raw)
			}
			for _, want := range []string{"--budget " + strconv.FormatInt(budget, 10), "hard_timeout_seconds: " + strconv.FormatInt(timeout, 10)} {
				if !bytes.Contains(raw, []byte(want)) {
					t.Fatalf("binary contract missing %s:\n%s", want, raw)
				}
			}
			if _, err := os.ReadFile(argv); err != nil {
				t.Fatalf("fake provider was not invoked: %v", err)
			}
			out, stderr, err = runCardexWorkflowProcess(t, bin, "show", wf.ID, "-root", root)
			if err != nil {
				t.Fatalf("binary show: %v %s", err, stderr)
			}
			var show map[string]any
			if err := json.Unmarshal([]byte(out), &show); err != nil {
				t.Fatal(err)
			}
			if intFromShow(t, show["current_round"]) != 2 || intFromShow(t, show["max_rounds"]) != 1 || show["extra_round"] == nil {
				t.Fatalf("binary show lost extra-round: %s", out)
			}
			disk, err := loadWorkflow(root, cfg, wf.ID)
			if err != nil {
				t.Fatal(err)
			}
			next, err := loadTask(root, disk.WriterTaskID)
			if err != nil {
				t.Fatal(err)
			}
			if next.Goal.BudgetTokens != budget || next.Goal.HardTimeoutSec != timeout || next.MaxAttempts != 1 || next.AutomaticCodex {
				t.Fatal("binary launch changed grant bounds")
			}
			taskAfter, attemptAfter := snapshotOldWriterBytes(t, root, old)
			if !bytes.Equal(taskBefore, taskAfter) || !bytes.Equal(attemptBefore, attemptAfter) {
				t.Fatal("binary launch changed predecessor evidence")
			}
			t.Logf("binary=%s grant=%d/%d new_writer=%s round=2/1 max_attempts=1 auto=false fake_launch_exit=0 old_evidence_unchanged=true", bin, budget, timeout, next.ID)
		})
	}
}
