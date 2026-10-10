package main

import "strings"

// executionDecisionKind is the smallest shared next-step recommendation.
// Adapters map protocol into this view; persist/CAS and the existing
// success/verify pipeline remain the only state writers.
type executionDecisionKind string

const (
	executionDecisionCancel     executionDecisionKind = "cancel"
	executionDecisionWaiting    executionDecisionKind = "waiting"
	executionDecisionSuccess    executionDecisionKind = "success"
	executionDecisionHeld       executionDecisionKind = "held"
	executionDecisionFailed     executionDecisionKind = "failed"
	executionDecisionRetry      executionDecisionKind = "retry"
	executionDecisionLimitPause executionDecisionKind = "limit_paused"
	executionDecisionUnknown    executionDecisionKind = "unknown"
)

// executionObservation is a protocol-free view. Callers must not pass raw
// stdout/stderr/response bodies.
type executionObservation struct {
	Complete         bool
	HasTerminal      bool
	Canceled         bool
	WaitingApproval  bool
	NeedsInteraction bool
	SuccessStructure bool
	QuotaHit         bool
	AuthFailed       bool
	InputTooLong     bool
	NecessaryDenied  bool
	VerifyFailed     bool
	WorkspaceHasDiff bool
	RemoteUnknown    bool
	Incomplete       bool
	ToolEvents       int
	SemanticEvents   int
	ModelEvents      int
	FailureClass     failureClass
	Reason           string
}

type executionAttemptFacts struct {
	Attempts    int
	MaxAttempts int
}

type executionDecision struct {
	Kind            executionDecisionKind
	ConsumesAttempt bool
	AllowsRetry     bool
	Status          string
	Reason          string
	FailureClass    failureClass
}

func decideExecutionOutcome(obs executionObservation, facts executionAttemptFacts) executionDecision {
	reason := strings.TrimSpace(obs.Reason)
	if obs.Canceled {
		return executionDecision{
			Kind: executionDecisionCancel, Status: statusCanceled,
			Reason: firstNonBlank(reason, "canceled"), FailureClass: obs.FailureClass,
		}
	}
	if obs.WaitingApproval {
		return executionDecision{
			Kind: executionDecisionWaiting, Reason: firstNonBlank(reason, "waiting_approval"),
			FailureClass: obs.FailureClass,
		}
	}
	if obs.NeedsInteraction || obs.NecessaryDenied {
		return executionDecision{
			Kind: executionDecisionHeld, Status: statusHeld,
			Reason:       firstNonBlank(reason, "needs_interaction"),
			FailureClass: failurePermission,
		}
	}
	if obs.QuotaHit {
		return executionDecision{
			Kind: executionDecisionLimitPause, Status: statusLimitPaused,
			Reason: firstNonBlank(reason, "quota"), FailureClass: obs.FailureClass,
		}
	}
	if obs.AuthFailed {
		return executionDecision{
			Kind: executionDecisionHeld, Status: statusHeld,
			Reason: firstNonBlank(reason, "auth"), FailureClass: failureAuth,
		}
	}
	if obs.InputTooLong {
		return executionDecision{
			Kind: executionDecisionFailed, Status: statusFailed,
			Reason: firstNonBlank(reason, "input_too_long"), FailureClass: failureInputTooLong,
		}
	}
	if obs.VerifyFailed && obs.SuccessStructure {
		return executionDecision{
			Kind: executionDecisionHeld, Status: statusHeld,
			Reason: firstNonBlank(reason, "verify_failed"), FailureClass: obs.FailureClass,
		}
	}
	if obs.SuccessStructure && !obs.VerifyFailed {
		return executionDecision{
			Kind: executionDecisionSuccess, Status: statusDone,
			Reason: firstNonBlank(reason, "success"), FailureClass: obs.FailureClass,
		}
	}
	if replayForbidden(obs) {
		return executionDecision{
			Kind: executionDecisionUnknown, Status: statusHeld,
			Reason: firstNonBlank(reason, "unknown_no_replay"), FailureClass: failureUnknown,
		}
	}
	if (obs.FailureClass == failureTimeout || obs.FailureClass == failureExecutorCrash) && retryEvidenceSufficient(obs) {
		return retryDecision(obs, facts, reason)
	}
	if !obs.HasTerminal || obs.Incomplete || !obs.Complete || obs.RemoteUnknown {
		return executionDecision{
			Kind: executionDecisionUnknown, Status: statusHeld,
			Reason: firstNonBlank(reason, "unknown_terminal"), FailureClass: failureUnknown,
		}
	}
	return executionDecision{
		Kind: executionDecisionUnknown, Status: statusHeld,
		Reason: firstNonBlank(reason, "unknown_terminal"), FailureClass: failureUnknown,
	}
}

// replayForbidden is true when the observation must not authorize another
// provider product call. Timeout and crash classes are not exemptions: those
// executions can already have had effects.
func replayForbidden(obs executionObservation) bool {
	if obs.RemoteUnknown {
		return true
	}
	if obs.Incomplete || !obs.Complete {
		return true
	}
	if !obs.HasTerminal {
		return true
	}
	if obs.ToolEvents > 0 {
		return true
	}
	zeroEvents := obs.ToolEvents == 0 && obs.SemanticEvents == 0 && obs.ModelEvents == 0
	if !obs.WorkspaceHasDiff && zeroEvents {
		return true
	}
	return false
}

func retryEvidenceSufficient(obs executionObservation) bool {
	return obs.Complete && !obs.Incomplete && obs.HasTerminal && obs.ToolEvents == 0 && !obs.RemoteUnknown
}

func retryDecision(obs executionObservation, facts executionAttemptFacts, reason string) executionDecision {
	if replayForbidden(obs) || !retryEvidenceSufficient(obs) {
		return executionDecision{
			Kind: executionDecisionUnknown, Status: statusHeld,
			Reason: firstNonBlank(reason, "unknown_no_replay"), FailureClass: obs.FailureClass,
		}
	}
	maxAttempts := facts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	next := facts.Attempts + 1
	if next >= maxAttempts {
		return executionDecision{
			Kind: executionDecisionFailed, ConsumesAttempt: true, Status: statusFailed,
			Reason: firstNonBlank(reason, "max_attempts"), FailureClass: obs.FailureClass,
		}
	}
	return executionDecision{
		Kind: executionDecisionRetry, ConsumesAttempt: true, AllowsRetry: true, Status: statusQueued,
		Reason: firstNonBlank(reason, string(obs.FailureClass)), FailureClass: obs.FailureClass,
	}
}
