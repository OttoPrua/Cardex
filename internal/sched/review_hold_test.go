package sched

import (
	"context"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
)

func TestHeldReviewDoesNotDropPin(t *testing.T) {
	home, db, repo := newHome(t)
	ctx := context.Background()
	_, obj := seedRepo(t, db, repo, "true")
	addPinChannel(t, db, pinChannel{id: "cha", modelName: "model-a", health: model.HealthAuthFailed, pref: 0})
	addPinChannel(t, db, pinChannel{id: "chb", modelName: "model-b", pref: 9})
	card := model.Card{
		ID: "rv", Objective: obj, Kind: model.KindReview, Title: "rework review", Tier: model.TierT3,
		Status: model.CardReady, Channel: "cha", Model: "model-a",
		Body:       loop.AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "cha", "model-a"),
		Deps:       []string{"dev"},
		Acceptance: []string{"true"},
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) { return "PROMPT", nil }
	stopLaunchHosts(t, r)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	got, err := db.GetCard(ctx, "rv")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CardNeedsInput || got.Channel != "cha" || got.Model != "model-a" {
		t.Fatalf("card %+v", got)
	}
	attempts, err := db.ListAttempts(ctx, "rv")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 {
		t.Fatalf("attempts %+v", attempts)
	}
	assertOneQuestion(t, ctx, db, obj, "rv", "review hold unmet")
	assertNoPinEvent(t, ctx, db, "rv")
	assertObjectiveUntouched(t, ctx, db, obj, "", "")
}

func TestHeldReviewRouteDoesNotLowerModel(t *testing.T) {
	home, db, repo := newHome(t)
	ctx := context.Background()
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	channels := []model.Channel{{
		ID: "cha", Runner: model.RunnerFake, Family: "fam", SourceGroup: "fam",
		Models: []string{"low", "high"}, Concurrency: 2, Health: model.HealthOK, Tier: model.TierT1,
	}}
	pos := []model.Positioning{
		{Model: "low", Family: "fam", Tiers: []model.Tier{model.TierT1}},
		{Model: "high", Family: "fam", Tiers: []model.Tier{model.TierT3}},
	}
	r := New(home)
	body := loop.AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "cha", "low")

	t.Run("pinned model below the floor asks", func(t *testing.T) {
		card := model.Card{
			ID: "rv-low", Objective: obj.ID, Kind: model.KindReview, Title: "rework review", Tier: model.TierT3,
			Status: model.CardReady, Channel: "cha", Model: "low", Body: body, Deps: []string{"dev"},
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		rt, _, _, asked, err := r.chooseHeldReviewRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierT3)
		if err != nil || !asked || rt.Channel != "" || rt.Model != "" {
			t.Fatalf("route %+v asked %v err %v", rt, asked, err)
		}
		got, err := db.GetCard(ctx, card.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.CardNeedsInput || got.Channel != "cha" || got.Model != "low" {
			t.Fatalf("card %+v", got)
		}
		assertOneQuestion(t, ctx, db, obj.ID, card.ID, "review hold unmet")
		assertNoPinEvent(t, ctx, db, card.ID)
	})

	t.Run("channel pin uses a model that meets the floor", func(t *testing.T) {
		card := model.Card{
			ID: "rv-ch", Objective: obj.ID, Kind: model.KindReview, Title: "rework review", Tier: model.TierT3,
			Status: model.CardReady, Channel: "cha",
			Body: loop.AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "cha", ""),
		}
		before := append([]string(nil), channels[0].Models...)
		rt, _, _, asked, err := r.chooseHeldReviewRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierT3)
		if err != nil || asked || rt.Channel != "cha" || rt.Model != "high" {
			t.Fatalf("route %+v asked %v err %v", rt, asked, err)
		}
		if strings.Join(channels[0].Models, ",") != strings.Join(before, ",") {
			t.Fatalf("channels mutated %v", channels[0].Models)
		}
	})

	t.Run("pinned model at the floor stays", func(t *testing.T) {
		card := model.Card{
			ID: "rv-high", Objective: obj.ID, Kind: model.KindReview, Title: "rework review", Tier: model.TierT3,
			Status: model.CardReady, Channel: "cha", Model: "high",
			Body: loop.AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "cha", "high"),
		}
		rt, _, _, asked, err := r.chooseHeldReviewRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierT3)
		if err != nil || asked || rt.Channel != "cha" || rt.Model != "high" {
			t.Fatalf("route %+v asked %v err %v", rt, asked, err)
		}
	})
}
