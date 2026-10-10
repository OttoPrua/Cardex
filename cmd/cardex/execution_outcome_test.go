package main

import "testing"

func TestR05DecisionParity(t *testing.T) {
	t.Parallel()
	facts := executionAttemptFacts{Attempts: 0, MaxAttempts: 3}
	success := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, SuccessStructure: true, SemanticEvents: 1, ModelEvents: 1, WorkspaceHasDiff: true}, facts)
	if success.Kind != executionDecisionSuccess {
		t.Fatalf("success: %+v", success)
	}
	verifyFail := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, SuccessStructure: true, VerifyFailed: true, SemanticEvents: 1, WorkspaceHasDiff: true}, facts)
	if verifyFail.Kind != executionDecisionHeld || verifyFail.Status != statusHeld {
		t.Fatalf("verify-fail: %+v", verifyFail)
	}
	quota := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, QuotaHit: true}, facts)
	if quota.Kind != executionDecisionLimitPause || quota.Status != statusLimitPaused {
		t.Fatalf("quota: %+v", quota)
	}
	auth := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, AuthFailed: true}, facts)
	if auth.Kind != executionDecisionHeld || auth.FailureClass != failureAuth || auth.ConsumesAttempt {
		t.Fatalf("auth: %+v", auth)
	}
	tooLong := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, InputTooLong: true}, facts)
	if tooLong.Kind != executionDecisionFailed || tooLong.FailureClass != failureInputTooLong || tooLong.ConsumesAttempt {
		t.Fatalf("input-too-long: %+v", tooLong)
	}
	cancel := decideExecutionOutcome(executionObservation{Canceled: true, SuccessStructure: true, QuotaHit: true}, facts)
	if cancel.Kind != executionDecisionCancel || cancel.Status != statusCanceled {
		t.Fatalf("cancel must precede other outcomes: %+v", cancel)
	}
	unknown := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: false}, facts)
	if unknown.Kind != executionDecisionUnknown || unknown.AllowsRetry {
		t.Fatalf("unknown must not default to retry: %+v", unknown)
	}
	emptyDenied := decideExecutionOutcome(executionObservation{Complete: true, HasTerminal: true, NeedsInteraction: true, NecessaryDenied: true}, facts)
	if emptyDenied.Kind != executionDecisionHeld || emptyDenied.ConsumesAttempt || emptyDenied.Status != statusHeld {
		t.Fatalf("R01 empty SUCCESS+denied: %+v", emptyDenied)
	}
}

func TestR05NoReplayFromWorkspaceOnly(t *testing.T) {
	t.Parallel()
	facts := executionAttemptFacts{Attempts: 0, MaxAttempts: 3}
	toolNoDiff := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, ToolEvents: 2, WorkspaceHasDiff: false, FailureClass: failureUnknown, Reason: "retry?",
	}, facts)
	if toolNoDiff.AllowsRetry || toolNoDiff.Kind == executionDecisionRetry {
		t.Fatalf("no-diff with tool events must not authorize retry: %+v", toolNoDiff)
	}
	remoteUnknown := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, RemoteUnknown: true, WorkspaceHasDiff: true, FailureClass: failureTimeout,
	}, facts)
	if remoteUnknown.AllowsRetry || remoteUnknown.Kind == executionDecisionRetry {
		t.Fatalf("remote unknown must not authorize retry: %+v", remoteUnknown)
	}
	incompleteTimeout := decideExecutionOutcome(executionObservation{
		Complete: false, Incomplete: true, HasTerminal: false, FailureClass: failureTimeout,
	}, facts)
	if incompleteTimeout.AllowsRetry || incompleteTimeout.Kind == executionDecisionRetry {
		t.Fatalf("incomplete+timeout must not retry from error class: %+v", incompleteTimeout)
	}
	toolCrash := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, ToolEvents: 3, WorkspaceHasDiff: true, FailureClass: failureExecutorCrash,
	}, facts)
	if toolCrash.AllowsRetry || toolCrash.Kind == executionDecisionRetry {
		t.Fatalf("tool-event+crash must not retry from error class: %+v", toolCrash)
	}
	zeroIncomplete := decideExecutionOutcome(executionObservation{
		Complete: false, Incomplete: true, HasTerminal: false, FailureClass: failureTimeout,
	}, facts)
	if zeroIncomplete.AllowsRetry || zeroIncomplete.Kind == executionDecisionRetry {
		t.Fatalf("zero-events+incomplete must not retry: %+v", zeroIncomplete)
	}
	legalTimeout := decideExecutionOutcome(executionObservation{
		Complete: true, HasTerminal: true, SemanticEvents: 1, ModelEvents: 1, WorkspaceHasDiff: true, FailureClass: failureTimeout,
	}, facts)
	if legalTimeout.Kind != executionDecisionRetry || !legalTimeout.AllowsRetry {
		t.Fatalf("complete timeout with no tool effects may retry: %+v", legalTimeout)
	}
}

func TestR05ApprovalNotTerminal(t *testing.T) {
	t.Parallel()
	dec := decideExecutionOutcome(executionObservation{
		WaitingApproval: true, Complete: true, SuccessStructure: true, QuotaHit: true,
	}, executionAttemptFacts{Attempts: 1, MaxAttempts: 3})
	if dec.Kind != executionDecisionWaiting {
		t.Fatalf("waiting must not be classified by the pure decision as completed/failed: %+v", dec)
	}
	if dec.Status == statusDone || dec.Status == statusFailed {
		t.Fatalf("waiting status must not be done/failed: %+v", dec)
	}
}
