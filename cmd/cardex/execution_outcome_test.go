package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestR05DecisionParity(t *testing.T) {
	t.Parallel()
	success := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, SuccessStructure: true, SemanticEvents: 1, ModelEvents: 1, WorkspaceHasDiff: true})
	if success.Kind != executionDecisionSuccess {
		t.Fatalf("success: %+v", success)
	}
	quota := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, QuotaHit: true})
	if quota.Kind != executionDecisionLimitPause {
		t.Fatalf("quota: %+v", quota)
	}
	auth := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, AuthFailed: true})
	if auth.Kind != executionDecisionHeld || auth.FailureClass != failureAuth {
		t.Fatalf("auth: %+v", auth)
	}
	tooLong := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, InputTooLong: true})
	if tooLong.Kind != executionDecisionFailed || tooLong.FailureClass != failureInputTooLong {
		t.Fatalf("input-too-long: %+v", tooLong)
	}
	unknown := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: false})
	if unknown.Kind != executionDecisionUnknown {
		t.Fatalf("unknown must not default to retry: %+v", unknown)
	}
	emptyDenied := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, NeedsInteraction: true, NecessaryDenied: true})
	if emptyDenied.Kind != executionDecisionHeld || emptyDenied.FailureClass != failurePermission {
		t.Fatalf("R01 empty SUCCESS+denied: %+v", emptyDenied)
	}
}

func TestR05NoReplayFromWorkspaceOnly(t *testing.T) {
	t.Parallel()
	toolNoDiff := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, ToolEvents: 2, WorkspaceHasDiff: false, FailureClass: failureUnknown, Reason: "retry?",
	})
	if toolNoDiff.Kind == executionDecisionRetry {
		t.Fatalf("no-diff with tool events must not authorize retry: %+v", toolNoDiff)
	}
	remoteUnknown := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, RemoteUnknown: true, WorkspaceHasDiff: true, FailureClass: failureTimeout,
	})
	if remoteUnknown.Kind == executionDecisionRetry {
		t.Fatalf("remote unknown must not authorize retry: %+v", remoteUnknown)
	}
	incompleteTimeout := decideExecutionOutcome(executionObservation{
		Complete: false, Incomplete: true, HasTerminal: false, FailureClass: failureTimeout,
	})
	if incompleteTimeout.Kind == executionDecisionRetry {
		t.Fatalf("incomplete+timeout must not retry from error class: %+v", incompleteTimeout)
	}
	toolCrash := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, ToolEvents: 3, WorkspaceHasDiff: true, FailureClass: failureExecutorCrash,
	})
	if toolCrash.Kind == executionDecisionRetry {
		t.Fatalf("tool-event+crash must not retry from error class: %+v", toolCrash)
	}
	zeroIncomplete := decideExecutionOutcome(executionObservation{
		Complete: false, Incomplete: true, HasTerminal: false, FailureClass: failureTimeout,
	})
	if zeroIncomplete.Kind == executionDecisionRetry {
		t.Fatalf("zero-events+incomplete must not retry: %+v", zeroIncomplete)
	}
	legalTimeout := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, SemanticEvents: 1, ModelEvents: 1, WorkspaceHasDiff: true, FailureClass: failureTimeout,
	})
	if legalTimeout.Kind != executionDecisionRetry {
		t.Fatalf("complete timeout with no tool effects may retry: %+v", legalTimeout)
	}
	cappedTimeout := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, SemanticEvents: 1, ModelEvents: 1, WorkspaceHasDiff: true, FailureClass: failureTimeout,
	})
	if cappedTimeout.Kind != executionDecisionRetry {
		t.Fatalf("attempt cap is owned by applyFailureDisposition, core must not recommend failed: %+v", cappedTimeout)
	}
}

func r05GitWorktree(t *testing.T, dirty bool) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestR05DelayedWorktreeObservation(t *testing.T) {
	t.Parallel()
	dirty := r05GitWorktree(t, true)
	clean := r05GitWorktree(t, false)
	timeoutErr := fmt.Errorf("步骤超时（1 分钟）")

	agySuccess := parseAntigravityJSON([]byte(`{"status":"SUCCESS","response":"exact-success-body","conversation_id":"sess-ok","num_turns":1,"usage":{"input_tokens":3,"output_tokens":2}}`))
	successObs := observationFromAntigravity(&Task{Type: typeSequence, Dir: dirty}, agySuccess, nil)
	if successObs.WorkspaceHasDiff {
		t.Fatalf("determined success must not extra-scan worktree: %+v", successObs)
	}
	if got := decideExecutionOutcome(successObs); got.Kind != executionDecisionSuccess {
		t.Fatalf("success: %+v", got)
	}

	agyQuota := parseAntigravityJSON([]byte(`{"status":"SUCCESS","response":"looks done","error":"quota exceeded"}`))
	quotaObs := observationFromAntigravity(&Task{Type: typeSequence, Dir: dirty}, agyQuota, nil)
	if quotaObs.WorkspaceHasDiff {
		t.Fatalf("determined quota must not extra-scan worktree: %+v", quotaObs)
	}
	if got := decideExecutionOutcome(quotaObs); got.Kind != executionDecisionLimitPause {
		t.Fatalf("quota: %+v", got)
	}

	agyDenied := parseAntigravityJSON([]byte(`{"conversation_id":"sess-denied","status":"SUCCESS","response":"","denied_actions":[{"action":"command","display_name":"RunCommand"}]}`))
	deniedObs := observationFromAntigravity(&Task{Type: typeSequence, Dir: dirty}, agyDenied, nil)
	if deniedObs.WorkspaceHasDiff {
		t.Fatalf("determined denied must not extra-scan worktree: %+v", deniedObs)
	}
	if got := decideExecutionOutcome(deniedObs); got.Kind != executionDecisionHeld || got.FailureClass != failurePermission {
		t.Fatalf("denied: %+v", got)
	}

	agyAuth := parseAntigravityJSON([]byte(`{"status":"ERROR","error":"unauthorized token expired"}`))
	authObs := observationFromAntigravity(&Task{Type: typeSequence, Dir: dirty}, agyAuth, nil)
	if !authObs.AuthFailed {
		t.Fatalf("auth fixture must classify as auth: %+v", authObs)
	}
	if authObs.WorkspaceHasDiff {
		t.Fatalf("determined auth must not extra-scan worktree: %+v", authObs)
	}
	if got := decideExecutionOutcome(authObs); got.Kind != executionDecisionHeld || got.FailureClass != failureAuth {
		t.Fatalf("auth: %+v", got)
	}

	agyInput := parseAntigravityJSON([]byte(`{"status":"ERROR","error":"prompt is too long"}`))
	inputObs := observationFromAntigravity(&Task{Type: typeSequence, Dir: dirty}, agyInput, nil)
	if !inputObs.InputTooLong {
		t.Fatalf("input fixture must classify as input-too-long: %+v", inputObs)
	}
	if inputObs.WorkspaceHasDiff {
		t.Fatalf("determined input-too-long must not extra-scan worktree: %+v", inputObs)
	}
	if got := decideExecutionOutcome(inputObs); got.Kind != executionDecisionFailed || got.FailureClass != failureInputTooLong {
		t.Fatalf("input-too-long: %+v", got)
	}

	unknownRes := parseAntigravityJSON([]byte(`{}`))
	unknownObs := observationFromAntigravity(&Task{Type: typeSequence, Dir: dirty}, unknownRes, nil)
	if unknownObs.WorkspaceHasDiff {
		t.Fatalf("unknown without timeout/crash must not extra-scan worktree: %+v", unknownObs)
	}
	if got := decideExecutionOutcome(unknownObs); got.Kind != executionDecisionUnknown {
		t.Fatalf("unknown: %+v", got)
	}

	timeoutRes := &claudeResult{ObservationComplete: true, TerminalEvents: 1, IsError: true, Subtype: antigravitySubtypeUnknownTerminal}
	retryObs := observationFromAntigravity(&Task{Type: typeSequence, Dir: dirty}, timeoutRes, timeoutErr)
	if !retryObs.WorkspaceHasDiff {
		t.Fatalf("timeout/retry still needs the worktree snapshot: %+v", retryObs)
	}
	if got := decideExecutionOutcome(retryObs); got.Kind != executionDecisionRetry || got.FailureClass != failureTimeout {
		t.Fatalf("dirty timeout must retry from snapshot: %+v obs=%+v", got, retryObs)
	}
	noDiffObs := observationFromAntigravity(&Task{Type: typeSequence, Dir: clean}, timeoutRes, timeoutErr)
	if noDiffObs.WorkspaceHasDiff {
		t.Fatalf("clean timeout snapshot must be empty: %+v", noDiffObs)
	}
	if got := decideExecutionOutcome(noDiffObs); got.Kind != executionDecisionUnknown {
		t.Fatalf("clean timeout must not replay: %+v obs=%+v", got, noDiffObs)
	}

	grokSuccess := &claudeResult{ObservationComplete: true, TerminalEvents: 1, Result: "GROK_OK"}
	gSuccess, _ := observationFromGrok(&Task{Dir: dirty}, grokSuccess, "", nil)
	if gSuccess.WorkspaceHasDiff {
		t.Fatalf("determined Grok success must not extra-scan worktree: %+v", gSuccess)
	}
	if got := decideExecutionOutcome(gSuccess); got.Kind != executionDecisionSuccess {
		t.Fatalf("grok success: %+v", got)
	}

	grokQuota := &claudeResult{ObservationComplete: true, TerminalEvents: 1, IsError: true, Result: "HTTP 429: usage limit reached"}
	gQuota, _ := observationFromGrok(&Task{Dir: dirty}, grokQuota, "HTTP 429: usage limit reached", fmt.Errorf("HTTP 429: usage limit reached"))
	if !gQuota.QuotaHit {
		t.Fatalf("grok quota fixture must set QuotaHit: %+v", gQuota)
	}
	if gQuota.WorkspaceHasDiff {
		t.Fatalf("determined Grok quota must not extra-scan worktree: %+v", gQuota)
	}
	if got := decideExecutionOutcome(gQuota); got.Kind != executionDecisionLimitPause {
		t.Fatalf("grok quota: %+v", got)
	}

	grokTimeout := &claudeResult{ObservationComplete: true, TerminalEvents: 1, IsError: true, Result: "步骤超时（1 分钟）"}
	gRetry, _ := observationFromGrok(&Task{Dir: dirty}, grokTimeout, "", timeoutErr)
	if !gRetry.WorkspaceHasDiff {
		t.Fatalf("Grok timeout/retry still needs the worktree snapshot: %+v", gRetry)
	}
	if got := decideExecutionOutcome(gRetry); got.Kind != executionDecisionRetry || got.FailureClass != failureTimeout {
		t.Fatalf("dirty Grok timeout must retry from snapshot: %+v obs=%+v", got, gRetry)
	}
	gUnknown, _ := observationFromGrok(&Task{Dir: clean}, grokTimeout, "", timeoutErr)
	if gUnknown.WorkspaceHasDiff {
		t.Fatalf("clean Grok timeout snapshot must be empty: %+v", gUnknown)
	}
	if got := decideExecutionOutcome(gUnknown); got.Kind != executionDecisionUnknown {
		t.Fatalf("clean Grok timeout must not replay: %+v obs=%+v", got, gUnknown)
	}
}

func TestR05WorktreeScanFollowsDecide(t *testing.T) {
	t.Parallel()
	cases := []executionObservation{
		{Complete: true, HasTerminal: true, SuccessStructure: true, SemanticEvents: 1, ModelEvents: 1},
		{Complete: true, HasTerminal: true, QuotaHit: true},
		{Complete: true, HasTerminal: true, AuthFailed: true},
		{Complete: true, HasTerminal: true, InputTooLong: true},
		{Complete: true, HasTerminal: true, NeedsInteraction: true, NecessaryDenied: true},
		{Complete: true, HasTerminal: false},
		{Complete: true, HasTerminal: true, ToolEvents: 2, FailureClass: failureUnknown, Reason: "retry?"},
		{Complete: true, HasTerminal: true, RemoteUnknown: true, FailureClass: failureTimeout},
		{Complete: false, Incomplete: true, HasTerminal: false, FailureClass: failureTimeout},
		{Complete: true, HasTerminal: true, ToolEvents: 3, FailureClass: failureExecutorCrash},
		{Complete: true, HasTerminal: true, SemanticEvents: 1, ModelEvents: 1, FailureClass: failureTimeout},
		{Complete: true, HasTerminal: true, FailureClass: failureTimeout},
		{Complete: true, HasTerminal: true, FailureClass: failureExecutorCrash},
		{Complete: true, HasTerminal: true},
		{Complete: true, HasTerminal: true, Reason: "empty_success"},
	}
	for i, obs := range cases {
		without := obs
		without.WorkspaceHasDiff = false
		with := obs
		with.WorkspaceHasDiff = true
		left := decideExecutionOutcome(without)
		right := decideExecutionOutcome(with)
		want := left != right
		if got := observationNeedsWorktree(obs); got != want {
			t.Fatalf("case %d needs=%v want=%v obs=%+v without=%+v with=%+v", i, got, want, obs, left, right)
		}
	}
	timeoutZero := executionObservation{Complete: true, HasTerminal: true, FailureClass: failureTimeout}
	if !observationNeedsWorktree(timeoutZero) {
		t.Fatalf("timeout zero-event must still request the snapshot: %+v / %+v",
			decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, FailureClass: failureTimeout, WorkspaceHasDiff: false}),
			decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, FailureClass: failureTimeout, WorkspaceHasDiff: true}))
	}
	success := executionObservation{Complete: true, HasTerminal: true, SuccessStructure: true, SemanticEvents: 1, ModelEvents: 1}
	if observationNeedsWorktree(success) {
		t.Fatalf("determined success must not request a scan: %+v", decideExecutionOutcome(success))
	}
	timeoutWithEvents := executionObservation{Complete: true, HasTerminal: true, SemanticEvents: 1, ModelEvents: 1, FailureClass: failureTimeout}
	if observationNeedsWorktree(timeoutWithEvents) {
		t.Fatalf("timeout with semantic/model events does not consult workspace: %+v", decideExecutionOutcome(timeoutWithEvents))
	}
}
