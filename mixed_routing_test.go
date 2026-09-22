package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mixedTestConfig() *Config {
	cfg := policyTestConfig()
	cfg.OwnerMixedRouting = true
	cfg.AntigravityBin = "/usr/bin/true"
	cfg.Antigravity = &AntigravityRoute{Enabled: true, Effort: "low"}
	return cfg
}

func TestMixedRoutingActualConsumers(t *testing.T) {
	cfg := mixedTestConfig()
	for _, tc := range []struct{ class, runner, model, effort string }{
		{"development", grokBuildRunnerName, "grok-4.7", "high"},
		{"simple-development", grokBuildRunnerName, "grok-4.6", "high"},
		{"gpt-complex", "codex", "gpt-6-astra", "high"},
		{"gpt-short", "codex", "gpt-5.6-sol", "xhigh"},
		{"management", antigravityRunnerName, "gemini-3.8-flash-high", "high"},
	} {
		t.Run(tc.class, func(t *testing.T) {
			task := &Task{ID: "mixed-test", WorkClass: tc.class, Type: typeSequence, Model: "sonnet", RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary, PreferRunner: "codex", Prompts: []string{"p"}, Dir: t.TempDir(), Status: statusQueued}
			before, _ := json.Marshal(task)
			pending := toBrief(cfg, task, time.Now())
			if pending.Model != tc.model || pending.Effort != tc.effort {
				t.Fatalf("pending board identity %+v", pending)
			}
			_, leg, cmd, ok := ownerManualDispatchCommand(cfg, task, "p")
			after, _ := json.Marshal(task)
			if !ok || leg.Runner != tc.runner || leg.Model != tc.model || leg.Effort != tc.effort || !strings.Contains(cmd, tc.model) || string(before) != string(after) {
				t.Fatalf("manual route/immutability mismatch %+v %s", leg, cmd)
			}
			if tc.class == "management" && !strings.Contains(cmd, "--mode plan") {
				t.Fatalf("management command is not plan: %s", cmd)
			}
			runner, matched := ownerPrimaryDispatch(testRoot(t), cfg, task, time.Now())
			if !matched || runner != tc.runner {
				t.Fatalf("dispatch %q %v", runner, matched)
			}
			if err := closedOwnerTaskStateError(task); err != nil {
				t.Fatal(err)
			}
			route, ok := resolveOwnerRouteReadback(cfg, task)
			if !ok || route.Legs[0] != leg {
				t.Fatalf("frozen readback mismatch %+v", route)
			}
			brief := toBrief(cfg, task, time.Now())
			if brief.Model != tc.model || brief.Effort != tc.effort {
				t.Fatalf("board identity %+v", brief)
			}
			if tc.runner == grokBuildRunnerName {
				cfg.GrokBuild.Model = "stable"
				if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
					t.Fatal(err)
				}
				if task.GrokModel != tc.model {
					t.Fatalf("stable upgraded pin: %s", task.GrokModel)
				}
			}
		})
	}
}

func TestMixedAdmissionDefaultsAndOldIdentity(t *testing.T) {
	cfg := mixedTestConfig()
	task := newTask(testRoot(t), cfg, typeSequence, "ordinary frontend", t.TempDir(), []string{"p"}, 1)
	if task.WorkClass != "development" {
		t.Fatalf("ordinary default %s", task.WorkClass)
	}
	task.RouteClass = routeClassGeneral
	route, ok := resolveOwnerRoute(cfg, task)
	if !ok || route.Legs[0].Model != "grok-4.7" {
		t.Fatalf("ordinary general was not native Grok: %+v", route)
	}
	old := *task
	old.WorkClass = ""
	old.Model = "sonnet"
	cfg.OwnerMixedRouting = false
	historical, ok := resolveOwnerRoute(cfg, &old)
	if !ok {
		t.Fatal("no legacy route")
	}
	if !pinOwnerPrimaryRoute(&old, historical) {
		t.Fatal("legacy pin failed")
	}
	frozen, _ := json.Marshal(&old)
	cfg.OwnerMixedRouting = true
	got, ok := resolveOwnerRouteReadback(cfg, &old)
	if !ok || !reflect.DeepEqual(got, historical) {
		t.Fatalf("old frozen route changed %+v", got)
	}
	current, _ := json.Marshal(&old)
	if string(current) != string(frozen) {
		t.Fatal("readback mutated legacy card")
	}
	for _, change := range []func(*Task){func(t *Task) { t.RunnerExplicit = true }, func(t *Task) { t.CodexModel = "explicit" }, func(t *Task) { t.GrokModel = "grok-4.6" }, func(t *Task) { t.SessionID = "existing" }, func(t *Task) { t.XRole = "a" }} {
		copy := *task
		change(&copy)
		if _, ok := resolveOwnerRoute(cfg, &copy); ok {
			t.Fatal("explicit/session identity rerouted")
		}
	}
	for _, pair := range []struct{ class, effort, want string }{{"gpt-complex", "medium", "medium"}, {"gpt-short", "max", "max"}} {
		task.WorkClass, task.Effort, task.EffortExplicit = pair.class, pair.effort, true
		route, _ := resolveOwnerRoute(cfg, task)
		if route.Legs[0].Effort != pair.want {
			t.Fatal("explicit effort lost")
		}
	}
}

func TestMixedQuotaStrictAndSameModel(t *testing.T) {
	cfg := mixedTestConfig()
	for _, class := range []string{"development", "simple-development"} {
		task := &Task{ID: "mixed-quota", Type: typeSequence, WorkClass: class, PreferRunner: "codex", RouteClass: routeClassGeneral, Prompts: []string{"p"}}
		route, _ := resolveOwnerRoute(cfg, task)
		pinOwnerPrimaryRoute(task, route)
		for _, tc := range []struct {
			diagnostic, subtype string
			want                bool
		}{
			{"quota exhausted", "grok_build_error", true}, {"HTTP 429: usage limit reached", "grok_build_error", true},
			{"429", "grok_build_error", false}, {"rate limit", "grok_build_error", false}, {"too many requests", "grok_build_error", false},
			{"401 unauthorized; quota exhausted", "grok_build_error", false}, {"refusal: quota exhausted", "grok_build_error", false},
			{"connection reset; quota exhausted", "grok_build_error", false}, {"quota exhausted", "grok_build_stream_incomplete", false}, {"quota exhausted", "grok_build_invalid_terminal", false},
		} {
			res := &claudeResult{IsError: true, Result: tc.diagnostic, Subtype: tc.subtype, ObservationComplete: true}
			kind, ok := classifyTaskPolicyFallbackFailure(task, grokBuildRunnerName, res, "", errors.New("exit status 1"))
			if ok != tc.want || ok && kind != fallbackQuota {
				t.Fatalf("%s: %s %v", tc.diagnostic, kind, ok)
			}
		}
		if _, ok := classifyTaskPolicyFallbackFailure(task, grokBuildRunnerName, &claudeResult{IsError: true, Subtype: "grok_build_error", ObservationComplete: true}, `{"type":"text","text":"quota exhausted"}`, errors.New("failed")); ok {
			t.Fatal("assistant quotation accepted")
		}
		proof := safeFixtureProof(t)
		auth, err := authorizePolicyFallback(proof)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(task)
		if err := queuePolicyFallback(cfg, task, fallbackTransport, auth); err == nil {
			t.Fatal("transport transition allowed")
		}
		after, _ := json.Marshal(task)
		if string(before) != string(after) {
			t.Fatal("denied transition mutated task")
		}
		task.LastRouteAttempt = &RouteAttemptReadback{RequestedRunner: grokBuildRunnerName, RequestedModel: task.GrokModel, RequestedEffort: "high"}
		if err := queuePolicyFallback(cfg, task, fallbackQuota, auth); err != nil {
			t.Fatal(err)
		}
		if task.PreferRunner != cursorRunnerName || task.CursorModel != mixedCursorEquivalent(task.LastRouteAttempt.RequestedModel) || task.LastRouteAttempt.RequestedEffort != "high" {
			t.Fatalf("fallback mismatch %+v", task)
		}
		if _, ok := resolveOwnerRouteReadback(cfg, task); !ok {
			t.Fatal("cursor frozen readback failed")
		}
		task.Runner = grokBuildRunnerName // last attempt must not override the queued leg
		_, leg, cmd, ok := ownerManualDispatchCommand(cfg, task, "p")
		if !ok || leg.Runner != cursorRunnerName || !strings.Contains(cmd, task.CursorModel) {
			t.Fatalf("manual fallback selected old runner: %+v %s", leg, cmd)
		}
		if policyFallbackCandidate(cfg, task, cursorRunnerName) {
			t.Fatal("cursor fallback can chain")
		}
	}
}

func TestMixedRunTaskNativeQuotaTransition(t *testing.T) {
	for _, tc := range []struct {
		diagnostic string
		fallback   bool
	}{{"HTTP 429: usage limit reached", true}, {"rate limit", false}, {"401 unauthorized; quota exhausted", false}, {"refusal: quota exhausted", false}, {"connection reset; quota exhausted", false}} {
		t.Run(tc.diagnostic, func(t *testing.T) {
			root := testRoot(t)
			event, _ := json.Marshal(map[string]string{"type": "error", "message": tc.diagnostic})
			bin, _, _ := fakeGrokBuild(t, string(event), "", 1)
			cfg := mixedTestConfig()
			cfg.GrokBuildBin = bin
			isolateGrokLifecycleHome(t, cfg)
			cfg.StepTimeoutMin = 1
			task := newTask(root, cfg, typeSequence, "bounded fixture", t.TempDir(), []string{"p"}, 1)
			task.WorkClass = "simple-development"
			task.RouteClass = routeClassGeneral
			route, _ := resolveOwnerRoute(cfg, task)
			pinOwnerPrimaryRoute(task, route)
			if err := saveTask(root, task); err != nil {
				t.Fatal(err)
			}
			if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
				t.Fatal(err)
			}
			got, err := loadTask(root, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (got.PreferRunner == cursorRunnerName) != tc.fallback {
				t.Fatalf("fallback=%v status=%s runner=%s error=%s", tc.fallback, got.Status, got.PreferRunner, got.LastError)
			}
			if tc.fallback && (got.CursorModel != "cursor-grok-4.6-high" || got.Status != statusQueued || got.LastRouteAttempt == nil || got.LastRouteAttempt.RequestedModel != "grok-4.6") {
				t.Fatalf("incorrect frozen fallback %+v", got)
			}
		})
	}
}

func TestMixedRouteCommandNoMutation(t *testing.T) {
	root := testRoot(t)
	cfg := mixedTestConfig()
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(root, "config.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := capturePlainPolicyFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdRoute([]string{"-root", root, "-work-class", "simple-development"}); err != nil {
		t.Fatal(err)
	}
	after, err := capturePlainPolicyFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest != after.Digest {
		t.Fatal("route command wrote data")
	}
}

func TestMixedAutomaticCodexBudgetRuntimeHold(t *testing.T) {
	for _, class := range []string{"gpt-complex", "gpt-short"} {
		for _, state := range []string{"missing", "stale", "above"} {
			t.Run(class+"/"+state, func(t *testing.T) {
				root := testRoot(t)
				cfg := mixedTestConfig()
				cfg.UsageFeed = filepath.Join(t.TempDir(), "usage.jsonl")
				cfg.UsageFeedMaxAgeMin = 90
				if state != "missing" {
					sampled := time.Now().UTC()
					used := 66
					if state == "stale" {
						sampled = sampled.Add(-24 * time.Hour)
						used = 1
					}
					sample, _ := json.Marshal(map[string]any{"provider": "codex", "sampledAt": sampled.Format(time.RFC3339), "usedPercent": used, "windowMinutes": 300, "windowKind": "primary"})
					if err := os.WriteFile(cfg.UsageFeed, append(sample, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
				task := newTask(root, cfg, typeSequence, "budget fixture", t.TempDir(), []string{"p"}, 1)
				task.WorkClass = class
				task.RouteClass = routeClassGeneral
				task.RiskClass = riskClassOrdinary
				route, _ := resolveOwnerRoute(cfg, task)
				if !pinOwnerPrimaryRoute(task, route) {
					t.Fatal("pin")
				}
				if !task.AutomaticCodex {
					t.Fatal("automatic budget flag missing")
				}
				if err := closedOwnerTaskStateError(task); err != nil {
					t.Fatal(err)
				}
				evidence := currentAutomaticCodexBudgetEvidence(cfg, time.Now())
				if ok, _ := automaticCodexBudgetAllowed(task, evidence, 65); ok {
					t.Fatal("budget allowed")
				}
				if err := saveTask(root, task); err != nil {
					t.Fatal(err)
				}
				if err := runTaskVia(context.Background(), root, cfg, task, "codex"); err != nil {
					t.Fatal(err)
				}
				got, err := loadTask(root, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != statusHeld || !strings.Contains(got.LastError, "automatic Codex held") || got.LastRouteAttempt != nil {
					t.Fatalf("budget did not hold before invocation: %+v", got)
				}
			})
		}
	}
}

func TestMixedNewManualGoalDefaultsAndFrozenIdentity(t *testing.T) {
	cfg := mixedTestConfig()
	for _, class := range []string{"development", "simple-development"} {
		task := newTask(testRoot(t), cfg, typeSequence, "workflow writer", t.TempDir(), []string{"p"}, 1)
		task.WorkClass = class
		task.PreferRunner = grokBuildRunnerName
		task.RunnerExplicit = true
		if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
			t.Fatal(err)
		}
		want := "grok-4.7"
		if class == "simple-development" {
			want = "grok-4.6"
		}
		if task.GrokModel != want || task.GrokEffort != "high" {
			t.Fatalf("manual freeze %+v", task)
		}
		task.SessionID = "admitted-session"
		// Native Goal tuple needs the existing explicit sandbox contract.
		task.Goal = &TaskGoalBinding{}
		args, _, err := manualGoalCommandArgs(cfg, task)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(args, " ")
		if !strings.Contains(text, "--model "+want) || !strings.Contains(text, "--reasoning-effort high") {
			t.Fatalf("goal argv %v", args)
		}
	}
	for _, task := range []*Task{
		{PreferRunner: grokBuildRunnerName, RunnerExplicit: true, WorkClass: "development", GrokModel: "grok-4.6", GrokEffort: "high"},
		{PreferRunner: grokBuildRunnerName, RunnerExplicit: true, WorkClass: "development", GrokModel: "grok-4.6", GrokEffort: "xhigh", SessionID: "old-session"},
	} {
		before := *task
		if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, *task) {
			t.Fatal("explicit/old identity changed")
		}
	}
}

func TestMixedAddAndYvonneEmitClassification(t *testing.T) {
	root := testRoot(t)
	cfg := mixedTestConfig()
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	if err := cmdAdd([]string{"-root", root, "-dir", t.TempDir(), "-work-class", "simple-development", "-route-class", "general", "-risk-class", "ordinary", "-hold", "bounded mechanical task"}); err != nil {
		t.Fatal(err)
	}
	cards, err := loadBoardTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].WorkClass != "simple-development" || cards[0].Status != statusHeld {
		t.Fatalf("add classification %+v", cards)
	}
	route, ok := resolveOwnerRoute(cfg, cards[0])
	if !ok || route.Legs[0].Model != "grok-4.6" {
		t.Fatalf("add readback %+v", route)
	}
	parent := newTask(root, cfg, typeCoordinate, "Yvonne", t.TempDir(), []string{"p"}, 1)
	result := "```json\n" + `{"tasks":[{"title":"ordinary frontend","type":"sequence","route_class":"general","risk_class":"ordinary","prompts":["p"]},{"title":"long GPT","type":"sequence","route_class":"general","risk_class":"ordinary","work_class":"gpt-complex","prompts":["p"]}]}` + "\n```"
	ids, err := enqueueEmitted(root, cfg, parent, result)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("emit %v", ids)
	}
	for i, id := range ids {
		card, err := loadTask(root, id)
		if err != nil {
			t.Fatal(err)
		}
		route, ok := resolveOwnerRoute(cfg, card)
		want := "grok-4.7"
		if i == 1 {
			want = "gpt-6-astra"
		}
		if !ok || route.Legs[0].Model != want {
			t.Fatalf("emit identity %+v", route)
		}
	}
}

func TestMixedCursorQuotaLegActualInvocation(t *testing.T) {
	root := testRoot(t)
	cfg := mixedTestConfig()
	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"CURSOR_OK"}]}}` + "\n" + `{"type":"result","subtype":"success","result":"CURSOR_OK"}`
	bin, argsPath := fakeCursorAgent(t, stream, "", 0)
	cfg.CursorBin = bin
	task := newTask(root, cfg, typeSequence, "fixture", t.TempDir(), []string{"p"}, 1)
	task.WorkClass = "simple-development"
	task.RouteClass = routeClassGeneral
	route, _ := resolveOwnerRoute(cfg, task)
	pinOwnerPrimaryRoute(task, route)
	task.Runner = grokBuildRunnerName
	auth, err := authorizePolicyFallback(safeFixtureProof(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := queuePolicyFallback(cfg, task, fallbackQuota, auth); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, cursorRunnerName); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusDone || got.RouteReason != mixedQuotaReason || !strings.Contains(string(args), "cursor-grok-4.6-high") || got.LastRouteAttempt.RequestedModel != "cursor-grok-4.6-high" || got.LastRouteAttempt.RequestedEffort != "high" {
		t.Fatalf("actual Cursor leg %s %+v", args, got)
	}
}

func TestMixedCodexExactArgv(t *testing.T) {
	for _, class := range []string{"gpt-complex", "gpt-short"} {
		cfg := mixedTestConfig()
		capture := filepath.Join(t.TempDir(), "argv")
		cfg.CodexBin = fakeCodexArgvCapture(t, capture)
		task := &Task{ID: "mixed-argv-" + class, Type: typeSequence, WorkClass: class, PreferRunner: "codex", RiskClass: riskClassOrdinary, RouteClass: routeClassGeneral, Dir: t.TempDir(), Prompts: []string{"p"}}
		route, _ := resolveOwnerRoute(cfg, task)
		pinOwnerPrimaryRoute(task, route)
		root := admitDirectInvoke(t, "", task)
		_, _, _ = invokeCodex(context.Background(), root, cfg, task, "p")
		args, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(args), route.Legs[0].Model) || !strings.Contains(string(args), "model_reasoning_effort="+route.Legs[0].Effort) {
			t.Fatalf("codex exact argv %s", args)
		}
	}
}
