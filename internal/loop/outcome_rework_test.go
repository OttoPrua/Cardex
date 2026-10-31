package loop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/quota"
	"cardex/internal/route"
)

func TestPassedCardsEnterReview(t *testing.T) {
	cases := []struct {
		kind   model.CardKind
		review bool
	}{
		{model.KindDev, true},
		{model.KindSkeleton, true},
		{model.KindRepair, true},
		{model.KindRebase, true},
		{model.KindBacklog, true},
		{model.KindDesign, false},
		{model.KindResearch, false},
		{model.KindSpike, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			ch := f.channel("author", "anthropic", "author-m")
			card := f.card(model.Card{ID: "c", Kind: tc.kind, Channel: ch.ID, Status: model.CardHarvesting})
			f.attempt(card, 0, model.OutcomePassed, ch.ID, "")
			f.step()
			got := f.mustCard("c")
			reviews := cardsKind(f, model.KindReview)
			if !tc.review {
				if got.Status != model.CardHarvesting || len(reviews) != 0 {
					t.Fatalf("card %+v reviews %+v", got, reviews)
				}
				f.step()
				if len(cardsKind(f, model.KindReview)) != 0 || f.mustCard("c").Status != model.CardHarvesting {
					t.Fatal("second step created a review")
				}
				return
			}
			if got.Status != model.CardReviewing || got.AuthorFamily != "anthropic" || len(reviews) != 1 {
				t.Fatalf("card %+v reviews %+v", got, reviews)
			}
			review := reviews[0]
			if review.Status != model.CardReady || review.AuthorFamily != "anthropic" || !hasDepID(review, "c") || !strings.Contains(review.Body, "anthropic") {
				t.Fatalf("review %+v", review)
			}
			f.step()
			if f.mustCard("c").Status != model.CardReviewing || len(cardsKind(f, model.KindReview)) != 1 {
				t.Fatal("second step left the card unreviewed")
			}
		})
	}
}

func TestAutomaticReassignmentUsesRoute(t *testing.T) {
	cases := []struct {
		name      string
		outcome   model.Outcome
		priors    int
		block     string
		want      string
		modelName string
	}{
		{name: "no_output tier", outcome: model.OutcomeNoOutput, block: "tier", want: "ch-c"},
		{name: "quota_hit redline", outcome: model.OutcomeQuotaHit, block: "redline", want: "ch-c"},
		{name: "auth_failed concurrency", outcome: model.OutcomeAuthFailed, block: "concurrency", want: "ch-c"},
		{name: "transport family", outcome: model.OutcomeTransport, priors: 2, block: "family", want: "ch-c"},
		{name: "timeout tier", outcome: model.OutcomeTimeout, priors: 2, block: "tier", want: "ch-c"},
		{name: "timeout stays once", outcome: model.OutcomeTimeout, priors: 1, block: "tier", want: "ch-a"},
		{name: "transport stays once", outcome: model.OutcomeTransport, priors: 1, block: "tier", want: "ch-a"},
		{name: "no_output no candidate", outcome: model.OutcomeNoOutput, block: "none", want: ""},
		{name: "empty positioning", outcome: model.OutcomeNoOutput, block: "empty", want: "ch-b"},
		{name: "transport empty positioning", outcome: model.OutcomeTransport, priors: 2, block: "empty", want: "ch-b"},
		{name: "timeout empty positioning", outcome: model.OutcomeTimeout, priors: 2, block: "empty", want: "ch-b"},
		{name: "empty positioning lower tier", outcome: model.OutcomeNoOutput, block: "empty-tier", want: "ch-c"},
		{name: "empty positioning family", outcome: model.OutcomeNoOutput, block: "empty-family", want: "ch-c"},
		{name: "empty shared model keeps channel tier", outcome: model.OutcomeNoOutput, block: "empty-shared", want: "ch-c", modelName: "shared"},
		{name: "overlap windows", outcome: model.OutcomeNoOutput, block: "overlap", want: "ch-b"},
		{name: "window and attempt once", outcome: model.OutcomeQuotaHit, block: "dedup", want: "ch-b"},
		{name: "other objective usage", outcome: model.OutcomeNoOutput, block: "other-usage", want: "ch-c"},
		{name: "other objective running", outcome: model.OutcomeAuthFailed, block: "other-run", want: "ch-c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			a := f.channel("ch-a", "fam-a", "ma")
			b := f.channel("ch-b", "fam-b", "mb")
			var c model.Channel
			if tc.block != "none" && tc.block != "empty" && tc.block != "overlap" && tc.block != "dedup" {
				c = f.channel("ch-c", "fam-c", "mc")
			}
			kind := model.KindDev
			tier := model.TierT2
			family := ""
			switch tc.block {
			case "tier", "none":
				tier = model.TierT3
				f.position("mb", "fam-b", model.TierT1)
				if tc.block != "none" {
					f.position("mc", "fam-c", model.TierT3)
				}
			case "redline":
				b.Redline = 0.5
				b.QuotaWindows[0].UsedTokens = 90
				f.saveChannel(b)
				c.Redline = 0.5
				f.saveChannel(c)
				f.position("mb", "fam-b", model.TierT2)
				f.position("mc", "fam-c", model.TierT2)
			case "concurrency":
				b.Concurrency = 1
				f.saveChannel(b)
				c.Concurrency = 1
				f.saveChannel(c)
				f.card(model.Card{ID: "busy", Status: model.CardRunning, Channel: b.ID})
				f.position("mb", "fam-b", model.TierT2)
				f.position("mc", "fam-c", model.TierT2)
			case "family":
				kind = model.KindReview
				tier = model.TierT3
				family = "fam-b"
				f.position("mb", "fam-b", model.TierT3)
				f.position("mc", "fam-c", model.TierT3)
			case "empty":
				tier = model.TierT2
			case "empty-tier":
				tier = model.TierT3
				b.Tier = model.TierT1
				f.saveChannel(b)
				c.Tier = model.TierT3
				f.saveChannel(c)
			case "empty-family":
				kind = model.KindReview
				tier = model.TierT3
				family = "fam-b"
				b.Tier = model.TierT3
				f.saveChannel(b)
				c.Tier = model.TierT3
				f.saveChannel(c)
			case "empty-shared":
				tier = model.TierT3
				a.Models = []string{"shared"}
				a.Tier = model.TierT2
				f.saveChannel(a)
				b.Tier = model.TierT1
				b.Models = []string{"shared"}
				f.saveChannel(b)
				c.Tier = model.TierT3
				c.Models = []string{"shared"}
				f.saveChannel(c)
			case "overlap":
				tier = model.TierT2
				reset := time.Now().Add(2 * time.Hour).UTC()
				b.QuotaWindows = []model.QuotaWindow{
					{Name: "5h", ResetRule: "rolling", LimitTokens: 100, UsedTokens: 20, ResetAt: reset, Remaining: 1},
					{Name: "daily", ResetRule: "rolling", LimitTokens: 5000, UsedTokens: 300, ResetAt: reset, Remaining: 1},
				}
				f.saveChannel(b)
				f.position("mb", "fam-b", model.TierT2)
			case "dedup":
				tier = model.TierT2
				reset := time.Now().Add(2 * time.Hour).UTC()
				b.QuotaWindows = []model.QuotaWindow{{
					Name: "5h", ResetRule: "rolling", LimitTokens: 30, UsedTokens: 20, ResetAt: reset, Remaining: 1,
				}}
				f.saveChannel(b)
				f.otherUsage(b.ID, 20)
				f.position("mb", "fam-b", model.TierT2)
			case "other-usage":
				tier = model.TierT2
				b.Redline = 0.5
				f.saveChannel(b)
				f.otherUsage(b.ID, 90)
				f.position("mb", "fam-b", model.TierT2)
				f.position("mc", "fam-c", model.TierT2)
			case "other-run":
				tier = model.TierT2
				b.Concurrency = 1
				f.saveChannel(b)
				c.Concurrency = 1
				f.saveChannel(c)
				f.otherRunning(b.ID)
				f.position("mb", "fam-b", model.TierT2)
				f.position("mc", "fam-c", model.TierT2)
			}
			card := f.card(model.Card{
				ID: "c", Kind: kind, Tier: tier, Channel: a.ID, AuthorFamily: family, Status: model.CardHarvesting,
			})
			for i := 0; i < tc.priors; i++ {
				f.attempt(card, i, tc.outcome, a.ID, "")
			}
			current := f.attempt(card, tc.priors, tc.outcome, a.ID, "")
			f.step()
			got := f.mustCard("c")
			if tc.want == "" {
				if got.Status != model.CardNeedsInput || got.Channel != a.ID || got.Model != "" || len(f.questions()) != 1 || latestAuto(f, "c").ok {
					t.Fatalf("card %+v questions %+v route %+v", got, f.questions(), latestAuto(f, "c"))
				}
				if !strings.Contains(f.questions()[0].Text, "no other channel") {
					t.Fatalf("question %+v", f.questions()[0])
				}
				f.step()
				if len(f.questions()) != 1 || latestAuto(f, "c").ok || f.mustCard("c").Channel != a.ID {
					t.Fatal("second step assigned a channel")
				}
				return
			}
			if tc.want == a.ID {
				if got.Status != model.CardReady || got.Channel != a.ID || latestAuto(f, "c").ok {
					t.Fatalf("stayed %+v route %+v", got, latestAuto(f, "c"))
				}
				f.step()
				if f.mustCard("c").Channel != a.ID || latestAuto(f, "c").ok {
					t.Fatal("second step left the channel")
				}
				return
			}
			routed := latestAuto(f, "c")
			if got.Status != model.CardReady || got.Channel != tc.want || got.Model != "" || !routed.ok || routed.channel != tc.want || routed.exclude != a.ID {
				t.Fatalf("card %+v route %+v", got, routed)
			}
			wantModel := tc.modelName
			if wantModel == "" && tc.want == c.ID {
				wantModel = "mc"
			}
			if wantModel == "" && tc.want == b.ID {
				wantModel = "mb"
			}
			if wantModel != "" && routed.model != wantModel {
				t.Fatalf("model %s route %+v", routed.model, routed)
			}
			if tc.block == "empty" || tc.block == "empty-tier" || tc.block == "empty-family" || tc.block == "empty-shared" {
				rows, err := f.db.ListPositioning(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 0 {
					t.Fatalf("persisted positioning %+v", rows)
				}
			}
			if tc.outcome == model.OutcomeQuotaHit || tc.outcome == model.OutcomeAuthFailed || tc.outcome == model.OutcomeTransport || tc.outcome == model.OutcomeTimeout {
				if got.Base != current.Branch {
					t.Fatalf("base %s", got.Base)
				}
			}
			if tc.outcome == model.OutcomeAuthFailed {
				stored, err := f.db.GetChannel(f.ctx, a.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.Health != model.HealthAuthFailed || len(f.events("notify")) != 1 {
					t.Fatalf("health %s notices %d", stored.Health, len(f.events("notify")))
				}
			}
			if tc.outcome == model.OutcomeQuotaHit && len(f.events("quota_block")) != 1 {
				t.Fatalf("quota events %+v", f.events("quota_block"))
			}
			f.step()
			again := latestAuto(f, "c")
			if again != routed || f.mustCard("c").Channel != tc.want || f.mustCard("c").Model != "" {
				t.Fatalf("second step card %+v route %+v", f.mustCard("c"), again)
			}
		})
	}
}

func TestAutomaticRouteIsNotAnExplicitPin(t *testing.T) {
	t.Run("chosen channel is filtered once it is over the redline", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "fam-a", "ma")
		b := f.channel("ch-b", "fam-b", "mb")
		c := f.channel("ch-c", "fam-c", "mc")
		d := f.channel("ch-d", "fam-d", "md")
		f.position("mb", "fam-b", model.TierT2)
		f.position("mc", "fam-c", model.TierT2)
		f.position("md", "fam-d", model.TierT2)
		card := f.card(model.Card{ID: "c", Kind: model.KindDev, Tier: model.TierT2, Status: model.CardHarvesting})
		f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "")
		f.step()
		got := f.mustCard("c")
		routed := latestAuto(f, "c")
		if got.Channel != b.ID || got.Model != "" || !routed.ok || routed.channel != b.ID || routed.model != "mb" || routed.exclude != a.ID {
			t.Fatalf("card %+v route %+v", got, routed)
		}
		b.Redline = 0.5
		b.QuotaWindows[0].UsedTokens = 90
		f.saveChannel(b)
		req, err := routeRequestFrom(f.ctx, f.db, got, f.objective(), time.Now().UTC(), "", false)
		if err != nil {
			t.Fatal(err)
		}
		if req.Card.Channel != "" || req.Card.Model != "" || req.Objective.Channel != "" || req.Objective.Model != "" {
			t.Fatalf("stored pin changed: card %q/%q objective %q/%q", req.Card.Channel, req.Card.Model, req.Objective.Channel, req.Objective.Model)
		}
		for _, ch := range req.Channels {
			if ch.ID == a.ID {
				t.Fatal("failed channel was still offered")
			}
		}
		picked, reason, err := route.Select(f.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if picked.Channel == b.ID || picked.Channel == a.ID || picked.Channel == d.ID || picked.Channel != c.ID {
			t.Fatalf("picked %+v filtered %+v scores %+v", picked, reason.Filtered, reason.Scores)
		}
		if len(reason.Scores) == 1 && reason.Scores[0].Score == 0 && len(reason.Filtered) == 0 {
			t.Fatalf("explicit pin path %+v", reason)
		}
	})

	t.Run("a user channel stays a pin when it is over the redline", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "fam-a", "ma")
		b := f.channel("ch-b", "fam-b", "mb")
		c := f.channel("ch-c", "fam-c", "mc")
		b.Redline = 0.5
		b.QuotaWindows[0].UsedTokens = 90
		f.saveChannel(b)
		f.position("mb", "fam-b", model.TierT2)
		f.position("mc", "fam-c", model.TierT2)
		card := f.card(model.Card{ID: "c", Channel: b.ID, Tier: model.TierT2, Status: model.CardHarvesting})
		f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "")
		f.step()
		got := f.mustCard("c")
		routed := latestAuto(f, "c")
		if got.Channel != c.ID || got.Model != "" || !routed.ok || routed.channel != c.ID || routed.model != "mc" || routed.exclude != a.ID {
			t.Fatalf("card %+v route %+v", got, routed)
		}
		req, err := routeRequestFrom(f.ctx, f.db, got, f.objective(), time.Now().UTC(), "", false)
		if err != nil {
			t.Fatal(err)
		}
		if req.Card.Channel != b.ID || req.Card.Model != "" {
			t.Fatalf("request cleared the user pin: %+v", req.Card)
		}
		dropped := true
		for _, ch := range req.Channels {
			if ch.ID == a.ID {
				dropped = false
			}
		}
		if !dropped {
			t.Fatal("failed channel was still offered")
		}
		picked, reason, err := route.Select(f.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if picked.Channel != b.ID || len(reason.Scores) != 1 || reason.Scores[0].Score != 0 || len(reason.Filtered) != 0 {
			t.Fatalf("picked %+v reason %+v", picked, reason)
		}
	})

	t.Run("a user pin on the failed channel is kept", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "fam-a", "ma")
		b := f.channel("ch-b", "fam-b", "mb")
		f.position("mb", "fam-b", model.TierT2)
		card := f.card(model.Card{ID: "c", Channel: a.ID, Tier: model.TierT2, Status: model.CardHarvesting})
		f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "")
		f.step()
		got := f.mustCard("c")
		routed := latestAuto(f, "c")
		if got.Channel != b.ID || got.Model != "" || !routed.ok || routed.channel != b.ID || routed.model != "mb" || routed.exclude != a.ID {
			t.Fatalf("card %+v route %+v", got, routed)
		}
		req, err := routeRequestFrom(f.ctx, f.db, got, f.objective(), time.Now().UTC(), "", false)
		if err != nil {
			t.Fatal(err)
		}
		if req.Card.Channel != a.ID {
			t.Fatalf("request cleared the user pin %q", req.Card.Channel)
		}
		kept := false
		for _, ch := range req.Channels {
			if ch.ID == a.ID {
				kept = true
			}
		}
		if !kept {
			t.Fatal("user pin was dropped from the request")
		}
		picked, reason, err := route.Select(f.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if picked.Channel != a.ID || len(reason.Scores) != 1 || reason.Scores[0].Score != 0 || len(reason.Filtered) != 0 {
			t.Fatalf("picked %+v reason %+v", picked, reason)
		}
	})

	t.Run("empty positioning choice is not a pin", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "fam-a", "ma")
		b := f.channel("ch-b", "fam-b", "mb")
		c := f.channel("ch-c", "fam-c", "mc")
		card := f.card(model.Card{ID: "c", Kind: model.KindDev, Tier: model.TierT2, Status: model.CardHarvesting})
		f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "")
		f.step()
		got := f.mustCard("c")
		routed := latestAuto(f, "c")
		if got.Channel != b.ID || got.Model != "" || got.Status != model.CardReady || !routed.ok || routed.channel != b.ID || routed.model != "mb" || routed.exclude != a.ID {
			t.Fatalf("card %+v route %+v", got, routed)
		}
		rows, err := f.db.ListPositioning(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("persisted positioning %+v", rows)
		}
		b.Redline = 0.5
		b.QuotaWindows[0].UsedTokens = 90
		f.saveChannel(b)
		req, err := routeRequestFrom(f.ctx, f.db, got, f.objective(), time.Now().UTC(), "", false)
		if err != nil {
			t.Fatal(err)
		}
		if req.Card.Channel != "" || req.Card.Model != "" || req.Objective.Channel != "" || req.Objective.Model != "" {
			t.Fatalf("stored automatic channel stayed a pin: card %q/%q objective %q/%q", req.Card.Channel, req.Card.Model, req.Objective.Channel, req.Objective.Model)
		}
		for _, ch := range req.Channels {
			if ch.ID == a.ID {
				t.Fatal("failed channel was still offered")
			}
		}
		picked, reason, err := route.Select(f.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if picked.Channel != c.ID || picked.Model == "" {
			t.Fatalf("picked %+v filtered %+v scores %+v", picked, reason.Filtered, reason.Scores)
		}
		if len(reason.Scores) == 1 && reason.Scores[0].Score == 0 && len(reason.Filtered) == 0 {
			t.Fatalf("explicit pin path %+v", reason)
		}
	})
}

func TestScenarioSwitchShapes(t *testing.T) {
	t.Run("no_output", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "ch-a")
		b := f.channel("ch-b", "ch-b")
		card := f.card(model.Card{ID: "c", Channel: a.ID, Status: model.CardHarvesting})
		f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "")
		f.step()
		got := f.mustCard("c")
		if got.Channel != b.ID || got.Status != model.CardReady || len(f.questions()) != 0 {
			t.Fatalf("switch %+v questions %+v", got, f.questions())
		}
		rows, err := f.db.ListPositioning(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("persisted positioning %+v", rows)
		}
		f.attempt(got, 1, model.OutcomeNoOutput, b.ID, "")
		f.step()
		got = f.mustCard("c")
		if got.Channel != b.ID || got.Status != model.CardNeedsInput || len(f.questions()) != 1 || f.questions()[0].Answer != "" {
			t.Fatalf("ask %+v questions %+v", got, f.questions())
		}
		f.step()
		if f.mustCard("c").Channel != b.ID || len(f.questions()) != 1 {
			t.Fatal("second step changed the no-output disposition")
		}
	})

	t.Run("quota_hit", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "ch-a")
		b := f.channel("ch-b", "ch-b")
		reset := time.Date(2026, 12, 1, 3, 4, 5, 0, time.UTC)
		card := f.card(model.Card{ID: "c", Channel: a.ID, Status: model.CardHarvesting})
		attempt := f.attempt(card, 0, model.OutcomeQuotaHit, a.ID, "quota reset "+reset.Format(time.RFC3339))
		f.step()
		got := f.mustCard("c")
		if got.Channel != b.ID || got.Status != model.CardReady || got.Base != attempt.Branch {
			t.Fatalf("continue %+v", got)
		}
		blocked, err := f.db.GetChannel(f.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if blocked.Health != model.HealthOK || len(blocked.QuotaWindows) != 1 || blocked.QuotaWindows[0].Remaining != 0 || !blocked.QuotaWindows[0].ResetAt.Equal(reset) {
			t.Fatalf("channel %+v", blocked)
		}
		led := quota.New()
		if err := RestoreQuota(f.ctx, led, f.events("")); err != nil {
			t.Fatal(err)
		}
		rem, err := led.Remaining(f.ctx, a.ID)
		if err != nil || !rem.Blocked {
			t.Fatalf("remaining %+v %v", rem, err)
		}
		if len(f.events("quota_block")) != 1 {
			t.Fatalf("events %+v", f.events("quota_block"))
		}
		f.step()
		if f.mustCard("c").Channel != b.ID || len(f.events("quota_block")) != 1 {
			t.Fatal("second step blocked or switched again")
		}
	})

	t.Run("auth_failed", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		obj := f.objective()
		obj.Origin = "lark:ou_s09"
		if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		f.ctx = WithNotifier(f.ctx, FileNotifier{Dir: dir})
		a := f.channel("ch-a", "ch-a")
		b := f.channel("ch-b", "ch-b")
		card := f.card(model.Card{ID: "c", Channel: a.ID, Status: model.CardHarvesting})
		attempt := f.attempt(card, 0, model.OutcomeAuthFailed, a.ID, "token rejected")
		f.step()
		got := f.mustCard("c")
		if got.Channel != b.ID || got.Base != attempt.Branch || got.Status != model.CardReady {
			t.Fatalf("switch %+v", got)
		}
		ch, err := f.db.GetChannel(f.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ch.Health != model.HealthAuthFailed {
			t.Fatalf("health %s", ch.Health)
		}
		body := noticeFile(t, dir, "auth_failed")
		if !strings.Contains(body, obj.Origin) || !strings.Contains(body, `"destination": "`+obj.Origin) || !strings.Contains(body, `"adapter": "file"`) {
			t.Fatalf("notice %s", body)
		}
		f.step()
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || f.mustCard("c").Channel != b.ID {
			t.Fatal("second step notified or switched again")
		}
	})
}

func noticeFile(t *testing.T, dir, kind string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `"kind": "`+kind+`"`) {
			return string(body)
		}
	}
	t.Fatalf("no %s notice in %s", kind, dir)
	return ""
}

func TestListKeysDoNotCollide(t *testing.T) {
	t.Run("default and reused keys on two nodes", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		other := f.nodeID("other", model.NodeFrozen)
		ch := f.channel("ch-a", "a")
		empty := writeResult(t, `[{}]`)
		named := writeResult(t, `[{"key":"card-1","title":"Named","kind":"dev"}]`)
		planA := f.card(model.Card{ID: "plan-a", Kind: model.KindPlan, Tier: model.TierT3, Channel: ch.ID})
		planB := f.card(model.Card{ID: "plan-b", Kind: model.KindPlan, Tier: model.TierT3, Node: other.ID, Channel: ch.ID})
		attemptA := f.attempt(planA, 0, model.OutcomePassed, ch.ID, "")
		attemptA.Worktree = empty
		attemptB := f.attempt(planB, 0, model.OutcomePassed, ch.ID, "")
		attemptB.Worktree = named
		if err := f.db.UpdateAttempt(f.ctx, &attemptA); err != nil {
			t.Fatal(err)
		}
		if err := f.db.UpdateAttempt(f.ctx, &attemptB); err != nil {
			t.Fatal(err)
		}
		f.step()
		var created []model.Card
		seen := map[string]bool{}
		for _, card := range f.cards() {
			if card.Kind != model.KindDev {
				continue
			}
			if card.ID == "card-1" || seen[card.ID] {
				t.Fatalf("colliding card %+v", card)
			}
			seen[card.ID] = true
			created = append(created, card)
		}
		if len(created) != 2 || f.mustCard("plan-a").Status != model.CardDone || f.mustCard("plan-b").Status != model.CardDone {
			t.Fatalf("created %+v", created)
		}
	})

	t.Run("duplicate key remaps the local dependency", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		dir := writeResult(t, `[
			{"key":"dup","title":"First","kind":"dev"},
			{"key":"dup","title":"Second","kind":"dev"},
			{"key":"tail","title":"Tail","kind":"dev","deps":["dup","missing"]}
		]`)
		plan := f.card(model.Card{ID: "plan", Kind: model.KindPlan, Tier: model.TierT3, Channel: ch.ID})
		attempt := f.attempt(plan, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		first := cardByTitle(f, "First")
		second := cardByTitle(f, "Second")
		tail := cardByTitle(f, "Tail")
		if first.ID == second.ID || first.ID == "dup" || second.ID == "dup" || tail.ID == "tail" {
			t.Fatalf("ids %s %s %s", first.ID, second.ID, tail.ID)
		}
		if !hasDepID(tail, first.ID) || hasDepID(tail, "dup") || hasDepID(tail, "missing") || hasDepID(tail, second.ID) {
			t.Fatalf("tail deps %v first %s", tail.Deps, first.ID)
		}
		found := false
		for _, event := range f.events("invalid_dep") {
			if strings.Contains(string(event.Data), "missing") {
				found = true
			}
		}
		if !found || f.mustCard("plan").Status != model.CardDone {
			t.Fatalf("events %+v plan %s", f.events("invalid_dep"), f.mustCard("plan").Status)
		}
	})

	t.Run("replan still edits an existing id", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		f.card(model.Card{ID: "keep", Status: model.CardReady, Title: "old", Channel: ch.ID})
		dir := writeResult(t, `[
			{"key":"keep","title":"edited","kind":"dev","deps":["fresh"]},
			{"key":"fresh","title":"Fresh","kind":"spike"}
		]`)
		replan := f.card(model.Card{ID: "replan", Kind: model.KindReplan, Tier: model.TierT3, Channel: ch.ID})
		attempt := f.attempt(replan, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		keep := f.mustCard("keep")
		fresh := cardByTitle(f, "Fresh")
		if keep.Title != "edited" || keep.ID != "keep" || fresh.ID == "fresh" || !hasDepID(keep, fresh.ID) {
			t.Fatalf("keep %+v fresh %+v", keep, fresh)
		}
		if f.mustCard("replan").Status != model.CardDone {
			t.Fatalf("replan %s", f.mustCard("replan").Status)
		}
	})
}

func TestBudgetLimitedContinuesOnce(t *testing.T) {
	cases := []struct {
		name string
		hard int64
	}{
		{name: "room remains", hard: 5000},
		{name: "hard tokens unset", hard: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			obj := f.objective()
			obj.Budget.HardTokens = tc.hard
			obj.Budget.SoftTokens = 0
			if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
				t.Fatal(err)
			}
			a := f.channel("ch-a", "a")
			card := f.card(model.Card{ID: "dev", Channel: a.ID})
			first := f.attempt(card, 0, model.OutcomeBudgetLimited, a.ID, "")
			f.step()
			got := f.mustCard("dev")
			if got.Status != model.CardReady || got.Channel != a.ID || got.Base != first.Branch || len(f.questions()) != 0 {
				t.Fatalf("continue %+v questions %+v", got, f.questions())
			}
			f.step()
			if f.mustCard("dev").Status != model.CardReady || len(f.questions()) != 0 {
				t.Fatal("the same attempt was treated as a second continuation")
			}
			f.attempt(f.mustCard("dev"), 1, model.OutcomeBudgetLimited, a.ID, "")
			f.step()
			got = f.mustCard("dev")
			n := 0
			for _, question := range f.questions() {
				if question.Kind == model.QuestionBudget && question.Scope == "dev" && question.Answer == "" {
					n++
				}
			}
			if got.Status != model.CardNeedsInput || got.Channel != a.ID || n != 1 {
				t.Fatalf("card %+v questions %+v", got, f.questions())
			}
			f.step()
			if f.mustCard("dev").Status != model.CardNeedsInput || len(f.questions()) != 1 {
				t.Fatal("second step continued again")
			}
		})
	}
}

func TestReplanTriggerConsumed(t *testing.T) {
	t.Run("reopen after the replan is terminal", func(t *testing.T) {
		f := newFx(t, model.ModeAuto)
		f.setNodeStatus(f.node.ID, model.NodeReopened)
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 1 {
			t.Fatalf("replans %+v", cardsKind(f, model.KindReplan))
		}
		finishReplan(t, f)
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 1 {
			t.Fatal("the same reopen created another replan")
		}
		f.setNodeStatus(f.node.ID, model.NodeFrozen)
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 1 {
			t.Fatal("leaving reopened created a replan")
		}
		f.setNodeStatus(f.node.ID, model.NodeReopened)
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 2 {
			t.Fatalf("new reopen replans %+v", cardsKind(f, model.KindReplan))
		}
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 2 {
			t.Fatal("second step repeated the new reopen")
		}
	})

	t.Run("breaker after the replan is terminal", func(t *testing.T) {
		f, ch := breakerWorld(t)
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "boom")
		f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "boom")
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 1 || len(questionsKind(f, model.QuestionBreaker)) != 1 {
			t.Fatalf("cards %+v questions %+v", f.cards(), f.questions())
		}
		if f.mustCard("sib").Status != model.CardPaused || f.mustCard("else").Status != model.CardReady {
			t.Fatalf("sib %s else %s", f.mustCard("sib").Status, f.mustCard("else").Status)
		}
		finishReplan(t, f)
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 1 || len(questionsKind(f, model.QuestionBreaker)) != 1 {
			t.Fatal("the same attempts created another replan")
		}
		if f.mustCard("else").Status != model.CardReady || f.mustCard("sib").Status != model.CardPaused {
			t.Fatal("the held breaker moved another node")
		}
		f.attempt(f.mustCard("dev"), 2, model.OutcomeTestsFailed, ch.ID, "boom")
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 2 || len(questionsKind(f, model.QuestionBreaker)) != 1 {
			t.Fatalf("new trigger replans %d questions %+v", len(cardsKind(f, model.KindReplan)), f.questions())
		}
		if f.mustCard("else").Status != model.CardReady {
			t.Fatal("new trigger paused the other node")
		}
	})

	t.Run("an unfinished replan suppresses a new trigger", func(t *testing.T) {
		f, ch := breakerWorld(t)
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "boom")
		f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "boom")
		f.step()
		f.attempt(f.mustCard("dev"), 2, model.OutcomeTestsFailed, ch.ID, "boom")
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 1 {
			t.Fatal("unfinished replan did not suppress the new trigger")
		}
		finishReplan(t, f)
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 2 || len(questionsKind(f, model.QuestionBreaker)) != 1 {
			t.Fatalf("replans %d questions %+v", len(cardsKind(f, model.KindReplan)), f.questions())
		}
	})

	t.Run("manual mode creates none", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.setNodeStatus(f.node.ID, model.NodeReopened)
		ch := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "boom")
		f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "boom")
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 0 || len(questionsKind(f, model.QuestionBreaker)) != 0 {
			t.Fatalf("manual %+v", f.questions())
		}
	})
}

func finishReplan(t *testing.T, f *fx) {
	t.Helper()
	replans := cardsKind(f, model.KindReplan)
	if len(replans) != 1 || TerminalCard(replans[0].Status) {
		t.Fatalf("replan %+v", replans)
	}
	replans[0].Status = model.CardDone
	if err := f.db.UpdateCard(f.ctx, &replans[0]); err != nil {
		t.Fatal(err)
	}
}

type autoRoute struct {
	ok      bool
	channel string
	model   string
	exclude string
}

func latestAuto(f *fx, cardID string) autoRoute {
	f.t.Helper()
	var last autoRoute
	for _, event := range f.events("auto_route") {
		if event.Entity != "card:"+cardID {
			continue
		}
		var data struct {
			Channel string `json:"channel"`
			Model   string `json:"model"`
			Exclude string `json:"exclude"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			continue
		}
		last = autoRoute{ok: true, channel: data.Channel, model: data.Model, exclude: data.Exclude}
	}
	return last
}

func (f *fx) otherObjective(id string) model.Objective {
	f.t.Helper()
	obj := &model.Objective{
		ID: id, Project: f.obj.Project, Title: id,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive,
		Origin: "file:" + id, IntegrationBranch: "integration-" + id,
	}
	if err := f.db.CreateObjective(f.ctx, obj); err != nil {
		f.t.Fatal(err)
	}
	return *obj
}

func (f *fx) otherNode(objectiveID, id string) model.DesignNode {
	f.t.Helper()
	node := &model.DesignNode{ID: id, Objective: objectiveID, Layer: model.LayerDetail, Status: model.NodeFrozen}
	if err := f.db.CreateNode(f.ctx, node); err != nil {
		f.t.Fatal(err)
	}
	return *node
}

func (f *fx) otherUsage(channelID string, tokens int64) {
	f.t.Helper()
	obj := f.otherObjective("obj-usage-" + channelID)
	node := f.otherNode(obj.ID, "node-usage-"+channelID)
	card := f.card(model.Card{
		ID: "usage-" + channelID, Objective: obj.ID, Node: node.ID,
		Status: model.CardDone, Channel: channelID,
	})
	attempt := f.attempt(card, 0, model.OutcomePassed, channelID, "")
	attempt.Usage.Tokens = tokens
	attempt.EndedAt = time.Now().UTC()
	if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) otherRunning(channelID string) {
	f.t.Helper()
	obj := f.otherObjective("obj-run-" + channelID)
	node := f.otherNode(obj.ID, "node-run-"+channelID)
	f.card(model.Card{
		ID: "run-" + channelID, Objective: obj.ID, Node: node.ID,
		Status: model.CardRunning, Channel: channelID,
	})
}

func (f *fx) position(modelName, family string, tiers ...model.Tier) {
	f.t.Helper()
	row := &model.Positioning{Model: modelName, Family: family, Tiers: tiers, Preference: 1, Version: 1}
	if err := f.db.CreatePositioning(f.ctx, row); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) saveChannel(ch model.Channel) {
	f.t.Helper()
	if err := f.db.UpdateChannel(f.ctx, &ch); err != nil {
		f.t.Fatal(err)
	}
}
