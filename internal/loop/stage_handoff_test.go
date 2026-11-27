package loop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStagePlanDispositionLinksExactProducingAttempt(t *testing.T) {
	f := newFx(t, model.ModeManual)
	card := model.Card{
		ID: "source-plan", Objective: f.obj.ID, Kind: model.KindPlan, Title: "synthetic plan",
		Tier: model.TierT2, Status: model.CardRunning,
	}
	if err := f.db.CreateCard(f.ctx, &card); err != nil {
		t.Fatal(err)
	}
	body := "## 新卡\n[{\"key\":\"implementation\",\"kind\":\"dev\",\"title\":\"synthetic implementation\"}]\n"
	if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	exit := 0
	a := model.Attempt{ID: "stage-source-attempt", Card: card.ID, Worktree: f.home, Exit: &exit}
	if err := f.db.CreateAttempt(f.ctx, &a); err != nil {
		t.Fatal(err)
	}
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), a.ID), f.db)
	if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: body}); err != nil {
		t.Fatal(err)
	}
	cards, err := f.db.ListCards(f.ctx, f.obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	var child string
	for _, c := range cards {
		if c.Kind == model.KindDev {
			child = c.ID
		}
	}
	if child == "" {
		t.Fatal("no implementation card")
	}
	events, err := f.db.ListEvents(f.ctx, "card:"+child)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != StageParentEvent {
			continue
		}
		var got map[string]string
		if err := json.Unmarshal(event.Data, &got); err != nil || got["card"] != card.ID || got["attempt"] != a.ID {
			t.Fatalf("wrong source: %+v: %v", got, err)
		}
		return
	}
	t.Fatal("missing exact source event")
}
