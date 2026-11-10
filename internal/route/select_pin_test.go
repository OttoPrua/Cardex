package route

import (
	"context"
	"errors"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestModelOnlyObjectivePinUsesSelectedChannel(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	const (
		author  = "author-fam"
		foreign = "foreign-fam"
		other   = "other-fam"
	)
	ch := func(id, family string, health model.Health, models ...string) model.Channel {
		return model.Channel{
			ID: id, Runner: model.RunnerGrok, Family: family,
			Models: models, Concurrency: 2, Health: health,
		}
	}
	row := func(name, family string, pref int) model.Positioning {
		return model.Positioning{
			Model: name, Family: family, Tiers: []model.Tier{model.TierT3}, Preference: pref,
		}
	}
	review := model.Card{Kind: model.KindReview, Tier: model.TierT3, AuthorFamily: author}

	cases := []struct {
		name      string
		channels  []model.Channel
		pos       []model.Positioning
		card      model.Card
		objective model.Objective
		channel   string
		modelName string
		err       error
	}{
		{
			name: "unhealthy foreign first does not pin the review to the author family",
			channels: []model.Channel{
				ch("bad", foreign, model.HealthDegraded, "m"),
				ch("good", author, model.HealthOK, "m"),
				ch("legal", other, model.HealthOK, "other-m"),
			},
			pos: []model.Positioning{
				row("m", "", 0),
				row("other-m", other, 5),
			},
			card:      review,
			objective: model.Objective{Model: "m"},
			channel:   "legal",
			modelName: "other-m",
		},
		{
			name: "author-family healthy pin with no other family is no candidate",
			channels: []model.Channel{
				ch("bad", foreign, model.HealthDegraded, "m"),
				ch("good", author, model.HealthOK, "m"),
			},
			pos:       []model.Positioning{row("m", "", 0)},
			card:      review,
			objective: model.Objective{Model: "m"},
			err:       ErrNoCandidate,
		},
		{
			name: "all unhealthy model-only pin uses the first provider",
			channels: []model.Channel{
				ch("bad", foreign, model.HealthDegraded, "m"),
				ch("authorish", author, model.HealthAuthFailed, "m"),
				ch("legal", other, model.HealthOK, "other-m"),
			},
			pos: []model.Positioning{
				row("m", "", 0),
				row("other-m", other, 0),
			},
			card:      review,
			objective: model.Objective{Model: "m"},
			err:       ErrPinnedUnavailable,
		},
		{
			name: "all unhealthy author-family fallback stays unavailable",
			channels: []model.Channel{
				ch("bad", author, model.HealthDegraded, "m"),
				ch("legal", other, model.HealthOK, "other-m"),
			},
			pos: []model.Positioning{
				row("m", "", 0),
				row("other-m", other, 0),
			},
			card:      review,
			objective: model.Objective{Model: "m"},
			err:       ErrPinnedUnavailable,
		},
		{
			name: "non-conflicting model-only pin keeps the healthy channel",
			channels: []model.Channel{
				ch("bad", foreign, model.HealthDegraded, "m"),
				ch("good", other, model.HealthOK, "m"),
				ch("fast", other, model.HealthOK, "fast-m"),
			},
			pos: []model.Positioning{
				row("m", "", 5),
				row("fast-m", other, 0),
			},
			card:      review,
			objective: model.Objective{Model: "m"},
			channel:   "good",
			modelName: "m",
		},
		{
			name: "card channel beats the objective",
			channels: []model.Channel{
				ch("card-ch", author, model.HealthOK, "card-m"),
				ch("obj", other, model.HealthOK, "obj-m"),
			},
			pos: []model.Positioning{
				row("card-m", author, 5),
				row("obj-m", other, 0),
			},
			card:      model.Card{Kind: model.KindReview, Tier: model.TierT3, AuthorFamily: author, Channel: "card-ch"},
			objective: model.Objective{Channel: "obj", Model: "obj-m"},
			channel:   "card-ch",
			modelName: "card-m",
		},
		{
			name: "card model beats the objective",
			channels: []model.Channel{
				ch("card-ch", author, model.HealthOK, "card-m"),
				ch("obj", other, model.HealthOK, "obj-m"),
			},
			pos: []model.Positioning{
				row("card-m", author, 5),
				row("obj-m", other, 0),
			},
			card:      model.Card{Kind: model.KindReview, Tier: model.TierT3, AuthorFamily: author, Model: "card-m"},
			objective: model.Objective{Channel: "obj", Model: "obj-m"},
			channel:   "card-ch",
			modelName: "card-m",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, _, err := Select(context.Background(), Request{
				Card:        tc.card,
				Objective:   tc.objective,
				Channels:    tc.channels,
				Positioning: tc.pos,
				Now:         now,
			})
			if tc.err != nil {
				if !errors.Is(err, tc.err) || rt.Channel != "" || rt.Model != "" {
					t.Fatalf("route %+v err %v", rt, err)
				}
				return
			}
			if err != nil || rt.Channel != tc.channel || rt.Model != tc.modelName {
				t.Fatalf("route %+v err %v", rt, err)
			}
		})
	}
}
