package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/harvest"
	"cardex/internal/model"
	"cardex/internal/store"
)

func TestDefaultHarvestDisposition(t *testing.T) {
	const (
		reworkText = "## 结论\ntests: pass\n\n## 复核结论\nrework\n"
		passText   = "## 结论\nrework the boundary\n\n## 复核结论\npass\n"
		designText = "## 结论\nship the design\n"
		badText    = "## 其他\nnope\n"
	)
	resetAt := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{name: "nil Dispose", run: func(t *testing.T) {
			ctx, home, db, repo, obj := dispositionHome(t)
			finishHarvest(t, db, home, repo, devCard(obj, "c"), model.Attempt{}, "", "", true, nil)
			r := New(home)
			r.Dispose = nil
			if err := r.Tick(ctx); err == nil {
				t.Fatal("nil disposition stored the harvest")
			}
			got := mustAttempt(t, db, "a-c")
			if got.Outcome != "" {
				t.Fatalf("consumed %s", got.Outcome)
			}
			if mustCard(t, db, "c").Status != model.CardRunning {
				t.Fatal("card changed")
			}
		}},
		{name: "failed Dispose", run: func(t *testing.T) {
			ctx, home, db, repo, obj := dispositionHome(t)
			finishHarvest(t, db, home, repo, devCard(obj, "c"), model.Attempt{}, "", "", true, nil)
			calls := 0
			r := New(home)
			r.Dispose = func(context.Context, *model.Card, harvest.Result) error {
				calls++
				return errDispose
			}
			if err := r.Tick(ctx); err == nil {
				t.Fatal("failed disposition stored the harvest")
			}
			if calls != 1 || mustAttempt(t, db, "a-c").Outcome != "" {
				t.Fatalf("calls %d outcome %s", calls, mustAttempt(t, db, "a-c").Outcome)
			}
			if mustCard(t, db, "c").Status != model.CardRunning {
				t.Fatal("card changed")
			}
			r.Dispose = func(context.Context, *model.Card, harvest.Result) error {
				calls++
				return nil
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || mustAttempt(t, db, "a-c").Outcome != model.OutcomePassed {
				t.Fatalf("calls %d outcome %s", calls, mustAttempt(t, db, "a-c").Outcome)
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("delivered again %d", calls)
			}
		}},
		{name: "passed dev", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			branch(t, repo, obj, "c", "keep.txt", "kept\n")
			finishHarvest(t, db, home, repo, devCard(obj, "c"), model.Attempt{Route: model.Route{Channel: "cha"}}, "", "", true, nil)
			tickDefault(t, home)
			card := mustCard(t, db, "c")
			if card.Status != model.CardReviewing {
				t.Fatalf("status %s", card.Status)
			}
			if len(reviewsOf(t, db, "c")) != 1 {
				t.Fatal("passed dev did not dispatch a review")
			}
			if mustAttempt(t, db, "a-c").Outcome != model.OutcomePassed {
				t.Fatal("attempt not consumed")
			}
		}},
		{name: "review pass", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			pauseObjective(t, db, obj)
			plantReview(t, db, home, repo, obj, passText)
			tickDefault(t, home)
			parent := mustCard(t, db, "c1")
			if parent.Status != model.CardMergeQueued {
				t.Fatalf("parent %s", parent.Status)
			}
			if mustCard(t, db, "rv").Status != model.CardDone {
				t.Fatal("review not done")
			}
		}},
		{name: "review rework", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			plantReview(t, db, home, repo, obj, reworkText)
			tickDefault(t, home)
			parent := mustCard(t, db, "c1")
			if parent.Status == model.CardMergeQueued || parent.Status == model.CardMerged {
				t.Fatalf("rework enqueued %s", parent.Status)
			}
			if parent.ReworkCount != 1 {
				t.Fatalf("rework_count %d status %s", parent.ReworkCount, parent.Status)
			}
			if mustCard(t, db, "rv").Status != model.CardDone {
				t.Fatal("review not done")
			}
		}},
		{name: "design", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			card := devCard(obj, "c")
			card.Kind = model.KindDesign
			finishHarvest(t, db, home, repo, card, model.Attempt{}, designText, "", false, nil)
			tickDefault(t, home)
			if mustCard(t, db, "c").Status != model.CardDone {
				t.Fatalf("status %s", mustCard(t, db, "c").Status)
			}
			if mustAttempt(t, db, "a-c").Outcome != model.OutcomePassed {
				t.Fatal("design attempt not consumed")
			}
		}},
		{name: "design via serve", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			card := devCard(obj, "c")
			card.Kind = model.KindDesign
			finishHarvest(t, db, home, repo, card, model.Attempt{}, designText, "", false, nil)
			serveOnce(t, home)
			if mustCard(t, db, "c").Status != model.CardDone {
				t.Fatalf("status %s", mustCard(t, db, "c").Status)
			}
			if mustAttempt(t, db, "a-c").Outcome != model.OutcomePassed {
				t.Fatal("serve left the design attempt unconsumed")
			}
		}},
		{name: "tests_failed", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			branch(t, repo, obj, "c", "keep.txt", "kept\n")
			card := devCard(obj, "c")
			card.Acceptance = []string{"false"}
			finishHarvest(t, db, home, repo, card, model.Attempt{Route: model.Route{Channel: "cha"}}, "", "", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.ReworkCount != 1 || !readyOrRunning(got.Status) {
				t.Fatalf("card %+v", got)
			}
			if mustAttempt(t, db, "a-c").Outcome != model.OutcomeTestsFailed {
				t.Fatalf("outcome %s", mustAttempt(t, db, "a-c").Outcome)
			}
		}},
		{name: "tests_failed past limit", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			card := devCard(obj, "c")
			card.Acceptance = []string{"false"}
			card.ReworkCount = 2
			finishHarvest(t, db, home, repo, card, model.Attempt{}, "", "", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Status != model.CardNeedsInput {
				t.Fatalf("status %s", got.Status)
			}
			if !strings.Contains(questionText(t, db), "rework_count") {
				t.Fatalf("questions %q", questionText(t, db))
			}
		}},
		{name: "no_output", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			card.Channel = "cha"
			finishHarvest(t, db, home, repo, card, model.Attempt{Route: model.Route{Channel: "cha"}}, "", "", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Channel != "chb" || !readyOrRunning(got.Status) {
				t.Fatalf("card %+v", got)
			}
		}},
		{name: "no_output again", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			card.Channel = "cha"
			prior := []model.Attempt{{Index: 0, Outcome: model.OutcomeNoOutput, Route: model.Route{Channel: "cha"}}}
			finishHarvest(t, db, home, repo, card, model.Attempt{Index: 2, Route: model.Route{Channel: "cha"}}, "", "", false, prior)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Status != model.CardNeedsInput || got.Channel != "cha" {
				t.Fatalf("card %+v", got)
			}
		}},
		{name: "result_unparsable", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			finishHarvest(t, db, home, repo, devCard(obj, "c"), model.Attempt{}, badText, "", false, nil)
			tickDefault(t, home)
			parent := mustCard(t, db, "c")
			if parent.Status != model.CardWaiting || !strings.HasPrefix(parent.WaitReason, "normalize:") {
				t.Fatalf("parent %+v", parent)
			}
			norm := normalizeOf(t, db, "c")
			if norm.Tier != model.TierT1 || norm.Kind != model.KindNormalize {
				t.Fatalf("normalize %+v", norm)
			}
		}},
		{name: "normalize failed", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			parent := devCard(obj, "c")
			parent.Status = model.CardWaiting
			if err := db.CreateCard(context.Background(), &parent); err != nil {
				t.Fatal(err)
			}
			norm := devCard(obj, "norm")
			norm.Kind = model.KindNormalize
			norm.Tier = model.TierT1
			norm.Deps = []string{"c"}
			finishHarvest(t, db, home, repo, norm, model.Attempt{}, badText, "", false, nil)
			tickDefault(t, home)
			if mustCard(t, db, "c").Status != model.CardNeedsInput {
				t.Fatalf("parent %s", mustCard(t, db, "c").Status)
			}
			if mustCard(t, db, "norm").Status != model.CardFailed {
				t.Fatalf("normalize %s", mustCard(t, db, "norm").Status)
			}
		}},
		{name: "quota_hit", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			finishHarvest(t, db, home, repo, card, model.Attempt{
				ErrorClass: model.ErrorQuotaHit, Route: model.Route{Channel: "cha"},
			}, "", "quota hit reset at "+resetAt, false, nil)
			tickDefault(t, home)
			ch, err := db.GetChannel(context.Background(), "cha")
			if err != nil || !strings.Contains(ch.LastError, "quota blocked") {
				t.Fatalf("channel %+v %v", ch, err)
			}
			got := mustCard(t, db, "c")
			if got.Channel != "chb" || !readyOrRunning(got.Status) {
				t.Fatalf("card %+v", got)
			}
		}},
		{name: "auth_failed", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			finishHarvest(t, db, home, repo, devCard(obj, "c"), model.Attempt{
				ErrorClass: model.ErrorAuthFailed, Route: model.Route{Channel: "cha"},
			}, "", "token rejected", false, nil)
			tickDefault(t, home)
			ch, err := db.GetChannel(context.Background(), "cha")
			if err != nil || ch.Health != model.HealthAuthFailed {
				t.Fatalf("channel %+v %v", ch, err)
			}
			if _, err := os.Stat(filepath.Join(home, "notifications", "auth-cha.json")); err != nil {
				t.Fatal(err)
			}
			got := mustCard(t, db, "c")
			if got.Channel != "chb" || !readyOrRunning(got.Status) {
				t.Fatalf("card %+v", got)
			}
		}},
		{name: "transport", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			finishHarvest(t, db, home, repo, card, model.Attempt{
				ErrorClass: model.ErrorTransport, Route: model.Route{Channel: "cha"},
			}, "", "connection reset", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Channel != "cha" || got.Status != model.CardWaiting || !strings.HasPrefix(got.WaitReason, "backoff:") {
				t.Fatalf("card %+v", got)
			}
		}},
		{name: "transport switch", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			priors := []model.Attempt{
				{Index: 0, Outcome: model.OutcomeTransport, Route: model.Route{Channel: "cha"}},
				{Index: 1, Outcome: model.OutcomeTransport, Route: model.Route{Channel: "cha"}},
			}
			finishHarvest(t, db, home, repo, card, model.Attempt{
				Index: 2, ErrorClass: model.ErrorTransport, Route: model.Route{Channel: "cha"},
			}, "", "connection reset", false, priors)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Channel != "chb" || !readyOrRunning(got.Status) {
				t.Fatalf("card %+v", got)
			}
		}},
		{name: "timeout", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			finishHarvest(t, db, home, repo, card, model.Attempt{
				ErrorClass: model.ErrorTimeout, Route: model.Route{Channel: "cha"},
			}, "", "timed out", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Channel != "cha" || got.Status != model.CardWaiting || !strings.HasPrefix(got.WaitReason, "backoff:") {
				t.Fatalf("card %+v", got)
			}
			if mustAttempt(t, db, "a-c").Outcome != model.OutcomeTimeout {
				t.Fatalf("outcome %s", mustAttempt(t, db, "a-c").Outcome)
			}
		}},
		{name: "permission_denied", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			card.Channel = "cha"
			finishHarvest(t, db, home, repo, card, model.Attempt{
				ErrorClass: model.ErrorPermissionDenied, Route: model.Route{Channel: "cha"},
			}, "", "rejected rm -rf /var/forbidden", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Status != model.CardNeedsInput || got.Channel != "cha" {
				t.Fatalf("card %+v", got)
			}
			if !strings.Contains(questionText(t, db), "rm -rf /var/forbidden") {
				t.Fatalf("questions %q", questionText(t, db))
			}
		}},
		{name: "refusal", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			card.Channel = "cha"
			finishHarvest(t, db, home, repo, card, model.Attempt{
				ErrorClass: model.ErrorRefusal, Route: model.Route{Channel: "cha"},
			}, "", "refused", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Status != model.CardNeedsInput || got.Channel != "cha" {
				t.Fatalf("card %+v", got)
			}
			if questionText(t, db) == "" {
				t.Fatal("no question")
			}
		}},
		{name: "budget_limited", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			seedRoutes(t, db)
			card := devCard(obj, "c")
			finishHarvest(t, db, home, repo, card, model.Attempt{
				ErrorClass: model.ErrorBudgetLimited, Route: model.Route{Channel: "cha"},
			}, "", "budget", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Status == model.CardDone || got.Status == model.CardNeedsInput || !readyOrRunning(got.Status) {
				t.Fatalf("card %+v", got)
			}
			if mustAttempt(t, db, "a-c").Outcome != model.OutcomeBudgetLimited {
				t.Fatalf("outcome %s", mustAttempt(t, db, "a-c").Outcome)
			}
		}},
		{name: "budget_limited again", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			card := devCard(obj, "c")
			prior := []model.Attempt{{Index: 0, Outcome: model.OutcomeBudgetLimited, Route: model.Route{Channel: "cha"}}}
			finishHarvest(t, db, home, repo, card, model.Attempt{
				Index: 2, ErrorClass: model.ErrorBudgetLimited, Route: model.Route{Channel: "cha"},
			}, "", "budget", false, prior)
			tickDefault(t, home)
			if mustCard(t, db, "c").Status != model.CardNeedsInput || questionText(t, db) == "" {
				t.Fatalf("card %+v questions %q", mustCard(t, db, "c"), questionText(t, db))
			}
		}},
		{name: "violation", run: func(t *testing.T) {
			_, home, db, repo, obj := dispositionHome(t)
			branch(t, repo, obj, "c", "keep.txt", "kept\n")
			card := devCard(obj, "c")
			finishHarvest(t, db, home, repo, card, model.Attempt{
				ErrorClass: model.ErrorViolation,
				Branch:     "cardex/" + obj + "/c/c",
			}, "", "touched a forbidden path", false, nil)
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			if got.Status != model.CardNeedsInput {
				t.Fatalf("status %s", got.Status)
			}
			if !branchExists(repo, "cardex/"+obj+"/c/c") {
				t.Fatal("violation deleted the branch")
			}
			if !eventsOf(t, db, "a-c", "attempt_voided") {
				t.Fatal("missing attempt_voided")
			}
			if questionText(t, db) == "" {
				t.Fatal("no question")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t) })
	}
}

func TestScheduleTierT9DoesNotFailTick(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	obj := model.Objective{
		ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	proj := model.Project{ID: "p", RepoPath: t.TempDir(), TestCmd: "true", DesignDir: "docs"}
	if err := db.CreateProject(ctx, &proj); err != nil || db.CreateObjective(ctx, &obj) != nil {
		t.Fatal("seed")
	}
	cfg := `{
	  "schedules": [
	    {"name":"bad-tier","every":"1h","card_template":{"kind":"ranking","tier":"T9","title":"BadTier"}},
	    {"name":"bad-kind","every":"1h","card_template":{"kind":"nope","tier":"T9","title":"BadKind"}},
	    {"name":"bad-every","every":"not-a-duration","card_template":{"kind":"ranking","title":"BadEvery"}},
	    {"name":"good","every":"1h","card_template":{"kind":"ranking","title":"Good"}}
	  ]
	}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Now = func() time.Time { return time.Date(2026, 1, 1, 15, 30, 0, 0, time.UTC) }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	cards, err := db.ListCards(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]model.Card{}
	for _, card := range cards {
		got[card.Title] = card
	}
	if _, ok := got["Good"]; !ok {
		t.Fatalf("sibling missing %+v", cards)
	}
	if _, ok := got["BadEvery"]; ok {
		t.Fatal("bad every enqueued")
	}
	for _, title := range []string{"BadTier", "BadKind"} {
		card, ok := got[title]
		if !ok || !card.Tier.Valid() || card.Tier == "T9" || !card.Kind.Valid() {
			t.Fatalf("%s %+v", title, card)
		}
	}
	if got["BadKind"].Kind != model.KindRanking {
		t.Fatalf("kind %+v", got["BadKind"])
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := db.ListCards(ctx, "o")
	if err != nil || len(again) != len(cards) {
		t.Fatalf("second tick %+v", again)
	}
}

func TestScheduleNamesKeepDistinctIdentities(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	obj := model.Objective{
		ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	proj := model.Project{ID: "p", RepoPath: t.TempDir(), TestCmd: "true", DesignDir: "docs"}
	if err := db.CreateProject(ctx, &proj); err != nil || db.CreateObjective(ctx, &obj) != nil {
		t.Fatal("seed")
	}
	type row struct {
		Name     string `json:"name"`
		Every    string `json:"every"`
		Template struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
		} `json:"card_template"`
	}
	names := []string{"日报", "周报", "a.b", "ab"}
	var entries []row
	for _, name := range names {
		var item row
		item.Name = name
		item.Every = "1h"
		item.Template.Kind = "ranking"
		item.Template.Title = name
		entries = append(entries, item)
	}
	raw, err := json.Marshal(map[string]any{"schedules": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if scheduleIdentity("日报") == scheduleIdentity("周报") || scheduleIdentity("a.b") == scheduleIdentity("ab") {
		t.Fatal("sanitized names share an identity")
	}
	r := New(home)
	r.Now = func() time.Time { return time.Date(2026, 1, 1, 15, 30, 0, 0, time.UTC) }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	cards, err := db.ListCards(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != len(names) {
		t.Fatalf("cards %+v", cards)
	}
	seen := map[string]bool{}
	byTitle := map[string]model.Card{}
	for _, card := range cards {
		if seen[card.ID] {
			t.Fatalf("duplicate id %s", card.ID)
		}
		seen[card.ID] = true
		byTitle[card.Title] = card
	}
	for _, name := range names {
		card, ok := byTitle[name]
		if !ok || !strings.Contains(card.ID, scheduleIdentity(name)) {
			t.Fatalf("%s %+v", name, card)
		}
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := db.ListCards(ctx, "o")
	if err != nil || len(again) != len(names) {
		t.Fatalf("second tick %+v", again)
	}
}

func TestDispatchSameSymbolTwoRepos(t *testing.T) {
	ctx := context.Background()
	home, db, repoA := newHome(t)
	_, objA := seedRepo(t, db, repoA, "true")
	repoB := filepath.Join(filepath.Dir(repoA), "repo-b")
	if err := os.MkdirAll(repoB, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repoB, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoB, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repoB, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	git(t, repoB, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	projB := model.Project{ID: "p2", RepoPath: repoB, WorktreeRoot: filepath.Join(filepath.Dir(repoB), "wt-b"), TestCmd: "true", DesignDir: "docs"}
	objB := model.Objective{
		ID: "o2", Project: "p2", Title: "o2", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateProject(ctx, &projB); err != nil || db.CreateObjective(ctx, &objB) != nil {
		t.Fatal("seed b")
	}
	seedRoutes(t, db)
	held := devCard(objA, "held")
	held.Status = model.CardRunning
	held.Claims = []model.Claim{{Path: "main.go", Symbol: "Run"}}
	if err := db.CreateCard(ctx, &held); err != nil {
		t.Fatal(err)
	}
	heldAttempt := finishedAttempt("a-held", "held")
	if err := db.CreateAttempt(ctx, &heldAttempt); err != nil {
		t.Fatal(err)
	}
	ready := devCard(objB.ID, "other")
	ready.Status = model.CardReady
	ready.Claims = []model.Claim{{Path: "main.go", Symbol: "Run"}}
	if err := db.CreateCard(ctx, &ready); err != nil {
		t.Fatal(err)
	}
	tickDefault(t, home)
	attempts, err := db.ListAttempts(ctx, "other")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts %+v %v", attempts, err)
	}
	if mustCard(t, db, "other").Status == model.CardWaiting {
		t.Fatal("other repo waited")
	}
}

func TestDispatchSameRepoStillWaits(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	held := devCard(obj, "held")
	held.Status = model.CardRunning
	held.Claims = []model.Claim{{Path: "main.go", Symbol: "Run"}}
	if err := db.CreateCard(ctx, &held); err != nil {
		t.Fatal(err)
	}
	heldAttempt := finishedAttempt("a-held", "held")
	if err := db.CreateAttempt(ctx, &heldAttempt); err != nil {
		t.Fatal(err)
	}
	ready := devCard(obj, "next")
	ready.Status = model.CardReady
	ready.Claims = []model.Claim{{Path: "main.go", Symbol: "Run"}}
	if err := db.CreateCard(ctx, &ready); err != nil {
		t.Fatal(err)
	}
	tickDefault(t, home)
	attempts, err := db.ListAttempts(ctx, "next")
	if err != nil || len(attempts) != 0 {
		t.Fatalf("started %+v %v", attempts, err)
	}
	got := mustCard(t, db, "next")
	if got.Status != model.CardWaiting || !strings.Contains(got.WaitReason, "Run") {
		t.Fatalf("wait %+v", got)
	}
}

func TestClaimedRunningCardResumes(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := devCard(obj, "c")
	card.Status = model.CardRunning
	card.Claims = []model.Claim{{Path: "main.go", Symbol: "Run"}}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	open := model.Attempt{
		ID: "a-c", Card: "c", PID: 0, Worktree: repo,
		Branch: "cardex/" + obj + "/c/c", Route: model.Route{Channel: "cha"},
		StartedAt: time.Now().UTC(),
	}
	if err := db.CreateAttempt(ctx, &open); err != nil {
		t.Fatal(err)
	}
	tickDefault(t, home)
	got := mustAttempt(t, db, "a-c")
	if got.PID != 41 {
		t.Fatalf("attempt %+v", got)
	}
	card = mustCard(t, db, "c")
	if card.Status == model.CardWaiting {
		t.Fatalf("self conflict %+v", card)
	}
}

func TestNestedSnapshotIsNotConfidentFull(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	soon := now.Add(30 * time.Minute)
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	ch := model.Channel{
		ID: "ch", Runner: model.RunnerFake, Family: "alpha", Models: []string{"model-a"},
		Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2, Redline: 0.2,
		QuotaWindows: []model.QuotaWindow{
			{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 90},
			{Name: "daily", ResetRule: "rolling", ResetAt: soon, LimitTokens: 1000, UsedTokens: 0},
		},
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{
		ID: "pos-ch", Model: "model-a", Family: "alpha",
		Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 1,
	}
	if err := db.CreatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
	back := model.Card{
		ID: "back", Objective: obj, Kind: model.KindBacklog, Title: "back",
		Tier: model.TierT1, Status: model.CardReady,
	}
	if err := db.CreateCard(ctx, &back); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Now = func() time.Time { return now }
	r.Launch = func(context.Context, string, string) (int, error) { return 41, nil }
	channels, err := db.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view, err := r.quotaView(ctx, db, channels)
	if err != nil {
		t.Fatal(err)
	}
	est, err := view.Remaining(ctx, "ch")
	if err != nil {
		t.Fatal(err)
	}
	if !est.Unknown || est.Confidence >= 1 {
		t.Fatalf("known full balance %+v", est)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListAttempts(ctx, "back")
	if err != nil || len(attempts) != 0 || mustCard(t, db, "back").Status != model.CardReady {
		t.Fatalf("filled %+v card %+v %v", attempts, mustCard(t, db, "back"), err)
	}
}

func dispositionHome(t *testing.T) (context.Context, string, *store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	return ctx, home, db, repo, obj
}

func finishedAttempt(id, cardID string) model.Attempt {
	ended := time.Now().UTC()
	exit := 0
	return model.Attempt{
		ID: id, Card: cardID, PID: 9, Route: model.Route{Channel: "cha"},
		StartedAt: ended.Add(-time.Minute), EndedAt: ended, Exit: &exit,
		Outcome: model.OutcomePassed,
	}
}

func devCard(obj, id string) model.Card {
	return model.Card{
		ID: id, Objective: obj, Kind: model.KindDev, Title: id,
		Tier: model.TierT2, Status: model.CardRunning,
	}
}

func tickDefault(t *testing.T, home string) {
	t.Helper()
	r := New(home)
	if r.Dispose == nil {
		t.Fatal("production runner left Dispose nil")
	}
	r.Launch = func(context.Context, string, string) (int, error) { return 41, nil }
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func pauseObjective(t *testing.T, db *store.Store, id string) {
	t.Helper()
	obj, err := db.GetObjective(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	obj.Status = model.ObjectivePaused
	if err := db.UpdateObjective(context.Background(), &obj); err != nil {
		t.Fatal(err)
	}
}

func plantReview(t *testing.T, db *store.Store, home, repo, obj, text string) {
	t.Helper()
	branch(t, repo, obj, "c1", "unique.txt", "candidate\n")
	subject := devCard(obj, "c1")
	subject.Status = model.CardReviewing
	if err := db.CreateCard(context.Background(), &subject); err != nil {
		t.Fatal(err)
	}
	card := devCard(obj, "rv")
	card.Kind = model.KindReview
	card.Tier = model.TierT3
	card.Deps = []string{"c1"}
	finishHarvest(t, db, home, repo, card, model.Attempt{}, text, "", false, nil)
}

func finishHarvest(t *testing.T, db *store.Store, home, repo string, card model.Card, attempt model.Attempt, text, output string, change bool, priors []model.Attempt) {
	t.Helper()
	ctx := context.Background()
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	exit := 0
	for i, prior := range priors {
		if prior.ID == "" {
			prior.ID = card.ID + "-prior-" + string(rune('a'+i))
		}
		prior.Card = card.ID
		if prior.Exit == nil {
			prior.Exit = &exit
		}
		if prior.EndedAt.IsZero() {
			prior.EndedAt = time.Now().UTC().Add(-time.Hour)
		}
		if err := db.CreateAttempt(ctx, &prior); err != nil {
			t.Fatal(err)
		}
	}
	wt := plantWorktree(t, repo, change)
	if text != "" {
		if err := os.WriteFile(filepath.Join(wt, ".cardex-result.md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if attempt.ID == "" {
		attempt.ID = "a-" + card.ID
	}
	attempt.Card = card.ID
	attempt.Worktree = wt
	if attempt.BaseCommit == "" {
		base := "HEAD"
		if change {
			base = "HEAD~1"
		}
		attempt.BaseCommit = rev(t, repo, base)
	}
	if attempt.EndedAt.IsZero() {
		attempt.EndedAt = time.Now().UTC()
	}
	if attempt.Exit == nil {
		attempt.Exit = &exit
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	if output != "" {
		dir := filepath.Join(home, "attempts", attempt.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "output.log"), []byte(output), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustCard(t *testing.T, db *store.Store, id string) model.Card {
	t.Helper()
	card, err := db.GetCard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func mustAttempt(t *testing.T, db *store.Store, id string) model.Attempt {
	t.Helper()
	attempt, err := db.GetAttempt(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func reviewsOf(t *testing.T, db *store.Store, id string) []model.Card {
	t.Helper()
	cards, err := db.ListCards(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Card
	for _, card := range cards {
		if card.Kind != model.KindReview {
			continue
		}
		for _, dep := range card.Deps {
			if dep == id {
				out = append(out, card)
			}
		}
	}
	return out
}

func normalizeOf(t *testing.T, db *store.Store, id string) model.Card {
	t.Helper()
	cards, err := db.ListCards(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		if card.Kind != model.KindNormalize {
			continue
		}
		for _, dep := range card.Deps {
			if dep == id {
				return card
			}
		}
	}
	t.Fatal("missing normalize card")
	return model.Card{}
}

func questionText(t *testing.T, db *store.Store) string {
	t.Helper()
	questions, err := db.ListQuestions(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, question := range questions {
		b.WriteString(question.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func readyOrRunning(status model.CardStatus) bool {
	return status == model.CardReady || status == model.CardRunning
}

func TestR3CLaneNoteStaysWithinLimit(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "v2", "lane-notes", "R3-C.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > 40 {
		t.Fatalf("R3-C.md has %d lines", len(lines))
	}
	if !strings.Contains(text, "接口请求") || !strings.Contains(text, "已知问题") {
		t.Fatal("R3-C.md dropped 接口请求 or 已知问题")
	}
}

var errDispose = errString("dispose down")

type errString string

func (e errString) Error() string { return string(e) }
