package main

import "strings"

// executionDecisionKind is the smallest shared next-step recommendation.
// Adapters map protocol into this view; persist/CAS and the existing
// success/verify pipeline remain the only state writers.
type executionDecisionKind string

const (
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
	NeedsInteraction bool
	SuccessStructure bool
	QuotaHit         bool
	AuthFailed       bool
	InputTooLong     bool
	NecessaryDenied  bool
	WorkspaceHasDiff bool
	RemoteUnknown    bool
	Incomplete       bool
	ToolEvents       int
	SemanticEvents   int
	ModelEvents      int
	FailureClass     failureClass
	Reason           string
}

type executionDecision struct {
	Kind         executionDecisionKind
	Reason       string
	FailureClass failureClass
}

func baseExecutionObservation(t *Task, res *claudeResult) executionObservation {
	obs := executionObservation{}
	if t != nil && t.Dir != "" {
		files := nativeWorktreeRelFiles(t.Dir)
		obs.WorkspaceHasDiff = nativeWorktreeHasDiff(t.Dir, files)
	}
	if res == nil {
		obs.Incomplete = true
		obs.Reason = "missing_result"
		return obs
	}
	obs.Complete = res.ObservationComplete
	obs.Incomplete = !res.ObservationComplete
	obs.HasTerminal = res.TerminalEvents > 0
	obs.ToolEvents = res.ToolEvents
	obs.SemanticEvents = res.SemanticEvents
	obs.ModelEvents = res.ModelEvents
	return obs
}

func applyProviderSession(t *Task, res *claudeResult) {
	if t != nil && res != nil && res.SessionID != "" {
		t.SessionID = res.SessionID
	}
}

func applyProviderSessionAndUsage(root string, t *Task, res *claudeResult) {
	if res == nil {
		return
	}
	applyProviderResultUsage(root, t, res)
	applyProviderSession(t, res)
}

func stampRouteFailure(t *Task, kind, class string) {
	if t == nil || t.LastRouteAttempt == nil {
		return
	}
	if kind != "" {
		t.LastRouteAttempt.FailureKind = kind
	}
	if class != "" {
		t.LastRouteAttempt.FailureClass = class
	}
}

func decideExecutionOutcome(obs executionObservation) executionDecision {
	reason := strings.TrimSpace(obs.Reason)
	if obs.NeedsInteraction || obs.NecessaryDenied {
		return executionDecision{
			Kind:         executionDecisionHeld,
			Reason:       firstNonBlank(reason, "needs_interaction"),
			FailureClass: failurePermission,
		}
	}
	if obs.QuotaHit {
		return executionDecision{
			Kind:         executionDecisionLimitPause,
			Reason:       firstNonBlank(reason, "quota"),
			FailureClass: obs.FailureClass,
		}
	}
	if obs.AuthFailed {
		return executionDecision{
			Kind:         executionDecisionHeld,
			Reason:       firstNonBlank(reason, "auth"),
			FailureClass: failureAuth,
		}
	}
	if obs.InputTooLong {
		return executionDecision{
			Kind:         executionDecisionFailed,
			Reason:       firstNonBlank(reason, "input_too_long"),
			FailureClass: failureInputTooLong,
		}
	}
	if obs.SuccessStructure {
		return executionDecision{
			Kind:         executionDecisionSuccess,
			Reason:       firstNonBlank(reason, "success"),
			FailureClass: obs.FailureClass,
		}
	}
	if replayForbidden(obs) {
		return executionDecision{
			Kind:         executionDecisionUnknown,
			Reason:       firstNonBlank(reason, "unknown_no_replay"),
			FailureClass: failureUnknown,
		}
	}
	if (obs.FailureClass == failureTimeout || obs.FailureClass == failureExecutorCrash) && retryEvidenceSufficient(obs) {
		return executionDecision{
			Kind:         executionDecisionRetry,
			Reason:       firstNonBlank(reason, string(obs.FailureClass)),
			FailureClass: obs.FailureClass,
		}
	}
	return executionDecision{
		Kind:         executionDecisionUnknown,
		Reason:       firstNonBlank(reason, "unknown_terminal"),
		FailureClass: failureUnknown,
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
