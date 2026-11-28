package route

import (
	"context"
	"errors"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestAPIChannelRuntimeHoldDoesNotDisplaceEligibleRoutes(t *testing.T) {
	ch := model.Channel{ID: "a-api", EngineProfile: "anthropic-compatible:fixture", Runner: model.RunnerClaude,
		Family: "provider-a", Health: model.HealthOK, Models: []string{"fixture-model", "legacy-model"}}
	legacy := model.Channel{ID: "z-legacy", Runner: model.RunnerCodex, Family: "provider-z",
		Health: model.HealthOK, Models: []string{"legacy-model"}}
	req := Request{
		Card:     model.Card{Kind: model.KindDev, Requires: []string{"browser"}, Tier: model.TierT2},
		Channels: []model.Channel{ch, legacy},
		Positioning: []model.Positioning{
			{Model: "fixture-model", Family: ch.Family, Tiers: []model.Tier{model.TierT2}, Preference: 0},
			{Model: "legacy-model", Family: legacy.Family, Tiers: []model.Tier{model.TierT2}, Preference: 99},
		},
	}
	got, why, err := Select(context.Background(), req)
	if err != nil || got.Channel != legacy.ID || len(why.Filtered) == 0 || why.Filtered[0].Reason != ReasonCompatibleRuntime {
		t.Fatalf("automatic route %v %v %v", got, why, err)
	}
	req.Card.Model = "legacy-model"
	got, _, err = Select(context.Background(), req)
	if err != nil || got.Channel != legacy.ID {
		t.Fatalf("model-only route %v %v", got, err)
	}
	req.Card.Model = "fixture-model"
	if _, _, err := Select(context.Background(), req); !errors.Is(err, ErrCompatibleRuntimeHeld) {
		t.Fatalf("exclusive model pin not held: %v", err)
	}
	req.Card.Channel = ch.ID
	if _, _, err := Select(context.Background(), req); !errors.Is(err, ErrCompatibleRuntimeHeld) {
		t.Fatalf("named API pin released: %v", err)
	}
	req.Card.Channel, req.Card.Model = "", ""
	req.Objective.Channel = ch.ID
	if _, _, err := Select(context.Background(), req); !errors.Is(err, ErrCompatibleRuntimeHeld) {
		t.Fatalf("objective API pin released: %v", err)
	}
}
