package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/store"
)

func localPrestartFixture(t *testing.T, profile string, receipt bool) (string, *store.Store, model.Card, model.Attempt, model.Objective, model.Channel) {
	t.Helper()
	home, db, work := newHome(t)
	ctx := t.Context()
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "operator-owned"), []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "fixture-project", RepoPath: work}
	if err := db.CreateProject(ctx, &p); err != nil {
		t.Fatal(err)
	}
	o := model.Objective{ID: "fixture-objective", Project: p.ID, Title: "Fixture", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive}
	if err := db.CreateObjective(ctx, &o); err != nil {
		t.Fatal(err)
	}
	ch := model.Channel{ID: "fixture-channel", Runner: model.RunnerClaude, Health: model.HealthOK, Tier: model.TierT2, Family: "fixture", EngineProfile: profile, Models: []string{"fixture-model"}, Concurrency: 1}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	c := model.Card{ID: "fixture-card", Kind: model.KindDev, Objective: o.ID, Status: model.CardRunning, Channel: ch.ID, Model: ch.Models[0]}
	if err := db.CreateCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	a := model.Attempt{ID: "fixture-attempt", Card: c.ID, Worktree: work, Route: model.Route{Channel: ch.ID, Model: ch.Models[0]},
		StartedAt: time.Now().UTC(), PromptPath: filepath.Join(home, "attempts", "fixture-attempt", "prompt.txt")}
	if err := db.WithWriteTx(ctx, func(tx context.Context) error {
		if err := db.CreateAttempt(tx, &a); err != nil {
			return err
		}
		if receipt {
			return db.RecordAttemptPreparation(tx, a)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetCard(ctx, c.ID)
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
	return home, db, c, a, o, ch
}

func TestDetachedLegacyClaudeLocalBuildBecomesOperatorHoldNotExecutor(t *testing.T) {
	const syntheticAuthEnv = "CARDEX_SYNTHETIC_LEGACY_AUTH_EMPTY_924"
	t.Setenv(syntheticAuthEnv, "")
	for _, tc := range []struct {
		name    string
		field   string
		park    bool
		receipt bool
	}{
		{"missing detached environment", "auth_env", true, true},
		{"transient file failure stays retryable", "auth_file", false, true},
		{"no current receipt stays unclaimed", "auth_env", false, false},
		{"resolved profile has no auth", "no_auth", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			home, db, c, a, _, _ := localPrestartFixture(t, "fixture-legacy", tc.receipt)
			auth := syntheticAuthEnv
			if tc.field == "auth_file" {
				auth = filepath.Join(home, "absent-synthetic-credential")
			}
			profile := map[string]string{"base_url": "https://legacy.example.invalid/v1"}
			if tc.field != "no_auth" {
				profile[tc.field] = auth
			}
			cfg, err := json.Marshal(map[string]any{"engines": map[string]any{"fixture-legacy": profile}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "config.json"), cfg, 0o600); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			started, err := r.startAttempt(ctx, db, &a, false)
			if err != nil || started {
				t.Fatalf("launcher became execution evidence: started=%v err=%v", started, err)
			}
			got, err := db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			card, err := db.GetCard(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			events, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, ev := range events {
				switch ev.Type {
				case model.EventLegacyClaudeBuildHeld:
					count++
				case "host_start", "host_finish", "executor_pgid", model.EventCompatibleRefusedNoWire, "dev_disposed":
					t.Fatalf("local build became execution/disposition evidence: %+v", ev)
				}
			}
			if !tc.park {
				if !reflect.DeepEqual(card, c) || !reflect.DeepEqual(got, a) || count != 0 || !resumable(card, []model.Attempt{got}) {
					t.Fatalf("unknown prestart was altered: %+v %+v events=%+v", card, got, events)
				}
				return
			}
			if card.Status != model.CardNeedsInput || card.WaitReason != model.LegacyClaudeBuildHold ||
				got.PID != 0 || got.Exit != nil || got.EndedAt.IsZero() || got.Outcome != model.OutcomeLaunchFailed ||
				got.ErrorClass != model.ErrorLaunchFailed || count != 1 || attemptOccupies(got) || resumable(card, []model.Attempt{got}) {
				t.Fatalf("permanent local block retained slot or lacks exact hold: %+v %+v events=%+v", card, got, events)
			}
			marker, err := os.ReadFile(filepath.Join(a.Worktree, "operator-owned"))
			if err != nil || string(marker) != "retain" {
				t.Fatalf("local hold touched checkout: %q %v", marker, err)
			}
			var disposition loop.DispositionReceipt
			dctx := loop.WithDispositionReceipt(loop.WithHome(loop.WithStore(loop.WithAttempt(ctx, a.ID), db), home), &disposition)
			if err := loop.Dispose(dctx, &card, harvest.Result{Outcome: model.OutcomePassed}); err != nil || !disposition.HeldAttempt {
				t.Fatalf("generic consumer can reinterpret local hold: %+v %v", disposition, err)
			}
		})
	}
}

func TestPreupgradeStagedPreparationParksCardWithoutRewritingAttempt(t *testing.T) {
	for _, mode := range []string{"historical", "wrong timestamp", "unfamiliar lifecycle", "already running", "newer attempt", "paused objective", "rebound card", "route no longer staged"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			home, db, c, a, o, ch := localPrestartFixture(t, "anthropic-compatible:fixture-staged", false)
			staleCard, staleObjective := c, o
			var err error
			switch mode {
			case "wrong timestamp":
				err = db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: model.EventAttemptPrepared,
					TS: a.StartedAt.Add(-time.Second), Data: []byte(`"old"`)})
			case "unfamiliar lifecycle":
				err = db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: "future_owner_pending"})
			case "already running":
				seen := a
				seen.PID = 321
				err = db.UpdateAttempt(ctx, &seen)
			case "newer attempt":
				err = db.CreateAttempt(ctx, &model.Attempt{ID: "fixture-newer", Card: c.ID, Index: a.Index + 1})
			case "paused objective":
				o.Status = model.ObjectivePaused
				err = db.UpdateObjective(ctx, &o)
			case "rebound card":
				c.Channel = "other"
				err = db.UpdateCard(ctx, &c)
			case "route no longer staged":
				ch.EngineProfile = "fixture-legacy"
				err = db.UpdateChannel(ctx, &ch)
			}
			if err != nil {
				t.Fatal(err)
			}
			originalEvents, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil {
				t.Fatal(err)
			}
			observedAttempt, err := db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			expectedCard, err := db.GetCard(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "route no longer staged" {
				// Simulate the scheduler's decision just before the rebind. A
				// real current legacy route is intentionally allowed to start.
				err = db.HoldPreparedCompatibleAttempt(ctx, staleCard, a, &staleObjective, time.Now())
			} else {
				r := New(home)
				r.Launch = func(context.Context, string, string) (int, error) {
					t.Fatal("hard-off dispatch reached launch")
					return 0, nil
				}
				r.Prompt = func(context.Context, prompt.Input) (string, error) {
					t.Fatal("hard-off dispatch generated a prompt")
					return "", nil
				}
				_, err = r.resumeAttempt(ctx, db, staleCard, staleObjective, &a)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := db.GetCard(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "historical" {
				expectedCard.Status, expectedCard.WaitReason = model.CardNeedsInput, model.CompatibleHistoricalRecoveryHold
			}
			if !reflect.DeepEqual(expectedCard, got) {
				t.Fatalf("historical park changed the wrong card or failed: got=%+v want=%+v", got, expectedCard)
			}
			finalAttempt, err := db.GetAttempt(ctx, a.ID)
			if err != nil || !reflect.DeepEqual(observedAttempt, finalAttempt) {
				t.Fatalf("historical park invented an attempt outcome: %+v %v", finalAttempt, err)
			}
			finalEvents, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil || !reflect.DeepEqual(originalEvents, finalEvents) {
				t.Fatalf("historical park invented an attempt receipt: %+v %v", finalEvents, err)
			}
		})
	}
}
