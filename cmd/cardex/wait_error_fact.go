package main

import (
	"strings"
)

// Shared wait/error facts projected onto list, board, and digest from existing
// task/result/log/LastRouteAttempt/Goal/permission-pending evidence. Display
// only: this is not a second status store and is never persisted on the card.
const (
	waitClassInFlight          = "in_flight"
	waitClassPermissionWaiting = "permission_waiting"
	waitClassFailure           = "failure"
	waitClassUnknown           = "unknown"
	waitClassBudgetLimited     = "budget_limited"
	waitClassAccountQuota      = "account_quota"
	waitClassTransport         = "transport"
	waitClassAuth              = "auth"
	waitClassPermission        = "permission"
	waitClassNetwork           = "network"
	waitClassCancellation      = "cancellation"
)

type waitErrorFact struct {
	Class  string `json:"wait_error_class,omitempty"`
	Reason string `json:"wait_error_reason,omitempty"`
}

// listTaskView overlays shared wait/error facts onto the existing list -json
// task object without persisting a second status store.
type listTaskView struct {
	*Task
	WaitErrorClass  string `json:"wait_error_class,omitempty"`
	WaitErrorReason string `json:"wait_error_reason,omitempty"`
}

func classifyWaitError(root string, t *Task) waitErrorFact {
	if t == nil {
		return waitErrorFact{Class: waitClassUnknown, Reason: "missing evidence; unknown"}
	}
	if pending, why := waitErrorPermissionPending(root, t); pending {
		return waitErrorFact{Class: waitClassPermissionWaiting, Reason: why}
	}
	if t.Status == statusCanceled || (t.Goal != nil && t.Goal.Observation == goalObsCanceled) {
		return waitErrorFact{Class: waitClassCancellation, Reason: firstWaitEvidence(t.LastError, t.LastSummary, "canceled")}
	}
	if t.Status == statusRunning || (t.Goal != nil && t.Goal.Observation == goalObsRunning) {
		return waitErrorFact{Class: waitClassInFlight, Reason: firstWaitEvidence(t.LastSummary, "in flight")}
	}
	if nativeBudgetLimited(t) {
		return waitErrorFact{Class: waitClassBudgetLimited, Reason: firstWaitEvidence(goalObservation(t), t.LastError, "native soft-budget stop (budget_limited); not accepted")}
	}
	if auth, why := waitErrorAuth(t); auth {
		return waitErrorFact{Class: waitClassAuth, Reason: why}
	}
	if perm, why := waitErrorPermissionHeld(t); perm {
		return waitErrorFact{Class: waitClassPermission, Reason: why}
	}
	if net, why := waitErrorNetwork(t); net {
		return waitErrorFact{Class: waitClassNetwork, Reason: why}
	}
	if transport, why := waitErrorTransport(t); transport {
		return waitErrorFact{Class: waitClassTransport, Reason: why}
	}
	if quota, why := waitErrorAccountQuota(t); quota {
		return waitErrorFact{Class: waitClassAccountQuota, Reason: why}
	}
	if t.Status == statusFailed || (t.Goal != nil && t.Goal.Observation == goalObsFailed) || strings.TrimSpace(t.LastError) != "" {
		return waitErrorFact{Class: waitClassFailure, Reason: firstWaitEvidence(t.LastError, t.LastSummary, goalObservation(t), "failed")}
	}
	if t.Status == statusHeld || (t.Goal != nil && t.Goal.Observation == goalObsUnknown) || strings.TrimSpace(t.Status) == "" {
		return waitErrorFact{Class: waitClassUnknown, Reason: firstWaitEvidence(t.LastError, t.LastSummary, goalObservation(t), "missing evidence; unknown")}
	}
	return waitErrorFact{}
}

func attachWaitErrorFact(root string, br *TaskBrief, t *Task) {
	if br == nil || t == nil {
		return
	}
	fact := classifyWaitError(root, t)
	br.WaitErrorClass = fact.Class
	br.WaitErrorReason = fact.Reason
}

func waitErrorCurrentLiveCall(t *Task) bool {
	if t == nil || t.terminal() {
		return false
	}
	if t.Goal != nil && t.Goal.Observation == goalObsCanceled {
		return false
	}
	return t.Status == statusRunning || (t.Goal != nil && t.Goal.Observation == goalObsRunning)
}

func waitErrorPermissionPending(root string, t *Task) (bool, string) {
	if !waitErrorCurrentLiveCall(t) {
		return false, ""
	}
	currentAttempt := strings.TrimSpace(t.ActiveAttemptID)
	if t.Goal != nil && currentAttempt == "" {
		currentAttempt = strings.TrimSpace(t.Goal.BoundAttemptID)
	}
	if root != "" {
		pending, err := listGrokPermissionPending(root, t.ID)
		if err == nil {
			for _, p := range pending {
				if !waitErrorPendingBoundToCurrent(t, currentAttempt, p) {
					continue
				}
				why := "waiting for grok-permission allow-once"
				if p.RequestID != "" {
					why += "; request=" + p.RequestID
				}
				if p.ToolCallID != "" {
					why += " tool=" + p.ToolCallID
				}
				return true, why
			}
		}
	}
	if t.SessionID != "" && nativePermissionPromptPending(grokNativeEventsPath(goalChildGrokHome(t), t.Dir, t.SessionID)) {
		return true, "native permission prompt pending; observe with workflow goal-input -observe"
	}
	return false, ""
}

func waitErrorPendingBoundToCurrent(t *Task, currentAttempt string, p grokPermissionPending) bool {
	if p.State != grokPermissionPendingState || p.TaskID != t.ID {
		return false
	}
	if currentAttempt == "" || p.AttemptID != currentAttempt {
		return false
	}
	if t.SessionID != "" && p.SessionID != "" && p.SessionID != t.SessionID {
		return false
	}
	return true
}

func nativeBudgetLimited(t *Task) bool {
	if t == nil || t.Goal == nil {
		return false
	}
	if normalizeNativeStatus(t.Goal.LastNativeStatus) == "budget_limited" {
		return true
	}
	note := strings.ToLower(strings.TrimSpace(t.Goal.ObservationNote))
	return strings.Contains(note, "budget_limited")
}

func waitErrorAuth(t *Task) (bool, string) {
	if failureClassOf(t) == string(failureAuth) || strings.HasPrefix(strings.TrimSpace(t.LastError), "[auth]") {
		return true, firstWaitEvidence(t.LastError, "auth")
	}
	switch preflightStateOf(t) {
	case providerAuthMissing, providerAuthExpiredRefreshable:
		return true, firstWaitEvidence(preflightReason(t), t.LastError, "auth")
	}
	return false, ""
}

func waitErrorPermissionHeld(t *Task) (bool, string) {
	if failureClassOf(t) == string(failurePermission) || strings.HasPrefix(strings.TrimSpace(t.LastError), "[permission]") {
		return true, firstWaitEvidence(t.LastError, "permission denied")
	}
	if strings.EqualFold(failureKindOf(t), string(grokBuildProcessClassPermissionEnvironment)) {
		return true, firstWaitEvidence(t.LastError, "permission environment")
	}
	return false, ""
}

func waitErrorNetwork(t *Task) (bool, string) {
	if preflightStateOf(t) == providerNetworkProxyUnreachable {
		return true, firstWaitEvidence(preflightReason(t), t.LastError, "network/proxy unreachable")
	}
	scan := waitErrorScan(t)
	if strings.Contains(scan, "proxyconnect") || strings.Contains(scan, "proxy unreachable") ||
		(strings.Contains(scan, "connection refused") && strings.Contains(scan, "127.0.0.1")) {
		return true, firstWaitEvidence(t.LastError, "network/proxy")
	}
	return false, ""
}

func waitErrorTransport(t *Task) (bool, string) {
	kind := strings.ToLower(failureKindOf(t))
	switch kind {
	case string(fallbackTransport), string(providerTransportFailed), string(providerModelUnavailable),
		string(providerModelStartFailed), string(failureTimeout):
		return true, firstWaitEvidence(t.LastError, "transport/connect timeout or provider unavailable; not quota")
	}
	if t != nil && t.FallbackReason == string(fallbackTransport) {
		return true, firstWaitEvidence(t.LastError, "transport/connect timeout or provider unavailable; not quota")
	}
	if failureClassOf(t) == string(failureTimeout) {
		return true, firstWaitEvidence(t.LastError, "transport/connect timeout or provider unavailable; not quota")
	}
	if d := grokDiagnosticsOf(t); d != nil && d.TimedOut != nil && *d.TimedOut {
		return true, firstWaitEvidence(t.LastError, "transport/connect timeout or provider unavailable; not quota")
	}
	scan := waitErrorScan(t)
	if timeoutClassRe.MatchString(scan) ||
		strings.Contains(scan, "connection reset") || strings.Contains(scan, "connection refused") ||
		strings.Contains(scan, "i/o timeout") || strings.Contains(scan, "dial tcp") ||
		strings.Contains(scan, "no such host") || strings.Contains(scan, "enotfound") ||
		strings.Contains(scan, "provider unavailable") || strings.Contains(scan, "model unavailable") ||
		strings.Contains(scan, "transport_failed") || strings.Contains(scan, "deadline exceeded") {
		return true, firstWaitEvidence(t.LastError, "transport/connect timeout or provider unavailable; not quota")
	}
	return false, ""
}

func waitErrorAccountQuota(t *Task) (bool, string) {
	if t != nil && t.Status == statusLimitPaused {
		return true, firstWaitEvidence(t.LastError, "account quota; waiting cooldown/resume")
	}
	kind := strings.ToLower(failureKindOf(t))
	switch kind {
	case string(fallbackQuota), string(providerRateLimited):
		return true, firstWaitEvidence(t.LastError, "account quota")
	}
	if t != nil && t.FallbackReason == string(fallbackQuota) {
		return true, firstWaitEvidence(t.LastError, "account quota")
	}
	scan := waitErrorScan(t)
	if strings.Contains(scan, "用量限额") || strings.Contains(scan, "额度红线") ||
		strings.Contains(scan, "usage limit") || strings.Contains(scan, "quota exceeded") ||
		strings.Contains(scan, "quota exhausted") || strings.Contains(scan, "账号限额") {
		return true, firstWaitEvidence(t.LastError, "account quota")
	}
	return false, ""
}

func waitErrorScan(t *Task) string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(t.LastError)
	b.WriteByte('\n')
	b.WriteString(t.LastSummary)
	b.WriteByte('\n')
	b.WriteString(failureClassOf(t))
	b.WriteByte('\n')
	b.WriteString(failureKindOf(t))
	if t.FallbackReason != "" {
		b.WriteByte('\n')
		b.WriteString(t.FallbackReason)
	}
	if t.Goal != nil {
		b.WriteByte('\n')
		b.WriteString(t.Goal.ObservationNote)
		b.WriteByte('\n')
		b.WriteString(t.Goal.LastNativeStatus)
		b.WriteByte('\n')
		b.WriteString(t.Goal.FailureClass)
	}
	if t.LastProviderPreflight != nil {
		b.WriteByte('\n')
		b.WriteString(string(t.LastProviderPreflight.State))
		b.WriteByte('\n')
		b.WriteString(t.LastProviderPreflight.Reason)
	}
	return strings.ToLower(b.String())
}

func failureClassOf(t *Task) string {
	if t == nil {
		return ""
	}
	if t.LastRouteAttempt != nil && strings.TrimSpace(t.LastRouteAttempt.FailureClass) != "" {
		return strings.TrimSpace(t.LastRouteAttempt.FailureClass)
	}
	if t.Goal != nil {
		return strings.TrimSpace(t.Goal.FailureClass)
	}
	return ""
}

func failureKindOf(t *Task) string {
	if t == nil || t.LastRouteAttempt == nil {
		return ""
	}
	return strings.TrimSpace(t.LastRouteAttempt.FailureKind)
}

func preflightStateOf(t *Task) providerPreflightState {
	if t == nil || t.LastProviderPreflight == nil {
		return ""
	}
	return t.LastProviderPreflight.State
}

func preflightReason(t *Task) string {
	if t == nil || t.LastProviderPreflight == nil {
		return ""
	}
	return strings.TrimSpace(t.LastProviderPreflight.Reason)
}

func grokDiagnosticsOf(t *Task) *grokBuildDiagnostics {
	if t == nil || t.LastRouteAttempt == nil {
		return nil
	}
	return t.LastRouteAttempt.GrokDiagnostics
}

func goalObservation(t *Task) string {
	if t == nil || t.Goal == nil {
		return ""
	}
	return firstWaitEvidence(t.Goal.ObservationNote, t.Goal.LastNativeStatus)
}

func firstWaitEvidence(parts ...string) string {
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			return s
		}
	}
	return ""
}

func formatWaitErrorNote(fact waitErrorFact) string {
	if fact.Class == "" {
		return ""
	}
	if fact.Reason == "" {
		return fact.Class
	}
	return fact.Class + ": " + fact.Reason
}
