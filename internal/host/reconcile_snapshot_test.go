package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestReconcileStaleLiveSnapshotCannotRewindStrictDisposition(t *testing.T) {
	for _, phase := range []string{"proven-no-executor", "prestart-objective-paused"} {
		t.Run(phase, func(t *testing.T) {
			ctx := t.Context()
			home := t.TempDir()
			db, err := store.Open(filepath.Join(home, "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			seedStrictHostObjective(t, db)
			card := model.Card{ID: "strict", Objective: "strict-objective", Kind: model.KindDev, Status: model.CardRunning, StrictDeps: true}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			// PID 1 is deliberately not probed/signaled by processAlive;
			// no group files or browser exist in the synthetic home.
			a := model.Attempt{ID: "a-old", Card: card.ID, PID: 1, Worktree: filepath.Join(home, "fixture"),
				Branch: "cardex/strict-objective/c/strict", BaseCommit: "synthetic", StartedAt: time.Now().UTC().Add(-time.Hour)}
			if err := db.CreateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			observed, err := db.ListAttempts(ctx, "")
			if err != nil || len(observed) != 1 || !LiveHost(observed[0]) {
				t.Fatalf("scan: %+v %v", observed, err)
			}
			current := observed[0]

			// Deterministic barrier: scan already returned the live row,
			// but the old Reconcile worker has not persisted its loss.
			if err := db.WithWriteTx(ctx, func(ctx context.Context) error {
				if phase == "proven-no-executor" {
					code := 127
					current.Exit, current.EndedAt = &code, time.Now().UTC()
					current.ErrorExcerpt = "synthetic no process from Start"
					if err := db.UpdateAttempt(ctx, &current); err != nil {
						return err
					}
					data, _ := json.Marshal(map[string]string{"checkout_id": current.StrictCheckoutID()})
					return db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + current.ID, Type: model.StrictNoExecutorEvent, Data: data})
				}
				current.PID = 0
				current.StartedAt = time.Time{}
				if err := db.UpdateAttempt(ctx, &current); err != nil {
					return err
				}
				return db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + current.ID, Type: EventDependencyDeferred,
					Data: []byte(`{"reason":"objective strict-objective is paused"}`)})
			}); err != nil {
				t.Fatal(err)
			}
			want, err := db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantEvents, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil || len(wantEvents) != 1 {
				t.Fatalf("before old write: %+v %v", wantEvents, err)
			}
			if err := reconcileSnapshot(ctx, db, home, observed, time.Now().UTC().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			// Model the later race too: external cleanup was already done
			// when another writer changed the claim, so only the atomic
			// persistence check can now stop the stale row.
			if err := persistLostHost(ctx, db, observed[0], time.Now().UTC().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			got, err := db.GetAttempt(ctx, a.ID)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("stale reconcile overwrote current state: got=%+v want=%+v err=%v", got, want, err)
			}
			gotEvents, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil || !reflect.DeepEqual(gotEvents, wantEvents) {
				t.Fatalf("stale reconcile changed exact receipt or added host_lost: got=%+v want=%+v err=%v", gotEvents, wantEvents, err)
			}
		})
	}
}
