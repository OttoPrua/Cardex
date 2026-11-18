package sched

import (
	"context"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestReviewTargetUsesSubjectObjective(t *testing.T) {
	ctx := context.Background()
	_, db, _ := newHome(t)
	proj := model.Project{ID: "p", RepoPath: t.TempDir()}
	if err := db.CreateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"o-dev", "o-rv"} {
		obj := model.Objective{ID: id, Project: "p", Title: id, Mode: model.ModeManual, FreezeMode: model.FreezeFast,
			Status: model.ObjectiveActive, IntegrationBranch: "main"}
		if err := db.CreateObjective(ctx, &obj); err != nil {
			t.Fatal(err)
		}
	}
	dev := model.Card{ID: "dev", Objective: "o-dev", Kind: model.KindDev, Title: "dev", Tier: model.TierT2, Status: model.CardDone}
	rv := model.Card{ID: "rv", Objective: "o-rv", Kind: model.KindReview, Title: "rv", Tier: model.TierT2, Deps: []string{"dev"}, Status: model.CardWaiting}
	solo := model.Card{ID: "solo", Objective: "o-rv", Kind: model.KindReview, Title: "solo", Tier: model.TierT2, Status: model.CardWaiting}
	for _, c := range []*model.Card{&dev, &rv, &solo} {
		if err := db.CreateCard(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	r := &Runner{}
	card, obj, cont, ro, err := r.workspaceTarget(ctx, db, rv, "o-rv")
	if err != nil || card != "dev" || obj != "o-dev" || !cont || !ro {
		t.Fatalf("cross-objective review target %s %s %v %v %v", card, obj, cont, ro, err)
	}
	card, obj, _, _, err = r.workspaceTarget(ctx, db, solo, "o-rv")
	if err != nil || card != "solo" || obj != "o-rv" {
		t.Fatalf("standalone review target %s %s %v", card, obj, err)
	}

	if got := reviewFallbackBase(ctx, db, "dev", "own"); got != "own" {
		t.Fatalf("no attempts: %q", got)
	}
	for i, base := range []string{"b0", "b1", ""} {
		a := model.Attempt{ID: "a" + string(rune('0'+i)), Card: "dev", Index: i, BaseCommit: base}
		if err := db.CreateAttempt(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	if got := reviewFallbackBase(ctx, db, "dev", "own"); got != "b1" {
		t.Fatalf("latest attempt base %q want b1", got)
	}
}
