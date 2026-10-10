package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConfigurableDispatch3x3Matrix(t *testing.T) {
	cfg := policyTestConfig()
	unique := RouteMatrix{
		"high": {
			"frontend": {Runner: grokBuildRunnerName, Model: "grok-high-fe", Effort: "xhigh"},
			"backend":  {Runner: grokBuildRunnerName, Model: "grok-high-be", Effort: "xhigh"},
			"general":  {Runner: grokBuildRunnerName, Model: "grok-high-gen", Effort: "xhigh"},
		},
		"medium": {
			"frontend": {Runner: grokBuildRunnerName, Model: "grok-med-fe", Effort: "high"},
			"backend":  {Runner: grokBuildRunnerName, Model: "grok-med-be", Effort: "high"},
			"general":  {Runner: grokBuildRunnerName, Model: "grok-med-gen", Effort: "high"},
		},
		"low": {
			"frontend": {Runner: grokBuildRunnerName, Model: "grok-low-fe", Effort: "high"},
			"backend":  {Runner: grokBuildRunnerName, Model: "grok-low-be", Effort: "high"},
			"general":  {Runner: grokBuildRunnerName, Model: "grok-low-gen", Effort: "high"},
		},
	}
	cfg.RouteMatrix = unique
	if err := validateConfigurableDispatch(cfg); err != nil {
		t.Fatal(err)
	}
	models := map[string]string{complexityHigh: "opus", complexityMedium: "sonnet", complexityLow: "haiku"}
	classes := map[string]string{routeCategoryFE: routeClassFrontend, routeCategoryBE: routeClassBackend, routeCategoryGen: routeClassGeneral}
	for _, complexity := range []string{complexityHigh, complexityMedium, complexityLow} {
		for _, category := range []string{routeCategoryFE, routeCategoryBE, routeCategoryGen} {
			task := &Task{Type: typeSequence, Model: models[complexity], RouteClass: classes[category], RiskClass: riskClassOrdinary, PreferRunner: "codex", FreshSteps: true, Prompts: []string{"p"}}
			route, ok := resolveOwnerRoute(cfg, task)
			if !ok || len(route.Legs) == 0 {
				t.Fatalf("%s/%s unresolved %+v", complexity, category, route)
			}
			want := unique[complexity][category]
			if route.Legs[0].Runner != want.Runner || route.Legs[0].Model != want.Model || route.Legs[0].Effort != want.Effort {
				t.Fatalf("%s/%s primary %+v want %+v", complexity, category, route.Legs[0], want)
			}
			if category == routeCategoryFE && route.ReleaseGate != nil {
				t.Fatalf("%s/frontend category created an unrequested specialized gate", complexity)
			}
			if category == routeCategoryBE && complexity == complexityHigh && (route.Review == nil || route.Name != "opus_backend_ordinary") {
				t.Fatalf("high/backend safety drifted %+v", route)
			}
		}
	}
	alias := &Task{Type: typeSequence, Model: "opus", RouteClass: routeClassFrontend, RiskClass: riskClassOrdinary, PreferRunner: "codex", FreshSteps: true, Prompts: []string{"p"}}
	got, ok := resolveOwnerRoute(cfg, alias)
	if !ok || got.Legs[0].Model != "grok-high-fe" {
		t.Fatalf("opus/frontend alias %+v", got)
	}
}

func TestConfigurableDispatchNewDefaultPrimaries(t *testing.T) {
	cfg := mixedTestConfig()
	for _, tc := range []struct{ class, runner, model, effort string }{
		{"development", grokBuildRunnerName, "grok-4.6", "xhigh"},
		{"simple-development", grokBuildRunnerName, "grok-4.6", "high"},
		{"gpt-complex", "codex", "gpt-6.1-sol", "high"},
		{"gpt-short", grokBuildRunnerName, "grok-4.6", "xhigh"},
		{"management", antigravityRunnerName, "gemini-3.8-flash-high", "high"},
	} {
		task := &Task{Type: typeSequence, WorkClass: tc.class, RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary, PreferRunner: "codex", Prompts: []string{"p"}}
		route, ok := resolveOwnerRoute(cfg, task)
		if !ok || route.Legs[0].Runner != tc.runner || route.Legs[0].Model != tc.model || route.Legs[0].Effort != tc.effort {
			t.Fatalf("%s %+v", tc.class, route)
		}
	}
	medium := &Task{Type: typeSequence, WorkClass: "gpt-complex", RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary, PreferRunner: "codex", Effort: "medium", EffortExplicit: true, Prompts: []string{"p"}}
	route, ok := resolveOwnerRoute(cfg, medium)
	if !ok || route.Legs[0].Model != "gpt-6.1-sol" || route.Legs[0].Effort != "medium" {
		t.Fatalf("gpt-complex explicit medium %+v", route)
	}
}

func TestConfigurableDispatchConfigEditFreezesOldRoute(t *testing.T) {
	cfg := mixedTestConfig()
	task := &Task{ID: "freeze-edit", Type: typeSequence, WorkClass: "gpt-complex", RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary, PreferRunner: "codex", Prompts: []string{"p"}, Status: statusQueued, Dir: t.TempDir()}
	route, ok := resolveOwnerRoute(cfg, task)
	if !ok || !pinOwnerPrimaryRoute(task, route) {
		t.Fatal("admit")
	}
	if !completeFrozenRoute(task) || task.FrozenRoute.Legs[0].Model != "gpt-6.1-sol" {
		t.Fatalf("missing snapshot %+v", task.FrozenRoute)
	}
	frozen, _ := json.Marshal(task.FrozenRoute)
	later := ownerRouteFromFrozen(task.FrozenRoute)
	cfg.WorkClassRoutes = map[string]ConfiguredRoute{
		"gpt-complex": {Runner: "codex", Model: "gpt-5.6-sol", Effort: "high", Fallback: []RouteLegConfig{{Runner: kimiCLIRunnerName, Model: "kimi-code/k3", Effort: "max"}}},
	}
	fresh := &Task{Type: typeSequence, WorkClass: "gpt-complex", RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary, PreferRunner: "codex", Prompts: []string{"p"}}
	updated, ok := resolveOwnerRoute(cfg, fresh)
	if !ok || updated.Legs[0].Model != "gpt-5.6-sol" || len(updated.Legs) < 2 || updated.Legs[1].Runner != kimiCLIRunnerName {
		t.Fatalf("new unfrozen did not consume config %+v", updated)
	}
	got, ok := resolveOwnerRouteReadback(cfg, task)
	if !ok || !reflect.DeepEqual(got, later) {
		t.Fatalf("frozen later legs drifted %+v", got)
	}
	current, _ := json.Marshal(task.FrozenRoute)
	if string(current) != string(frozen) {
		t.Fatal("snapshot bytes changed")
	}
	if err := closedOwnerTaskStateError(task); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurableDispatchLegacyIncompleteSnapshot(t *testing.T) {
	cfg := mixedTestConfig()
	old := &Task{ID: "legacy-astra", Type: typeSequence, WorkClass: "gpt-complex", RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary, PreferRunner: "codex", CodexModel: "gpt-6-astra", Effort: "high", EffortExplicit: true, AutomaticCodex: true, OwnerRouteName: "mixed_gpt_complex", OwnerRouteLeg: 1, OwnerRouteStage: routeStagePrimary, RouteReason: mixedRouteReason, Prompts: []string{"p"}}
	cfg.WorkClassRoutes = map[string]ConfiguredRoute{
		"gpt-complex": {Runner: "codex", Model: "gpt-5.6-sol", Effort: "xhigh", Fallback: []RouteLegConfig{{Runner: kimiCLIRunnerName, Model: "kimi-code/k3", Effort: "max"}}},
	}
	got, ok := resolveOwnerRouteReadback(cfg, old)
	if !ok || got.Legs[0].Model != "gpt-6-astra" || got.Legs[0].Effort != "high" {
		t.Fatalf("legacy identity rewritten %+v", got)
	}
	if len(got.Legs) > 1 && got.Legs[1].Runner == kimiCLIRunnerName {
		t.Fatal("legacy incomplete snapshot derived a new fallback from live config")
	}
	if err := closedOwnerTaskStateError(old); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurableDispatchInvalidConfigRefused(t *testing.T) {
	root := testRoot(t)
	cfg := mixedTestConfig()
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath(root))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["work_class_routes"] = map[string]any{"gpt-complex": map[string]any{"runner": "not-a-runner", "model": "x", "effort": "high"}}
	bad, _ := json.MarshalIndent(fields, "", "  ")
	if err := os.WriteFile(configPath(root), append(bad, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = loadConfig(root)
	if err == nil || !strings.Contains(err.Error(), "not a legal configured identity") {
		t.Fatalf("invalid runner accepted: %v", err)
	}
	fields["work_class_routes"] = map[string]any{"gpt-short": map[string]any{"runner": grokBuildRunnerName, "model": "grok-4.6", "effort": "max"}}
	bad, _ = json.MarshalIndent(fields, "", "  ")
	if err := os.WriteFile(configPath(root), append(bad, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = loadConfig(root)
	if err == nil || !strings.Contains(err.Error(), "grok-build effort") {
		t.Fatalf("grok max accepted: %v", err)
	}
}

func TestConfigurableDispatchPinsHeldTerminalUnknownUnchanged(t *testing.T) {
	cfg := mixedTestConfig()
	base := &Task{ID: "pin-hold", Type: typeSequence, WorkClass: "development", RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary, PreferRunner: "codex", Prompts: []string{"p"}, Dir: t.TempDir(), Status: statusQueued}
	route, _ := resolveOwnerRoute(cfg, base)
	if !pinOwnerPrimaryRoute(base, route) {
		t.Fatal("pin")
	}
	cfg.WorkClassRoutes = map[string]ConfiguredRoute{
		"development": {Runner: grokBuildRunnerName, Model: "grok-4.7", Effort: "high"},
	}
	for _, mutate := range []func(*Task){
		func(t *Task) { t.Status = statusHeld },
		func(t *Task) { t.Status = statusDone },
		func(t *Task) { t.Status = statusFailed },
		func(t *Task) { t.RunnerExplicit = true },
		func(t *Task) { t.SessionID = "existing-session" },
		func(t *Task) { t.CodexModel = "explicit-pin" },
		func(t *Task) { t.XRole = "a" },
	} {
		copy := *base
		if copy.FrozenRoute != nil {
			snap := *copy.FrozenRoute
			copy.FrozenRoute = &snap
		}
		mutate(&copy)
		before, _ := json.Marshal(copy)
		if refreshUnstartedDispatchMode(cfg, &copy, time.Now()) {
			t.Fatal("rerouted")
		}
		if _, ok := resolveOwnerRoute(cfg, &copy); ok && (copy.RunnerExplicit || copy.SessionID != "" || copy.CodexModel == "explicit-pin" || copy.XRole != "") {
			t.Fatal("explicit identity became auto-routable")
		}
		after, _ := json.Marshal(copy)
		if string(before) != string(after) {
			t.Fatal("pin/held/terminal mutated")
		}
	}
}

func TestConfigurableDispatchFallbackSafeguards(t *testing.T) {
	cfg := mixedTestConfig()
	cfg.WorkClassRoutes = map[string]ConfiguredRoute{
		"development": {
			Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "xhigh",
			Fallback: []RouteLegConfig{{Runner: cursorRunnerName, Model: "cursor-grok-4.6-xhigh", Effort: "xhigh"}},
		},
	}
	task := &Task{ID: "fb-safe", Type: typeSequence, WorkClass: "development", RouteClass: routeClassGeneral, PreferRunner: "codex", Prompts: []string{"p"}, Dir: t.TempDir()}
	route, _ := resolveOwnerRoute(cfg, task)
	if !pinOwnerPrimaryRoute(task, route) {
		t.Fatal("pin")
	}
	if len(route.Legs) != 2 || route.Legs[1].Runner != cursorRunnerName {
		t.Fatalf("configured fallback dropped %+v", route)
	}
	auth, err := authorizePolicyFallback(safeFixtureProof(t))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(task)
	if err := queuePolicyFallback(cfg, task, fallbackTransport, auth); err == nil {
		t.Fatal("transport fallback accepted")
	}
	after, _ := json.Marshal(task)
	if string(before) != string(after) {
		t.Fatal("denied fallback mutated task")
	}
	if err := queuePolicyFallback(cfg, task, fallbackQuota, auth); err != nil {
		t.Fatal(err)
	}
	if task.PreferRunner != cursorRunnerName || task.CursorModel != "cursor-grok-4.6-xhigh" {
		t.Fatalf("quota successor %+v", task)
	}
	got, ok := resolveOwnerRouteReadback(cfg, task)
	if !ok || got.Legs[1].Model != "cursor-grok-4.6-xhigh" {
		t.Fatalf("frozen fallback readback %+v", got)
	}
}

func TestConfigurableDispatchAddEmitGoalAdmission(t *testing.T) {
	root := testRoot(t)
	cfg := mixedTestConfig()
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := cmdAdd([]string{"-root", root, "-dir", work, "-work-class", "gpt-complex", "-route-class", "general", "-risk-class", "ordinary", "-hold", "bounded gpt-complex"}); err != nil {
		t.Fatal(err)
	}
	cards, err := loadBoardTasks(root)
	if err != nil || len(cards) != 1 {
		t.Fatalf("add %v %+v", err, cards)
	}
	first := cards[0]
	if !completeFrozenRoute(first) || first.FrozenRoute.Legs[0].Model != "gpt-6.1-sol" || first.Status != statusHeld {
		t.Fatalf("add did not freeze %+v", first.FrozenRoute)
	}
	parent := newTask(root, cfg, typeCoordinate, "emit-parent", work, []string{"p"}, 1)
	result := "```json\n" + `{"tasks":[{"title":"emitted general","type":"sequence","route_class":"general","risk_class":"ordinary","work_class":"gpt-short","prompts":["p"]}]}` + "\n```"
	ids, err := enqueueEmitted(root, cfg, parent, result)
	if err != nil || len(ids) != 1 {
		t.Fatalf("emit %v %v", err, ids)
	}
	emitted, err := loadTask(root, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if !completeFrozenRoute(emitted) || emitted.FrozenRoute.Legs[0].Runner != grokBuildRunnerName || emitted.FrozenRoute.Legs[0].Model != "grok-4.6" {
		t.Fatalf("emit freeze %+v", emitted.FrozenRoute)
	}
	goal := newTask(root, cfg, typeSequence, "goal writer", work, []string{"p"}, 1)
	goal.WorkClass = "development"
	goal.PreferRunner = grokBuildRunnerName
	goal.RunnerExplicit = true
	goal.Goal = &TaskGoalBinding{BudgetTokens: 40}
	if err := freezeManualGoalModel(context.Background(), root, cfg, goal); err != nil {
		t.Fatal(err)
	}
	if goal.GrokModel != "grok-4.6" || goal.GrokEffort != "xhigh" || !completeFrozenRoute(goal) {
		t.Fatalf("goal freeze %+v %+v", goal.GrokModel, goal.FrozenRoute)
	}
	cfg.WorkClassRoutes = map[string]ConfiguredRoute{
		"gpt-complex": {Runner: "codex", Model: "gpt-5.6-sol", Effort: "high"},
		"gpt-short":   {Runner: grokBuildRunnerName, Model: "grok-4.7", Effort: "high"},
		"development": {Runner: grokBuildRunnerName, Model: "grok-4.7", Effort: "high"},
	}
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	oldComplex, ok := resolveOwnerRouteReadback(loaded, first)
	if !ok || oldComplex.Legs[0].Model != "gpt-6.1-sol" {
		t.Fatalf("held card rerouted %+v", oldComplex)
	}
	oldShort, ok := resolveOwnerRouteReadback(loaded, emitted)
	if !ok || oldShort.Legs[0].Model != "grok-4.6" {
		t.Fatalf("emitted card rerouted %+v", oldShort)
	}
	if err := freezeManualGoalModel(context.Background(), "", loaded, goal); err != nil {
		t.Fatal(err)
	}
	if goal.GrokModel != "grok-4.6" {
		t.Fatalf("goal identity changed %s", goal.GrokModel)
	}
	if err := cmdAdd([]string{"-root", root, "-dir", work, "-work-class", "gpt-complex", "-route-class", "general", "-risk-class", "ordinary", "-hold", "new after edit"}); err != nil {
		t.Fatal(err)
	}
	all, _ := loadBoardTasks(root)
	var newest *Task
	for _, card := range all {
		if card.ID != first.ID && card.WorkClass == "gpt-complex" {
			newest = card
		}
	}
	if newest == nil || newest.FrozenRoute == nil || newest.FrozenRoute.Legs[0].Model != "gpt-5.6-sol" {
		t.Fatalf("new card did not pick up config %+v", newest)
	}
}

func TestConfigurableDispatchRoutePreviewMatchesAdmission(t *testing.T) {
	root := testRoot(t)
	cfg := mixedTestConfig()
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := cmdRoute([]string{"-root", root, "-work-class", "gpt-complex"})
	_ = w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	_, _ = buf.ReadFrom(r)
	if !strings.Contains(buf.String(), "gpt-6.1-sol") || !strings.Contains(buf.String(), `"Runner":"codex"`) {
		t.Fatalf("preview %s", buf.String())
	}
	task := &Task{Type: typeSequence, WorkClass: "gpt-complex", RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary, PreferRunner: "codex", Prompts: []string{"p"}}
	route, ok := resolveOwnerRoute(cfg, task)
	if !ok || route.Legs[0].Model != "gpt-6.1-sol" {
		t.Fatalf("admission %+v", route)
	}
	if err := cmdRoute([]string{"-root", root, "-complexity", "high", "-category", "frontend"}); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurableDispatchRouteCommandMatrixAndInvalid(t *testing.T) {
	root := testRoot(t)
	cfg := mixedTestConfig()
	cfg.RouteMatrix = RouteMatrix{"high": {"frontend": {Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "xhigh"}}}
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	if err := cmdRoute([]string{"-root", root, "-complexity", "opus", "-category", "frontend"}); err != nil {
		t.Fatal(err)
	}
	cfg.WorkClassRoutes = map[string]ConfiguredRoute{"gpt-complex": {Runner: "nope", Model: "x", Effort: "high"}}
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(root); err == nil {
		t.Fatal("invalid config loaded")
	}
}

func TestConfigurableDispatchCLIIsolatedRoot(t *testing.T) {
	root := testRoot(t)
	cfg := mixedTestConfig()
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := cmdRoute([]string{"-root", root, "-work-class", "development"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdAdd([]string{"-root", root, "-dir", work, "-work-class", "simple-development", "-route-class", "general", "-risk-class", "ordinary", "-hold", "isolated add"}); err != nil {
		t.Fatal(err)
	}
	cards, err := loadBoardTasks(root)
	if err != nil || len(cards) != 1 || cards[0].FrozenRoute == nil || cards[0].FrozenRoute.Legs[0].Model != "grok-4.6" {
		t.Fatalf("isolated add %+v %v", cards, err)
	}
	parent := newTask(root, cfg, typeCoordinate, "cli-emit", work, []string{"p"}, 1)
	ids, err := enqueueEmitted(root, cfg, parent, "```json\n"+`{"tasks":[{"title":"cli emit","type":"sequence","route_class":"frontend","risk_class":"ordinary","work_class":"development","prompts":["p"]}]}`+"\n```")
	if err != nil || len(ids) != 1 {
		t.Fatalf("cli emit %v %v", err, ids)
	}
	emitted, _ := loadTask(root, ids[0])
	if emitted.RouteClass != routeClassFrontend || emitted.FrozenRoute == nil {
		t.Fatalf("frontend emit %+v", emitted)
	}
	goal := newTask(root, cfg, typeSequence, "cli-goal", work, []string{"p"}, 1)
	goal.PreferRunner = grokBuildRunnerName
	goal.RunnerExplicit = true
	goal.WorkClass = "development"
	goal.Goal = &TaskGoalBinding{BudgetTokens: 12}
	if err := freezeManualGoalModel(context.Background(), root, cfg, goal); err != nil {
		t.Fatal(err)
	}
	if goal.GrokModel != "grok-4.6" || goal.FrozenRoute == nil {
		t.Fatalf("cli goal %+v", goal)
	}
	_ = filepath.Join(root, "config.json")
}

func TestConfigurableDispatchExactFallbackAndAliases(t *testing.T) {
	cfg := mixedTestConfig()
	cfg.DispatchMode = dispatchModeDaily
	for _, fallback := range [][]RouteLegConfig{nil, {}, {{Runner: cursorRunnerName, Model: "cursor-grok-4.6-xhigh", Effort: "xhigh"}}} {
		cfg.WorkClassRoutes = map[string]ConfiguredRoute{"development": {Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "xhigh", Fallback: fallback}}
		raw, _ := json.Marshal(cfg)
		var copy Config
		if err := json.Unmarshal(raw, &copy); err != nil {
			t.Fatal(err)
		}
		probe := &Task{Type: typeSequence, PreferRunner: "codex", WorkClass: "development", Prompts: []string{"p"}}
		route, ok := resolveOwnerRoute(&copy, probe)
		expected := 3
		if fallback != nil {
			expected = 1 + len(fallback)
		}
		if !ok || len(route.Legs) != expected {
			t.Fatalf("fallback=%#v legs=%+v", fallback, route.Legs)
		}
	}
	cfg = policyTestConfig()
	spec := ConfiguredRoute{Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: "high"}
	cfg.RouteMatrix = RouteMatrix{"opus": {"general": spec}, "high": {"general": spec}}
	if validateConfigurableDispatch(cfg) == nil {
		t.Fatal("duplicate tier alias accepted")
	}
	cfg.RouteMatrix = RouteMatrix{"OPUS": {"General": spec, "general": spec}}
	if validateConfigurableDispatch(cfg) == nil {
		t.Fatal("duplicate category alias accepted")
	}
	cfg.RouteMatrix = RouteMatrix{"OPUS": {"General": spec}}
	if err := validateConfigurableDispatch(cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.RouteMatrix["high"]["general"]; !ok {
		t.Fatal("aliases not canonicalized")
	}
	if validateRouteLegConfig("test", "opencode", "provider/model", "high") == nil {
		t.Fatal("unimplemented configured adapter accepted")
	}
}

func TestConfigurableDispatchMatrixRunnerPins(t *testing.T) {
	for _, runner := range []string{grokBuildRunnerName, cursorRunnerName, kimiCLIRunnerName, "codex", antigravityRunnerName} {
		cfg := policyTestConfig()
		cfg.RouteMatrix = RouteMatrix{"medium": {"general": {Runner: runner, Model: map[string]string{grokBuildRunnerName: "grok-4.6", cursorRunnerName: "cursor-grok-4.6-high", kimiCLIRunnerName: "kimi-code/k3", "codex": "gpt-6.1-sol", antigravityRunnerName: "gemini-3.8-flash-high"}[runner], Effort: "high", Fallback: []RouteLegConfig{}}}}
		task := &Task{Type: typeSequence, Model: "sonnet", PreferRunner: "codex", RouteClass: routeClassGeneral, Prompts: []string{"p"}}
		if err := admitTaskRoute(cfg, task); err != nil {
			t.Fatal(runner, err)
		}
		if task.PreferRunner != runner {
			t.Fatalf("%s actual pin %+v", runner, task)
		}
		if err := closedOwnerTaskStateError(task); err != nil {
			t.Fatal(runner, err)
		}
		if _, ok := resolveOwnerRouteReadback(cfg, task); !ok {
			t.Fatal(runner, "unreadable frozen route")
		}
	}
}

func TestConfigurableDispatchActualCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Unix provider fixture")
	}
	bin := filepath.Join(t.TempDir(), "cardex")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	run := func(args ...string) []byte {
		t.Helper()
		c := exec.Command(bin, args...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return out
	}
	root, dir := workflowTestRoot(t)
	cfg := mixedTestConfig()
	cfg.RouteMatrix = RouteMatrix{}
	for _, tier := range []string{"high", "medium", "low"} {
		cfg.RouteMatrix[tier] = map[string]ConfiguredRoute{}
		for _, cat := range []string{"frontend", "backend", "general"} {
			cfg.RouteMatrix[tier][cat] = ConfiguredRoute{Runner: grokBuildRunnerName, Model: "grok-4.6", Effort: map[string]string{"high": "xhigh", "medium": "high", "low": "medium"}[tier], Fallback: []RouteLegConfig{}}
		}
	}
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	for tier, model := range map[string]string{"high": "opus", "medium": "sonnet", "low": "haiku"} {
		for _, cat := range []string{"frontend", "backend", "general"} {
			var preview struct{ Primary policyLeg }
			if err := json.Unmarshal(run("route", "-root", root, "-complexity", tier, "-category", cat), &preview); err != nil {
				t.Fatal(err)
			}
			title := tier + "-" + cat
			run("add", "-root", root, "-dir", dir, "-title", title, "-model", model, "-route-class", cat, "-hold", "-review-after=false", "p")
			cards, _ := loadBoardTasks(root)
			var found *Task
			for _, card := range cards {
				if card.Title == title {
					found = card
				}
			}
			if found == nil || !completeFrozenRoute(found) || !ownerRouteSnapshotLegMatches(found, preview.Primary) {
				t.Fatalf("preview/add %s: %+v %+v", title, preview, found)
			}
		}
	}
	initWorkflow := func(module string) *WorkflowRecord {
		path := "internal/auth"
		if strings.HasSuffix(module, "new") {
			path = "internal/billing"
		}
		run("workflow", "init", "-root", root, "-module", module, "-goal", "bounded fixture", "-dir", dir, "-write-paths", path, "-terminal-criteria", "fake check", "-writer-engine", "auto", "-reviewer-engine", grokBuildRunnerName, "-model", "sonnet", "-route-class", "general", "-design-receipt", writeAstraDesignReceipt(t))
		wfs, err := loadWorkflows(root, cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, wf := range wfs {
			if wf.ModuleID == module {
				return wf
			}
		}
		t.Fatal("missing workflow")
		return nil
	}
	first := initWorkflow("matrix-goal-old")
	run("workflow", "writer", "-root", root, "-mode", "manual", first.ID)
	first, _ = loadWorkflow(root, cfg, first.ID)
	old, _ := loadTask(root, first.WriterTaskID)
	if old.GrokModel != "grok-4.6" || !completeFrozenRoute(old) || old.Goal.Provider != grokBuildRunnerName {
		t.Fatalf("old Goal %+v", old)
	}
	cfg.RouteMatrix["medium"]["general"] = ConfiguredRoute{Runner: grokBuildRunnerName, Model: "grok-4.7", Effort: "xhigh", Fallback: []RouteLegConfig{}}
	argv := filepath.Join(t.TempDir(), "goal.argv")
	cfg.GrokBuildBin = fakeGrokGoalBin(t, argv)
	isolateGrokLifecycleHome(t, cfg)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}

	// Actual dispatcher emits a matrix-selected child through a fake Codex CLI.
	emittedText := "```json\n" + `{"tasks":[{"title":"actual emitted matrix","type":"sequence","model":"sonnet","route_class":"general","prompts":["p"]}]}` + "\n```"
	cfg.CodexBin, _ = fakeNamedCodex(t, `{"type":"turn.completed"}`, "", emittedText, 0)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	run("add", "-root", root, "-dir", dir, "-title", "fake emit parent", "-type", "coordinate", "-runner", "codex", "-codex-model", "gpt-6.1-sol", "-emit", "-review-after=false", "p")
	parents, _ := loadBoardTasks(root)
	parentID := ""
	for _, p := range parents {
		if p.Title == "fake emit parent" {
			parentID = p.ID
		}
	}
	run("run", "-root", root, parentID)
	emittedCards, _ := loadBoardTasks(root)
	foundEmit := false
	for _, card := range emittedCards {
		if card.Title == "actual emitted matrix" {
			foundEmit = card.EmittedBy == parentID && completeFrozenRoute(card) && card.GrokModel == "grok-4.7"
		}
	}
	if !foundEmit {
		t.Fatal("actual emit ignored configured matrix")
	}
	second := initWorkflow("matrix-goal-new")
	run("workflow", "writer", "-root", root, "-mode", "manual", second.ID)
	second, _ = loadWorkflow(root, cfg, second.ID)
	fresh, _ := loadTask(root, second.WriterTaskID)
	if fresh.GrokModel != "grok-4.7" || fresh.GrokEffort != "xhigh" || len(fresh.FrozenRoute.Legs) != 1 {
		t.Fatalf("new Goal %+v", fresh)
	}
	oldAfter, _ := loadTask(root, old.ID)
	before, _ := json.Marshal(old)
	after, _ := json.Marshal(oldAfter)
	if !bytes.Equal(before, after) {
		t.Fatal("old Goal changed after config edit")
	}
	// Exercise the actual hosted adapter argv. Fake provider deliberately lacks
	// native completion: launch exit cannot be promoted to overall acceptance.
	launch := exec.Command(bin, "workflow", "goal-run", "-root", root, "-hosted", second.ID)
	_, _ = launch.CombinedOutput()
	args, err := os.ReadFile(argv)
	if err != nil || !strings.Contains(string(args), "grok-4.7") || !strings.Contains(string(args), "xhigh") {
		t.Fatalf("actual Goal argv %s %v", args, err)
	}
	final, _ := loadTask(root, fresh.ID)
	if final.Goal.Observation == goalObsDone {
		t.Fatal("fake exit misclassified complete")
	}
	t.Log("actual CLI 3x3 preview/add; edited config -> new Goal pin and hosted argv; old Goal unchanged; incomplete fake remains not done")
}

func TestConfigurableDispatchAutoGoalDirections(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := policyTestConfig()
	cfg.RouteMatrix = RouteMatrix{"medium": {"general": {Runner: grokBuildRunnerName, Model: "grok-4.7", Effort: "high", Fallback: []RouteLegConfig{}}}}
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	wf := initTestWorkflow(t, root, dir, "-writer-engine", "auto", "-reviewer-engine", grokBuildRunnerName)
	for _, name := range []string{"one", "two"} {
		task, err := admitNativeGoalDirection(root, cfg, wf, NativeGoalDirectionSpec{Name: name, Domain: &WriteDomain{ID: name, Lineage: name, Component: name, Paths: []string{"internal/" + name}}})
		if err != nil {
			t.Fatal(err)
		}
		if task.GrokModel != "grok-4.7" || task.GrokEffort != "high" || !completeFrozenRoute(task) {
			t.Fatalf("direction %s did not consume auto route %+v", name, task)
		}
	}
	if wf.NativeGoalTaskIDs[0] == wf.NativeGoalTaskIDs[1] {
		t.Fatal("direction identities reused")
	}
	cfg.OwnerRoutingEnforced, cfg.OwnerMixedRouting = false, false
	if _, err := loadWorkflow(root, cfg, wf.ID); err != nil {
		t.Fatalf("historical auto workflow readback depended on live defaults: %v", err)
	}
}

func TestConfigurableDispatchFrozenSnapshotRejectsDrift(t *testing.T) {
	cfg := policyTestConfig()
	task := &Task{Type: typeSequence, Model: "sonnet", PreferRunner: "codex", RouteClass: routeClassGeneral, Prompts: []string{"p"}}
	if err := admitTaskRoute(cfg, task); err != nil {
		t.Fatal(err)
	}
	task.FrozenRoute.Legs[0].Model = "changed"
	if _, ok := resolveOwnerRouteReadback(cfg, task); ok {
		t.Fatal("invalid full snapshot fell through to live configuration")
	}
	task = &Task{Type: typeSequence, Model: "sonnet", PreferRunner: "codex", RouteClass: routeClassFrontend, SpecializedFrontend: true, Prompts: []string{"p"}}
	route, ok := resolveOwnerRoute(cfg, task)
	if !ok || route.ReleaseGate == nil {
		t.Fatal("explicit specialized frontend gate lost")
	}
}
