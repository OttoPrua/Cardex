package sched

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestDesignPhaseErrorStoresEventAndDisposes(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	git(t, wt, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	proj := model.Project{ID: "p", RepoPath: wt, TestCmd: "true", DesignDir: "docs"}
	if err := db.CreateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	obj := model.Objective{
		ID: "auto", Project: proj.ID, Title: "auto", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "inv", Objective: obj.ID, Kind: model.KindResearch, Title: "盘点",
		Body: "Q0\n", Tier: model.TierT1, Status: model.CardDone,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	// A terminal inventory card whose absorbed Q0 is not JSON makes designStep
	// fail inside inventoryQ0, after the project row exists so §5 can run.
	if err := db.AppendEvent(ctx, &model.Event{Entity: "card:inv", Type: "loop.absorbed", Data: []byte("{")}); err != nil {
		t.Fatal(err)
	}
	attempt := model.Attempt{
		ID: "a-open", Card: card.ID, Index: 0, Worktree: wt, StartedAt: time.Now().UTC(),
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	decoy := t.TempDir()
	t.Setenv("CARDEX_HOME", decoy)
	if err := New(home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CARDEX_HOME") != decoy {
		t.Fatalf("CARDEX_HOME leaked: %s", os.Getenv("CARDEX_HOME"))
	}
	events, err := db.ListEvents(ctx, "objective:auto")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range events {
		if event.Type == "design_error" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("design_error events %d %+v", n, events)
	}
	got, err := db.GetAttempt(ctx, "a-open")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome == "" {
		t.Fatal("open design attempt was not disposed")
	}
	if !eventsOf(t, db, "a-open", "dev_disposed") {
		t.Fatal("missing dev_disposed from the existing disposition")
	}
}
