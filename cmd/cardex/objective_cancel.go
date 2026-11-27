package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/route"
	"github.com/OttoPrua/Cardex/internal/store"
)

type objectiveCancelReceipt struct {
	Objective string                `json:"objective"`
	Status    model.ObjectiveStatus `json:"status"`
	Cards     []string              `json:"canceled_cards"`
}

// cascadeObjectiveCancel persists scoped state and its audit in one reserved
// write transaction. It neither signals workers nor changes another objective.
func cascadeObjectiveCancel(ctx context.Context, db *store.Store, id string) (objectiveCancelReceipt, error) {
	receipt := objectiveCancelReceipt{Objective: id, Status: model.ObjectiveCanceled, Cards: []string{}}
	err := db.WithWriteTx(ctx, func(tx context.Context) error {
		o, err := db.GetObjective(tx, id)
		if err != nil {
			return err
		}
		if o.Status == model.ObjectiveDone {
			return fmt.Errorf("a completed objective cannot be cascade canceled")
		}
		cards, err := db.ListCards(tx, id)
		if err != nil {
			return err
		}
		if cardID, err := db.ObjectiveOpenAttemptCard(tx, id); err != nil {
			return err
		} else if cardID != "" {
			return fmt.Errorf("card %s still has an unfinished attempt; wait until it settles", cardID)
		}
		var canceled []model.Card
		for _, card := range cards {
			switch card.Status {
			case model.CardRunning, model.CardHarvesting, model.CardMergeQueued:
				return fmt.Errorf("card %s still has %s work; wait until it settles", card.ID, card.Status)
			case model.CardDone, model.CardMerged, model.CardCanceled, model.CardFailed:
				continue
			case model.CardReady, model.CardWaiting, model.CardNeedsInput, model.CardPaused, model.CardReviewing:
				if !legacyReadOnly(card.Legacy.Status) {
					canceled = append(canceled, card)
				}
			default:
				return fmt.Errorf("card %s has an unknown state", card.ID)
			}
		}
		for _, card := range canceled {
			if card.Status == model.CardNeedsInput {
				events, err := db.ListEvents(tx, "card:"+card.ID)
				if err != nil {
					return err
				}
				auto := route.LatestAutoSelection(events, card.ID)
				if auto.EventID != "" && auto.Channel != "" && card.Channel == auto.Channel && !route.InterruptedRecoveryHeld(card, events) {
					hold, err := route.HoldInterruptedRecovery(card.ID, auto.EventID)
					if err != nil {
						return err
					}
					if err := db.AppendEvent(tx, &hold); err != nil {
						return err
					}
				}
			}
			before := card.Status
			card.Status = model.CardCanceled
			card.WaitReason = "objective cascade canceled"
			if err := db.UpdateCard(tx, &card); err != nil {
				return err
			}
			raw, err := json.Marshal(struct {
				Objective string           `json:"objective"`
				Before    model.CardStatus `json:"before"`
			}{id, before})
			if err != nil {
				return err
			}
			if err := db.AppendEvent(tx, &model.Event{Entity: "card:" + card.ID, Type: "objective_cascade_canceled_card", Data: raw}); err != nil {
				return err
			}
			receipt.Cards = append(receipt.Cards, card.ID)
		}
		if o.Status == model.ObjectiveCanceled && len(canceled) == 0 {
			return nil
		}
		o.Status = model.ObjectiveCanceled
		if err := db.UpdateObjective(tx, &o); err != nil {
			return err
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		return db.AppendEvent(tx, &model.Event{Entity: "objective:" + id, Type: "objective_cascade_canceled", Data: raw})
	})
	if err != nil {
		return objectiveCancelReceipt{}, err
	}
	return receipt, nil
}
