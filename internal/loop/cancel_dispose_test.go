package loop

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
)

// A card canceled while its attempt was running must stay canceled when the
// killed attempt is harvested: no reroute, retry, rework, or review card.
func TestDisposeCanceledDevStaysCanceled(t *testing.T) {
	for _, outcome := range []model.Outcome{model.OutcomeNoOutput, model.OutcomePassed} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			card := model.Card{
				ID: "dev", Objective: f.obj.ID, Kind: model.KindDev, Title: "dev",
				Tier: model.TierT2, Status: model.CardRunning,
			}
			if err := f.db.CreateCard(f.ctx, &card); err != nil {
				t.Fatal(err)
			}
			exit := 1
			attempt := model.Attempt{
				ID: "a-dev", Card: "dev", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC(),
				Route: model.Route{Channel: "codex"},
			}
			if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			canceled := card
			canceled.Status = model.CardCanceled
			if err := f.db.UpdateCard(f.ctx, &canceled); err != nil {
				t.Fatal(err)
			}
			ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
			if err := Dispose(ctx, &card, harvest.Result{Outcome: outcome}); err != nil {
				t.Fatal(err)
			}
			got, err := f.db.GetCard(f.ctx, "dev")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != model.CardCanceled || got.ReworkCount != 0 {
				t.Fatalf("card revived: %+v", got)
			}
			if n := len(reviewDeps(t, f, "dev")); n != 0 {
				t.Fatalf("%d review cards for a canceled card", n)
			}
			if done, err := attemptDisposed(f.ctx, f.db, attempt.ID); err != nil || !done {
				t.Fatalf("attempt not disposed: %v %v", done, err)
			}
		})
	}
}

func TestReviewVerdictLeavesCanceledParent(t *testing.T) {
	for _, verdict := range []string{"pass", "rework"} {
		t.Run(verdict, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			parent := model.Card{
				ID: "c1", Objective: f.obj.ID, Kind: model.KindDev, Title: "c1",
				Tier: model.TierT2, Status: model.CardCanceled,
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
			text := "## 结论\nok\n\n## 复核结论\n" + verdict + "\n"
			if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			exit := 0
			attempt := model.Attempt{ID: "a-rv", Card: "rv", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC()}
			if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
			if err := Dispose(ctx, &review, harvest.Result{Outcome: model.OutcomePassed, ResultText: text}); err != nil {
				t.Fatal(err)
			}
			got, err := f.db.GetCard(f.ctx, "c1")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != model.CardCanceled || got.ReworkCount != 0 {
				t.Fatalf("parent revived: %+v", got)
			}
			if review.Status != model.CardDone {
				t.Fatalf("review %s", review.Status)
			}
		})
	}
}
