package store

import (
	"context"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestChannelRoundTripKeepsProfileReference(t *testing.T) {
	db := openDB(t)
	const canary = "cdx-canary-a-7f3e9c"
	ch := model.Channel{
		ID: "api-a", Runner: model.RunnerClaude, EngineProfile: "anthropic-compatible:vendor-a", Family: "vendor-a",
		Health: model.HealthOK, Tier: model.TierT2, Models: []string{"vendor-a-model-1"},
	}
	if err := db.CreateChannel(context.Background(), &ch); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChannel(context.Background(), "api-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.EngineProfile != "anthropic-compatible:vendor-a" || got.Family != "vendor-a" || len(got.Models) != 1 || got.Models[0] != "vendor-a-model-1" {
		t.Fatalf("%+v", got)
	}
	var schema string
	if err := db.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='channels'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(schema)
	if strings.Contains(lower, "secret") || strings.Contains(lower, "api_key") || strings.Contains(lower, "credential") || strings.Contains(schema, canary) {
		t.Fatalf("schema %s", schema)
	}
	if !strings.Contains(lower, "engine_profile") || !strings.Contains(lower, "family") {
		t.Fatalf("schema %s", schema)
	}
}
