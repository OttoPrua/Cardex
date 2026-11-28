package host_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestAPI2ComparisonRechecksHealthAndNeedsActualEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Claude uses a Unix script")
	}
	for _, mode := range []string{"no independent approval", "already unavailable", "became unavailable", "forged CLI identity"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			home, id, _ := seedPreparedCard(t, t.TempDir(), model.RunnerClaude, "vendor-a", 1, func(c *model.Card) {
				c.Kind, c.Node, c.Body = model.KindResearch, "", "approved fixture question"
				c.Channel, c.Model = "ch1", "fake"
				c.Requires = []string{model.RequireStrictComparison}
			}, nil)
			config := `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test","credential":{"source":"env","name":"CARDEX_SYNTH_PROBE"}}}}`
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(home, "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ch, err := db.GetChannel(ctx, "ch1")
			if err != nil {
				t.Fatal(err)
			}
			ch, err = runner.ValidateChannelBinding(home, ch, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := db.GetAttempt(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			c, err := db.GetCard(ctx, attempt.Card)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.CreateObjective(ctx, &model.Objective{ID: c.Objective, Project: "fixture-project", Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe}); err != nil {
				t.Fatal(err)
			}
			_, digest := c.ComparisonInput()
			attempt.RouteReason.Dispatch = &model.RouteDispatch{Runner: model.RunnerClaude, StrictComparison: true,
				Channel: ch.ID, Model: attempt.Route.Model, CompatibleBindingID: runner.CompatibleBindingID(ch.EngineProfile), ComparisonInputDigest: digest}
			if err := db.UpdateAttempt(ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			if mode == "already unavailable" {
				ch.Health = model.HealthDegraded
			}
			if err := db.UpdateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"model\":\"fake\",\"result\":\"looks valid but has no provider proof\"}'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			calls := 0
			if mode != "no independent approval" {
				restore := host.SetStrictComparisonRelayForTest(func(context.Context, []byte, ...string) (*runner.CompatibleRelay, error) {
					calls++
					if mode == "already unavailable" {
						t.Fatal("unhealthy strict route reached credential startup")
					}
					if mode == "became unavailable" {
						ch.Health = model.HealthDegraded
						if err := db.UpdateChannel(ctx, &ch); err != nil {
							t.Fatal(err)
						}
					}
					return &runner.CompatibleRelay{LocalURL: "http://127.0.0.1:38021", LocalKey: "cdx-relay-" + strings.Repeat("a", 64)}, nil
				})
				defer restore()
			}
			if err := host.Run(ctx, home, id); err != nil {
				t.Fatal(err)
			}
			got, events := loadAttempt(t, home, id)
			if got.Outcome != model.OutcomeUnavailable {
				t.Fatalf("strict failure was not terminal: %+v", got)
			}
			_, started := eventType(events, "host_start")
			if mode == "forged CLI identity" {
				if !started || got.RouteReason.ProviderModel == nil || got.RouteReason.ProviderModel.Source != model.ModelSourceUnknown {
					t.Fatalf("unverified legacy/CLI source accepted or not traced: start=%t, reason=%s", started, got.ErrorExcerpt)
				}
			} else if started {
				t.Fatal("unavailable exact binding launched a process")
			}
			if mode != "already unavailable" && mode != "no independent approval" && calls != 1 {
				t.Fatalf("unexpected relay start count: %d, reason=%s", calls, got.ErrorExcerpt)
			}
			if mode == "no independent approval" {
				if !strings.Contains(got.ErrorExcerpt, "independently authenticated") || got.PID != 0 ||
					!got.StartedAt.IsZero() || got.Exit != nil {
					t.Fatal("default path did not record a pristine no-wire refusal")
				}
				if _, exists := eventType(events, store.EventComparisonRefusedNoWire); !exists {
					t.Fatal("typed no-wire refusal was not recorded")
				}
				if _, exists := eventType(events, "strict_comparison_claim"); exists {
					t.Fatal("default disabled path acquired an execution claim")
				}
			}
			card, err := db.GetCard(ctx, got.Card)
			if err != nil || card.Status != model.CardNeedsInput {
				t.Fatalf("strict failure modified routing: %+v %v", card, err)
			}
		})
	}
}

func TestAPI2DuplicateActualHostAndRestartCannotReopenCredentials(t *testing.T) {
	ctx := context.Background()
	home, id, _ := seedPreparedCard(t, t.TempDir(), model.RunnerClaude, "vendor-a", 1, func(c *model.Card) {
		c.Kind, c.Node, c.Body = model.KindResearch, "", "approved fixture question"
		c.Channel, c.Model = "ch1", "fake"
		c.Requires = []string{model.RequireStrictComparison}
	}, nil)
	doc := `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test","credential":{"source":"env","name":"CARDEX_SYNTH_PROBE"}}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ch, err := db.GetChannel(ctx, "ch1")
	if err != nil {
		t.Fatal(err)
	}
	ch, err = runner.ValidateChannelBinding(home, ch, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	a, err := db.GetAttempt(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.GetCard(ctx, a.Card)
	if err != nil {
		t.Fatal(err)
	}
	_, digest := c.ComparisonInput()
	a.RouteReason.Dispatch = &model.RouteDispatch{Runner: model.RunnerClaude, Channel: ch.ID, Model: c.Model, StrictComparison: true,
		CompatibleBindingID: runner.CompatibleBindingID(ch.EngineProfile), ComparisonInputDigest: digest}
	if err := db.UpdateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	first, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	restore := host.SetStrictComparisonRelayForTest(func(context.Context, []byte, ...string) (*runner.CompatibleRelay, error) {
		if calls.Add(1) == 1 {
			close(first)
			<-release
		}
		return nil, errors.New("synthetic stop before network")
	})
	defer restore()
	finished := make(chan error, 2)
	go func() { finished <- host.Run(ctx, home, id) }()
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("first host did not acquire a durable claim")
	}
	go func() { finished <- host.Run(ctx, home, id) }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("duplicate host did not fail closed")
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate reached credential/relay setup")
	}
	open, err := db.GetAttempt(ctx, id)
	if err != nil || !open.EndedAt.IsZero() {
		t.Fatal("duplicate overwrote the original attempt")
	}
	close(release)
	released = true
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	// Reopen through another host.Run, with no original context/relay state.
	if err := host.Run(ctx, home, id); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("restart replay reopened credential/relay setup")
	}
}

// Direct host is intentionally called outside the scheduler so historical
// markers cannot depend on admission by the ordinary dispatch path.
func TestAPI2DisabledDirectHostLeavesStaleForeignAndExecutedAttemptsUntouched(t *testing.T) {
	for _, mode := range []string{
		"foreign pid", "prepare timestamp", "owned event", "recorded spawn",
		"newer attempt", "ordinary card", "canceled card", "changed exact pin",
	} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			home, id, _ := seedPreparedCard(t, t.TempDir(), model.RunnerClaude, "gateway", 1, func(c *model.Card) {
				c.Kind, c.Node, c.Body = model.KindResearch, "", "synthetic approved question"
				c.Channel, c.Model = "ch1", "fake"
				c.Requires = []string{model.RequireStrictComparison}
			}, nil)
			db, err := store.Open(filepath.Join(home, "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a, err := db.GetAttempt(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			c, err := db.GetCard(ctx, a.Card)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.CreateObjective(ctx, &model.Objective{ID: c.Objective, Project: "fixture-project", Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe}); err != nil {
				t.Fatal(err)
			}
			ch, err := db.GetChannel(ctx, c.Channel)
			if err != nil {
				t.Fatal(err)
			}
			binding := strings.Repeat("a", 64)
			ch.EngineProfile = "anthropic-compatible:gateway@" + binding
			if err := db.UpdateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			_, input := c.ComparisonInput()
			a.RouteReason.Dispatch = &model.RouteDispatch{
				StrictComparison: true, Runner: model.RunnerClaude, Channel: c.Channel, Model: c.Model,
				CompatibleBindingID: binding, ComparisonInputDigest: input,
			}
			switch mode {
			case "foreign pid":
				a.PID = os.Getpid()
			case "prepare timestamp":
				a.StartedAt = time.Now().UTC()
			case "newer attempt":
				if err := db.CreateAttempt(ctx, &model.Attempt{ID: "newer", Card: c.ID, Index: a.Index + 1, Route: a.Route}); err != nil {
					t.Fatal(err)
				}
			case "ordinary card":
				c.Kind, c.Requires = model.KindDev, nil
			case "canceled card":
				c.Status = model.CardCanceled
			case "changed exact pin":
				c.Model = "different-fixture-model"
			}
			if err := db.UpdateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "owned event":
				if _, ok, err := db.ClaimStrictComparison(ctx, a); err != nil || !ok {
					t.Fatalf("could not create the synthetic owner receipt: %t, %v", ok, err)
				}
			case "recorded spawn":
				if err := db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: "host_start", Data: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			a, prior := loadAttempt(t, home, id)
			c, err = db.GetCard(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Run(ctx, home, id); err != nil {
				t.Fatal(err)
			}
			got, events := loadAttempt(t, home, id)
			fresh, err := db.GetCard(ctx, c.ID)
			if err != nil || !reflect.DeepEqual(fresh, c) || !reflect.DeepEqual(got, a) || !reflect.DeepEqual(events, prior) {
				t.Fatal("disabled direct host changed a stale, foreign, or previously executed attempt or its card")
			}
		})
	}
}
