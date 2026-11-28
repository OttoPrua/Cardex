package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestLocalPrestartRecoveryCannotDisplaceTypedOwner(t *testing.T) {
	for _, test := range []struct {
		name, table, profile string
		build                bool
	}{
		{"legacy_st1", "STRONG_PRODUCERS", "named-legacy", true},
		{"legacy_st2", "STRONG_RUN_INTENTS", "named-legacy", true},
		{"historical_st1", "STRONG_PRODUCERS", "anthropic-compatible:named", false},
		{"historical_st2", "STRONG_RUN_INTENTS", "anthropic-compatible:named", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			db, err := Open(filepath.Join(t.TempDir(), "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			o := model.Objective{ID: "fixture-o", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive}
			if err := db.CreateObjective(ctx, &o); err != nil {
				t.Fatal(err)
			}
			ch := model.Channel{ID: "fixture-ch", Runner: model.RunnerClaude, Health: model.HealthOK, EngineProfile: test.profile}
			if err := db.CreateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			c := model.Card{ID: "fixture-c", Objective: o.ID, Kind: model.KindDev, Channel: ch.ID, Status: model.CardRunning}
			if err := db.CreateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			// ST1 is already installed by the current v15 Store. Register a
			// genuine synthetic producer while the fixture is still pending,
			// then simulate the uncertain old scheduler timestamp. A failure
			// writer must never overwrite an attempt with this custody.
			a := model.Attempt{ID: "fixture-a", Card: c.ID, Route: model.Route{Channel: ch.ID, Model: "fixture-model"}, Worktree: t.TempDir()}
			if err := db.CreateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			if test.table == "STRONG_PRODUCERS" {
				source := model.StrongStage{Run: "fixture-run", Stage: "fixture-stage", Seat: "fixture-seat", Role: model.StrongBlind}
				registration, err := db.RegisterStrongProducer(ctx, source, a.ID)
				if err != nil || !registration.Valid() || registration.Attempt != a.ID {
					t.Fatalf("can't register synthetic ST1 custody: %v", err)
				}
			} else {
				// The optional ST2 table is not installed in an ordinary
				// home. Keep an uppercase declared-name fixture to exercise
				// lookup of foreign ownership without installing a prototype.
				if _, err := db.db.ExecContext(ctx, `CREATE TABLE `+test.table+` (attempt_id TEXT PRIMARY KEY)`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.db.ExecContext(ctx, `INSERT INTO `+test.table+`(attempt_id) VALUES (?)`, a.ID); err != nil {
					t.Fatal(err)
				}
			}
			a.StartedAt = time.Now().UTC()
			if err := db.WithWriteTx(ctx, func(tx context.Context) error {
				if err := db.UpdateAttempt(tx, &a); err != nil {
					return err
				}
				if test.build {
					return db.RecordAttemptPreparation(tx, a)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			c, err = db.GetCard(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			a, err = db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			o, err = db.GetObjective(ctx, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			ch, err = db.GetChannel(ctx, ch.ID)
			if err != nil {
				t.Fatal(err)
			}
			events, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.build {
				held, err := db.HoldLegacyClaudeBuild(ctx, c, a, o, ch, time.Now(), func() bool {
					t.Fatal("foreign typed owner reached local config probe")
					return true
				})
				if err != nil || held {
					t.Fatalf("local hold displaced typed owner: held=%v err=%v", held, err)
				}
			} else if err := db.HoldPreparedCompatibleAttempt(ctx, c, a, &o, time.Now()); err != nil {
				t.Fatal(err)
			}
			nowC, err := db.GetCard(ctx, c.ID)
			if err != nil || !reflect.DeepEqual(nowC, c) {
				t.Fatalf("typed owner's card changed: %+v %v", nowC, err)
			}
			nowA, err := db.GetAttempt(ctx, a.ID)
			if err != nil || !reflect.DeepEqual(nowA, a) {
				t.Fatalf("typed owner's attempt changed: %+v %v", nowA, err)
			}
			nowEvents, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil || !reflect.DeepEqual(nowEvents, events) {
				t.Fatalf("typed owner's evidence changed: %+v %v", nowEvents, err)
			}
		})
	}
}
