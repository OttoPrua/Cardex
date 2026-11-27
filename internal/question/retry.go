package question

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/route"
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
	return queueRetry(ctx, db, id, option, "")
}

// RetryApprovedAutoRoute consumes a previously answered, exact current
// scheduler offer. Bare recovery is not approval and cannot target a stale
// question. Use AnswerAndRetry for the combined, unanswered form.
func RetryApprovedAutoRoute(ctx context.Context, db *store.Store, cardID string) (RetryResult, error) {
	if cardID == "" {
		return RetryResult{}, retryError("a card ID is required")
	}
	return queueRetry(ctx, db, "", "approve", cardID)
}

func queueRetry(ctx context.Context, db *store.Store, id, option, approvedCard string) (RetryResult, error) {
	var result RetryResult
	err := db.WithWriteTx(ctx, func(ctx context.Context) error {
		if approvedCard != "" {
			c, err := db.GetCard(ctx, approvedCard)
			if err != nil {
				return err
			}
			if c.WaitQuestionID == "" {
				return retryError("there is no current retry decision to recover")
			}
			id = c.WaitQuestionID
		}
		q, err := db.GetQuestion(ctx, id)
		if err != nil {
			return err
		}
		if option != "approve" || !containsOption(q.Options, option) {
			return retryError("select the stored approve option to queue a retry")
		}
		if approvedCard == "" && q.Answer != "" {
			return retryError("the question is already answered")
		}
		if approvedCard != "" && (q.Answer != option || q.Scope != approvedCard) {
			return retryError("the current card retry has not been approved")
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
		pin, err := recoverPin(ctx, db, c, obj, latest, q.ID, approvedCard != "")
		if err != nil {
			return err
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

		if approvedCard == "" {
			if _, err := New(db, nil).Answer(ctx, q.ID, option); err != nil {
				return err
			}
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
		if pin.Route.Channel != "" {
			ev, err := route.RecoveryPinEvent(c.ID, pin)
			if err != nil {
				return err
			}
			if err := db.AppendEvent(ctx, &ev); err != nil {
				return err
			}
			c.Channel, c.Model = pin.Route.Channel, pin.Route.Model
		}
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

// recoverPin checks the precise previous fallback, not historical questions or
// terminal attempts on the same channel from an earlier switch generation.
func recoverPin(ctx context.Context, db *store.Store, c model.Card, obj model.Objective, latest model.Attempt, questionID string, requireAuto bool) (route.RecoveryAuthorization, error) {
	events, err := db.ListEvents(ctx, "card:"+c.ID)
	if err != nil {
		return route.RecoveryAuthorization{}, err
	}
	auto := route.LatestAutoSelection(events, c.ID)
	if auto.Channel == "" || c.Channel != auto.Channel {
		if requireAuto {
			return route.RecoveryAuthorization{}, retryError("the card is not on the current automatic route")
		}
		return route.RecoveryAuthorization{}, nil
	}
	if auto.EventID == "" || latest.Route.Channel != auto.Channel ||
		latest.StartedAt.IsZero() || latest.StartedAt.Before(auto.At) {
		return route.RecoveryAuthorization{}, retryError("the approved attempt is not from the current automatic switch")
	}
	block, exists := route.FallbackRetryBlockFor(c, events)
	if !exists || block.Question != questionID || block.Attempt != latest.ID {
		return route.RecoveryAuthorization{}, retryError("this question no longer authorizes the current automatic switch")
	}
	channelID, modelName, ok := route.RestartTarget(c, obj, events, auto.Channel)
	if !ok {
		return route.RecoveryAuthorization{}, retryError("there is no recorded channel to recover")
	}
	channel, err := db.GetChannel(ctx, channelID)
	if err != nil || channel.Health != model.HealthOK || (modelName != "" && !slices.Contains(channel.Models, modelName)) {
		return route.RecoveryAuthorization{}, retryError("the recorded recovery channel or model is unavailable")
	}
	return route.RecoveryAuthorization{Route: model.Route{Channel: channelID, Model: modelName}, Question: questionID, Attempt: latest.ID, AutoRoute: auto.EventID}, nil
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
