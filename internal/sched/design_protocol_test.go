package sched

import (
	"context"
	"testing"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestDesignConclusionProseCompletesThroughHarvest(t *testing.T) {
	for _, mode := range []model.ObjectiveMode{model.ModeManual, model.ModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, home, db, repo, objectiveID := dispositionHome(t)
			obj, err := db.GetObjective(ctx, objectiveID)
			if err != nil {
				t.Fatal(err)
			}
			obj.Mode = mode
			obj.FreezeMode = model.FreezeSafe
			if err := db.UpdateObjective(ctx, &obj); err != nil {
				t.Fatal(err)
			}
			node := model.DesignNode{ID: "n-design", Objective: objectiveID, Layer: model.LayerModule, Status: model.NodeDrafting, DocPath: "docs/design/o/module.md"}
			if err := db.CreateNode(ctx, &node); err != nil {
				t.Fatal(err)
			}
			card := devCard(objectiveID, "c-design")
			card.Node, card.Kind = node.ID, model.KindDesign
			finishHarvest(t, db, home, repo, card, model.Attempt{}, "## 结论\n接口冻结是一项发布检查点。\n\n下一版关注其他约束。\n", "", false, nil)
			tickDefault(t, home)
			tickDefault(t, home)
			got := mustCard(t, db, card.ID)
			attempt := mustAttempt(t, db, "a-"+card.ID)
			if got.Status != model.CardDone || got.WaitReason != "" || attempt.Outcome != model.OutcomePassed {
				t.Fatalf("finished harvest left card=%+v attempt=%+v", got, attempt)
			}
			cards, err := db.ListCards(ctx, objectiveID)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range cards {
				if c.Kind == model.KindNormalize {
					t.Fatalf("ordinary prose created normalizer %s", c.ID)
				}
			}
			events, err := db.ListEvents(ctx, "attempt:"+attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			absorptions := 0
			for _, e := range events {
				if e.Type == "loop.absorbed" {
					absorptions++
				}
			}
			if absorptions != 1 {
				t.Fatalf("persisted absorptions=%d", absorptions)
			}
		})
	}
}

func TestStaleDesignHarvestDoesNotWriteBackSchedulerSnapshot(t *testing.T) {
	ctx, home, db, _, objectiveID := dispositionHome(t)
	card := devCard(objectiveID, "stale")
	card.Kind = model.KindDesign
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	old := model.Attempt{ID: "old-attempt", Card: card.ID, Index: 0}
	newer := model.Attempt{ID: "current-attempt", Card: card.ID, Index: 1}
	for _, a := range []*model.Attempt{&old, &newer} {
		if err := db.CreateAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	original := r.Dispose
	r.Dispose = func(ctx context.Context, proposed *model.Card, res harvest.Result) error {
		if err := original(ctx, proposed, res); err != nil {
			return err
		}
		// Stands in for another writer updating the live card immediately
		// after the real disposition transaction commits.
		current := mustCard(t, db, card.ID)
		current.Status = model.CardPaused
		return db.UpdateCard(ctx, &current)
	}
	if err := r.deliver(ctx, db, &card, &old, harvest.Result{Outcome: model.OutcomePassed}); err != nil {
		t.Fatal(err)
	}
	if got := mustCard(t, db, card.ID); got.Status != model.CardPaused {
		t.Fatalf("stale scheduler snapshot overwrote current card: %+v", got)
	}
	if got := mustAttempt(t, db, old.ID); got.Outcome != model.OutcomePassed {
		t.Fatalf("historical harvest was not recorded: %+v", got)
	}
}

func TestCurrentDesignHarvestDoesNotOverwritePostCommitState(t *testing.T) {
	ctx, home, db, _, objectiveID := dispositionHome(t)
	card := devCard(objectiveID, "current")
	card.Kind = model.KindDesign
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	attempt := model.Attempt{ID: "done-attempt", Card: card.ID, Index: 0}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	original := r.Dispose
	r.Dispose = func(ctx context.Context, proposed *model.Card, res harvest.Result) error {
		if err := original(ctx, proposed, res); err != nil {
			return err
		}
		current := mustCard(t, db, card.ID)
		if current.Status != model.CardDone || len(current.Claims) != 1 || current.Claims[0].Path != "kept-claim" {
			t.Fatalf("design state or incoming claims were not persisted in disposition: %+v", current)
		}
		current.Status = model.CardCanceled
		return db.UpdateCard(ctx, &current)
	}
	card.Claims = []model.Claim{{Path: "kept-claim"}}
	if err := r.deliver(ctx, db, &card, &attempt, harvest.Result{Outcome: model.OutcomePassed, ResultText: "## 结论\nordinary prose\n", Claims: card.Claims}); err != nil {
		t.Fatal(err)
	}
	if got := mustCard(t, db, card.ID); got.Status != model.CardCanceled {
		t.Fatalf("post-commit state was overwritten: %+v", got)
	}
}
