package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"cardex/internal/harvest"
	"cardex/internal/host"
	"cardex/internal/merge"
	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/prompt"
	"cardex/internal/store"
)

func TestTickDispatchClaimsAndSecondTick(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	proj, obj := seedRepo(t, db, repo, "true")
	_ = proj
	seedRoutes(t, db)
	hold := `{"attempts":{"0":{"hold":"3s","write_files":[{"path":"note.txt","content":"x\n"}]}}}`
	mk := func(id, symbol string) {
		t.Helper()
		card := model.Card{
			ID: id, Objective: obj, Kind: model.KindDev, Title: id, Body: hold,
			Tier: model.TierT2, Status: model.CardReady,
			Claims:     []model.Claim{{Path: "f.go", Symbol: symbol}},
			Acceptance: []string{"true"},
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
	}
	mk("c1", "Foo")
	mk("c2", "Bar")
	mk("c3", "Foo")
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) { return "PROMPT-OK", nil }
	trackLaunch(t, r)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts %+v", attempts)
	}
	running := 0
	for _, attempt := range attempts {
		if attempt.PID <= 0 || !processAlive(attempt.PID) {
			t.Fatalf("attempt not live %+v", attempt)
		}
		card, err := db.GetCard(ctx, attempt.Card)
		if err != nil || card.Status != model.CardRunning {
			t.Fatalf("card %+v %v", card, err)
		}
		body, err := os.ReadFile(attempt.PromptPath)
		if err != nil || !strings.Contains(string(body), "PROMPT-OK") {
			t.Fatalf("prompt %s %v", body, err)
		}
		running++
	}
	if running != 2 {
		t.Fatalf("running %d", running)
	}
	waiting, err := db.GetCard(ctx, "c3")
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != model.CardWaiting || !strings.Contains(waiting.WaitReason, "c1") || !strings.Contains(waiting.WaitReason, "Foo") {
		t.Fatalf("wait %+v", waiting)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := db.ListAttempts(ctx, "")
	if err != nil || len(again) != 2 {
		t.Fatalf("second tick attempts %+v %v", again, err)
	}
}

func TestTickManualMergesWithoutAutoCards(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, manual := seedRepo(t, db, repo, "true")
	auto := model.Objective{
		ID: "auto", Project: "p", Title: "auto", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateObjective(ctx, &auto); err != nil {
		t.Fatal(err)
	}
	seedRoutes(t, db)
	script := `{"attempts":{"0":{"write_files":[{"path":"note.txt","content":"shipped\n"}]}}}`
	card := model.Card{
		ID: "dev", Objective: manual, Kind: model.KindDev, Title: "dev", Body: script,
		Tier: model.TierT2, Status: model.CardReady, Acceptance: []string{"true"},
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	called := map[string]int{}
	r := New(home)
	r.Dispose = passDisposition(db)
	r.Engine = func(_ context.Context, id string) error {
		called[id]++
		extra := model.Card{
			ID: "design-" + id, Objective: id, Kind: model.KindDesign, Title: "design",
			Tier: model.TierT3, Status: model.CardNeedsInput,
		}
		if _, err := db.GetCard(context.Background(), extra.ID); err == nil {
			return nil
		}
		return db.CreateCard(context.Background(), &extra)
	}
	var head string
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if err := r.Tick(ctx); err != nil {
			t.Fatal(readLog(home, err))
		}
		got, err := db.GetCard(ctx, "dev")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == model.CardMerged {
			out, err := exec.Command("git", "-C", repo, "rev-parse", "main").Output()
			if err != nil {
				t.Fatal(err)
			}
			head = strings.TrimSpace(string(out))
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if head == "" {
		dumpCards(t, db)
		t.Fatal("manual card did not merge")
	}
	if called["auto"] == 0 || called[manual] != 0 {
		t.Fatalf("engine calls %+v", called)
	}
	cards, err := db.ListCards(ctx, manual)
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		switch card.Kind {
		case model.KindDesign, model.KindPlan, model.KindReplan:
			t.Fatalf("manual grew %+v", card)
		}
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "main").Output()
	if err != nil || strings.TrimSpace(string(out)) != head {
		t.Fatalf("second tick moved %s -> %s", head, out)
	}
	show, err := exec.Command("git", "-C", repo, "show", "main:note.txt").Output()
	if err != nil || !strings.Contains(string(show), "shipped") {
		t.Fatalf("note %q %v", show, err)
	}
}

func TestClsWorkHarvestedOnManualAndAuto(t *testing.T) {
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
			script := `{"attempts":{"0":{"write_files":[{"path":"note.txt","content":"shipped\n"}]}}}`
			card := model.Card{
				ID: "cls-work", Objective: objID, Kind: model.KindDev, Title: "cls-work", Body: script,
				Tier: model.TierT2, Status: model.CardReady, Acceptance: []string{"true"},
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			called := map[string]int{}
			r := New(home)
			r.Dispose = passDisposition(db)
			r.Engine = func(_ context.Context, id string) error {
				called[id]++
				return nil
			}
			deadline := time.Now().Add(25 * time.Second)
			for time.Now().Before(deadline) {
				if err := r.Tick(ctx); err != nil {
					t.Fatal(readLog(home, err))
				}
				got, err := db.GetCard(ctx, "cls-work")
				if err != nil {
					t.Fatal(err)
				}
				if got.Status == model.CardMerged {
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			got, err := db.GetCard(ctx, "cls-work")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == model.CardRunning || got.Status != model.CardMerged {
				dumpCards(t, db)
				t.Fatalf("cls-work status %s", got.Status)
			}
			attempts, err := db.ListAttempts(ctx, "cls-work")
			if err != nil || len(attempts) == 0 {
				t.Fatalf("attempts %+v %v", attempts, err)
			}
			if attempts[0].Outcome == "" || attempts[0].Outcome == model.OutcomeNoOutput {
				t.Fatalf("cls-work outcome %q", attempts[0].Outcome)
			}
			review, err := db.GetCard(ctx, "rv-cls-work")
			if err != nil || review.Kind != model.KindReview {
				t.Fatalf("review %+v %v", review, err)
			}
			show, err := exec.Command("git", "-C", repo, "show", "main:note.txt").Output()
			if err != nil || !strings.Contains(string(show), "shipped") {
				t.Fatalf("note %q %v", show, err)
			}
			cards, err := db.ListCards(ctx, objID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == model.ModeManual {
				if called[objID] != 0 {
					t.Fatalf("manual engine calls %d", called[objID])
				}
				for _, item := range cards {
					switch item.Kind {
					case model.KindDesign, model.KindPlan, model.KindReplan:
						t.Fatalf("manual grew %+v", item)
					}
				}
			} else if called[objID] == 0 {
				t.Fatal("auto did not advance")
			}
		})
	}
}

func TestTickFill(t *testing.T) {
	now := time.Now().UTC()
	t.Run("true", func(t *testing.T) {
		ctx := context.Background()
		home, db, repo := newHome(t)
		_, obj := seedRepo(t, db, repo, "true")
		ch := channelNear(t, db, "ch", now.Add(30*time.Minute), 0.9, 0)
		card := model.Card{
			ID: "back", Objective: obj, Kind: model.KindBacklog, Title: "back",
			Body: `{"attempts":{"0":{"write_files":[{"path":"b.txt","content":"b\n"}]}}}`,
			Tier: model.TierT1, Status: model.CardReady,
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		r := New(home)
		r.Now = func() time.Time { return now }
		trackLaunch(t, r)
		if err := r.Tick(ctx); err != nil {
			t.Fatal(readLog(home, err))
		}
		got, err := db.GetCard(ctx, "back")
		if err != nil || got.Status != model.CardRunning {
			t.Fatalf("backlog %+v %v", got, err)
		}
		attempts, err := db.ListAttempts(ctx, "back")
		if err != nil || len(attempts) != 1 || attempts[0].Route.Channel != ch {
			t.Fatalf("attempt %+v %v", attempts, err)
		}
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		again, _ := db.ListAttempts(ctx, "back")
		if len(again) != 1 {
			t.Fatalf("second fill %+v", again)
		}
	})
	t.Run("reset too far", func(t *testing.T) {
		assertNoFill(t, now, now.Add(5*time.Hour), 0.9, 0, false)
	})
	t.Run("below margin", func(t *testing.T) {
		assertNoFill(t, now, now.Add(30*time.Minute), 0.2, 0.1, false)
	})
	t.Run("ready card needs channel", func(t *testing.T) {
		assertNoFill(t, now, now.Add(30*time.Minute), 0.9, 0, true)
	})
}

func assertNoFill(t *testing.T, now, reset time.Time, remaining, redline float64, withReady bool) {
	t.Helper()
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	channelNear(t, db, "ch", reset, remaining, redline)
	back := model.Card{ID: "back", Objective: obj, Kind: model.KindBacklog, Title: "back", Tier: model.TierT1, Status: model.CardReady}
	if err := db.CreateCard(ctx, &back); err != nil {
		t.Fatal(err)
	}
	if withReady {
		dev := model.Card{ID: "dev", Objective: obj, Kind: model.KindDev, Title: "dev", Tier: model.TierT2, Status: model.CardReady}
		if err := db.CreateCard(ctx, &dev); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Now = func() time.Time { return now }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCard(ctx, "back")
	if err != nil || got.Status != model.CardReady {
		t.Fatalf("backlog moved %+v %v", got, err)
	}
	attempts, err := db.ListAttempts(ctx, "back")
	if err != nil || len(attempts) != 0 {
		t.Fatalf("filled anyway %+v", attempts)
	}
}

func TestTickSchedules(t *testing.T) {
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
	now := time.Date(2026, 1, 1, 15, 30, 0, 0, time.UTC)
	cfg := `{
	  "unknown": true,
	  "schedules": [
	    {"name":"hourly","every":"1h","card_template":{"kind":"ranking","title":"Hourly","body":"rank"}},
	    {"name":"later","every":"1h","card_template":{"kind":"ranking","title":"Later"}},
	    {"name":"morning","cron":"30 15 1 1 *","card_template":{"kind":"ranking","title":"Morning"}},
	    {"name":"night","cron-lite":"0 0 1 1 *","card_template":{"kind":"ranking","title":"Night"}}
	  ]
	}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	slot := strconvSlot(now.Unix() / int64(time.Hour/time.Second))
	if err := os.MkdirAll(filepath.Join(home, "schedules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scheduleStamp(home, "later"), []byte(slot), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Now = func() time.Time { return now }
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
	if _, ok := got["Hourly"]; !ok {
		t.Fatalf("missing hourly %+v", cards)
	}
	if _, ok := got["Morning"]; !ok {
		t.Fatalf("missing cron %+v", cards)
	}
	if _, ok := got["Later"]; ok {
		t.Fatal("not-due every enqueued")
	}
	if _, ok := got["Night"]; ok {
		t.Fatal("not-due cron enqueued")
	}
	if got["Hourly"].Kind != model.KindRanking || got["Hourly"].Status != model.CardReady {
		t.Fatalf("template %+v", got["Hourly"])
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := db.ListCards(ctx, "o")
	if err != nil || len(again) != len(cards) {
		t.Fatalf("second tick cards %d -> %+v", len(cards), again)
	}
}

func TestTickNotifyOnce(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	q := model.Question{ID: "q1", Objective: "o", Kind: model.QuestionCustom, Text: "decide"}
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
	if len(calls) != 2 || calls[0].QuestionID != "q1" || calls[0].Text != "decide" || calls[1].QuestionID != "q1" {
		t.Fatalf("calls %+v", calls)
	}
	got, err := db.GetQuestion(ctx, "q1")
	if err != nil || len(got.Deliveries) != 1 {
		t.Fatalf("delivery %+v %v", got, err)
	}
}

func TestTickMergeConflictAndRed(t *testing.T) {
	ctx := context.Background()
	t.Run("conflict", func(t *testing.T) {
		home, db, repo := newHome(t)
		_, obj := seedRepo(t, db, repo, "true")
		branch(t, repo, obj, "c1", "a.txt", "left\n")
		branch(t, repo, obj, "c2", "a.txt", "right\n")
		queue(t, db, obj, "c1")
		queue(t, db, obj, "c2")
		r := New(home)
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		head := rev(t, repo, "main")
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if rev(t, repo, "main") != head {
			t.Fatal("conflict moved integration HEAD")
		}
		follow, err := db.GetCard(ctx, "rb-c2")
		if err != nil {
			t.Fatal(err)
		}
		if follow.Kind != model.KindRebase || !strings.Contains(follow.Body, "a.txt") || !strings.Contains(follow.Body, "<<<<<<<") {
			t.Fatalf("rebase %+v", follow)
		}
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if rev(t, repo, "main") != head {
			t.Fatal("third tick moved HEAD")
		}
	})
	t.Run("red", func(t *testing.T) {
		home, db, repo := newHome(t)
		_, obj := seedRepo(t, db, repo, "echo RED_TEST; exit 1")
		before := rev(t, repo, "main")
		branch(t, repo, obj, "c1", "a.txt", "changed\n")
		queue(t, db, obj, "c1")
		r := New(home)
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if rev(t, repo, "main") != before {
			t.Fatal("red test moved HEAD")
		}
		repair, err := db.GetCard(ctx, "rp-c1")
		if err != nil || !strings.Contains(repair.Body, "RED_TEST") || repair.Kind != model.KindRepair {
			t.Fatalf("repair %+v %v", repair, err)
		}
	})
}

func TestTickAndServeTakeOverHost(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	proj := model.Project{ID: "p", RepoPath: t.TempDir(), TestCmd: "true", DesignDir: "docs"}
	obj := model.Objective{
		ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateProject(ctx, &proj); err != nil || db.CreateObjective(ctx, &obj) != nil {
		t.Fatal("seed")
	}
	card := model.Card{ID: "c", Objective: "o", Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardRunning}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = host.SignalGroup(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	attempt := model.Attempt{ID: "a-live", Card: "c", PID: cmd.Process.Pid, StartedAt: time.Now().UTC()}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAttempt(ctx, "a-live")
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != cmd.Process.Pid || !got.EndedAt.IsZero() || got.Exit != nil {
		t.Fatalf("live attempt changed %+v", got)
	}
	if eventsOf(t, db, "a-live", "host_lost") {
		t.Fatal("live host recorded host_lost")
	}
	if !processAlive(cmd.Process.Pid) {
		t.Fatal("tick killed the host")
	}

	serveCtx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(serveCtx, home, "127.0.0.1:0") }()
	waitIterations(t, home, 2)
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("serve did not return")
	}
	got, err = db.GetAttempt(ctx, "a-live")
	if err != nil || got.PID != cmd.Process.Pid || !got.EndedAt.IsZero() {
		t.Fatalf("serve changed live attempt %+v %v", got, err)
	}
	if !processAlive(cmd.Process.Pid) || eventsOf(t, db, "a-live", "host_lost") {
		t.Fatal("serve killed or lost the host")
	}

	_ = host.SignalGroup(cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetAttempt(ctx, "a-live")
	if err != nil || got.EndedAt.IsZero() || got.PID != cmd.Process.Pid {
		t.Fatalf("dead host %+v %v", got, err)
	}
	if !eventsOf(t, db, "a-live", "host_lost") {
		t.Fatal("missing host_lost")
	}
}

func TestTickAutoCallsRealEngine(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	obj := model.Objective{
		ID: "auto", Project: "p", Title: "auto", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	proj := model.Project{ID: "p", RepoPath: t.TempDir(), TestCmd: "true", DesignDir: "docs"}
	if err := db.CreateProject(ctx, &proj); err != nil || db.CreateObjective(ctx, &obj) != nil {
		t.Fatal(err)
	}
	manual := obj
	manual.ID = "manual"
	manual.Mode = model.ModeManual
	if err := db.CreateObjective(ctx, &manual); err != nil {
		t.Fatal(err)
	}
	decoy := t.TempDir()
	t.Setenv("CARDEX_HOME", decoy)
	runner := New(home)
	if runner.Engine == nil {
		t.Fatal("New left Engine nil")
	}
	if err := runner.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CARDEX_HOME") != decoy {
		t.Fatalf("CARDEX_HOME leaked: %s", os.Getenv("CARDEX_HOME"))
	}
	cards, err := db.ListCards(ctx, "auto")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, card := range cards {
		if card.Kind == model.KindResearch && strings.Contains(card.Title, "盘点") {
			found = true
		}
	}
	if !found {
		t.Fatalf("auto tick did not run design, cards %+v", cards)
	}
	manualCards, err := db.ListCards(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(manualCards) != 0 {
		t.Fatalf("manual objective entered design: %+v", manualCards)
	}
}

func newHome(t *testing.T) (string, *store.Store, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return home, db, repo
}

func seedRepo(t *testing.T, db *store.Store, repo, testCmd string) (string, string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	proj := model.Project{
		ID: "p", RepoPath: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), "wt"),
		TestCmd: testCmd, DesignDir: "docs/design/",
	}
	obj := model.Objective{
		ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	ctx := context.Background()
	if err := db.CreateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	return proj.ID, obj.ID
}

func seedRoutes(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, spec := range []struct {
		id, family, modelName string
	}{
		{"cha", "alpha", "model-a"},
		{"chb", "beta", "model-b"},
	} {
		ch := model.Channel{
			ID: spec.id, Runner: model.RunnerFake, Family: spec.family, SourceGroup: spec.family,
			Models: []string{spec.modelName}, Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2,
		}
		if err := db.CreateChannel(ctx, &ch); err != nil {
			t.Fatal(err)
		}
		pos := model.Positioning{
			ID: "pos-" + spec.id, Model: spec.modelName, Family: spec.family,
			Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 1,
		}
		if err := db.CreatePositioning(ctx, &pos); err != nil {
			t.Fatal(err)
		}
	}
}

func channelNear(t *testing.T, db *store.Store, id string, reset time.Time, remaining, redline float64) string {
	t.Helper()
	ch := model.Channel{
		ID: id, Runner: model.RunnerFake, Family: "alpha", Models: []string{"model-a"},
		Concurrency: 2, Health: model.HealthOK, Tier: model.TierT1, Redline: redline,
		QuotaWindows: []model.QuotaWindow{
			windowWithRemaining("5h", reset, remaining),
		},
	}
	if err := db.CreateChannel(context.Background(), &ch); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{
		ID: "pos-" + ch.ID, Model: "model-a", Family: "alpha",
		Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 1,
	}
	if err := db.CreatePositioning(context.Background(), &pos); err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

func windowWithRemaining(name string, reset time.Time, remaining float64) model.QuotaWindow {
	const limit int64 = 100
	used := int64((1 - remaining) * float64(limit))
	if used < 0 {
		used = 0
	}
	if used > limit {
		used = limit
	}
	return model.QuotaWindow{
		Name: name, ResetRule: "rolling", ResetAt: reset,
		LimitTokens: limit, UsedTokens: used, Remaining: remaining, Confidence: 1,
	}
}

func branch(t *testing.T, repo, obj, id, file, body string) {
	t.Helper()
	name := "cardex/" + obj + "/c/" + id
	git(t, repo, "checkout", "-b", name)
	if err := os.WriteFile(filepath.Join(repo, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", file)
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", id)
	git(t, repo, "checkout", "main")
}

func queue(t *testing.T, db *store.Store, obj, id string) {
	t.Helper()
	card := model.Card{
		ID: id, Objective: obj, Kind: model.KindDev, Title: id, Tier: model.TierT2, Status: model.CardMergeQueued,
	}
	if err := db.CreateCard(context.Background(), &card); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func rev(t *testing.T, repo, name string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", name)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := host.SignalProcess(pid, 0)
	return err == nil || err == syscall.EPERM
}

var (
	launchedMu   sync.Mutex
	launchedByID = map[string]int{}
)

func rememberLaunch(id string, pid int) {
	if pid <= 1 || id == "" {
		return
	}
	launchedMu.Lock()
	launchedByID[id] = pid
	launchedMu.Unlock()
}

func knownLaunch(id string, pid int) bool {
	launchedMu.Lock()
	got, ok := launchedByID[id]
	launchedMu.Unlock()
	return ok && got == pid && pid > 1
}

func forgetLaunch(id string) {
	launchedMu.Lock()
	delete(launchedByID, id)
	launchedMu.Unlock()
}

func executorPGID(home, id string) int {
	b, err := os.ReadFile(filepath.Join(home, "attempts", id, "executor.pgid"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 1 {
		return 0
	}
	return n
}

func signalStarted(home, id string, pid int) {
	if !knownLaunch(id, pid) {
		return
	}
	if pgid := executorPGID(home, id); pgid > 1 {
		_ = host.SignalGroup(pgid, syscall.SIGKILL)
	}
	_ = host.SignalGroup(pid, syscall.SIGKILL)
	_ = host.SignalProcess(pid, syscall.SIGKILL)
}

func killHost(home, id string, pid int) {
	signalStarted(home, id, pid)
}

func trackLaunch(t *testing.T, r *Runner) {
	t.Helper()
	inner := r.Launch
	var mu sync.Mutex
	type started struct {
		home, id string
		pid      int
	}
	var mine []started
	r.Launch = func(ctx context.Context, home, id string) (int, error) {
		var pid int
		var err error
		if inner != nil {
			pid, err = inner(ctx, home, id)
		} else {
			pid, err = launchDetached(home, id)
		}
		if err == nil && pid > 1 {
			rememberLaunch(id, pid)
			mu.Lock()
			mine = append(mine, started{home: home, id: id, pid: pid})
			mu.Unlock()
		}
		return pid, err
	}
	t.Cleanup(func() {
		mu.Lock()
		got := append([]started(nil), mine...)
		mu.Unlock()
		for _, item := range got {
			killHost(item.home, item.id, item.pid)
			forgetLaunch(item.id)
		}
	})
}

func eventsOf(t *testing.T, db *store.Store, attemptID, kind string) bool {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "attempt:"+attemptID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == kind {
			return true
		}
	}
	return false
}

func waitIterations(t *testing.T, home string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(home, "serve-loop.log"))
		if err == nil && strings.Count(string(b), "\n") >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile(filepath.Join(home, "serve-loop.log"))
	t.Fatalf("serve iterations %q", b)
}

func readLog(home string, err error) error {
	matches, _ := filepath.Glob(filepath.Join(home, "attempts", "*", "launcher.log"))
	var b strings.Builder
	b.WriteString(err.Error())
	for _, path := range matches {
		body, _ := os.ReadFile(path)
		b.WriteString("\n")
		b.WriteString(path)
		b.WriteString("\n")
		b.Write(body)
	}
	return &logErr{b.String()}
}

type logErr struct{ s string }

func (e *logErr) Error() string { return e.s }

const reviewPassScript = `{
  "attempts": {
    "0": {
      "write_files": [
        {"path": ".cardex-result.md", "content": "## 复核结论\npass\n"}
      ],
      "exit_code": 0
    }
  }
}`

// passDisposition is the test stand-in for the missing loop disposition entry.
func passDisposition(db *store.Store) func(context.Context, *model.Card, harvest.Result) error {
	return func(ctx context.Context, card *model.Card, res harvest.Result) error {
		if card.Kind == model.KindReview {
			subjectID := ""
			if len(card.Deps) > 0 {
				subjectID = card.Deps[0]
			}
			if res.Outcome == model.OutcomePassed && subjectID != "" {
				switch reviewWord(res.ResultText) {
				case "rework":
					subject, err := db.GetCard(ctx, subjectID)
					if err != nil {
						return err
					}
					subject.Status = model.CardReady
					subject.ReworkCount++
					subject.ReworkNote = strings.TrimSpace(res.ResultText)
					if err := db.UpdateCard(ctx, &subject); err != nil {
						return err
					}
				case "pass":
					if err := merge.New(db).Enqueue(ctx, subjectID); err != nil {
						return err
					}
				default:
					card.Status = model.CardNeedsInput
					return nil
				}
				card.Status = model.CardDone
				return nil
			}
			card.Status = model.CardNeedsInput
			return nil
		}
		if res.Outcome == model.OutcomePassed && mergesKind(card.Kind) {
			id := "rv-" + card.ID
			existing, err := db.GetCard(ctx, id)
			switch {
			case err == nil && reviewFinished(existing.Status):
				return nil
			case err == nil:
			case errors.Is(err, sql.ErrNoRows):
				review := model.Card{
					ID: id, Objective: card.Objective, Node: card.Node, Kind: model.KindReview,
					Title: "review " + card.Title, Body: reviewPassScript, Tier: model.TierT3,
					Status: model.CardReady, Deps: []string{card.ID}, AuthorFamily: card.AuthorFamily,
				}
				if err := db.CreateCard(ctx, &review); err != nil {
					return err
				}
			default:
				return err
			}
			card.Status = model.CardReviewing
			return nil
		}
		if res.Outcome == model.OutcomePassed {
			card.Status = model.CardDone
			return nil
		}
		if res.Outcome == model.OutcomeTestsFailed {
			card.Status = model.CardReady
			card.ReworkCount++
			card.ReworkNote = res.Excerpt
			return nil
		}
		card.Status = model.CardNeedsInput
		if card.WaitReason == "" {
			card.WaitReason = string(res.Outcome)
		}
		return nil
	}
}

func reviewFinished(status model.CardStatus) bool {
	switch status {
	case model.CardDone, model.CardMerged, model.CardCanceled, model.CardFailed:
		return true
	default:
		return false
	}
}

func reviewWord(text string) string {
	for _, field := range strings.Fields(strings.ToLower(text)) {
		field = strings.Trim(field, "`*_.,;:()[]\"'")
		if field == "rework" {
			return "rework"
		}
		if field == "pass" {
			return "pass"
		}
	}
	return ""
}

func mergesKind(kind model.CardKind) bool {
	switch kind {
	case model.KindDev, model.KindRepair, model.KindRebase, model.KindSkeleton, model.KindBacklog:
		return true
	default:
		return false
	}
}

func dumpCards(t *testing.T, db *store.Store) {
	t.Helper()
	cards, err := db.ListCards(context.Background(), "")
	if err != nil {
		t.Log(err)
		return
	}
	raw, _ := json.MarshalIndent(cards, "", "  ")
	t.Log(string(raw))
}

var _ = host.LiveHost
