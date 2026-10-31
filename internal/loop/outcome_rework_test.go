package loop

import (
	"strings"
	"testing"

	"cardex/internal/model"
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
		name    string
		outcome model.Outcome
		priors  int
		block   string
		want    string
	}{
		{name: "no_output tier", outcome: model.OutcomeNoOutput, block: "tier", want: "ch-c"},
		{name: "quota_hit redline", outcome: model.OutcomeQuotaHit, block: "redline", want: "ch-c"},
		{name: "auth_failed concurrency", outcome: model.OutcomeAuthFailed, block: "concurrency", want: "ch-c"},
		{name: "transport family", outcome: model.OutcomeTransport, priors: 2, block: "family", want: "ch-c"},
		{name: "timeout tier", outcome: model.OutcomeTimeout, priors: 2, block: "tier", want: "ch-c"},
		{name: "transport stays once", outcome: model.OutcomeTransport, priors: 1, block: "tier", want: "ch-a"},
		{name: "no_output no candidate", outcome: model.OutcomeNoOutput, block: "none", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			a := f.channel("ch-a", "fam-a", "ma")
			b := f.channel("ch-b", "fam-b", "mb")
			var c model.Channel
			if tc.block != "none" {
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
				if got.Status != model.CardNeedsInput || got.Channel == b.ID || got.Model == "mb" || len(f.questions()) != 1 {
					t.Fatalf("card %+v questions %+v", got, f.questions())
				}
				if !strings.Contains(f.questions()[0].Text, "no other channel") {
					t.Fatalf("question %+v", f.questions()[0])
				}
				f.step()
				if len(f.questions()) != 1 || f.mustCard("c").Channel == b.ID {
					t.Fatal("second step assigned a filtered channel")
				}
				return
			}
			if got.Channel != tc.want || got.Status != model.CardReady {
				t.Fatalf("card %+v", got)
			}
			if tc.want == c.ID && got.Model != "mc" {
				t.Fatalf("model %s card %+v", got.Model, got)
			}
			if tc.want == a.ID && got.Channel == b.ID {
				t.Fatalf("retried on %s", got.Channel)
			}
			if tc.outcome == model.OutcomeQuotaHit || tc.outcome == model.OutcomeAuthFailed || tc.outcome == model.OutcomeTransport || tc.outcome == model.OutcomeTimeout {
				if tc.want != a.ID && got.Base != current.Branch {
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
			if f.mustCard("c").Channel != tc.want {
				t.Fatal("second step switched again")
			}
		})
	}
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
