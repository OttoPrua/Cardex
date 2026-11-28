package route

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestAPI2ComparisonNeverSubstitutesChannelOrModel(t *testing.T) {
	card := model.Card{Kind: model.KindResearch, Body: "approved fixture question", Requires: []string{model.RequireStrictComparison}, Channel: "fixture-a", Model: "model-exact"}
	base := model.Channel{ID: "fixture-a", Runner: model.RunnerClaude, EngineProfile: "anthropic-compatible:a@" + strings.Repeat("a", 64),
		Health: model.HealthOK, Models: []string{"model-exact", "model-other"}, Concurrency: 2}
	alt := base
	alt.ID = "fixture-b"
	for _, tc := range []struct {
		name string
		edit func(*Request)
		pass bool
	}{
		{"exact", func(*Request) {}, true},
		{"health degraded", func(req *Request) { req.Channels[0].Health = model.HealthDegraded }, false},
		{"busy", func(req *Request) { req.InUse = map[string]int{"fixture-a": 2} }, false},
		{"routing exclusion", func(req *Request) { req.AvoidChannels = map[string]bool{"fixture-a": true} }, false},
		{"no model", func(req *Request) { req.Channels[0].Models = []string{"model-other"} }, false},
		{"legacy cannot prove", func(req *Request) { req.Channels[0].EngineProfile = "legacy" }, false},
		{"objective pin must not substitute", func(req *Request) { req.Card.Channel = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Card: card, Channels: []model.Channel{base, alt}, Objective: model.Objective{Channel: "fixture-b", Model: "model-exact"}}
			tc.edit(&req)
			rt, _, err := Select(context.Background(), req)
			if tc.pass {
				if err != nil || rt.Channel != card.Channel || rt.Model != card.Model {
					t.Fatalf("wrong verified route: %+v %v", rt, err)
				}
			} else if !errors.Is(err, ErrStrictComparisonUnavailable) || rt != (model.Route{}) {
				t.Fatalf("unsupported comparison fell back: %+v %v", rt, err)
			}
		})
	}
}
