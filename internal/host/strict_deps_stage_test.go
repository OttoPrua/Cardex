package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestStrictHostRechecksAfterClaimWithoutTouchingAnotherHost(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	up := model.Card{ID: "up", Kind: model.KindDev, Status: model.CardDone}
	seedStrictHostObjective(t, db)
	card := model.Card{ID: "down", Objective: "strict-objective", Kind: model.KindDev, Status: model.CardRunning, StrictDeps: true, Deps: []string{up.ID}}
	for _, c := range []*model.Card{&up, &card} {
		if err := db.CreateCard(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	a := model.Attempt{ID: "a", Card: card.ID, StartedAt: time.Now().Add(-24 * time.Hour).UTC()}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	claimFloor := time.Now().UTC()
	if allowed, err := strictLaunchGate(ctx, db, &a, strictClaim); err != nil || !allowed || a.PID != os.Getpid() ||
		a.StartedAt.Before(claimFloor) || a.StartedAt.After(time.Now().UTC()) {
		t.Fatalf("first claim allowed %t pid %d started %s err %v", allowed, a.PID, a.StartedAt, err)
	}
	up.Status = model.CardWaiting
	if err := db.UpdateCard(ctx, &up); err != nil {
		t.Fatal(err)
	}
	if allowed, err := strictLaunchGate(ctx, db, &a, strictProbe); err == nil || allowed {
		t.Fatalf("duplicate launch handled existing owner: allowed %t err %v", allowed, err)
	}
	owned, err := db.GetAttempt(ctx, a.ID)
	if err != nil || owned.PID != os.Getpid() || !owned.StartedAt.Equal(a.StartedAt) {
		t.Fatalf("duplicate changed owned attempt: %+v err %v", owned, err)
	}
	if allowed, err := strictLaunchGate(ctx, db, &a, strictExec); err != nil || allowed {
		t.Fatalf("current recheck did not defer: allowed %t err %v", allowed, err)
	}
	got, err := db.GetAttempt(ctx, a.ID)
	if err != nil || got.PID != 0 || !got.StartedAt.IsZero() || got.Exit != nil || !got.EndedAt.IsZero() {
		t.Fatalf("deferred attempt %+v err %v", got, err)
	}

	up.Status = model.CardMerged
	if err := db.UpdateCard(ctx, &up); err != nil {
		t.Fatal(err)
	}
	resumeFloor := time.Now().UTC()
	if allowed, err := strictLaunchGate(ctx, db, &got, strictClaim); err != nil || !allowed ||
		got.PID != os.Getpid() || got.StartedAt.Before(resumeFloor) || got.StartedAt.After(time.Now().UTC()) {
		t.Fatalf("second claim allowed %t attempt %+v err %v", allowed, got, err)
	}
}

func TestStrictDirectHostDoesNotRewindNewerAttempt(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedStrictHostObjective(t, db)
	card := model.Card{ID: "strict", Objective: "strict-objective", Kind: model.KindDev, Status: model.CardRunning, StrictDeps: true}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	old := model.Attempt{ID: "old", Card: card.ID, Index: 0}
	newer := model.Attempt{ID: "newer", Card: card.ID, Index: 1, Outcome: model.OutcomePassed, EndedAt: time.Now().UTC()}
	for _, attempt := range []*model.Attempt{&old, &newer} {
		if err := db.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	if allowed, err := strictLaunchGate(ctx, db, &old, strictClaim); err != nil || allowed {
		t.Fatalf("obsolete direct host allowed %t err %v", allowed, err)
	}
	got, err := db.GetCard(ctx, card.ID)
	if err != nil || got.Status != model.CardRunning {
		t.Fatalf("obsolete host changed newer attempt's card %+v err %v", got, err)
	}
	prepared, err := db.GetAttempt(ctx, old.ID)
	if err != nil || prepared.PID != 0 || prepared.Outcome != "" {
		t.Fatalf("obsolete preparation claimed %+v err %v", prepared, err)
	}
	receipt, err := db.GetAttempt(ctx, newer.ID)
	if err != nil || receipt.Outcome != model.OutcomePassed {
		t.Fatalf("newer receipt changed %+v err %v", receipt, err)
	}
}

func seedStrictHostObjective(t *testing.T, db *store.Store) {
	t.Helper()
	if err := db.CreateObjective(t.Context(), &model.Objective{ID: "strict-objective", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive}); err != nil {
		t.Fatal(err)
	}
}

func TestStrictHostDefersObjectivePausedAfterClaim(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedStrictHostObjective(t, db)
	card := model.Card{ID: "strict", Objective: "strict-objective", Status: model.CardRunning, Kind: model.KindDev, StrictDeps: true}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	a := model.Attempt{ID: "prepared", Card: card.ID}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	ok, err := strictLaunchGate(ctx, db, &a, strictClaim)
	if err != nil || !ok {
		t.Fatalf("initial claim %t %v", ok, err)
	}
	obj, err := db.GetObjective(ctx, card.Objective)
	if err != nil {
		t.Fatal(err)
	}
	obj.Status = model.ObjectivePaused
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	ok, err = strictLaunchGate(ctx, db, &a, strictExec)
	if err != nil || ok {
		t.Fatalf("paused objective reached final admission: %t %v", ok, err)
	}
	saved, err := db.GetAttempt(ctx, a.ID)
	if err != nil || saved.PID != 0 || !saved.StartedAt.IsZero() || saved.Exit != nil || saved.Outcome != "" {
		t.Fatalf("deferred preparation not reusable: %+v %v", saved, err)
	}
	current, err := db.GetObjective(ctx, obj.ID)
	if err != nil || current.Status != model.ObjectivePaused {
		t.Fatalf("host altered objective: %+v %v", current, err)
	}
	ok, err = strictLaunchGate(ctx, db, &saved, strictProbe)
	if err != nil || ok {
		t.Fatalf("paused direct host probe: %t %v", ok, err)
	}
	obj.Status = model.ObjectiveDoneCandidate
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	if ok, err := strictLaunchGate(ctx, db, &saved, strictClaim); err != nil || !ok {
		t.Fatalf("resume after objective reopened: %t %v", ok, err)
	}
}
