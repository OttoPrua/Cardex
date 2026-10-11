package main

import "testing"

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
