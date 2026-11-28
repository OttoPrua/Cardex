package sched

import (
	"context"
	"reflect"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// A comparison without trustworthy evidence never reaches generic automatic
// disposition or any score/result processing.
func (r *Runner) stopComparison(ctx context.Context, db *store.Store, card *model.Card, attempt *model.Attempt, why string) error {
	return db.WithWriteTx(ctx, func(ctx context.Context) error {
		freshAttempt, err := db.GetAttempt(ctx, attempt.ID)
		if err != nil || freshAttempt.Outcome != "" || !reflect.DeepEqual(freshAttempt, *attempt) {
			return err
		}
		freshCard, err := db.GetCard(ctx, card.ID)
		if err != nil {
			return err
		}
		if !freshCard.ValidStrictComparison() || !freshAttempt.StrictComparison() {
			return nil
		}
		if reflect.DeepEqual(freshCard, *card) && (canDispatch(freshCard) || freshCard.Status == model.CardHarvesting) {
			attempts, err := db.ListAttempts(ctx, card.ID)
			if err != nil {
				return err
			}
			current := true
			for _, seen := range attempts {
				if seen.ID != attempt.ID && seen.Index >= attempt.Index {
					current = false
					break
				}
			}
			if current {
				freshCard.Status = model.CardNeedsInput
				freshCard.WaitReason = why
				if err := db.UpdateCard(ctx, &freshCard); err != nil {
					return err
				}
			}
		}
		freshAttempt.Outcome = model.OutcomeUnavailable
		freshAttempt.ErrorClass = model.ErrorUnavailable
		freshAttempt.ErrorExcerpt = why
		if freshAttempt.EndedAt.IsZero() {
			freshAttempt.EndedAt = r.now().UTC()
		}
		return db.UpdateAttempt(ctx, &freshAttempt)
	})
}
