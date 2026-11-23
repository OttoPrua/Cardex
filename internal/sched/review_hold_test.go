package sched

import (
	"context"
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
