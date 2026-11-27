package sched

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestLaunchWritesInheritedHardRules(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	projectID, objectiveID := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	project, err := db.GetProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	project.HardRules = []string{"Synthetic project rule"}
	if err := db.UpdateProject(ctx, &project); err != nil {
		t.Fatal(err)
	}
	objective, err := db.GetObjective(ctx, objectiveID)
	if err != nil {
		t.Fatal(err)
	}
	objective.HardRules = []string{"Synthetic objective rule"}
	if err := db.UpdateObjective(ctx, &objective); err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "rules-card", Objective: objectiveID, Kind: model.KindDev, Status: model.CardReady, Title: "Synthetic card", Tier: model.TierT2, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 61, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempt count %d: %v", len(attempts), err)
	}
	raw, err := os.ReadFile(attempts[0].PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "- Synthetic project rule\n- Synthetic objective rule\n") {
		t.Fatal("launch prompt lost inherited rules")
	}
}
