// Breaker conditions, per-kind budgets, and home config. Design §6.5.
package loop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cardex/internal/model"
)

const (
	defaultDesignSoft = 8000
	defaultDesignHard = 20000
	defaultStallSpend = 100000
)

// Config is the development-loop thresholds. Zero soft or hard objective
// lines are unset and do not fire; those live on the objective, not here.
type Config struct {
	ReworkLimit        int
	TransportRetries   int
	RetryBackoff       time.Duration
	BreakerConsecutive int
	BreakerSameError   int
	BreakerRepair      int
	BreakerStall       time.Duration
	BreakerSpend       int64
	Budgets            map[model.CardKind]model.Budget
	Warnings           []string
}

// DefaultConfig returns the design defaults. Research is one quarter of design.
func DefaultConfig() Config {
	design := model.Budget{SoftTokens: defaultDesignSoft, HardTokens: defaultDesignHard}
	cfg := Config{
		ReworkLimit:        2,
		TransportRetries:   2,
		RetryBackoff:       0,
		BreakerConsecutive: 3,
		BreakerSameError:   2,
		BreakerRepair:      2,
		BreakerStall:       6 * time.Hour,
		BreakerSpend:       defaultStallSpend,
		Budgets:            map[model.CardKind]model.Budget{},
	}
	for _, kind := range model.AllCardKinds() {
		cfg.Budgets[kind] = design
	}
	cfg.Budgets[model.KindResearch] = quarter(design)
	return cfg
}

func quarter(b model.Budget) model.Budget {
	return model.Budget{SoftTokens: b.SoftTokens / 4, HardTokens: b.HardTokens / 4}
}

// BudgetFor returns the configured default for kind.
func (c Config) BudgetFor(kind model.CardKind) model.Budget {
	if b, ok := c.Budgets[kind]; ok {
		return b
	}
	if kind == model.KindResearch {
		if d, ok := c.Budgets[model.KindDesign]; ok {
			return quarter(d)
		}
	}
	if d, ok := c.Budgets[model.KindDesign]; ok {
		return d
	}
	return model.Budget{SoftTokens: defaultDesignSoft, HardTokens: defaultDesignHard}
}

// LoadConfig reads $CARDEX_HOME/config.json. A missing file uses defaults.
// Unknown keys are ignored and returned as warnings. Invalid JSON uses defaults.
func LoadConfig(home string) Config {
	cfg := DefaultConfig()
	if home == "" {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg
		}
		cfg.Warnings = append(cfg.Warnings, "config.json unreadable")
		return cfg
	}
	if len(bytesTrim(data)) == 0 {
		return cfg
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		cfg.Warnings = append(cfg.Warnings, "config.json unparsable")
		return cfg
	}
	for key := range top {
		if !knownTop(key) {
			cfg.Warnings = append(cfg.Warnings, "unknown config key "+key)
		}
	}
	raw, ok := top["loop"]
	if !ok {
		return cfg
	}
	var loop map[string]json.RawMessage
	if err := json.Unmarshal(raw, &loop); err != nil {
		cfg.Warnings = append(cfg.Warnings, "loop config unparsable")
		return cfg
	}
	known := map[string]bool{
		"rework_limit": true, "transport_retries": true, "retry_backoff_seconds": true,
		"breaker_consecutive": true, "breaker_same_error": true, "breaker_repair": true,
		"breaker_stall_hours": true, "breaker_spend_tokens": true, "budgets": true,
	}
	for key := range loop {
		if !known[key] {
			cfg.Warnings = append(cfg.Warnings, "unknown loop key "+key)
		}
	}
	if n, ok := intFrom(loop, "rework_limit"); ok && n > 0 {
		cfg.ReworkLimit = n
	}
	if n, ok := intFrom(loop, "transport_retries"); ok && n > 0 {
		cfg.TransportRetries = n
	}
	if n, ok := intFrom(loop, "retry_backoff_seconds"); ok && n >= 0 {
		cfg.RetryBackoff = time.Duration(n) * time.Second
	}
	if n, ok := intFrom(loop, "breaker_consecutive"); ok && n > 0 {
		cfg.BreakerConsecutive = n
	}
	if n, ok := intFrom(loop, "breaker_same_error"); ok && n > 0 {
		cfg.BreakerSameError = n
	}
	if n, ok := intFrom(loop, "breaker_repair"); ok && n > 0 {
		cfg.BreakerRepair = n
	}
	if n, ok := intFrom(loop, "breaker_stall_hours"); ok {
		if n <= 0 {
			cfg.BreakerStall = 0
		} else {
			cfg.BreakerStall = time.Duration(n) * time.Hour
		}
	}
	if n, ok := int64From(loop, "breaker_spend_tokens"); ok {
		cfg.BreakerSpend = n
	}
	budgetsRaw, ok := loop["budgets"]
	if !ok {
		return cfg
	}
	var budgets map[string]json.RawMessage
	if err := json.Unmarshal(budgetsRaw, &budgets); err != nil {
		cfg.Warnings = append(cfg.Warnings, "budgets unparsable")
		return cfg
	}
	designSet := false
	researchSet := false
	for key, raw := range budgets {
		kind := model.CardKind(key)
		if !kindKnown(kind) {
			cfg.Warnings = append(cfg.Warnings, "unknown budget kind "+key)
			continue
		}
		var b model.Budget
		if err := json.Unmarshal(raw, &b); err != nil {
			cfg.Warnings = append(cfg.Warnings, "budget "+key+" unparsable")
			continue
		}
		cfg.Budgets[kind] = b
		if kind == model.KindDesign {
			designSet = true
		}
		if kind == model.KindResearch {
			researchSet = true
		}
	}
	if designSet && !researchSet {
		cfg.Budgets[model.KindResearch] = quarter(cfg.Budgets[model.KindDesign])
	}
	return cfg
}

func knownTop(key string) bool {
	switch key {
	case "loop", "positioning", "engines", "unknown_quota", "host":
		return true
	default:
		return false
	}
}

func kindKnown(kind model.CardKind) bool {
	for _, k := range model.AllCardKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

func intFrom(m map[string]json.RawMessage, key string) (int, bool) {
	n, ok := int64From(m, key)
	return int(n), ok
}

func int64From(m map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

// AttemptFact is one attempt used by breaker evaluation.
type AttemptFact struct {
	ID       string
	Card     string
	Kind     model.CardKind
	Outcome  model.Outcome
	Class    model.ErrorClass
	Excerpt  string
	Tokens   int64
	At       time.Time
	RepairOf string
}

// NodeCard is the card status breaker evaluation needs.
type NodeCard struct {
	ID     string
	Status model.CardStatus
	Latest time.Time
}

// BreakerInput is one node's breaker check.
// MergeAt is the latest persisted merge-success time on the node.
// A merged status with no such time is not a merge time.
type BreakerInput struct {
	Attempts []AttemptFact
	Cards    []NodeCard
	Spend    int64
	Now      time.Time
	Config   Config
	MergeAt  time.Time
}

// EvaluateBreaker reports the first matching design §6.5 condition.
func EvaluateBreaker(in BreakerInput) (string, bool) {
	cfg := in.Config
	if cfg.BreakerConsecutive <= 0 {
		cfg.BreakerConsecutive = 3
	}
	if cfg.BreakerSameError <= 0 {
		cfg.BreakerSameError = 2
	}
	if cfg.BreakerRepair <= 0 {
		cfg.BreakerRepair = 2
	}
	facts := append([]AttemptFact{}, in.Attempts...)
	sortFacts(facts)
	if n := trailingFailures(facts); n >= cfg.BreakerConsecutive {
		return fmt.Sprintf("consecutive failures %d", n), true
	}
	if sig, n := repeatedSignature(facts, cfg.BreakerSameError); n >= cfg.BreakerSameError {
		return "repeated error " + sig, true
	}
	if parent, n := repairStreak(facts, cfg.BreakerRepair); n >= cfg.BreakerRepair {
		return fmt.Sprintf("repair streak %d on %s", n, parent), true
	}
	if stalled(in, cfg) {
		return "no merge within stall window", true
	}
	return "", false
}

func sortFacts(facts []AttemptFact) {
	for i := 1; i < len(facts); i++ {
		j := i
		for j > 0 && factBefore(facts[j], facts[j-1]) {
			facts[j], facts[j-1] = facts[j-1], facts[j]
			j--
		}
	}
}

func factBefore(a, b AttemptFact) bool {
	if a.At.Equal(b.At) {
		return a.ID < b.ID
	}
	if a.At.IsZero() {
		return false
	}
	if b.At.IsZero() {
		return true
	}
	return a.At.Before(b.At)
}

func trailingFailures(facts []AttemptFact) int {
	n := 0
	for i := len(facts) - 1; i >= 0; i-- {
		if !countsAsFailure(facts[i].Outcome) {
			if facts[i].Outcome == model.OutcomePassed {
				break
			}
			continue
		}
		n++
	}
	return n
}

func countsAsFailure(outcome model.Outcome) bool {
	switch outcome {
	case model.OutcomeTestsFailed, model.OutcomeNoOutput, model.OutcomeResultUnparsable,
		model.OutcomePermissionDenied, model.OutcomeRefusal, model.OutcomeBudgetLimited, model.OutcomeViolation:
		return true
	default:
		return false
	}
}

func repeatedSignature(facts []AttemptFact, threshold int) (string, int) {
	counts := map[string]int{}
	bestN := 0
	best := ""
	for _, fact := range facts {
		if fact.Outcome == "" || fact.Outcome == model.OutcomePassed {
			continue
		}
		sig := signature(fact)
		if sig == "" {
			continue
		}
		counts[sig]++
		if counts[sig] > bestN || (counts[sig] == bestN && sig < best) {
			bestN = counts[sig]
			best = sig
		}
	}
	if bestN < threshold {
		return "", bestN
	}
	return best, bestN
}

func signature(fact AttemptFact) string {
	text := strings.ToLower(strings.Join(strings.Fields(fact.Excerpt), " "))
	if text != "" {
		return text
	}
	return string(fact.Class)
}

func repairStreak(facts []AttemptFact, threshold int) (string, int) {
	groups := map[string][]AttemptFact{}
	for _, fact := range facts {
		if fact.Kind != model.KindRepair {
			continue
		}
		parent := fact.RepairOf
		if parent == "" {
			parent = fact.Card
		}
		groups[parent] = append(groups[parent], fact)
	}
	bestParent := ""
	bestN := 0
	for parent, list := range groups {
		sortFacts(list)
		n := 0
		for i := len(list) - 1; i >= 0; i-- {
			if list[i].Outcome == model.OutcomePassed {
				break
			}
			if list[i].Outcome != model.OutcomeTestsFailed {
				continue
			}
			n++
		}
		if n > bestN {
			bestN = n
			bestParent = parent
		}
	}
	if bestN < threshold {
		return "", bestN
	}
	return bestParent, bestN
}

func stalled(in BreakerInput, cfg Config) bool {
	if cfg.BreakerStall <= 0 || cfg.BreakerSpend <= 0 || in.Spend <= cfg.BreakerSpend {
		return false
	}
	var first time.Time
	for _, fact := range in.Attempts {
		if fact.At.IsZero() {
			continue
		}
		if first.IsZero() || fact.At.Before(first) {
			first = fact.At
		}
	}
	// A merge-success time is the only merge clock. Attempt end is not one.
	// With no merge-success time, including a merged card that never recorded
	// one, the anchor stays the first attempt.
	anchor := first
	if !in.MergeAt.IsZero() {
		anchor = in.MergeAt
	}
	if anchor.IsZero() || in.Now.IsZero() {
		return false
	}
	return in.Now.Sub(anchor) >= cfg.BreakerStall
}

// SoftHit reports spend at or above 80% of a configured soft line.
func SoftHit(spend, soft int64) bool {
	if soft <= 0 {
		return false
	}
	return spend*5 >= soft*4
}

// HardHit reports spend at or above a configured hard line.
func HardHit(spend, hard int64) bool {
	if hard <= 0 {
		return false
	}
	return spend >= hard
}

// TerminalCard is a status that no longer advances for replan or breaker pause.
func TerminalCard(status model.CardStatus) bool {
	switch status {
	case model.CardMerged, model.CardDone, model.CardFailed, model.CardCanceled, model.CardNeedsInput:
		return true
	default:
		return false
	}
}
