package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestLifecycleIdempotentAndDefaultOff(t *testing.T) {
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
	setEvents(t, db, "on", "")
	if due, err := db.DueLifecycle(ctx, time.Now()); err != nil || len(due) != 0 {
		t.Fatalf("default off queued events: %v, %v", due, err)
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
		t.Fatalf("project on delivered %d, want 1", *calls)
	}
	if err := FlushLifecycle(ctx, db); err != nil {
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
		(*calls)++
		return errors.New("down")
	}
	t.Cleanup(func() { lifecycleExec = prev })
	card := mustLifeCard(t, db)
	card.Status = model.CardNeedsInput
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := flushLifecycle(ctx, db, time.Now().Add(time.Duration(i+1)*2*lifecycleRetry)); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != store.LifecycleMaxAttempts {
		t.Fatalf("attempts %d, want %d", *calls, store.LifecycleMaxAttempts)
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
	attempt := model.Attempt{
		ID: "a1", Card: "c1", Route: model.Route{Channel: "ch", Model: "real-model"},
		HeadCommit: "sha-1", ErrorExcerpt: "boom",
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	card := mustLifeCard(t, db)
	card.Status = model.CardNeedsInput
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		if err := db.UpdateCard(ctx, &card); err != nil {
			return err
		}
		return db.CreateQuestion(ctx, &model.Question{
			ID: "q-rework", Objective: "o1", Scope: "c1", ScopeKind: "card",
			Kind: model.QuestionCustom, Text: "rework_count would exceed 2 (tests failed)",
		})
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.NoteChannelDegraded(ctx, card, attempt, "unavailable: model real-model"); err != nil {
			t.Fatal(err)
		}
	}
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

func TestLifecyclePersistsAndClaimsAcrossStores(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	db := openLifeDB(t, home)
	seedLife(t, db, "", "on")
	card := mustLifeCard(t, db)
	card.Status = model.CardFailed
	rollback := errors.New("rollback")
	published := installExec(t)
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		if err := db.UpdateCard(ctx, &card); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rollback result: %v", err)
	}
	if err := FlushLifecycle(ctx, db); err != nil || *published != 0 {
		t.Fatalf("rolled-back event was delivered: calls %d, error %v", *published, err)
	}

	// A writer can exit before delivery begins. A later tick still has the event.
	hook := store.PublishCardNote
	store.PublishCardNote = nil
	err := db.UpdateCard(ctx, &card)
	store.PublishCardNote = hook
	if err != nil {
		t.Fatal(err)
	}

	second := openLifeDB(t, home)
	entered := make(chan []string, 1)
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	exec := lifecycleExec
	t.Cleanup(func() { lifecycleExec = exec })
	lifecycleExec = func(ctx context.Context, argv []string) error {
		if calls.Add(1) == 1 {
			entered <- argv
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- FlushLifecycle(ctx, db) }()
	var argv []string
	select {
	case argv = <-entered:
	case <-ctx.Done():
		t.Fatal("no persisted event delivered")
	}
	if err := FlushLifecycle(ctx, second); err != nil {
		t.Fatal(err)
	}
	var payload store.Pulse
	if err := json.Unmarshal([]byte(argv[len(argv)-1]), &payload); err != nil || payload.ID != "c1" || payload.Status != "failed" {
		t.Fatalf("adapter payload: %+v, %v", payload, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent delivery count = %d", calls.Load())
	}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := FlushLifecycle(ctx, second); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("recorded event delivered again: %d", calls.Load())
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
