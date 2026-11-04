package sched

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"cardex/internal/harvest"
	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/prompt"
	"cardex/internal/quota"
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

func TestQuotaWindowsKeepTheirOwnRatio(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	for _, windows := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d", windows), func(t *testing.T) {
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			wins := make([]model.QuotaWindow, windows)
			names := []string{"5h", "daily"}
			for i := range wins {
				wins[i] = model.QuotaWindow{
					Name: names[i], ResetRule: "rolling", ResetAt: now.Add(30 * time.Minute),
					LimitTokens: 100, UsedTokens: 40, Remaining: 0.6, Confidence: 1,
				}
			}
			ch := model.Channel{
				ID: "ch", Runner: model.RunnerFake, Family: "alpha", Models: []string{"model-a"},
				Concurrency: 2, Health: model.HealthOK, Tier: model.TierT2, Redline: 0.2,
				QuotaWindows: wins,
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
			card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			prev := chooseRoute
			t.Cleanup(func() { chooseRoute = prev })
			chooseRoute = func(ctx context.Context, req route.Request) (model.Route, model.RouteReason, error) {
				if req.Quota == nil {
					t.Fatal("nil quota")
				}
				est, err := req.Quota.Remaining(ctx, "ch")
				if err != nil || est.Unknown || est.LimitTokens <= 0 {
					t.Fatalf("estimate %+v %v", est, err)
				}
				ratio := float64(est.LimitTokens-est.UsedTokens) / float64(est.LimitTokens)
				if ratio < 0.5 {
					t.Fatalf("windows summed into ratio %v (used %d limit %d)", ratio, est.UsedTokens, est.LimitTokens)
				}
				return prev(ctx, req)
			}
			r := New(home)
			r.Now = func() time.Time { return now }
			r.Launch = func(context.Context, string, string) (int, error) { return 63, nil }
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			attempts, err := db.ListAttempts(ctx, "c")
			if err != nil || len(attempts) != 1 || attempts[0].PID != 63 {
				t.Fatalf("attempts %+v %v", attempts, err)
			}
		})
	}
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
			wins := []model.QuotaWindow{
				windowWithRemaining("5h", now.Add(30*time.Minute), tc.remain),
			}
			if tc.mask {
				wins = append(wins, windowWithRemaining("daily", now.Add(40*time.Minute), 0.1))
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
				held := exec.Command("sleep", "60")
				held.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := held.Start(); err != nil {
					t.Fatal(err)
				}
				heldPID := held.Process.Pid
				t.Cleanup(func() {
					_ = host.SignalGroup(heldPID, syscall.SIGKILL)
					_ = host.SignalProcess(heldPID, syscall.SIGKILL)
					_ = held.Wait()
				})
				live := model.Attempt{ID: "a-hold", Card: "hold", PID: heldPID, Route: model.Route{Channel: "ch", Model: "model-a"}, StartedAt: now}
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
				ended := time.Now().UTC()
				exit := 0
				live := model.Attempt{
					ID: "a-back", Card: "back", PID: 9, Route: model.Route{Channel: "cha"},
					StartedAt: ended.Add(-time.Minute), EndedAt: ended, Exit: &exit,
					Outcome: model.OutcomePassed,
				}
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

func TestBacklogFailureResumesSameAttempt(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	cases := []struct {
		name   string
		prompt bool
	}{
		{name: "prompt", prompt: true},
		{name: "host", prompt: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			channelNear(t, db, "ch", now.Add(30*time.Minute), 0.9, 0)
			card := model.Card{
				ID: "back", Objective: obj, Kind: model.KindBacklog, Title: "back",
				Tier: model.TierT1, Status: model.CardReady,
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			n := 0
			r := New(home)
			r.Now = func() time.Time { return now }
			if tc.prompt {
				r.Prompt = func(context.Context, prompt.Input) (string, error) {
					n++
					if n == 1 {
						return "", errors.New("prompt down")
					}
					return "PROMPT-OK", nil
				}
				r.Launch = func(context.Context, string, string) (int, error) { return 83, nil }
			} else {
				r.Launch = func(context.Context, string, string) (int, error) {
					n++
					if n == 1 {
						return 0, errors.New("host down")
					}
					return 83, nil
				}
			}
			if err := r.Tick(ctx); err == nil {
				t.Fatal("first tick swallowed the failure")
			}
			first, err := db.ListAttempts(ctx, "back")
			if err != nil || len(first) != 1 || first[0].PID != 0 || !first[0].EndedAt.IsZero() {
				t.Fatalf("partial %+v %v", first, err)
			}
			got, err := db.GetCard(ctx, "back")
			if err != nil || got.Status != model.CardRunning {
				t.Fatalf("card %+v %v", got, err)
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			again, err := db.ListAttempts(ctx, "back")
			if err != nil || len(again) != 1 || again[0].ID != first[0].ID || again[0].PID != 83 {
				t.Fatalf("resume %+v %v", again, err)
			}
			if again[0].Branch != first[0].Branch || !branchExists(repo, again[0].Branch) {
				t.Fatalf("branch %s", again[0].Branch)
			}
		})
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
	if err != nil {
		t.Fatal(err)
	}
	consumed := false
	for _, attempt := range quiet {
		if attempt.ID == first[0].ID && attempt.Outcome == model.OutcomeNoOutput {
			consumed = true
		}
	}
	if !consumed || len(quiet) > 2 {
		t.Fatalf("harvest %+v", quiet)
	}
}

func TestNotifyUsesObjectiveOriginAndReminds(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	if err := db.CreateProject(ctx, &model.Project{ID: "p"}); err != nil {
		t.Fatal(err)
	}
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

func TestHarvestStaysPendingUntilDisposition(t *testing.T) {
	cases := []struct {
		name    string
		missing bool
		fail    bool
		want    model.Outcome
	}{
		{name: "success", want: model.OutcomePassed},
		{name: "failure", fail: true, want: model.OutcomeNoOutput},
		{name: "missing worktree", missing: true, want: model.OutcomeNoOutput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			card := model.Card{
				ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2,
				Status: model.CardRunning,
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			wt := plantWorktree(t, repo, !tc.fail && !tc.missing)
			if tc.missing {
				wt = filepath.Join(t.TempDir(), "gone")
			}
			exit := 0
			base := rev(t, repo, "HEAD")
			if !tc.missing && !tc.fail {
				base = rev(t, repo, "HEAD~1")
			}
			attempt := model.Attempt{
				ID: "a-c", Card: "c", Worktree: wt, BaseCommit: base,
				Exit: &exit, EndedAt: time.Now().UTC(),
			}
			if err := db.CreateAttempt(ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			pending := func() {
				t.Helper()
				got, err := db.GetAttempt(ctx, "a-c")
				if err != nil {
					t.Fatal(err)
				}
				if got.Outcome != "" {
					t.Fatalf("consumed outcome %s", got.Outcome)
				}
				card, err := db.GetCard(ctx, "c")
				if err != nil || card.Status != model.CardRunning {
					t.Fatalf("card %+v %v", card, err)
				}
			}
			bare := New(home)
			if bare.Dispose == nil {
				t.Fatal("production runner has no disposition")
			}
			bare.Dispose = nil
			if err := bare.Tick(ctx); err == nil {
				t.Fatal("nil disposition stored the harvest")
			}
			pending()
			hook := New(home)
			hook.Dispose = func(context.Context, *model.Card, harvest.Result) error {
				return errors.New("dispose down")
			}
			if err := hook.Tick(ctx); err == nil {
				t.Fatal("dispose error was stored")
			}
			pending()
			var calls int
			var got harvest.Result
			hook = New(home)
			hook.Dispose = func(_ context.Context, seen *model.Card, res harvest.Result) error {
				calls++
				got = res
				if seen.Status != model.CardRunning {
					t.Fatalf("status %s before disposition returned", seen.Status)
				}
				return nil
			}
			if err := hook.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.Outcome != tc.want {
				t.Fatalf("calls %d outcome %s", calls, got.Outcome)
			}
			stored, err := db.GetAttempt(ctx, "a-c")
			if err != nil || stored.Outcome != tc.want {
				t.Fatalf("stored %+v %v", stored, err)
			}
			if err := hook.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("delivered again, calls %d", calls)
			}
		})
	}
}

func serveOnce(t *testing.T, home string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, home, "127.0.0.1:0")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		body, _ := os.ReadFile(filepath.Join(home, "serve-loop.log"))
		if strings.Contains(string(body), "iteration") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("serve did not tick: %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return")
	}
}

func TestQuotaEstimateDrivesDispatchAndFill(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	soon := base.Add(30 * time.Minute)
	hit := base.Add(10 * time.Minute)
	cases := []struct {
		name     string
		now      time.Time
		windows  []model.QuotaWindow
		redline  float64
		spent    []spentAttempt
		dispatch bool
		fill     bool
		check    func(quota.Estimate) error
	}{
		{
			name: "finished attempt usage",
			now:  base,
			windows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100,
			}},
			redline: 0.2,
			spent:   []spentAttempt{{tokens: 80, ended: base.Add(-time.Minute)}},
			check: func(est quota.Estimate) error {
				if est.Unknown || est.UsedTokens < 80 || est.Ratio > 0.2 {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "usage before reset",
			now:  base,
			windows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100,
			}},
			redline:  0.2,
			spent:    []spentAttempt{{tokens: 90, ended: base.Add(-6 * time.Hour)}},
			dispatch: true,
			fill:     true,
			check: func(est quota.Estimate) error {
				if est.Unknown || est.UsedTokens != 0 || est.Ratio <= 0.5 {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "request limit",
			now:  base,
			windows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: soon,
				LimitTokens: 1000, LimitRequests: 1,
			}},
			redline: 0.2,
			spent:   []spentAttempt{{tokens: 1, ended: base.Add(-time.Minute)}},
			check: func(est quota.Estimate) error {
				if est.Unknown || est.LimitRequests != 1 || est.UsedRequests < 1 || est.LimitTokens != 1000 {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "quota hit before reset",
			now:  base,
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 1000},
				{Name: "daily", ResetRule: "rolling", ResetAt: base.Add(3 * time.Hour), LimitTokens: 1000},
			},
			redline: 0.2,
			spent:   []spentAttempt{{tokens: 1, ended: base.Add(-time.Minute), hit: hit}},
			check: func(est quota.Estimate) error {
				if !est.Blocked || !est.Reset.Equal(hit.UTC()) {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "quota hit after reset",
			now:  hit.Add(time.Minute),
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 1000},
				{Name: "daily", ResetRule: "rolling", ResetAt: base.Add(3 * time.Hour), LimitTokens: 1000},
			},
			redline:  0.2,
			spent:    []spentAttempt{{tokens: 1, ended: base.Add(-time.Minute), hit: hit}},
			dispatch: true,
			fill:     true,
			check: func(est quota.Estimate) error {
				if est.Blocked || est.Unknown || est.Ratio <= 0.5 {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "name only zero remaining",
			now:  base,
			windows: []model.QuotaWindow{{
				Name: "daily", ResetRule: "rolling", ResetAt: soon, Remaining: 0,
			}},
			redline:  0.2,
			dispatch: true,
			check: func(est quota.Estimate) error {
				if !est.Unknown || est.LimitTokens != 0 || est.LimitRequests != 0 || est.Blocked {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "snapshot ample attempt at redline",
			now:  base,
			windows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: soon,
				LimitTokens: 100, UsedTokens: 10, Remaining: 0.9,
			}},
			redline: 0.2,
			spent:   []spentAttempt{{tokens: 70, ended: base.Add(-time.Minute)}},
			check: func(est quota.Estimate) error {
				if est.Unknown || est.LimitTokens != 100 || est.UsedTokens < 80 || est.Ratio > 0.2 {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "positive remaining without a limit",
			now:  base,
			windows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: soon, Remaining: 0.9,
			}},
			dispatch: true,
			check: func(est quota.Estimate) error {
				if !est.Unknown || est.LimitTokens != 0 || est.LimitRequests != 0 {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"dispatch", "fill"} {
				t.Run(mode, func(t *testing.T) {
					est, saw, started := runQuotaMode(t, tc.now, tc.windows, tc.redline, tc.spent, mode)
					want := tc.dispatch
					if mode == "fill" {
						want = tc.fill
					}
					if started != want {
						t.Fatalf("started %v want %v estimate %+v", started, want, est)
					}
					if !saw {
						if mode == "dispatch" || want {
							t.Fatal("route did not see the quota")
						}
						return
					}
					if err := tc.check(est); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

type spentAttempt struct {
	id       string
	tokens   int64
	ended    time.Time
	hit      time.Time
	excerpt  string
	log      string
	quotaHit bool
}

func runQuotaMode(t *testing.T, now time.Time, windows []model.QuotaWindow, redline float64, spent []spentAttempt, mode string) (quota.Estimate, bool, bool) {
	t.Helper()
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	ch := model.Channel{
		ID: "ch", Runner: model.RunnerFake, Family: "alpha", Models: []string{"model-a"},
		Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2, Redline: redline,
		QuotaWindows: windows,
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
	if len(spent) > 0 {
		done := model.Card{
			ID: "spent", Objective: obj, Kind: model.KindDev, Title: "spent",
			Tier: model.TierT2, Status: model.CardDone,
		}
		if err := db.CreateCard(ctx, &done); err != nil {
			t.Fatal(err)
		}
		exit := 0
		for i, item := range spent {
			id := item.id
			if id == "" {
				id = fmt.Sprintf("spent-%d", i)
			}
			att := model.Attempt{
				ID: id, Card: "spent",
				Route:     model.Route{Channel: "ch", Model: "model-a"},
				StartedAt: item.ended.Add(-time.Minute), EndedAt: item.ended, Exit: &exit,
				Usage:   model.Usage{Tokens: item.tokens, Source: "event", Confidence: 1},
				Outcome: model.OutcomePassed,
			}
			if !item.hit.IsZero() || item.quotaHit {
				att.Outcome = model.OutcomeQuotaHit
				att.ErrorClass = model.ErrorQuotaHit
				switch {
				case item.excerpt != "":
					att.ErrorExcerpt = item.excerpt
				case !item.hit.IsZero():
					att.ErrorExcerpt = "quota hit, reset at " + item.hit.UTC().Format(time.RFC3339)
				}
			}
			if err := db.CreateAttempt(ctx, &att); err != nil {
				t.Fatal(err)
			}
			if item.log != "" {
				dir := filepath.Join(home, "attempts", id)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "output.log"), []byte(item.log), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	cardID := "c"
	card := model.Card{
		ID: cardID, Objective: obj, Kind: model.KindDev, Title: "c",
		Tier: model.TierT2, Status: model.CardReady,
	}
	if mode == "fill" {
		cardID = "back"
		card = model.Card{
			ID: cardID, Objective: obj, Kind: model.KindBacklog, Title: "back",
			Tier: model.TierT1, Status: model.CardReady,
		}
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	prev := chooseRoute
	defer func() { chooseRoute = prev }()
	var est quota.Estimate
	saw := false
	chooseRoute = func(ctx context.Context, req route.Request) (model.Route, model.RouteReason, error) {
		got, err := req.Quota.Remaining(ctx, "ch")
		if err != nil {
			t.Errorf("remaining %v", err)
		}
		est = got
		saw = true
		return prev(ctx, req)
	}
	r := New(home)
	r.Now = func() time.Time { return now }
	r.Launch = func(context.Context, string, string) (int, error) { return 91, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListAttempts(ctx, cardID)
	if err != nil {
		t.Fatal(err)
	}
	return est, saw, len(attempts) > 0
}

func TestBacklogFillStoredRatioWithoutCap(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	soon := now.Add(30 * time.Minute)
	far := now.Add(5 * time.Hour)
	uncapped := model.QuotaWindow{
		Name: "5h", ResetRule: "rolling", ResetAt: soon, Remaining: 0.9, Confidence: 1,
	}
	capped := model.QuotaWindow{
		Name: "5h", ResetRule: "rolling", ResetAt: soon,
		LimitTokens: 100, UsedTokens: 10, Remaining: 0.9, Confidence: 1,
	}
	cases := []struct {
		name     string
		windows  []model.QuotaWindow
		redline  float64
		readyDev bool
		want     int
		again    bool
	}{
		{
			name: "stored ratio near reset without cap", windows: []model.QuotaWindow{uncapped},
		},
		{
			name: "neither cap nor remaining ratio",
			windows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: soon,
			}},
		},
		{
			name: "capped ratio not above redline plus margin",
			windows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: soon,
				LimitTokens: 100, UsedTokens: 80, Remaining: 0.2, Confidence: 1,
			}},
			redline: 0.2,
		},
		{
			name: "capped reset outside horizon",
			windows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: far,
				LimitTokens: 100, UsedTokens: 10, Remaining: 0.9, Confidence: 1,
			}},
		},
		{
			name: "ready card needs channel", windows: []model.QuotaWindow{capped}, readyDev: true,
		},
		{
			name: "uncapped near reset beside capped far window",
			windows: []model.QuotaWindow{
				{Name: "daily", ResetRule: "rolling", ResetAt: far, LimitTokens: 100, UsedTokens: 10, Remaining: 0.9, Confidence: 1},
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, Remaining: 0.9, Confidence: 1},
			},
		},
		{
			name: "capped near reset above margin", windows: []model.QuotaWindow{capped},
			want: 1, again: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			ch := model.Channel{
				ID: "ch", Runner: model.RunnerFake, Family: "alpha", Models: []string{"model-a"},
				Concurrency: 2, Health: model.HealthOK, Tier: model.TierT1, Redline: tc.redline,
				QuotaWindows: tc.windows,
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
			if tc.readyDev {
				dev := model.Card{
					ID: "dev", Objective: obj, Kind: model.KindDev, Title: "dev",
					Tier: model.TierT2, Status: model.CardReady,
				}
				if err := db.CreateCard(ctx, &dev); err != nil {
					t.Fatal(err)
				}
			}
			r := New(home)
			r.Now = func() time.Time { return now }
			r.Launch = holdLaunch(t)
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
			if len(attempts) != tc.want {
				t.Fatalf("attempts %d want %d card %+v attempts %+v", len(attempts), tc.want, got, attempts)
			}
			t.Logf("attempts=%d", len(attempts))
			if tc.want == 0 {
				if got.Status != model.CardReady {
					t.Fatalf("backlog moved %+v", got)
				}
				return
			}
			if got.Status != model.CardRunning || attempts[0].Route.Channel != ch.ID || !host.LiveHost(attempts[0]) {
				t.Fatalf("backlog %+v attempts %+v", got, attempts)
			}
			if err := host.SignalProcess(attempts[0].PID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
				t.Fatalf("attempt host not alive: %v", err)
			}
			if !tc.again {
				return
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			again, err := db.ListAttempts(ctx, "back")
			if err != nil || len(again) != 1 || again[0].ID != attempts[0].ID {
				t.Fatalf("second tick %+v %v", again, err)
			}
			t.Logf("second attempts=%d", len(again))
		})
	}
}

func holdLaunch(t *testing.T) func(context.Context, string, string) (int, error) {
	t.Helper()
	var pids []int
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = host.SignalGroup(pid, syscall.SIGKILL)
			_ = host.SignalProcess(pid, syscall.SIGKILL)
		}
	})
	return func(context.Context, string, string) (int, error) {
		cmd := exec.Command("sleep", "60")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		pids = append(pids, cmd.Process.Pid)
		return cmd.Process.Pid, nil
	}
}

func TestDispatchChecksAssociationEdgeAndStopState(t *testing.T) {
	cases := []struct {
		name   string
		kind   model.CardKind
		status model.CardStatus
		reason string
		deps   string
		open   bool
		start  bool
	}{
		{name: "review association", kind: model.KindReview, status: model.CardReady, deps: "edge", start: true},
		{name: "repair association", kind: model.KindRepair, status: model.CardReady, deps: "edge", start: true},
		{name: "rebase association", kind: model.KindRebase, status: model.CardReady, deps: "edge", start: true},
		{name: "normalize association", kind: model.KindNormalize, status: model.CardReady, deps: "edge", start: true},
		{name: "normalize plus skeleton", kind: model.KindNormalize, status: model.CardReady, deps: "skel"},
		{name: "normalize plus merged skeleton", kind: model.KindNormalize, status: model.CardReady, deps: "done", start: true},
		{name: "normalize paused", kind: model.KindNormalize, status: model.CardPaused, deps: "edge"},
		{name: "review plus skeleton", kind: model.KindReview, status: model.CardReady, deps: "skel"},
		{name: "repair plus skeleton", kind: model.KindRepair, status: model.CardReady, deps: "skel"},
		{name: "rebase plus skeleton", kind: model.KindRebase, status: model.CardReady, deps: "skel"},
		{name: "review plus merged skeleton", kind: model.KindReview, status: model.CardReady, deps: "done", start: true},
		{name: "repair plus done skeleton", kind: model.KindRepair, status: model.CardReady, deps: "done", start: true},
		{name: "rebase plus canceled skeleton", kind: model.KindRebase, status: model.CardReady, deps: "canceled", start: true},
		{name: "paused", status: model.CardPaused, open: true},
		{name: "canceled", status: model.CardCanceled, open: true},
		{name: "failed", status: model.CardFailed, open: true},
		{name: "merged", status: model.CardMerged, open: true},
		{name: "done", status: model.CardDone, open: true},
		{name: "needs input", status: model.CardNeedsInput, open: true},
		{name: "harvesting", status: model.CardHarvesting, open: true},
		{name: "reviewing", status: model.CardReviewing, open: true},
		{name: "waiting", status: model.CardWaiting, open: true},
		{name: "ready", status: model.CardReady, start: true},
		{name: "interrupted running", status: model.CardRunning, open: true, start: true},
		{name: "claim wait", status: model.CardWaiting, reason: "f.go Foo 占用", start: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			seedRoutes(t, db)
			kind := tc.kind
			if kind == "" {
				kind = model.KindDev
			}
			var deps []string
			if tc.deps != "" {
				parent := model.Card{
					ID: "dep", Objective: obj, Kind: model.KindDev, Title: "dep",
					Tier: model.TierT2, Status: model.CardWaiting,
				}
				if err := db.CreateCard(ctx, &parent); err != nil {
					t.Fatal(err)
				}
				branch(t, repo, obj, "dep", "dep.txt", "parent\n")
				deps = append(deps, "dep")
				skelStatus := model.CardReady
				switch tc.deps {
				case "done":
					skelStatus = model.CardMerged
				case "canceled":
					skelStatus = model.CardCanceled
				}
				if tc.deps != "edge" {
					skel := model.Card{
						ID: "skel", Objective: obj, Kind: model.KindSkeleton, Title: "skel",
						Tier: model.TierT3, Status: skelStatus,
					}
					if err := db.CreateCard(ctx, &skel); err != nil {
						t.Fatal(err)
					}
					deps = append(deps, "skel")
				}
			}
			card := model.Card{
				ID: "c", Objective: obj, Kind: kind, Title: "c", Tier: model.TierT2,
				Status: tc.status, WaitReason: tc.reason, Deps: deps,
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			if tc.open {
				open := model.Attempt{
					ID: "a-c", Card: "c", PID: 0, Worktree: repo,
					Branch: "cardex/" + obj + "/c/c", StartedAt: time.Now().UTC(),
				}
				if err := db.CreateAttempt(ctx, &open); err != nil {
					t.Fatal(err)
				}
			}
			launches := 0
			r := New(home)
			r.Launch = func(context.Context, string, string) (int, error) {
				launches++
				return 93, nil
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			attempts, err := db.ListAttempts(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			got, err := db.GetCard(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			if !tc.start {
				if len(attempts) != boolCount(tc.open) {
					t.Fatalf("attempts %+v", attempts)
				}
				if got.Status != tc.status {
					t.Fatalf("status %s", got.Status)
				}
				if tc.open && (attempts[0].PID != 0 || launches != 0) {
					t.Fatalf("resumed pid %d launches %d", attempts[0].PID, launches)
				}
				return
			}
			if launches != 1 || len(attempts) != 1 || attempts[0].PID != 93 {
				t.Fatalf("launches %d attempts %+v", launches, attempts)
			}
			if tc.open && attempts[0].ID != "a-c" {
				t.Fatalf("new attempt %+v", attempts[0])
			}
			if tc.status == model.CardRunning && got.Status != model.CardRunning {
				t.Fatalf("status %s", got.Status)
			}
		})
	}
}

func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestWorkspaceExistsBeforeAttemptResumes(t *testing.T) {
	ctx := context.Background()
	t.Run("attempts directory fails after the branch exists", func(t *testing.T) {
		home, db, repo := newHome(t)
		_, obj := seedRepo(t, db, repo, "true")
		seedRoutes(t, db)
		card := model.Card{
			ID: "c", Objective: obj, Kind: model.KindDev, Title: "c",
			Tier: model.TierT2, Status: model.CardReady,
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "attempts"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := New(home)
		r.Launch = func(context.Context, string, string) (int, error) { return 94, nil }
		if err := r.Tick(ctx); err == nil {
			t.Fatal("tick succeeded before the attempt row existed")
		}
		first, err := db.ListAttempts(ctx, "c")
		if err != nil || len(first) != 0 {
			t.Fatalf("attempt stored early %+v %v", first, err)
		}
		name := "cardex/" + obj + "/c/c"
		if !branchExists(repo, name) {
			t.Fatal("branch was not created")
		}
		if err := os.Remove(filepath.Join(home, "attempts")); err != nil {
			t.Fatal(err)
		}
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		again, err := db.ListAttempts(ctx, "c")
		if err != nil || len(again) != 1 || again[0].Branch != name || again[0].PID != 94 {
			t.Fatalf("resume %+v %v", again, err)
		}
		if !branchExists(repo, name) {
			t.Fatal("branch lost")
		}
	})
	t.Run("pre-existing branch and worktree", func(t *testing.T) {
		home, db, repo := newHome(t)
		_, obj := seedRepo(t, db, repo, "true")
		seedRoutes(t, db)
		branch(t, repo, obj, "c", "keep.txt", "kept\n")
		wt := filepath.Join(t.TempDir(), "existing")
		git(t, repo, "worktree", "add", wt, "cardex/"+obj+"/c/c")
		card := model.Card{
			ID: "c", Objective: obj, Kind: model.KindDev, Title: "c",
			Tier: model.TierT2, Status: model.CardReady,
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		r := New(home)
		r.Launch = func(context.Context, string, string) (int, error) { return 95, nil }
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		attempts, err := db.ListAttempts(ctx, "c")
		if err != nil || len(attempts) != 1 || attempts[0].PID != 95 {
			t.Fatalf("attempts %+v %v", attempts, err)
		}
		name := "cardex/" + obj + "/c/c"
		if attempts[0].Branch != name {
			t.Fatalf("branch %s", attempts[0].Branch)
		}
		body, err := os.ReadFile(filepath.Join(attempts[0].Worktree, "keep.txt"))
		if err != nil || !strings.Contains(string(body), "kept") {
			t.Fatalf("worktree %q %v", body, err)
		}
	})
}

func TestProductionHarvestLeavesDispositionPending(t *testing.T) {
	cli, err := os.ReadFile(filepath.Join("..", "..", "cmd", "cardex", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cli), "loop := sched.New(home)") {
		t.Fatal("runServe does not tick sched.New")
	}
	serveSrc, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serveSrc), "runner := New(home)") {
		t.Fatal("Serve does not tick New")
	}
	if strings.Contains(string(cli), ".Dispose") || strings.Contains(string(serveSrc), ".Dispose") {
		t.Fatal("Serve or cmd assigns Dispose")
	}
	if New(t.TempDir()).Dispose == nil {
		t.Fatal("production runner left Dispose nil")
	}
	t.Log("cmd constructs the runner with loop := sched.New(home) and does not assign Dispose")
	t.Log("Serve constructs the runner with runner := New(home) and does not assign Dispose")
}

func TestWindowSnapshotKeepsItsOwnUsage(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	soon := now.Add(30 * time.Minute)
	cases := []struct {
		name    string
		windows []model.QuotaWindow
		spent   []spentAttempt
		check   func(quota.Estimate) error
	}{
		{
			name: "zero-used 5h does not take daily 400",
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 0},
				{Name: "daily", ResetRule: "rolling", ResetAt: soon, LimitTokens: 1000, UsedTokens: 400},
			},
			check: func(est quota.Estimate) error {
				if est.Unknown || est.Blocked || est.LimitTokens != 1000 || est.UsedTokens != 400 {
					return fmt.Errorf("estimate %+v", est)
				}
				ratio := float64(est.LimitTokens-est.UsedTokens) / float64(est.LimitTokens)
				if ratio < 0.55 || ratio > 0.65 {
					return fmt.Errorf("ratio %v estimate %+v", ratio, est)
				}
				return nil
			},
		},
		{
			name: "fresh 5h does not take daily 400",
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: now.Add(-time.Hour), LimitTokens: 100, UsedTokens: 0},
				{Name: "daily", ResetRule: "rolling", ResetAt: soon, LimitTokens: 1000, UsedTokens: 400},
			},
			check: func(est quota.Estimate) error {
				if est.Unknown || est.Blocked || est.LimitTokens != 1000 || est.UsedTokens != 400 {
					return fmt.Errorf("estimate %+v", est)
				}
				ratio := float64(est.LimitTokens-est.UsedTokens) / float64(est.LimitTokens)
				if ratio < 0.55 || ratio > 0.65 {
					return fmt.Errorf("ratio %v estimate %+v", ratio, est)
				}
				return nil
			},
		},
		{
			name: "5h 40/100 and daily 400/1000",
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 40, Remaining: 0.6, Confidence: 1},
				{Name: "daily", ResetRule: "rolling", ResetAt: soon, LimitTokens: 1000, UsedTokens: 400, Remaining: 0.6, Confidence: 1},
			},
			check: func(est quota.Estimate) error {
				if est.Unknown || est.Blocked || est.LimitTokens != 100 || est.UsedTokens != 40 {
					return fmt.Errorf("estimate %+v", est)
				}
				ratio := float64(est.LimitTokens-est.UsedTokens) / float64(est.LimitTokens)
				if ratio < 0.55 || ratio > 0.65 {
					return fmt.Errorf("ratio %v estimate %+v", ratio, est)
				}
				return nil
			},
		},
		{
			name: "daily keeps 400 against its own limit",
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 10},
				{Name: "daily", ResetRule: "rolling", ResetAt: soon.Add(time.Hour), LimitTokens: 1000, UsedTokens: 400},
			},
			check: func(est quota.Estimate) error {
				if est.Unknown || est.Blocked || est.LimitTokens != 1000 || est.UsedTokens != 400 {
					return fmt.Errorf("estimate %+v", est)
				}
				ratio := float64(est.LimitTokens-est.UsedTokens) / float64(est.LimitTokens)
				if ratio < 0.55 || ratio > 0.65 {
					return fmt.Errorf("ratio %v estimate %+v", ratio, est)
				}
				return nil
			},
		},
		{
			name: "finished attempt counted once",
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 40},
				{Name: "daily", ResetRule: "rolling", ResetAt: soon, LimitTokens: 1000, UsedTokens: 400},
			},
			spent: []spentAttempt{{tokens: 15, ended: now.Add(-time.Minute)}},
			check: func(est quota.Estimate) error {
				if est.Unknown || est.Blocked || est.LimitTokens != 100 || est.UsedTokens != 55 {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"dispatch", "fill"} {
				t.Run(mode, func(t *testing.T) {
					est, saw, started := runQuotaMode(t, now, tc.windows, 0.2, tc.spent, mode)
					if !started {
						t.Fatalf("started false estimate %+v", est)
					}
					if !saw {
						t.Fatal("route did not see the quota")
					}
					if err := tc.check(est); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestQuotaHitBlockFollowsResetNotOrder(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	soon := now.Add(30 * time.Minute)
	future := now.Add(2 * time.Hour)
	pastReset := now.Add(-time.Hour)
	eventAt := now.Add(-3 * time.Hour)
	loose := now.Add(4 * time.Hour)
	windows := []model.QuotaWindow{{
		Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 1000,
	}}
	event := eventAt.UTC().Format(time.RFC3339)
	resetText := future.UTC().Format(time.RFC3339)
	both := "event at " + event + " quota hit, reset at " + resetText
	cases := []struct {
		name  string
		spent []spentAttempt
		start bool
		check func(quota.Estimate) error
	}{
		{
			name: "a-new future reset outlives z-old",
			spent: []spentAttempt{
				{id: "a-new", tokens: 1, ended: now.Add(-2 * time.Minute), quotaHit: true, excerpt: "quota hit, reset at " + resetText},
				{id: "z-old", tokens: 1, ended: now.Add(-time.Minute), quotaHit: true, excerpt: "quota hit, reset at " + pastReset.UTC().Format(time.RFC3339)},
			},
			check: func(est quota.Estimate) error {
				if !est.Blocked || !est.Reset.Equal(future) {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "excerpt uses the reset time",
			spent: []spentAttempt{{
				id: "a-excerpt", tokens: 1, ended: now.Add(-time.Minute), quotaHit: true, excerpt: both,
			}},
			check: func(est quota.Estimate) error {
				if !est.Blocked || !est.Reset.Equal(future) || est.Reset.Equal(eventAt) {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "output log uses the reset time",
			spent: []spentAttempt{{
				id: "a-log", tokens: 1, ended: now.Add(-time.Minute), quotaHit: true,
				excerpt: "event at " + loose.UTC().Format(time.RFC3339),
				log:     both,
			}},
			check: func(est quota.Estimate) error {
				if !est.Blocked || !est.Reset.Equal(future) {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
		{
			name: "unrelated timestamp does not block",
			spent: []spentAttempt{{
				id: "a-event", tokens: 1, ended: now.Add(-time.Minute), quotaHit: true,
				excerpt: "event at " + loose.UTC().Format(time.RFC3339) + " finished",
			}},
			start: true,
			check: func(est quota.Estimate) error {
				if est.Blocked || est.Reset.Equal(loose) {
					return fmt.Errorf("estimate %+v", est)
				}
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"dispatch", "fill"} {
				t.Run(mode, func(t *testing.T) {
					est, saw, started := runQuotaMode(t, now, windows, 0.2, tc.spent, mode)
					if started != tc.start {
						t.Fatalf("started %v want %v estimate %+v", started, tc.start, est)
					}
					if !saw {
						if mode == "dispatch" || tc.start {
							t.Fatal("route did not see the quota")
						}
						return
					}
					if err := tc.check(est); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestSnapshotOverlapIsNotOmission(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	soon := now.Add(30 * time.Minute)
	cases := []struct {
		name      string
		windows   []model.QuotaWindow
		confident bool
		used      int64
	}{
		{
			name: "5h and daily 50 covered by one event",
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 50},
				{Name: "daily", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 50},
			},
			confident: true,
			used:      50,
		},
		{
			name: "5h used more than daily can absorb",
			windows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 80},
				{Name: "daily", ResetRule: "rolling", ResetAt: soon, LimitTokens: 100, UsedTokens: 50},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"dispatch", "fill"} {
				t.Run(mode, func(t *testing.T) {
					est, saw, started := runQuotaMode(t, now, tc.windows, 0.2, nil, mode)
					if !saw {
						t.Fatalf("route did not see the quota estimate %+v started %v", est, started)
					}
					if tc.confident {
						if est.Unknown || est.Confidence != 1 || est.UsedTokens != tc.used || est.LimitTokens != 100 {
							t.Fatalf("estimate %+v", est)
						}
						return
					}
					if est.Confidence >= 1 {
						t.Fatalf("unplaceable snapshot stayed confident %+v", est)
					}
				})
			}
		})
	}
}

func TestSchedulerPromptFacts(t *testing.T) {
	question := model.OpenQuestion{
		ID: "Q1", Text: "事实正文不重复", Type: model.OQFact, Status: model.OQAnswered,
		Answer: "没有上限", Evidence: "internal/sched/loop.go", Confidence: "高",
	}
	summary := "## 事实摘要\n事实正文不重复\n"
	cases := []struct {
		name      string
		kind      model.CardKind
		summary   bool
		questions []model.OpenQuestion
		facts     bool
	}{
		{name: "design summary only", kind: model.KindDesign, summary: true},
		{name: "plan summary only", kind: model.KindPlan, summary: true},
		{name: "replan summary only", kind: model.KindReplan, summary: true},
		{name: "design node facts", kind: model.KindDesign, summary: true, questions: []model.OpenQuestion{question}, facts: true},
		{name: "plan node facts", kind: model.KindPlan, questions: []model.OpenQuestion{question}, facts: true},
		{name: "replan node facts", kind: model.KindReplan, summary: true, questions: []model.OpenQuestion{question}, facts: true},
		{name: "dev ignores facts", kind: model.KindDev, summary: true, questions: []model.OpenQuestion{question}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			seedRoutes(t, db)
			node := model.DesignNode{
				ID: "n-facts", Objective: obj, Layer: model.LayerModule, Status: model.NodeDrafting,
				OpenQuestions: tc.questions,
			}
			if err := db.CreateNode(ctx, &node); err != nil {
				t.Fatal(err)
			}
			body := "实现这张卡\n"
			if tc.summary {
				body += summary
			}
			card := model.Card{
				ID: "c", Objective: obj, Node: node.ID, Kind: tc.kind, Title: tc.name,
				Tier: model.TierT3, Status: model.CardReady, Body: body,
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			r.Launch = func(context.Context, string, string) (int, error) { return 97, nil }
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			text := attemptPrompt(t, home, db, "c")
			stored, err := db.GetCard(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			if tc.summary && !strings.Contains(stored.Body, "## 事实摘要") {
				t.Fatal("stored card lost its fact summary")
			}
			empty := strings.Contains(text, "<!-- cardex-facts -->") &&
				!strings.Contains(text, "问题编号：")
			if empty {
				t.Fatalf("empty facts section\n%s", text)
			}
			if !tc.facts && strings.Contains(text, "<!-- cardex-facts -->") {
				t.Fatalf("facts section\n%s", text)
			}
			if tc.facts {
				if !strings.Contains(text, "<!-- cardex-facts -->") || !strings.Contains(text, "问题：事实正文不重复") {
					t.Fatalf("missing node facts\n%s", text)
				}
				if strings.Contains(text, "## 事实摘要") {
					t.Fatalf("fact summary duplicated\n%s", text)
				}
				if strings.Count(text, "事实正文不重复") != 1 {
					t.Fatalf("fact text count\n%s", text)
				}
			}
			if tc.summary && !tc.facts {
				if !strings.Contains(text, "## 事实摘要") || strings.Count(text, "事实正文不重复") != 1 {
					t.Fatalf("card summary missing\n%s", text)
				}
			}
		})
	}
}
