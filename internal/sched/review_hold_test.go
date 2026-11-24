package sched

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/route"
)

func TestHeldReviewDoesNotDropPin(t *testing.T) {
	home, db, repo := newHome(t)
	ctx := context.Background()
	_, obj := seedRepo(t, db, repo, "true")
	addPinChannel(t, db, pinChannel{id: "cha", modelName: "model-a", health: model.HealthAuthFailed, pref: 0})
	addPinChannel(t, db, pinChannel{id: "chb", modelName: "model-b", pref: 9})
	dev := model.Card{ID: "dev", Objective: obj, Kind: model.KindDev, Title: "dev", Status: model.CardReviewing}
	if err := db.CreateCard(ctx, &dev); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "rv", Objective: obj, Kind: model.KindReview, Title: "rework review", Tier: model.TierT3,
		Status: model.CardReady, Channel: "cha", Model: "model-a",
		Body:       loop.AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "cha", "model-a"),
		Deps:       []string{"dev"},
		Acceptance: []string{"true"},
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) { return "PROMPT", nil }
	stopLaunchHosts(t, r)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	got, err := db.GetCard(ctx, "rv")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CardNeedsInput || got.Channel != "cha" || got.Model != "model-a" {
		t.Fatalf("card %+v", got)
	}
	attempts, err := db.ListAttempts(ctx, "rv")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 {
		t.Fatalf("attempts %+v", attempts)
	}
	assertOneQuestion(t, ctx, db, obj, "rv", "review hold unmet")
	assertNoPinEvent(t, ctx, db, "rv")
	assertObjectiveUntouched(t, ctx, db, obj, "", "")
}

func TestHeldReviewRouteDoesNotLowerModel(t *testing.T) {
	home, db, repo := newHome(t)
	ctx := context.Background()
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	channels := []model.Channel{{
		ID: "cha", Runner: model.RunnerFake, Family: "fam", SourceGroup: "fam",
		Models: []string{"low", "high"}, Concurrency: 2, Health: model.HealthOK, Tier: model.TierT1,
	}}
	pos := []model.Positioning{
		{Model: "low", Family: "fam", Tiers: []model.Tier{model.TierT1}},
		{Model: "high", Family: "fam", Tiers: []model.Tier{model.TierT3}},
	}
	r := New(home)
	body := loop.AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "cha", "low")

	t.Run("pinned model below the floor asks", func(t *testing.T) {
		card := model.Card{
			ID: "rv-low", Objective: obj.ID, Kind: model.KindReview, Title: "rework review", Tier: model.TierT3,
			Status: model.CardReady, Channel: "cha", Model: "low", Body: body, Deps: []string{"dev"},
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		rt, _, _, asked, err := r.chooseHeldReviewRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierT3)
		if err != nil || !asked || rt.Channel != "" || rt.Model != "" {
			t.Fatalf("route %+v asked %v err %v", rt, asked, err)
		}
		got, err := db.GetCard(ctx, card.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.CardNeedsInput || got.Channel != "cha" || got.Model != "low" {
			t.Fatalf("card %+v", got)
		}
		assertOneQuestion(t, ctx, db, obj.ID, card.ID, "review hold unmet")
		assertNoPinEvent(t, ctx, db, card.ID)
	})

	t.Run("channel pin uses a model that meets the floor", func(t *testing.T) {
		card := model.Card{
			ID: "rv-ch", Objective: obj.ID, Kind: model.KindReview, Title: "rework review", Tier: model.TierT3,
			Status: model.CardReady, Channel: "cha",
			Body: loop.AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "cha", ""),
		}
		before := append([]string(nil), channels[0].Models...)
		rt, _, _, asked, err := r.chooseHeldReviewRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierT3)
		if err != nil || asked || rt.Channel != "cha" || rt.Model != "high" {
			t.Fatalf("route %+v asked %v err %v", rt, asked, err)
		}
		if strings.Join(channels[0].Models, ",") != strings.Join(before, ",") {
			t.Fatalf("channels mutated %v", channels[0].Models)
		}
	})

	t.Run("pinned model at the floor stays", func(t *testing.T) {
		card := model.Card{
			ID: "rv-high", Objective: obj.ID, Kind: model.KindReview, Title: "rework review", Tier: model.TierT3,
			Status: model.CardReady, Channel: "cha", Model: "high",
			Body: loop.AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "cha", "high"),
		}
		rt, _, _, asked, err := r.chooseHeldReviewRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierT3)
		if err != nil || asked || rt.Channel != "cha" || rt.Model != "high" {
			t.Fatalf("route %+v asked %v err %v", rt, asked, err)
		}
	})

	t.Run("old marker restores a missing model pin", func(t *testing.T) {
		card := model.Card{
			ID: "rv-marker", Objective: obj.ID, Kind: model.KindReview, Tier: model.TierT3,
			Status: model.CardReady, Channel: "cha",
			Body: loop.AppendReviewHold("held", model.TierT3, "cha", "low"),
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		rt, _, _, asked, err := r.resolveLaunchRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierT3)
		if err != nil || !asked || rt.Model != "" {
			t.Fatalf("route %+v asked %v err %v", rt, asked, err)
		}
		assertOneQuestion(t, ctx, db, obj.ID, card.ID, "review hold unmet")
	})
}

func TestHeldReviewWaitsForConcurrency(t *testing.T) {
	home, db, repo := newHome(t)
	ctx := context.Background()
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	channels := []model.Channel{{ID: "reviewer", Family: "reviewer", Models: []string{"strong"}, Health: model.HealthOK, Concurrency: 1}}
	pos := []model.Positioning{{Model: "strong", Tiers: []model.Tier{model.TierT3}}}
	r := New(home)
	for _, pin := range []struct{ id, channel, name string }{
		{"auto", "", ""}, {"pinned", "reviewer", "strong"},
	} {
		t.Run(pin.id, func(t *testing.T) {
			card := model.Card{ID: pin.id, Objective: objID, Kind: model.KindReview, Status: model.CardReady, Tier: model.TierT3,
				Body: loop.AppendReviewHold("held", model.TierT3, pin.channel, pin.name)}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			rt, _, _, settled, err := r.resolveLaunchRoute(ctx, db, card, obj, channels, pos, map[string]int{"reviewer": 1}, nil, model.TierNone)
			if (!errors.Is(err, route.ErrNoCandidate) && !errors.Is(err, route.ErrPinnedUnavailable)) || settled || rt.Channel != "" {
				t.Fatalf("busy route %+v settled %v err %v", rt, settled, err)
			}
			if got, err := db.GetCard(ctx, card.ID); err != nil || got.Status != model.CardReady {
				t.Fatalf("card %+v err %v", got, err)
			}
			if questions, err := db.ListQuestions(ctx, objID); err != nil || len(questions) != 0 {
				t.Fatalf("questions %+v err %v", questions, err)
			}
			rt, reason, _, settled, err := r.resolveLaunchRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierNone)
			if err != nil || settled || rt.Channel != "reviewer" || reason.SelectedTier(rt) != model.TierT3 {
				t.Fatalf("free route %+v reason %+v settled %v err %v", rt, reason, settled, err)
			}
		})
	}
}

func TestReviewSummaryIgnoresAuthorPinAndRetainsRetryTier(t *testing.T) {
	home, db, repo := newHome(t)
	ctx := context.Background()
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	obj.Channel, obj.Model = "author", "draft"
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	addPinChannel(t, db, pinChannel{id: "author", modelName: "draft"})
	addPinChannel(t, db, pinChannel{id: "reviewer", modelName: "strong"})
	channels, err := db.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The cross-family review has to borrow T2 from its requested T1.
	pos := []model.Positioning{
		{Model: "draft", Family: "author", Tiers: []model.Tier{model.TierT1}},
		{Model: "strong", Family: "reviewer", Tiers: []model.Tier{model.TierT2}},
	}
	for _, row := range pos {
		row.ID = "pos-" + row.Family
		if err := db.UpdatePositioning(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	card := model.Card{ID: "rv", Objective: objID, Kind: model.KindReview, Title: "review", Status: model.CardReady,
		Tier: model.TierT1, AuthorFamily: "author", Deps: []string{"dev"}}
	dev := model.Card{ID: "dev", Objective: objID, Kind: model.KindDev, Title: "dev", Status: model.CardReviewing}
	for _, c := range []*model.Card{&dev, &card} {
		if err := db.CreateCard(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) { return "PROMPT", nil }
	r.Launch = func(context.Context, string, string) (int, error) { return 0, errors.New("fake launch failure") }
	if _, err := r.launchCard(ctx, db, card, obj, channels, pos, nil, nil); err != nil {
		t.Fatal(err)
	}
	loadSummary := func(id string) {
		t.Helper()
		sum, err := harvest.LoadSummary(harvest.SummaryPath(home, id))
		if err != nil || !sum.Match || sum.Runner != string(model.RunnerFake) || sum.Channel != "reviewer" || sum.Model != "strong" ||
			sum.Tier != model.TierT2 || sum.Expected != (harvest.ExpectedRun{Tier: "T1"}) {
			t.Fatalf("summary %+v err %v", sum, err)
		}
	}
	if failed, err := db.ListAttempts(ctx, card.ID); err != nil || len(failed) != 1 || failed[0].Outcome != model.OutcomeLaunchFailed {
		t.Fatalf("failed attempts %+v err %v", failed, err)
	} else {
		loadSummary(failed[0].ID)
	}
	r.Launch = func(context.Context, string, string) (int, error) { return 84, nil }
	if ch, err := r.launchCard(ctx, db, card, obj, channels, pos, nil, nil); err != nil || ch != "reviewer" {
		t.Fatalf("retry channel %q err %v", ch, err)
	}
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts %+v err %v", attempts, err)
	}
	attempt := newestAttempt(attempts)
	if err := os.WriteFile(filepath.Join(attempt.Worktree, ".cardex-result.md"), []byte("## 复核结论\npass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		if legacy {
			attempt.RouteReason.Dispatch = nil
		}
		res, err := harvest.Harvest(ctx, r.harvestInput(ctx, db, card, attempt, 0, nil))
		if err != nil || res.EvidenceErr != nil || res.Outcome != model.OutcomePassed {
			t.Fatalf("legacy %v result %+v err %v", legacy, res, err)
		}
		loadSummary(attempt.ID)
	}
	card.Body = loop.AppendReviewHold("held", model.TierT2, "", "")
	rt, reason, _, settled, err := r.resolveLaunchRoute(ctx, db, card, obj, channels, pos, nil, nil, model.TierNone)
	if err != nil || settled || rt.Channel != "reviewer" || reason.SelectedTier(rt) != model.TierT2 {
		t.Fatalf("held route %+v reason %+v settled %v err %v", rt, reason, settled, err)
	}
}
