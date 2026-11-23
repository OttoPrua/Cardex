package loop

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// An rv- review with no dependency writes the verdict to the prefixed card
// when that card is stored.
func TestRVPrefixReviewWritesExistingSubject(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardReviewing})
	mkCard(t, f, model.Card{ID: "rv-dev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardRunning})
	disposeReviewText(t, f, "rv-dev", "## 结论\nok\n\n## 复核结论\npass\n")
	if got := rvCardStatus(t, f, "rv-dev"); got != model.CardDone {
		t.Fatalf("review status %s", got)
	}
	if got := rvCardStatus(t, f, "dev"); got != model.CardMergeQueued {
		t.Fatalf("dev status %s, want merge_queued", got)
	}
	if !hasCardEvent(t, f, "dev", "review_verdict") {
		t.Fatal("verdict not recorded on dev")
	}
}

// An explicit dependency that is not stored does not receive the verdict.
func TestMissingReviewDepDoesNotWriteParent(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "rv-dev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardRunning, Deps: []string{"missing"}})
	disposeReviewText(t, f, "rv-dev", "## 结论\nok\n\n## 复核结论\npass\n")
	got, err := f.db.GetCard(f.ctx, "rv-dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CardNeedsInput || !strings.Contains(got.WaitReason, "missing") {
		t.Fatalf("status %s reason %q", got.Status, got.WaitReason)
	}
	if _, err := f.db.GetCard(f.ctx, "missing"); err == nil {
		t.Fatal("missing subject was created")
	}
	if hasCardEvent(t, f, "missing", "review_verdict") {
		t.Fatal("verdict written to a card that does not exist")
	}
	if !hasCardEvent(t, f, "rv-dev", "review_subject_missing") {
		t.Fatal("missing subject was not recorded")
	}
}

// A review pinned standalone at launch does not adopt a card created later,
// even when that card's id is the rv- suffix.
func TestPinnedStandaloneIgnoresSubjectCreatedLater(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "rv-future", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardRunning})
	mkCard(t, f, model.Card{ID: "future", Kind: model.KindDev, Status: model.CardPaused})
	exit := 0
	attempt := model.Attempt{ID: "a-rv-future", Card: "rv-future", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC()}
	if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	pinReviewAttempt(t, f, attempt.ID, "rv-future", true)
	text := "## 结论\nok\n\n## 复核结论\npass\n"
	if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	card, _ := f.db.GetCard(f.ctx, "rv-future")
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
	if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: text}); err != nil {
		t.Fatal(err)
	}
	if got := rvCardStatus(t, f, "future"); got != model.CardPaused {
		t.Fatalf("future status %s, want paused", got)
	}
	if hasCardEvent(t, f, "future", "review_verdict") {
		t.Fatal("verdict written to a card that was not the pinned subject")
	}
	if got := rvCardStatus(t, f, "rv-future"); got != model.CardDone {
		t.Fatalf("review status %s, want done", got)
	}
	if !hasCardEvent(t, f, "rv-future", "review_verdict") {
		t.Fatal("standalone verdict was not recorded on the review")
	}
}

// A pin naming an existing card still writes that card, not a later guess.
func TestPinnedSubjectWritesThatCard(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardReviewing})
	mkCard(t, f, model.Card{ID: "other", Kind: model.KindDev, Status: model.CardReviewing})
	mkCard(t, f, model.Card{ID: "rv-dev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardRunning})
	exit := 0
	attempt := model.Attempt{ID: "a-rv-dev", Card: "rv-dev", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC()}
	if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	pinReviewAttempt(t, f, attempt.ID, "dev", false)
	text := "## 结论\nok\n\n## 复核结论\npass\n"
	if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	card, _ := f.db.GetCard(f.ctx, "rv-dev")
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
	if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: text}); err != nil {
		t.Fatal(err)
	}
	if got := rvCardStatus(t, f, "dev"); got != model.CardMergeQueued {
		t.Fatalf("dev status %s, want merge_queued", got)
	}
	if got := rvCardStatus(t, f, "other"); got != model.CardReviewing {
		t.Fatalf("other status %s", got)
	}
	if !hasCardEvent(t, f, "dev", "review_verdict") || hasCardEvent(t, f, "other", "review_verdict") {
		t.Fatal("verdict did not stay on the pinned subject")
	}
}

// An open review pinned standalone is not reused when the suffix card passes.
func TestPinnedStandaloneReviewIsNotReused(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "future", Kind: model.KindDev, Status: model.CardRunning})
	mkCard(t, f, model.Card{ID: "rv-future", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardPaused})
	exit := 0
	devAttempt := model.Attempt{ID: "a-future", Card: "future", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC(), Route: model.Route{Channel: "codex"}}
	rvAttempt := model.Attempt{ID: "a-rv-future", Card: "rv-future", Index: 0, Exit: &exit, EndedAt: time.Now().UTC()}
	for _, attempt := range []*model.Attempt{&devAttempt, &rvAttempt} {
		if err := f.db.CreateAttempt(f.ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	pinReviewAttempt(t, f, rvAttempt.ID, "rv-future", true)
	card, _ := f.db.GetCard(f.ctx, "future")
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), devAttempt.ID), f.db)
	if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed}); err != nil {
		t.Fatal(err)
	}
	if got := rvCardStatus(t, f, "future"); got != model.CardReviewing {
		t.Fatalf("future status %s, want reviewing", got)
	}
	if got := rvCardStatus(t, f, "rv-future"); got != model.CardPaused {
		t.Fatalf("standalone review changed to %s", got)
	}
	if hasCardEvent(t, f, "future", "review_reused") {
		t.Fatal("pinned standalone review was reused")
	}
}

func pinReviewAttempt(t *testing.T, f *fx, attemptID, subject string, standalone bool) {
	t.Helper()
	raw, err := json.Marshal(model.PinnedReviewSubject{Subject: subject, Standalone: standalone})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.AppendEvent(f.ctx, &model.Event{
		Entity: "attempt:" + attemptID,
		Type:   model.ReviewSubjectEvent,
		Data:   raw,
	}); err != nil {
		t.Fatal(err)
	}
}

// A prefix pass merges the dev card only when the attempt read that card's
// current candidate commit. A pass read from main stays on the review.
func TestPrefixPassMatchesCandidateCommit(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "base")
	mainHead := run("rev-parse", "HEAD")
	run("checkout", "-b", "cardex/obj/c/dev")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "candidate")
	candidate := run("rev-parse", "HEAD")
	run("checkout", "main")
	if mainHead == candidate {
		t.Fatal("candidate commit matches main")
	}

	for _, tc := range []struct {
		name   string
		base   string
		status model.CardStatus
	}{
		{name: "matches", base: candidate, status: model.CardMergeQueued},
		{name: "main", base: mainHead, status: model.CardPaused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			proj, err := f.db.GetProject(f.ctx, "p")
			if err != nil {
				t.Fatal(err)
			}
			proj.RepoPath = repo
			if err := f.db.UpdateProject(f.ctx, &proj); err != nil {
				t.Fatal(err)
			}
			mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardPaused})
			mkCard(t, f, model.Card{ID: "rv-dev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardRunning})
			exit := 0
			attempt := model.Attempt{ID: "a-rv-dev", Card: "rv-dev", Worktree: f.home, BaseCommit: tc.base, Exit: &exit, EndedAt: time.Now().UTC()}
			if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			pinReviewAttempt(t, f, attempt.ID, "dev", false)
			text := "## 结论\nok\n\n## 复核结论\npass\n"
			if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			card, _ := f.db.GetCard(f.ctx, "rv-dev")
			ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
			if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: text}); err != nil {
				t.Fatal(err)
			}
			if got := rvCardStatus(t, f, "dev"); got != tc.status {
				t.Fatalf("dev status %s, want %s", got, tc.status)
			}
			if got := rvCardStatus(t, f, "rv-dev"); got != model.CardDone {
				t.Fatalf("review status %s", got)
			}
			onDev := hasCardEvent(t, f, "dev", "review_verdict")
			if tc.status == model.CardMergeQueued && !onDev {
				t.Fatal("matching pass did not record the verdict on dev")
			}
			if tc.status != model.CardMergeQueued && onDev {
				t.Fatal("main pass recorded the verdict on dev")
			}
			if mismatch := hasCardEvent(t, f, "rv-dev", "review_candidate_mismatch"); mismatch != (tc.status != model.CardMergeQueued) {
				t.Fatalf("mismatch event %v", mismatch)
			}
		})
	}
}

// --dep wins over an rv- prefix when the verdict is written back.
func TestReviewDepWinsOverPrefix(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardReviewing})
	mkCard(t, f, model.Card{ID: "other", Kind: model.KindDev, Status: model.CardReviewing})
	mkCard(t, f, model.Card{ID: "rv-dev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardRunning, Deps: []string{"other"}})
	disposeReviewText(t, f, "rv-dev", "## 结论\nok\n\n## 复核结论\npass\n")
	if got := rvCardStatus(t, f, "other"); got != model.CardMergeQueued {
		t.Fatalf("other status %s", got)
	}
	if got := rvCardStatus(t, f, "dev"); got != model.CardReviewing {
		t.Fatalf("dev status %s", got)
	}
	if !hasCardEvent(t, f, "other", "review_verdict") || hasCardEvent(t, f, "dev", "review_verdict") {
		t.Fatal("verdict landed on the wrong card")
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
