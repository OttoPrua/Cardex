package host_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestStrictHostDirectLaunchDefersAndResumes(t *testing.T) {
	for _, strict := range []bool{true, false} {
		name := "ordinary"
		if strict {
			name = "strict"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			home, id, work := seedPreparedCard(t, t.TempDir(), model.RunnerFake, "", 50, func(c *model.Card) {
				c.StrictDeps, c.Deps, c.Status = strict, []string{"upstream"}, model.CardRunning
			}, func(string) any {
				return map[string]any{"attempts": map[string]any{"0": map[string]any{
					"write_files": []map[string]string{{"path": "ran.txt", "content": "ran"}},
				}}}
			})
			db, err := store.Open(filepath.Join(home, "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.CreateObjective(ctx, &model.Objective{ID: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive}); err != nil {
				t.Fatal(err)
			}
			up := model.Card{ID: "upstream", Kind: model.KindDev, Status: model.CardCanceled}
			if err := db.CreateCard(ctx, &up); err != nil {
				t.Fatal(err)
			}
			if err := host.Run(ctx, home, id); err != nil {
				t.Fatal(err)
			}
			if strict {
				a, events := loadAttempt(t, home, id)
				if a.PID != 0 || a.Exit != nil || !a.EndedAt.IsZero() || a.Outcome != "" {
					t.Fatalf("deferred attempt not reusable: %+v", a)
				}
				ev, ok := eventType(events, host.EventDependencyDeferred)
				if !ok || !strings.Contains(string(ev.Data), "upstream is canceled") {
					t.Fatalf("missing typed reason: %v", events)
				}
				if _, ok := eventType(events, "host_start"); ok {
					t.Fatal("blocked host recorded a runner start")
				}
				if _, err := os.Stat(filepath.Join(work, "ran.txt")); !os.IsNotExist(err) {
					t.Fatalf("blocked executor ran: %v", err)
				}
				c, err := db.GetCard(ctx, "c1")
				if err != nil || c.Status != model.CardReady {
					t.Fatalf("prepared card not made dispatchable: %+v; err %v", c, err)
				}
				up.Status = model.CardMerged
				if err := db.UpdateCard(ctx, &up); err != nil {
					t.Fatal(err)
				}
				if err := host.Run(ctx, home, id); err != nil {
					t.Fatal(err)
				}
				if err := host.Run(ctx, home, id); err == nil {
					t.Fatal("strict closed attempt was relaunched")
				}
			}
			if b, err := os.ReadFile(filepath.Join(work, "ran.txt")); err != nil || string(b) != "ran" {
				t.Fatalf("executor did not run once allowed: %q; err %v", b, err)
			}
		})
	}
}

func TestStrictAPIChannelStillEnforcesBindingAndHealthAfterDependenciesPass(t *testing.T) {
	for _, test := range []struct {
		name, want string
	}{
		{"stale binding", "dispatch binding changed"},
		{"inactive channel", "activation or dispatch binding changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			home, id, _ := seedPreparedCard(t, t.TempDir(), model.RunnerClaude, "vendor", 1, func(c *model.Card) {
				c.Kind, c.StrictDeps, c.Deps = model.KindReview, true, []string{"subject", "upstream"}
			}, nil)
			config := `{"engines":{"vendor":{"protocol":"anthropic-compatible","base_url":"https://fixture.example.test","credential":{"source":"env","name":"CARDEX_STRICT_API1_FIXTURE"}}}}`
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(home, "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.CreateObjective(ctx, &model.Objective{ID: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive}); err != nil {
				t.Fatal(err)
			}
			subject := model.Card{ID: "subject", Kind: model.KindDev, Status: model.CardReady}
			partner := model.Card{ID: "upstream", Kind: model.KindDev, Status: model.CardCanceled}
			for _, c := range []*model.Card{&subject, &partner} {
				if err := db.CreateCard(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			attempt, err := db.GetAttempt(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			ch, err := db.GetChannel(ctx, attempt.Route.Channel)
			if err != nil {
				t.Fatal(err)
			}
			ch, err = runner.ValidateChannelBinding(home, ch, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			binding := runner.CompatibleBindingID(ch.EngineProfile)
			if test.name == "stale binding" {
				binding = strings.Repeat("a", 64)
			} else {
				ch.Health = model.HealthDegraded
			}
			attempt.RouteReason.Dispatch = &model.RouteDispatch{CompatibleBindingID: binding}
			if err := db.UpdateAttempt(ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			restore := host.SetCompatibleRelayForTest(func(context.Context, []byte, ...string) (*runner.CompatibleRelay, error) {
				t.Fatal("an unadmitted API1 route reached credentials or relay start")
				return nil, nil
			})
			defer restore()

			if err := host.Run(ctx, home, id); err != nil {
				t.Fatal(err)
			}
			prepared, events := loadAttempt(t, home, id)
			if _, ok := eventType(events, host.EventDependencyDeferred); !ok ||
				prepared.PID != 0 || !prepared.EndedAt.IsZero() || prepared.ErrorExcerpt != "" {
				t.Fatalf("strict gate did not defer before API1 handoff: %+v; events %v", prepared, events)
			}
			partner.Status = model.CardDone
			if err := db.UpdateCard(ctx, &partner); err != nil {
				t.Fatal(err)
			}
			if err := host.Run(ctx, home, id); err != nil {
				t.Fatal(err)
			}
			refused, events := loadAttempt(t, home, id)
			if !strings.Contains(refused.ErrorExcerpt, test.want) {
				t.Fatalf("satisfied dependency bypassed API1 refusal: got %q, want %q", refused.ErrorExcerpt, test.want)
			}
			if _, ok := eventType(events, "host_start"); ok {
				t.Fatal("refused API1 route started an executor")
			}
		})
	}
}
