package main

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Attributable raw usage/duration with per-field known vs missing.
// Weighted quota (appendUsage) and money equivalent stay distinct; this file
// never invents totals, cache-overlap, or subscription-to-spend.

const rawUsageSchemaV1 = "cardex-raw-usage-v1"

const (
	usageWindowProviderCall               = "provider_call"
	usageWindowNativeGoal                 = "native_goal"
	usageWindowNativeGoalComplete         = "native_goal_complete"
	usageWindowNativePostCompleteFollowup = "native_post_complete_followup"

	usageScopePerCall    = "per_call"
	usageScopeCumulative = "cumulative"

	durationSrcProviderDurationMS = "provider_duration_ms"
	durationSrcNativeElapsedMS    = "native_elapsed_ms"
	durationSrcAttemptActiveWall  = "attempt_active_wall"

	usageSrcClaudeResult        = "claude_result"
	usageSrcGrokBuildEnd        = "grok_build_end"
	usageSrcCursorResult        = "cursor_result"
	usageSrcOpenCodeStepFinish  = "opencode_step_finish"
	usageSrcGeminiStats         = "gemini_stats"
	usageSrcGrokNativeGoalState = "grok_native_goal_state"
)

const (
	rawFieldInput         = "input_tokens"
	rawFieldOutput        = "output_tokens"
	rawFieldCacheRead     = "cache_read_input_tokens"
	rawFieldCacheCreation = "cache_creation_input_tokens"
	rawFieldReasoning     = "reasoning_tokens"
	rawFieldTotal         = "total_tokens"
	rawFieldCostUSD       = "cost_usd"
	rawFieldTurns         = "turns"
	rawFieldElapsedMS     = "elapsed_ms"
)

var rawUsageTrackedFields = []string{
	rawFieldInput, rawFieldOutput, rawFieldCacheRead, rawFieldCacheCreation,
	rawFieldReasoning, rawFieldTotal, rawFieldCostUSD, rawFieldTurns, rawFieldElapsedMS,
}

const durationLabelIncludesWaits = "includes waits/cooldown/pause; not model compute"

const (
	usageRejectMissingIdentity = "missing_identity"
	usageRejectForeignIdentity = "foreign_identity"
	usageRejectRegressOrReset  = "regress_or_reset"
)

const (
	usageModelSrcProviderReported = "provider_reported"
	usageModelSrcConfiguration    = "configuration"
)

const maxAppliedCallIDs = 64

type usageFieldSet struct {
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	ReasoningTokens          *int64 `json:"reasoning_tokens,omitempty"`
	TotalTokens              *int64 `json:"total_tokens,omitempty"`
}

type usageDuration struct {
	ElapsedMS     *int64 `json:"elapsed_ms,omitempty"`
	Source        string `json:"source"`
	IncludesWaits bool   `json:"includes_waits,omitempty"`
	Label         string `json:"label,omitempty"`
}

type usageObservation struct {
	Identity          string         `json:"identity"`
	Source            string         `json:"source"`
	Runner            string         `json:"runner,omitempty"`
	Model             string         `json:"model,omitempty"`
	ModelSource       string         `json:"model_source,omitempty"`
	TaskID            string         `json:"task_id,omitempty"`
	AttemptID         string         `json:"attempt_id,omitempty"`
	SessionID         string         `json:"session_id,omitempty"`
	NativeGoalID      string         `json:"native_goal_id,omitempty"`
	Step              int            `json:"step,omitempty"`
	ObservationWindow string         `json:"observation_window"`
	Scope             string         `json:"scope"`
	Fields            usageFieldSet  `json:"fields"`
	CostUSD           *float64       `json:"cost_usd,omitempty"`
	Turns             *int64         `json:"turns,omitempty"`
	Duration          *usageDuration `json:"duration,omitempty"`
	Unavailable       []string       `json:"unavailable,omitempty"`
	AppliedDelta      *usageFieldSet `json:"applied_delta,omitempty"`
	AppliedCostDelta  *float64       `json:"applied_cost_delta,omitempty"`
	AppliedTurnsDelta *int64         `json:"applied_turns_delta,omitempty"`
	Rejected          string         `json:"rejected,omitempty"`
}

type taskRawUsage struct {
	Schema                string            `json:"schema"`
	Last                  *usageObservation `json:"last,omitempty"`
	Accumulated           usageFieldSet     `json:"accumulated"`
	AccumulatedCostUSD    *float64          `json:"accumulated_cost_usd,omitempty"`
	AccumulatedTurns      *int64            `json:"accumulated_turns,omitempty"`
	AccumulatedDurationMS *int64            `json:"accumulated_duration_ms,omitempty"`
	Duration              *usageDuration    `json:"duration,omitempty"`
	BoundIdentity         string            `json:"bound_identity,omitempty"`
	BoundWindow           string            `json:"bound_window,omitempty"`
	LastApplied           usageFieldSet     `json:"last_applied"`
	LastAppliedCost       *float64          `json:"last_applied_cost,omitempty"`
	LastAppliedTurns      *int64            `json:"last_applied_turns,omitempty"`
	LastAppliedElapsed    *int64            `json:"last_applied_elapsed_ms,omitempty"`
	AppliedCallIDs        []string          `json:"applied_call_ids,omitempty"`
	LastAttemptWallID     string            `json:"last_attempt_wall_id,omitempty"`
	LastAttemptWallMS     *int64            `json:"last_attempt_wall_ms,omitempty"`
}

func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }

func cloneInt64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneFloat64(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneFieldSet(s usageFieldSet) usageFieldSet {
	return usageFieldSet{
		InputTokens:              cloneInt64(s.InputTokens),
		OutputTokens:             cloneInt64(s.OutputTokens),
		CacheReadInputTokens:     cloneInt64(s.CacheReadInputTokens),
		CacheCreationInputTokens: cloneInt64(s.CacheCreationInputTokens),
		ReasoningTokens:          cloneInt64(s.ReasoningTokens),
		TotalTokens:              cloneInt64(s.TotalTokens),
	}
}

func addOptInt(dst *int64, src *int64) *int64 {
	if src == nil {
		return dst
	}
	if dst == nil {
		return cloneInt64(src)
	}
	v := *dst + *src
	return &v
}

func addOptFloat(dst *float64, src *float64) *float64 {
	if src == nil {
		return dst
	}
	if dst == nil {
		return cloneFloat64(src)
	}
	v := *dst + *src
	return &v
}

func usageFieldsFromJSON(raw json.RawMessage, aliases map[string]string) usageFieldSet {
	var out usageFieldSet
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return out
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return out
	}
	for key, dest := range aliases {
		v, ok := obj[key]
		if !ok {
			continue
		}
		n, ok := parseOptionalJSONInt64(v)
		if !ok {
			continue
		}
		switch dest {
		case rawFieldInput:
			out.InputTokens = n
		case rawFieldOutput:
			out.OutputTokens = n
		case rawFieldCacheRead:
			out.CacheReadInputTokens = n
		case rawFieldCacheCreation:
			out.CacheCreationInputTokens = n
		case rawFieldReasoning:
			out.ReasoningTokens = n
		case rawFieldTotal:
			out.TotalTokens = n
		}
	}
	return out
}

func nonnegInt64(n int64) (*int64, bool) {
	if n < 0 {
		return nil, false
	}
	return &n, true
}

func nonnegFloat64(f float64) (*float64, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return nil, false
	}
	return &f, true
}

func parseOptionalJSONInt64(raw json.RawMessage) (*int64, bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, false
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		if n < 0 {
			return nil, false
		}
		return &n, true
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, false
	}
	if f < 0 || f > float64(math.MaxInt64) || f != math.Trunc(f) {
		return nil, false
	}
	v := int64(f)
	return &v, true
}

func parseOptionalJSONFloat64(raw json.RawMessage) (*float64, bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, false
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return nil, false
	}
	return &f, true
}

func rawKeyPresent(obj map[string]json.RawMessage, key string) bool {
	if obj == nil {
		return false
	}
	v, ok := obj[key]
	if !ok {
		return false
	}
	return strings.TrimSpace(string(v)) != "null"
}

func decodeJSONObject(raw []byte) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	return obj
}

var claudeUsageAliases = map[string]string{
	"input_tokens":                rawFieldInput,
	"output_tokens":               rawFieldOutput,
	"cache_read_input_tokens":     rawFieldCacheRead,
	"cache_creation_input_tokens": rawFieldCacheCreation,
	"reasoning_tokens":            rawFieldReasoning,
	"total_tokens":                rawFieldTotal,
}

var grokUsageAliases = claudeUsageAliases

var cursorUsageAliases = map[string]string{
	"inputTokens":      rawFieldInput,
	"outputTokens":     rawFieldOutput,
	"cacheReadTokens":  rawFieldCacheRead,
	"cacheWriteTokens": rawFieldCacheCreation,
}

var openCodeUsageAliases = map[string]string{
	"input":     rawFieldInput,
	"output":    rawFieldOutput,
	"reasoning": rawFieldReasoning,
	"total":     rawFieldTotal,
	"cache":     rawFieldCacheRead,
}

func unavailableFields(obs usageObservation) []string {
	present := map[string]bool{}
	if obs.Fields.InputTokens != nil {
		present[rawFieldInput] = true
	}
	if obs.Fields.OutputTokens != nil {
		present[rawFieldOutput] = true
	}
	if obs.Fields.CacheReadInputTokens != nil {
		present[rawFieldCacheRead] = true
	}
	if obs.Fields.CacheCreationInputTokens != nil {
		present[rawFieldCacheCreation] = true
	}
	if obs.Fields.ReasoningTokens != nil {
		present[rawFieldReasoning] = true
	}
	if obs.Fields.TotalTokens != nil {
		present[rawFieldTotal] = true
	}
	if obs.CostUSD != nil {
		present[rawFieldCostUSD] = true
	}
	if obs.Turns != nil {
		present[rawFieldTurns] = true
	}
	if obs.Duration != nil && obs.Duration.ElapsedMS != nil {
		present[rawFieldElapsedMS] = true
	}
	var out []string
	for _, name := range rawUsageTrackedFields {
		if !present[name] {
			out = append(out, name)
		}
	}
	return out
}

func providerCallIdentity(t *Task, source, window string) string {
	if t == nil {
		return ""
	}
	attempt := strings.TrimSpace(t.ActiveAttemptID)
	if attempt == "" && t.Goal != nil {
		attempt = strings.TrimSpace(t.Goal.BoundAttemptID)
	}
	runner := strings.TrimSpace(t.Runner)
	if runner == "" {
		runner = "claude"
	}
	return strings.Join([]string{
		usageScopePerCall, source, t.ID, attempt, strconv.Itoa(t.Step), t.SessionID, window,
	}, "|")
}

func cumulativeIdentity(source, session, goal, window string) string {
	return strings.Join([]string{usageScopeCumulative, source, session, goal, window}, "|")
}

func splitCumulativeIdentity(id string) (source, session, goal, window string, ok bool) {
	parts := strings.Split(id, "|")
	if len(parts) != 5 || parts[0] != usageScopeCumulative {
		return "", "", "", "", false
	}
	return parts[1], parts[2], parts[3], parts[4], true
}

func relatedCumulativeIdentities(bound, incoming string) (sameSource bool, scopeChange bool) {
	bs, bsess, bgoal, _, bok := splitCumulativeIdentity(bound)
	is, isess, igoal, _, iok := splitCumulativeIdentity(incoming)
	if !bok || !iok {
		return false, false
	}
	if bs != is || bsess != isess || bgoal != igoal || bs == "" || bsess == "" || bgoal == "" {
		return false, false
	}
	if bound == incoming {
		return true, false
	}
	return true, true
}

func fieldSetEmpty(s usageFieldSet) bool {
	return s.InputTokens == nil && s.OutputTokens == nil && s.CacheReadInputTokens == nil &&
		s.CacheCreationInputTokens == nil && s.ReasoningTokens == nil && s.TotalTokens == nil
}

func optIntZero(p *int64) bool   { return p == nil || *p == 0 }
func optFloatZero(p *float64) bool { return p == nil || *p == 0 }

func fieldDeltaEmpty(s usageFieldSet) bool {
	return optIntZero(s.InputTokens) && optIntZero(s.OutputTokens) && optIntZero(s.CacheReadInputTokens) &&
		optIntZero(s.CacheCreationInputTokens) && optIntZero(s.ReasoningTokens) && optIntZero(s.TotalTokens)
}

func cumulativeFieldDelta(last, incoming *int64) (delta *int64, ok bool) {
	if incoming == nil {
		return nil, true
	}
	if last == nil {
		return cloneInt64(incoming), true
	}
	if *incoming < *last {
		return nil, false
	}
	v := *incoming - *last
	return &v, true
}

func cumulativeFloatDelta(last, incoming *float64) (delta *float64, ok bool) {
	if incoming == nil {
		return nil, true
	}
	if last == nil {
		return cloneFloat64(incoming), true
	}
	if *incoming < *last {
		return nil, false
	}
	v := *incoming - *last
	return &v, true
}

func cumulativeDelta(last, incoming usageFieldSet) (usageFieldSet, bool) {
	var out usageFieldSet
	var ok bool
	if out.InputTokens, ok = cumulativeFieldDelta(last.InputTokens, incoming.InputTokens); !ok {
		return usageFieldSet{}, false
	}
	if out.OutputTokens, ok = cumulativeFieldDelta(last.OutputTokens, incoming.OutputTokens); !ok {
		return usageFieldSet{}, false
	}
	if out.CacheReadInputTokens, ok = cumulativeFieldDelta(last.CacheReadInputTokens, incoming.CacheReadInputTokens); !ok {
		return usageFieldSet{}, false
	}
	if out.CacheCreationInputTokens, ok = cumulativeFieldDelta(last.CacheCreationInputTokens, incoming.CacheCreationInputTokens); !ok {
		return usageFieldSet{}, false
	}
	if out.ReasoningTokens, ok = cumulativeFieldDelta(last.ReasoningTokens, incoming.ReasoningTokens); !ok {
		return usageFieldSet{}, false
	}
	if out.TotalTokens, ok = cumulativeFieldDelta(last.TotalTokens, incoming.TotalTokens); !ok {
		return usageFieldSet{}, false
	}
	return out, true
}

func applyFieldSet(dst *usageFieldSet, delta usageFieldSet) {
	dst.InputTokens = addOptInt(dst.InputTokens, delta.InputTokens)
	dst.OutputTokens = addOptInt(dst.OutputTokens, delta.OutputTokens)
	dst.CacheReadInputTokens = addOptInt(dst.CacheReadInputTokens, delta.CacheReadInputTokens)
	dst.CacheCreationInputTokens = addOptInt(dst.CacheCreationInputTokens, delta.CacheCreationInputTokens)
	dst.ReasoningTokens = addOptInt(dst.ReasoningTokens, delta.ReasoningTokens)
	dst.TotalTokens = addOptInt(dst.TotalTokens, delta.TotalTokens)
}

func overlayFieldSet(dst *usageFieldSet, src usageFieldSet) {
	if src.InputTokens != nil {
		dst.InputTokens = cloneInt64(src.InputTokens)
	}
	if src.OutputTokens != nil {
		dst.OutputTokens = cloneInt64(src.OutputTokens)
	}
	if src.CacheReadInputTokens != nil {
		dst.CacheReadInputTokens = cloneInt64(src.CacheReadInputTokens)
	}
	if src.CacheCreationInputTokens != nil {
		dst.CacheCreationInputTokens = cloneInt64(src.CacheCreationInputTokens)
	}
	if src.ReasoningTokens != nil {
		dst.ReasoningTokens = cloneInt64(src.ReasoningTokens)
	}
	if src.TotalTokens != nil {
		dst.TotalTokens = cloneInt64(src.TotalTokens)
	}
}

func usageContainsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func applyUsageObservation(state *taskRawUsage, obs usageObservation) *taskRawUsage {
	if state == nil {
		state = &taskRawUsage{Schema: rawUsageSchemaV1}
	}
	if state.Schema == "" {
		state.Schema = rawUsageSchemaV1
	}
	obs.Unavailable = unavailableFields(obs)
	if strings.TrimSpace(obs.Identity) == "" {
		obs.Rejected = usageRejectMissingIdentity
		cp := obs
		state.Last = &cp
		return state
	}
	if obs.Scope == usageScopePerCall {
		return applyPerCallObservation(state, obs)
	}
	return applyCumulativeObservation(state, obs)
}

func applyPerCallObservation(state *taskRawUsage, obs usageObservation) *taskRawUsage {
	if usageContainsString(state.AppliedCallIDs, obs.Identity) {
		zero := usageFieldSet{}
		obs.AppliedDelta = &zero
		obs.AppliedCostDelta = float64Ptr(0)
		obs.AppliedTurnsDelta = int64Ptr(0)
		cp := obs
		state.Last = &cp
		return state
	}
	delta := cloneFieldSet(obs.Fields)
	applyFieldSet(&state.Accumulated, delta)
	state.AccumulatedCostUSD = addOptFloat(state.AccumulatedCostUSD, obs.CostUSD)
	state.AccumulatedTurns = addOptInt(state.AccumulatedTurns, obs.Turns)
	if obs.Duration != nil && obs.Duration.ElapsedMS != nil {
		state.AccumulatedDurationMS = addOptInt(state.AccumulatedDurationMS, obs.Duration.ElapsedMS)
		state.Duration = cloneDuration(obs.Duration)
	}
	obs.AppliedDelta = &delta
	obs.AppliedCostDelta = cloneFloat64(obs.CostUSD)
	obs.AppliedTurnsDelta = cloneInt64(obs.Turns)
	state.AppliedCallIDs = append(state.AppliedCallIDs, obs.Identity)
	if len(state.AppliedCallIDs) > maxAppliedCallIDs {
		state.AppliedCallIDs = state.AppliedCallIDs[len(state.AppliedCallIDs)-maxAppliedCallIDs:]
	}
	state.BoundIdentity = obs.Identity
	state.BoundWindow = obs.ObservationWindow
	cp := obs
	state.Last = &cp
	return state
}

func cloneDuration(d *usageDuration) *usageDuration {
	if d == nil {
		return nil
	}
	out := *d
	out.ElapsedMS = cloneInt64(d.ElapsedMS)
	return &out
}

func applyCumulativeObservation(state *taskRawUsage, obs usageObservation) *taskRawUsage {
	if state.BoundIdentity != "" && state.BoundIdentity != obs.Identity {
		same, scopeChange := relatedCumulativeIdentities(state.BoundIdentity, obs.Identity)
		if !same {
			obs.Rejected = usageRejectForeignIdentity
			cp := obs
			state.Last = &cp
			return state
		}
		if scopeChange {
			state.BoundIdentity = obs.Identity
			state.BoundWindow = obs.ObservationWindow
			return applyCumulativeDelta(state, obs)
		}
		obs.Rejected = usageRejectForeignIdentity
		cp := obs
		state.Last = &cp
		return state
	}
	if state.BoundIdentity == "" {
		state.BoundIdentity = obs.Identity
		state.BoundWindow = obs.ObservationWindow
		return applyCumulativeDelta(state, obs)
	}
	return applyCumulativeDelta(state, obs)
}

func applyCumulativeDelta(state *taskRawUsage, obs usageObservation) *taskRawUsage {
	delta, ok := cumulativeDelta(state.LastApplied, obs.Fields)
	if !ok {
		obs.Rejected = usageRejectRegressOrReset
		cp := obs
		state.Last = &cp
		return state
	}
	costDelta, ok := cumulativeFloatDelta(state.LastAppliedCost, obs.CostUSD)
	if !ok {
		obs.Rejected = usageRejectRegressOrReset
		cp := obs
		state.Last = &cp
		return state
	}
	turnsDelta, ok := cumulativeFieldDelta(state.LastAppliedTurns, obs.Turns)
	if !ok {
		obs.Rejected = usageRejectRegressOrReset
		cp := obs
		state.Last = &cp
		return state
	}
	var elapsedIn *int64
	if obs.Duration != nil {
		elapsedIn = obs.Duration.ElapsedMS
	}
	elapsedDelta, ok := cumulativeFieldDelta(state.LastAppliedElapsed, elapsedIn)
	if !ok {
		obs.Rejected = usageRejectRegressOrReset
		cp := obs
		state.Last = &cp
		return state
	}
	applyFieldSet(&state.Accumulated, delta)
	state.AccumulatedCostUSD = addOptFloat(state.AccumulatedCostUSD, costDelta)
	state.AccumulatedTurns = addOptInt(state.AccumulatedTurns, turnsDelta)
	if elapsedDelta != nil {
		state.AccumulatedDurationMS = addOptInt(state.AccumulatedDurationMS, elapsedDelta)
		if obs.Duration != nil {
			state.Duration = cloneDuration(obs.Duration)
		}
	}
	overlayFieldSet(&state.LastApplied, obs.Fields)
	if obs.CostUSD != nil {
		state.LastAppliedCost = cloneFloat64(obs.CostUSD)
	}
	if obs.Turns != nil {
		state.LastAppliedTurns = cloneInt64(obs.Turns)
	}
	if elapsedIn != nil {
		state.LastAppliedElapsed = cloneInt64(elapsedIn)
	}
	obs.AppliedDelta = &delta
	obs.AppliedCostDelta = costDelta
	obs.AppliedTurnsDelta = turnsDelta
	cp := obs
	state.Last = &cp
	return state
}

func cloneTaskRawUsage(s *taskRawUsage) *taskRawUsage {
	if s == nil {
		return nil
	}
	out := *s
	out.Accumulated = cloneFieldSet(s.Accumulated)
	out.AccumulatedCostUSD = cloneFloat64(s.AccumulatedCostUSD)
	out.AccumulatedTurns = cloneInt64(s.AccumulatedTurns)
	out.AccumulatedDurationMS = cloneInt64(s.AccumulatedDurationMS)
	out.Duration = cloneDuration(s.Duration)
	out.LastApplied = cloneFieldSet(s.LastApplied)
	out.LastAppliedCost = cloneFloat64(s.LastAppliedCost)
	out.LastAppliedTurns = cloneInt64(s.LastAppliedTurns)
	out.LastAppliedElapsed = cloneInt64(s.LastAppliedElapsed)
	out.LastAttemptWallMS = cloneInt64(s.LastAttemptWallMS)
	if s.AppliedCallIDs != nil {
		out.AppliedCallIDs = append([]string(nil), s.AppliedCallIDs...)
	}
	if s.Last != nil {
		cp := *s.Last
		cp.Fields = cloneFieldSet(s.Last.Fields)
		cp.CostUSD = cloneFloat64(s.Last.CostUSD)
		cp.Turns = cloneInt64(s.Last.Turns)
		cp.Duration = cloneDuration(s.Last.Duration)
		if s.Last.AppliedDelta != nil {
			d := cloneFieldSet(*s.Last.AppliedDelta)
			cp.AppliedDelta = &d
		}
		cp.AppliedCostDelta = cloneFloat64(s.Last.AppliedCostDelta)
		cp.AppliedTurnsDelta = cloneInt64(s.Last.AppliedTurnsDelta)
		if s.Last.Unavailable != nil {
			cp.Unavailable = append([]string(nil), s.Last.Unavailable...)
		}
		out.Last = &cp
	}
	return &out
}

func optIntEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func optFloatEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func fieldSetEqual(a, b usageFieldSet) bool {
	return optIntEqual(a.InputTokens, b.InputTokens) &&
		optIntEqual(a.OutputTokens, b.OutputTokens) &&
		optIntEqual(a.CacheReadInputTokens, b.CacheReadInputTokens) &&
		optIntEqual(a.CacheCreationInputTokens, b.CacheCreationInputTokens) &&
		optIntEqual(a.ReasoningTokens, b.ReasoningTokens) &&
		optIntEqual(a.TotalTokens, b.TotalTokens)
}

func durationEqual(a, b *usageDuration) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Source == b.Source && a.IncludesWaits == b.IncludesWaits && a.Label == b.Label &&
		optIntEqual(a.ElapsedMS, b.ElapsedMS)
}

func rawUsageChanged(before, after *taskRawUsage) bool {
	if after == nil {
		return false
	}
	if before == nil {
		return after.Last != nil
	}
	if after.Last != nil && after.Last.Rejected != "" {
		return false
	}
	if before.BoundIdentity != after.BoundIdentity || before.BoundWindow != after.BoundWindow {
		return true
	}
	if !fieldSetEqual(before.Accumulated, after.Accumulated) {
		return true
	}
	if !fieldSetEqual(before.LastApplied, after.LastApplied) {
		return true
	}
	if !optFloatEqual(before.AccumulatedCostUSD, after.AccumulatedCostUSD) {
		return true
	}
	if !optIntEqual(before.AccumulatedTurns, after.AccumulatedTurns) {
		return true
	}
	if !optIntEqual(before.AccumulatedDurationMS, after.AccumulatedDurationMS) {
		return true
	}
	if !optIntEqual(before.LastAppliedElapsed, after.LastAppliedElapsed) {
		return true
	}
	if !durationEqual(before.Duration, after.Duration) {
		return true
	}
	if before.LastAttemptWallID != after.LastAttemptWallID || !optIntEqual(before.LastAttemptWallMS, after.LastAttemptWallMS) {
		return true
	}
	return false
}

func usageResultLane(t *Task, res *claudeResult) string {
	source := ""
	if res != nil {
		source = strings.TrimSpace(res.UsageSource)
	}
	switch source {
	case usageSrcGrokBuildEnd:
		return grokBuildRunnerName
	case usageSrcCursorResult:
		return cursorRunnerName
	case usageSrcOpenCodeStepFinish:
		return "opencode"
	case usageSrcGeminiStats:
		return "gemini"
	case usageSrcClaudeResult:
		return "claude"
	}
	if t != nil {
		return strings.TrimSpace(t.Runner)
	}
	return ""
}

// observedUsageModel prefers the provider-reported assistant model. A
// configuration binding is used only when this call's actual resolver
// readback matches this result's lane (ActualRunner + ActualModel).
// Request-only, empty, foreign, or stale LastRouteAttempt records stay
// unknown. Generic Task.Model is never the actual model.
func observedUsageModel(t *Task, res *claudeResult) (model, source string) {
	if res != nil {
		if m := strings.TrimSpace(res.ObservedAssistantModel); m != "" {
			return m, usageModelSrcProviderReported
		}
	}
	if t == nil || t.LastRouteAttempt == nil {
		return "", ""
	}
	lane := usageResultLane(t, res)
	actualRunner := strings.TrimSpace(t.LastRouteAttempt.ActualRunner)
	actualModel := strings.TrimSpace(t.LastRouteAttempt.ActualModel)
	if lane == "" || actualRunner == "" || actualModel == "" || actualRunner != lane {
		return "", ""
	}
	return actualModel, usageModelSrcConfiguration
}

func observationFromProviderResult(root string, t *Task, res *claudeResult) usageObservation {
	var obs usageObservation
	if t == nil || res == nil {
		obs.Rejected = usageRejectMissingIdentity
		return obs
	}
	source := strings.TrimSpace(res.UsageSource)
	if source == "" {
		source = usageSrcClaudeResult
	}
	obs.Source = source
	obs.Runner = t.Runner
	obs.Model, obs.ModelSource = observedUsageModel(t, res)
	obs.TaskID = t.ID
	obs.AttemptID = t.ActiveAttemptID
	obs.SessionID = t.SessionID
	obs.Step = t.Step
	obs.ObservationWindow = usageWindowProviderCall
	obs.Scope = usageScopePerCall
	obs.Identity = providerCallIdentity(t, source, obs.ObservationWindow)
	obs.Fields = res.UsageFields
	if res.CostPresent {
		if c, ok := nonnegFloat64(res.TotalCostUSD); ok {
			obs.CostUSD = c
		}
	}
	if res.TurnsPresent {
		if n, ok := nonnegInt64(int64(res.NumTurns)); ok {
			obs.Turns = n
		}
	}
	if res.DurationPresent {
		if n, ok := nonnegInt64(res.DurationMS); ok {
			obs.Duration = &usageDuration{
				ElapsedMS: n,
				Source:    durationSrcProviderDurationMS,
			}
		}
	} else if d := attemptActiveWallDuration(root, t); d != nil {
		obs.Duration = d
	}
	obs.Unavailable = unavailableFields(obs)
	return obs
}

func parseAttemptTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts, true
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts, true
	}
	return time.Time{}, false
}

func attemptAgeMS(rec *AttemptRecord, now time.Time) (int64, bool) {
	if rec == nil {
		return 0, false
	}
	start, ok := parseAttemptTime(rec.CreatedAt)
	if !ok {
		return 0, false
	}
	end := now
	switch rec.State {
	case attemptExited, attemptRevoked:
		if u, ok := parseAttemptTime(rec.UpdatedAt); ok {
			end = u
		}
	default:
		end = now
	}
	ms := end.Sub(start).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return ms, true
}

func attemptActiveWallDuration(root string, t *Task) *usageDuration {
	if t == nil || strings.TrimSpace(t.ActiveAttemptID) == "" || root == "" {
		return nil
	}
	rec, err := loadAttempt(root, t.ID, t.ActiveAttemptID)
	if err != nil || rec == nil {
		return nil
	}
	age, ok := attemptAgeMS(rec, time.Now())
	if !ok {
		return nil
	}
	delta := age
	if t.RawUsage != nil && t.RawUsage.LastAttemptWallID == rec.AttemptID && t.RawUsage.LastAttemptWallMS != nil {
		delta = age - *t.RawUsage.LastAttemptWallMS
		if delta < 0 {
			delta = 0
		}
	}
	return &usageDuration{
		ElapsedMS:     int64Ptr(delta),
		Source:        durationSrcAttemptActiveWall,
		IncludesWaits: true,
		Label:         durationLabelIncludesWaits,
	}
}

func noteAttemptWallWatermark(root string, t *Task) {
	if t == nil || t.RawUsage == nil || strings.TrimSpace(t.ActiveAttemptID) == "" || root == "" {
		return
	}
	rec, err := loadAttempt(root, t.ID, t.ActiveAttemptID)
	if err != nil || rec == nil {
		return
	}
	age, ok := attemptAgeMS(rec, time.Now())
	if !ok {
		return
	}
	t.RawUsage.LastAttemptWallID = rec.AttemptID
	t.RawUsage.LastAttemptWallMS = int64Ptr(age)
}

func applyProviderResultUsage(root string, t *Task, res *claudeResult) {
	if t == nil || res == nil {
		return
	}
	obs := observationFromProviderResult(root, t, res)
	t.RawUsage = applyUsageObservation(t.RawUsage, obs)
	if obs.Duration != nil && obs.Duration.ElapsedMS != nil {
		noteAttemptWallWatermark(root, t)
	}
}

func nativeHighWaterIncreased(t *Task, obs nativeGoalObservation) bool {
	if t == nil || t.RawUsage == nil {
		return false
	}
	if obs.HighWaterTokens != nil && t.RawUsage.LastApplied.TotalTokens != nil &&
		*obs.HighWaterTokens > *t.RawUsage.LastApplied.TotalTokens {
		return true
	}
	return false
}

func nativeObservationWindow(t *Task, obs nativeGoalObservation) string {
	status := strings.ToLower(strings.TrimSpace(obs.NativeStatus))
	if status != "complete" {
		return usageWindowNativeGoal
	}
	if t != nil && t.RawUsage != nil && t.RawUsage.BoundWindow == usageWindowNativePostCompleteFollowup {
		return usageWindowNativePostCompleteFollowup
	}
	alreadyDone := t != nil && (t.Status == statusDone || (t.Goal != nil && t.Goal.Observation == goalObsDone))
	if t != nil && t.RawUsage != nil && t.RawUsage.BoundWindow == usageWindowNativeGoalComplete {
		if alreadyDone && nativeHighWaterIncreased(t, obs) {
			return usageWindowNativePostCompleteFollowup
		}
		return usageWindowNativeGoalComplete
	}
	if alreadyDone {
		return usageWindowNativePostCompleteFollowup
	}
	return usageWindowNativeGoalComplete
}

func observationFromNativeGoal(t *Task, obs nativeGoalObservation) usageObservation {
	var out usageObservation
	if t == nil {
		out.Rejected = usageRejectMissingIdentity
		return out
	}
	session := strings.TrimSpace(obs.SessionID)
	if session == "" {
		session = strings.TrimSpace(t.SessionID)
	}
	goalID := strings.TrimSpace(obs.GoalID)
	if goalID == "" && t.Goal != nil {
		goalID = strings.TrimSpace(t.Goal.NativeGoalID)
	}
	window := nativeObservationWindow(t, obs)
	out.Source = usageSrcGrokNativeGoalState
	out.Runner = grokBuildRunnerName
	out.Model = t.GrokModel
	out.TaskID = t.ID
	out.AttemptID = t.ActiveAttemptID
	if out.AttemptID == "" && t.Goal != nil {
		out.AttemptID = t.Goal.BoundAttemptID
	}
	out.SessionID = session
	out.NativeGoalID = goalID
	out.ObservationWindow = window
	out.Scope = usageScopeCumulative
	out.Identity = cumulativeIdentity(usageSrcGrokNativeGoalState, session, goalID, window)
	if session == "" || goalID == "" {
		out.Identity = ""
		out.Rejected = usageRejectMissingIdentity
	}
	if obs.HighWaterTokens != nil {
		out.Fields.TotalTokens = cloneInt64(obs.HighWaterTokens)
	}
	if obs.ElapsedMS != nil {
		out.Duration = &usageDuration{
			ElapsedMS: cloneInt64(obs.ElapsedMS),
			Source:    durationSrcNativeElapsedMS,
		}
	}
	out.Unavailable = unavailableFields(out)
	return out
}

func applyNativeGoalUsage(t *Task, obs nativeGoalObservation) bool {
	if t == nil {
		return false
	}
	before := cloneTaskRawUsage(t.RawUsage)
	t.RawUsage = applyUsageObservation(t.RawUsage, observationFromNativeGoal(t, obs))
	return rawUsageChanged(before, t.RawUsage)
}

func knownTaskCost(t *Task) (bool, float64) {
	if t == nil {
		return false, 0
	}
	if t.RawUsage != nil && t.RawUsage.AccumulatedCostUSD != nil {
		return true, *t.RawUsage.AccumulatedCostUSD
	}
	if t.CostUSD > 0 {
		return true, t.CostUSD
	}
	return false, 0
}

func knownTaskTurns(t *Task) (bool, int) {
	if t == nil {
		return false, 0
	}
	if t.RawUsage != nil && t.RawUsage.AccumulatedTurns != nil {
		return true, int(*t.RawUsage.AccumulatedTurns)
	}
	if t.TurnsUsed > 0 {
		return true, t.TurnsUsed
	}
	return false, 0
}

func projectRawUsageIntoDetail(detail map[string]any, t *Task) {
	if detail == nil || t == nil || t.RawUsage == nil {
		return
	}
	raw, err := json.Marshal(t.RawUsage)
	if err != nil {
		return
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	detail["raw_usage"] = obj
	if t.RawUsage.Last != nil && t.RawUsage.Last.ObservationWindow != "" {
		detail["observation_window"] = t.RawUsage.Last.ObservationWindow
	} else if t.RawUsage.BoundWindow != "" {
		detail["observation_window"] = t.RawUsage.BoundWindow
	}
}

func annotateResultPresenceFromJSON(res *claudeResult, raw []byte, source string) {
	if res == nil || len(raw) == 0 {
		return
	}
	env := decodeJSONObject(raw)
	if env == nil {
		return
	}
	if source != "" && res.UsageSource == "" {
		res.UsageSource = source
	}
	if u, ok := env["usage"]; ok {
		res.UsageFields = usageFieldsFromJSON(u, claudeUsageAliases)
	}
	if rawKeyPresent(env, "total_cost_usd") {
		res.CostPresent = true
	}
	if rawKeyPresent(env, "num_turns") {
		res.TurnsPresent = true
	}
	if rawKeyPresent(env, "duration_ms") {
		res.DurationPresent = true
	}
}

func mergeUsageFieldSet(a, b usageFieldSet) usageFieldSet {
	out := cloneFieldSet(a)
	overlayFieldSet(&out, b)
	return out
}

func sumUsageFieldSet(a, b usageFieldSet) usageFieldSet {
	return usageFieldSet{
		InputTokens:              addOptInt(a.InputTokens, b.InputTokens),
		OutputTokens:             addOptInt(a.OutputTokens, b.OutputTokens),
		CacheReadInputTokens:     addOptInt(a.CacheReadInputTokens, b.CacheReadInputTokens),
		CacheCreationInputTokens: addOptInt(a.CacheCreationInputTokens, b.CacheCreationInputTokens),
		ReasoningTokens:          addOptInt(a.ReasoningTokens, b.ReasoningTokens),
		TotalTokens:              addOptInt(a.TotalTokens, b.TotalTokens),
	}
}
