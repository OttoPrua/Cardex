package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const mixedRouteReason = "owner_mixed_primary"
const mixedQuotaReason = "owner_mixed_cursor_quota"

const (
	dispatchModeDaily         = "daily"
	dispatchModeCodexPriority = "codex-priority"
)

// dispatchNow is a test seam for dispatch boundaries, never persisted authority.
var dispatchNow = time.Now

var errNamedDispatchModeChanged = errors.New("dispatch mode changed before provider invocation")
var namedDispatchPreflightHook func()
var namedCodexPreparedHook func()

// Re-read only at invocation boundaries. Existing children are never interrupted
// by this function. The caller must close any provisional attempt before requeue.
func checkNamedDispatchInvocation(root string, cfg *Config, t *Task) error {
	if frozenDispatchMode(t) == "" {
		return nil
	}
	latest, err := loadConfig(root)
	if err != nil {
		return fmt.Errorf("dispatch mode config unavailable: %w", err)
	}
	now := dispatchNow()
	if effectiveDispatchMode(latest, now) != frozenDispatchMode(t) {
		return errNamedDispatchModeChanged
	}
	if t.RouteReason == codexSnapshotExhaustedReason && t.Step == 0 && t.SessionID == "" &&
		!codexSnapshotExhausted(latest, now) {
		return fmt.Errorf("quota snapshot no longer proves fresh account exhaustion")
	}
	if t.AutomaticCodex {
		if cd := loadEngineCooldown(root, "codex"); cd.active(now) {
			return fmt.Errorf("confirmed Codex quota cooldown active")
		}
		e := currentAutomaticCodexBudgetEvidence(latest, now)
		if allowed, reason := automaticCodexBudgetAllowed(t, e, latest.AutomaticCodexBudgetStopPercent); !allowed {
			return fmt.Errorf("%s", reason)
		}
	}
	return nil
}

func holdNamedDispatchBeforeInvocation(root string, t *Task, reason string) error {
	t.Status = statusHeld
	t.LastError = "dispatch mode preinvoke hold: " + reason
	t.touch()
	return finishIfStopped(persistTaskEvent(root, t, evHeld, "runner:dispatch-mode", statusHeld, t.Step, withCostTelemetry(map[string]any{"reason": "dispatch_mode_preinvoke_hold", "detail": reason, "provider_started": false}, t)))
}

func effectiveDispatchMode(cfg *Config, now time.Time) string {
	if cfg != nil && cfg.DispatchMode == dispatchModeCodexPriority {
		start, e1 := time.Parse(time.RFC3339, cfg.CodexPriorityStart)
		end, e2 := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
		if e1 == nil && e2 == nil && end.After(start) && !now.Before(start) && now.Before(end) {
			return dispatchModeCodexPriority
		}
	}
	return dispatchModeDaily
}

func frozenDispatchMode(t *Task) string {
	if t != nil {
		if strings.HasPrefix(t.OwnerRouteName, "mixed_priority_") {
			return dispatchModeCodexPriority
		}
		if strings.HasPrefix(t.OwnerRouteName, "mixed_daily_") {
			return dispatchModeDaily
		}
	}
	return ""
}

// WorkClass is an admission-time declaration, never inferred from prompt text.
// Empty means a historical card, which keeps the pre-mixed policy.
func defaultMixedWorkClass(typ string) string {
	switch typ {
	case typeSequence, typeAssembly:
		return "development"
	case typeCoordinate, typeProgressPull:
		return "management"
	default:
		return "gpt-short"
	}
}

func validMixedWorkClass(class string) bool {
	switch class {
	case "", "development", "simple-development", "gpt-complex", "gpt-short", "management":
		return true
	default:
		return false
	}
}

func mixedOwnerTask(t *Task) bool {
	return t != nil && strings.HasPrefix(t.OwnerRouteName, "mixed_")
}

func resolveMixedOwnerRoute(cfg *Config, t *Task) (ownerRoute, bool) {
	return resolveMixedOwnerRouteAt(cfg, t, dispatchNow())
}

func resolveMixedOwnerRouteAt(cfg *Config, t *Task, now time.Time) (ownerRoute, bool) {
	if t == nil {
		return ownerRoute{}, false
	}
	if t.WorkClass == "" || !validMixedWorkClass(t.WorkClass) {
		return ownerRoute{}, false
	}
	risk := effectiveOwnerRiskClass(t)
	leg := policyLeg{Stage: routeStagePrimary, ReadOnly: t.Type != typeSequence}
	name := "mixed_" + strings.ReplaceAll(t.WorkClass, "-", "_")
	mode := frozenDispatchMode(t)
	if mode == "" && t.Goal == nil && t.OwnerRouteName == "" && cfg != nil && cfg.DispatchMode != "" {
		mode = effectiveDispatchMode(cfg, now)
	}
	if mode != "" {
		prefix := "mixed_daily_"
		if mode == dispatchModeCodexPriority {
			prefix = "mixed_priority_"
		}
		name = prefix + strings.ReplaceAll(t.WorkClass, "-", "_")
	}
	kimi := policyLeg{Runner: kimiCLIRunnerName, Model: "kimi-code/k3", Effort: "max", Stage: routeStageFallbackReview, ReadOnly: leg.ReadOnly}
	if mode == dispatchModeCodexPriority {
		leg.Runner, leg.Model, leg.Effort = "codex", "gpt-5.6-sol", "xhigh"
		if risk != riskClassOrdinary || (t.EffortExplicit && t.Effort == "max") {
			leg.Effort = "max"
		}
		if t.WorkClass == "development" || t.WorkClass == "gpt-complex" {
			leg.Model, leg.Effort = "gpt-6-astra", "high"
			if t.EffortExplicit && t.Effort == "medium" {
				leg.Effort = "medium"
			}
		}
		return ownerRoute{Name: name, RiskClass: risk, Legs: []policyLeg{leg, kimi}}, true
	}
	switch t.WorkClass {
	case "development", "simple-development":
		leg.Runner, leg.Model, leg.Effort = grokBuildRunnerName, "grok-4.7", "high"
		cursor := "grok-4.7-high"
		if t.WorkClass == "simple-development" {
			leg.Model, cursor = "grok-4.6", "cursor-grok-4.6-high"
		}
		legs := []policyLeg{leg, {Runner: cursorRunnerName, Model: cursor, Effort: "high", Stage: routeStageFallbackReview, ReadOnly: leg.ReadOnly}}
		if mode != "" {
			legs = append(legs, kimi)
		}
		return ownerRoute{Name: name, RiskClass: risk, Legs: legs}, true
	case "gpt-complex":
		leg.Runner, leg.Model, leg.Effort = "codex", "gpt-6-astra", "high"
		if t.EffortExplicit && t.Effort == "medium" {
			leg.Effort = "medium"
		}
	case "gpt-short":
		leg.Runner, leg.Model, leg.Effort = "codex", "gpt-5.6-sol", "xhigh"
		if risk != riskClassOrdinary || (t.EffortExplicit && t.Effort == "max") {
			leg.Effort = "max"
		}
	case "management":
		leg.ReadOnly = true
		leg.Runner, leg.Model, leg.Effort = antigravityRunnerName, "gemini-3.8-flash-high", "high"
	}
	legs := []policyLeg{leg}
	if mode != "" && leg.Runner == "codex" {
		legs = append(legs, kimi)
	}
	return ownerRoute{Name: name, RiskClass: risk, Legs: legs}, true
}

func pinMixedOwnerPrimary(t *Task, route ownerRoute) bool {
	return pinMixedOwnerLeg(t, route, 0)
}

func pinMixedOwnerLeg(t *Task, route ownerRoute, index int) bool {
	leg := route.Legs[index]
	t.OwnerRouteName, t.OwnerRouteLeg, t.OwnerRouteStage = route.Name, index+1, leg.Stage
	t.RouteReason = mixedRouteReason
	t.RiskClass = route.RiskClass
	t.PreferRunner = leg.Runner
	t.AutomaticCodex = leg.Runner == "codex"
	switch leg.Runner {
	case grokBuildRunnerName:
		t.GrokModel, t.GrokEffort = leg.Model, leg.Effort
	case "codex":
		t.CodexModel, t.Effort, t.EffortExplicit = leg.Model, leg.Effort, true
		t.AutomaticCodex = true
	case antigravityRunnerName:
		t.AgyModel, t.Effort, t.EffortExplicit = leg.Model, leg.Effort, true
	case kimiCLIRunnerName:
		t.KimiModel, t.Effort, t.EffortExplicit = leg.Model, leg.Effort, true
	case cursorRunnerName:
		t.CursorModel, t.Effort, t.EffortExplicit = leg.Model, leg.Effort, true
	default:
		return false
	}
	return true
}

// A proved quota successor may cross the mode boundary only while still queued
// and while its durable zero-work proof and current workspace/custody agree.
// The previous actual attempt and consumed budgets are deliberately untouched.
func refreshQueuedQuotaSuccessor(root string, cfg *Config, t *Task, now time.Time) bool {
	if cfg == nil || t == nil || frozenDispatchMode(t) == "" || frozenDispatchMode(t) == effectiveDispatchMode(cfg, now) ||
		t.Goal != nil || t.Status != statusQueued || t.OwnerRouteLeg < 2 || t.RouteReason != mixedQuotaReason || t.RunnerExplicit || t.ActiveAttemptID != "" || !t.schedulingAllowed() {
		return false
	}
	p := t.LastRouteAttempt
	if p == nil || p.FailureKind != string(fallbackQuota) || !p.ObservationOK || p.SemanticEvents != 0 || p.ModelEvents != 0 || p.ToolEvents != 0 || p.ProcessResidue || p.WorkspaceBefore == "" || p.WorkspaceBefore != p.WorkspaceAfter {
		return false
	}
	if _, ok := resolveOwnerRouteReadback(cfg, t); !ok {
		return false
	}
	if taskProcessResidue(t.ID) || anyTaskProcAlive(t.ID) || workspaceProcessResidue(t.Dir) {
		return false
	}
	fp, err := capturePolicyWorkspaceFingerprint(t.Dir)
	if err != nil || fp.Digest != p.WorkspaceAfter {
		return false
	}
	probe := *t
	clearMixedProviderPins(&probe)
	probe.OwnerRouteName, probe.OwnerRouteLeg, probe.OwnerRouteStage = "", 0, ""
	route, ok := resolveMixedOwnerRouteAt(cfg, &probe, now)
	if !ok {
		return false
	}
	index := 0
	if route.Legs[index].Runner == p.ActualRunner {
		index++
		if index >= len(route.Legs) {
			return false
		}
	}
	if !pinMixedOwnerLeg(&probe, route, index) {
		return false
	}
	probe.RouteReason = mixedQuotaReason
	*t = probe
	return true
}

func validateMixedOwnerConfig(cfg *Config) error {
	if cfg != nil && cfg.DispatchMode != "" {
		if cfg.DispatchMode != dispatchModeDaily && cfg.DispatchMode != dispatchModeCodexPriority {
			return fmt.Errorf("unknown dispatch_mode %q", cfg.DispatchMode)
		}
		if !cfg.OwnerMixedRouting {
			return fmt.Errorf("dispatch_mode requires owner_mixed_routing")
		}
		if cfg.DispatchMode == dispatchModeCodexPriority && strings.TrimSpace(cfg.CodexAllowanceSnapshot) == "" {
			return fmt.Errorf("codex-priority requires codex_allowance_snapshot")
		}
		if cfg.DispatchMode == dispatchModeCodexPriority {
			start, e1 := time.Parse(time.RFC3339, cfg.CodexPriorityStart)
			end, e2 := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
			if e1 != nil || e2 != nil || !end.After(start) || end.Sub(start) > time.Hour {
				return fmt.Errorf("codex-priority requires a finite authorized window of at most one hour")
			}
		}
	}
	if cfg == nil || !cfg.OwnerMixedRouting {
		return nil
	}
	if !cfg.OwnerRoutingEnforced || cfg.DefaultRunner != "codex" || !grokBuildEnabled(cfg) || !cursorEnabled(cfg) || !antigravityEnabled(cfg) || strings.TrimSpace(cfg.CodexBin) == "" {
		return fmt.Errorf("owner_mixed_routing requires owner_routing_enforced, default_runner=codex, and enabled Grok Build, Cursor, Codex and Antigravity executors")
	}
	return nil
}

// No new task ID, files, catalog probe or provider call: suitable for managers and installed readback.
func cmdRoute(args []string) error {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	effort := fs.String("effort", "", "explicit medium for gpt-complex or max for gpt-short")
	root := fs.String("root", "", "Cardex root (read only)")
	class := fs.String("work-class", "development", "development|simple-development|gpt-complex|gpt-short|management")
	typ := fs.String("type", typeSequence, "task type")
	risk := fs.String("risk-class", riskClassOrdinary, "ordinary|high-risk|critical|production")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !validTypes[*typ] || *class == "" || !validMixedWorkClass(*class) {
		return fmt.Errorf("invalid type or work-class")
	}
	cfg, err := loadConfig(resolveRoot(*root))
	if err != nil {
		return err
	}
	if !cfg.OwnerMixedRouting {
		return fmt.Errorf("owner_mixed_routing is not enabled")
	}
	t := &Task{Type: *typ, PreferRunner: "codex", WorkClass: *class, RiskClass: *risk, RouteClass: routeClassGeneral, Prompts: []string{"route readback"}}
	if err := closedOwnerTaskStateError(t); err != nil {
		return err
	}
	t.Effort, t.EffortExplicit = *effort, *effort != ""
	route, ok := resolveOwnerRoute(cfg, t)
	if !ok {
		return fmt.Errorf("no eligible owner route")
	}
	out := struct {
		ConfiguredMode    string     `json:"configured_mode,omitempty"`
		EffectiveMode     string     `json:"effective_mode"`
		Policy            string     `json:"policy"`
		WorkClass         string     `json:"work_class"`
		Primary           policyLeg  `json:"primary"`
		QuotaFallback     *policyLeg `json:"quota_fallback,omitempty"`
		FallbackCondition string     `json:"fallback_condition,omitempty"`
	}{ConfiguredMode: cfg.DispatchMode, EffectiveMode: effectiveDispatchMode(cfg, dispatchNow()), Policy: route.Name, WorkClass: *class, Primary: route.Legs[0]}
	if len(route.Legs) > 1 {
		out.QuotaFallback = &route.Legs[1]
		out.FallbackCondition = "proven quota exhaustion; complete zero-activity terminal; unchanged workspace; no process residue; no auth/refusal/transport/unknown outcome"
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

// The configuration update retains unrelated and unknown keys. Neither status nor
// an early return touches cards, attempts, providers, timers, or held authority.
func cmdDispatchMode(args []string) error {
	fs := flag.NewFlagSet("mode", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "Cardex root")
	set := fs.String("set", "", "daily|codex-priority (omitted: status)")
	start := fs.String("start", "", "authorized RFC3339 start; never inferred from installation time")
	expires := fs.String("expires-at", "", "authorized RFC3339 expiry")
	allowance := fs.String("allowance", "", "nonsecret Codex usage snapshot path")
	taskID := fs.String("task", "", "include frozen task identity without modifying it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := resolveRoot(*rootFlag)
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	if *set == "" && (*start != "" || *expires != "" || *allowance != "") {
		return fmt.Errorf("window and allowance flags require -set")
	}
	if *set != "" {
		cfg.DispatchMode = *set
		if *start != "" {
			cfg.CodexPriorityStart = *start
		}
		if *expires != "" {
			cfg.CodexPriorityExpiresAt = *expires
		}
		if *allowance != "" {
			cfg.CodexAllowanceSnapshot = *allowance
		}
		if err := validateMixedOwnerConfig(cfg); err != nil {
			return err
		}
		raw, err := os.ReadFile(configPath(root))
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for key, val := range map[string]string{"dispatch_mode": cfg.DispatchMode, "codex_priority_start": cfg.CodexPriorityStart, "codex_priority_expires_at": cfg.CodexPriorityExpiresAt, "codex_allowance_snapshot": cfg.CodexAllowanceSnapshot} {
			if val != "" {
				fields[key], _ = json.Marshal(val)
			}
		}
		raw, err = json.MarshalIndent(fields, "", "  ")
		if err != nil {
			return err
		}
		if err := atomicWriteSync(configPath(root), append(raw, '\n')); err != nil {
			return err
		}
	}
	now := dispatchNow()
	out := map[string]any{"configured_mode": cfg.DispatchMode, "effective_mode": effectiveDispatchMode(cfg, now), "authorized_start": cfg.CodexPriorityStart, "authorized_expires_at": cfg.CodexPriorityExpiresAt, "observed_at": now.UTC().Format(time.RFC3339)}
	out["daily_mode_label"] = "日常派发模式"
	if effectiveDispatchMode(cfg, now) == dispatchModeCodexPriority {
		e := readCodexTemporaryAllowance(cfg, now)
		out["allowance"] = map[string]any{"available": e.Available, "source": e.Source, "window_duration_mins": e.WindowMinutes, "used_percent": e.UsedPercent, "sampled_at": e.SampledAt, "reason": e.Reason}
	}
	if *taskID != "" {
		t, err := findTask(root, *taskID)
		if err != nil {
			return err
		}
		out["frozen_mode"], out["frozen_route"], out["frozen_leg"] = frozenDispatchMode(t), t.OwnerRouteName, t.OwnerRouteLeg
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func mixedCursorEquivalent(model string) string {
	switch model {
	case "grok-4.7":
		return "grok-4.7-high"
	case "grok-4.6":
		return "cursor-grok-4.6-high"
	default:
		return ""
	}
}

// A transient 429 is not proof that the subscription is exhausted. Refusal,
// auth, transport and unknown terminals take precedence even in mixed diagnostics.
var mixedQuotaExhaustedRe = regexp.MustCompile(`(?i)quota (?:exceeded|exhausted)|usage limit (?:reached|exceeded)|insufficient (?:quota|credits)|out of (?:credits|usage)|额度(?:不足|已用完)|配额(?:不足|已用尽)|限额(?:不足|已用尽)`)
var mixedRefusalRe = regexp.MustCompile(`(?i)refus(?:al|ed)|cannot (?:comply|assist|help)|can't (?:comply|assist|help)|policy (?:denied|violation)|unauthenticated|unauthorized|\b401\b|\b403\b`)

func classifyTaskPolicyFallbackFailure(t *Task, via string, res *claudeResult, combined string, runErr error) (fallbackFailureKind, bool) {
	if frozenDispatchMode(t) != "" {
		if res == nil || !res.IsError || !res.ObservationComplete {
			return "", false
		}
		scan := policyFailureScanText(via, res, combined, runErr) + "\n" + res.Subtype
		if via == "codex" {
			// Only the named-mode JSON observer can supply this exact structured
			// terminal. Never scan an assistant transcript for quota authority.
			if res.Subtype != "codex_quota_terminal" {
				return "", false
			}
			scan = res.Result
		}
		if policyUnsafeTerminal(scan) || mixedRefusalRe.MatchString(scan) || policyTransportRe.MatchString(scan) || policyStallRe.MatchString(scan) ||
			strings.Contains(res.Subtype, "incomplete") || strings.Contains(res.Subtype, "invalid_terminal") || !mixedQuotaExhaustedRe.MatchString(scan) {
			return "", false
		}
		return fallbackQuota, true
	}
	kind, ok := classifyPolicyFallbackFailure(via, res, combined, runErr)
	if !mixedOwnerTask(t) {
		return kind, ok
	}
	if !ok || kind != fallbackQuota || via != grokBuildRunnerName || res == nil || !res.ObservationComplete {
		return "", false
	}
	scan := policyFailureScanText(via, res, combined, runErr) + "\n" + res.Subtype
	if policyUnsafeTerminal(scan) || mixedRefusalRe.MatchString(scan) || policyTransportRe.MatchString(scan) || policyStallRe.MatchString(scan) ||
		strings.Contains(res.Subtype, "stream_incomplete") || strings.Contains(res.Subtype, "invalid_terminal") || !mixedQuotaExhaustedRe.MatchString(scan) {
		return "", false
	}
	return fallbackQuota, true
}

// Mixed primary work shares the provider budget gate, while the historical
// Sol-only review reservation remains scoped to its original frozen lineages.
func mixedCodexPrimary(t *Task) bool {
	if t == nil {
		return false
	}
	if t.OwnerRouteName == "mixed_gpt_complex" || t.OwnerRouteName == "mixed_gpt_short" {
		return true
	}
	if frozenDispatchMode(t) != "" && t.OwnerRouteLeg == 1 {
		route, ok := resolveMixedOwnerRoute(nil, t)
		return ok && route.Legs[0].Runner == "codex"
	}
	return false
}

// codexSnapshotExhaustedReason records a fresh 100% bucket admission. It is not
// an invocation terminal and not a zero-work proof.
const codexSnapshotExhaustedReason = "quota-snapshot/budget-exhausted"

func unstartedForSnapshotAdmission(t *Task) bool {
	if t == nil || t.WorkClass == "" || !validMixedWorkClass(t.WorkClass) || t.RunnerExplicit ||
		t.RemoteHost != "" || t.XRole != "" || t.SessionID != "" || t.MidStep {
		return false
	}
	if t.Status != statusQueued && t.Status != "" {
		return false
	}
	if t.Attempts != 0 || t.Step != 0 || t.Runner != "" || t.LastRouteAttempt != nil || t.ActiveAttemptID != "" {
		return false
	}
	return t.schedulingAllowed() && !t.terminal()
}

func unstartedCodexSnapshotSuccessor(t *Task) bool {
	return unstartedForSnapshotAdmission(t) && t.RouteReason == codexSnapshotExhaustedReason &&
		frozenDispatchMode(t) != "" && t.PreferRunner == kimiCLIRunnerName &&
		t.KimiModel == "kimi-code/k3" && t.Effort == "max" && t.EffortExplicit && !t.AutomaticCodex &&
		t.OwnerRouteLeg >= 2 && t.CodexModel == "" && t.GrokModel == "" && t.CursorModel == ""
}

func codexSnapshotSuccessorEligible(root string, cfg *Config, t *Task, now time.Time) bool {
	if !codexSnapshotExhausted(cfg, now) || !unstartedForSnapshotAdmission(t) || t.Goal != nil ||
		t.AutomaticSolCalls != 0 || t.AutomaticSolInvocations != 0 || t.OwnerRouteLeg != 1 ||
		t.RouteReason != mixedRouteReason || t.PreferRunner != "codex" || !t.AutomaticCodex ||
		frozenDispatchMode(t) == "" || frozenDispatchMode(t) != effectiveDispatchMode(cfg, now) {
		return false
	}
	if taskProcessResidue(t.ID) || anyTaskProcAlive(t.ID) || workspaceProcessResidue(t.Dir) || !kimiCLIReady(root, cfg, now) {
		return false
	}
	route, ok := resolveOwnerRouteReadback(cfg, t)
	if !ok || len(route.Legs) < 2 || route.Legs[0].Runner != "codex" || !ownerRouteSnapshotLegMatches(t, route.Legs[0]) {
		return false
	}
	for _, leg := range route.Legs[1:] {
		if leg.Runner == kimiCLIRunnerName && leg.Model == "kimi-code/k3" && leg.Effort == "max" {
			return true
		}
	}
	return false
}

// selectUnstartedCodexSnapshotSuccessor pins Kimi for one unstarted named-mode
// Codex primary when the account snapshot is fresh and exactly 100% used.
// Attempts, max_attempts, Sol/Goal budgets and LastRouteAttempt stay untouched.
func selectUnstartedCodexSnapshotSuccessor(root string, cfg *Config, t *Task, now time.Time) bool {
	if !codexSnapshotSuccessorEligible(root, cfg, t, now) {
		return false
	}
	route, ok := resolveOwnerRouteReadback(cfg, t)
	if !ok {
		return false
	}
	kimiIndex := -1
	for i, leg := range route.Legs {
		if i > 0 && leg.Runner == kimiCLIRunnerName && leg.Model == "kimi-code/k3" && leg.Effort == "max" {
			kimiIndex = i
			break
		}
	}
	if kimiIndex < 0 {
		return false
	}
	before := *t
	probe := *t
	clearMixedProviderPins(&probe)
	probe.Effort, probe.EffortExplicit, probe.AutomaticCodex = "", false, false
	if !pinMixedOwnerLeg(&probe, route, kimiIndex) {
		return false
	}
	probe.RouteReason = codexSnapshotExhaustedReason
	probe.Attempts, probe.MaxAttempts = before.Attempts, before.MaxAttempts
	probe.AutomaticSolCalls, probe.AutomaticSolInvocations = before.AutomaticSolCalls, before.AutomaticSolInvocations
	probe.Goal, probe.LastRouteAttempt, probe.Status, probe.FallbackReason = before.Goal, before.LastRouteAttempt, before.Status, before.FallbackReason
	if probe.KimiModel != "kimi-code/k3" || probe.Effort != "max" || probe.PreferRunner != kimiCLIRunnerName || probe.AutomaticCodex || probe.Attempts != before.Attempts || probe.MaxAttempts != before.MaxAttempts {
		return false
	}
	*t = probe
	if err := persistTaskEvent(root, t, evRetry, "runner:automatic-codex-budget", statusQueued, t.Step, map[string]any{
		"reason": codexSnapshotExhaustedReason, "provider_started": false,
		"budget_source": "codex_app.get_usage_limits", "used_percent": 100, "window_duration_mins": 10080,
		"selected_runner": kimiCLIRunnerName, "selected_model": "kimi-code/k3", "selected_effort": "max",
	}); err != nil {
		*t = before
		return false
	}
	return true
}

// Only policy-owned, unstarted queued primaries may follow a changed mode. No
// running/held/terminal identity or completed-attempt receipt is ever rewritten.
func refreshUnstartedDispatchMode(cfg *Config, t *Task, now time.Time) bool {
	if cfg == nil || cfg.DispatchMode == "" || t == nil || t.Goal != nil || (t.Status != statusQueued && t.Status != "") ||
		t.WorkClass == "" || t.RunnerExplicit || t.RemoteHost != "" || t.XRole != "" || t.SessionID != "" || t.MidStep ||
		t.Attempts != 0 || t.Step != 0 || t.Runner != "" || t.LastRouteAttempt != nil ||
		t.ActiveAttemptID != "" || !t.schedulingAllowed() {
		return false
	}
	if unstartedCodexSnapshotSuccessor(t) {
		if effectiveDispatchMode(cfg, now) == frozenDispatchMode(t) {
			return false
		}
		if taskProcessResidue(t.ID) || anyTaskProcAlive(t.ID) || workspaceProcessResidue(t.Dir) {
			return false
		}
		clearMixedProviderPins(t)
		t.OwnerRouteName, t.OwnerRouteLeg, t.OwnerRouteStage = "", 0, ""
		t.RouteReason, t.AutomaticCodex = "", false
		t.Effort, t.EffortExplicit = "", false
	}
	probe := *t
	if t.OwnerRouteName != "" {
		if !mixedOwnerTask(t) || t.OwnerRouteLeg != 1 || t.RouteReason != mixedRouteReason {
			return false
		}
		old, ok := resolveMixedOwnerRoute(nil, t)
		if !ok || old.Name != t.OwnerRouteName || !ownerRouteSnapshotLegMatches(t, old.Legs[0]) {
			return false
		}
		clearMixedProviderPins(&probe)
		probe.OwnerRouteName, probe.OwnerRouteLeg, probe.OwnerRouteStage = "", 0, ""
		probe.AutomaticCodex = false
	}
	if !ownerAutoRouteEligible(&probe) {
		return false
	}
	route, ok := resolveMixedOwnerRouteAt(cfg, &probe, now)
	if !ok || !pinMixedOwnerPrimary(&probe, route) {
		return false
	}
	*t = probe
	return true
}

func clearMixedProviderPins(t *Task) {
	t.PreferRunner = "codex"
	t.CodexModel, t.XCodexModel, t.GeminiModel, t.AgyModel = "", "", "", ""
	t.OpenCodeModel, t.KimiModel, t.GrokModel, t.GrokEffort, t.CursorModel = "", "", "", "", ""
}

// observeNamedCodexJSON is deliberately a small closed observer for `exec --json`.
// Known item events are work; unknown/malformed events or unframed stderr prevent
// a zero-work proof. A turn.failed terminal is required for quota succession.
func observeNamedCodexJSON(res *claudeResult, stdout, stderr string) {
	if res == nil {
		return
	}
	complete := strings.TrimSpace(stderr) == ""
	failed := false
	var failureText []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &event) != nil {
			complete = false
			continue
		}
		var typ string
		if json.Unmarshal(event["type"], &typ) != nil {
			complete = false
			continue
		}
		if res.TerminalEvents > 0 {
			complete = false
		}
		switch typ {
		case "thread.started":
			var id string
			if json.Unmarshal(event["thread_id"], &id) != nil || id == "" || len(event) != 2 {
				complete = false
			}
		case "turn.started":
			if len(event) != 1 {
				complete = false
			}
		case "item.started", "item.updated", "item.completed":
			res.SemanticEvents++
			var item struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(event["item"], &item) != nil {
				complete = false
			}
			switch item.Type {
			case "agent_message", "reasoning":
				res.ModelEvents++
			default:
				res.ToolEvents++
			}
		case "error":
			var message string
			if json.Unmarshal(event["message"], &message) != nil || message == "" || len(event) != 2 {
				complete = false
			}
			failureText = append(failureText, message)
		case "turn.failed":
			res.TerminalEvents++
			failed = true
			var e map[string]json.RawMessage
			var message string
			if json.Unmarshal(event["error"], &e) != nil || json.Unmarshal(e["message"], &message) != nil || message == "" || len(e) != 1 || len(event) != 2 {
				complete = false
			}
			failureText = append(failureText, message)
		case "turn.completed":
			res.TerminalEvents++
			if len(event) != 2 || event["usage"] == nil {
				complete = false
			}
		default:
			complete = false
		}
	}
	res.ObservationComplete = complete && res.TerminalEvents == 1
	if failed {
		res.IsError = true
		res.ResultFromTranscript = false
		diagnostic := strings.Join(failureText, "\n")
		res.Result = "Codex provider error"
		res.Subtype = "codex_error"
		if !policyUnsafeTerminal(diagnostic) && !mixedRefusalRe.MatchString(diagnostic) && !policyTransportRe.MatchString(diagnostic) && !policyStallRe.MatchString(diagnostic) && mixedQuotaExhaustedRe.MatchString(diagnostic) {
			res.Subtype = "codex_quota_terminal"
			res.Result = "quota exhausted"
		}
	}
	if !res.ObservationComplete {
		res.IsError = true
		res.Subtype = "codex_stream_incomplete"
		res.Result = "Codex JSON observation incomplete"
	}
}

// Workflow admission explicitly chooses an executor, but leaves its model/effort
// empty. Only that new, never-executed mixed card may receive current defaults.
func mixedNewGrokModel(cfg *Config, t *Task) string {
	if cfg == nil || !cfg.OwnerMixedRouting || t == nil || t.PreferRunner != grokBuildRunnerName ||
		t.GrokModel != "" || t.GrokEffort != "" || t.Attempts != 0 || t.Step != 0 || t.terminal() || grokHasExecutionEvidence(t) {
		return ""
	}
	switch t.WorkClass {
	case "development":
		return "grok-4.7"
	case "simple-development":
		return "grok-4.6"
	default:
		return ""
	}
}
