package sched

import (
	"context"
	"encoding/json"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/route"
	"github.com/OttoPrua/Cardex/internal/store"
)

// recoveryTarget consumes only an explicit typed route recovery written by the
// operator retry transaction. Historical questions cannot restore a channel.
func (r *Runner) recoveryTarget(ctx context.Context, db *store.Store, card model.Card, attempts []model.Attempt, channels []model.Channel, quotas route.Quotas) (model.Route, bool, error) {
	events, err := db.ListEvents(ctx, "card:"+card.ID)
	if err != nil {
		return model.Route{}, false, err
	}
	target, ok := route.RecoveryPin(card, events)
	if !ok {
		if block, blocked := route.FallbackRetryBlockFor(card, events); blocked {
			var latest model.Attempt
			for _, attempt := range attempts {
				if latest.ID == "" || attempt.Index > latest.Index {
					latest = attempt
				}
			}
			if latest.ID == block.Attempt && latest.Outcome != "" && !latest.EndedAt.IsZero() {
				card.Status = model.CardNeedsInput
				card.WaitQuestionID = block.Question
				return model.Route{}, true, db.UpdateCard(ctx, &card)
			}
		}
		return model.Route{}, false, nil
	}
	bad, _, err := channelUnusable(ctx, target.Channel, channels, quotas, r.now())
	if err != nil {
		return target, false, err
	}
	channel, found := channelByID(channels, target.Channel)
	if bad || !found || (target.Model != "" && !channelOffers(channel, target.Model)) {
		return target, true, r.lockAutoRouteRestart(ctx, db, &card)
	}
	return target, false, nil
}

func (r *Runner) lockAutoRouteRestart(ctx context.Context, db *store.Store, card *model.Card) error {
	card.Status = model.CardNeedsInput
	card.WaitReason = "repair recovery: no usable recorded channel"
	return db.UpdateCard(ctx, card)
}

func repairHint(ctx context.Context, db *store.Store, card model.Card) (model.RepairHint, error) {
	if card.Kind != model.KindRepair && card.Kind != model.KindRebase {
		return "", nil
	}
	events, err := db.ListEvents(ctx, "card:"+card.ID)
	if err != nil {
		return "", err
	}
	var hint model.RepairHint
	for _, event := range events {
		if event.Type != model.EventRepairHint {
			continue
		}
		var data struct {
			Code model.RepairHint `json:"code"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.Code.PromptText() != "" {
			hint = data.Code
		}
	}
	return hint, nil
}
