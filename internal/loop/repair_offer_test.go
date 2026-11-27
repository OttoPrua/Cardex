package loop

import (
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/route"
)

func TestSchedulerBindsFallbackRetryToSwitchAndLatestAttempt(t *testing.T) {
	f := newFx(t, model.ModeManual)
	a := f.channel("original", "original")
	b := f.channel("fallback", "fallback")
	card := f.card(model.Card{ID: "repair", Kind: model.KindRepair, Channel: a.ID, Status: model.CardHarvesting})
	f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "")
	f.step()
	card = f.mustCard(card.ID)
	if card.Channel != b.ID || card.Status != model.CardReady {
		t.Fatal("ordinary automatic fallback changed")
	}
	events, err := f.db.ListEvents(f.ctx, "card:"+card.ID)
	if err != nil {
		t.Fatal(err)
	}
	auto := route.LatestAutoSelection(events, card.ID)
	if auto.EventID == "" {
		t.Fatal("missing switch")
	}
	latest := f.attempt(card, 1, model.OutcomeNoOutput, b.ID, "")
	latest.StartedAt = auto.At.Add(time.Microsecond)
	if err := f.db.UpdateAttempt(f.ctx, &latest); err != nil {
		t.Fatal(err)
	}
	f.step()
	card = f.mustCard(card.ID)
	events, err = f.db.ListEvents(f.ctx, "card:"+card.ID)
	if err != nil {
		t.Fatal(err)
	}
	block, ok := route.FallbackRetryBlockFor(card, events)
	if !ok || card.WaitQuestionID == "" || block.Question != card.WaitQuestionID ||
		block.Attempt != latest.ID || block.AutoRoute != auto.EventID {
		t.Fatal("fallback retry decision was not bound to the exact attempt and auto route")
	}
}
