package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestEventsEnabledDefaultsOff(t *testing.T) {
	if EventsEnabled("", "") || EventsEnabled("off", "on") || EventsEnabled("", "off") {
		t.Fatal("lifecycle events must stay off unless a project or objective turns them on")
	}
	if !EventsEnabled("on", "") || !EventsEnabled("", "on") || !EventsEnabled("on", "off") {
		t.Fatal("on must enable, and an objective on/off must beat the project")
	}
}

func TestLifecycleIdempotentRetryAndDefaultOff(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db := openLifeDB(t, home)
	seedLife(t, db, "", "")
	calls := installExec(t)
	card := mustLifeCard(t, db)
	card.Status = model.CardMerged
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Fatalf("default off delivered %d", *calls)
	}
	if _, err := os.Stat(lifecyclePath(home)); !os.IsNotExist(err) {
		t.Fatalf("default off wrote a ledger: %v", err)
	}

	card.Status = model.CardReady
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	setEvents(t, db, "on", "")
	card.Status = model.CardMerged
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("project on delivered %d, want 1", *calls)
	}
	if err := FlushLifecycle(ctx, home); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("flush repeated a successful event: %d", *calls)
	}
	card.Status = model.CardReady
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	card.Status = model.CardMerged
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("same merged event delivered again: %d", *calls)
	}

	setEvents(t, db, "on", "off")
	card.Status = model.CardFailed
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("objective off overrode project on: %d", *calls)
	}

	card.Status = model.CardReady
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	setEvents(t, db, "", "on")
	card.Status = model.CardFailed
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("objective on delivered %d, want 2", *calls)
	}
}

func TestLifecycleRetriesThenStops(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db := openLifeDB(t, home)
	seedLife(t, db, "", "on")
	calls := installExec(t)
	prev := lifecycleExec
	lifecycleExec = func(context.Context, []string) error {
		*calls++
		return errors.New("down")
	}
	t.Cleanup(func() { lifecycleExec = prev })
	card := mustLifeCard(t, db)
	card.Status = model.CardNeedsInput
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := FlushLifecycle(ctx, home); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != lifecycleMaxAttempts {
		t.Fatalf("attempts %d, want %d", *calls, lifecycleMaxAttempts)
	}
}

func TestLifecycleReworkAndChannelOnce(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db := openLifeDB(t, home)
	seedLife(t, db, "", "on")
	obj, err := db.GetObjective(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	obj.Origin = "file:box"
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateQuestion(ctx, &model.Question{
		ID: "q-rework", Objective: "o1", Scope: "c1", ScopeKind: "card",
		Kind: model.QuestionCustom, Text: "rework_count would exceed 2 (tests failed)",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "a1", Card: "c1", Route: model.Route{Channel: "ch", Model: "real-model"},
		HeadCommit: "sha-1", ErrorExcerpt: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	card := mustLifeCard(t, db)
	card.Status = model.CardNeedsInput
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	db.NoteLifecycle(ctx, card.ID, EventChannelDegraded, "unavailable: model real-model", "ch")
	db.NoteLifecycle(ctx, card.ID, EventChannelDegraded, "unavailable: model real-model", "ch")
	matches, err := filepath.Glob(filepath.Join(home, "inbox", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("inbox files %d, want 3 (needs_input, rework_limit, channel_degraded once)", len(matches))
	}
	var text string
	for _, match := range matches {
		body, err := os.ReadFile(match)
		if err != nil {
			t.Fatal(err)
		}
		text += string(body)
	}
	for _, status := range []string{`"status":"needs_input"`, `"status":"rework_limit"`, `"status":"channel_degraded"`} {
		if !strings.Contains(text, status) {
			t.Fatalf("missing %s in %s", status, text)
		}
	}
	if !strings.Contains(text, "real-model") || !strings.Contains(text, "sha-1") || !strings.Contains(text, "q-rework") {
		t.Fatalf("pulse fields %s", text)
	}
}

func openLifeDB(t *testing.T, home string) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedLife(t *testing.T, db *store.Store, projectEvents, objectiveEvents string) {
	t.Helper()
	ctx := context.Background()
	if err := db.CreateProject(ctx, &model.Project{
		ID: "p1", RepoPath: t.TempDir(), TestCmd: "true", DesignDir: "docs",
		ForbiddenPaths: []string{}, Events: projectEvents,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "o1", Project: "p1", Title: "obj", Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
		Origin: "codex:thread-1", Status: model.ObjectiveActive, IntegrationBranch: "cardex/o1/main",
		Events: objectiveEvents,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &model.Card{
		ID: "c1", Objective: "o1", Kind: model.KindDev, Title: "card", Status: model.CardReady,
	}); err != nil {
		t.Fatal(err)
	}
}

func setEvents(t *testing.T, db *store.Store, projectEvents, objectiveEvents string) {
	t.Helper()
	ctx := context.Background()
	p, err := db.GetProject(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	p.Events = projectEvents
	if err := db.UpdateProject(ctx, &p); err != nil {
		t.Fatal(err)
	}
	o, err := db.GetObjective(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	o.Events = objectiveEvents
	if err := db.UpdateObjective(ctx, &o); err != nil {
		t.Fatal(err)
	}
}

func mustLifeCard(t *testing.T, db *store.Store) model.Card {
	t.Helper()
	card, err := db.GetCard(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func installExec(t *testing.T) *int {
	t.Helper()
	prev := lifecycleExec
	var calls int
	lifecycleExec = func(context.Context, []string) error {
		calls++
		return nil
	}
	t.Cleanup(func() { lifecycleExec = prev })
	return &calls
}
