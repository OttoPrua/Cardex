package question

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// RetryResult is the compact acknowledgment of an answer and a queued retry.
type RetryResult struct {
	Question string           `json:"question"`
	Answer   string           `json:"answer"`
	Card     string           `json:"card"`
	Status   model.CardStatus `json:"status"`
}

// AnswerAndRetry records approval and returns a card to the scheduler in one
// transaction. It does not bypass routing, authorization, or budget checks, or
// start an executor itself.
func AnswerAndRetry(ctx context.Context, db *store.Store, id, option string) (RetryResult, error) {
	var result RetryResult
	err := db.WithTx(ctx, func(ctx context.Context) error {
		q, err := db.GetQuestion(ctx, id)
		if err != nil {
			return err
		}
		if option != "approve" || !containsOption(q.Options, option) {
			return retryError("select the stored approve option to queue a retry")
		}
		if q.Answer != "" {
			return retryError("the question is already answered")
		}
		if q.ScopeKind != "card" || q.Kind != model.QuestionCustom || q.Scope == "" || q.Objective == "" {
			return retryError("this is not a card retry question")
		}
		c, err := db.GetCard(ctx, q.Scope)
		if err != nil {
			return err
		}
		if c.Objective != q.Objective || c.Status != model.CardNeedsInput || c.WaitQuestionID != q.ID || c.WaitReason != "" ||
			!c.Kind.CanRetryTransient() || readOnlyHistory(c.Legacy.Status) {
			return retryError("the question is not the current retry blocker for this card")
		}
		obj, err := db.GetObjective(ctx, c.Objective)
		if err != nil {
			return err
		}
		if obj.Status != model.ObjectiveActive {
			return retryError("the objective is not active")
		}
		if c.Node != "" {
			node, err := db.GetNode(ctx, c.Node)
			if err != nil {
				return err
			}
			if node.Objective != obj.ID || node.Status != model.NodeFrozen {
				return retryError("the card is not on a frozen node in this objective")
			}
		}
		attempts, err := db.ListAttempts(ctx, c.ID)
		if err != nil {
			return err
		}
		latest, ok := latestSettledAttempt(attempts)
		if !ok {
			return retryError("there is no settled transient failure to retry")
		}
		offered, err := hasRetryOffer(ctx, db, q.ID, c.ID, latest)
		if err != nil {
			return err
		}
		if !offered {
			return retryError("the scheduler did not offer a retry for the latest transient failure")
		}
		questions, err := db.ListQuestions(ctx, obj.ID)
		if err != nil {
			return err
		}
		for _, other := range questions {
			if other.Kind == model.QuestionBudget && other.Scope == obj.ID && strings.TrimSpace(other.Answer) == "" {
				return retryError("the objective has a pending budget decision")
			}
		}
		atLimit, err := db.ObjectiveTokenLimitReached(ctx, obj.ID, obj.Budget.HardTokens)
		if err != nil {
			return err
		}
		if atLimit {
			return retryError("the objective has reached its hard token budget")
		}

		if _, err := New(db, nil).Answer(ctx, q.ID, option); err != nil {
			return err
		}
		// The cross-objective check comes after recording this answer so the
		// pending lookup can't just return the question we're clearing. This
		// temporary answer is rolled back if another blocker exists.
		if other, err := db.FindPendingCardQuestion(ctx, c.ID); err != nil {
			return err
		} else if other != nil {
			return retryError("the card has another pending question")
		}
		c.Status = model.CardReady
		if err := db.UpdateCard(ctx, &c); err != nil {
			return err
		}
		data, err := json.Marshal(struct {
			Question string `json:"question"`
		}{Question: q.ID})
		if err != nil {
			return err
		}
		if err := db.AppendEvent(ctx, &model.Event{Entity: "card:" + c.ID, Type: "question_retry", Data: data}); err != nil {
			return err
		}
		result = RetryResult{Question: q.ID, Answer: option, Card: c.ID, Status: c.Status}
		return nil
	})
	if err != nil {
		return RetryResult{}, err
	}
	return result, nil
}

func retryError(reason string) error {
	return fmt.Errorf("question --retry: %s; no answer or retry was recorded", reason)
}

func readOnlyHistory(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "failed", "canceled", "unparsed":
		return true
	default:
		return false
	}
}

func latestSettledAttempt(attempts []model.Attempt) (model.Attempt, bool) {
	var last model.Attempt
	seen := make(map[int]bool, len(attempts))
	for _, a := range attempts {
		if a.EndedAt.IsZero() || a.Outcome == "" || seen[a.Index] {
			return model.Attempt{}, false
		}
		seen[a.Index] = true
		if last.ID == "" || a.Index > last.Index {
			last = a
		}
	}
	return last, last.ID != ""
}

func hasRetryOffer(ctx context.Context, db *store.Store, questionID, cardID string, latest model.Attempt) (bool, error) {
	events, err := db.ListEvents(ctx, "question:"+questionID)
	if err != nil {
		return false, err
	}
	var matched bool
	for _, event := range events {
		if event.Type != model.EventCardRetryOffered {
			continue
		}
		// There is exactly one offer per newly issued question.
		var offer model.CardRetryOffer
		if matched || json.Unmarshal(event.Data, &offer) != nil ||
			offer.Card != cardID || offer.Attempt != latest.ID || !offer.Reason.Permits(latest) {
			return false, nil
		}
		matched = true
	}
	return matched, nil
}
