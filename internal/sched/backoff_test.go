package sched

import (
	"context"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/prompt"
)

func TestDispatchBackoffRelease(t *testing.T) {
	for _, mode := range []model.ObjectiveMode{model.ModeManual, model.ModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			_, objID := seedRepo(t, db, repo, "true")
			obj, err := db.GetObjective(ctx, objID)
			if err != nil {
				t.Fatal(err)
			}
			obj.Mode = mode
			if err := db.UpdateObjective(ctx, &obj); err != nil {
				t.Fatal(err)
			}
			seedRoutes(t, db)
			now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
			card := model.Card{
				ID: "c", Objective: objID, Kind: model.KindDev, Title: "c",
				Tier: model.TierT2, Status: model.CardWaiting, Channel: "cha",
				WaitReason: "backoff:" + now.Add(30*time.Second).UTC().Format(time.RFC3339Nano),
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			r.Now = func() time.Time { return now }
			r.Launch = func(context.Context, string, string) (int, error) { return 41, nil }
			r.Prompt = func(context.Context, prompt.Input) (string, error) { return "p", nil }
			if _, err := r.dispatch(ctx, db); err != nil {
				t.Fatal(err)
			}
			held := mustCard(t, db, "c")
			attempts, err := db.ListAttempts(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			if held.Status != model.CardWaiting || !strings.HasPrefix(held.WaitReason, "backoff:") || len(attempts) != 0 {
				t.Fatalf("before expiry %+v attempts %d", held, len(attempts))
			}

			r.Now = func() time.Time { return now.Add(30 * time.Second) }
			if _, err := r.dispatch(ctx, db); err != nil {
				t.Fatal(err)
			}
			launched := mustCard(t, db, "c")
			attempts, err = db.ListAttempts(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			if launched.Status != model.CardRunning || launched.Channel != "cha" || launched.WaitReason != "" || len(attempts) != 1 || attempts[0].Route.Channel != "cha" {
				t.Fatalf("after expiry %+v attempts %+v", launched, attempts)
			}
		})
	}
}

func TestDispatchThirdTransportSwitches(t *testing.T) {
	for _, mode := range []model.ObjectiveMode{model.ModeManual, model.ModeAuto} {
		for _, class := range []model.ErrorClass{model.ErrorTransport, model.ErrorTimeout} {
			t.Run(string(mode)+"/"+string(class), func(t *testing.T) {
				ctx := context.Background()
				_, home, db, repo, objID := dispositionHome(t)
				obj, err := db.GetObjective(ctx, objID)
				if err != nil {
					t.Fatal(err)
				}
				obj.Mode = mode
				if err := db.UpdateObjective(ctx, &obj); err != nil {
					t.Fatal(err)
				}
				seedRoutes(t, db)
				card := devCard(objID, "c")
				card.Channel = "cha"
				priors := []model.Attempt{
					{Index: 0, Outcome: model.OutcomeTransport, Route: model.Route{Channel: "cha"}},
					{Index: 1, Outcome: model.OutcomeTimeout, Route: model.Route{Channel: "cha"}},
				}
				finishHarvest(t, db, home, repo, card, model.Attempt{
					Index: 2, ErrorClass: class, Route: model.Route{Channel: "cha"},
				}, "", "down", false, priors)
				r := New(home)
				r.Launch = func(context.Context, string, string) (int, error) { return 41, nil }
				r.Prompt = func(context.Context, prompt.Input) (string, error) { return "p", nil }
				r.Engine = func(context.Context, string) error { return nil }
				if err := r.Tick(ctx); err != nil {
					t.Fatal(err)
				}
				got := mustCard(t, db, "c")
				if got.Channel != "chb" || got.Status == model.CardWaiting || strings.HasPrefix(got.WaitReason, "backoff:") {
					t.Fatalf("switch %+v", got)
				}
			})
		}
	}
}
