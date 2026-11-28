package sched

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
)

func TestAPI2StrictComparisonUnavailableStaysNeedsInputWithoutAlternate(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	addClassifierChannel(t, db)
	for _, c := range []model.Channel{
		{ID: "fixture-a", Runner: model.RunnerClaude, Health: model.HealthDegraded, EngineProfile: "anthropic-compatible:a@" + strings.Repeat("a", 64), Family: "a", Models: []string{"fixture-model"}},
		{ID: "fixture-b", Runner: model.RunnerClaude, Health: model.HealthOK, EngineProfile: "anthropic-compatible:b@" + strings.Repeat("b", 64), Family: "b", Models: []string{"fixture-model"}},
	} {
		if err := db.CreateChannel(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}
	card := model.Card{ID: "strict", Objective: obj, Title: "comparison", Body: "approved fixture question", Kind: model.KindResearch, Status: model.CardReady,
		Channel: "fixture-a", Model: "fixture-model", Requires: []string{model.RequireStrictComparison}}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) {
		t.Fatal("strict unavailable card fell back")
		return 0, nil
	}
	r.Classify = func(context.Context, string, string) (string, error) {
		t.Fatal("comparison prompt sent to side classifier")
		return "", nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	got, err := db.GetCard(ctx, card.ID)
	if err != nil || got.Status != model.CardNeedsInput || got.Channel != card.Channel || got.Model != card.Model {
		t.Fatalf("strict comparison route mutated: %+v %v", got, err)
	}
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatal("strict missing target prepared an executor")
	}
}

func TestAPI2UnverifiedComparisonCannotReachHarvestDisposition(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	card := model.Card{ID: "strict", Objective: obj, Title: "comparison", Body: "approved fixture question", Kind: model.KindResearch, Status: model.CardRunning,
		Channel: "fixture-a", Model: "fixture-model", Requires: []string{model.RequireStrictComparison}}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	attemptDir := filepath.Join(home, "attempts", "strict1")
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attemptDir, "output.log"), []byte(`{"type":"result","model":"fixture-model","result":"misleading answer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exit := 0
	attempt := model.Attempt{ID: "strict1", Card: card.ID, Worktree: repo, PromptPath: filepath.Join(attemptDir, "prompt.txt"),
		Route: model.Route{Channel: card.Channel, Model: card.Model}, Exit: &exit, EndedAt: time.Now(),
		RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{StrictComparison: true, Channel: card.Channel,
			Model: card.Model, CompatibleBindingID: strings.Repeat("a", 64)}}}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Dispose = func(context.Context, *model.Card, harvest.Result) error {
		t.Fatal("CLI-only claim was considered a result")
		return nil
	}
	if err := r.harvest(ctx, db); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAttempt(ctx, attempt.ID)
	if err != nil || got.Outcome != model.OutcomeUnavailable {
		t.Fatalf("no evidence was not terminal: %+v %v", got, err)
	}
	settled, err := db.GetCard(ctx, card.ID)
	if err != nil || settled.Status != model.CardNeedsInput {
		t.Fatalf("wrong card disposition: %+v %v", settled, err)
	}
}

func TestAPI2StrictQuestionIsIndependentOfProjectContextAndAcceptance(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, objectiveID := seedRepo(t, db, repo, "never execute this generic acceptance")
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) {
		t.Fatal("comparison called the ordinary project prompt")
		return "", nil
	}
	card := model.Card{ID: "compare", Title: "must not send this title",
		Objective: objectiveID, Kind: model.KindResearch, Body: "Explicitly approved question?",
		Channel: "fixture-route", Model: "fixture-model", Requires: []string{model.RequireStrictComparison}}
	approved, digest := card.ComparisonInput()
	dir := filepath.Join(home, "attempts", "approved")
	a := model.Attempt{ID: "approved", Card: card.ID, PromptPath: filepath.Join(dir, "prompt.txt"),
		RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{StrictComparison: true, ComparisonInputDigest: digest}}}
	obj := model.Objective{ID: objectiveID, Title: "must not send objective", Acceptance: model.Acceptance{Commands: []string{"not approved"}}}
	if err := r.writePrompt(ctx, db, card, obj, model.Project{RepoPath: repo}, &a); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.PromptPath)
	if err != nil || string(data) != approved || strings.Contains(string(data), "must not send") {
		t.Fatal("comparison included unapproved context")
	}
	card.Body = "changed after dispatch"
	if err := r.writePrompt(ctx, db, card, obj, model.Project{}, &a); err == nil {
		t.Fatal("changed comparison input reused approval")
	}
	data, err = os.ReadFile(a.PromptPath)
	if err != nil || string(data) != approved {
		t.Fatal("refused question overwrote approved prompt")
	}
}

func TestAPI2HealthyHistoricalPinHeldBeforeCheckout(t *testing.T) {
	ctx := t.Context()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	addClassifierChannel(t, db)
	ch := model.Channel{ID: "historical", Runner: model.RunnerClaude, Health: model.HealthOK,
		EngineProfile: "anthropic-compatible:old@" + strings.Repeat("a", 64), Family: "fixture",
		Models: []string{"fixture-model"}}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "healthy-comparison", Objective: obj, Title: "fixture", Body: "fixture question",
		Kind: model.KindResearch, Status: model.CardReady, Channel: ch.ID, Model: ch.Models[0],
		Requires: []string{model.RequireStrictComparison}, StrictDeps: true}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) {
		t.Fatal("archived health enabled a comparison prompt")
		return "", nil
	}
	r.Classify = func(context.Context, string, string) (string, error) {
		t.Fatal("archived health enabled a classifier")
		return "", nil
	}
	r.Launch = func(context.Context, string, string) (int, error) {
		t.Fatal("archived health enabled a host")
		return 0, nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	got, err := db.GetCard(ctx, card.ID)
	if err != nil || got.Status != model.CardNeedsInput || got.Channel != ch.ID || got.Model != ch.Models[0] {
		t.Fatal("healthy historical comparison pin did not stay held")
	}
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatal("held comparison created an attempt")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), "wt")); !os.IsNotExist(err) {
		t.Fatal("held comparison prepared a checkout")
	}
}

func TestAPI2DualMarkedLaunchFailureCannotEnterD1Recovery(t *testing.T) {
	for _, dual := range []bool{true, false} {
		name := "ordinary-D1-positive"
		if dual {
			name = "dual-marked-no-authoritative-no-wire"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			home, db, repo := newHome(t)
			_, oid := seedRepo(t, db, repo, "true")
			wt := filepath.Join(filepath.Dir(repo), "fixture-checkout")
			git(t, repo, "worktree", "add", "-b", "fixture-checkout", wt)
			c := model.Card{ID: "fixture", Objective: oid, Kind: model.KindResearch, Status: model.CardRunning, StrictDeps: true, Title: "fixture", Body: "question", Channel: "fixture-route", Model: "fixture-model"}
			if dual {
				c.Requires = []string{model.RequireStrictComparison}
			}
			if dual && !c.ValidStrictComparison() {
				t.Fatal("invalid dual-marked fixture")
			}
			if err := db.CreateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			c, err := db.GetCard(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			a := model.Attempt{ID: "fixture-attempt", Card: c.ID, Worktree: wt, Route: model.Route{Channel: c.Channel, Model: c.Model}}
			if dual {
				_, digest := c.ComparisonInput()
				a.RouteReason.Dispatch = &model.RouteDispatch{StrictComparison: true, ComparisonInputDigest: digest, Channel: c.Channel, Model: c.Model}
			}
			if err := db.CreateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			a, err = db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeCard, beforeAttempt := c, a
			r := New(home)
			if _, err := r.failLaunch(ctx, db, &c, &a, launchStepHost, errors.New("synthetic startup error with unknown execution phase"), a.Route, launchScratch{repo: repo, worktree: wt, branch: "fixture-checkout", base: "main", dropWorktree: true, dropBranch: true}); err != nil {
				t.Fatal(err)
			}
			gotCard, err := db.GetCard(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			gotAttempt, err := db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if dual {
				if !reflect.DeepEqual(gotCard, beforeCard) || !reflect.DeepEqual(gotAttempt, beforeAttempt) {
					t.Fatalf("generic failure rewrote comparison: %+v %+v", gotCard, gotAttempt)
				}
				if _, err := os.Stat(filepath.Join(wt, "a.txt")); err != nil {
					t.Fatalf("generic cleanup touched comparison: %v", err)
				}
				git(t, repo, "show-ref", "--verify", "refs/heads/fixture-checkout")
				ev, err := db.ListEvents(ctx, "attempt:"+a.ID)
				if err != nil || len(ev) != 0 {
					t.Fatalf("generic failure fabricated evidence: %+v %v", ev, err)
				}
			} else if gotAttempt.Outcome != model.OutcomeLaunchFailed || gotCard.Status != model.CardWaiting || !strings.HasPrefix(gotCard.WaitReason, "backoff:") {
				t.Fatalf("ordinary non-comparison D1 no longer reaches existing recovery: %+v %+v", gotCard, gotAttempt)
			}
		})
	}
}
