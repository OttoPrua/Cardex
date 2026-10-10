package main

import (
	"fmt"
	"strings"
)

// RouteLegConfig is one configured runner/model/effort identity.
type RouteLegConfig struct {
	Runner string `json:"runner"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// ConfiguredRoute is a primary identity plus an optional ordered fallback list.
// An omitted/null fallback inherits defaults; an explicit list (including []) is exact.
type ConfiguredRoute struct {
	Runner   string           `json:"runner"`
	Model    string           `json:"model"`
	Effort   string           `json:"effort"`
	Fallback []RouteLegConfig `json:"fallback"`
}

// RouteMatrix is complexity → category → route. Complexity keys are
// high/medium/low (aliases opus/sonnet/haiku). Category keys are
// frontend/backend/general.
type RouteMatrix map[string]map[string]ConfiguredRoute

type frozenPolicyLeg struct {
	Runner   string `json:"runner"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Stage    string `json:"stage,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type frozenOwnerRoute struct {
	Name               string            `json:"name"`
	Legs               []frozenPolicyLeg `json:"legs"`
	Review             *frozenPolicyLeg  `json:"review,omitempty"`
	Merge              *frozenPolicyLeg  `json:"merge,omitempty"`
	ReleaseGate        *frozenPolicyLeg  `json:"release_gate,omitempty"`
	ConditionalSol     *frozenPolicyLeg  `json:"conditional_sol,omitempty"`
	RiskClass          string            `json:"risk_class,omitempty"`
	IndependentAnswers bool              `json:"independent_answers,omitempty"`
}

const (
	complexityHigh   = "high"
	complexityMedium = "medium"
	complexityLow    = "low"
	routeCategoryFE  = "frontend"
	routeCategoryBE  = "backend"
	routeCategoryGen = "general"
)

func freezeIfEmpty(t *Task, route ownerRoute) {
	if t == nil || t.FrozenRoute != nil || len(route.Legs) == 0 {
		return
	}
	t.FrozenRoute = freezeFromOwnerRoute(route)
}

func freezeFromOwnerRoute(r ownerRoute) *frozenOwnerRoute {
	out := &frozenOwnerRoute{
		Name:               r.Name,
		Legs:               freezeLegs(r.Legs),
		RiskClass:          r.RiskClass,
		IndependentAnswers: r.IndependentAnswers,
	}
	out.Review = freezeLegPtr(r.Review)
	out.Merge = freezeLegPtr(r.Merge)
	out.ReleaseGate = freezeLegPtr(r.ReleaseGate)
	out.ConditionalSol = freezeLegPtr(r.ConditionalSol)
	return out
}

func freezeLegs(legs []policyLeg) []frozenPolicyLeg {
	if len(legs) == 0 {
		return nil
	}
	out := make([]frozenPolicyLeg, len(legs))
	for i, leg := range legs {
		out[i] = freezeLeg(leg)
	}
	return out
}

func freezeLeg(leg policyLeg) frozenPolicyLeg {
	return frozenPolicyLeg{Runner: leg.Runner, Model: leg.Model, Effort: leg.Effort, Stage: leg.Stage, ReadOnly: leg.ReadOnly}
}

func freezeLegPtr(leg *policyLeg) *frozenPolicyLeg {
	if leg == nil {
		return nil
	}
	v := freezeLeg(*leg)
	return &v
}

func ownerRouteFromFrozen(f *frozenOwnerRoute) ownerRoute {
	if f == nil {
		return ownerRoute{}
	}
	out := ownerRoute{
		Name:               f.Name,
		Legs:               thawLegs(f.Legs),
		RiskClass:          f.RiskClass,
		IndependentAnswers: f.IndependentAnswers,
	}
	out.Review = thawLegPtr(f.Review)
	out.Merge = thawLegPtr(f.Merge)
	out.ReleaseGate = thawLegPtr(f.ReleaseGate)
	out.ConditionalSol = thawLegPtr(f.ConditionalSol)
	return out
}

func thawLegs(legs []frozenPolicyLeg) []policyLeg {
	if len(legs) == 0 {
		return nil
	}
	out := make([]policyLeg, len(legs))
	for i, leg := range legs {
		out[i] = thawLeg(leg)
	}
	return out
}

func thawLeg(leg frozenPolicyLeg) policyLeg {
	return policyLeg{Runner: leg.Runner, Model: leg.Model, Effort: leg.Effort, Stage: leg.Stage, ReadOnly: leg.ReadOnly}
}

func thawLegPtr(leg *frozenPolicyLeg) *policyLeg {
	if leg == nil {
		return nil
	}
	v := thawLeg(*leg)
	return &v
}

func completeFrozenRoute(t *Task) bool {
	return t != nil && t.FrozenRoute != nil && t.FrozenRoute.Name != "" && len(t.FrozenRoute.Legs) > 0
}

func frozenRouteReadback(t *Task) (ownerRoute, bool) {
	if !completeFrozenRoute(t) {
		return ownerRoute{}, false
	}
	if t.XRole != "" || t.RunnerExplicit || t.RemoteHost != "" || t.SessionID != "" || t.MidStep {
		return ownerRoute{}, false
	}
	if t.OwnerRouteName != "" && t.OwnerRouteName != t.FrozenRoute.Name {
		return ownerRoute{}, false
	}
	if t.OwnerRouteLeg < 1 || t.OwnerRouteLeg > len(t.FrozenRoute.Legs) {
		return ownerRoute{}, false
	}
	route := ownerRouteFromFrozen(t.FrozenRoute)
	if !ownerRouteSnapshotLegMatches(t, route.Legs[t.OwnerRouteLeg-1]) {
		return ownerRoute{}, false
	}
	return route, true
}

func canonicalComplexity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case complexityHigh, "opus":
		return complexityHigh
	case complexityMedium, "sonnet":
		return complexityMedium
	case complexityLow, "haiku":
		return complexityLow
	default:
		return ""
	}
}

func complexityFromTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "opus":
		return complexityHigh
	case "sonnet":
		return complexityMedium
	case "haiku":
		return complexityLow
	default:
		return canonicalComplexity(tier)
	}
}

func canonicalCategory(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case routeCategoryFE:
		return routeCategoryFE
	case routeCategoryBE:
		return routeCategoryBE
	case routeCategoryGen:
		return routeCategoryGen
	default:
		return ""
	}
}

func routeCategory(t *Task) string {
	if t == nil {
		return routeCategoryGen
	}
	if cat := canonicalCategory(t.RouteClass); cat != "" {
		return cat
	}
	if t.SpecializedFrontend {
		return routeCategoryFE
	}
	if backendDevelopmentTask(t) {
		return routeCategoryBE
	}
	return routeCategoryGen
}

func specializedFrontendTask(t *Task) bool {
	return t != nil && t.SpecializedFrontend
}

func lookupWorkClassRoute(cfg *Config, class string) (ConfiguredRoute, bool) {
	if cfg == nil || len(cfg.WorkClassRoutes) == 0 {
		return ConfiguredRoute{}, false
	}
	spec, ok := cfg.WorkClassRoutes[strings.ToLower(strings.TrimSpace(class))]
	if !ok || strings.TrimSpace(spec.Runner) == "" {
		return ConfiguredRoute{}, false
	}
	return spec, true
}

func lookupRouteMatrix(cfg *Config, complexity, category string) (ConfiguredRoute, bool) {
	if cfg == nil || len(cfg.RouteMatrix) == 0 {
		return ConfiguredRoute{}, false
	}
	row, ok := cfg.RouteMatrix[complexity]
	if !ok {
		for key, candidate := range cfg.RouteMatrix {
			if canonicalComplexity(key) == complexity {
				row, ok = candidate, true
				break
			}
		}
	}
	if !ok || row == nil {
		return ConfiguredRoute{}, false
	}
	spec, ok := row[category]
	if !ok {
		for key, candidate := range row {
			if canonicalCategory(key) == category {
				spec, ok = candidate, true
				break
			}
		}
	}
	if !ok || strings.TrimSpace(spec.Runner) == "" {
		return ConfiguredRoute{}, false
	}
	return spec, true
}

func overlayRouteMatrix(cfg *Config, t *Task, route ownerRoute) ownerRoute {
	if cfg == nil || t == nil || t.Type == typeReview || len(cfg.RouteMatrix) == 0 || t.OwnerRouteName != "" || route.Name == "fable_explicit" {
		return route
	}
	complexity := complexityFromTier(modelTierKeyword(cfg, t.Model))
	if complexity == "" {
		return route
	}
	spec, ok := lookupRouteMatrix(cfg, complexity, routeCategory(t))
	if !ok {
		return route
	}
	return applyConfiguredPrimary(route, spec)
}

func applyConfiguredPrimary(route ownerRoute, spec ConfiguredRoute) ownerRoute {
	if len(route.Legs) == 0 {
		return route
	}
	primary := route.Legs[0]
	primary.Runner = strings.TrimSpace(spec.Runner)
	primary.Model = strings.TrimSpace(spec.Model)
	primary.Effort = strings.ToLower(strings.TrimSpace(spec.Effort))
	legs := []policyLeg{primary}
	if spec.Fallback != nil {
		readOnly := primary.ReadOnly
		if len(route.Legs) > 1 {
			readOnly = route.Legs[1].ReadOnly
		}
		for _, fb := range spec.Fallback {
			legs = append(legs, policyLeg{
				Runner:   strings.TrimSpace(fb.Runner),
				Model:    strings.TrimSpace(fb.Model),
				Effort:   strings.ToLower(strings.TrimSpace(fb.Effort)),
				Stage:    routeStageFallbackReview,
				ReadOnly: readOnly,
			})
		}
	} else if len(route.Legs) > 1 {
		legs = append(legs, route.Legs[1:]...)
	}
	route.Legs = legs
	return route
}

func mixedBuiltinPrimary(class, ownerRouteName string) (runner, model, effort string, historical bool) {
	admitted := strings.TrimSpace(ownerRouteName) != ""
	switch class {
	case "development":
		return grokBuildRunnerName, "grok-4.6", "xhigh", false
	case "simple-development":
		return grokBuildRunnerName, "grok-4.6", "high", false
	case "gpt-complex":
		if admitted {
			return "codex", "gpt-6-astra", "high", true
		}
		return "codex", "gpt-6.1-sol", "high", false
	case "gpt-short":
		if admitted {
			return "codex", "gpt-5.6-sol", "xhigh", true
		}
		return grokBuildRunnerName, "grok-4.6", "xhigh", false
	case "management":
		return antigravityRunnerName, "gemini-3.8-flash-high", "high", false
	default:
		return "", "", "", false
	}
}

func applyWorkClassSpec(leg policyLeg, spec ConfiguredRoute, class string, t *Task) policyLeg {
	leg.Runner = strings.TrimSpace(spec.Runner)
	leg.Model = strings.TrimSpace(spec.Model)
	leg.Effort = strings.ToLower(strings.TrimSpace(spec.Effort))
	if class == "gpt-complex" && t != nil && t.EffortExplicit && t.Effort == "medium" {
		leg.Effort = "medium"
	}
	if class == "management" {
		leg.ReadOnly = true
	}
	return leg
}

func configuredWorkClassLegs(leg policyLeg, spec ConfiguredRoute, class string, t *Task, mode string, kimi policyLeg) []policyLeg {
	primary := applyWorkClassSpec(leg, spec, class, t)
	legs := []policyLeg{primary}
	if spec.Fallback != nil {
		for _, fb := range spec.Fallback {
			fbLeg := policyLeg{
				Runner:   strings.TrimSpace(fb.Runner),
				Model:    strings.TrimSpace(fb.Model),
				Effort:   strings.ToLower(strings.TrimSpace(fb.Effort)),
				Stage:    routeStageFallbackReview,
				ReadOnly: primary.ReadOnly,
			}
			if fbLeg.Effort == "" {
				fbLeg.Effort = primary.Effort
			}
			legs = append(legs, fbLeg)
		}
	} else if primary.Runner == grokBuildRunnerName && (class == "development" || class == "simple-development") {
		cursor := mixedCursorEquivalent(primary.Model, primary.Effort)
		if cursor != "" {
			legs = append(legs, policyLeg{Runner: cursorRunnerName, Model: cursor, Effort: primary.Effort, Stage: routeStageFallbackReview, ReadOnly: primary.ReadOnly})
		}
	}
	if spec.Fallback == nil && mode != "" && (primary.Runner == "codex" || class == "development" || class == "simple-development" || class == "gpt-short") {
		hasKimi := false
		for _, existing := range legs {
			if existing.Runner == kimiCLIRunnerName {
				hasKimi = true
				break
			}
		}
		if !hasKimi {
			legs = append(legs, kimi)
		}
	}
	return legs
}

func legalConfiguredRunner(runner string) bool {
	switch strings.TrimSpace(runner) {
	case grokBuildRunnerName, cursorRunnerName, kimiCLIRunnerName, "codex", antigravityRunnerName:
		return true
	default:
		return false
	}
}

func validateRouteLegConfig(prefix string, runner, model, effort string) error {
	runner = strings.TrimSpace(runner)
	model = strings.TrimSpace(model)
	effort = strings.ToLower(strings.TrimSpace(effort))
	if !legalConfiguredRunner(runner) {
		return fmt.Errorf("%s runner %q is not a legal configured identity", prefix, runner)
	}
	if model == "" {
		return fmt.Errorf("%s model must be non-empty", prefix)
	}
	switch runner {
	case grokBuildRunnerName:
		switch effort {
		case "low", "medium", "high", "xhigh":
		default:
			return fmt.Errorf("%s grok-build effort %q is invalid (optional low/medium/high/xhigh)", prefix, effort)
		}
	case antigravityRunnerName:
		switch effort {
		case "low", "medium", "high":
		default:
			return fmt.Errorf("%s agy effort %q is invalid (optional low/medium/high)", prefix, effort)
		}
	case cursorRunnerName:
		if effort != "" && !validEfforts[effort] {
			return fmt.Errorf("%s cursor effort %q is invalid", prefix, effort)
		}
	case "codex":
		if effort != "ultra" && !validEfforts[effort] {
			return fmt.Errorf("%s codex effort %q is invalid", prefix, effort)
		}
	default:
		if effort == "" || !validEfforts[effort] {
			return fmt.Errorf("%s effort %q is invalid", prefix, effort)
		}
	}
	return nil
}

func validateConfiguredRoute(prefix string, spec ConfiguredRoute) error {
	if err := validateRouteLegConfig(prefix, spec.Runner, spec.Model, spec.Effort); err != nil {
		return err
	}
	for i, fb := range spec.Fallback {
		if err := validateRouteLegConfig(fmt.Sprintf("%s.fallback[%d]", prefix, i), fb.Runner, fb.Model, fb.Effort); err != nil {
			return err
		}
	}
	return nil
}

func validateConfigurableDispatch(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	for class, spec := range cfg.WorkClassRoutes {
		if !validMixedWorkClass(class) || class == "" {
			return fmt.Errorf("work_class_routes.%s is not a known work_class", class)
		}
		if err := validateConfiguredRoute("work_class_routes."+class, spec); err != nil {
			return err
		}
		if class == "management" && spec.Runner != antigravityRunnerName && spec.Runner != grokBuildRunnerName && spec.Runner != "codex" && spec.Runner != kimiCLIRunnerName && spec.Runner != cursorRunnerName && spec.Runner != "opencode" {
			return fmt.Errorf("work_class_routes.management runner %q is not a legal configured identity", spec.Runner)
		}
	}
	normalized := RouteMatrix{}
	for complexity, row := range cfg.RouteMatrix {
		canonical := canonicalComplexity(complexity)
		if _, exists := normalized[canonical]; exists {
			return fmt.Errorf("duplicate route_matrix complexity alias %q", complexity)
		}
		normalized[canonical] = map[string]ConfiguredRoute{}
		if canonicalComplexity(complexity) == "" {
			return fmt.Errorf("route_matrix.%s is not a known complexity (high/medium/low or opus/sonnet/haiku)", complexity)
		}
		for category, spec := range row {
			cat := canonicalCategory(category)
			if _, exists := normalized[canonical][cat]; exists {
				return fmt.Errorf("duplicate route_matrix category alias %q", category)
			}
			normalized[canonical][cat] = spec
			if canonicalCategory(category) == "" {
				return fmt.Errorf("route_matrix.%s.%s is not a known category (frontend/backend/general)", complexity, category)
			}
			if err := validateConfiguredRoute("route_matrix."+complexity+"."+category, spec); err != nil {
				return err
			}
		}
	}
	if cfg.RouteMatrix != nil {
		cfg.RouteMatrix = normalized
	}
	if len(cfg.WorkClassRoutes) > 0 && cfg.OwnerMixedRouting {
		if err := validateConfiguredRunnersEnabled(cfg, cfg.WorkClassRoutes); err != nil {
			return err
		}
	}
	if len(cfg.RouteMatrix) > 0 && cfg.OwnerRoutingEnforced {
		for _, row := range cfg.RouteMatrix {
			if err := validateConfiguredRunnersEnabled(cfg, row); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConfiguredRunnersEnabled(cfg *Config, routes map[string]ConfiguredRoute) error {
	seen := map[string]bool{}
	var check func(runner string) error
	check = func(runner string) error {
		if seen[runner] {
			return nil
		}
		seen[runner] = true
		switch runner {
		case grokBuildRunnerName:
			if !grokBuildEnabled(cfg) {
				return fmt.Errorf("configured runner grok-build is not enabled")
			}
		case cursorRunnerName:
			if !cursorEnabled(cfg) {
				return fmt.Errorf("configured runner cursor is not enabled")
			}
		case kimiCLIRunnerName:
			if strings.TrimSpace(cfg.KimiCLIBin) == "" {
				return fmt.Errorf("configured runner kimi-cli requires kimi_cli_bin")
			}
		case "codex":
			if strings.TrimSpace(cfg.CodexBin) == "" {
				return fmt.Errorf("configured runner codex requires codex_bin")
			}
		case antigravityRunnerName:
			if !antigravityEnabled(cfg) {
				return fmt.Errorf("configured runner agy is not enabled")
			}
		case "opencode":
			if strings.TrimSpace(cfg.OpenCodeBin) == "" {
				return fmt.Errorf("configured runner opencode requires opencode_bin")
			}
		}
		return nil
	}
	for _, spec := range routes {
		if err := check(strings.TrimSpace(spec.Runner)); err != nil {
			return err
		}
		for _, fb := range spec.Fallback {
			if err := check(strings.TrimSpace(fb.Runner)); err != nil {
				return err
			}
		}
	}
	return nil
}

func admitTaskRoute(cfg *Config, t *Task) error {
	if cfg == nil || t == nil || completeFrozenRoute(t) {
		return nil
	}
	if !cfg.OwnerRoutingEnforced && !cfg.OwnerMixedRouting {
		return nil
	}
	if !ownerAutoRouteEligible(t) {
		return nil
	}
	route, ok := resolveOwnerRoute(cfg, t)
	if !ok {
		if t.WorkClass != "" && cfg.OwnerMixedRouting {
			return fmt.Errorf("no eligible owner route")
		}
		return nil
	}
	if !pinOwnerPrimaryRoute(t, route) {
		return fmt.Errorf("failed to freeze admitted route %s", route.Name)
	}
	return nil
}

func freezeGoalRouteSnapshot(t *Task) {
	if t == nil || completeFrozenRoute(t) {
		return
	}
	if strings.TrimSpace(t.PreferRunner) == "" {
		return
	}
	leg := policyLeg{Runner: t.PreferRunner, Stage: routeStagePrimary, ReadOnly: t.Type != typeSequence}
	switch t.PreferRunner {
	case grokBuildRunnerName:
		if strings.TrimSpace(t.GrokModel) == "" || strings.TrimSpace(t.GrokEffort) == "" {
			return
		}
		leg.Model, leg.Effort = t.GrokModel, t.GrokEffort
	case "codex":
		if strings.TrimSpace(t.CodexModel) == "" {
			return
		}
		leg.Model, leg.Effort = t.CodexModel, t.Effort
	case antigravityRunnerName:
		if strings.TrimSpace(t.AgyModel) == "" {
			return
		}
		leg.Model, leg.Effort = t.AgyModel, t.Effort
	default:
		return
	}
	name := t.OwnerRouteName
	if name == "" {
		name = "goal_" + strings.ReplaceAll(firstNonBlank(t.WorkClass, t.Type), "-", "_")
	}
	freezeIfEmpty(t, ownerRoute{Name: name, Legs: []policyLeg{leg}, RiskClass: t.RiskClass})
}

// Only an explicit auto writer inherits live configuration. Existing engine pins
// and already admitted task snapshots never pass through this resolver again.
func resolveWorkflowDefaultRoute(cfg *Config, wf *WorkflowRecord) (ownerRoute, error) {
	if cfg == nil || (!cfg.OwnerRoutingEnforced && !cfg.OwnerMixedRouting) {
		return ownerRoute{}, fmt.Errorf("auto workflow writer requires enabled owner routing")
	}
	if !validMixedWorkClass(wf.WriterWorkClass) || canonicalCategory(wf.WriterRouteClass) == "" || complexityFromTier(modelTierKeyword(cfg, wf.WriterModel)) == "" {
		return ownerRoute{}, fmt.Errorf("invalid auto workflow writer work-class/model/route-class")
	}
	probe := &Task{Type: typeSequence, PreferRunner: "codex", Model: wf.WriterModel, WorkClass: wf.WriterWorkClass, RouteClass: wf.WriterRouteClass, RiskClass: riskClassOrdinary, Prompts: []string{wf.Goal}}
	route, ok := resolveOwnerRoute(cfg, probe)
	if !ok || len(route.Legs) == 0 {
		return ownerRoute{}, fmt.Errorf("auto workflow writer has no configured route")
	}
	return route, nil
}

func configureWorkflowWriter(cfg *Config, wf *WorkflowRecord, t *Task) error {
	if wf.WriterEngine != "auto" {
		t.PreferRunner, t.RunnerExplicit = wf.WriterEngine, true
		return nil
	}
	route, err := resolveWorkflowDefaultRoute(cfg, wf)
	if err != nil {
		return err
	}
	t.Model, t.WorkClass, t.RouteClass = wf.WriterModel, wf.WriterWorkClass, wf.WriterRouteClass
	t.PreferRunner, t.RunnerExplicit = "codex", false
	if !pinOwnerPrimaryRoute(t, route) {
		return fmt.Errorf("cannot pin auto workflow writer route %s", route.Name)
	}
	return nil
}
