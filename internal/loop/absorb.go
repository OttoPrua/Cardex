package loop

import (
	"context"
	"fmt"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// AbsorbResult reports what AbsorbPassed did to a stuck card.
type AbsorbResult struct {
	Card         string           `json:"card"`
	Attempt      string           `json:"attempt"`
	Status       model.CardStatus `json:"status"`
	Review       string           `json:"review,omitempty"`
	ReviewStatus model.CardStatus `json:"review_status,omitempty"`
}

// AbsorbPassed repairs a card left in running after its latest attempt passed
// and was already disposed (no later tick revisits a disposed attempt). It runs
// the normal review step: the card goes to reviewing and gets a review card, or
// reuses the open one. It never starts an executor or touches a live attempt.
func AbsorbPassed(ctx context.Context, db *store.Store, home, cardID string) (AbsorbResult, error) {
	card, err := db.GetCard(ctx, cardID)
	if err != nil {
		return AbsorbResult{}, err
	}
	if card.Status != model.CardRunning {
		return AbsorbResult{}, fmt.Errorf("card %s is %s, not running", card.ID, card.Status)
	}
	s, err := loadSnap(ctx, db, card.Objective)
	if err != nil {
		return AbsorbResult{}, err
	}
	var latest model.Attempt
	for _, a := range s.attempts[card.ID] {
		if latest.ID == "" || a.Index > latest.Index {
			latest = a
		}
	}
	if latest.ID == "" {
		return AbsorbResult{}, fmt.Errorf("card %s has no attempts", card.ID)
	}
	if latest.Outcome != model.OutcomePassed {
		return AbsorbResult{}, fmt.Errorf("latest attempt %s outcome is %q, not passed", latest.ID, latest.Outcome)
	}
	if !s.disposed(latest.ID) {
		return AbsorbResult{}, fmt.Errorf("latest attempt %s is not disposed yet; the next tick will handle it", latest.ID)
	}
	st := &step{ctx: ctx, db: db, home: home, cfg: LoadConfig(home), snap: s, now: clockFrom(ctx)}
	dec := Decide(card, latest.Outcome, latest.ErrorExcerpt, st.cfg.ReworkLimit, st.cfg.TransportRetries, st.budgetLeft(), historyBefore(s.attempts[card.ID], latest))
	if dec.Action != ActionReview {
		return AbsorbResult{}, fmt.Errorf("card %s kind %s does not go to review after a pass", card.ID, card.Kind)
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		st.ctx = ctx
		if err := st.ensureReview(card, latest); err != nil {
			return err
		}
		return st.event("card:"+card.ID, "card_absorbed", map[string]string{"card": card.ID, "attempt": latest.ID})
	})
	if err != nil {
		return AbsorbResult{}, err
	}
	res := AbsorbResult{Card: card.ID, Attempt: latest.ID}
	if saved, ok := st.snap.card(card.ID); ok {
		res.Status = saved.Status
	}
	for _, other := range st.snap.cards {
		if other.Kind == model.KindReview && !TerminalCard(other.Status) && (hasDep(other, card.ID) || reviewNames(other, card.ID)) {
			res.Review, res.ReviewStatus = other.ID, other.Status
		}
	}
	return res, nil
}
