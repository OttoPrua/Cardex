package sched

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestExplainNoCandidate(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	card := model.Card{ID: "c1", Kind: model.KindDev, Tier: model.TierT1, Status: model.CardReady}
	obj := model.Objective{ID: "o1"}
	ok := model.Channel{ID: "grok", Runner: model.RunnerGrok, Models: []string{"grok-4.7"}, Health: model.HealthOK, Concurrency: 1}
	row := model.Positioning{ID: "p", Model: "grok-4.7", Family: "grok", Tiers: []model.Tier{model.TierT1}, Preference: 1}

	if ex, show := explainNoCandidate(ctx, card, obj, nil, nil, nil, nil, now); !show || !strings.Contains(ex.Reason, "no channel") {
		t.Fatalf("no channel: %+v %v", ex, show)
	}
	if ex, show := explainNoCandidate(ctx, card, obj, []model.Channel{ok}, []model.Positioning{{ID: "cardex.positioning.version", Version: 1}}, nil, nil, now); !show || !strings.Contains(ex.Reason, "no positioning is approved") || !strings.Contains(ex.Hint, "positioning approve") {
		t.Fatalf("no positioning: %+v %v", ex, show)
	}
	if _, show := explainNoCandidate(ctx, card, obj, []model.Channel{ok}, []model.Positioning{row}, nil, nil, now); show {
		t.Fatal("routable card explained")
	}
	bad := ok
	bad.Health = model.HealthAuthFailed
	if ex, show := explainNoCandidate(ctx, card, obj, []model.Channel{bad}, []model.Positioning{row}, nil, nil, now); !show || !strings.Contains(ex.Reason, "health auth_failed") || !strings.Contains(ex.Hint, "not logged in") {
		t.Fatalf("auth failed: %+v %v", ex, show)
	}
	if _, show := explainNoCandidate(ctx, card, obj, []model.Channel{ok}, []model.Positioning{row}, map[string]int{"grok": 1}, nil, now); show {
		t.Fatal("busy-only channel explained")
	}
	t3 := row
	t3.Tiers = []model.Tier{model.TierT3}
	if ex, show := explainNoCandidate(ctx, card, obj, []model.Channel{ok}, []model.Positioning{t3}, nil, nil, now); !show || !strings.Contains(ex.Reason, "tier T1") {
		t.Fatalf("tier gap: %+v %v", ex, show)
	}
}

func TestMissingCaps(t *testing.T) {
	chs := []model.Channel{{ID: "g", Models: []string{"m1"}}}
	pos := []model.Positioning{{ID: "a", Model: "m1", Tiers: []model.Tier{model.TierT2}}, {ID: "b", Model: "m2", Capabilities: []string{"web"}}}
	if got := missingCaps([]string{"web", "browser"}, chs, pos); len(got) != 1 || got[0] != "web" {
		t.Fatalf("missing = %v", got)
	}
	pos[0].Capabilities = []string{"web"}
	if got := missingCaps([]string{"web"}, chs, pos); len(got) != 0 {
		t.Fatalf("missing = %v", got)
	}
}
