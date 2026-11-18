package route

import (
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// One model can carry per-kind rows: design cards try astra first, dev cards
// in the same tier try grok first, and a model listed twice is tried once.
func TestSelectKindsOrderPerCardKind(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	specs := []spec{
		{id: "codex", modelName: "astra", family: "openai", source: "codex", tier: model.TierT3, pref: 3, limit: 100, anchor: now},
		{id: "grok", modelName: "grok", family: "grok", source: "grok", tier: model.TierT3, pref: 1, limit: 100, anchor: now},
	}
	led, _, channels, pos := build(t, now, specs)
	pos = append(pos, model.Positioning{ID: "pos-astra-design", Model: "astra", Family: "openai",
		Tiers: []model.Tier{model.TierT3}, Preference: 1, Kinds: []model.CardKind{model.KindDesign}})
	pos = append(pos, model.Positioning{ID: "pos-grok-design", Model: "grok", Family: "grok",
		Tiers: []model.Tier{model.TierT3}, Preference: 2, Kinds: []model.CardKind{model.KindDesign}})
	// The all-kinds grok row has pref 1; restrict it to dev so design order holds.
	for i := range pos {
		if pos[i].Model == "grok" && len(pos[i].Kinds) == 0 {
			pos[i].Kinds = []model.CardKind{model.KindDev}
		}
	}

	rt, reason, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDesign, Tier: model.TierT3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Channel != "codex" || len(reason.Candidates) != 2 || reason.Candidates[1].Channel != "grok" {
		t.Fatalf("design route %+v candidates %+v", rt, reason.Candidates)
	}

	rt, reason, err = call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Channel != "grok" || reason.Candidates[0].Channel != "grok" {
		t.Fatalf("dev route %+v candidates %+v", rt, reason.Candidates)
	}
	seen := map[string]int{}
	for _, c := range reason.Candidates {
		seen[c.Channel+"/"+c.Model]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Fatalf("%s listed %d times: %+v", k, n, reason.Candidates)
		}
	}

	// Plan cards match no kind-limited row except the all-kinds astra row.
	rt, _, err = call(t, now, led, channels, pos, model.Card{Kind: model.KindPlan, Tier: model.TierT3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Channel != "codex" {
		t.Fatalf("plan route %+v", rt)
	}
}
