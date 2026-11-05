package sched

import (
	"context"
	"strings"

	"cardex/internal/classify"
	"cardex/internal/model"
	"cardex/internal/store"
)

// classifierChannelID is the design §7.4 channel name.
// Channel has no classifier slot, so only an id of this exact value is passed through.
const classifierChannelID = "classifier"

// classifyBeforeRoute persists a tier before the first route when the card has none.
// A card that already has a tier is returned unchanged and Call is not used.
// The returned tier is the value route.Request.Classified should carry.
// An empty returned tier means the card tier was already set.
func (r *Runner) classifyBeforeRoute(ctx context.Context, db *store.Store, card model.Card) (model.Card, model.Tier, error) {
	if card.Tier.Valid() && card.Tier != model.TierNone {
		return card, "", nil
	}
	var call classify.Call
	if r != nil {
		call = r.Classify
	}
	channelID := classifierChannel(ctx, db)
	if r != nil && r.Classify != nil && strings.TrimSpace(channelID) == "" {
		if err := r.noteClassify(ctx, db, card.ID, "classifier channel unavailable"); err != nil {
			return card, "", err
		}
	}
	res, err := classify.Classify(withClassifyCard(ctx, card.ID), card, classify.Options{
		Channel: channelID,
		Call:    call,
	})
	if err != nil {
		return card, "", err
	}
	tier := res.Tier
	if !tier.Valid() || tier == model.TierNone {
		tier = model.DefaultTier(card.Kind)
	}
	card.Tier = tier
	if err := db.UpdateCard(ctx, &card); err != nil {
		return card, "", err
	}
	return card, tier, nil
}

// classifierChannel reads the §7.4 channel by id.
// Fill passes only the backlog channel into routing, so the route list is not the config.
// A missing row leaves the channel empty and Classify uses the kind default.
func classifierChannel(ctx context.Context, db *store.Store) string {
	ch, err := db.GetChannel(ctx, classifierChannelID)
	if err != nil || ch.ID != classifierChannelID {
		return ""
	}
	return ch.ID
}
