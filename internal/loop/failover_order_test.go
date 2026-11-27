package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/route"
)

func TestOrderedAutomaticFailoverExhaustsChoices(t *testing.T) {
	f := newFx(t, model.ModeManual)
	// Preference, rather than channel ID order, is what selects B before C.
	a := f.channel("z-first", "first", "ma")
	b := f.channel("m-second", "second", "mb")
	c := f.channel("a-third", "third", "mc")
	for i, ch := range []model.Channel{a, b, c} {
		row := model.Positioning{Model: ch.Models[0], Family: ch.Family, Tiers: []model.Tier{model.TierT2}, Preference: i, Version: 1}
		if err := f.db.CreatePositioning(f.ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	card := f.card(model.Card{ID: "card", Channel: a.ID})
	for i, ch := range []model.Channel{a, b, c} {
		// Two retries on the same carrier are intentional. The third failure
		// opens the existing automatic other-channel disposition.
		for j := 0; j < 3; j++ {
			f.attempt(card, i*3+j, model.OutcomeTransport, ch.ID, "")
		}
		f.step()
		card = f.mustCard(card.ID)
		if i < 2 {
			want := []string{b.ID, c.ID}[i]
			if card.Status != model.CardReady || card.Channel != want {
				t.Fatalf("after %d synthetic transport attempts: got status %s and channel %s, want ready %s", (i+1)*3, card.Status, card.Channel, want)
			}
		}
	}
	if card.Status != model.CardNeedsInput || card.WaitQuestionID == "" {
		t.Fatalf("exhaustion: got status %s with no decision", card.Status)
	}
	if q := f.questions(); len(q) != 1 || !strings.Contains(q[0].Text, "no other channel is available") {
		t.Fatalf("expected one no-channel decision, got %+v", q)
	}
	if events := f.events("auto_route"); len(events) != 2 {
		t.Fatalf("three carriers require exactly two automatic switches, got %d", len(events))
	}
	t.Log("9 synthetic transport outcomes, 3 distinct carriers, 2 automatic switches, 1 owner question")
}

func TestSuccessfulAttemptResetsAutomaticFailureChain(t *testing.T) {
	f := newFx(t, model.ModeManual)
	a := f.channel("first", "first", "ma")
	b := f.channel("second", "second", "mb")
	f.position("ma", a.Family, model.TierT2)
	f.position("mb", b.Family, model.TierT2)
	failed := f.card(model.Card{ID: "card", Channel: b.ID})
	at := time.Now().UTC()
	event := model.Event{Entity: "card:" + failed.ID, Type: "auto_route", TS: at.Add(-time.Hour), Data: []byte(`{"channel":"second","exclude":"first"}`)}
	if err := f.db.AppendEvent(f.ctx, &event); err != nil {
		t.Fatal(err)
	}
	request := func() (model.Route, error) {
		req, err := routeRequestFrom(f.ctx, f.db, failed, f.objective(), at, b.ID, true)
		if err != nil {
			return model.Route{}, err
		}
		got, _, err := route.Select(f.ctx, req)
		return got, err
	}
	if got, err := request(); err != route.ErrNoCandidate || got.Channel != "" {
		t.Fatalf("prior failure was selected again: %+v, err %v", got, err)
	}
	f.attempt(failed, 0, model.OutcomePassed, b.ID, "")
	if got, err := request(); err != nil || got.Channel != a.ID {
		t.Fatalf("passed attempt did not reset the previous failure chain: %+v, err %v", got, err)
	}
	// Read without a new failure, as when routing later rework: the old
	// suggestion must not erase a current channel or hide the old carrier.
	req, err := routeRequestFrom(f.ctx, f.db, failed, f.objective(), at, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if req.Card.Channel != b.ID || req.AvoidChannels[a.ID] || len(req.Channels) != 2 {
		t.Fatalf("successful chain still projected its old suggestion or exclusions: pin %q, avoid %v, channels %v", req.Card.Channel, req.AvoidChannels, req.Channels)
	}
}
