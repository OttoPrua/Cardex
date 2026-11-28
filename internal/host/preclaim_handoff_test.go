package host

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestPreclaimHandoffPreservesCompetingOwnerAndExactUnchangedHandoff(t *testing.T) {
	const reason = "engines profile unresolved; synthetic local failure"
	for _, name := range []string{
		"historical_park", "typed_named_build_hold", "same_binding_historical_park", "route_changed",
		"card_aba", "attempt_field_changed", "other_host_claim", "other_lifecycle_claim", "newer_attempt",
		"objective_paused", "objective_noncascade_canceled", "objective_pin_rebound", "objective_removed",
		"initially_paused_objective", "objective_inserted", "strict_untyped_deferral", "strict_late_deferral", "strict_foreign_deferral", "ordinary_probe_deferral", "unchanged_legacy", "unchanged_no_objective", "unchanged_legacy_orphan",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			db, err := store.Open(filepath.Join(t.TempDir(), "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			objectiveID := "strict-objective"
			switch name {
			case "unchanged_no_objective":
				objectiveID = ""
			case "unchanged_legacy_orphan", "objective_inserted":
				// Legacy direct callers may supply a historical card with
				// no current objective row.
			default:
				seedStrictHostObjective(t, db)
			}
			channel := model.Channel{ID: "fixture-channel", Runner: model.RunnerClaude, Health: model.HealthOK, EngineProfile: "fixture-legacy"}
			if name == "same_binding_historical_park" {
				channel.EngineProfile = "anthropic-compatible:fixture"
			}
			if err := db.CreateChannel(ctx, &channel); err != nil {
				t.Fatal(err)
			}
			c := model.Card{ID: "fixture-card", Objective: objectiveID, Kind: model.KindDev, Status: model.CardRunning, Channel: channel.ID}
			if name == "strict_untyped_deferral" || name == "strict_late_deferral" || name == "strict_foreign_deferral" {
				c.StrictDeps = true
			}
			if name == "objective_pin_rebound" {
				// Exercise the effective objective pin, not a card override.
				c.Channel = ""
			}
			if err := db.CreateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			a := model.Attempt{ID: "fixture-attempt", Card: c.ID, StartedAt: time.Now().UTC(), Route: model.Route{Channel: channel.ID, Model: "fixture-model"}}
			if err := db.CreateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			if name == "typed_named_build_hold" {
				if err := db.RecordAttemptPreparation(ctx, a); err != nil {
					t.Fatal(err)
				}
			}
			var obj model.Objective
			if objectiveID != "" && name != "unchanged_legacy_orphan" && name != "objective_inserted" {
				obj, err = db.GetObjective(ctx, c.Objective)
				if err != nil {
					t.Fatal(err)
				}
				if name == "objective_pin_rebound" {
					obj.Channel, obj.Model = channel.ID, a.Route.Model
				}
				if name == "initially_paused_objective" {
					obj.Status = model.ObjectivePaused
				}
				if name == "objective_pin_rebound" || name == "initially_paused_objective" {
					if err := db.UpdateObjective(ctx, &obj); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Simulate the actual host read before Build/config/duration work.
			stale, err := db.ReadHostPreclaim(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			channel, err = db.GetChannel(ctx, channel.ID)
			if err != nil {
				t.Fatal(err)
			}
			stamp := time.Now().UTC()
			switch name {
			case "historical_park", "route_changed", "same_binding_historical_park":
				if name != "same_binding_historical_park" {
					staged := channel
					staged.EngineProfile = "anthropic-compatible:fixture"
					if err := db.UpdateChannel(ctx, &staged); err != nil {
						t.Fatal(err)
					}
				}
				if name != "route_changed" {
					err = db.HoldPreparedCompatibleAttempt(ctx, stale.Card, stale.Attempt, &obj, stamp)
				}
			case "typed_named_build_hold":
				held, heldErr := db.HoldLegacyClaudeBuild(ctx, stale.Card, stale.Attempt, obj, channel, stamp, func() bool { return true })
				if heldErr != nil || !held {
					t.Fatalf("fixture did not record exact typed local hold: held=%v err=%v", held, heldErr)
				}
			case "card_aba":
				edited := stale.Card
				edited.Status = model.CardPaused
				if err := db.UpdateCard(ctx, &edited); err != nil {
					t.Fatal(err)
				}
				original := stale.Card
				err = db.UpdateCard(ctx, &original)
			case "attempt_field_changed":
				edited := stale.Attempt
				edited.PromptPath = "newly-owned/prompt.txt"
				err = db.UpdateAttempt(ctx, &edited)
			case "other_host_claim":
				edited := stale.Attempt
				edited.PID = 321 // synthetic observation, never signaled
				err = db.UpdateAttempt(ctx, &edited)
			case "other_lifecycle_claim":
				err = db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: "executor_pgid", Data: []byte(`{"pgid":321}`)})
			case "strict_untyped_deferral", "strict_late_deferral", "strict_foreign_deferral", "ordinary_probe_deferral":
				receipt := model.HostDependencyDeferral{Card: c.ID, Attempt: a.ID, Phase: model.HostDependencyPreclaimProbe, Reason: "fixture"}
				switch name {
				case "strict_untyped_deferral":
					receipt.Attempt, receipt.Phase = "", ""
				case "strict_late_deferral":
					receipt.Phase = "exec"
				case "strict_foreign_deferral":
					receipt.Attempt = "different-attempt"
				}
				raw, marshalErr := json.Marshal(receipt)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: model.EventHostDependencyDeferred, Data: raw})

			case "newer_attempt":
				err = db.CreateAttempt(ctx, &model.Attempt{ID: "newer-attempt", Index: a.Index + 1, Card: c.ID, Route: a.Route})
			case "objective_paused":
				obj.Status = model.ObjectivePaused
				err = db.UpdateObjective(ctx, &obj)
			case "objective_noncascade_canceled":
				obj.Status = model.ObjectiveCanceled
				err = db.UpdateObjective(ctx, &obj)
			case "objective_pin_rebound":
				obj.Channel, obj.Model = "different-channel", "different-model"
				err = db.UpdateObjective(ctx, &obj)
			case "objective_removed":
				err = db.DeleteObjective(ctx, obj.ID)
			case "objective_inserted":
				seedStrictHostObjective(t, db)
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := db.ReadHostPreclaim(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (name == "historical_park" || name == "same_binding_historical_park") &&
				(want.Card.Status != model.CardNeedsInput || want.Card.WaitReason != model.CompatibleHistoricalRecoveryHold || !reflect.DeepEqual(want.Attempt, stale.Attempt)) {
				t.Fatalf("fixture failed to create a card-only historical park: %+v", want)
			}
			if name == "card_aba" && !reflect.DeepEqual(want.Card, stale.Card) {
				t.Fatalf("ABA test did not restore identical visible fields: %+v versus %+v", want.Card, stale.Card)
			}
			before, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := handoffBeforeClaim(ctx, db, stale, channel, reason); err != nil {
				t.Fatal(err)
			}
			got, err := db.ReadHostPreclaim(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			events, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if name == "unchanged_legacy" || name == "unchanged_no_objective" || name == "unchanged_legacy_orphan" {
				if got.Card.Status != model.CardNeedsInput || got.Card.WaitReason != reason || got.Attempt.EndedAt.IsZero() ||
					got.Attempt.ErrorExcerpt != reason || got.Attempt.PID != 0 || got.Attempt.Exit != nil || got.Attempt.Outcome != "" ||
					len(events) != 1 || events[0].Type != "needs_review" {
					t.Fatalf("unchanged legacy handoff did not preserve work/reason semantics: %+v events=%+v", got, events)
				}
				var payload map[string]string
				if err := json.Unmarshal(events[0].Data, &payload); err != nil || payload["reason"] != reason {
					t.Fatalf("legacy review reason receipt changed: %v %v", payload, err)
				}
				return
			}
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(events, before) {
				t.Fatalf("stale preclaim overwrote current card/attempt or receipt: got=%+v want=%+v events=%+v want=%+v", got, want, events, before)
			}
		})
	}
}
