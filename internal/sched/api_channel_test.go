package sched

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/runner"
)

func TestAPIChannelDispatchPersistsExactBinding(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	doc := `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test","default_model":"fixture-model","credential":{"source":"env","name":"CARDEX_API1_FIXTURE"}}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	ch, err := runner.ValidateChannelBinding(home, model.Channel{
		ID: "api-a", Runner: model.RunnerClaude, Health: model.HealthOK,
		Family: "vendor-a", EngineProfile: "vendor-a",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "c1", Objective: obj, Kind: model.KindResearch, Title: "fixture task",
		Tier: model.TierT2, Status: model.CardReady, Channel: ch.ID,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) { return "PROMPT", nil }
	r.Launch = func(context.Context, string, string) (int, error) { return 123, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("no prepared attempt, count %d: %v", len(attempts), err)
	}
	got := attempts[0]
	if got.Route.Channel != ch.ID || got.RouteReason.Dispatch == nil ||
		got.RouteReason.Dispatch.CompatibleBindingID == "" ||
		got.RouteReason.Dispatch.CompatibleBindingID != runner.CompatibleBindingID(ch.EngineProfile) {
		t.Fatal("dispatch did not persist the channel's exact binding")
	}
}
