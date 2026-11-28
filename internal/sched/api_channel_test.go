package sched

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/runner"
)

func TestAPIChannelDispatchHoldsExplicitPinWithoutClassifierOrFallback(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	doc := `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test","default_model":"fixture-model","credential":{"source":"env","name":"CARDEX_API1_FIXTURE"}}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	ch, err := runner.ValidateChannelBinding(home, model.Channel{
		ID: "api-a", Runner: model.RunnerClaude, Health: model.HealthOK,
		Family: "vendor-a", EngineProfile: "vendor-a",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	legacy := model.Channel{ID: classifierChannelID, Runner: model.RunnerFake, Health: model.HealthOK,
		Family: "stock", Models: []string{"stock-model"}}
	if err := db.CreateChannel(ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{ID: "legacy", Model: "stock-model", Family: "stock", Tiers: []model.Tier{model.TierT2}}
	if err := db.CreatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "c1", Objective: obj, Kind: model.KindResearch, Title: "fixture task",
		Status: model.CardReady, Channel: ch.ID,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) {
		t.Fatal("a held pin generated a prompt")
		return "", nil
	}
	r.Classify = func(context.Context, string, string) (string, error) {
		t.Fatal("a held pin reached the classifier")
		return "", nil
	}
	r.Launch = func(context.Context, string, string) (int, error) {
		t.Fatal("a held pin started an executor")
		return 0, nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("held API pin allocated %d attempts: %v", len(attempts), err)
	}
	got, err := db.GetCard(ctx, card.ID)
	if err != nil || got.Channel != ch.ID || got.Status != model.CardNeedsInput || got.WaitReason != model.CompatibleRuntimeHold {
		t.Fatalf("explicit hold was not persisted: %+v: %v", got, err)
	}
}

func TestAPIChannelGitCheckoutHookSeesOnlyOrdinaryEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic Git hook is a shell script")
	}
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"engines":{
		"synthetic-env":{"protocol":"anthropic-compatible","credential":{"source":"env","name":"CARD_FIXTURE_PROVIDER"}},
		"synthetic-keychain":{"protocol":"anthropic-compatible","credential":{"source":"keychain","name":"KIMI_API_KEY"}}
	}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hooksDir := t.TempDir()
	git(t, repo, "config", "core.hooksPath", hooksDir)
	hook := `#!/bin/sh
	test -z "${card_fixture_provider+x}" &&
	test -z "${CARDEX_API_ORPHAN+x}" &&
	test "$KIMI_API_KEY" = fixture-legacy-only &&
	printf '%s\n' scrubbed > .checkout-env-proof
	`
	if err := os.WriteFile(filepath.Join(hooksDir, "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("card_fixture_provider", "fixture-configured-only")
	t.Setenv("CARDEX_API_ORPHAN", "fixture-reserved-only")
	t.Setenv("KIMI_API_KEY", "fixture-legacy-only")
	// A process default is not authoritative for a Tick against another home.
	t.Setenv("CARDEX_HOME", t.TempDir())
	card := model.Card{ID: "checkout", Objective: obj, Kind: model.KindDev, Status: model.CardReady,
		Tier: model.TierT2, Title: "fixture", Channel: "cha"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) { return "TEST", nil }
	r.Launch = func(context.Context, string, string) (int, error) { return 123, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("checkout didn't produce a prepared attempt: %d, %v", len(attempts), err)
	}
	proof, err := os.ReadFile(filepath.Join(attempts[0].Worktree, ".checkout-env-proof"))
	if err != nil || string(proof) != "scrubbed\n" {
		t.Fatalf("checkout hook did not verify the env boundary: %v", err)
	}
}

func TestAPIChannelHoldPreservesInterleavedOperatorChanges(t *testing.T) {
	for _, strict := range []bool{false, true} {
		ctx := t.Context()
		home, db, repo := newHome(t)
		_, oid := seedRepo(t, db, repo, "true")
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test","default_model":"fixture-model","credential":{"source":"env","name":"CARDEX_API1_FIXTURE"}}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		ch, err := runner.ValidateChannelBinding(home, model.Channel{ID: "api-a", Runner: model.RunnerClaude, Health: model.HealthOK, Family: "vendor-a", EngineProfile: "vendor-a"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CreateChannel(ctx, &ch); err != nil {
			t.Fatal(err)
		}
		obj, err := db.GetObjective(ctx, oid)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range []model.CardStatus{model.CardPaused, model.CardCanceled, model.CardReady} {
			card := model.Card{ID: "card-" + string(status), Objective: oid, Kind: model.KindResearch, Status: model.CardReady, Channel: ch.ID, StrictDeps: strict}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			before, err := db.GetCard(ctx, card.ID)
			if err != nil {
				t.Fatal(err)
			}
			// Deterministic barrier: a route already owns a card/objective
			// observation; commit the operator edit before it writes its hold.
			latest := before
			latest.Status, latest.Title, latest.Deps = status, "operator edit", []string{"new-dependency"}
			latest.StrictDeps = true
			if err := db.UpdateCard(ctx, &latest); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			r.Launch = func(context.Context, string, string) (int, error) { t.Fatal("held/stale card launched"); return 0, nil }
			if routed, err := r.launchCard(ctx, db, before, obj, nil, nil, nil, nil); err != nil || routed != "" {
				t.Fatalf("unexpected dispatch: %s %v", routed, err)
			}
			after, err := db.GetCard(ctx, card.ID)
			if err != nil || !reflect.DeepEqual(after, latest) {
				t.Fatalf("strict=%t stale hold overwrote operator state: after=%+v want=%+v error=%v", strict, after, latest, err)
			}
		}
	}
}

func TestAPIChannelHoldHarvestsOnlyCurrentEndedAttempt(t *testing.T) {
	ctx := t.Context()
	home, db, repo := newHome(t)
	_, oid := seedRepo(t, db, repo, "true")
	for _, mode := range []string{"current", "newer", "edited", "canceled"} {
		card := model.Card{ID: mode, Objective: oid, Kind: model.KindResearch, Status: model.CardRunning}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		a := model.Attempt{ID: "ended-" + mode, Card: card.ID, Index: 0, Worktree: repo, EndedAt: time.Now(), RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{CompatibleBindingID: "synthetic"}}}
		if err := db.CreateAttempt(ctx, &a); err != nil {
			t.Fatal(err)
		}
		if mode == "newer" {
			if err := db.CreateAttempt(ctx, &model.Attempt{ID: "newer-" + mode, Card: card.ID, Index: 1, StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
		}
		if mode == "edited" {
			card.Title = "operator retained title"
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
		}
		if mode == "canceled" {
			card.Status = model.CardCanceled
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
		}
	}
	r := New(home)
	r.Dispose = func(context.Context, *model.Card, harvest.Result) error {
		t.Fatal("raw worktree evidence reached generic harvest")
		return nil
	}
	if err := r.harvest(ctx, db); err != nil {
		t.Fatal(err)
	}
	for mode, want := range map[string]model.CardStatus{"current": model.CardNeedsInput, "newer": model.CardRunning, "edited": model.CardRunning, "canceled": model.CardCanceled} {
		got, err := db.GetCard(ctx, mode)
		if err != nil || got.Status != want {
			t.Fatalf("%s: %+v %v", mode, got, err)
		}
		if mode == "edited" && got.Title != "operator retained title" {
			t.Fatal("lost latest edit")
		}
		a, err := db.GetAttempt(ctx, "ended-"+mode)
		if err != nil || a.Outcome != model.OutcomeNoOutput || a.ErrorExcerpt != model.CompatibleRuntimeHold {
			t.Fatalf("%s: %+v %v", mode, a, err)
		}
	}
}
