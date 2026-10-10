package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDailyGrok46NewRouteAndFrozenHistory(t *testing.T) {
	cfg := mixedTestConfig()
	cfg.DispatchMode = dispatchModeDaily
	task := modeTask("development")
	route, ok := resolveOwnerRoute(cfg, task)
	if !ok || route.Legs[0].Model != "grok-4.6" || route.Legs[0].Effort != "xhigh" ||
		route.Legs[1].Model != "cursor-grok-4.6-xhigh" || route.Legs[1].Effort != "xhigh" {
		t.Fatalf("new daily route: %+v", route)
	}
	if !pinOwnerPrimaryRoute(task, route) {
		t.Fatal("pin failed")
	}
	if got, ok := resolveOwnerRouteReadback(cfg, task); !ok || !reflect.DeepEqual(got, route) {
		t.Fatalf("new frozen route: %+v", got)
	}
	_, leg, cmd, ok := ownerManualDispatchCommand(cfg, task, "p")
	if !ok || leg.Model != "grok-4.6" || !strings.Contains(cmd, "--reasoning-effort xhigh") {
		t.Fatalf("manual consumer: %+v %s", leg, cmd)
	}
	old := *task
	old.FrozenRoute = nil // Legacy task predates complete route snapshots.
	old.OwnerRouteName = "mixed_daily_development"
	old.GrokModel, old.GrokEffort = "grok-4.7", "high"
	if got, ok := resolveOwnerRouteReadback(cfg, &old); !ok || got.Legs[0].Model != "grok-4.7" || got.Legs[0].Effort != "high" {
		t.Fatalf("historical route was changed: %+v", got)
	}
	for _, status := range []string{statusRunning, statusHeld, statusDone} {
		frozen := old
		frozen.Status = status
		before := frozen
		if refreshUnstartedDispatchMode(cfg, &frozen, time.Now()) || !reflect.DeepEqual(before, frozen) {
			t.Fatalf("changed %s task", status)
		}
	}
	if !refreshUnstartedDispatchMode(cfg, &old, time.Now()) || old.GrokModel != "grok-4.6" || old.GrokEffort != "xhigh" {
		t.Fatalf("unstarted task did not adopt daily route: %+v", old)
	}
}

func TestDailyGrok46ManualGoalAndQuotaEquivalent(t *testing.T) {
	cfg := mixedTestConfig()
	task := &Task{ID: "daily-grok46", Type: typeSequence, WorkClass: "development", PreferRunner: grokBuildRunnerName, RunnerExplicit: true}
	if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
		t.Fatal(err)
	}
	if task.GrokModel != "grok-4.6" || task.GrokEffort != "xhigh" {
		t.Fatalf("manual goal defaults: %+v", task)
	}
	task = modeTask("development")
	route, _ := resolveOwnerRoute(cfg, task)
	pinOwnerPrimaryRoute(task, route)
	auth, err := authorizePolicyFallback(safeFixtureProof(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := queuePolicyFallback(cfg, task, fallbackQuota, auth); err != nil {
		t.Fatal(err)
	}
	if task.CursorModel != "cursor-grok-4.6-xhigh" || task.Effort != "xhigh" {
		t.Fatalf("quota successor weakened effort: %+v", task)
	}
	if _, ok := resolveOwnerRouteReadback(cfg, task); !ok {
		t.Fatal("quota successor readback failed")
	}
}
