package loop

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

const objectiveComparisonExemptionEvent = "objective_optional_comparison_completion"

type comparisonExemption struct {
	Question   string                   `json:"question_id"`
	Cards      []string                 `json:"comparison_cards"`
	Acceptance *comparisonAcceptancePin `json:"acceptance,omitempty"`
	Supersedes string                   `json:"supersedes_question_id,omitempty"`
}

// comparisonCompletionReceipt follows explicit supersession rather than
// timestamps, whose clock may repeat or be reset. An ambiguous marked
// history does not fall back to legacy approval behavior.
func (s *snap) comparisonCompletionReceipt() (comparisonExemption, bool) {
	history := map[string]comparisonExemption{}
	for _, ev := range s.events {
		if ev.Entity != "objective:"+s.obj.ID || ev.Type != objectiveComparisonExemptionEvent {
			continue
		}
		var r comparisonExemption
		if json.Unmarshal(ev.Data, &r) != nil || strings.TrimSpace(r.Question) == "" ||
			r.Question == r.Supersedes || history[r.Question].Question != "" || (r.Acceptance != nil && !r.Acceptance.valid()) {
			return comparisonExemption{}, true
		}
		seen := make(map[string]bool, len(r.Cards))
		for _, id := range r.Cards {
			if !comparisonReceiptCardID(id) || seen[id] {
				return comparisonExemption{}, true
			}
			seen[id] = true
		}
		history[r.Question] = r
	}
	if len(history) == 0 {
		return comparisonExemption{}, false
	}
	superseded := map[string]bool{}
	for _, r := range history {
		if r.Supersedes == "" {
			continue
		}
		if superseded[r.Supersedes] || history[r.Supersedes].Question == "" {
			return comparisonExemption{}, true
		}
		superseded[r.Supersedes] = true
	}
	var current comparisonExemption
	for _, r := range history {
		if !superseded[r.Question] {
			if current.Question != "" {
				return comparisonExemption{}, true
			}
			current = r
		}
	}
	remaining := len(history)
	for at := current; at.Question != ""; at = history[at.Supersedes] {
		remaining--
		if remaining < 0 {
			return comparisonExemption{}, true
		}
	}
	if remaining != 0 {
		return comparisonExemption{}, true
	}
	return current, true
}

// doneApproval preserves the old question selection for ordinary objectives.
// A marked objective instead keeps its exact durable candidate question.
func (s *snap) doneApproval() (question model.Question, gated bool) {
	if exemption, marked := s.comparisonCompletionReceipt(); marked {
		for _, q := range s.questions {
			if q.ID == exemption.Question && q.Kind == model.QuestionDone && q.Objective == s.obj.ID &&
				q.Scope == s.obj.ID && q.ScopeKind == "objective" &&
				(exemption.Acceptance == nil || q.Text == exemption.Acceptance.question()) {
				return q, true
			}
		}
		return question, true
	}
	for _, q := range s.questions {
		if q.Kind == model.QuestionDone && q.Objective == s.obj.ID {
			question = q
		}
	}
	return question, false
}

// ResumeOptionalComparisonCandidate is the scheduler-only continuation
// for an automatic candidate that used this exemption. The preflight sees
// only the exact durable question; a held/unapproved candidate has no side
// effects, and an ordinary historical candidate is not opted in.
func ResumeOptionalComparisonCandidate(ctx context.Context, db *store.Store, obj model.Objective) error {
	if obj.Mode != model.ModeAuto || obj.Status != model.ObjectiveDoneCandidate {
		return nil
	}
	return runOptionalComparisonCandidate(ctx, db, obj, true)
}

// priorComparisonCardsSafe checks receipt identities even after a card
// leaves the objective inventory. Only proven deletion with no remaining
// incoming graph edge or pending card decision is harmless. A moved or
// repurposed row is not proof about the original comparison.
func (st *step) priorComparisonCardsSafe(exemption comparisonExemption) (bool, error) {
	for _, id := range exemption.Cards {
		c, err := st.db.GetCard(st.ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			linked, err := st.db.CardHasDependencyReference(st.ctx, id)
			if err != nil || linked {
				return false, err
			}
			pending, err := st.db.CardHasPendingCompletionDecision(st.ctx, id)
			if err != nil || pending {
				return false, err
			}
			continue
		}
		if err != nil || c.Objective != st.snap.obj.ID || !c.ValidStrictComparison() {
			return false, err
		}
		if c.Status != model.CardDone && c.Status != model.CardMerged {
			linked, err := st.db.CardHasStrictExecutionReference(st.ctx, id)
			if err != nil || linked {
				return false, err
			}
		}
	}
	return true, nil
}

// Store accepts raw ID strings. Preserve their exact bytes in the receipt,
// but do not offer approval for an identity JSON cannot round-trip exactly.
func comparisonReceiptCardID(id string) bool {
	return strings.TrimSpace(id) != "" && utf8.ValidString(id) && !strings.ContainsRune(id, '\x00')
}

func hardHeldComparison(c model.Card) bool {
	return c.Status == model.CardNeedsInput && c.StrictComparison() &&
		(c.WaitReason == model.CompatibleRuntimeHold || c.WaitReason == store.StrictComparisonDisabled)
}

// optionalHeldComparison only exempts the isolated capability's known
// no-execution hold. The card stays NeedsInput and visible with its reason;
// this is not a status change, a satisfied dependency, or a comparison result.
func (st *step) optionalHeldComparison(observed model.Card) (bool, error) {
	if !comparisonReceiptCardID(observed.ID) || !hardHeldComparison(observed) {
		return false, nil
	}
	c, err := st.db.GetCard(st.ctx, observed.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if c.Objective != st.snap.obj.ID || !hardHeldComparison(c) || !c.ValidStrictComparison() ||
		len(c.Deps) != 0 || len(c.Claims) != 0 || len(c.Acceptance) != 0 || c.WaitQuestionID != "" {
		return false, nil
	}
	linked, err := st.db.CardHasDependencyReference(st.ctx, c.ID)
	if err != nil || linked {
		return false, err
	}
	pending, err := st.db.CardHasPendingCompletionDecision(st.ctx, c.ID)
	if err != nil || pending {
		return false, err
	}
	attempts, err := st.db.ListAttempts(st.ctx, c.ID)
	if err != nil {
		return false, err
	}
	// A current-build card can be held before any attempt exists. A
	// historical prepared attempt instead needs its typed no-wire record.
	if len(attempts) == 0 {
		return c.WaitReason == model.CompatibleRuntimeHold, nil
	}
	_, input := c.ComparisonInput()
	for _, a := range attempts {
		if !a.StrictComparison() || a.Card != c.ID || a.Route != (model.Route{Channel: c.Channel, Model: c.Model}) ||
			a.RouteReason.Dispatch.Channel != c.Channel || a.RouteReason.Dispatch.Model != c.Model ||
			a.RouteReason.Dispatch.ComparisonInputDigest != input || a.RouteReason.Dispatch.Runner != model.RunnerClaude ||
			a.PID != 0 || !a.StartedAt.IsZero() || a.EndedAt.IsZero() || a.Exit != nil || a.HeadCommit != "" || a.Usage != (model.Usage{}) ||
			a.RouteReason.ProviderModel != nil || a.Outcome != model.OutcomeUnavailable || a.ErrorClass != model.ErrorUnavailable ||
			a.ErrorExcerpt != store.StrictComparisonDisabled {
			return false, nil
		}
		ev, err := st.db.GetEvent(st.ctx, store.EventComparisonRefusedNoWire+":"+a.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if ev.Entity != "attempt:"+a.ID || ev.Type != store.EventComparisonRefusedNoWire ||
			!bytes.Equal(ev.Data, []byte(`{"status":"approval_unavailable","attempted":false}`)) {
			return false, nil
		}
	}
	return true, nil
}
