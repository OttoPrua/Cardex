package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeNamedCodex(t *testing.T, body, stderr, final string, exit int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	capture := filepath.Join(dir, "argv")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' \"$@\" > " + shellQuote(capture) + "\nwhile [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then shift; printf '%s' " + shellQuote(final) + " > \"$1\"; fi; shift; done\nprintf '%s\\n' " + shellQuote(body) + "\nprintf '%s' " + shellQuote(stderr) + " >&2\nexit " + fmt.Sprint(exit) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return bin, capture
}

func TestDispatchModeActualCodexQuotaAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name, body, stderr string
		fallback           bool
	}{
		{"quota", `{"type":"turn.failed","error":{"message":"quota exhausted PRIVATE_SENTINEL"}}`, "", true},
		{"auth", `{"type":"turn.failed","error":{"message":"401 unauthorized; quota exhausted PRIVATE_SENTINEL"}}`, "", false},
		{"429", `{"type":"turn.failed","error":{"message":"429 PRIVATE_SENTINEL"}}`, "", false},
		{"partial", `{"type":"turn.failed","error":"PRIVATE_SENTINEL"}`, "", false},
		{"stderr", `{"type":"turn.failed","error":{"message":"quota exhausted"}}`, "PRIVATE_SENTINEL", false},
		{"work", `{"type":"item.completed","item":{"type":"command_execution","command":"PRIVATE_SENTINEL"}}` + "\n" + `{"type":"turn.failed","error":{"message":"quota exhausted"}}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, now := modeFixture(t)
			cfg.MaxAttempts = 3
			root := testRoot(t)
			cfg.CodexBin, _ = fakeNamedCodex(t, tc.body, tc.stderr, "", 1)
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			old := dispatchNow
			dispatchNow = func() time.Time { return now }
			defer func() { dispatchNow = old }()
			task := modeTask("development")
			task.Dir = t.TempDir()
			refreshUnstartedDispatchMode(cfg, task, now)
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
			if (got.KimiModel == "kimi-code/k3") != tc.fallback {
				t.Fatalf("fallback=%v task=%+v", tc.fallback, got)
			}
			if tc.fallback && (got.Attempts != 1 || got.Status != statusQueued || got.LastRouteAttempt.RequestedModel != "gpt-6-astra" || got.LastRouteAttempt.WorkspaceBefore != got.LastRouteAttempt.WorkspaceAfter) {
				t.Fatalf("bad proof %+v", got)
			}
			if !tc.fallback && got.Status != statusHeld {
				t.Fatalf("unsafe result not held: %+v", got)
			}
			if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				b, e := os.ReadFile(path)
				if e != nil {
					return e
				}
				if strings.Contains(string(b), "PRIVATE_SENTINEL") {
					t.Errorf("private provider payload persisted in %s", path)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDispatchModeActualLateBoundaries(t *testing.T) {
	for _, boundary := range []string{"tick-to-run", "after-preflight", "after-codex-prepare", "early-return"} {
		t.Run(boundary, func(t *testing.T) {
			cfg, now := modeFixture(t)
			root := testRoot(t)
			codexBin, capture := fakeNamedCodex(t, `{"type":"turn.completed","usage":{}}`, "", "CODEX_OK", 0)
			cfg.CodexBin = codexBin
			catalog := filepath.Join(t.TempDir(), "catalog.txt")
			writeCatalog(t, catalog, grok47CatalogText("grok-4.7"))
			grokBin, grokArgs, _ := newGrokCatalogFake(t, catalog, 0, false)
			cfg.GrokBuildBin = grokBin
			isolateGrokLifecycleHome(t, cfg)
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			old := dispatchNow
			dispatchNow = func() time.Time { return now }
			defer func() { dispatchNow = old; namedDispatchPreflightHook = nil; namedCodexPreparedHook = nil }()
			task := modeTask("development")
			task.Type = typeCoordinate
			task.Dir = t.TempDir()
			via, ok := ownerPrimaryDispatch(root, cfg, task, now)
			if !ok || via != "codex" {
				t.Fatal("priority not selected")
			}
			if err := saveTask(root, task); err != nil {
				t.Fatal(err)
			}
			expire := func() { now, _ = time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt) }
			switch boundary {
			case "tick-to-run":
				expire()
			case "after-preflight":
				namedDispatchPreflightHook = expire
			case "after-codex-prepare":
				namedCodexPreparedHook = expire
			case "early-return":
				cfg.DispatchMode = dispatchModeDaily
				if err := saveConfig(root, cfg); err != nil {
					t.Fatal(err)
				}
			}
			if err := runTaskVia(context.Background(), root, cfg, task, via); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(capture); !os.IsNotExist(err) {
				t.Fatal("stale Codex invoked")
			}
			got, err := loadTask(root, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == statusQueued {
				if got.LastRouteAttempt != nil || got.ActiveAttemptID != "" || got.Attempts != 0 {
					t.Fatalf("provisional identity retained %+v", got)
				}
				namedDispatchPreflightHook = nil
				namedCodexPreparedHook = nil
				if err := runTaskVia(context.Background(), root, cfg, got, got.PreferRunner); err != nil {
					t.Fatal(err)
				}
			}
			args, err := os.ReadFile(grokArgs)
			if err != nil || !strings.Contains(string(args), "grok-4.7") {
				t.Fatalf("daily provider not invoked: %s %v task=%+v", args, err, got)
			}
		})
	}
}

func TestDispatchModeActualQuotaSuccessorBoundary(t *testing.T) {
	for _, boundary := range []string{"queued-expiry", "queued-early-return", "failed-at-expiry", "changed-workspace"} {
		t.Run(boundary, func(t *testing.T) {
			cfg, now := modeFixture(t)
			cfg.MaxAttempts = 3
			root := testRoot(t)
			bin, capture := fakeNamedCodex(t, `{"type":"turn.failed","error":{"message":"quota exhausted"}}`, "", "", 1)
			cfg.CodexBin = bin
			catalog := filepath.Join(t.TempDir(), "catalog.txt")
			writeCatalog(t, catalog, grok47CatalogText("grok-4.7"))
			grok, grokArgs, _ := newGrokCatalogFake(t, catalog, 0, false)
			cfg.GrokBuildBin = grok
			isolateGrokLifecycleHome(t, cfg)
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			end, _ := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
			old := dispatchNow
			dispatchNow = func() time.Time {
				if boundary == "failed-at-expiry" {
					if _, err := os.Stat(capture); err == nil {
						return end
					}
				}
				return now
			}
			defer func() { dispatchNow = old }()
			task := modeTask("development")
			task.Type = typeCoordinate
			task.Dir = t.TempDir()
			refreshUnstartedDispatchMode(cfg, task, now)
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
			prior, _ := json.Marshal(got.LastRouteAttempt)
			if got.Status != statusQueued || got.Attempts != 1 {
				t.Fatalf("quota successor %+v", got)
			}
			if boundary == "queued-early-return" {
				cfg.DispatchMode = dispatchModeDaily
				if err := saveConfig(root, cfg); err != nil {
					t.Fatal(err)
				}
			} else {
				now = end
			}
			if boundary == "changed-workspace" {
				if err := os.WriteFile(filepath.Join(got.Dir, "changed"), []byte("new work"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if boundary != "changed-workspace" {
				refreshQueuedQuotaSuccessor(root, cfg, got, dispatchNow())
				if frozenDispatchMode(got) != dispatchModeDaily || got.PreferRunner != grokBuildRunnerName || got.Attempts != 1 {
					t.Fatalf("successor did not rebind: %+v", got)
				}
				after, _ := json.Marshal(got.LastRouteAttempt)
				if string(prior) != string(after) {
					t.Fatal("prior actual receipt changed")
				}
			}
			if err := runTaskVia(context.Background(), root, cfg, got, got.PreferRunner); err != nil {
				t.Fatal(err)
			}
			if boundary == "changed-workspace" {
				held, _ := loadTask(root, got.ID)
				after, _ := json.Marshal(held.LastRouteAttempt)
				if held.Status != statusHeld || string(prior) != string(after) || held.Attempts != 1 {
					t.Fatalf("unsafe rebind/receipt rewrite %+v", held)
				}
				if _, err := os.Stat(grokArgs); !os.IsNotExist(err) {
					t.Fatal("unproved daily provider started")
				}
			} else {
				if _, err := os.Stat(grokArgs); err != nil {
					t.Fatal("daily successor not invoked", err)
				}
			}
		})
	}
}

func TestDispatchModeActualCodexSuccessAndLateQuotaHold(t *testing.T) {
	for _, state := range []string{"success", "exhausted", "not-allowed"} {
		t.Run(state, func(t *testing.T) {
			cfg, now := modeFixture(t)
			root := testRoot(t)
			bin, capture := fakeNamedCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"PRIVATE_SENTINEL"}}`+"\n"+`{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":1}}`, "", "PUBLIC_FINAL", 0)
			cfg.CodexBin = bin
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			old := dispatchNow
			dispatchNow = func() time.Time { return now }
			defer func() { dispatchNow = old; namedCodexPreparedHook = nil }()
			task := modeTask("gpt-short")
			task.Effort, task.EffortExplicit = "max", true
			task.Dir = t.TempDir()
			refreshUnstartedDispatchMode(cfg, task, now)
			if state != "success" {
				namedCodexPreparedHook = func() {
					writeAllowanceFixture(t, cfg, now, func(m map[string]any) {
						if state == "exhausted" {
							m["used_percent"] = 100
						} else {
							m["ordinary_usage_allowed"] = false
						}
					})
				}
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
			if state == "success" {
				args, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != statusDone || !strings.Contains(string(args), "--json") || !strings.Contains(string(args), "gpt-5.6-sol") || !strings.Contains(string(args), "model_reasoning_effort=max") {
					t.Fatalf("success identity %s %+v", args, got)
				}
			} else {
				if _, err := os.Stat(capture); !os.IsNotExist(err) {
					t.Fatal("spent blocked quota")
				}
				if got.Status != statusHeld || got.KimiModel != "" || got.LastRouteAttempt != nil {
					t.Fatalf("late quota hold %+v", got)
				}
			}
			_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !info.IsDir() {
					b, _ := os.ReadFile(path)
					if strings.Contains(string(b), "PRIVATE_SENTINEL") {
						t.Errorf("raw Codex JSON persisted in %s", path)
					}
				}
				return nil
			})
		})
	}
}

func modeFixture(t *testing.T) (*Config, time.Time) {
	t.Helper()
	cfg := mixedTestConfig()
	cfg.DispatchMode = dispatchModeCodexPriority
	cfg.CodexAllowanceAccountID = "fixture-account"
	cfg.CodexPriorityStart = "2026-09-22T11:09:13Z"
	cfg.CodexPriorityExpiresAt = "2026-09-22T12:09:13Z"
	cfg.CodexAllowanceSnapshot = filepath.Join(t.TempDir(), "allowance.json")
	now, _ := time.Parse(time.RFC3339, "2026-09-22T11:26:18Z")
	writeAllowanceFixture(t, cfg, now, nil)
	return cfg, now
}

func writeAllowanceFixture(t *testing.T, cfg *Config, now time.Time, change func(map[string]any)) {
	t.Helper()
	m := map[string]any{"schema": "cardex-codex-allowance-v1", "account_id": cfg.CodexAllowanceAccountID, "source": "codex_app.get_usage_limits", "provider": "codex", "limit_id": "codex", "window_duration_mins": 10080, "used_percent": 95, "ordinary_usage_allowed": true, "secondary_present": false, "sampled_at": now.Format(time.RFC3339), "resets_at": now.Add(24 * time.Hour).Unix(), "authorized_start": cfg.CodexPriorityStart, "authorized_expires_at": cfg.CodexPriorityExpiresAt}
	if change != nil {
		change(m)
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(cfg.CodexAllowanceSnapshot, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func modeTask(class string) *Task {
	return &Task{ID: "mode-fixture", WorkClass: class, Type: typeSequence, Status: statusQueued, PreferRunner: "codex", RiskClass: riskClassOrdinary, RouteClass: routeClassGeneral, Prompts: []string{"p"}}
}

func TestDispatchModeExactClockAndDailyTable(t *testing.T) {
	cfg, _ := modeFixture(t)
	start, _ := time.Parse(time.RFC3339, cfg.CodexPriorityStart)
	end, _ := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
	for _, tc := range []struct {
		at   time.Time
		want string
	}{{start.Add(-time.Nanosecond), dispatchModeDaily}, {start, dispatchModeCodexPriority}, {end.Add(-time.Nanosecond), dispatchModeCodexPriority}, {end, dispatchModeDaily}, {end.Add(time.Hour), dispatchModeDaily}} {
		if got := effectiveDispatchMode(cfg, tc.at); got != tc.want {
			t.Fatalf("%s: %s", tc.at, got)
		}
		for _, class := range []string{"development", "simple-development", "gpt-complex", "gpt-short", "management"} {
			r, ok := resolveMixedOwnerRouteAt(cfg, modeTask(class), tc.at)
			if !ok {
				t.Fatal(class)
			}
			if tc.want == dispatchModeCodexPriority {
				want := "gpt-5.6-sol"
				if class == "development" || class == "gpt-complex" {
					want = "gpt-6-astra"
				}
				if r.Legs[0].Model != want || r.Legs[0].Runner != "codex" || r.Legs[1].Model != "kimi-code/k3" || r.Legs[1].Effort != "max" {
					t.Fatalf("priority %+v", r)
				}
			} else {
				want := map[string]string{"development": "grok-4.7", "simple-development": "grok-4.6", "gpt-complex": "gpt-6-astra", "gpt-short": "gpt-5.6-sol", "management": "gemini-3.8-flash-high"}[class]
				if r.Legs[0].Model != want {
					t.Fatalf("daily %+v", r)
				}
			}
		}
	}
	cfg.DispatchMode = dispatchModeDaily
	if effectiveDispatchMode(cfg, start.Add(time.Minute)) != dispatchModeDaily {
		t.Fatal("early switch ignored")
	}
}

func TestDispatchModeWeeklyAllowanceHonestAndFresh(t *testing.T) {
	cfg, now := modeFixture(t)
	for _, tc := range []struct {
		name    string
		change  func(map[string]any)
		allowed bool
	}{
		{"weekly95-no5h", nil, true},
		{"false", func(m map[string]any) { m["ordinary_usage_allowed"] = false }, false},
		{"exhausted", func(m map[string]any) { m["used_percent"] = 100 }, false},
		{"missing-percent", func(m map[string]any) { delete(m, "used_percent") }, false},
		{"future", func(m map[string]any) { m["sampled_at"] = now.Add(time.Second).Format(time.RFC3339) }, false},
		{"stale", func(m map[string]any) { m["sampled_at"] = now.Add(-15*time.Minute - time.Second).Format(time.RFC3339) }, false},
		{"wrong-provider", func(m map[string]any) { m["provider"] = "claude" }, false},
		{"luna", func(m map[string]any) { m["limit_id"] = "codex-luna" }, false},
		{"5h-fiction", func(m map[string]any) { m["window_duration_mins"] = 300 }, false},
		{"extended-authority", func(m map[string]any) { m["authorized_expires_at"] = now.Add(2 * time.Hour).Format(time.RFC3339) }, false},
		{"secondary-changed", func(m map[string]any) { m["secondary_present"] = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeAllowanceFixture(t, cfg, now, tc.change)
			e := currentAutomaticCodexBudgetEvidence(cfg, now)
			allowed, reason := automaticCodexBudgetAllowed(&Task{AutomaticCodex: true}, e, 65)
			if allowed != tc.allowed {
				t.Fatalf("allowed=%v: %s %+v", allowed, reason, e)
			}
			if allowed && (e.WindowMinutes != 10080 || !strings.Contains(reason, "weekly")) {
				t.Fatal("weekly mislabelled")
			}
		})
	}
	writeAllowanceFixture(t, cfg, now, nil)
	end, _ := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
	e := currentAutomaticCodexBudgetEvidence(cfg, end)
	if e.TemporaryAllowance {
		t.Fatal("allowance survived expiry")
	}
	if ok, _ := automaticCodexBudgetAllowed(&Task{AutomaticCodex: true}, e, 65); ok {
		t.Fatal("old missing-5h guard removed outside window")
	}
}

func TestDispatchModeQueuedRebindAndFrozenHistory(t *testing.T) {
	cfg, now := modeFixture(t)
	end, _ := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
	queued := modeTask("development")
	if !refreshUnstartedDispatchMode(cfg, queued, now) || queued.CodexModel != "gpt-6-astra" {
		t.Fatalf("priority %+v", queued)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Task)
	}{
		{"running", func(t *Task) { t.Status = statusRunning }}, {"held", func(t *Task) { t.Status = statusHeld }},
		{"explicit", func(t *Task) { t.RunnerExplicit = true }}, {"attempt", func(t *Task) { t.LastRouteAttempt = &RouteAttemptReadback{RequestedRunner: "codex"} }},
		{"goal-contract", func(t *Task) { t.Goal = &TaskGoalBinding{BudgetTokens: 40} }},
		{"old-history", func(t *Task) { t.WorkClass = "" }}, {"attempt-budget", func(t *Task) { t.Attempts = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *queued
			tc.mutate(&copy)
			before, _ := json.Marshal(copy)
			if refreshUnstartedDispatchMode(cfg, &copy, end) {
				t.Fatal("identity rebound")
			}
			after, _ := json.Marshal(copy)
			if string(before) != string(after) {
				t.Fatal("history mutated")
			}
		})
	}
	goal := modeTask("development")
	goal.Goal = &TaskGoalBinding{BudgetTokens: 40}
	if route, ok := resolveMixedOwnerRouteAt(cfg, goal, now); !ok || route.Name != "mixed_development" || route.Legs[0].Runner != grokBuildRunnerName {
		t.Fatalf("native Goal inherited mode override: %+v", route)
	}
	if !refreshUnstartedDispatchMode(cfg, queued, end) || queued.GrokModel != "grok-4.7" || queued.AutomaticCodex {
		t.Fatalf("expiry %+v", queued)
	}
	if err := closedOwnerTaskStateError(queued); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchModeQuotaTransitionPreservesAuthority(t *testing.T) {
	cfg, now := modeFixture(t)
	end, _ := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
	cfg.MaxAttempts = 3
	task := modeTask("development")
	refreshUnstartedDispatchMode(cfg, task, now)
	task.Status = statusRunning
	auth, err := authorizePolicyFallback(safeFixtureProof(t))
	if err != nil {
		t.Fatal(err)
	}
	old := dispatchNow
	dispatchNow = func() time.Time { return end }
	defer func() { dispatchNow = old }()
	expired := *task
	if err := queuePolicyFallback(cfg, &expired, fallbackQuota, auth); err != nil {
		t.Fatal(err)
	}
	if expired.PreferRunner != grokBuildRunnerName || expired.GrokModel != "grok-4.7" || expired.KimiModel != "" || expired.Attempts != 1 {
		t.Fatalf("expiry must resolve daily, not yesterday's Kimi: %+v", expired)
	}
	dispatchNow = func() time.Time { return now }
	for _, kind := range []fallbackFailureKind{fallbackTransport, fallbackStreamIncomplete, fallbackInvalidTerminal} {
		copy := *task
		before, _ := json.Marshal(copy)
		if err := queuePolicyFallback(cfg, &copy, kind, auth); err == nil {
			t.Fatal("unsafe transition")
		}
		after, _ := json.Marshal(copy)
		if string(before) != string(after) {
			t.Fatal("rejected transition mutated")
		}
	}
	noRetry := *task
	noRetry.MaxAttempts = 1
	if err := queuePolicyFallback(cfg, &noRetry, fallbackQuota, auth); err == nil {
		t.Fatal("no-retry bypass")
	}
	if err := queuePolicyFallback(cfg, task, fallbackQuota, auth); err != nil {
		t.Fatal(err)
	}
	if task.KimiModel != "kimi-code/k3" || task.Effort != "max" || task.Attempts != 1 || task.AutomaticCodex || task.AutomaticSolCalls != 0 {
		t.Fatalf("transition %+v", task)
	}
	if _, ok := resolveOwnerRouteReadback(cfg, task); !ok {
		t.Fatal("frozen Kimi readback")
	}
}

func TestDispatchModeCLIOnlyChangesConfiguration(t *testing.T) {
	cfg, now := modeFixture(t)
	root := testRoot(t)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	task := modeTask("development")
	task.Dir = t.TempDir()
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(taskPath(root, task.ID))
	old := dispatchNow
	dispatchNow = func() time.Time { return now }
	defer func() { dispatchNow = old }()
	if err := cmdDispatchMode([]string{"-root", root, "-set", "daily", "-task", task.ID}); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DispatchMode != dispatchModeDaily || loaded.CodexPriorityExpiresAt != cfg.CodexPriorityExpiresAt {
		t.Fatal("early switch moved authority")
	}
	command, err := captureStdout(t, func() error { return cmdCmd([]string{"-root", root, task.ID}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, " run -root "+shellQuote(root)+" "+shellQuote(task.ID)) || strings.Contains(command, "model_reasoning_effort") || strings.Contains(command, " hold ") {
		t.Fatalf("manual command bypasses checked dispatcher: %s", command)
	}
	after, _ := os.ReadFile(taskPath(root, task.ID))
	if string(before) != string(after) {
		t.Fatal("mode/cmd rewrote task")
	}
}

func TestDispatchModeFreshSnapshotAdmitsKimiWithoutCodex(t *testing.T) {
	cfg, now := modeFixture(t)
	cfg.MaxAttempts = 3
	cfg.KimiCLIHome = t.TempDir()
	root := testRoot(t)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	withSchedulerLock(t, root)
	end, _ := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
	base := modeTask("development")
	base.Dir = t.TempDir()
	base.MaxAttempts = 3
	if !refreshUnstartedDispatchMode(cfg, base, now) || base.PreferRunner != "codex" || base.CodexModel != "gpt-6-astra" {
		t.Fatalf("priority primary %+v", base)
	}
	if err := saveTask(root, base); err != nil {
		t.Fatal(err)
	}
	writeAllowanceFixture(t, cfg, now, func(m map[string]any) {
		m["used_percent"] = 100
		m["ordinary_usage_allowed"] = true
		m["note"] = "PRIVATE_SNAPSHOT_SENTINEL"
	})
	evidence := currentAutomaticCodexBudgetEvidence(cfg, now)
	if allowed, reason := automaticCodexBudgetAllowed(&Task{AutomaticCodex: true}, evidence, 65); allowed || !codexSnapshotExhausted(cfg, now) {
		t.Fatalf("100%% must deny Codex and admit the snapshot: allowed=%v %s %+v", allowed, reason, evidence)
	}
	outside := automaticCodexBudgetEvidence{Available: true, UsedPercent: 70, Source: "codex-usage-feed"}
	if allowed, _ := automaticCodexBudgetAllowed(&Task{AutomaticCodex: true}, outside, 65); allowed {
		t.Fatal("fresh five-hour 65% policy waived")
	}
	admitted := *base
	if !selectUnstartedCodexSnapshotSuccessor(root, cfg, &admitted, now) {
		t.Fatal("fresh 100% snapshot did not admit Kimi")
	}
	if admitted.PreferRunner != kimiCLIRunnerName || admitted.KimiModel != "kimi-code/k3" || admitted.Effort != "max" ||
		admitted.Attempts != 0 || admitted.MaxAttempts != 3 || admitted.AutomaticSolCalls != 0 || admitted.AutomaticSolInvocations != 0 ||
		admitted.AutomaticCodex || admitted.LastRouteAttempt != nil || admitted.CodexModel != "" || admitted.RouteReason != codexSnapshotExhaustedReason {
		t.Fatalf("snapshot admission rewrote budgets or invented an invocation %+v", admitted)
	}
	if route, ok := resolveOwnerRouteReadback(cfg, &admitted); !ok || admitted.OwnerRouteLeg != 2 || route.Legs[admitted.OwnerRouteLeg-1].Model != "kimi-code/k3" || route.Legs[admitted.OwnerRouteLeg-1].Effort != "max" {
		t.Fatalf("snapshot pin readback %+v ok=%v task=%+v", route, ok, admitted)
	}
	events, err := os.ReadFile(eventsPath(root, admitted.ID))
	if err != nil || !strings.Contains(string(events), `"reason":"quota-snapshot/budget-exhausted"`) || !strings.Contains(string(events), `"provider_started":false`) {
		t.Fatalf("snapshot record missing: %s %v", events, err)
	}
	if strings.Contains(string(events), "PRIVATE_SNAPSHOT_SENTINEL") || strings.Contains(string(events), "codex_quota_terminal") {
		t.Fatal("snapshot record stored raw provider material")
	}
	kept := admitted
	if refreshUnstartedDispatchMode(cfg, &kept, now) {
		t.Fatal("in-window snapshot pin was cleared")
	}
	rebound := admitted
	if !refreshUnstartedDispatchMode(cfg, &rebound, end) || rebound.PreferRunner != grokBuildRunnerName || rebound.GrokModel != "grok-4.7" ||
		rebound.Attempts != 0 || rebound.LastRouteAttempt != nil || rebound.MaxAttempts != 3 {
		t.Fatalf("expired unstarted snapshot pin did not return to daily %+v", rebound)
	}
	for _, tc := range []struct {
		name   string
		at     time.Time
		change func(map[string]any)
		mutate func(*Task)
		cfg    func(*Config)
	}{
		{"held", now, func(m map[string]any) { m["used_percent"] = 100 }, func(task *Task) { task.Status = statusHeld }, nil},
		{"running", now, func(m map[string]any) { m["used_percent"] = 100 }, func(task *Task) { task.Status = statusRunning }, nil},
		{"attempt", now, func(m map[string]any) { m["used_percent"] = 100 }, func(task *Task) { task.Attempts = 1 }, nil},
		{"sol", now, func(m map[string]any) { m["used_percent"] = 100 }, func(task *Task) { task.AutomaticSolCalls = 1 }, nil},
		{"goal", now, func(m map[string]any) { m["used_percent"] = 100 }, func(task *Task) { task.Goal = &TaskGoalBinding{BudgetTokens: 40} }, nil},
		{"explicit", now, func(m map[string]any) { m["used_percent"] = 100 }, func(task *Task) { task.RunnerExplicit = true }, nil},
		{"receipt", now, func(m map[string]any) { m["used_percent"] = 100 }, func(task *Task) { task.LastRouteAttempt = &RouteAttemptReadback{RequestedRunner: "codex"} }, nil},
		{"ordinary-false", now, func(m map[string]any) { m["ordinary_usage_allowed"] = false }, nil, nil},
		{"stale", now, func(m map[string]any) {
			m["used_percent"] = 100
			m["sampled_at"] = now.Add(-16 * time.Minute).Format(time.RFC3339)
		}, nil, nil},
		{"future", now, func(m map[string]any) {
			m["used_percent"] = 100
			m["sampled_at"] = now.Add(time.Second).Format(time.RFC3339)
		}, nil, nil},
		{"luna", now, func(m map[string]any) { m["used_percent"] = 100; m["limit_id"] = "codex-luna" }, nil, nil},
		{"wrong-provider", now, func(m map[string]any) { m["used_percent"] = 100; m["provider"] = "claude" }, nil, nil},
		{"five-hour", now, func(m map[string]any) { m["used_percent"] = 100; m["window_duration_mins"] = 300 }, nil, nil},
		{"text-not-bucket", now, func(m map[string]any) { m["note"] = "401 unauthorized; 429; quota exhausted" }, nil, nil},
		{"expired-window", end, func(m map[string]any) { m["used_percent"] = 100 }, nil, nil},
		{"kimi-unavailable", now, func(m map[string]any) { m["used_percent"] = 100 }, nil, func(cfg *Config) { cfg.KimiCLIBin = "" }},
		{"bad-json", now, func(m map[string]any) { m["used_percent"] = 100 }, nil, func(cfg *Config) {
			if err := os.WriteFile(cfg.CodexAllowanceSnapshot, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unreadable", now, func(m map[string]any) { m["used_percent"] = 100 }, nil, func(cfg *Config) {
			cfg.CodexAllowanceSnapshot = filepath.Join(t.TempDir(), "missing.json")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeAllowanceFixture(t, cfg, now, tc.change)
			copy := *base
			if tc.mutate != nil {
				tc.mutate(&copy)
			}
			local := cfg
			if tc.cfg != nil {
				cloned := *cfg
				tc.cfg(&cloned)
				local = &cloned
			}
			before, _ := json.Marshal(copy)
			if selectUnstartedCodexSnapshotSuccessor(root, local, &copy, tc.at) {
				t.Fatal("unauthorized snapshot selected Kimi")
			}
			after, _ := json.Marshal(copy)
			if string(before) != string(after) {
				t.Fatal("rejected snapshot mutated the task")
			}
		})
	}
	writeAllowanceFixture(t, cfg, now, func(m map[string]any) {
		m["used_percent"] = 100
		m["note"] = "PRIVATE_SNAPSHOT_SENTINEL"
	})
	codexBin, capture := fakeNamedCodex(t, `{"type":"turn.failed","error":{"message":"quota exhausted PRIVATE_SNAPSHOT_SENTINEL"}}`, "", "", 1)
	cfg.CodexBin = codexBin
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	live := *base
	live.ID = "mode-snapshot-live"
	live.Dir = t.TempDir()
	if err := saveTask(root, &live); err != nil {
		t.Fatal(err)
	}
	old := dispatchNow
	dispatchNow = func() time.Time { return now }
	defer func() { dispatchNow = old }()
	if err := runTaskVia(context.Background(), root, cfg, &live, "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("Codex was invoked to manufacture quota rejection")
	}
	got, err := loadTask(root, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PreferRunner != kimiCLIRunnerName || got.KimiModel != "kimi-code/k3" || got.Attempts != 0 || got.MaxAttempts != 3 || got.Status != statusHeld {
		t.Fatalf("live admission %+v", got)
	}
	if got.LastRouteAttempt == nil || got.LastRouteAttempt.RequestedRunner != kimiCLIRunnerName || got.LastRouteAttempt.FailureKind != string(providerAuthMissing) || got.LastRouteAttempt.RequestedModel != "kimi-code/k3" {
		t.Fatalf("preflight was not the Kimi auth hold: %+v", got.LastRouteAttempt)
	}
	raw, err := os.ReadFile(eventsPath(root, got.ID))
	if err != nil || !strings.Contains(string(raw), `"provider_started":false`) || strings.Contains(string(raw), "PRIVATE_SNAPSHOT_SENTINEL") || strings.Contains(string(raw), "codex_quota_terminal") {
		t.Fatalf("live record leaked or missed the snapshot admission: %s %v", raw, err)
	}
}

func TestDispatchModeExhaustedSnapshotDoesNotOutliveWindow(t *testing.T) {
	cfg, now := modeFixture(t)
	end, err := time.Parse(time.RFC3339, cfg.CodexPriorityExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	root := testRoot(t)
	codexBin, codexCapture := fakeNamedCodex(t, `{"type":"turn.completed","usage":{}}`, "", "CODEX_OK", 0)
	cfg.CodexBin = codexBin
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText("grok-4.7"))
	grokBin, grokArgs, _ := newGrokCatalogFake(t, catalog, 0, false)
	cfg.GrokBuildBin = grokBin
	isolateGrokLifecycleHome(t, cfg)
	kimiBin, kimiArgs, _ := fakeKimiCLI(t, `{"role":"assistant","content":"KIMI_SHOULD_NOT_START"}`, 0)
	cfg.KimiCLIBin = kimiBin
	cfg.KimiCLIHome = t.TempDir()
	if err := os.Mkdir(filepath.Join(cfg.KimiCLIHome, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAllowanceFixture(t, cfg, now, func(m map[string]any) {
		m["used_percent"] = 100
		m["note"] = "PRIVATE_SNAPSHOT_SENTINEL"
	})
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	withSchedulerLock(t, root)
	old := dispatchNow
	dispatchNow = func() time.Time { return end }
	defer func() { dispatchNow = old }()
	for _, tc := range []struct {
		name string
		pin  func(*Task)
	}{
		{"primary", func(task *Task) {}},
		{"admitted", func(task *Task) {
			if !selectUnstartedCodexSnapshotSuccessor(root, cfg, task, now) {
				t.Fatal("admission")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(grokArgs)
			_ = os.Remove(codexCapture)
			_ = os.Remove(kimiArgs)
			task := modeTask("development")
			task.ID = "mode-expiry-" + tc.name
			task.Type = typeCoordinate
			task.Dir = t.TempDir()
			if !refreshUnstartedDispatchMode(cfg, task, now) {
				t.Fatal("pin")
			}
			if err := saveTask(root, task); err != nil {
				t.Fatal(err)
			}
			tc.pin(task)
			if err := runTaskVia(context.Background(), root, cfg, task, "codex"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(codexCapture); !os.IsNotExist(err) {
				t.Fatal("expired window invoked Codex")
			}
			if _, err := os.Stat(kimiArgs); !os.IsNotExist(err) {
				t.Fatal("expired window invoked Kimi")
			}
			args, err := os.ReadFile(grokArgs)
			if err != nil || !strings.Contains(string(args), "grok-4.7") {
				t.Fatalf("daily successor missing: %s %v", args, err)
			}
			got, err := loadTask(root, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.KimiModel != "" || got.PreferRunner != grokBuildRunnerName {
				t.Fatalf("expired task stayed on the snapshot successor %+v", got)
			}
			_ = os.Remove(grokArgs)
		})
	}
}

func TestDispatchModeCodexJSONQuotaProof(t *testing.T) {
	start := "{\"type\":\"thread.started\",\"thread_id\":\"fixture\"}\n{\"type\":\"turn.started\"}\n"
	fail := "{\"type\":\"turn.failed\",\"error\":{\"message\":\"quota exhausted\"}}\n"
	for _, tc := range []struct {
		name, body, stderr string
		want               bool
	}{
		{"closed-error", start + fail, "", true},
		{"plain-text", "quota exhausted", "", false},
		{"missing-terminal", start + `{"type":"error","message":"quota exhausted"}`, "", false},
		{"partial", start + `{"type":"turn.failed"`, "", false},
		{"unknown", start + `{"type":"unknown"}` + "\n" + fail, "", false},
		{"stderr", start + fail, "unframed diagnostic", false},
		{"model-work", start + `{"type":"item.completed","item":{"type":"agent_message","text":"quota exhausted"}}` + "\n" + fail, "", false},
		{"tool-work", start + `{"type":"item.started","item":{"type":"command_execution"}}` + "\n" + fail, "", false},
		{"after-terminal", start + fail + `{"type":"error","message":"quota exhausted"}`, "", false},
		{"auth", start + strings.ReplaceAll(fail, "quota exhausted", "401 unauthorized; quota exhausted"), "", false},
		{"refusal", start + strings.ReplaceAll(fail, "quota exhausted", "refusal; quota exhausted"), "", false},
		{"429", start + strings.ReplaceAll(fail, "quota exhausted", "429 too many requests"), "", false},
		{"network", start + strings.ReplaceAll(fail, "quota exhausted", "connection reset; quota exhausted"), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &claudeResult{Type: "result", IsError: true}
			observeNamedCodexJSON(res, tc.body, tc.stderr)
			task := modeTask("development")
			task.OwnerRouteName = "mixed_priority_development"
			_, classified := classifyTaskPolicyFallbackFailure(task, "codex", res, tc.body, nil)
			proof := safeFixtureProof(t)
			proof.ObservationComplete = res.ObservationComplete
			proof.SemanticEvents = res.SemanticEvents
			proof.ModelEvents = res.ModelEvents
			proof.ToolEvents = res.ToolEvents
			_, err := authorizePolicyFallback(proof)
			if (classified && err == nil) != tc.want {
				t.Fatalf("classified=%v proof=%v res=%+v", classified, err, res)
			}
		})
	}
}

func TestDispatchModeObserverDoesNotRetainPrivateDiagnostic(t *testing.T) {
	for _, body := range []string{
		`{"type":"turn.failed","error":{"message":"quota exhausted PRIVATE_SENTINEL"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"PRIVATE_SENTINEL"}}` + "\n" + `{"type":"turn.failed","error":{"message":"PRIVATE_SENTINEL"}}`,
		`{"unknown":"PRIVATE_SENTINEL"}`,
	} {
		res := &claudeResult{Type: "result", IsError: true, Result: "PRIVATE_SENTINEL"}
		observeNamedCodexJSON(res, body, "")
		b, _ := json.Marshal(res)
		if strings.Contains(string(b), "PRIVATE_SENTINEL") {
			t.Fatalf("private parser data retained: %s", b)
		}
	}
}

// Exercises the same consumer used by locked manual and scheduled dispatch.
// These are offline provider fixtures, not live quota or Kimi capability claims.
func TestDispatchModeDailySnapshotActualAdmission(t *testing.T) {
	for _, class := range []string{"gpt-complex", "gpt-short"} {
		for _, boundary := range []string{"fresh", "stale-before-run", "stale-after-preflight", "recovered-after-preflight", "wrong-account", "held-unknown"} {
			t.Run(class+"/"+boundary, func(t *testing.T) {
				cfg, now := modeFixture(t)
				cfg.DispatchMode = dispatchModeDaily
				// A daily snapshot is independent of yesterday's expired override.
				now = now.Add(24 * time.Hour)
				cfg.MaxAttempts = 1
				root := testRoot(t)
				cfg.CodexBin, _ = fakeNamedCodex(t, `{"type":"turn.completed","usage":{}}`, "", "MUST_NOT_INVOKE_CODEX", 0)
				kimi, args, _ := fakeKimiCLI(t, `{"role":"meta","type":"system.version","version":"0.42.0"}`+"\n"+`{"role":"assistant","content":"DAILY_KIMI_OK"}`+"\n"+`{"role":"meta","type":"session.resume_hint","session_id":"daily-fixture"}`, 0)
				kimiConfig := kimiCLITestConfig(t, kimi)
				cfg.KimiCLIBin, cfg.KimiCLIHome = kimiConfig.KimiCLIBin, kimiConfig.KimiCLIHome
				writeAllowanceFixture(t, cfg, now, func(m map[string]any) {
					m["used_percent"] = 100
					if boundary == "stale-before-run" {
						m["sampled_at"] = now.Add(-16 * time.Minute).Format(time.RFC3339)
					}
					if boundary == "wrong-account" {
						m["account_id"] = "other-account"
					}
				})
				if err := saveConfig(root, cfg); err != nil {
					t.Fatal(err)
				}
				old := dispatchNow
				dispatchNow = func() time.Time { return now }
				defer func() { dispatchNow = old; namedDispatchPreflightHook = nil }()
				task := modeTask(class)
				task.Dir = t.TempDir()
				task.Type = typeCoordinate
				task.MaxAttempts = 1
				via, ok := ownerPrimaryDispatch(root, cfg, task, now)
				if !ok || via != "codex" {
					t.Fatalf("primary %q %v", via, ok)
				}
				if boundary == "held-unknown" {
					task.Status = statusHeld
					task.LastRouteAttempt = &RouteAttemptReadback{FailureKind: "invalid_terminal_result", ModelEvents: 1}
					if codexSnapshotSuccessorEligible(root, cfg, task, now) {
						t.Fatal("unknown outcome admitted")
					}
					return
				}
				if err := saveTask(root, task); err != nil {
					t.Fatal(err)
				}
				switch boundary {
				case "stale-after-preflight":
					namedDispatchPreflightHook = func() { now = now.Add(16 * time.Minute) }
				case "recovered-after-preflight":
					namedDispatchPreflightHook = func() { writeAllowanceFixture(t, cfg, now, func(m map[string]any) { m["used_percent"] = 1 }) }
				}
				var runErr error
				if class == "gpt-short" && boundary == "fresh" {
					runErr = tickFilter(root, cfg, false, true, task.ID)
				} else {
					runErr = runTaskVia(context.Background(), root, cfg, task, via)
				}
				if runErr != nil {
					t.Fatal(runErr)
				}
				got, err := loadTask(root, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				_, invoked := os.Stat(args)
				if boundary == "fresh" {
					if invoked != nil || got.Status != statusDone || got.LastRouteAttempt == nil || got.LastRouteAttempt.RequestedModel != "kimi-code/k3" || got.RouteReason != codexSnapshotExhaustedReason || got.MaxAttempts != 1 {
						t.Fatalf("daily snapshot actual Kimi path: %+v args=%v", got, invoked)
					}
				} else if !os.IsNotExist(invoked) || got.Status != statusHeld || got.Attempts != 0 {
					t.Fatalf("invalid evidence invoked alternate or consumed attempt: %+v args=%v", got, invoked)
				}
			})
		}
	}
}

func TestDispatchModeAssemblyDoesNotImplicitlySelectGPT(t *testing.T) {
	cfg, now := modeFixture(t)
	cfg.DispatchMode = dispatchModeDaily
	task := modeTask(defaultMixedWorkClass(typeAssembly))
	task.Type = typeAssembly
	route, ok := resolveMixedOwnerRouteAt(cfg, task, now)
	if !ok || route.Legs[0].Runner != grokBuildRunnerName || route.Legs[0].Model != "grok-4.7" {
		t.Fatalf("implicit GPT selection: %+v", route)
	}
	task.WorkClass = "gpt-complex"
	route, ok = resolveMixedOwnerRouteAt(cfg, task, now)
	if !ok || route.Legs[0].Runner != "codex" || route.Legs[0].Model != "gpt-6-astra" {
		t.Fatalf("explicit GPT class lost: %+v", route)
	}
}
