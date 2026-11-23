package sched

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
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
	orphan := model.Card{ID: "rv-gone", Objective: "o", Kind: model.KindReview, Title: "orphan", Tier: model.TierT3, Status: model.CardReady}
	if err := db.CreateCard(ctx, &orphan); err != nil {
		t.Fatal(err)
	}
	card, objID, cont, ro, err := r.workspaceTarget(ctx, db, orphan, "o")
	if err != nil || card != "rv-gone" || objID != "o" || !cont || !ro {
		t.Fatalf("orphan review target %s %s %v %v %v", card, objID, cont, ro, err)
	}
	a := model.Attempt{ID: "a-orphan", Card: "rv-gone", Index: 0, BaseCommit: "from-review"}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if got := reviewFallbackBase(ctx, db, card, "own"); got != "from-review" {
		t.Fatalf("fallback base %q", got)
	}

	linked := model.Card{ID: "rv-dev", Objective: "o", Kind: model.KindReview, Title: "prefix", Tier: model.TierT3, Status: model.CardReady}
	card, objID, _, _, err = r.workspaceTarget(ctx, db, linked, "o")
	if err != nil || card != "dev" || objID != "o" {
		t.Fatalf("prefix review target %s %s %v", card, objID, err)
	}

	explicit := model.Card{ID: "rv-dev", Objective: "o", Kind: model.KindReview, Title: "dep", Tier: model.TierT3, Deps: []string{"other"}, Status: model.CardReady}
	card, _, _, _, err = r.workspaceTarget(ctx, db, explicit, "o")
	if err != nil || card != "other" {
		t.Fatalf("dep review target %s %v", card, err)
	}

	missing := model.Card{ID: "rv-dev", Objective: "o", Kind: model.KindReview, Title: "bad", Tier: model.TierT3, Deps: []string{"nope"}, Status: model.CardReady}
	_, _, _, _, err = r.workspaceTarget(ctx, db, missing, "o")
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
		{ID: "rv-gone", Objective: obj, Kind: model.KindReview, Title: "standalone", Tier: model.TierT3, Status: model.CardReady, Body: reviewPassScript},
		{ID: "rv-c1", Objective: obj, Kind: model.KindReview, Title: "prefix", Tier: model.TierT3, Status: model.CardReady, Body: reviewPassScript},
		{ID: "rv-other", Objective: obj, Kind: model.KindReview, Title: "dep wins", Tier: model.TierT3, Status: model.CardReady, Deps: []string{"c1"}, Body: reviewPassScript},
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
		ID: "rv-gone", Objective: obj, Kind: model.KindReview, Title: "bad dep", Tier: model.TierT3,
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
