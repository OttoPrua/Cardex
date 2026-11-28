package host

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
)

func comparisonTargetReady(card model.Card, attempt model.Attempt, ch model.Channel) bool {
	if !card.ValidStrictComparison() || !attempt.StrictComparison() ||
		(card.Status != model.CardReady && card.Status != model.CardRunning) {
		return false
	}
	d := attempt.RouteReason.Dispatch
	_, digest := card.ComparisonInput()
	return digest != "" && d.ComparisonInputDigest == digest && d.Runner == model.RunnerClaude && ch.Runner == model.RunnerClaude &&
		attempt.Route.Channel == ch.ID && ch.ID == d.Channel && d.Channel == card.Channel &&
		attempt.Route.Model == d.Model && d.Model == card.Model && slices.Contains(ch.Models, card.Model) &&
		ch.Health == model.HealthOK && d.CompatibleBindingID != "" &&
		d.CompatibleBindingID == runner.CompatibleBindingID(ch.EngineProfile)
}

func refuseDisabledComparison(ctx context.Context, db *store.Store, card model.Card, attempt model.Attempt) error {
	if !card.ValidStrictComparison() || !attempt.StrictComparison() {
		return nil
	}
	obj, err := db.GetObjective(ctx, card.Objective)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return db.RefuseDisabledComparison(ctx, card, attempt, obj, time.Now())
}

// Only the synthetic comparison adapter reaches this path. Default runtime
// refusal uses the separate pristine no-wire transaction above.
func stopHostComparison(ctx context.Context, db *store.Store, attempt *model.Attempt, card *model.Card, reason string) error {
	pctx, cancel := persistContext(ctx)
	defer cancel()
	attempt.EndedAt = time.Now().UTC()
	attempt.Outcome = model.OutcomeUnavailable
	attempt.ErrorClass = model.ErrorUnavailable
	attempt.ErrorExcerpt = trimExcerpt(reason)
	code := 1
	attempt.Exit = &code
	err := db.WithTx(pctx, func(ctx context.Context) error {
		fresh, err := db.GetCard(ctx, card.ID)
		if err != nil {
			return err
		}
		if fresh.Status == model.CardRunning || fresh.Status == model.CardReady || fresh.Status == model.CardHarvesting {
			fresh.Status = model.CardNeedsInput
			fresh.WaitReason = attempt.ErrorExcerpt
			if err := db.UpdateCard(ctx, &fresh); err != nil {
				return err
			}
		}
		return db.UpdateAttempt(ctx, attempt)
	})
	if err == nil {
		appendEvent(pctx, db, attempt.ID, "strict_comparison_refused", map[string]string{"reason": attempt.ErrorExcerpt})
	}
	return err
}
