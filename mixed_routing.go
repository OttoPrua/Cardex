package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const mixedRouteReason = "owner_mixed_primary"
const mixedQuotaReason = "owner_mixed_cursor_quota"

// WorkClass is an admission-time declaration, never inferred from prompt text.
// Empty means a historical card, which keeps the pre-mixed policy.
func defaultMixedWorkClass(typ string) string {
	switch typ {
	case typeSequence:
		return "development"
	case typeCoordinate, typeProgressPull:
		return "management"
	case typeAssembly:
		return "gpt-complex"
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
	if t.WorkClass == "" || !validMixedWorkClass(t.WorkClass) {
		return ownerRoute{}, false
	}
	risk := effectiveOwnerRiskClass(t)
	leg := policyLeg{Stage: routeStagePrimary, ReadOnly: t.Type != typeSequence}
	name := "mixed_" + strings.ReplaceAll(t.WorkClass, "-", "_")
	switch t.WorkClass {
	case "development", "simple-development":
		leg.Runner, leg.Model, leg.Effort = grokBuildRunnerName, "grok-4.7", "high"
		cursor := "grok-4.7-high"
		if t.WorkClass == "simple-development" {
			leg.Model, cursor = "grok-4.6", "cursor-grok-4.6-high"
		}
		return ownerRoute{Name: name, RiskClass: risk, Legs: []policyLeg{leg,
			{Runner: cursorRunnerName, Model: cursor, Effort: "high", Stage: routeStageFallbackReview, ReadOnly: leg.ReadOnly}}}, true
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
	return ownerRoute{Name: name, RiskClass: risk, Legs: []policyLeg{leg}}, true
}

func pinMixedOwnerPrimary(t *Task, route ownerRoute) bool {
	leg := route.Legs[0]
	t.OwnerRouteName, t.OwnerRouteLeg, t.OwnerRouteStage = route.Name, 1, leg.Stage
	t.RouteReason = mixedRouteReason
	t.RiskClass = route.RiskClass
	t.PreferRunner = leg.Runner
	switch leg.Runner {
	case grokBuildRunnerName:
		t.GrokModel, t.GrokEffort = leg.Model, leg.Effort
	case "codex":
		t.CodexModel, t.Effort, t.EffortExplicit = leg.Model, leg.Effort, true
		t.AutomaticCodex = true
	case antigravityRunnerName:
		t.AgyModel, t.Effort, t.EffortExplicit = leg.Model, leg.Effort, true
	default:
		return false
	}
	return true
}

func validateMixedOwnerConfig(cfg *Config) error {
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
		Policy            string     `json:"policy"`
		WorkClass         string     `json:"work_class"`
		Primary           policyLeg  `json:"primary"`
		QuotaFallback     *policyLeg `json:"quota_fallback,omitempty"`
		FallbackCondition string     `json:"fallback_condition,omitempty"`
	}{Policy: route.Name, WorkClass: *class, Primary: route.Legs[0]}
	if len(route.Legs) > 1 {
		out.QuotaFallback = &route.Legs[1]
		out.FallbackCondition = "proven quota exhaustion; complete zero-activity terminal; unchanged workspace; no process residue; no auth/refusal/transport/unknown outcome"
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
	return t != nil && (t.OwnerRouteName == "mixed_gpt_complex" || t.OwnerRouteName == "mixed_gpt_short")
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
