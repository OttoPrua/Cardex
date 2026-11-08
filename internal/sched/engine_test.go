package sched

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/store"
)

func TestDesignPhaseErrorStoresEventAndDisposes(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	git(t, wt, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	proj := model.Project{ID: "p", RepoPath: wt, TestCmd: "true", DesignDir: "docs"}
	if err := db.CreateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	obj := model.Objective{
		ID: "auto", Project: proj.ID, Title: "auto", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "inv", Objective: obj.ID, Kind: model.KindResearch, Title: "盘点",
		Body: "Q0\n", Tier: model.TierT1, Status: model.CardDone,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	// A terminal inventory card whose absorbed Q0 is not JSON makes designStep
	// fail inside inventoryQ0. That error does not name the open attempt.
	if err := db.AppendEvent(ctx, &model.Event{Entity: "card:inv", Type: "loop.absorbed", Data: []byte("{")}); err != nil {
		t.Fatal(err)
	}
	attempt := model.Attempt{
		ID: "a-open", Card: card.ID, Index: 0, Worktree: wt, StartedAt: time.Now().UTC(),
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	decoy := t.TempDir()
	t.Setenv("CARDEX_HOME", decoy)
	if err := New(home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CARDEX_HOME") != decoy {
		t.Fatalf("CARDEX_HOME leaked: %s", os.Getenv("CARDEX_HOME"))
	}
	events, err := db.ListEvents(ctx, "objective:auto")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range events {
		if event.Type == "design_error" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("design_error events %d %+v", n, events)
	}
	got, err := db.GetAttempt(ctx, "a-open")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "" || got.Exit != nil || !got.EndedAt.IsZero() {
		t.Fatalf("unnamed open attempt was disposed: outcome %q exit %v ended %v", got.Outcome, got.Exit, got.EndedAt)
	}
	if eventsOf(t, db, "a-open", "dev_disposed") {
		t.Fatal("unnamed open attempt took the §5 path")
	}
}

func TestDesignErrorLeavesRunningSibling(t *testing.T) {
	kinds := []model.CardKind{model.KindDesign, model.KindResearch, model.KindPlan, model.KindReplan, model.KindSpike}
	results := []struct {
		name string
		body string
	}{
		{name: "no-result", body: ""},
		{name: "partial-result", body: "## 结论\n还在写\n"},
	}
	for _, kind := range kinds {
		for _, result := range results {
			t.Run(string(kind)+"/"+result.name, func(t *testing.T) {
				ctx := context.Background()
				home, db, wt := designErrorRepo(t)
				seedInventoryFailure(t, db)
				card := model.Card{
					ID: "sib", Objective: "auto", Kind: kind, Title: "旁路",
					Body: "进行中", Tier: model.TierT1, Status: model.CardRunning,
				}
				if err := db.CreateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
				if result.body != "" {
					if err := os.WriteFile(filepath.Join(wt, ".cardex-result.md"), []byte(result.body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				hostPID := startSleepHost(t)
				attempt := model.Attempt{
					ID: "sib-" + string(kind), Card: card.ID, Index: 0, Worktree: wt,
					PID: hostPID, StartedAt: time.Now().UTC(),
				}
				if err := db.CreateAttempt(ctx, &attempt); err != nil {
					t.Fatal(err)
				}
				runner := New(home)
				for n := 0; n < 2; n++ {
					if err := runner.Tick(ctx); err != nil {
						t.Fatalf("tick %d: %v", n+1, err)
					}
				}
				events, err := db.ListEvents(ctx, "objective:auto")
				if err != nil {
					t.Fatal(err)
				}
				n := 0
				for _, event := range events {
					if event.Type == "design_error" {
						n++
					}
				}
				if n != 2 {
					t.Fatalf("design_error events %d, want 2", n)
				}
				got, err := db.GetAttempt(ctx, attempt.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Outcome != "" || got.Exit != nil || !got.EndedAt.IsZero() {
					t.Fatalf("running %s sibling outcome %q exit %v ended %v", kind, got.Outcome, got.Exit, got.EndedAt)
				}
				if eventsOf(t, db, attempt.ID, "dev_disposed") {
					t.Fatal("running sibling was disposed")
				}
				if !processAlive(hostPID) {
					t.Fatal("running sibling host is gone")
				}
			})
		}
	}
}

func TestDesignErrorDisposesNamedExit(t *testing.T) {
	ctx := context.Background()
	home, db, wt := designErrorRepo(t)
	obj := model.Objective{
		ID: "auto", Project: "p", Title: "auto", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "named", Objective: obj.ID, Kind: model.KindResearch, Title: "研究",
		Body: "notes", Tier: model.TierT1, Status: model.CardRunning,
	}
	other := model.Card{
		ID: "other", Objective: obj.ID, Kind: model.KindPlan, Title: "拆卡",
		Body: "notes", Tier: model.TierT3, Status: model.CardRunning,
	}
	devCard := model.Card{
		ID: "devcard", Objective: obj.ID, Kind: model.KindDev, Title: "实现",
		Body: "code", Tier: model.TierT2, Status: model.CardRunning,
	}
	for _, item := range []*model.Card{&card, &other, &devCard} {
		if err := db.CreateCard(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	code, code2, devCode := 0, 0, 0
	ended := time.Now().UTC()
	attempt := model.Attempt{
		ID: "att-named", Card: card.ID, Index: 0, Worktree: wt,
		StartedAt: ended.Add(-time.Minute), EndedAt: ended, Exit: &code,
	}
	second := model.Attempt{
		ID: "att-other", Card: other.ID, Index: 0, Worktree: wt,
		StartedAt: ended.Add(-time.Minute), EndedAt: ended, Exit: &code2,
	}
	devAttempt := model.Attempt{
		ID: "att-dev", Card: devCard.ID, Index: 0, Worktree: wt,
		StartedAt: ended.Add(-time.Minute), EndedAt: ended, Exit: &devCode,
	}
	livePID := startSleepHost(t)
	openAttempt := model.Attempt{
		ID: "att-live", Card: card.ID, Index: 1, Worktree: wt,
		PID: livePID, StartedAt: time.Now().UTC(),
	}
	for _, item := range []*model.Attempt{&attempt, &second, &devAttempt, &openAttempt} {
		if err := db.CreateAttempt(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	runner := New(home)
	if err := runner.noteDesignFailure(ctx, db, obj.ID, fmt.Errorf("design failed around %s-more", attempt.ID)); err != nil {
		t.Fatal(err)
	}
	if err := runner.noteDesignFailure(ctx, db, obj.ID, fmt.Errorf("attempts %s and %s plus %s and %s", second.ID, attempt.ID, devAttempt.ID, openAttempt.ID)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{attempt.ID, second.ID, devAttempt.ID, openAttempt.ID} {
		item, err := db.GetAttempt(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Outcome != "" || eventsOf(t, db, id, "dev_disposed") {
			t.Fatalf("attempt %s outcome %q was disposed before a single match", id, item.Outcome)
		}
	}
	if err := runner.noteDesignFailure(ctx, db, obj.ID, fmt.Errorf("design failed for attempt %s", attempt.ID)); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != model.OutcomeNoOutput {
		t.Fatalf("named exit outcome %q, want %s", got.Outcome, model.OutcomeNoOutput)
	}
	if !eventsOf(t, db, attempt.ID, "dev_disposed") {
		t.Fatal("named exit did not take the §5 path")
	}
	for _, id := range []string{second.ID, devAttempt.ID, openAttempt.ID} {
		item, err := db.GetAttempt(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Outcome != "" || eventsOf(t, db, id, "dev_disposed") {
			t.Fatalf("attempt %s outcome %q was disposed", id, item.Outcome)
		}
	}
	if err := runner.noteDesignFailure(ctx, db, obj.ID, fmt.Errorf("dev %s live %s", devAttempt.ID, openAttempt.ID)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{devAttempt.ID, openAttempt.ID} {
		item, err := db.GetAttempt(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Outcome != "" || eventsOf(t, db, id, "dev_disposed") {
			t.Fatalf("attempt %s outcome %q was disposed", id, item.Outcome)
		}
	}
	if !processAlive(livePID) {
		t.Fatal("named live host was signaled")
	}
}

func TestDesignErrorHarvestWritesEvidence(t *testing.T) {
	ctx := context.Background()
	home, db, wt := designErrorRepo(t)
	body := "## 事实\nkept result\n"
	if err := os.WriteFile(filepath.Join(wt, ".cardex-result.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	obj := model.Objective{
		ID: "auto", Project: "p", Title: "auto", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "named", Objective: obj.ID, Kind: model.KindResearch, Title: "研究",
		Body: "notes", Tier: model.TierT1, Status: model.CardRunning,
		Acceptance: []string{"false"},
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	code := 0
	ended := time.Now().UTC()
	attempt := model.Attempt{
		ID: "att-ev", Card: card.ID, Index: 0, Worktree: wt,
		StartedAt: ended.Add(-time.Minute), EndedAt: ended, Exit: &code,
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	runner := New(home)
	if err := runner.noteDesignFailure(ctx, db, obj.ID, fmt.Errorf("design failed for attempt %s", attempt.ID)); err != nil {
		t.Fatal(err)
	}
	acc := filepath.Join(home, "attempts", attempt.ID, "acceptance.log")
	if _, err := os.Stat(acc); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "attempts", attempt.ID, "result.md"))
	if err != nil || string(got) != body {
		t.Fatalf("result.md %q %v", got, err)
	}
}

func designErrorRepo(t *testing.T) (string, *store.Store, string) {
	t.Helper()
	home, db, repo := newHome(t)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	proj := model.Project{ID: "p", RepoPath: repo, TestCmd: "true", DesignDir: "docs"}
	if err := db.CreateProject(context.Background(), &proj); err != nil {
		t.Fatal(err)
	}
	return home, db, repo
}

func seedInventoryFailure(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	obj := model.Objective{
		ID: "auto", Project: "p", Title: "auto", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "inv", Objective: obj.ID, Kind: model.KindResearch, Title: "盘点",
		Body: "Q0\n", Tier: model.TierT1, Status: model.CardDone,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendEvent(ctx, &model.Event{Entity: "card:inv", Type: "loop.absorbed", Data: []byte("{")}); err != nil {
		t.Fatal(err)
	}
}

func startSleepHost(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "180")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process == nil || cmd.Process.Pid <= 1 {
			return
		}
		_ = host.SignalProcess(cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	if cmd.Process.Pid <= 1 {
		t.Fatalf("sleep pid %d", cmd.Process.Pid)
	}
	return cmd.Process.Pid
}
