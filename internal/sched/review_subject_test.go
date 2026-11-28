package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

func TestReviewSubjectResolution(t *testing.T) {
	ctx := context.Background()
	_, db, _ := newHome(t)
	proj := model.Project{ID: "p", RepoPath: t.TempDir()}
	if err := db.CreateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	obj := model.Objective{ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main"}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	dev := model.Card{ID: "dev", Objective: "o", Kind: model.KindDev, Title: "dev", Tier: model.TierT2, Status: model.CardDone}
	other := model.Card{ID: "other", Objective: "o", Kind: model.KindDev, Title: "other", Tier: model.TierT2, Status: model.CardDone}
	for _, c := range []*model.Card{&dev, &other} {
		if err := db.CreateCard(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	r := &Runner{}
	orphan := model.Card{ID: "rv-gone", Objective: "o", Kind: model.KindReview, Title: "orphan", Tier: model.TierT1, Status: model.CardReady}
	if err := db.CreateCard(ctx, &orphan); err != nil {
		t.Fatal(err)
	}
	card, objID, cont, ro, pin, err := r.workspaceTarget(ctx, db, orphan, "o")
	if err != nil || card != "rv-gone" || objID != "o" || !cont || !ro || !pin.Standalone || pin.Subject != "rv-gone" {
		t.Fatalf("orphan review target %s %s %v %v %+v %v", card, objID, cont, ro, pin, err)
	}
	a := model.Attempt{ID: "a-orphan", Card: "rv-gone", Index: 0, BaseCommit: "from-review"}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if got := reviewFallbackBase(ctx, db, card, "own"); got != "from-review" {
		t.Fatalf("fallback base %q", got)
	}

	linked := model.Card{ID: "rv-dev", Objective: "o", Kind: model.KindReview, Title: "prefix", Tier: model.TierT1, Status: model.CardReady}
	card, objID, _, _, pin, err = r.workspaceTarget(ctx, db, linked, "o")
	if err != nil || card != "dev" || objID != "o" || pin.Standalone || pin.Subject != "dev" {
		t.Fatalf("prefix review target %s %s %+v %v", card, objID, pin, err)
	}

	explicit := model.Card{ID: "rv-dev", Objective: "o", Kind: model.KindReview, Title: "dep", Tier: model.TierT1, Deps: []string{"other"}, Status: model.CardReady}
	card, _, _, _, pin, err = r.workspaceTarget(ctx, db, explicit, "o")
	if err != nil || card != "other" || pin.Standalone || pin.Subject != "other" {
		t.Fatalf("dep review target %s %+v %v", card, pin, err)
	}

	missing := model.Card{ID: "rv-dev", Objective: "o", Kind: model.KindReview, Title: "bad", Tier: model.TierT1, Deps: []string{"nope"}, Status: model.CardReady}
	_, _, _, _, _, err = r.workspaceTarget(ctx, db, missing, "o")
	if err == nil || strings.Contains(err.Error(), "no rows") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("missing dep error %v", err)
	}
}

func TestIndependentRVReviewDispatches(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	branch(t, repo, obj, "c1", "unique.txt", "candidate-not-on-main\n")
	cards := []model.Card{
		{ID: "c1", Objective: obj, Kind: model.KindDev, Title: "c1", Tier: model.TierT2, Status: model.CardReviewing},
		{ID: "other", Objective: obj, Kind: model.KindDev, Title: "other", Tier: model.TierT2, Status: model.CardDone},
		{ID: "rv-gone", Objective: obj, Kind: model.KindReview, Title: "standalone", Tier: model.TierT1, Status: model.CardReady, Body: reviewPassScript},
		{ID: "rv-c1", Objective: obj, Kind: model.KindReview, Title: "prefix", Tier: model.TierT1, Status: model.CardReady, Body: reviewPassScript},
		{ID: "rv-other", Objective: obj, Kind: model.KindReview, Title: "dep wins", Tier: model.TierT1, Status: model.CardReady, Deps: []string{"c1"}, Body: reviewPassScript},
	}
	for i := range cards {
		if err := db.CreateCard(ctx, &cards[i]); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	assertReviewWorktree := func(id string, wantCandidate bool) {
		t.Helper()
		attempts, err := db.ListAttempts(ctx, id)
		if err != nil || len(attempts) != 1 || attempts[0].Worktree == "" {
			t.Fatalf("%s attempts %+v %v", id, attempts, err)
		}
		body, err := os.ReadFile(filepath.Join(attempts[0].Worktree, "unique.txt"))
		has := err == nil && strings.Contains(string(body), "candidate-not-on-main")
		if has != wantCandidate {
			t.Fatalf("%s candidate %v body %q err %v", id, wantCandidate, body, err)
		}
	}
	assertReviewWorktree("rv-gone", false)
	assertReviewWorktree("rv-c1", true)
	assertReviewWorktree("rv-other", true)
	if branchExists(repo, "cardex/"+obj+"/c/gone") || branchExists(repo, "cardex/"+obj+"/c/rv-gone") {
		t.Fatal("standalone review created a branch for the missing subject or its own id")
	}
	if branchExists(repo, "cardex/"+obj+"/c/rv-c1") {
		t.Fatal("prefix review created a branch from its own id")
	}
	head := rev(t, reviewAttempt(t, db, "rv-c1").Worktree, "HEAD")
	if head != rev(t, repo, "cardex/"+obj+"/c/c1") {
		t.Fatalf("prefix review HEAD %s is not c1", head)
	}
	head = rev(t, reviewAttempt(t, db, "rv-other").Worktree, "HEAD")
	if head != rev(t, repo, "cardex/"+obj+"/c/c1") {
		t.Fatalf("dep review HEAD %s is not c1", head)
	}
	if n := countEvents(t, db, "", model.EventTickError); n != 0 {
		t.Fatalf("tick_error count %d", n)
	}
}

func TestMissingReviewDepParksOnce(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{
		ID: "rv-gone", Objective: obj, Kind: model.KindReview, Title: "bad dep", Tier: model.TierT1,
		Status: model.CardReady, Deps: []string{"nope"}, Body: reviewPassScript,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	for i := 0; i < 2; i++ {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	got, err := db.GetCard(ctx, "rv-gone")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CardNeedsInput || !strings.Contains(got.WaitReason, "nope") {
		t.Fatalf("status %s reason %q", got.Status, got.WaitReason)
	}
	attempts, err := db.ListAttempts(ctx, "rv-gone")
	if err != nil || len(attempts) != 0 {
		t.Fatalf("attempts %+v %v", attempts, err)
	}
	if _, err := db.GetCard(ctx, "nope"); err == nil {
		t.Fatal("missing subject was created")
	}
	if n := countEvents(t, db, "card:rv-gone", "review_subject_missing"); n != 1 {
		t.Fatalf("review_subject_missing count %d", n)
	}
	if n := countEvents(t, db, "", model.EventTickError); n != 0 {
		t.Fatalf("tick_error count %d", n)
	}
}

// Dispatch pins the subject that exists at launch. A card created afterwards
// keeps its own status when the standalone review passes.
func TestPinnedStandaloneSurvivesLaterSubject(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{
		ID: "rv-future", Objective: obj, Kind: model.KindReview, Title: "standalone", Tier: model.TierT1,
		Status: model.CardReady, Body: reviewPassScript,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempt := reviewAttempt(t, db, "rv-future")
	if rev(t, attempt.Worktree, "HEAD") != rev(t, repo, "main") {
		t.Fatal("standalone review did not start from main")
	}
	if body, err := os.ReadFile(filepath.Join(attempt.Worktree, "unique.txt")); err == nil && strings.Contains(string(body), "candidate-not-on-main") {
		t.Fatal("standalone review checked out a candidate branch")
	}
	pin := pinnedSubjectEvent(t, db, attempt.ID)
	if !pin.Standalone || pin.Subject != "rv-future" {
		t.Fatalf("pin %+v", pin)
	}

	branch(t, repo, obj, "future", "unique.txt", "candidate-not-on-main\n")
	future := model.Card{
		ID: "future", Objective: obj, Kind: model.KindDev, Title: "future", Tier: model.TierT2, Status: model.CardPaused,
	}
	if err := db.CreateCard(ctx, &future); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attempt.Worktree, ".cardex-result.md"), []byte("## 复核结论\npass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exit := 0
	attempt.Exit = &exit
	attempt.EndedAt = time.Now().UTC()
	if err := db.UpdateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCard(ctx, "future")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CardPaused {
		t.Fatalf("future status %s, want paused", got.Status)
	}
	if n := countEvents(t, db, "card:future", "review_verdict"); n != 0 {
		t.Fatalf("future review_verdict count %d", n)
	}
	review, err := db.GetCard(ctx, "rv-future")
	if err != nil || review.Status != model.CardDone {
		t.Fatalf("review %+v %v", review, err)
	}
	if n := countEvents(t, db, "card:rv-future", "review_verdict"); n != 1 {
		t.Fatalf("review verdict count %d", n)
	}
}

// A prefix review launched while the subject exists still reviews that card.
func TestPinnedPrefixReviewsExistingSubject(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	branch(t, repo, obj, "present", "unique.txt", "candidate-not-on-main\n")
	for _, card := range []model.Card{
		{ID: "present", Objective: obj, Kind: model.KindDev, Title: "present", Tier: model.TierT2, Status: model.CardPaused},
		{ID: "rv-present", Objective: obj, Kind: model.KindReview, Title: "prefix", Tier: model.TierT1, Status: model.CardReady, Body: reviewPassScript},
	} {
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempt := reviewAttempt(t, db, "rv-present")
	if rev(t, attempt.Worktree, "HEAD") != rev(t, repo, "cardex/"+obj+"/c/present") {
		t.Fatal("prefix review did not use the subject branch")
	}
	pin := pinnedSubjectEvent(t, db, attempt.ID)
	if pin.Standalone || pin.Subject != "present" {
		t.Fatalf("pin %+v", pin)
	}
	if err := os.WriteFile(filepath.Join(attempt.Worktree, ".cardex-result.md"), []byte("## 复核结论\npass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exit := 0
	attempt.Exit = &exit
	attempt.EndedAt = time.Now().UTC()
	if err := db.UpdateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	// Keep the queued subject from being merged in the same tick.
	stored, err := db.GetObjective(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = model.ObjectivePaused
	if err := db.UpdateObjective(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCard(ctx, "present")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CardMergeQueued {
		t.Fatalf("present status %s, want merge_queued", got.Status)
	}
	if n := countEvents(t, db, "card:present", "review_verdict"); n != 1 {
		t.Fatalf("present review_verdict count %d", n)
	}
}

// A missing first dependency is parked before a later dependency can hide it.
func TestMissingFirstDepBeforeGate(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	cards := []model.Card{
		{ID: "gate", Objective: obj, Kind: model.KindDev, Title: "gate", Tier: model.TierT2, Status: model.CardPaused},
		{ID: "dev", Objective: obj, Kind: model.KindDev, Title: "dev", Tier: model.TierT2, Status: model.CardPaused},
		{ID: "rv-old", Objective: obj, Kind: model.KindReview, Title: "old", Tier: model.TierT1, Status: model.CardReady, Deps: []string{"bad", "gate"}, Body: reviewPassScript},
		{ID: "rv-ok", Objective: obj, Kind: model.KindReview, Title: "ok", Tier: model.TierT1, Status: model.CardReady, Deps: []string{"dev", "gate"}, Body: reviewPassScript},
	}
	for i := range cards {
		if err := db.CreateCard(ctx, &cards[i]); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	for i := 0; i < 2; i++ {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	old, err := db.GetCard(ctx, "rv-old")
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != model.CardNeedsInput || !strings.Contains(old.WaitReason, "bad") || old.WaitReason == "" {
		t.Fatalf("old status %s reason %q", old.Status, old.WaitReason)
	}
	if n := countEvents(t, db, "card:rv-old", "review_subject_missing"); n != 1 {
		t.Fatalf("review_subject_missing count %d", n)
	}
	ok, err := db.GetCard(ctx, "rv-ok")
	if err != nil {
		t.Fatal(err)
	}
	if ok.Status != model.CardReady || ok.WaitReason != "" {
		t.Fatalf("gated review status %s reason %q", ok.Status, ok.WaitReason)
	}
	if n := countEvents(t, db, "card:rv-ok", "review_subject_missing"); n != 0 {
		t.Fatalf("valid review missing events %d", n)
	}
	attempts, err := db.ListAttempts(ctx, "rv-ok")
	if err != nil || len(attempts) != 0 {
		t.Fatalf("gated review attempts %+v %v", attempts, err)
	}
	if n := countEvents(t, db, "", model.EventTickError); n != 0 {
		t.Fatalf("tick_error count %d", n)
	}
}

// A prefix review of a dev card stays ready until that card has a branch,
// then checks out the branch and pins the dev card.
func TestPrefixDevWaitsForCandidateBranch(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	cards := []model.Card{
		{ID: "bare", Objective: obj, Kind: model.KindDev, Title: "bare", Tier: model.TierT2, Status: model.CardPaused},
		{ID: "rv-bare", Objective: obj, Kind: model.KindReview, Title: "prefix", Tier: model.TierT1, Status: model.CardReady, Body: reviewPassScript},
	}
	for i := range cards {
		if err := db.CreateCard(ctx, &cards[i]); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	for i := 0; i < 2; i++ {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if attempts, err := db.ListAttempts(ctx, "rv-bare"); err != nil || len(attempts) != 0 {
		t.Fatalf("prefix attempts before branch %+v %v", attempts, err)
	}
	got, err := db.GetCard(ctx, "rv-bare")
	if err != nil || got.Status != model.CardReady || got.WaitReason != "" {
		t.Fatalf("prefix status %s reason %q %v", got.Status, got.WaitReason, err)
	}
	if n := countEvents(t, db, "", model.EventTickError); n != 0 {
		t.Fatalf("tick_error count %d", n)
	}

	branch(t, repo, obj, "bare", "unique.txt", "candidate-not-on-main\n")
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempt := reviewAttempt(t, db, "rv-bare")
	if rev(t, attempt.Worktree, "HEAD") != rev(t, repo, "cardex/"+obj+"/c/bare") {
		t.Fatal("prefix review did not wait for the candidate branch")
	}
	pin := pinnedSubjectEvent(t, db, attempt.ID)
	if pin.Standalone || pin.Subject != "bare" {
		t.Fatalf("prefix pin %+v", pin)
	}
}

// An explicit dependency still reviews a dev card that has no branch yet.
func TestExplicitDepReviewsWithoutCandidateBranch(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	cards := []model.Card{
		{ID: "bare", Objective: obj, Kind: model.KindDev, Title: "bare", Tier: model.TierT2, Status: model.CardPaused},
		{ID: "rv-explicit", Objective: obj, Kind: model.KindReview, Title: "dep", Tier: model.TierT1, Status: model.CardReady, Deps: []string{"bare"}, Body: reviewPassScript},
	}
	for i := range cards {
		if err := db.CreateCard(ctx, &cards[i]); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	explicit := reviewAttempt(t, db, "rv-explicit")
	if rev(t, explicit.Worktree, "HEAD") != rev(t, repo, "main") {
		t.Fatal("explicit review of a branchless card did not start from main")
	}
	pin := pinnedSubjectEvent(t, db, explicit.ID)
	if pin.Standalone || pin.Subject != "bare" {
		t.Fatalf("explicit pin %+v", pin)
	}
	if n := countEvents(t, db, "", model.EventTickError); n != 0 {
		t.Fatalf("tick_error count %d", n)
	}
}

// If a dev card reuses a running review, advancing the candidate requires a
// new attempt before the verdict can be applied to the dev card.
func TestPrefixPassRequiresCandidateCommit(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	branch(t, repo, obj, "present", "unique.txt", "candidate-not-on-main\n")
	for _, card := range []model.Card{
		{ID: "present", Objective: obj, Kind: model.KindDev, Title: "present", Tier: model.TierT2, Status: model.CardRunning},
		{ID: "rv-present", Objective: obj, Kind: model.KindReview, Title: "prefix", Tier: model.TierT1, Status: model.CardReady, Body: reviewPassScript},
	} {
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempt := reviewAttempt(t, db, "rv-present")
	reviewed := attempt.BaseCommit
	if reviewed == "" || reviewed != rev(t, repo, "cardex/"+obj+"/c/present") {
		t.Fatalf("reviewed %s", reviewed)
	}
	name := "cardex/" + obj + "/c/present"
	git(t, repo, "checkout", name)
	if err := os.WriteFile(filepath.Join(repo, "unique.txt"), []byte("moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "unique.txt")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "move")
	git(t, repo, "checkout", "main")
	candidate := rev(t, repo, name)
	if candidate == reviewed {
		t.Fatal("candidate commit did not move")
	}
	dev, err := db.GetCard(ctx, "present")
	if err != nil {
		t.Fatal(err)
	}
	exit := 0
	devAttempt := model.Attempt{ID: "a-new-dev", Card: dev.ID, Branch: name, HeadCommit: candidate, Exit: &exit, EndedAt: time.Now().UTC()}
	if err := db.CreateAttempt(ctx, &devAttempt); err != nil {
		t.Fatal(err)
	}
	if err := r.deliver(ctx, db, &dev, &devAttempt, harvest.Result{Outcome: model.OutcomePassed}); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(t, db, "card:present", "review_reused"); dev.Status != model.CardReviewing || n != 1 {
		t.Fatalf("dev status %s, review_reused count %d", dev.Status, n)
	}
	finish := func(attempt model.Attempt) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(attempt.Worktree, ".cardex-result.md"), []byte("## 复核结论\npass\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		attempt.Exit = &exit
		attempt.EndedAt = time.Now().UTC()
		if err := db.UpdateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
	}
	finish(attempt)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCard(ctx, "present")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CardReviewing {
		t.Fatalf("present status %s, want reviewing", got.Status)
	}
	if n := countEvents(t, db, "card:present", "review_verdict"); n != 0 {
		t.Fatalf("present review_verdict count %d", n)
	}
	if n := countEvents(t, db, "card:rv-present", "review_candidate_mismatch"); n != 1 {
		t.Fatalf("mismatch events %d", n)
	}
	attempts, err := db.ListAttempts(ctx, "rv-present")
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempt count %d, want a replacement review: %v", len(attempts), err)
	}
	replacement := attempts[0]
	if replacement.ID == attempt.ID {
		replacement = attempts[1]
	}
	if replacement.BaseCommit != candidate {
		t.Fatalf("replacement base %s, want %s", replacement.BaseCommit, candidate)
	}
	finish(replacement)
	if err := r.harvest(ctx, db); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetCard(ctx, "present")
	if err != nil || got.Status != model.CardMergeQueued {
		t.Fatalf("present status %s, want merge_queued: %v", got.Status, err)
	}
	if n := countEvents(t, db, "card:present", "review_verdict"); n != 1 {
		t.Fatalf("present review_verdict count %d, want 1", n)
	}
}

// The subject is fixed when the worktree is chosen. A card created while the
// checkout runs does not receive the verdict from that attempt.
func TestSubjectCreatedDuringWorktreeStaysUnpinned(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{
		ID: "rv-late", Objective: obj, Kind: model.KindReview, Title: "standalone", Tier: model.TierT1,
		Status: model.CardReady, Body: reviewPassScript,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	orig := addWorktree
	t.Cleanup(func() { addWorktree = orig })
	addWorktree = func(ctx context.Context, req workspace.CreateRequest) (workspace.Worktree, error) {
		if req.CardID != "rv-late" || !req.Detach {
			t.Fatalf("worktree request card %s detach %v continue %v", req.CardID, req.Detach, req.Continue)
		}
		late := model.Card{
			ID: "late", Objective: obj, Kind: model.KindDev, Title: "late", Tier: model.TierT2, Status: model.CardPaused,
		}
		if err := db.CreateCard(ctx, &late); err != nil {
			t.Fatal(err)
		}
		branch(t, repo, obj, "late", "unique.txt", "candidate-not-on-main\n")
		return orig(ctx, req)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempt := reviewAttempt(t, db, "rv-late")
	if rev(t, attempt.Worktree, "HEAD") != rev(t, repo, "main") {
		t.Fatal("worktree was not the standalone checkout")
	}
	if body, err := os.ReadFile(filepath.Join(attempt.Worktree, "unique.txt")); err == nil && strings.Contains(string(body), "candidate-not-on-main") {
		t.Fatal("standalone worktree contains the candidate created during checkout")
	}
	pin := pinnedSubjectEvent(t, db, attempt.ID)
	if !pin.Standalone || pin.Subject != "rv-late" {
		t.Fatalf("pin %+v", pin)
	}
	if err := os.WriteFile(filepath.Join(attempt.Worktree, ".cardex-result.md"), []byte("## 复核结论\npass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exit := 0
	attempt.Exit = &exit
	attempt.EndedAt = time.Now().UTC()
	if err := db.UpdateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetObjective(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = model.ObjectivePaused
	if err := db.UpdateObjective(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCard(ctx, "late")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CardPaused {
		t.Fatalf("late status %s, want paused", got.Status)
	}
	if n := countEvents(t, db, "card:late", "review_verdict"); n != 0 {
		t.Fatalf("late review_verdict count %d", n)
	}
	review, err := db.GetCard(ctx, "rv-late")
	if err != nil || review.Status != model.CardDone {
		t.Fatalf("review %+v %v", review, err)
	}
}

func pinnedSubjectEvent(t *testing.T, db *store.Store, attemptID string) model.PinnedReviewSubject {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "attempt:"+attemptID)
	if err != nil {
		t.Fatal(err)
	}
	var found model.PinnedReviewSubject
	n := 0
	for _, event := range events {
		if event.Type != model.ReviewSubjectEvent {
			continue
		}
		n++
		if err := json.Unmarshal(event.Data, &found); err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 {
		t.Fatalf("review_subject events %d", n)
	}
	return found
}

func reviewAttempt(t *testing.T, db *store.Store, id string) model.Attempt {
	t.Helper()
	attempts, err := db.ListAttempts(context.Background(), id)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("%s attempts %+v %v", id, attempts, err)
	}
	return attempts[0]
}

func countEvents(t *testing.T, db *store.Store, entity, typ string) int {
	t.Helper()
	events, err := db.ListEvents(context.Background(), entity)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range events {
		if event.Type == typ {
			n++
		}
	}
	return n
}
