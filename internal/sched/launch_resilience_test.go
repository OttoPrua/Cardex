package sched

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestLaunchResilienceNeitherPrimaryAndSibling(t *testing.T) {
	ctx := context.Background()
	home, db, goodRepo := newHome(t)
	writeEmptySchedules(t, home)
	_, goodObj := seedRepo(t, db, goodRepo, "true")
	seedRoutes(t, db)
	badRepo := filepath.Join(filepath.Dir(goodRepo), "side")
	if err := os.MkdirAll(badRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, badRepo, "init", "-b", "side")
	if err := os.WriteFile(filepath.Join(badRepo, "README"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, badRepo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "README")
	git(t, badRepo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "side")
	if err := db.CreateProject(ctx, &model.Project{
		ID: "badp", RepoPath: badRepo, WorktreeRoot: filepath.Join(filepath.Dir(badRepo), "badwt"),
		TestCmd: "true", DesignDir: "docs/design/",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "bado", Project: "badp", Title: "bad", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "github.com/OttoPrua/Cardex/bado/main",
	}); err != nil {
		t.Fatal(err)
	}
	good := model.Card{ID: "good", Objective: goodObj, Kind: model.KindDev, Title: "good", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	bad := model.Card{ID: "bad", Objective: "bado", Kind: model.KindDev, Title: "bad", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &good); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &bad); err != nil {
		t.Fatal(err)
	}
	var launched []string
	r := New(home)
	r.Launch = func(_ context.Context, _ string, id string) (int, error) {
		launched = append(launched, id)
		return 61, nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	gotBad, err := db.GetCard(ctx, "bad")
	if err != nil || gotBad.Status != model.CardWaiting || !strings.HasPrefix(gotBad.WaitReason, "backoff:") || gotBad.Channel != "cha" {
		t.Fatalf("bad card %+v %v", gotBad, err)
	}
	gotGood, err := db.GetCard(ctx, "good")
	if err != nil || gotGood.Status != model.CardRunning {
		t.Fatalf("good card %+v %v", gotGood, err)
	}
	attempts, err := db.ListAttempts(ctx, "bad")
	if err != nil || len(attempts) != 0 {
		t.Fatalf("bad attempts %+v %v", attempts, err)
	}
	if branchExists(badRepo, "github.com/OttoPrua/Cardex/bado/c/bad") || branchExists(badRepo, "github.com/OttoPrua/Cardex/bado/main") {
		t.Fatal("bad repo kept a cardex ref")
	}
	if entries, _ := os.ReadDir(filepath.Join(filepath.Dir(badRepo), "badwt")); len(entries) != 0 {
		t.Fatalf("worktree residue %+v", entries)
	}
	events, err := db.ListEvents(ctx, "card:bad")
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, ev := range events {
		if ev.Type != model.EventLaunchFailed {
			continue
		}
		var payload struct {
			Step  string `json:"step"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Step == "worktree" && strings.Contains(payload.Error, "no main or master") && !strings.Contains(payload.Error, "output.log") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("events %+v", events)
	}
	if len(launched) != 1 {
		t.Fatalf("launched %+v", launched)
	}
	qs, err := db.ListQuestions(ctx, "bado")
	if err != nil || len(qs) != 0 {
		t.Fatalf("question %+v %v", qs, err)
	}
}

func TestLaunchResilienceQuestionOnThirdFailure(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	r := New(home)
	r.Now = func() time.Time { return now }
	r.Prompt = func(context.Context, prompt.Input) (string, error) {
		return "", errors.New("prompt down")
	}
	r.Launch = func(context.Context, string, string) (int, error) { return 70, nil }
	for i, wait := range []time.Duration{0, 31 * time.Second, 61 * time.Second} {
		now = now.Add(wait)
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		qs, err := db.ListQuestions(ctx, obj)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && len(qs) != 0 {
			t.Fatalf("failure %d opened %+v", i+1, qs)
		}
		if i == 2 {
			if len(qs) != 1 || qs[0].Kind != model.QuestionCustom || qs[0].Answer != "" {
				t.Fatalf("question %+v", qs)
			}
			if !strings.Contains(qs[0].Text, "step: prompt") || !strings.Contains(qs[0].Text, "prompt down") {
				t.Fatalf("text %q", qs[0].Text)
			}
			got, err := db.GetCard(ctx, "c")
			if err != nil || got.Status == model.CardNeedsInput || got.Status != model.CardWaiting {
				t.Fatalf("card %+v %v", got, err)
			}
		}
	}
	now = now.Add(61 * time.Second)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	qs, err := db.ListQuestions(ctx, obj)
	if err != nil || len(qs) != 1 {
		t.Fatalf("questions %+v %v", qs, err)
	}
}

func TestLaunchResilienceSuccessResetsStreak(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	fail := true
	r := New(home)
	r.Now = func() time.Time { return now }
	r.Prompt = func(context.Context, prompt.Input) (string, error) {
		if fail {
			return "", errors.New("prompt down")
		}
		return "PROMPT-OK", nil
	}
	r.Launch = func(context.Context, string, string) (int, error) { return 64, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	fail = false
	now = now.Add(31 * time.Second)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCard(ctx, "c")
	if err != nil || got.Status != model.CardRunning {
		t.Fatalf("success %+v %v", got, err)
	}
	fail = true
	// The stub pid is not a live process. The next tick harvests it and the
	// card can be dispatched again. Two failures after that start stay under
	// the question threshold.
	now = now.Add(time.Second)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for _, wait := range []time.Duration{0, 31 * time.Second} {
		now = now.Add(wait)
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	qs, err := db.ListQuestions(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 0 {
		t.Fatalf("streak was not reset %+v", qs)
	}
}

func TestLaunchResilienceSummaryLimit(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("e", 3000) + "输出"
	r := New(home)
	r.Prompt = func(context.Context, prompt.Input) (string, error) {
		return "", errors.New(long)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := db.ListEvents(ctx, "card:c")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Error string `json:"error"`
	}
	found := false
	for _, ev := range events {
		if ev.Type != model.EventLaunchFailed {
			continue
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	if !found || len(payload.Error) > 2048 || len(payload.Error) == 0 {
		t.Fatalf("summary %d %q", len(payload.Error), payload.Error)
	}
	if strings.Contains(payload.Error, "output.log") || !strings.Contains(payload.Error, "e") {
		t.Fatalf("summary %q", payload.Error)
	}
}

func TestLaunchResilienceCommittedBranchKept(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	branch(t, repo, obj, "c", "keep.txt", "kept\n")
	name := "github.com/OttoPrua/Cardex/" + obj + "/c/c"
	before := rev(t, repo, name)
	card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) {
		return 0, errors.New("host down")
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if rev(t, repo, name) != before {
		t.Fatal("committed branch was recreated")
	}
	attempts, err := db.ListAttempts(ctx, "c")
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != model.OutcomeLaunchFailed {
		t.Fatalf("attempt %+v %v", attempts, err)
	}
	if _, err := os.Stat(attempts[0].Worktree); !os.IsNotExist(err) {
		t.Fatalf("failed launch left worktree %v", err)
	}
	got, err := db.GetCard(ctx, "c")
	if err != nil || got.Status != model.CardWaiting || got.Channel != "cha" {
		t.Fatalf("card %+v %v", got, err)
	}
}

func TestLaunchResilienceHostStartBackoff(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{ID: "c", Objective: obj, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	r := New(home)
	r.Now = func() time.Time { return now }
	r.Launch = func(context.Context, string, string) (int, error) {
		return 0, errors.New("host down")
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListAttempts(ctx, "c")
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != model.OutcomeLaunchFailed || attempts[0].ErrorClass != model.ErrorLaunchFailed || attempts[0].EndedAt.IsZero() {
		t.Fatalf("attempt %+v %v", attempts, err)
	}
	got, err := db.GetCard(ctx, "c")
	if err != nil || got.Status != model.CardWaiting || !strings.HasPrefix(got.WaitReason, "backoff:") || got.Channel != "cha" || got.Model != "model-a" {
		t.Fatalf("card %+v %v", got, err)
	}
	events, err := db.ListEvents(ctx, "card:c")
	if err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, ev := range events {
		if ev.Type != model.EventLaunchFailed {
			continue
		}
		var payload struct {
			Attempt string `json:"attempt"`
			Step    string `json:"step"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Step == "host" && payload.Attempt == attempts[0].ID {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("events %+v", events)
	}
}

func TestLaunchResilienceMergeIsolation(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, goodObj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	branch(t, repo, goodObj, "m", "feat.txt", "feat\n")
	queue(t, db, goodObj, "m")
	badRepo := filepath.Join(filepath.Dir(repo), "bad")
	if err := os.MkdirAll(badRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, badRepo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(badRepo, "README"), []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, badRepo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "README")
	git(t, badRepo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "bad")
	if err := db.CreateProject(ctx, &model.Project{
		ID: "badp", RepoPath: badRepo, WorktreeRoot: filepath.Join(filepath.Dir(badRepo), "wt"),
		TestCmd: "true", DesignDir: "docs/design/",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "bado", Project: "badp", Title: "bad", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "github.com/OttoPrua/Cardex/bado/main",
	}); err != nil {
		t.Fatal(err)
	}
	queue(t, db, "bado", "stuck")
	ready := model.Card{ID: "ready", Objective: goodObj, Kind: model.KindDev, Title: "ready", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &ready); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateQuestion(ctx, &model.Question{
		ID: "q-note", Objective: goodObj, Scope: "ready", ScopeKind: "card",
		Kind: model.QuestionCustom, Text: "ping", Options: []string{"approve", "reject"},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var launches int
	r := New(home)
	r.Now = func() time.Time { return now }
	r.Launch = func(context.Context, string, string) (int, error) {
		launches++
		return 71, nil
	}
	r.Notify = func(context.Context, notify.Message) error { return nil }
	for i := 0; i < 2; i++ {
		_ = r.Tick(ctx)
	}
	merged, err := db.GetCard(ctx, "m")
	if err != nil || merged.Status != model.CardMerged {
		t.Fatalf("merged %+v %v", merged, err)
	}
	stuck, err := db.GetCard(ctx, "stuck")
	if err != nil || stuck.Status != model.CardMergeQueued {
		t.Fatalf("stuck %+v %v", stuck, err)
	}
	got, err := db.GetCard(ctx, "ready")
	if err != nil || got.Status != model.CardRunning || launches < 1 {
		t.Fatalf("ready %+v launches %d %v", got, launches, err)
	}
	q, err := db.GetQuestion(ctx, "q-note")
	if err != nil || len(q.Deliveries) != 1 {
		t.Fatalf("delivery %+v %v", q, err)
	}
	n := 0
	events, err := db.ListEvents(ctx, "objective:bado")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != model.EventTickError {
			continue
		}
		n++
		var payload struct {
			Step      string `json:"step"`
			Objective string `json:"objective"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Step != "merge" || payload.Objective != "bado" || payload.Error == "" || strings.Contains(payload.Error, "output.log") {
			t.Fatalf("payload %+v", payload)
		}
	}
	if n != 1 {
		t.Fatalf("tick_error count %d %+v", n, events)
	}
	if branchExists(badRepo, "github.com/OttoPrua/Cardex/bado/main") {
		t.Fatal("merge failure created the integration ref")
	}
}

func TestLaunchResilienceServeStaysUp(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, goodObj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	branch(t, repo, goodObj, "m", "feat.txt", "feat\n")
	queue(t, db, goodObj, "m")
	badRepo := filepath.Join(filepath.Dir(repo), "bad")
	if err := os.MkdirAll(badRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, badRepo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(badRepo, "README"), []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, badRepo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "README")
	git(t, badRepo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "bad")
	if err := db.CreateProject(ctx, &model.Project{
		ID: "badp", RepoPath: badRepo, WorktreeRoot: filepath.Join(filepath.Dir(badRepo), "wt"),
		TestCmd: "true", DesignDir: "docs/design/",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "bado", Project: "badp", Title: "bad", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "github.com/OttoPrua/Cardex/bado/main",
	}); err != nil {
		t.Fatal(err)
	}
	queue(t, db, "bado", "stuck")
	ready := model.Card{ID: "ready", Objective: goodObj, Kind: model.KindDev, Title: "ready", Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &ready); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopHomeHosts(home, db) })
	serveCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(serveCtx, home, "127.0.0.1:0")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		body, _ := os.ReadFile(filepath.Join(home, "serve-loop.log"))
		if strings.Count(string(body), "iteration ") >= 2 {
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("serve exited: %v\n%s", err, body)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("serve log %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
	merged, err := db.GetCard(ctx, "m")
	if err != nil || merged.Status != model.CardMerged {
		t.Fatalf("merged %+v %v", merged, err)
	}
	stuck, err := db.GetCard(ctx, "stuck")
	if err != nil || stuck.Status != model.CardMergeQueued {
		t.Fatalf("stuck %+v %v", stuck, err)
	}
	events, err := db.ListEvents(ctx, "objective:bado")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == model.EventTickError {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("tick_error count %d", n)
	}
}

func TestLaunchResilienceStartup(t *testing.T) {
	t.Run("database", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, "cardex.db"), 0o755); err != nil {
			t.Fatal(err)
		}
		stderr := captureStderr(t)
		err := Serve(context.Background(), home, "127.0.0.1:0")
		text := stderr()
		if err == nil || !strings.Contains(text, "serve:") || !strings.Contains(err.Error(), "database") {
			t.Fatalf("err %v stderr %q", err, text)
		}
	})
	t.Run("config", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		stderr := captureStderr(t)
		err := Serve(context.Background(), home, "127.0.0.1:0")
		text := stderr()
		if err == nil || !strings.Contains(text, "cannot parse") {
			t.Fatalf("err %v stderr %q", err, text)
		}
	})
	t.Run("missing config", func(t *testing.T) {
		home := t.TempDir()
		stderr := captureStderr(t)
		serveCtx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- Serve(serveCtx, home, "127.0.0.1:0")
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			body, _ := os.ReadFile(filepath.Join(home, "serve-loop.log"))
			if strings.Contains(string(body), "iteration ") {
				break
			}
			select {
			case err := <-errCh:
				t.Fatalf("serve exited: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("missing config did not loop")
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("serve did not return")
		}
		if text := stderr(); strings.Contains(text, "cannot parse") || strings.Contains(text, "database:") {
			t.Fatalf("stderr %q", text)
		}
	})
}

func writeEmptySchedules(t *testing.T, home string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{\"schedules\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stopHomeHosts(home string, db *store.Store) {
	attempts, err := db.ListAttempts(context.Background(), "")
	if err != nil {
		return
	}
	for _, attempt := range attempts {
		if attempt.PID > 1 {
			_ = host.SignalProcess(attempt.PID, syscall.SIGKILL)
		}
		if pgid := executorPGID(home, attempt.ID); pgid > 1 {
			_ = host.SignalGroup(pgid, syscall.SIGKILL)
		}
	}
}

func TestLaunchResilienceFailedClaimDoesNotBlockSibling(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	for _, id := range []string{"c1", "c2"} {
		card := model.Card{
			ID: id, Objective: obj, Kind: model.KindDev, Title: id,
			Tier: model.TierT2, Status: model.CardReady,
			Channel: "cha", Model: "model-a",
			Claims: []model.Claim{{ID: "cl-" + id, Path: "f.go", Symbol: "Run"}},
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
	}
	r := New(home)
	r.Prompt = func(_ context.Context, in prompt.Input) (string, error) {
		if in.Card.ID == "c1" {
			return "", errors.New("prompt down")
		}
		return "PROMPT-OK", nil
	}
	var launched []string
	r.Launch = func(_ context.Context, _ string, id string) (int, error) {
		launched = append(launched, id)
		return 77, nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	c1, err := db.GetCard(ctx, "c1")
	if err != nil || c1.Status != model.CardWaiting || !strings.HasPrefix(c1.WaitReason, "backoff:") {
		t.Fatalf("lower card %+v %v", c1, err)
	}
	failed, err := db.ListAttempts(ctx, "c1")
	if err != nil || len(failed) != 1 || failed[0].Outcome != model.OutcomeLaunchFailed {
		t.Fatalf("lower attempts %+v %v", failed, err)
	}
	c2, err := db.GetCard(ctx, "c2")
	if err != nil || c2.Status != model.CardRunning {
		t.Fatalf("higher card %+v %v", c2, err)
	}
	started, err := db.ListAttempts(ctx, "c2")
	if err != nil || len(started) != 1 || started[0].PID != 77 {
		t.Fatalf("higher attempts %+v %v", started, err)
	}
	if len(launched) != 1 {
		t.Fatalf("launches %v", launched)
	}
}

func captureStderr(t *testing.T) func() string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	closed := false
	t.Cleanup(func() {
		if !closed {
			os.Stderr = old
			_ = w.Close()
			<-done
		}
	})
	return func() string {
		if !closed {
			closed = true
			os.Stderr = old
			_ = w.Close()
			<-done
		}
		return buf.String()
	}
}
