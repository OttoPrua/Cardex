package loop

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

const objectiveComparisonExemptionEvent = "objective_optional_comparison_completion"

type comparisonExemption struct {
	Question string   `json:"question_id"`
	Cards    []string `json:"comparison_cards"`
}

// comparisonCompletionReceipt returns the original approved question and
// every card that was excluded from that exact candidate. An ambiguous
// marked receipt does not fall back to legacy approval behavior.
func (s *snap) comparisonCompletionReceipt() (exemption comparisonExemption, marked bool) {
	for i := len(s.events) - 1; i >= 0; i-- {
		ev := s.events[i]
		if ev.Entity != "objective:"+s.obj.ID || ev.Type != objectiveComparisonExemptionEvent {
			continue
		}
		if json.Unmarshal(ev.Data, &exemption) != nil || strings.TrimSpace(exemption.Question) == "" {
			return comparisonExemption{}, true
		}
		seen := make(map[string]bool, len(exemption.Cards))
		for _, id := range exemption.Cards {
			if id == "" || strings.TrimSpace(id) != id || seen[id] {
				return comparisonExemption{}, true
			}
			seen[id] = true
		}
		return exemption, true
	}
	return comparisonExemption{}, false
}

// doneApproval preserves the old question selection for ordinary objectives.
// A marked objective instead keeps its exact durable candidate question.
func (s *snap) doneApproval() (question model.Question, gated bool) {
	if exemption, marked := s.comparisonCompletionReceipt(); marked {
		for _, q := range s.questions {
			if q.ID == exemption.Question && q.Kind == model.QuestionDone && q.Objective == s.obj.ID &&
				q.Scope == s.obj.ID && q.ScopeKind == "objective" {
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
	events, err := db.ListEvents(ctx, "objective:"+obj.ID)
	if err != nil {
		return err
	}
	hint := snap{obj: obj, events: events}
	exemption, marked := hint.comparisonCompletionReceipt()
	if !marked || exemption.Question == "" {
		return nil
	}
	q, err := db.GetQuestion(ctx, exemption.Question)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	hint.questions = []model.Question{q}
	approval, _ := hint.doneApproval()
	if !approved(approval.Answer) {
		return nil
	}
	return db.WithWriteTx(ctx, func(ctx context.Context) error {
		fresh, err := loadSnap(ctx, db, obj.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		q, stillMarked := fresh.doneApproval()
		if fresh.obj.Mode != model.ModeAuto || fresh.obj.Status != model.ObjectiveDoneCandidate ||
			!stillMarked || q.ID != approval.ID || !approved(q.Answer) {
			return nil
		}
		// Approval is the only continuation, not another design/development
		// pass. The reservation is live through the graph check and Git.
		st := &step{ctx: ctx, db: db, home: db.Home(), snap: fresh, now: clockFrom(ctx), completionReserved: true}
		return st.complete()
	})
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
			question, err := st.db.FindPendingCardQuestion(st.ctx, id)
			if err != nil || question != nil {
				return false, err
			}
			continue
		}
		if err != nil || c.Objective != st.snap.obj.ID || !c.ValidStrictComparison() {
			return false, err
		}
	}
	return true, nil
}

func hardHeldComparison(c model.Card) bool {
	return c.Status == model.CardNeedsInput && c.StrictComparison() &&
		(c.WaitReason == model.CompatibleRuntimeHold || c.WaitReason == store.StrictComparisonDisabled)
}

// optionalHeldComparison only exempts the isolated capability's known
// no-execution hold. The card stays NeedsInput and visible with its reason;
// this is not a status change, a satisfied dependency, or a comparison result.
func (st *step) optionalHeldComparison(observed model.Card) (bool, error) {
	if !hardHeldComparison(observed) {
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
	question, err := st.db.FindPendingCardQuestion(st.ctx, c.ID)
	if err != nil || question != nil {
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
