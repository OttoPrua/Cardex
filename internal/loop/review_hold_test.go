package loop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestReworkReviewKeepsTier(t *testing.T) {
	t.Run("small diff stays at the prior tier", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "author")
		dev := f.card(model.Card{ID: "dev", Status: model.CardHarvesting, Channel: ch.ID, Tier: model.TierT2})
		f.card(model.Card{
			ID: "rev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardDone,
			Deps: []string{"dev"}, Title: "prior review",
		})
		att := f.attempt(dev, 1, model.OutcomePassed, ch.ID, "")
		dir, base, head := holdRepo(t)
		att.Worktree, att.BaseCommit, att.HeadCommit = dir, base, head
		if err := f.db.UpdateAttempt(f.ctx, &att); err != nil {
			t.Fatal(err)
		}
		f.step()
		fresh := readyReview(t, f, "dev")
		if fresh.Tier != model.TierT3 || fresh.Channel != "" || fresh.Model != "" {
			t.Fatalf("review %+v", fresh)
		}
		if !strings.Contains(fresh.Body, ReviewHoldMarker) || !strings.Contains(fresh.Body, "tier=T3") {
			t.Fatalf("body %s", fresh.Body)
		}
		if f.mustCard("dev").Status != model.CardReviewing {
			t.Fatalf("dev %s", f.mustCard("dev").Status)
		}
	})

	t.Run("prior channel and model are copied", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("hold", "fam", "model-a")
		f.position("model-a", "fam", model.TierT3)
		dev := f.card(model.Card{ID: "dev", Status: model.CardHarvesting, Channel: ch.ID, Tier: model.TierT2})
		f.card(model.Card{
			ID: "rev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardDone,
			Deps: []string{"dev"}, Channel: ch.ID, Model: "model-a", Title: "prior review",
		})
		att := f.attempt(dev, 1, model.OutcomePassed, ch.ID, "")
		dir, base, head := holdRepo(t)
		att.Worktree, att.BaseCommit, att.HeadCommit = dir, base, head
		if err := f.db.UpdateAttempt(f.ctx, &att); err != nil {
			t.Fatal(err)
		}
		f.step()
		fresh := readyReview(t, f, "dev")
		if fresh.Tier != model.TierT3 || fresh.Channel != ch.ID || fresh.Model != "model-a" {
			t.Fatalf("review %+v", fresh)
		}
		if !strings.Contains(fresh.Body, "channel="+ch.ID) || !strings.Contains(fresh.Body, "model=model-a") {
			t.Fatalf("body %s", fresh.Body)
		}
	})

	t.Run("model below the floor asks instead of lowering", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("hold", "fam", "model-a")
		f.position("model-a", "fam", model.TierT1)
		dev := f.card(model.Card{ID: "dev", Status: model.CardHarvesting, Channel: ch.ID, Tier: model.TierT2})
		f.card(model.Card{
			ID: "rev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardDone,
			Deps: []string{"dev"}, Channel: ch.ID, Model: "model-a", Title: "prior review",
		})
		att := f.attempt(dev, 1, model.OutcomePassed, ch.ID, "")
		dir, base, head := holdRepo(t)
		att.Worktree, att.BaseCommit, att.HeadCommit = dir, base, head
		if err := f.db.UpdateAttempt(f.ctx, &att); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("dev")
		if got.Status != model.CardNeedsInput || !strings.Contains(got.WaitReason, "review hold unmet") || !strings.Contains(got.WaitReason, "T3") || !strings.Contains(got.WaitReason, "model-a") {
			t.Fatalf("dev %+v", got)
		}
		if len(cardsKind(f, model.KindReview)) != 1 {
			t.Fatalf("reviews %+v", cardsKind(f, model.KindReview))
		}
		if len(f.questions()) != 1 || !strings.Contains(f.questions()[0].Text, "review hold unmet") {
			t.Fatalf("questions %+v", f.questions())
		}
		if len(f.events("review_hold_unmet")) != 1 {
			t.Fatalf("events %+v", f.events("review_hold_unmet"))
		}
	})

	t.Run("borrowed tier and inherited model survive rework", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("borrowed", "fam", "model-a")
		f.position("model-a", "fam", model.TierT2)
		dev := f.card(model.Card{ID: "dev", Status: model.CardHarvesting, Tier: model.TierT2})
		prior := f.card(model.Card{
			ID: "rev", Kind: model.KindReview, Tier: model.TierT1, Status: model.CardDone,
			Deps: []string{dev.ID},
		})
		reviewAttempt := f.attempt(prior, 0, model.OutcomePassed, ch.ID, "")
		reviewAttempt.Route.Model = "model-a"
		reviewAttempt.RouteReason.Dispatch = &model.RouteDispatch{Model: "model-a", Tier: model.TierT1, ActualTier: model.TierT2}
		reviewAttempt.RouteReason.Candidates = []model.RouteCandidate{{Channel: ch.ID, Model: "model-a", Tier: model.TierT2}}
		if err := f.db.UpdateAttempt(f.ctx, &reviewAttempt); err != nil {
			t.Fatal(err)
		}
		if err := markDevDisposed(f.ctx, f.db, reviewAttempt, reviewAttempt.Outcome); err != nil {
			t.Fatal(err)
		}
		att := f.attempt(dev, 1, model.OutcomePassed, ch.ID, "")
		att.Worktree, att.BaseCommit, att.HeadCommit = holdRepo(t)
		if err := f.db.UpdateAttempt(f.ctx, &att); err != nil {
			t.Fatal(err)
		}
		f.step()
		fresh := readyReview(t, f, dev.ID)
		if fresh.Tier != model.TierT2 || fresh.Model != "model-a" {
			t.Fatalf("review %+v", fresh)
		}
	})

	t.Run("first review of a small diff stays T1", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "author")
		dev := f.card(model.Card{ID: "dev", Status: model.CardHarvesting, Channel: ch.ID, Tier: model.TierT2})
		att := f.attempt(dev, 0, model.OutcomePassed, ch.ID, "")
		dir, base, head := holdRepo(t)
		att.Worktree, att.BaseCommit, att.HeadCommit = dir, base, head
		if err := f.db.UpdateAttempt(f.ctx, &att); err != nil {
			t.Fatal(err)
		}
		f.step()
		fresh := readyReview(t, f, "dev")
		if fresh.Tier != model.TierT1 || strings.Contains(fresh.Body, ReviewHoldMarker) {
			t.Fatalf("review %+v body %s", fresh, fresh.Body)
		}
	})
}

func TestReviewHoldRoundTrip(t *testing.T) {
	body := AppendReviewHold("复核模式：review，档位 T3。", model.TierT3, "hold", "model-a")
	tier, channel, modelName, ok := ReviewHold(model.Card{Body: body})
	if !ok || tier != model.TierT3 || channel != "hold" || modelName != "model-a" {
		t.Fatalf("hold %s %s %s %v", tier, channel, modelName, ok)
	}
	plan := raiseReviewFloor(reviewPlan{Mode: model.ReviewCheck, Tier: model.TierT1, Why: "completion check"}, model.Card{Tier: model.TierT3, Review: string(model.ReviewFull)})
	if plan.Mode != model.ReviewFull || plan.Tier != model.TierT3 {
		t.Fatalf("plan %+v", plan)
	}
}

func readyReview(t *testing.T, f *fx, dep string) model.Card {
	t.Helper()
	var fresh model.Card
	n := 0
	for _, item := range cardsKind(f, model.KindReview) {
		if item.Status == model.CardReady && hasDepID(item, dep) {
			fresh = item
			n++
		}
	}
	if n != 1 {
		t.Fatalf("ready reviews %d among %+v", n, cardsKind(f, model.KindReview))
	}
	return fresh
}

func holdRepo(t *testing.T) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	holdGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	holdGit(t, dir, "add", "note.txt")
	holdGit(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	base = strings.TrimSpace(holdGitOut(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	holdGit(t, dir, "add", "note.txt")
	holdGit(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "head")
	head = strings.TrimSpace(holdGitOut(t, dir, "rev-parse", "HEAD"))
	return dir, base, head
}

func holdGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func holdGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
