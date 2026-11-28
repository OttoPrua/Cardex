package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestStrictExecutionDependencies(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for id, state := range map[string]model.CardStatus{
		"done": model.CardDone, "merged": model.CardMerged, "canceled": model.CardCanceled,
		"failed": model.CardFailed, "waiting": model.CardWaiting, "ready": model.CardReady,
		"needs-input": model.CardNeedsInput, "queued": model.CardMergeQueued,
	} {
		dep := model.Card{ID: id, Kind: model.KindDev, Status: state}
		if err := db.CreateCard(ctx, &dep); err != nil {
			t.Fatal(err)
		}
	}
	for id, status := range map[string]model.CardStatus{"review-rework": model.CardDone, "review-no-proof": model.CardMerged} {
		review := model.Card{ID: id, Kind: model.KindReview, Status: status}
		if err := db.CreateCard(ctx, &review); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AppendEvent(ctx, &model.Event{
		Entity: "card:subject", Type: "review_verdict",
		Data: json.RawMessage(`{"review_card_id":"review-rework","verdict":"rework"}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		draft  string
		allows bool
	}{
		{"done", `{"kind":"dev","strict_deps":true,"deps":["done"]}`, true},
		{"merged", `{"kind":"dev","strict_deps":true,"deps":["merged"]}`, true},
		{"canceled", `{"kind":"dev","strict_deps":true,"deps":["canceled"]}`, false},
		{"failed", `{"kind":"dev","strict_deps":true,"deps":["failed"]}`, false},
		{"waiting", `{"kind":"dev","strict_deps":true,"deps":["waiting"]}`, false},
		{"ready", `{"kind":"dev","strict_deps":true,"deps":["ready"]}`, false},
		{"needs input", `{"kind":"dev","strict_deps":true,"deps":["needs-input"]}`, false},
		{"merge queued", `{"kind":"dev","strict_deps":true,"deps":["queued"]}`, false},
		{"missing", `{"kind":"dev","strict_deps":true,"deps":["missing"]}`, false},
		{"all successful", `{"kind":"dev","strict_deps":true,"deps":["done","merged"]}`, true},
		{"later canceled", `{"kind":"dev","strict_deps":true,"deps":["done","canceled"]}`, false},
		{"no deps", `{"kind":"dev","strict_deps":true}`, true},
		{"legacy array", `{"kind":"dev","deps":["done","canceled"]}`, true},
		{"legacy review still satisfies", `{"kind":"dev","deps":["review-rework"]}`, true},
		{"review rework done is not success", `{"kind":"dev","strict_deps":true,"deps":["review-rework"]}`, false},
		{"review without proof not even merged", `{"kind":"dev","strict_deps":true,"deps":["review-no-proof"]}`, false},
		{"review source remains exempt", `{"kind":"repair","strict_deps":true,"deps":["review-rework","done"]}`, true},
		{"legacy failed", `{"kind":"dev","deps":["failed"]}`, false},
		{"review subject", `{"kind":"review","strict_deps":true,"deps":["failed","done"]}`, true},
		{"review later gate", `{"kind":"review","strict_deps":true,"deps":["failed","canceled"]}`, false},
		{"repair source", `{"kind":"repair","strict_deps":true,"deps":["waiting","done"]}`, true},
		{"rebase source", `{"kind":"rebase","strict_deps":true,"deps":["waiting","done"]}`, true},
		{"normalize source", `{"kind":"normalize","strict_deps":true,"deps":["waiting","done"]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var card model.Card
			if err := json.Unmarshal([]byte(tc.draft), &card); err != nil {
				t.Fatal(err)
			}
			card.Status = model.CardReady
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			card, err := db.GetCard(ctx, card.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := executionDepsDone(ctx, db, card); got != tc.allows {
				t.Fatalf("allows = %t, want %t (draft: %s)", got, tc.allows, tc.draft)
			}
		})
	}
}

func TestStrictSchedulerKeepsDeferredPreparationWithoutInventingPID(t *testing.T) {
	ctx := t.Context()
	home, db, repo := newHome(t)
	_, objective := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	up := model.Card{ID: "up", Objective: objective, Kind: model.KindDev, Status: model.CardDone}
	strict := model.Card{
		ID: "strict", Objective: objective, Kind: model.KindDev, Status: model.CardReady,
		Tier: model.TierT2, StrictDeps: true, Deps: []string{up.ID},
	}
	for _, c := range []*model.Card{&up, &strict} {
		if err := db.CreateCard(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Launch = func(ctx context.Context, home, attemptID string) (int, error) {
		// A previously successful dependency changes after dispatch's first
		// check but before a delayed detached host actually starts.
		up.Status = model.CardCanceled
		if err := db.UpdateCard(ctx, &up); err != nil {
			return 0, err
		}
		return 123, host.Run(ctx, home, attemptID)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListAttempts(ctx, strict.ID)
	if err != nil || len(attempts) != 1 || attempts[0].PID != 0 || !attemptOpen(attempts[0]) {
		t.Fatalf("deferred attempt %+v; error %v", attempts, err)
	}
	got, err := db.GetCard(ctx, strict.ID)
	if err != nil || got.Status != model.CardReady || got.WaitReason != "" {
		t.Fatalf("deferred card %+v; error %v", got, err)
	}
	events, err := db.ListEvents(ctx, "attempt:"+attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == host.EventDependencyDeferred {
			return
		}
	}
	t.Fatal("host deferral event missing")
}

func TestStrictDependenciesChooseLocalDefaultWithoutClassifierSideAttempt(t *testing.T) {
	ctx := t.Context()
	home, db, _ := newHome(t)
	addClassifierChannel(t, db)
	r := New(home)
	calls := 0
	r.Classify = func(context.Context, string, string) (string, error) {
		calls++
		return "TIER: T1\nREASON: synthetic\n", nil
	}
	for _, test := range []struct {
		id             string
		kind           model.CardKind
		configured     model.Tier
		strict         bool
		want, returned model.Tier
		calls          int
	}{
		{"strict-dev", model.KindDev, model.TierNone, true, model.DefaultTier(model.KindDev), model.DefaultTier(model.KindDev), 0},
		{"strict-review", model.KindReview, model.TierNone, true, model.DefaultTier(model.KindReview), model.DefaultTier(model.KindReview), 0},
		{"strict-explicit", model.KindDev, model.TierT3, true, model.TierT3, model.TierNone, 0},
		{"ordinary", model.KindDev, model.TierNone, false, model.TierT1, model.TierT1, 1},
	} {
		card := model.Card{ID: test.id, Kind: test.kind, Tier: test.configured, StrictDeps: test.strict, Status: model.CardReady}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		got, returned, err := r.classifyBeforeRoute(ctx, db, card)
		if err != nil || got.Tier != test.want || returned != test.returned || calls != test.calls {
			t.Fatalf("%s tier/returned=%s/%s calls=%d: %v", test.id, got.Tier, returned, calls, err)
		}
		stored, err := db.GetCard(ctx, test.id)
		if err != nil || stored.Tier != test.want {
			t.Fatalf("%s stored tier=%s: %v", test.id, stored.Tier, err)
		}
	}
	// The producer rejects a strict source independently, even if a future
	// caller gets as far as the side-attempt builder.
	if _, err := r.classifyAttempt(ctx, db, "strict-dev", model.Channel{}, "synthetic"); err == nil || !strings.Contains(err.Error(), "automatic classification is disabled") {
		t.Fatalf("strict side attempt not rejected: %v", err)
	}
	if _, err := db.GetCard(ctx, "cls-strict-dev"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("strict classification side card exists: %v", err)
	}
}
