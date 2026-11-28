package sched

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/route"
)

func TestLaunchKeepsStoredPinUntilHardReleaseWithoutReopeningFailedChannel(t *testing.T) {
	home, db, repo := newHome(t)
	ctx := context.Background()
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := New(home)
	r.Now = func() time.Time { return now }
	channels := []model.Channel{
		{ID: "first", Family: "first", Models: []string{"ma"}, Health: model.HealthOK},
		{ID: "second", Family: "second", Models: []string{"mb"}, Health: model.HealthOK, Concurrency: 1},
		{ID: "third", Family: "third", Models: []string{"mc"}, Health: model.HealthOK},
	}
	rows := []model.Positioning{
		{Model: "ma", Tiers: []model.Tier{model.TierT2}, Preference: 1},
		{Model: "mb", Tiers: []model.Tier{model.TierT2}, Preference: 2},
		{Model: "mc", Tiers: []model.Tier{model.TierT2}, Preference: 3},
	}
	card := model.Card{ID: "c", Objective: obj.ID, Kind: model.KindDev, Status: model.CardReady, Tier: model.TierT2, Channel: "second"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	first := model.Attempt{ID: "failed", Card: card.ID, Route: model.Route{Channel: "first", Model: "ma"}, Outcome: model.OutcomeTransport, EndedAt: now.Add(-time.Second)}
	if err := db.CreateAttempt(ctx, &first); err != nil {
		t.Fatal(err)
	}
	event := model.Event{Entity: "card:" + card.ID, Type: "auto_route", TS: now, Data: []byte(`{"channel":"second","model":"mb","exclude":"first","attempt":"failed"}`)}
	if err := db.AppendEvent(ctx, &event); err != nil {
		t.Fatal(err)
	}

	unhealthy := append([]model.Channel(nil), channels...)
	unhealthy[1].Health = model.HealthAuthFailed
	selected, why, rel, settled, err := r.resolveLaunchRoute(ctx, db, card, obj, unhealthy, rows, nil, nil, model.TierNone)
	if err != nil || settled || !rel.released || selected.Channel != "third" {
		t.Fatalf("hard release must skip earlier failed channel: %+v, released %v, settled %v, err %v", selected, rel.released, settled, err)
	}
	var reason string
	for _, filtered := range why.Filtered {
		if filtered.Channel == "first" {
			reason = filtered.Reason
		}
	}
	if reason != route.ReasonPriorFailure {
		t.Fatalf("missing active-chain reason for first: %+v", why.Filtered)
	}

	manual := card
	manual.Channel = "first"
	selected, _, rel, settled, err = r.resolveLaunchRoute(ctx, db, manual, obj, channels, rows, nil, nil, model.TierNone)
	if err != nil || settled || rel.released || selected.Channel != "first" {
		t.Fatalf("explicit new channel pin must be allowed: %+v, released %v, settled %v, err %v", selected, rel.released, settled, err)
	}

	// A full-record store update cannot distinguish a same-valued manual
	// channel choice from the original automatic suggestion. Keep the same
	// channel as an ordinary pin until the existing hard-release check drops it.
	card.Channel = "second"
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	pinned, _, rel, _, err := r.resolveLaunchRoute(ctx, db, card, obj, channels, rows, map[string]int{"second": 1}, nil, model.TierNone)
	if err != nil || rel.released || pinned.Channel != "second" {
		t.Fatalf("same-valued stored pin was redirected: %+v, released %v, err %v", pinned, rel.released, err)
	}

	// With no remaining eligible carrier, hard release opens one question
	// rather than circling back to the previously failed route.
	for i := 0; i < 2; i++ {
		selected, _, _, settled, err = r.resolveLaunchRoute(ctx, db, card, obj, unhealthy[:2], rows, nil, nil, model.TierNone)
		if err != nil || !settled || selected.Channel != "" {
			t.Fatalf("exhausted launch: %+v, settled %v, err %v", selected, settled, err)
		}
	}
	questions, err := db.ListQuestions(ctx, obj.ID)
	if err != nil || len(questions) != 1 || questions[0].Scope != card.ID {
		t.Fatalf("exhaustion must open one question: %+v, err %v", questions, err)
	}

	// A later failed attempt is not a success or a new manual route. B may
	// still be retried while viable; a hard release must still exclude A.
	second := model.Attempt{ID: "retry", Card: card.ID, Index: 1, Route: model.Route{Channel: "second", Model: "mb"}, Outcome: model.OutcomeTransport, EndedAt: now.Add(time.Second)}
	if err := db.CreateAttempt(ctx, &second); err != nil {
		t.Fatal(err)
	}
	selected, _, rel, settled, err = r.resolveLaunchRoute(ctx, db, card, obj, channels, rows, map[string]int{"second": 1}, nil, model.TierNone)
	if err != nil || settled || rel.released || selected.Channel != "second" {
		t.Fatalf("ordinary retry must still use B while viable: %+v, released %v, settled %v, err %v", selected, rel.released, settled, err)
	}
	selected, _, rel, settled, err = r.resolveLaunchRoute(ctx, db, card, obj, unhealthy, rows, nil, nil, model.TierNone)
	if err != nil || settled || !rel.released || selected.Channel != "third" {
		t.Fatalf("hard release after a same-channel retry must keep A excluded: %+v, released %v, settled %v, err %v", selected, rel.released, settled, err)
	}
	selected, _, _, settled, err = r.resolveLaunchRoute(ctx, db, card, obj, unhealthy[:2], rows, nil, nil, model.TierNone)
	if err != nil || !settled || selected.Channel != "" {
		t.Fatalf("hard release after retry must stop when C is absent: %+v, settled %v, err %v", selected, settled, err)
	}
	questions, err = db.ListQuestions(ctx, obj.ID)
	if err != nil || len(questions) != 1 {
		t.Fatalf("exhaustion across retries must still open exactly one question: %+v, err %v", questions, err)
	}
}

func TestFailoverExplainsEarlierFailureBeforeUnrelatedHealth(t *testing.T) {
	req := route.Request{
		Card: model.Card{ID: "c", Kind: model.KindDev, Tier: model.TierT2},
		Channels: []model.Channel{
			{ID: "failed", Models: []string{"ma"}, Health: model.HealthAuthFailed},
			{ID: "busy", Models: []string{"mb"}, Health: model.HealthOK, Concurrency: 1},
		},
		Positioning: []model.Positioning{
			{Model: "ma", Tiers: []model.Tier{model.TierT2}},
			{Model: "mb", Tiers: []model.Tier{model.TierT2}},
		},
		AvoidChannels: map[string]bool{"failed": true},
		InUse:         map[string]int{"busy": 1},
	}
	_, why, err := route.Select(context.Background(), req)
	if f := why.Filtered; !errors.Is(err, route.ErrNoCandidate) || len(f) != 2 ||
		f[0].Reason != route.ReasonPriorFailure || f[1].Reason != route.ReasonConcurrency {
		t.Fatalf("rejection must use current chain eligibility before health: %+v, err %v", f, err)
	}
}

func TestLaunchPersistsOrderedFailoverAcrossTwoHardReleases(t *testing.T) {
	home, db, repo := newHome(t)
	ctx := context.Background()
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	for i, spec := range []pinChannel{
		{id: "first", modelName: "ma"},
		{id: "second", modelName: "mb", health: model.HealthAuthFailed},
		{id: "third", modelName: "mc"},
		{id: "fourth", modelName: "md"},
	} {
		spec.pref = i
		addPinChannel(t, db, spec)
	}
	rows, err := db.ListPositioning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "c", Objective: obj.ID, Kind: model.KindDev, Status: model.CardReady, Tier: model.TierT2, Channel: "second"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cause := model.Attempt{ID: "failed", Card: card.ID, Route: model.Route{Channel: "first", Model: "ma"}, Outcome: model.OutcomeTransport, EndedAt: now.Add(-2 * time.Second)}
	if err := db.CreateAttempt(ctx, &cause); err != nil {
		t.Fatal(err)
	}
	event := model.Event{Entity: "card:" + card.ID, Type: "auto_route", TS: now.Add(-time.Second), Data: []byte(`{"channel":"second","model":"mb","exclude":"first","attempt":"failed"}`)}
	if err := db.AppendEvent(ctx, &event); err != nil {
		t.Fatal(err)
	}
	for i, expected := range []model.Route{{Channel: "third", Model: "mc"}, {Channel: "fourth", Model: "md"}} {
		if i == 1 {
			for id, health := range map[string]model.Health{"second": model.HealthOK, "third": model.HealthAuthFailed} {
				ch, err := db.GetChannel(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				ch.Health = health
				if err := db.UpdateChannel(ctx, &ch); err != nil {
					t.Fatal(err)
				}
			}
			card.Status = model.CardReady
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
		}
		// Fresh scheduler each time: only persisted state carries the switch.
		now = now.Add(time.Second)
		r := New(home)
		r.Now = func() time.Time { return now }
		r.Prompt = func(context.Context, prompt.Input) (string, error) { return "synthetic", nil }
		r.Launch = func(ctx context.Context, _, id string) (int, error) {
			a, err := db.GetAttempt(ctx, id)
			if err != nil {
				return 0, err
			}
			exit := 1
			a.EndedAt, a.Outcome, a.Exit = now, model.OutcomeTransport, &exit
			if err := db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + id, Type: "executor_pgid", Data: []byte(`{"pgid":123}`)}); err != nil {
				return 0, err
			}
			return 0, db.UpdateAttempt(ctx, &a)
		}
		channels, err := db.ListChannels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		started, err := r.launchCard(ctx, db, card, obj, channels, rows, nil, nil)
		if err != nil || started != expected.Channel {
			t.Fatalf("hard release %d: started %q, err %v; want %q", i+1, started, err, expected.Channel)
		}
		card, err = db.GetCard(ctx, card.ID)
		if err != nil || card.Channel != expected.Channel || card.Model != expected.Model {
			t.Fatalf("hard release %d: persisted %s/%s, err %v; want %+v", i+1, card.Channel, card.Model, err, expected)
		}
	}
	events, err := db.ListEvents(ctx, "card:"+card.ID)
	if err != nil {
		t.Fatal(err)
	}
	var releases int
	for _, e := range events {
		if e.Type != eventPinReleased {
			continue
		}
		releases++
		var record struct {
			Root string `json:"failover_origin"`
		}
		if err := json.Unmarshal(e.Data, &record); err != nil || record.Root != event.ID {
			t.Fatalf("release did not preserve event lineage: %+v, err %v", record, err)
		}
	}
	if releases != 2 {
		t.Fatalf("two automatic releases must be stored, got %d", releases)
	}
	t.Log("stored A→B automatic switch, B→C→D through two production launches despite A and B being healthy")
}
