package sched

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/harvest"
	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/prompt"
	"cardex/internal/route"
)

func TestHarvestDeliversBeforeStatusChange(t *testing.T) {
	cases := []struct {
		name    string
		kind    model.CardKind
		fail    bool
		review  bool
		unclear bool
		reuse   bool
		want    model.Outcome
	}{
		{name: "passed dev", kind: model.KindDev, want: model.OutcomePassed},
		{name: "passed design", kind: model.KindDesign, want: model.OutcomePassed},
		{name: "failed dev", kind: model.KindDev, fail: true, want: model.OutcomeNoOutput},
		{name: "unclear review", kind: model.KindReview, review: true, unclear: true, want: model.OutcomePassed},
		{name: "finished review id", kind: model.KindDev, reuse: true, want: model.OutcomePassed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			subject := model.Card{
				ID: "c1", Objective: obj, Kind: model.KindDev, Title: "c1", Tier: model.TierT2,
				Status: model.CardReviewing,
			}
			if err := db.CreateCard(ctx, &subject); err != nil {
				t.Fatal(err)
			}
			card := subject
			card.ID = "c1"
			card.Kind = tc.kind
			card.Status = model.CardRunning
			if tc.review {
				card.ID = "rv-c1"
				card.Deps = []string{"c1"}
				if err := db.CreateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
			} else if tc.reuse {
				done := model.Card{
					ID: "rv-c1", Objective: obj, Kind: model.KindReview, Title: "review",
					Tier: model.TierT3, Status: model.CardDone, Deps: []string{"c1"},
				}
				if err := db.CreateCard(ctx, &done); err != nil {
					t.Fatal(err)
				}
				if err := db.UpdateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
			} else if tc.kind != model.KindDev || tc.fail {
				if err := db.UpdateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.UpdateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
			}
			changed := !tc.fail && tc.kind != model.KindDesign && !tc.unclear
			wt := plantWorktree(t, repo, changed)
			if tc.kind == model.KindDesign || tc.unclear {
				body := "## 结论\ndone\n"
				if tc.unclear {
					body = "## 结论\nno verdict\n"
				}
				if err := os.WriteFile(filepath.Join(wt, ".cardex-result.md"), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			exit := 0
			ended := time.Now().UTC()
			base := rev(t, repo, "HEAD")
			if changed {
				base = rev(t, repo, "HEAD~1")
			}
			attempt := model.Attempt{
				ID: "a-" + card.ID, Card: card.ID, Worktree: wt, BaseCommit: base,
				Exit: &exit, EndedAt: ended,
			}
			if err := db.CreateAttempt(ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			var got harvest.Result
			var status model.CardStatus
			var calls int
			r := New(home)
			r.Dispose = func(_ context.Context, seen *model.Card, res harvest.Result) error {
				calls++
				got = res
				status = seen.Status
				return nil
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("disposition calls %d", calls)
			}
			if got.Outcome != tc.want {
				t.Fatalf("outcome %s want %s", got.Outcome, tc.want)
			}
			if status == model.CardReviewing || status == model.CardDone || status == model.CardNeedsInput || status == model.CardMerged || status == model.CardMergeQueued {
				t.Fatalf("status %s changed before disposition returned", status)
			}
			after, err := db.GetCard(ctx, card.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != model.CardRunning {
				t.Fatalf("production status %s", after.Status)
			}
			if tc.unclear {
				sub, err := db.GetCard(ctx, "c1")
				if err != nil || sub.Status == model.CardMergeQueued || sub.Status == model.CardMerged {
					t.Fatalf("unclear review enqueued subject %+v %v", sub, err)
				}
			}
			if tc.reuse && after.Status == model.CardReviewing {
				t.Fatal("reused finished rv-c1 and left the card reviewing")
			}
		})
	}
}

func TestReviewWorkspaceShowsUnmergedCandidate(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	branch(t, repo, obj, "c1", "unique.txt", "candidate-not-on-main\n")
	subject := model.Card{
		ID: "c1", Objective: obj, Kind: model.KindDev, Title: "c1", Tier: model.TierT2,
		Status: model.CardReviewing,
	}
	if err := db.CreateCard(ctx, &subject); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "rv-c1", Objective: obj, Kind: model.KindReview, Title: "review c1",
		Tier: model.TierT3, Status: model.CardReady, Deps: []string{"c1"},
		Body: reviewPassScript,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListAttempts(ctx, "rv-c1")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts %+v %v", attempts, err)
	}
	body, err := os.ReadFile(filepath.Join(attempts[0].Worktree, "unique.txt"))
	if err != nil || !strings.Contains(string(body), "candidate-not-on-main") {
		t.Fatalf("review worktree %q %v", body, err)
	}
	if branchExists(repo, "cardex/"+obj+"/c/rv-c1") {
		t.Fatal("review dispatch created a branch from its own id")
	}
	head := rev(t, attempts[0].Worktree, "HEAD")
	if head != rev(t, repo, "cardex/"+obj+"/c/c1") {
		t.Fatalf("review HEAD %s is not the candidate", head)
	}
}

func TestReviewOfRepairShowsRootCandidate(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	branch(t, repo, obj, "c1", "unique.txt", "repair-candidate\n")
	for _, card := range []model.Card{
		{ID: "c1", Objective: obj, Kind: model.KindDev, Title: "c1", Tier: model.TierT2, Status: model.CardWaiting},
		{ID: "rp-c1", Objective: obj, Kind: model.KindRepair, Title: "repair", Tier: model.TierT2, Status: model.CardReviewing, Deps: []string{"c1"}},
		{ID: "rv-rp-c1", Objective: obj, Kind: model.KindReview, Title: "review repair", Tier: model.TierT3, Status: model.CardReady, Deps: []string{"rp-c1"}, Body: reviewPassScript},
	} {
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListAttempts(ctx, "rv-rp-c1")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts %+v %v", attempts, err)
	}
	body, err := os.ReadFile(filepath.Join(attempts[0].Worktree, "unique.txt"))
	if err != nil || !strings.Contains(string(body), "repair-candidate") {
		t.Fatalf("review worktree %q %v", body, err)
	}
	if branchExists(repo, "cardex/"+obj+"/c/rv-rp-c1") || branchExists(repo, "cardex/"+obj+"/c/rp-c1") {
		t.Fatal("review of a repair created a branch under the review or repair id")
	}
	head := rev(t, attempts[0].Worktree, "HEAD")
	if head != rev(t, repo, "cardex/"+obj+"/c/c1") {
		t.Fatalf("review HEAD %s is not the root candidate", head)
	}
}

func TestAutomaticRouteSeesQuotaAndOccupancy(t *testing.T) {
	ctx := context.Background()
	t.Run("redline", func(t *testing.T) {
		home, db, repo := newHome(t)
		_, obj := seedRepo(t, db, repo, "true")
		now := time.Now().UTC()
		ch := model.Channel{
			ID: "ch", Runner: model.RunnerFake, Family: "alpha", Models: []string{"model-a"},
			Concurrency: 2, Health: model.HealthOK, Tier: model.TierT2, Redline: 0.2,
			QuotaWindows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: now.Add(30 * time.Minute),
				LimitTokens: 100, UsedTokens: 80, Remaining: 0.2, Confidence: 1,
			}},
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
		card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		prev := chooseRoute
		t.Cleanup(func() { chooseRoute = prev })
		saw := false
		chooseRoute = func(ctx context.Context, req route.Request) (model.Route, model.RouteReason, error) {
			if req.Quota == nil {
				t.Fatal("nil quota")
			}
			saw = true
			return prev(ctx, req)
		}
		r := New(home)
		r.Now = func() time.Time { return now }
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if !saw {
			t.Fatal("Select was not called")
		}
		got, err := db.GetCard(ctx, "c")
		if err != nil || got.Status != model.CardReady {
			t.Fatalf("redline card %+v %v", got, err)
		}
		attempts, _ := db.ListAttempts(ctx, "c")
		if len(attempts) != 0 {
			t.Fatalf("started at redline %+v", attempts)
		}
	})
	t.Run("concurrency", func(t *testing.T) {
		home, db, repo := newHome(t)
		_, obj := seedRepo(t, db, repo, "true")
		ch := model.Channel{
			ID: "ch", Runner: model.RunnerFake, Family: "alpha", Models: []string{"model-a"},
			Concurrency: 1, Health: model.HealthOK, Tier: model.TierT2,
		}
		if err := db.CreateChannel(ctx, &ch); err != nil {
			t.Fatal(err)
		}
		pos := model.Positioning{
			ID: "pos-ch", Model: "model-a", Family: "alpha",
			Tiers: []model.Tier{model.TierT2}, Preference: 1,
		}
		if err := db.CreatePositioning(ctx, &pos); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"c1", "c2"} {
			card := model.Card{
				ID: id, Objective: obj, Kind: model.KindDev, Title: id, Tier: model.TierT2, Status: model.CardReady,
				Claims: []model.Claim{{Path: "f.go", Symbol: id}},
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
		}
		prev := chooseRoute
		t.Cleanup(func() { chooseRoute = prev })
		var uses []int
		chooseRoute = func(ctx context.Context, req route.Request) (model.Route, model.RouteReason, error) {
			if req.Quota == nil {
				t.Fatal("nil quota")
			}
			uses = append(uses, req.InUse["ch"])
			return prev(ctx, req)
		}
		r := New(home)
		r.Launch = func(context.Context, string, string) (int, error) { return 50, nil }
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		attempts, err := db.ListAttempts(ctx, "")
		if err != nil || len(attempts) != 1 {
			t.Fatalf("attempts %+v %v", attempts, err)
		}
		if len(uses) < 2 || uses[1] < 1 {
			t.Fatalf("same-tick occupancy %+v", uses)
		}
	})
}

func TestFillReusesAutomaticChecks(t *testing.T) {
	now := time.Now().UTC()
	ctx := context.Background()
	cases := []struct {
		name    string
		health  model.Health
		remain  float64
		redline float64
		mask    bool
		web     bool
		busy    bool
		prefer  bool
	}{
		{name: "health", health: model.HealthDegraded, remain: 0.9},
		{name: "redline", health: model.HealthOK, remain: 0.1, redline: 0.2},
		{name: "slack masks redline", health: model.HealthOK, remain: 0.9, redline: 0.2, mask: true},
		{name: "capability", health: model.HealthOK, remain: 0.9, web: true},
		{name: "concurrency", health: model.HealthOK, remain: 0.9, busy: true},
		{name: "not first model", health: model.HealthOK, remain: 0.9, prefer: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			wins := []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: now.Add(30 * time.Minute),
				Remaining: tc.remain, Confidence: 1,
			}}
			if tc.mask {
				wins = append(wins, model.QuotaWindow{
					Name: "daily", ResetRule: "rolling", ResetAt: now.Add(40 * time.Minute),
					Remaining: 0.1, Confidence: 1,
				})
			}
			models := []string{"model-a"}
			if tc.prefer {
				models = []string{"model-a", "model-b"}
			}
			ch := model.Channel{
				ID: "ch", Runner: model.RunnerFake, Family: "alpha", Models: models,
				Concurrency: 1, Health: tc.health, Tier: model.TierT1, Redline: tc.redline,
				QuotaWindows: wins,
			}
			if err := db.CreateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			prefA := 1
			if tc.prefer {
				prefA = 2
			}
			rows := []model.Positioning{{
				ID: "pos-a", Model: "model-a", Family: "alpha",
				Tiers: []model.Tier{model.TierT1}, Preference: prefA,
			}}
			if tc.prefer {
				rows = append(rows, model.Positioning{
					ID: "pos-b", Model: "model-b", Family: "alpha",
					Tiers: []model.Tier{model.TierT1}, Preference: 1,
				})
			}
			for _, row := range rows {
				if err := db.CreatePositioning(ctx, &row); err != nil {
					t.Fatal(err)
				}
			}
			card := model.Card{
				ID: "back", Objective: obj, Kind: model.KindBacklog, Title: "back",
				Tier: model.TierT1, Status: model.CardReady,
			}
			if tc.web {
				card.Requires = []string{"web"}
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			if tc.busy {
				holder := model.Card{ID: "hold", Objective: obj, Kind: model.KindDev, Title: "hold", Status: model.CardRunning}
				if err := db.CreateCard(ctx, &holder); err != nil {
					t.Fatal(err)
				}
				live := model.Attempt{ID: "a-hold", Card: "hold", PID: 1, Route: model.Route{Channel: "ch", Model: "model-a"}, StartedAt: now}
				if err := db.CreateAttempt(ctx, &live); err != nil {
					t.Fatal(err)
				}
			}
			prev := chooseRoute
			t.Cleanup(func() { chooseRoute = prev })
			sawQuota := false
			chooseRoute = func(ctx context.Context, req route.Request) (model.Route, model.RouteReason, error) {
				if req.Quota == nil {
					t.Fatal("nil quota")
				}
				if req.Card.Channel != "" || req.Objective.Channel != "" {
					t.Fatal("fill pinned a channel")
				}
				sawQuota = true
				return prev(ctx, req)
			}
			r := New(home)
			r.Now = func() time.Time { return now }
			r.Launch = func(context.Context, string, string) (int, error) { return 61, nil }
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := db.GetCard(ctx, "back")
			if err != nil {
				t.Fatal(err)
			}
			attempts, err := db.ListAttempts(ctx, "back")
			if err != nil {
				t.Fatal(err)
			}
			if tc.prefer {
				if !sawQuota || len(attempts) != 1 || attempts[0].Route.Model != "model-b" {
					t.Fatalf("model %+v saw %v card %+v", attempts, sawQuota, got)
				}
				return
			}
			if (tc.name == "health" || tc.name == "capability" || tc.name == "concurrency") && !sawQuota {
				t.Fatal("fill did not call Select")
			}
			if got.Status != model.CardReady || len(attempts) != 0 {
				t.Fatalf("fill started %+v attempts %+v", got, attempts)
			}
		})
	}
}

func TestDispatchRefusesUnfinishedDepsPausedClaims(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		status  model.ObjectiveStatus
		kind    model.CardKind
		deps    bool
		claim   bool
		backlog bool
		start   bool
	}{
		{name: "unfinished dep", status: model.ObjectiveActive, kind: model.KindDev, deps: true},
		{name: "paused backlog", status: model.ObjectivePaused, kind: model.KindBacklog, backlog: true},
		{name: "canceled backlog", status: model.ObjectiveCanceled, kind: model.KindBacklog, backlog: true},
		{name: "running backlog holds symbol", status: model.ObjectiveActive, kind: model.KindDev, claim: true},
		{name: "review does not wait for merge", status: model.ObjectiveActive, kind: model.KindReview, deps: true, start: true},
		{name: "repair does not wait for merge", status: model.ObjectiveActive, kind: model.KindRepair, deps: true, start: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, db, repo := newHome(t)
			_, objID := seedRepo(t, db, repo, "true")
			obj, err := db.GetObjective(ctx, objID)
			if err != nil {
				t.Fatal(err)
			}
			obj.Status = tc.status
			if err := db.UpdateObjective(ctx, &obj); err != nil {
				t.Fatal(err)
			}
			if tc.backlog {
				now := time.Now().UTC()
				channelNear(t, db, "ch", now.Add(30*time.Minute), 0.9, 0)
			} else {
				seedRoutes(t, db)
			}
			if tc.deps && tc.kind != model.KindReview && tc.kind != model.KindRepair {
				other := model.Card{ID: "dep", Objective: objID, Kind: model.KindDev, Title: "dep", Tier: model.TierT2, Status: model.CardReady}
				if err := db.CreateCard(ctx, &other); err != nil {
					t.Fatal(err)
				}
			}
			if tc.claim {
				held := model.Card{
					ID: "back", Objective: objID, Kind: model.KindBacklog, Title: "back",
					Status: model.CardRunning, Claims: []model.Claim{{Path: "f.go", Symbol: "Foo"}},
				}
				if err := db.CreateCard(ctx, &held); err != nil {
					t.Fatal(err)
				}
				live := model.Attempt{ID: "a-back", Card: "back", PID: 9, Route: model.Route{Channel: "cha"}, StartedAt: time.Now().UTC()}
				if err := db.CreateAttempt(ctx, &live); err != nil {
					t.Fatal(err)
				}
			}
			card := model.Card{
				ID: "c", Objective: objID, Kind: tc.kind, Title: "c", Tier: model.TierT2, Status: model.CardReady,
			}
			if tc.kind == model.KindBacklog {
				card.Tier = model.TierT1
			}
			if tc.deps {
				card.Deps = []string{"dep"}
				if tc.kind == model.KindReview || tc.kind == model.KindRepair {
					parent := model.Card{ID: "dep", Objective: objID, Kind: model.KindDev, Title: "dep", Tier: model.TierT2, Status: model.CardWaiting}
					if err := db.CreateCard(ctx, &parent); err != nil {
						t.Fatal(err)
					}
					if tc.kind == model.KindRepair || tc.kind == model.KindReview {
						branch(t, repo, objID, "dep", "dep.txt", "parent\n")
					}
				}
			}
			if tc.claim {
				card.Claims = []model.Claim{{Path: "f.go", Symbol: "Foo"}}
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			if tc.backlog {
				r.Now = func() time.Time { return time.Now().UTC() }
			}
			r.Launch = func(context.Context, string, string) (int, error) { return 70, nil }
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			attempts, err := db.ListAttempts(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			if tc.start {
				if len(attempts) != 1 {
					t.Fatalf("want start, attempts %+v", attempts)
				}
				return
			}
			if len(attempts) != 0 {
				t.Fatalf("started anyway %+v", attempts)
			}
			if tc.claim {
				got, err := db.GetCard(ctx, "c")
				if err != nil || got.Status != model.CardWaiting || !strings.Contains(got.WaitReason, "Foo") {
					t.Fatalf("claim wait %+v %v", got, err)
				}
			}
		})
	}
}

func TestPromptFailureResumesSameBranch(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	n := 0
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) {
		n++
		if n == 1 {
			return "", errors.New("prompt down")
		}
		return "PROMPT-OK", nil
	}
	r.Launch = func(context.Context, string, string) (int, error) { return 81, nil }
	if err := r.Tick(ctx); err == nil {
		t.Fatal("prompt failure did not surface")
	}
	first, err := db.ListAttempts(ctx, "c")
	if err != nil || len(first) != 1 || first[0].PID != 0 {
		t.Fatalf("partial %+v %v", first, err)
	}
	if !branchExists(repo, first[0].Branch) {
		t.Fatal("branch missing after prompt failure")
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := db.ListAttempts(ctx, "c")
	if err != nil || len(again) != 1 || again[0].ID != first[0].ID || again[0].PID != 81 {
		t.Fatalf("resume %+v %v", again, err)
	}
	if again[0].Branch != first[0].Branch {
		t.Fatalf("branch changed %s -> %s", first[0].Branch, again[0].Branch)
	}
}

func TestHostStartFailureResumesOnNewRunner(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 0, errors.New("host down") }
	if err := r.Tick(ctx); err == nil {
		t.Fatal("host failure did not surface")
	}
	first, err := db.ListAttempts(ctx, "c")
	if err != nil || len(first) != 1 || first[0].PID != 0 || !first[0].EndedAt.IsZero() {
		t.Fatalf("partial %+v %v", first, err)
	}
	got, err := db.GetCard(ctx, "c")
	if err != nil || got.Status != model.CardRunning {
		t.Fatalf("card %+v %v", got, err)
	}
	next := New(home)
	next.Launch = func(context.Context, string, string) (int, error) { return 82, nil }
	if err := next.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := db.ListAttempts(ctx, "c")
	if err != nil || len(again) != 1 || again[0].ID != first[0].ID || again[0].PID != 82 {
		t.Fatalf("restart %+v %v", again, err)
	}
	if err := next.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	quiet, err := db.ListAttempts(ctx, "c")
	if err != nil || len(quiet) != 1 {
		t.Fatalf("extra attempt %+v %v", quiet, err)
	}
}

func TestNotifyUsesObjectiveOriginAndReminds(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	obj := model.Objective{
		ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, Origin: "codex:thread-9", IntegrationBranch: "main",
	}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	q := model.Question{
		ID: "q1", Objective: "o", Scope: "card:c1", Kind: model.QuestionCustom, Text: "decide",
		Deliveries: []model.Delivery{{At: time.Now().UTC(), Adapter: "file", Destination: "q1"}},
	}
	if err := db.CreateQuestion(ctx, &q); err != nil {
		t.Fatal(err)
	}
	var calls []notify.Message
	r := New(home)
	r.Notify = func(_ context.Context, msg notify.Message) error {
		calls = append(calls, msg)
		return nil
	}
	if err := r.Tick(ctx); err != nil || r.Tick(ctx) != nil {
		t.Fatal("tick")
	}
	if len(calls) != 2 || calls[0].Origin != "codex:thread-9" || calls[1].Origin != "codex:thread-9" {
		t.Fatalf("calls %+v", calls)
	}
	if calls[0].Origin == q.Scope {
		t.Fatal("origin used the question scope")
	}
	got, err := db.GetQuestion(ctx, "q1")
	if err != nil || len(got.Deliveries) != 1 {
		t.Fatalf("deliveries %+v %v", got, err)
	}
}

func plantWorktree(t *testing.T, repo string, change bool) string {
	t.Helper()
	if change {
		path := filepath.Join(repo, "changed.txt")
		if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "changed.txt")
		git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "change")
	}
	return repo
}

func branchExists(repo, name string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Dir = repo
	return cmd.Run() == nil
}
