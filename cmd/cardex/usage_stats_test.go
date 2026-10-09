package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseOptionalJSONRejectsNegativeFractionOutOfRangeNull(t *testing.T) {
	t.Parallel()
	zero, ok := parseOptionalJSONInt64(json.RawMessage("0"))
	if !ok || zero == nil || *zero != 0 {
		t.Fatalf("known zero int: %v ok=%v", zero, ok)
	}
	if n, ok := parseOptionalJSONInt64(json.RawMessage("-1")); ok || n != nil {
		t.Fatalf("negative int must be unavailable: %v ok=%v", n, ok)
	}
	if n, ok := parseOptionalJSONInt64(json.RawMessage("1.5")); ok || n != nil {
		t.Fatalf("fraction must be unavailable: %v ok=%v", n, ok)
	}
	if n, ok := parseOptionalJSONInt64(json.RawMessage("null")); ok || n != nil {
		t.Fatalf("null must be unavailable: %v ok=%v", n, ok)
	}
	if n, ok := parseOptionalJSONInt64(json.RawMessage("")); ok || n != nil {
		t.Fatalf("empty must be unavailable: %v ok=%v", n, ok)
	}
	if n, ok := parseOptionalJSONInt64(json.RawMessage("1e20")); ok || n != nil {
		t.Fatalf("out-of-range must be unavailable: %v ok=%v", n, ok)
	}
	if n, ok := parseOptionalJSONInt64(json.RawMessage(`"nope"`)); ok || n != nil {
		t.Fatalf("string must be unavailable: %v ok=%v", n, ok)
	}
	fz, ok := parseOptionalJSONFloat64(json.RawMessage("0"))
	if !ok || fz == nil || *fz != 0 {
		t.Fatalf("known zero float: %v ok=%v", fz, ok)
	}
	if f, ok := parseOptionalJSONFloat64(json.RawMessage("-0.01")); ok || f != nil {
		t.Fatalf("negative cost must be unavailable: %v ok=%v", f, ok)
	}
	if f, ok := parseOptionalJSONFloat64(json.RawMessage("null")); ok || f != nil {
		t.Fatalf("null float must be unavailable: %v ok=%v", f, ok)
	}
	if f, ok := parseOptionalJSONFloat64(json.RawMessage("NaN")); ok || f != nil {
		t.Fatalf("NaN must be unavailable: %v ok=%v", f, ok)
	}
	neg := usageFieldsFromJSON(json.RawMessage(`{"input_tokens":-3,"output_tokens":4}`), claudeUsageAliases)
	if neg.InputTokens != nil {
		t.Fatalf("negative input must stay missing: %+v", neg)
	}
	if neg.OutputTokens == nil || *neg.OutputTokens != 4 {
		t.Fatalf("valid output kept: %+v", neg)
	}
	tk := &Task{ID: "t", ActiveAttemptID: "a"}
	obs := observationFromProviderResult("", tk, &claudeResult{
		CostPresent: true, TotalCostUSD: -1.25,
		TurnsPresent: true, NumTurns: -2,
		DurationPresent: true, DurationMS: -9,
		UsageSource: usageSrcClaudeResult,
	})
	if obs.CostUSD != nil || obs.Turns != nil || (obs.Duration != nil && obs.Duration.ElapsedMS != nil) {
		t.Fatalf("negative provider cost/turns/duration must be unavailable: %+v", obs)
	}
}

func TestApplyUsageObservationKnownZeroVsMissing(t *testing.T) {
	t.Parallel()
	obs := usageObservation{
		Identity:          "per_call|claude|t1|a1|1|s1|provider_call",
		Source:            usageSrcClaudeResult,
		ObservationWindow: usageWindowProviderCall,
		Scope:             usageScopePerCall,
		Fields: usageFieldSet{
			InputTokens:  int64Ptr(0),
			OutputTokens: int64Ptr(12),
		},
		CostUSD: float64Ptr(0.5),
		Turns:   int64Ptr(0),
	}
	st := applyUsageObservation(nil, obs)
	if st.Accumulated.InputTokens == nil || *st.Accumulated.InputTokens != 0 {
		t.Fatalf("known zero input must persist: %+v", st.Accumulated)
	}
	if st.Accumulated.OutputTokens == nil || *st.Accumulated.OutputTokens != 12 {
		t.Fatalf("present output: %+v", st.Accumulated)
	}
	if st.Accumulated.CacheReadInputTokens != nil || st.Accumulated.ReasoningTokens != nil || st.Accumulated.TotalTokens != nil {
		t.Fatalf("omitted fields must stay missing: %+v", st.Accumulated)
	}
	if !containsString(st.Last.Unavailable, rawFieldCacheRead) || !containsString(st.Last.Unavailable, rawFieldReasoning) {
		t.Fatalf("unsupported fields must be listed unavailable: %v", st.Last.Unavailable)
	}
	if st.AccumulatedCostUSD == nil || *st.AccumulatedCostUSD != 0.5 {
		t.Fatalf("cost: %v", st.AccumulatedCostUSD)
	}
	if st.AccumulatedTurns == nil || *st.AccumulatedTurns != 0 {
		t.Fatalf("known zero turns: %v", st.AccumulatedTurns)
	}

	raw, err := json.Marshal(st.Accumulated)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"input_tokens":0`) {
		t.Fatalf("known zero must appear in JSON: %s", s)
	}
	if strings.Contains(s, "cache_read_input_tokens") || strings.Contains(s, "reasoning_tokens") {
		t.Fatalf("omitted fields must not appear: %s", s)
	}
}

func TestApplyUsageObservationDuplicateCallDoesNotDoubleCount(t *testing.T) {
	t.Parallel()
	obs := usageObservation{
		Identity:          "per_call|claude|t1|a1|1||provider_call",
		Source:            usageSrcClaudeResult,
		ObservationWindow: usageWindowProviderCall,
		Scope:             usageScopePerCall,
		Fields:            usageFieldSet{OutputTokens: int64Ptr(4)},
		CostUSD:           float64Ptr(0.01),
		Turns:             int64Ptr(1),
	}
	st := applyUsageObservation(nil, obs)
	st = applyUsageObservation(st, obs)
	if st.Accumulated.OutputTokens == nil || *st.Accumulated.OutputTokens != 4 {
		t.Fatalf("duplicate call charged twice: %+v", st.Accumulated)
	}
	if st.Last == nil || st.Last.AppliedDelta == nil || !fieldSetEmpty(*st.Last.AppliedDelta) {
		t.Fatalf("repeat must record zero delta: %+v", st.Last)
	}
}

func TestApplyUsageObservationStepsAndRetriesAttributable(t *testing.T) {
	t.Parallel()
	step1 := usageObservation{
		Identity: "per_call|claude|t1|att-1|1|s|provider_call", Scope: usageScopePerCall,
		ObservationWindow: usageWindowProviderCall, Fields: usageFieldSet{OutputTokens: int64Ptr(3)}, Turns: int64Ptr(1),
	}
	retry := usageObservation{
		Identity: "per_call|claude|t1|att-2|1|s|provider_call", Scope: usageScopePerCall,
		ObservationWindow: usageWindowProviderCall, Fields: usageFieldSet{OutputTokens: int64Ptr(5)}, Turns: int64Ptr(1),
	}
	step2 := usageObservation{
		Identity: "per_call|claude|t1|att-2|2|s|provider_call", Scope: usageScopePerCall,
		ObservationWindow: usageWindowProviderCall, Fields: usageFieldSet{OutputTokens: int64Ptr(7)}, Turns: int64Ptr(2),
	}
	st := applyUsageObservation(nil, step1)
	st = applyUsageObservation(st, retry)
	st = applyUsageObservation(st, step2)
	if st.Accumulated.OutputTokens == nil || *st.Accumulated.OutputTokens != 15 {
		t.Fatalf("steps/retries must add: got %+v", st.Accumulated)
	}
	if st.AccumulatedTurns == nil || *st.AccumulatedTurns != 4 {
		t.Fatalf("turns: %v", st.AccumulatedTurns)
	}
}

func TestApplyUsageObservationCumulativeDeltaAndRejects(t *testing.T) {
	t.Parallel()
	id := cumulativeIdentity(usageSrcGrokNativeGoalState, "sess", "goal-a", usageWindowNativeGoal)
	first := usageObservation{
		Identity: id, Source: usageSrcGrokNativeGoalState, Scope: usageScopeCumulative,
		ObservationWindow: usageWindowNativeGoal, Fields: usageFieldSet{TotalTokens: int64Ptr(100)},
		Duration: &usageDuration{ElapsedMS: int64Ptr(500), Source: durationSrcNativeElapsedMS},
	}
	st := applyUsageObservation(nil, first)
	if st.Accumulated.TotalTokens == nil || *st.Accumulated.TotalTokens != 100 {
		t.Fatalf("first high-water: %+v", st.Accumulated)
	}
	st = applyUsageObservation(st, first)
	if *st.Accumulated.TotalTokens != 100 {
		t.Fatalf("duplicate sync double-counted: %+v", st.Accumulated)
	}
	inc := first
	inc.Fields.TotalTokens = int64Ptr(150)
	inc.Duration = &usageDuration{ElapsedMS: int64Ptr(500), Source: durationSrcNativeElapsedMS}
	st = applyUsageObservation(st, inc)
	if *st.Accumulated.TotalTokens != 150 {
		t.Fatalf("true delta not applied: %+v", st.Accumulated)
	}
	if st.Last.AppliedDelta == nil || st.Last.AppliedDelta.TotalTokens == nil || *st.Last.AppliedDelta.TotalTokens != 50 {
		t.Fatalf("delta want 50: %+v", st.Last.AppliedDelta)
	}
	if st.AccumulatedDurationMS == nil || *st.AccumulatedDurationMS != 500 {
		t.Fatalf("lagged elapsed_ms must be preserved, not summed: %v", st.AccumulatedDurationMS)
	}

	reg := inc
	reg.Fields.TotalTokens = int64Ptr(80)
	st = applyUsageObservation(st, reg)
	if st.Last == nil || st.Last.Rejected != usageRejectRegressOrReset {
		t.Fatalf("regress must reject: %+v", st.Last)
	}
	if *st.Accumulated.TotalTokens != 150 {
		t.Fatalf("regress mutated accumulated: %+v", st.Accumulated)
	}

	foreign := inc
	foreign.Identity = cumulativeIdentity(usageSrcGrokNativeGoalState, "sess", "goal-other", usageWindowNativeGoal)
	st = applyUsageObservation(st, foreign)
	if st.Last == nil || st.Last.Rejected != usageRejectForeignIdentity {
		t.Fatalf("foreign identity must reject: %+v", st.Last)
	}
	if *st.Accumulated.TotalTokens != 150 {
		t.Fatalf("foreign identity summed: %+v", st.Accumulated)
	}
}

func TestApplyUsageObservationCompleteVsFollowupWindows(t *testing.T) {
	t.Parallel()
	tk := &Task{ID: "t-goal", SessionID: "sess", Status: statusRunning, Goal: &TaskGoalBinding{NativeGoalID: "goal-a"}}
	obs := nativeGoalObservation{SessionID: "sess", GoalID: "goal-a", NativeStatus: "complete", HighWaterTokens: int64Ptr(1000), ElapsedMS: int64Ptr(8000)}
	if !applyNativeGoalUsage(tk, obs) {
		t.Fatal("first complete high-water should apply")
	}
	if tk.RawUsage.BoundWindow != usageWindowNativeGoalComplete {
		t.Fatalf("window=%s", tk.RawUsage.BoundWindow)
	}
	if *tk.RawUsage.Accumulated.TotalTokens != 1000 {
		t.Fatalf("complete accumulated=%v", tk.RawUsage.Accumulated.TotalTokens)
	}
	tk.Status = statusDone
	tk.Goal.Observation = goalObsDone
	if applyNativeGoalUsage(tk, obs) {
		t.Fatal("repeat complete snapshot must not change totals")
	}
	if tk.RawUsage.BoundWindow != usageWindowNativeGoalComplete {
		t.Fatalf("repeat complete must stay complete window, got %s", tk.RawUsage.BoundWindow)
	}
	follow := obs
	follow.HighWaterTokens = int64Ptr(1100)
	if !applyNativeGoalUsage(tk, follow) {
		t.Fatal("post-complete increase should apply as followup")
	}
	if tk.RawUsage.BoundWindow != usageWindowNativePostCompleteFollowup {
		t.Fatalf("followup window=%s", tk.RawUsage.BoundWindow)
	}
	if *tk.RawUsage.Accumulated.TotalTokens != 1100 {
		t.Fatalf("followup summed full snapshot: %v", tk.RawUsage.Accumulated.TotalTokens)
	}
	if tk.RawUsage.Last.AppliedDelta == nil || *tk.RawUsage.Last.AppliedDelta.TotalTokens != 100 {
		t.Fatalf("followup delta want 100: %+v", tk.RawUsage.Last.AppliedDelta)
	}
}

func TestApplyNativeGoalUsageElapsedOnlyAndKnownZeroAreChanges(t *testing.T) {
	t.Parallel()
	tk := &Task{ID: "t-goal", SessionID: "sess", Status: statusRunning, Goal: &TaskGoalBinding{NativeGoalID: "goal-a"}}
	first := nativeGoalObservation{SessionID: "sess", GoalID: "goal-a", NativeStatus: "complete", HighWaterTokens: int64Ptr(1000), ElapsedMS: int64Ptr(8000)}
	if !applyNativeGoalUsage(tk, first) {
		t.Fatal("first complete observation should apply")
	}
	if tk.RawUsage.BoundWindow != usageWindowNativeGoalComplete {
		t.Fatalf("window=%s", tk.RawUsage.BoundWindow)
	}
	tk.Status = statusDone
	tk.Goal.Observation = goalObsDone
	elapsedOnly := first
	elapsedOnly.ElapsedMS = int64Ptr(9000)
	if !applyNativeGoalUsage(tk, elapsedOnly) {
		t.Fatal("elapsed-only increment must count as a change")
	}
	if tk.RawUsage.BoundWindow != usageWindowNativeGoalComplete {
		t.Fatalf("elapsed-only must stay complete window, got %s", tk.RawUsage.BoundWindow)
	}
	if tk.RawUsage.LastAppliedElapsed == nil || *tk.RawUsage.LastAppliedElapsed != 9000 {
		t.Fatalf("elapsed: %v", tk.RawUsage.LastAppliedElapsed)
	}
	if tk.RawUsage.Accumulated.TotalTokens == nil || *tk.RawUsage.Accumulated.TotalTokens != 1000 {
		t.Fatalf("high-water mutated on elapsed-only: %+v", tk.RawUsage.Accumulated)
	}

	zeroCard := &Task{ID: "t-zero", SessionID: "sess", Status: statusRunning, Goal: &TaskGoalBinding{NativeGoalID: "goal-z"}}
	base := nativeGoalObservation{SessionID: "sess", GoalID: "goal-z", NativeStatus: "complete", ElapsedMS: int64Ptr(100)}
	if !applyNativeGoalUsage(zeroCard, base) {
		t.Fatal("elapsed-only first observation should apply")
	}
	if zeroCard.RawUsage.LastApplied.TotalTokens != nil {
		t.Fatalf("total must stay missing until observed: %+v", zeroCard.RawUsage.LastApplied)
	}
	withZero := base
	withZero.HighWaterTokens = int64Ptr(0)
	if !applyNativeGoalUsage(zeroCard, withZero) {
		t.Fatal("newly observed known-zero high-water must count as a change")
	}
	if zeroCard.RawUsage.LastApplied.TotalTokens == nil || *zeroCard.RawUsage.LastApplied.TotalTokens != 0 {
		t.Fatalf("known-zero total: %+v", zeroCard.RawUsage.LastApplied)
	}
}

func TestApplyUsageObservationDurationLabels(t *testing.T) {
	t.Parallel()
	provider := usageObservation{
		Identity: "per_call|claude|t|a|1||provider_call", Scope: usageScopePerCall,
		ObservationWindow: usageWindowProviderCall,
		Duration:          &usageDuration{ElapsedMS: int64Ptr(321), Source: durationSrcProviderDurationMS},
	}
	st := applyUsageObservation(nil, provider)
	if st.Duration == nil || st.Duration.Source != durationSrcProviderDurationMS || st.Duration.IncludesWaits {
		t.Fatalf("provider duration: %+v", st.Duration)
	}
	wall := usageObservation{
		Identity: "per_call|claude|t|a|2||provider_call", Scope: usageScopePerCall,
		ObservationWindow: usageWindowProviderCall,
		Duration: &usageDuration{
			ElapsedMS: int64Ptr(4000), Source: durationSrcAttemptActiveWall,
			IncludesWaits: true, Label: durationLabelIncludesWaits,
		},
	}
	st = applyUsageObservation(st, wall)
	if st.Duration == nil || st.Duration.Source != durationSrcAttemptActiveWall || !st.Duration.IncludesWaits {
		t.Fatalf("wall duration label: %+v", st.Duration)
	}
	if !strings.Contains(st.Duration.Label, "not model compute") {
		t.Fatalf("label: %q", st.Duration.Label)
	}
}

func TestLegacyTaskJSONOmitsRawUsage(t *testing.T) {
	t.Parallel()
	tk := &Task{ID: "legacy", TurnsUsed: 0, CostUSD: 0}
	raw, err := json.Marshal(tk)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "raw_usage") {
		t.Fatalf("legacy card must omit raw_usage: %s", raw)
	}
	d := withCostTelemetry(nil, tk)
	if unavail, _ := d[evDetailCostUnavailable].(bool); !unavail {
		t.Fatalf("legacy unknown: %+v", d)
	}
}

func TestObservationFromProviderResultWrongTierNeverUsesGenericTaskModel(t *testing.T) {
	t.Parallel()
	genericOpus := &Task{
		ID: "t-grok", Model: "opus", PreferRunner: grokBuildRunnerName, GrokModel: "opus-as-task-field",
		Runner: grokBuildRunnerName,
	}
	reported := observationFromProviderResult("", genericOpus, &claudeResult{
		UsageSource:            usageSrcGrokBuildEnd,
		ObservedAssistantModel: "grok-4.6",
	})
	if reported.Model != "grok-4.6" || reported.ModelSource != usageModelSrcProviderReported {
		t.Fatalf("provider-reported must win over generic opus: %+v", reported)
	}

	bound := *genericOpus
	bound.LastRouteAttempt = &RouteAttemptReadback{
		RequestedRunner: grokBuildRunnerName, RequestedModel: "grok-4.6",
		ActualRunner: grokBuildRunnerName, ActualModel: "grok-4.6",
	}
	configured := observationFromProviderResult("", &bound, &claudeResult{UsageSource: usageSrcGrokBuildEnd})
	if configured.Model != "grok-4.6" || configured.ModelSource != usageModelSrcConfiguration {
		t.Fatalf("proven resolver binding: %+v", configured)
	}

	unknown := observationFromProviderResult("", genericOpus, &claudeResult{UsageSource: usageSrcGrokBuildEnd})
	if unknown.Model != "" || unknown.ModelSource != "" {
		t.Fatalf("unproven Task.Model must not be labeled actual: %+v", unknown)
	}

	requestOnly := *genericOpus
	requestOnly.LastRouteAttempt = &RouteAttemptReadback{RequestedModel: "grok-4.6"}
	if obs := observationFromProviderResult("", &requestOnly, &claudeResult{UsageSource: usageSrcGrokBuildEnd}); obs.Model != "" {
		t.Fatalf("request-only readback: %+v", obs)
	}
	emptyActual := *genericOpus
	emptyActual.LastRouteAttempt = &RouteAttemptReadback{RequestedModel: "grok-4.6", ActualRunner: grokBuildRunnerName}
	if obs := observationFromProviderResult("", &emptyActual, &claudeResult{UsageSource: usageSrcGrokBuildEnd}); obs.Model != "" {
		t.Fatalf("empty ActualModel: %+v", obs)
	}
	foreign := *genericOpus
	foreign.LastRouteAttempt = &RouteAttemptReadback{
		RequestedRunner: "claude", RequestedModel: "opus",
		ActualRunner: "claude", ActualModel: "opus",
	}
	if obs := observationFromProviderResult("", &foreign, &claudeResult{UsageSource: usageSrcGrokBuildEnd}); obs.Model != "" {
		t.Fatalf("foreign lane readback: %+v", obs)
	}
	staleCursor := *genericOpus
	staleCursor.LastRouteAttempt = &RouteAttemptReadback{
		RequestedRunner: grokBuildRunnerName, RequestedModel: "grok-4.6",
		ActualRunner: grokBuildRunnerName, ActualModel: "grok-4.6",
	}
	if obs := observationFromProviderResult("", &staleCursor, &claudeResult{UsageSource: usageSrcCursorResult}); obs.Model != "" {
		t.Fatalf("stale lane vs cursor result: %+v", obs)
	}

	native := observationFromNativeGoal(&Task{
		ID: "t-goal", SessionID: "sess", GrokModel: "grok-4.6",
		Goal: &TaskGoalBinding{NativeGoalID: "goal-a"},
	}, nativeGoalObservation{SessionID: "sess", GoalID: "goal-a", NativeStatus: "running"})
	if native.Model != "grok-4.6" {
		t.Fatalf("native Goal attribution drifted: %+v", native)
	}
}

func TestMissingUsageDoesNotCreateCompletionGate(t *testing.T) {
	t.Parallel()
	complete := nativeGoalObservation{
		SessionID: "sess", GoalID: "goal-a", NativeStatus: "complete",
		UpdatesOK: true, Classifier: "achieved", FinalStatus: "complete", FinalClassifier: "achieved",
	}
	withTokens := complete
	withTokens.HighWaterTokens = int64Ptr(9)
	without := mapNativeGoalToTask(complete, true, false, false)
	with := mapNativeGoalToTask(withTokens, true, false, false)
	if !without.AcceptDone || !with.AcceptDone {
		t.Fatalf("high-water presence must not change done mapping: without=%+v with=%+v", without, with)
	}
	tk := &Task{ID: "t", SessionID: "sess", Goal: &TaskGoalBinding{NativeGoalID: "goal-a"}}
	applyNativeGoalUsage(tk, complete)
	if tk.RawUsage == nil || tk.RawUsage.Last == nil {
		t.Fatal("missing usage should still record observation")
	}
	if !containsString(tk.RawUsage.Last.Unavailable, rawFieldTotal) || !containsString(tk.RawUsage.Last.Unavailable, rawFieldElapsedMS) {
		t.Fatalf("missing fields: %v", tk.RawUsage.Last.Unavailable)
	}
}

func TestProviderResultUsageIdentityUsesCallSessionBeforeFreshSteps(t *testing.T) {
	root := testRoot(t)
	work := t.TempDir()
	bin := fakeClaudeBin(t, mkOKResultJSON("sess-keep"), "", 0)
	cfg := runTaskCfg(t, bin)
	tk := newTask(root, cfg, typeSequence, "fresh-session identity", work, []string{"a"}, 5)
	tk.FreshSteps = true
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := runTask(context.Background(), root, cfg, tk, false); err != nil {
		t.Fatal(err)
	}
	got, err := findTaskAnywhere(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RawUsage == nil || got.RawUsage.Last == nil {
		t.Fatal("raw_usage missing after run")
	}
	if got.RawUsage.Last.SessionID != "sess-keep" {
		t.Fatalf("completed-call session=%q identity=%q", got.RawUsage.Last.SessionID, got.RawUsage.Last.Identity)
	}
	if !strings.Contains(got.RawUsage.Last.Identity, "sess-keep") {
		t.Fatalf("identity lost session after FreshSteps: %q", got.RawUsage.Last.Identity)
	}
	if got.RawUsage.Last.Step != 0 {
		t.Fatalf("completed-call step should be pre-increment index, got %d", got.RawUsage.Last.Step)
	}
	if got.Step != 1 {
		t.Fatalf("card step after success=%d", got.Step)
	}
}

func writeAttemptFixture(t *testing.T, root string, rec *AttemptRecord) {
	t.Helper()
	path := attemptPath(root, rec.TaskID, rec.AttemptID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptActiveWallDurationActiveVsExitedAndNonoverlap(t *testing.T) {
	root := testRoot(t)
	cfg := testCfg()
	tk := newTask(root, cfg, typeSequence, "attempt wall", t.TempDir(), []string{"a", "b"}, 1)
	tk.ActiveAttemptID = "att-wall"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-5 * time.Second)
	writeAttemptFixture(t, root, &AttemptRecord{
		TaskID: tk.ID, AttemptID: "att-wall", State: attemptBound,
		CreatedAt: created.Format(time.RFC3339Nano),
		UpdatedAt: created.Format(time.RFC3339Nano),
	})
	active := attemptActiveWallDuration(root, tk)
	if active == nil || active.ElapsedMS == nil || *active.ElapsedMS < 4000 {
		t.Fatalf("bound attempt must use now-CreatedAt, not launch UpdatedAt: %+v", active)
	}
	res := &claudeResult{Type: "result", UsageSource: usageSrcClaudeResult}
	applyProviderResultUsage(root, tk, res)
	if tk.RawUsage == nil || tk.RawUsage.AccumulatedDurationMS == nil {
		t.Fatalf("first wall fallback missing: %+v", tk.RawUsage)
	}
	first := *tk.RawUsage.AccumulatedDurationMS
	if first < 4000 {
		t.Fatalf("first wall age=%d", first)
	}
	applyProviderResultUsage(root, tk, res)
	second := *tk.RawUsage.AccumulatedDurationMS
	if second-first > 2000 {
		t.Fatalf("second step summed overlapping attempt age: first=%d second=%d", first, second)
	}

	fresh := newTask(root, cfg, typeSequence, "exited wall", t.TempDir(), []string{"a"}, 1)
	fresh.ActiveAttemptID = "att-exit"
	if err := saveTask(root, fresh); err != nil {
		t.Fatal(err)
	}
	writeAttemptFixture(t, root, &AttemptRecord{
		TaskID: fresh.ID, AttemptID: "att-exit", State: attemptExited,
		CreatedAt: created.Format(time.RFC3339Nano),
		UpdatedAt: created.Add(1500 * time.Millisecond).Format(time.RFC3339Nano),
	})
	got := attemptActiveWallDuration(root, fresh)
	if got == nil || got.ElapsedMS == nil {
		t.Fatal("exited wall missing")
	}
	if *got.ElapsedMS < 1000 || *got.ElapsedMS > 2500 {
		t.Fatalf("exited wall should be UpdatedAt-CreatedAt ~1500ms, got %d", *got.ElapsedMS)
	}
}

func TestRawUsagePersistsKnownZeroOnTaskJSON(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	cfg := testCfg()
	tk := newTask(root, cfg, typeSequence, "raw usage persist", t.TempDir(), []string{"p"}, 1)
	res := &claudeResult{
		Type: "result", NumTurns: 0, TotalCostUSD: 0, DurationMS: 0,
		UsageFields:     usageFieldSet{InputTokens: int64Ptr(0), OutputTokens: int64Ptr(9)},
		CostPresent:     true,
		TurnsPresent:    true,
		DurationPresent: true,
		UsageSource:     usageSrcClaudeResult,
	}
	applyProviderResultUsage(root, tk, res)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RawUsage == nil || got.RawUsage.Accumulated.InputTokens == nil || *got.RawUsage.Accumulated.InputTokens != 0 {
		t.Fatalf("loaded known zero: %+v", got.RawUsage)
	}
	if got.RawUsage.Accumulated.CacheReadInputTokens != nil {
		t.Fatalf("omitted cache must stay missing: %+v", got.RawUsage.Accumulated)
	}
	body, err := os.ReadFile(filepath.Join(root, "tasks", tk.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"input_tokens": 0`)) && !bytes.Contains(body, []byte(`"input_tokens":0`)) {
		t.Fatalf("task JSON must keep known zero: %s", body)
	}
}

func TestRawUsageFakeCLIConsumerLoop(t *testing.T) {
	bin := buildCardexCLI(t)
	root := t.TempDir()
	work := t.TempDir()
	fake := filepath.Join(t.TempDir(), "claude")
	result := `{"type":"result","result":"ok","session_id":"sess-raw","num_turns":0,"total_cost_usd":0.5,"duration_ms":100,"is_error":false,"usage":{"input_tokens":0,"output_tokens":12}}`
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version|-v) echo 'claude 1.0'; exit 0;; esac\n" +
		"cat <<'JSON_EOF'\n" + result + "\nJSON_EOF\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"tasks", "archive", "logs", "events", "templates"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := defaultConfig(fake)
	cfg.StepTimeoutMin = 1
	cfg.HarvestMode = harvestModeOff
	cfg.MaxParallel = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	add := exec.Command(bin, "add", "-root", root, "-dir", work, "-title", "raw-usage-cli",
		"-review-after=false", "one bounded step")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("cardex add: %v\n%s", err, out)
	}
	id := onlyTaskID(t, root)
	run1, err := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
	if err != nil {
		t.Fatalf("cardex run: %v\n%s", err, run1)
	}
	list1, err := exec.Command(bin, "list", "-json", "-root", root, "-all").CombinedOutput()
	if err != nil {
		t.Fatalf("cardex list -json: %v\n%s", err, list1)
	}
	var tasks []*Task
	if err := json.Unmarshal(list1, &tasks); err != nil {
		t.Fatalf("list json: %v\n%s", err, list1)
	}
	if len(tasks) != 1 || tasks[0].RawUsage == nil {
		t.Fatalf("list json missing raw_usage: %s", list1)
	}
	ru := tasks[0].RawUsage
	if ru.Accumulated.InputTokens == nil || *ru.Accumulated.InputTokens != 0 {
		t.Fatalf("known zero input: %+v", ru.Accumulated)
	}
	if ru.Accumulated.OutputTokens == nil || *ru.Accumulated.OutputTokens != 12 {
		t.Fatalf("present output: %+v", ru.Accumulated)
	}
	if ru.Accumulated.CacheReadInputTokens != nil || ru.Accumulated.ReasoningTokens != nil {
		t.Fatalf("omitted fields present: %+v", ru.Accumulated)
	}
	if ru.Last == nil || ru.Last.ObservationWindow != usageWindowProviderCall {
		t.Fatalf("window: %+v", ru.Last)
	}
	if ru.AccumulatedCostUSD == nil || *ru.AccumulatedCostUSD != 0.5 {
		t.Fatalf("cost: %v", ru.AccumulatedCostUSD)
	}
	firstOut := *ru.Accumulated.OutputTokens
	run2, run2Err := exec.Command(bin, "run", "-root", root, id).CombinedOutput()
	if run2Err != nil && !bytes.Contains(run2, []byte("remains done")) && !bytes.Contains(run2, []byte("not dispatched")) {
		t.Fatalf("repeat cardex run: %v\n%s", run2Err, run2)
	}
	got, err := findTaskAnywhere(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.RawUsage == nil || got.RawUsage.Accumulated.OutputTokens == nil || *got.RawUsage.Accumulated.OutputTokens != firstOut {
		t.Fatalf("repeat terminal increased cumulative: first=%d now=%v\nrun2=%s", firstOut, got.RawUsage, run2)
	}
	events, _, err := loadTaskEvents(root, id)
	if err != nil {
		t.Fatal(err)
	}
	var done *TaskEvent
	for i := range events {
		if events[i].Type == evDone {
			done = &events[i]
		}
	}
	if done == nil {
		t.Fatalf("missing done event: %v", eventTypes(events))
	}
	if done.Detail["raw_usage"] == nil {
		t.Fatalf("done event missing raw_usage: %+v", done.Detail)
	}
	if done.Detail["observation_window"] != usageWindowProviderCall {
		t.Fatalf("event window: %+v", done.Detail)
	}
	cost, unavail := assertCostTelemetry(t, *done)
	if unavail || cost != 0.5 {
		t.Fatalf("known cost 0.5 vs unavail=%v cost=%v detail=%+v", unavail, cost, done.Detail)
	}
}
