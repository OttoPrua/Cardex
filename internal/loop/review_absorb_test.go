package loop

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
)

func mkCard(t *testing.T, f *fx, c model.Card) {
	t.Helper()
	c.Objective = f.obj.ID
	if c.Tier == "" {
		c.Tier = model.TierT2
	}
	if c.Title == "" {
		c.Title = c.ID
	}
	if err := f.db.CreateCard(f.ctx, &c); err != nil {
		t.Fatal(err)
	}
}

func rvCardStatus(t *testing.T, f *fx, id string) model.CardStatus {
	t.Helper()
	c, err := f.db.GetCard(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return c.Status
}

func hasCardEvent(t *testing.T, f *fx, id, typ string) bool {
	t.Helper()
	events, err := f.db.ListEvents(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Entity == "card:"+id && e.Type == typ {
			return true
		}
	}
	return false
}

// A passed dev attempt whose card already has a paused review must still leave
// running: the attempt is disposed and nothing revisits it later.
func TestPassedDevWithPausedReviewGoesReviewing(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardRunning})
	mkCard(t, f, model.Card{ID: "rv-dev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardPaused, Deps: []string{"dev"}})
	exit := 0
	attempt := model.Attempt{ID: "a-dev", Card: "dev", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC(), Route: model.Route{Channel: "codex"}}
	if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	card, _ := f.db.GetCard(f.ctx, "dev")
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
	if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed}); err != nil {
		t.Fatal(err)
	}
	if got := rvCardStatus(t, f, "dev"); got != model.CardReviewing {
		t.Fatalf("dev status %s, want reviewing", got)
	}
	if n := len(reviewDeps(t, f, "dev")); n != 1 {
		t.Fatalf("%d review cards, want the existing one only", n)
	}
	if got := rvCardStatus(t, f, "rv-dev"); got != model.CardPaused {
		t.Fatalf("paused review changed to %s", got)
	}
	if !hasCardEvent(t, f, "dev", "review_reused") {
		t.Fatal("missing review_reused event")
	}
}

// A standalone review (no deps) with a valid verdict is finished, not unparsable.
func TestStandaloneReviewVerdictFinishes(t *testing.T) {
	for _, verdict := range []string{"pass", "rework"} {
		t.Run(verdict, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			mkCard(t, f, model.Card{ID: "rv-solo", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardRunning})
			text := "## 结论\nok\n\n## 复核结论\n" + verdict + "\n"
			if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			exit := 0
			attempt := model.Attempt{ID: "a-rv", Card: "rv-solo", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC()}
			if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			card, _ := f.db.GetCard(f.ctx, "rv-solo")
			ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
			if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: text}); err != nil {
				t.Fatal(err)
			}
			if got := rvCardStatus(t, f, "rv-solo"); got != model.CardDone {
				t.Fatalf("status %s, want done", got)
			}
			if hasCardEvent(t, f, "rv-solo", "review_unparsable") {
				t.Fatal("standalone review marked unparsable")
			}
			if !hasCardEvent(t, f, "rv-solo", "review_verdict") {
				t.Fatal("verdict not recorded")
			}
		})
	}
}

// AbsorbPassed repairs a card already stuck by the old ensureReview early return.
func TestAbsorbPassedRepairsStuckCard(t *testing.T) {
	for _, withReview := range []bool{true, false} {
		name := "creates-review"
		if withReview {
			name = "reuses-paused-review"
		}
		t.Run(name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardRunning})
			if withReview {
				mkCard(t, f, model.Card{ID: "rv-dev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardPaused, Deps: []string{"dev"}})
			}
			exit := 0
			attempt := model.Attempt{ID: "a-dev", Card: "dev", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC(), Outcome: model.OutcomePassed, Route: model.Route{Channel: "codex"}}
			if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			// Not disposed yet: refuse.
			if _, err := AbsorbPassed(f.ctx, f.db, f.home, "dev"); err == nil {
				t.Fatal("absorbed an undisposed attempt")
			}
			if err := f.db.AppendEvent(f.ctx, &model.Event{Entity: "attempt:a-dev", Type: "dev_disposed", Data: []byte(`{}`), TS: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			res, err := AbsorbPassed(f.ctx, f.db, f.home, "dev")
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != model.CardReviewing || rvCardStatus(t, f, "dev") != model.CardReviewing {
				t.Fatalf("result %+v", res)
			}
			reviews := reviewDeps(t, f, "dev")
			if len(reviews) != 1 || res.Review != reviews[0].ID {
				t.Fatalf("reviews %+v result %+v", reviews, res)
			}
			if withReview && res.ReviewStatus != model.CardPaused {
				t.Fatalf("review status %s", res.ReviewStatus)
			}
			if !withReview && res.ReviewStatus != model.CardReady {
				t.Fatalf("new review status %s", res.ReviewStatus)
			}
			// Second call: card no longer running.
			if _, err := AbsorbPassed(f.ctx, f.db, f.home, "dev"); err == nil {
				t.Fatal("absorbed twice")
			}
		})
	}
}
