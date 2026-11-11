package loop

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestDisposeAndDevStepShareDecision(t *testing.T) {
	t.Run("Dispose passed dev", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		card := model.Card{
			ID: "dev", Objective: f.obj.ID, Kind: model.KindDev, Title: "dev",
			Tier: model.TierT2, Status: model.CardRunning,
		}
		if err := f.db.CreateCard(f.ctx, &card); err != nil {
			t.Fatal(err)
		}
		exit := 0
		attempt := model.Attempt{
			ID: "a-dev", Card: "dev", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC(),
		}
		if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
		if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed}); err != nil {
			t.Fatal(err)
		}
		if card.Status != model.CardReviewing {
			t.Fatalf("status %s", card.Status)
		}
		if len(reviewDeps(t, f, "dev")) != 1 {
			t.Fatal("Dispose did not dispatch a review")
		}
	})
	t.Run("DevStep passed dev", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		card := model.Card{
			ID: "dev", Objective: f.obj.ID, Kind: model.KindDev, Title: "dev",
			Tier: model.TierT2, Status: model.CardRunning,
		}
		if err := f.db.CreateCard(f.ctx, &card); err != nil {
			t.Fatal(err)
		}
		exit := 0
		attempt := model.Attempt{
			ID: "a-dev", Card: "dev", Worktree: f.home, Outcome: model.OutcomePassed,
			Exit: &exit, EndedAt: time.Now().UTC(),
		}
		if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		if err := DevStep(f.ctx, f.obj.ID); err != nil {
			t.Fatal(err)
		}
		if len(reviewDeps(t, f, "dev")) != 1 {
			t.Fatal("DevStep did not dispatch a review")
		}
	})
	t.Run("Dispose review pass", func(t *testing.T) {
		parent, review := disposeReview(t, "## 结论\nrework the boundary\n\n## 复核结论\npass\n")
		if parent.Status != model.CardMergeQueued {
			t.Fatalf("parent %s", parent.Status)
		}
		if review.Status != model.CardDone {
			t.Fatalf("review %s", review.Status)
		}
	})
	t.Run("Dispose review rework", func(t *testing.T) {
		parent, review := disposeReview(t, "## 结论\ntests: pass\n\n## 复核结论\nrework\n")
		if parent.Status == model.CardMergeQueued || parent.ReworkCount != 1 {
			t.Fatalf("parent %+v", parent)
		}
		if review.Status != model.CardDone {
			t.Fatalf("review %s", review.Status)
		}
	})
	t.Run("Dispose design", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		card := model.Card{
			ID: "des", Objective: f.obj.ID, Kind: model.KindDesign, Title: "des",
			Tier: model.TierT3, Status: model.CardRunning,
		}
		if err := f.db.CreateCard(f.ctx, &card); err != nil {
			t.Fatal(err)
		}
		exit := 0
		attempt := model.Attempt{
			ID: "a-des", Card: "des", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC(),
		}
		if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
		res := harvest.Result{Outcome: model.OutcomePassed, ResultText: "## 结论\nship the design\n"}
		if err := Dispose(ctx, &card, res); err != nil {
			t.Fatal(err)
		}
		if card.Status != model.CardDone {
			t.Fatalf("status %s", card.Status)
		}
	})
}

func disposeReview(t *testing.T, text string) (model.Card, model.Card) {
	t.Helper()
	f := newFx(t, model.ModeManual)
	parent := model.Card{
		ID: "c1", Objective: f.obj.ID, Kind: model.KindDev, Title: "c1",
		Tier: model.TierT2, Status: model.CardReviewing,
	}
	if err := f.db.CreateCard(f.ctx, &parent); err != nil {
		t.Fatal(err)
	}
	review := model.Card{
		ID: "rv", Objective: f.obj.ID, Kind: model.KindReview, Title: "rv",
		Tier: model.TierT3, Status: model.CardRunning, Deps: []string{"c1"},
	}
	if err := f.db.CreateCard(f.ctx, &review); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	exit := 0
	attempt := model.Attempt{
		ID: "a-rv", Card: "rv", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC(),
	}
	if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
	if err := Dispose(ctx, &review, harvest.Result{Outcome: model.OutcomePassed, ResultText: text}); err != nil {
		t.Fatal(err)
	}
	parent, err := f.db.GetCard(f.ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	return parent, review
}

func reviewDeps(t *testing.T, f *fx, id string) []model.Card {
	t.Helper()
	cards, err := f.db.ListCards(f.ctx, f.obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Card
	for _, card := range cards {
		if card.Kind != model.KindReview {
			continue
		}
		for _, dep := range card.Deps {
			if dep == id {
				out = append(out, card)
			}
		}
	}
	return out
}
