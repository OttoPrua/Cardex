package sched

import (
	"context"
	"strings"
	"time"

	"cardex/internal/model"
	"cardex/internal/store"
)

// releaseDueBackoff returns cards whose backoff deadline has passed to ready
// so this dispatch can launch them. A future deadline stays waiting.
func releaseDueBackoff(ctx context.Context, db *store.Store, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cards, err := db.ListCards(ctx, "")
	if err != nil {
		return err
	}
	for _, card := range cards {
		until, ok := backoffUntil(card.WaitReason)
		if !ok || card.Status != model.CardWaiting || until.After(now) {
			continue
		}
		card.Status = model.CardReady
		card.WaitReason = ""
		if err := db.UpdateCard(ctx, &card); err != nil {
			return err
		}
	}
	return nil
}

func backoffUntil(reason string) (time.Time, bool) {
	const prefix = "backoff:"
	if !strings.HasPrefix(reason, prefix) {
		return time.Time{}, false
	}
	until, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(reason, prefix))
	if err != nil {
		return time.Time{}, false
	}
	return until, true
}
