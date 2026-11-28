package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/route"
	"github.com/OttoPrua/Cardex/internal/sched"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestStoredRepairRetryRestoresChannel(t *testing.T) {
	const (
		head = "HEAD-SENTINEL-aa11"
		mid  = "MID-SENTINEL-bb22"
		tail = "TAIL-SENTINEL-cc33"
		bare = "UNLABELED-SENTINEL-dd44"
		note = "NOTE-SENTINEL-ee77"
		key  = "FIELD-SENTINEL-ff66"
	)
	archive := head + "\n" + bare + "\nsession_key: " + key + "\n<<<<<<< ours\ntoken=SUPERSECRET-ee55\n" +
		strings.Repeat("M", 3000) + "\n" + mid + "\n" + strings.Repeat("Z", 500) + "\n" + tail + "\n"
	rework := "raw note " + note

	t.Run("question retry launches on the pin", func(t *testing.T) {
		home, repo, db := seedRepairRetryHome(t, "cha", archive, rework)
		before := gitRev(t, repo, "refs/heads/cardex/o/c/dev")
		db.Close()

		mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
		mustCLI(t, "--home", home, "card", "retry", "rp")
		db = openHome(t, home)
		card := mustGetCard(t, db, "rp")
		if card.Status != model.CardReady || card.Channel != "cha" || card.Body != archive || card.ReworkNote != rework || len(card.Deps) != 1 || card.Deps[0] != "dev" {
			t.Fatalf("cli left %+v", card)
		}
		db.Close()

		var reminded []string
		r := storedRepairFixtureRunner(t, home)
		r.Notify = func(_ context.Context, msg notify.Message) error {
			reminded = append(reminded, msg.QuestionID)
			return nil
		}
		r.Script = func(model.Card) ([]byte, bool) {
			return []byte(`{"attempts":{"0":{"exit_code":0,"output":"ack"},"1":{"exit_code":0,"output":"ack"}}}`), true
		}
		// One tick proves the outbound route and absence of an old reminder.
		// A second tick could already consume a newly failed fake attempt and
		// correctly create a new question.
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		db = openHome(t, home)
		defer db.Close()
		card = mustGetCard(t, db, "rp")
		if card.Channel != "cha" || card.Body != archive || card.ReworkNote != rework || len(card.Deps) != 1 || card.Deps[0] != "dev" {
			t.Fatalf("after tick %+v", card)
		}
		if gitRev(t, repo, "refs/heads/cardex/o/c/dev") != before {
			t.Fatal("dev branch moved")
		}
		attempts := mustAttempts(t, db, "rp")
		var launched *model.Attempt
		for i := range attempts {
			if attempts[i].Index == 0 {
				if attempts[i].Route.Channel != "chb" || attempts[i].PromptPath != "" {
					t.Fatalf("old attempt %+v", attempts[i])
				}
				continue
			}
			if attempts[i].Route.Channel == "chb" || attempts[i].PromptPath == "" {
				t.Fatalf("restart attempt %+v", attempts[i])
			}
			launched = &attempts[i]
		}
		if launched == nil || launched.Route.Channel != "cha" {
			t.Fatalf("attempts %+v", attempts)
		}
		prompt := readAttemptPrompt(t, launched)
		for _, leak := range []string{head, mid, tail, bare, note, key, "SUPERSECRET-ee55", "<<<<<<<", "session_key", "返工意见"} {
			if strings.Contains(prompt, leak) {
				t.Fatalf("prompt leaked %s", leak)
			}
		}
		if !strings.Contains(prompt, "错误类别：") || !strings.Contains(prompt, "计数：") {
			t.Fatalf("prompt %s", prompt)
		}
		kept, err := os.ReadFile(filepath.Join(launched.Worktree, "a.txt"))
		if err != nil || string(kept) != "OLD-COMMIT-9f3a\n" {
			t.Fatalf("worktree %q %v", kept, err)
		}
		questions := cardQuestions(t, db, "rp")
		if len(questions) != 1 || questions[0].ID != "q-old" || questions[0].Answer != "approve" || len(questions[0].Deliveries) != 1 {
			t.Fatalf("questions %+v", questions)
		}
		if len(reminded) != 0 {
			t.Fatalf("reminded answered question %v", reminded)
		}
	})

	t.Run("missing pin locks before a prompt", func(t *testing.T) {
		home, _, db := seedRepairRetryHome(t, "", archive, rework)
		db.Close()
		mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
		mustRejectRecovery(t, home, "retry", "rp")
		r := storedRepairFixtureRunner(t, home)
		r.Script = func(model.Card) ([]byte, bool) {
			return []byte(`{"attempts":{"1":{"exit_code":0,"output":"ack"}}}`), true
		}
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		db = openHome(t, home)
		defer db.Close()
		card := mustGetCard(t, db, "rp")
		if card.Status != model.CardNeedsInput || card.Channel != "chb" || card.Body != archive || card.Deps[0] != "dev" {
			t.Fatalf("locked %+v", card)
		}
		attempts := mustAttempts(t, db, "rp")
		if len(attempts) != 1 || attempts[0].PromptPath != "" || attempts[0].Route.Channel != "chb" {
			t.Fatalf("attempts %+v", attempts)
		}
		questions := cardQuestions(t, db, "rp")
		if len(questions) != 1 || len(questions[0].Deliveries) != 1 || questions[0].Answer != "approve" {
			t.Fatalf("questions %+v", questions)
		}
	})

	t.Run("recover injects a short note and keeps the branch", func(t *testing.T) {
		home, repo, db := seedRepairRetryHome(t, "cha", archive, rework)
		before := gitRev(t, repo, "refs/heads/cardex/o/c/dev")
		db.Close()
		var stderr bytes.Buffer
		if code := Main([]string{"--home", home, "card", "recover", "--note", bare, "rp"}, ioDiscard(), &stderr); code == 0 {
			t.Fatal("raw diagnostic note was accepted")
		}
		db = openHome(t, home)
		if mustGetCard(t, db, "rp").Channel != "chb" {
			t.Fatal("refused note changed the channel")
		}
		db.Close()
		mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
		mustCLI(t, "--home", home, "card", "recover", "--note", "boundary-check", "rp")
		mustCLI(t, "--home", home, "card", "recover", "rp")
		db = openHome(t, home)
		card := mustGetCard(t, db, "rp")
		if card.Status != model.CardReady || card.Channel != "cha" || card.Body != archive || card.ReworkNote != rework || card.Deps[0] != "dev" {
			t.Fatalf("recovered %+v", card)
		}
		db.Close()
		r := storedRepairFixtureRunner(t, home)
		r.Script = func(model.Card) ([]byte, bool) {
			return []byte(`{"attempts":{"1":{"exit_code":0,"output":"ack"}}}`), true
		}
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		db = openHome(t, home)
		defer db.Close()
		if gitRev(t, repo, "refs/heads/cardex/o/c/dev") != before {
			t.Fatal("dev branch moved")
		}
		attempts := mustAttempts(t, db, "rp")
		var launched *model.Attempt
		for i := range attempts {
			if attempts[i].Route.Channel == "chb" && attempts[i].PromptPath != "" {
				t.Fatal("prompt created for chb")
			}
			if attempts[i].Index > 0 {
				launched = &attempts[i]
			}
		}
		if launched == nil || launched.Route.Channel != "cha" {
			t.Fatalf("attempts %+v", attempts)
		}
		prompt := readAttemptPrompt(t, launched)
		if !strings.Contains(prompt, "批准摘要：补上边界检查") || strings.Contains(prompt, bare) || strings.Contains(prompt, note) || strings.Contains(prompt, mid) {
			t.Fatalf("prompt\n%s", prompt)
		}
	})

	t.Run("empty event pin uses the objective channel and model", func(t *testing.T) {
		home, repo, db := seedRepairRetry(t, repairRetrySeed{
			archive: archive, rework: rework,
			objChannel: "cha", objModel: "model-a", cardModel: "model-b",
			chaModels: []string{"other-a", "model-a"}, failBoth: true,
		})
		before := gitRev(t, repo, "refs/heads/cardex/o/c/dev")
		parent := mustGetCard(t, db, "dev")
		db.Close()

		mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
		mustCLI(t, "--home", home, "card", "recover", "--note", "boundary-check", "rp")
		mustCLI(t, "--home", home, "card", "recover", "rp")
		db = openHome(t, home)
		card := mustGetCard(t, db, "rp")
		if card.Status != model.CardReady || card.Channel != "cha" || card.Model != "model-a" || card.Body != archive || card.ReworkNote != rework || len(card.Deps) != 1 || card.Deps[0] != "dev" {
			t.Fatalf("recovered %+v", card)
		}
		dev := mustGetCard(t, db, "dev")
		if dev.Status != parent.Status || dev.WaitReason != parent.WaitReason || dev.WaitReason != "repair rp" {
			t.Fatalf("parent %+v", dev)
		}
		db.Close()

		r := storedRepairFixtureRunner(t, home)
		r.Script = func(model.Card) ([]byte, bool) {
			return []byte(`{"attempts":{"0":{"exit_code":1,"output":""},"1":{"exit_code":1,"output":""},"2":{"exit_code":1,"output":""},"3":{"exit_code":1,"output":""}}}`), true
		}
		deadline := time.Now().Add(20 * time.Second)
		var launched *model.Attempt
		for time.Now().Before(deadline) {
			if err := r.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			db = openHome(t, home)
			attempts := mustAttempts(t, db, "rp")
			db.Close()
			for i := range attempts {
				if attempts[i].PromptPath == "" {
					continue
				}
				if attempts[i].Route.Channel == "chb" {
					t.Fatalf("prompt created for chb %+v", attempts[i])
				}
				launched = &attempts[i]
			}
			if launched != nil {
				logPath := filepath.Join(home, "attempts", launched.ID, "output.log")
				logBody, err := os.ReadFile(logPath)
				if err == nil && strings.Contains(string(logBody), "prompt="+launched.PromptPath) {
					break
				}
			}
			launched = nil
			time.Sleep(30 * time.Millisecond)
		}
		if launched == nil || launched.Route.Channel != "cha" || launched.Route.Model != "model-a" {
			t.Fatalf("first send %+v", launched)
		}
		prompt := readAttemptPrompt(t, launched)
		for _, leak := range []string{head, mid, tail, bare, note, key, "SUPERSECRET-ee55", "<<<<<<<", "session_key", "返工意见", "chb", "model-b", "other-a"} {
			if strings.Contains(prompt, leak) {
				t.Fatalf("prompt leaked %s", leak)
			}
		}
		if !strings.Contains(prompt, "批准摘要：补上边界检查") || !strings.Contains(prompt, "错误类别：") {
			t.Fatalf("prompt\n%s", prompt)
		}
		for time.Now().Before(deadline) {
			if err := r.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			db = openHome(t, home)
			got, err := db.GetAttempt(context.Background(), launched.ID)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != "" {
				copy := got
				launched = &copy
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		if launched.Outcome != model.OutcomeNoOutput {
			t.Fatalf("first send outcome %q, attempt %+v", launched.Outcome, launched)
		}
		db = openHome(t, home)
		defer db.Close()
		if gitRev(t, repo, "refs/heads/cardex/o/c/dev") != before {
			t.Fatal("dev branch moved")
		}
		card = mustGetCard(t, db, "rp")
		if card.Channel != "cha" || card.Model != "model-a" || card.Body != archive || card.ReworkNote != rework || len(card.Deps) != 1 || card.Deps[0] != "dev" {
			t.Fatalf("after retry %+v", card)
		}
		dev = mustGetCard(t, db, "dev")
		if dev.Status != model.CardWaiting || dev.WaitReason != "repair rp" {
			t.Fatalf("parent after retry %+v", dev)
		}
		attempts := mustAttempts(t, db, "rp")
		prompted := 0
		for i := range attempts {
			if attempts[i].PromptPath == "" {
				if attempts[i].Route.Channel != "cha" && attempts[i].Route.Channel != "chb" {
					t.Fatalf("old attempt %+v", attempts[i])
				}
				continue
			}
			prompted++
			if attempts[i].Route.Channel != "cha" || attempts[i].Route.Model != "model-a" {
				t.Fatalf("send %+v", attempts[i])
			}
			kept, err := os.ReadFile(filepath.Join(attempts[i].Worktree, "a.txt"))
			if err != nil || string(kept) != "OLD-COMMIT-9f3a\n" {
				t.Fatalf("worktree %q %v", kept, err)
			}
		}
		if prompted != 1 {
			t.Fatalf("prompts %d attempts %+v", prompted, attempts)
		}
		questions := cardQuestions(t, db, "rp")
		var old *model.Question
		for i := range questions {
			if questions[i].ID == "q-old" {
				old = &questions[i]
			}
		}
		if old == nil || old.Answer != "approve" || len(old.Deliveries) != 1 || old.Deliveries[0].Destination != "file:box" {
			t.Fatalf("questions %+v", questions)
		}
		obj, err := db.GetObjective(context.Background(), "o")
		if err != nil || obj.Channel != "cha" || obj.Model != "model-a" {
			t.Fatalf("objective %+v %v", obj, err)
		}
	})

	t.Run("retry restores an empty pin before the prompt", func(t *testing.T) {
		home, _, db := seedRepairRetry(t, repairRetrySeed{
			archive: archive, rework: rework,
			objChannel: "cha", objModel: "model-a", cardModel: "model-b",
			chaModels: []string{"other-a", "model-a"}, failBoth: true,
		})
		db.Close()
		mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
		mustCLI(t, "--home", home, "card", "retry", "rp")
		db = openHome(t, home)
		if card := mustGetCard(t, db, "rp"); card.Channel != "cha" || card.Model != "model-a" {
			t.Fatalf("retry did not stage the authorized target %+v", card)
		}
		db.Close()
		r := storedRepairFixtureRunner(t, home)
		r.Script = func(model.Card) ([]byte, bool) {
			return []byte(`{"attempts":{"2":{"exit_code":1,"output":""}}}`), true
		}
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		db = openHome(t, home)
		defer db.Close()
		prompted := 0
		for _, attempt := range mustAttempts(t, db, "rp") {
			if attempt.PromptPath == "" {
				continue
			}
			prompted++
			if attempt.Route.Channel != "cha" || attempt.Route.Model != "model-a" {
				t.Fatalf("scheduler send %+v", attempt)
			}
		}
		if prompted != 1 {
			t.Fatal("scheduler wrote no prompt")
		}
		card := mustGetCard(t, db, "rp")
		if card.Channel != "cha" || card.Model != "model-a" {
			t.Fatalf("scheduler card %+v", card)
		}
	})

	t.Run("unavailable objective channel stays locked", func(t *testing.T) {
		home, _, db := seedRepairRetry(t, repairRetrySeed{
			archive: archive, rework: rework,
			objChannel: "cha", objModel: "model-a", cardModel: "model-b", failBoth: true,
		})
		ch, err := db.GetChannel(context.Background(), "cha")
		if err != nil {
			t.Fatal(err)
		}
		ch.Health = model.HealthDegraded
		if err := db.UpdateChannel(context.Background(), &ch); err != nil {
			t.Fatal(err)
		}
		db.Close()
		mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
		mustCLI(t, "--home", home, "card", "recover", "--note", "boundary-check", "rp")
		mustRejectRecovery(t, home, "recover", "rp")
		mustRejectRecovery(t, home, "retry", "rp")
		r := storedRepairFixtureRunner(t, home)
		r.Script = func(model.Card) ([]byte, bool) {
			return []byte(`{"attempts":{"2":{"exit_code":0,"output":"ack"}}}`), true
		}
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		db = openHome(t, home)
		defer db.Close()
		card := mustGetCard(t, db, "rp")
		if card.Status != model.CardNeedsInput || card.Channel != "chb" || card.Model != "model-b" || card.Body != archive || card.Deps[0] != "dev" {
			t.Fatalf("locked %+v", card)
		}
		for _, attempt := range mustAttempts(t, db, "rp") {
			if attempt.PromptPath != "" || (attempt.Route.Channel != "cha" && attempt.Route.Channel != "chb") {
				t.Fatalf("attempt %+v", attempt)
			}
		}
	})

	t.Run("card pin stays ahead of the objective", func(t *testing.T) {
		home, _, db := seedRepairRetry(t, repairRetrySeed{
			pin: "cha", archive: archive, rework: rework,
			objChannel: "chb", objModel: "model-b", cardModel: "model-b",
		})
		db.Close()
		mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
		mustCLI(t, "--home", home, "card", "recover", "rp")
		db = openHome(t, home)
		defer db.Close()
		card := mustGetCard(t, db, "rp")
		if card.Status != model.CardReady || card.Channel != "cha" || card.Model != "" {
			t.Fatalf("card pin %+v", card)
		}
	})

	t.Run("missing card pin does not borrow a bad objective", func(t *testing.T) {
		home, _, db := seedRepairRetry(t, repairRetrySeed{
			pin: "gone", archive: archive, rework: rework,
			objChannel: "cha", objModel: "model-a", cardModel: "model-b",
		})
		db.Close()
		mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
		mustRejectRecovery(t, home, "recover", "rp")
		mustRejectRecovery(t, home, "retry", "rp")
		r := storedRepairFixtureRunner(t, home)
		r.Script = func(model.Card) ([]byte, bool) {
			return []byte(`{"attempts":{"1":{"exit_code":0,"output":"ack"}}}`), true
		}
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		db = openHome(t, home)
		defer db.Close()
		card := mustGetCard(t, db, "rp")
		if card.Status != model.CardNeedsInput || card.Channel != "chb" || card.Model != "model-b" {
			t.Fatalf("locked %+v", card)
		}
		for _, attempt := range mustAttempts(t, db, "rp") {
			if attempt.PromptPath != "" {
				t.Fatalf("prompt %+v", attempt)
			}
		}
	})
}

// storedRepairFixtureRunner keeps detached host behavior but explicitly
// joins each synthetic host before testing.TempDir removes its Git files.
func storedRepairFixtureRunner(t *testing.T, home string) *sched.Runner {
	t.Helper()
	r := sched.New(home)
	r.Launch = func(_ context.Context, home, attempt string) (int, error) {
		exe, err := os.Executable()
		if err != nil {
			return 0, err
		}
		cmd := exec.Command(exe, "host", attempt)
		cmd.Env = append(os.Environ(), "CARDEX_HOME="+home, "CARDEX_SCHED_HOST=1", "CARDEX_SCHED_ATTEMPT="+attempt)
		proc.Detach(cmd)
		var log bytes.Buffer
		cmd.Stdout, cmd.Stderr = &log, &log
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			if err := <-done; err != nil {
				t.Errorf("synthetic host did not drain: %v: %s", err, &log)
			}
		})
		return cmd.Process.Pid, nil
	}
	return r
}

// A recovered card has a single restart destination even if a third
// provider becomes eligible before the scheduler sees it.
func TestRecoveredCardNeverFallsBackOnDegradedTarget(t *testing.T) {
	ctx := context.Background()
	home, _, db := seedRepairRetryHome(t, "cha", "diagnostic archive", "")
	ch := model.Channel{ID: "chc", Runner: model.RunnerFake, Family: "gamma",
		SourceGroup: "gamma", Models: []string{"model-c"}, Concurrency: 1,
		Health: model.HealthOK, Tier: model.TierT2}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{ID: "pos-chc", Model: "model-c", Family: "gamma",
		Tiers: []model.Tier{model.TierT2}, Preference: 1}
	if err := db.CreatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
	db.Close()
	mustCLI(t, "--home", home, "question", "answer", "q-old", "approve")
	mustCLI(t, "--home", home, "card", "recover", "rp")
	db = openHome(t, home)
	target, err := db.GetChannel(ctx, "cha")
	if err != nil {
		t.Fatal(err)
	}
	target.Health = model.HealthDegraded
	if err := db.UpdateChannel(ctx, &target); err != nil {
		t.Fatal(err)
	}
	db.Close()
	r := sched.New(home)
	r.Launch = func(context.Context, string, string) (int, error) {
		t.Fatal("fallback launched without approval")
		return 0, nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	db = openHome(t, home)
	defer db.Close()
	got := mustGetCard(t, db, "rp")
	if got.Status != model.CardNeedsInput || got.Channel != "cha" || got.WaitReason == "" {
		t.Fatalf("recovery did not stay pinned: status=%s channel=%s reason=%q", got.Status, got.Channel, got.WaitReason)
	}
	if len(mustAttempts(t, db, "rp")) != 1 {
		t.Fatal("a prompt materialized for a non-approved destination")
	}
}

// A note changes prompt input only. It is not approval to unblock a
// different question when the card was not sitting on an automatic route.
func TestRecoveryNoteDoesNotUnblockOtherQuestion(t *testing.T) {
	ctx := context.Background()
	home, _, db := seedRepairRetryHome(t, "cha", "diagnostic archive", "")
	card := model.Card{ID: "note-only", Objective: "o", Kind: model.KindRepair,
		Title: "synthetic repair", Tier: model.TierT2, Status: model.CardNeedsInput,
		Channel: "cha", WaitReason: "stored question"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	db.Close()
	mustCLI(t, "--home", home, "card", "recover", "--note", "boundary-check", card.ID)
	db = openHome(t, home)
	defer db.Close()
	got := mustGetCard(t, db, card.ID)
	if got.Status != model.CardNeedsInput || got.WaitReason != card.WaitReason {
		t.Fatalf("note released unrelated blocker: status=%s reason=%q", got.Status, got.WaitReason)
	}
	events, err := db.ListEvents(ctx, "card:"+card.ID)
	if err != nil || len(events) != 1 || events[0].Type != model.EventRepairHint {
		t.Fatalf("note event missing: count=%d error=%v", len(events), err)
	}
}

func TestRepairHintCannotAuthorizeRecoveryOrEchoFreeform(t *testing.T) {
	ctx := context.Background()
	home, _, db := seedRepairRetryHome(t, "missing-channel", "token=SYNTHETIC_AUTH_VALUE\nfirst diagnostic line\nsecond diagnostic line", "")
	q, err := db.GetQuestion(ctx, "q-old")
	if err != nil {
		t.Fatal(err)
	}
	q.Answer = "approve"
	if err := db.UpdateQuestion(ctx, &q); err != nil {
		t.Fatal(err)
	}
	before := mustGetCard(t, db, "rp")
	before.WaitReason, before.WaitQuestionID = "unrelated authorization hold", ""
	if err := db.UpdateCard(ctx, &before); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for _, fragment := range []string{"SYNTHETIC_AUTH_VALUE", "first diagnostic line second diagnostic line", "diagnostic"} {
		var out, errBuf bytes.Buffer
		c := Main([]string{"--home", home, "card", "recover", "--note", fragment, "rp"}, &out, &errBuf)
		if c == 0 || strings.Contains(out.String()+errBuf.String(), fragment) {
			t.Fatal("raw hint was accepted or echoed")
		}
	}
	var out, errBuf bytes.Buffer
	code := Main([]string{"--home", home, "--json", "card", "recover", "--note", "boundary-check", "rp"}, &out, &errBuf)
	if code != 0 || strings.Contains(out.String()+errBuf.String(), "SYNTHETIC_AUTH_VALUE") {
		t.Fatalf("fixed-hint command failed or echoed the archive: code %d", code)
	}
	mustRejectRecovery(t, home, "recover", "rp")
	mustRejectRecovery(t, home, "retry", "rp")
	db = openHome(t, home)
	defer db.Close()
	got := mustGetCard(t, db, "rp")
	if got.Status != before.Status || got.WaitReason != before.WaitReason || got.WaitQuestionID != before.WaitQuestionID ||
		got.Channel != before.Channel || got.Model != before.Model || got.Body != before.Body {
		t.Fatal("saving a fixed hint changed the current unrelated blocker or route")
	}
	events, err := db.ListEvents(ctx, "card:rp")
	if err != nil {
		t.Fatal(err)
	}
	hints := 0
	for _, event := range events {
		if event.Type == model.EventRepairHint {
			hints++
			if string(event.Data) != `{"code":"boundary-check"}` {
				t.Fatal("hint event contains arbitrary data")
			}
		}
		if event.Type == "repair_recovered" {
			t.Fatal("hint created a recovery grant")
		}
	}
	if hints != 1 {
		t.Fatalf("stored %d hint events, want one", hints)
	}
}

func TestRepairRecoveryCannotUseEarlierSwitchOrAttempt(t *testing.T) {
	for _, changed := range []string{"switch", "attempt"} {
		t.Run(changed, func(t *testing.T) {
			ctx := context.Background()
			home, _, db := seedRepairRetryHome(t, "cha", "synthetic archive", "")
			q, err := db.GetQuestion(ctx, "q-old")
			if err != nil {
				t.Fatal(err)
			}
			q.Answer = "approve"
			if err := db.UpdateQuestion(ctx, &q); err != nil {
				t.Fatal(err)
			}
			if changed == "switch" {
				if err := db.AppendEvent(ctx, &model.Event{Entity: "card:rp", Type: "auto_route", Data: []byte(`{"channel":"chb","pin":"cha"}`)}); err != nil {
					t.Fatal(err)
				}
			} else {
				attempt, err := db.GetAttempt(ctx, "a-old")
				if err != nil {
					t.Fatal(err)
				}
				attempt.ID, attempt.Index = "a-new", attempt.Index+1
				if err := db.CreateAttempt(ctx, &attempt); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			mustRejectRecovery(t, home, "recover", "rp")
			mustRejectRecovery(t, home, "retry", "rp")
			db = openHome(t, home)
			defer db.Close()
			got := mustGetCard(t, db, "rp")
			if got.Status != model.CardNeedsInput || got.Channel != "chb" || got.WaitQuestionID != q.ID {
				t.Fatal("previous approval replaced current blocker or route")
			}
		})
	}
}

func TestCombinedAnswerRetryUsesRecordedRecoveryRoute(t *testing.T) {
	home, _, db := seedRepairRetryHome(t, "cha", "synthetic archive", "")
	db.Close()
	mustCLI(t, "--home", home, "question", "answer", "--retry", "q-old", "approve")
	db = openHome(t, home)
	defer db.Close()
	got := mustGetCard(t, db, "rp")
	if got.Status != model.CardReady || got.Channel != "cha" {
		t.Fatal("answer and retry did not stage the exact recovery route")
	}
	events, err := db.ListEvents(context.Background(), "card:rp")
	if err != nil {
		t.Fatal(err)
	}
	if pin, ok := route.RecoveryPin(got, events); !ok || pin.Channel != "cha" {
		t.Fatal("recovery grant was not recorded for the current switch")
	}
}

func TestPauseCannotUnblockLegacyAutomaticRecovery(t *testing.T) {
	for _, tc := range []struct {
		name           string
		answer         string
		noQuestion     bool
		alreadyPaused  bool
		noAttemptProof bool
		noTiming       bool
	}{
		{name: "rejected decision", answer: "reject"},
		{name: "no decision", noQuestion: true},
		{name: "hold survives without historical timing", noQuestion: true, noAttemptProof: true},
		{name: "legacy client already paused", answer: "reject", alreadyPaused: true},
		{name: "legacy client with malformed question timing", answer: "reject", alreadyPaused: true, noTiming: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, _, db := seedRepairRetry(t, repairRetrySeed{
				pin: "cha", archive: "synthetic archive", legacyNoOffer: true, legacyNoQuestion: tc.noQuestion,
			})
			before := mustGetCard(t, db, "rp")
			before.WaitReason = "current authorization blocker"
			if tc.alreadyPaused {
				// Simulate a pre-fix client that already cleared its question link.
				before.Status = model.CardPaused
				before.WaitQuestionID = ""
			}
			if err := db.UpdateCard(ctx, &before); err != nil {
				t.Fatal(err)
			}
			if tc.noTiming {
				q, err := db.GetQuestion(ctx, "q-old")
				if err != nil {
					t.Fatal(err)
				}
				q.Text += "\n- attempt a-old on chb/model-b: unclassified"
				q.Deliveries = nil
				if err := db.UpdateQuestion(ctx, &q); err != nil {
					t.Fatal(err)
				}
			}
			if tc.noAttemptProof {
				if err := db.AppendEvent(ctx, &model.Event{
					Entity: "card:rp", Type: "auto_route",
					Data: []byte(`{"channel":"chb","pin":"cha"}`),
				}); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			if tc.answer != "" {
				mustCLI(t, "--home", home, "question", "answer", "q-old", tc.answer)
			}
			if !tc.alreadyPaused {
				mustCLI(t, "--home", home, "card", "pause", "rp")
			}
			for _, verb := range []string{"retry", "resume", "revive", "recover"} {
				mustRejectRecovery(t, home, verb, "rp")
			}
			db = openHome(t, home)
			defer db.Close()
			got := mustGetCard(t, db, "rp")
			if got.Status != model.CardPaused || got.WaitQuestionID != "" || got.Channel != before.Channel || got.Model != before.Model ||
				got.WaitReason != before.WaitReason || len(mustAttempts(t, db, got.ID)) != 1 {
				t.Fatal("failed retry changed the paused card, its blocker, or its attempt history")
			}
		})
	}
}

func TestResumeOrdinarySameChannelRetryAfterPause(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		outcome      model.Outcome
		status       model.CardStatus
		noQuestion   bool
	}{
		{name: "immediate retry", config: "0", outcome: model.OutcomeTransport, status: model.CardReady},
		{name: "retry backoff", config: "3600", outcome: model.OutcomeTimeout, status: model.CardWaiting},
		{name: "ordinary rework without a decision", config: "3600", outcome: model.OutcomeTestsFailed, status: model.CardReady, noQuestion: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, _, db := seedRepairRetry(t, repairRetrySeed{pin: "cha", legacyNoOffer: true, legacyNoQuestion: tc.noQuestion})
			t.Setenv("CARDEX_HOME", home)
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"schedules":[],"loop":{"retry_backoff_seconds":`+tc.config+`}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			// An earlier, rejected legacy decision has its own attempt. It
			// cannot hold the new ordinary retry on the same switch.
			old, err := db.GetAttempt(ctx, "a-old")
			if err != nil {
				t.Fatal(err)
			}
			if !tc.noQuestion {
				q, err := db.GetQuestion(ctx, "q-old")
				if err != nil {
					t.Fatal(err)
				}
				q.Text, q.Answer = "no other channel is available", "reject"
				q.Deliveries = append(q.Deliveries, model.Delivery{At: old.EndedAt.Add(time.Hour), Adapter: "file"})
				if err := db.UpdateQuestion(ctx, &q); err != nil {
					t.Fatal(err)
				}
			}
			card := mustGetCard(t, db, "rp")
			card.Status = model.CardRunning
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			attempt := model.Attempt{
				ID: "a-current", Card: card.ID, Index: old.Index + 1,
				Route: old.Route, Outcome: tc.outcome,
				StartedAt: old.EndedAt.Add(time.Second), EndedAt: old.EndedAt.Add(2 * time.Second),
			}
			if err := db.CreateAttempt(ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := loop.DevStep(loop.WithClock(ctx, attempt.EndedAt.Add(time.Second)), card.Objective); err != nil {
				t.Fatal(err)
			}
			db = openHome(t, home)
			before := mustGetCard(t, db, card.ID)
			if before.Status != tc.status || before.Channel != old.Route.Channel || before.WaitQuestionID != "" {
				t.Fatalf("ordinary scheduler disposition yielded %s on %s instead of %s on %s", before.Status, before.Channel, tc.status, old.Route.Channel)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			mustCLI(t, "--home", home, "card", "pause", card.ID)
			mustCLI(t, "--home", home, "card", "resume", card.ID)
			db = openHome(t, home)
			defer db.Close()
			got := mustGetCard(t, db, card.ID)
			if got.Status != model.CardReady || got.Channel != before.Channel || got.Model != before.Model ||
				got.WaitQuestionID != "" || len(mustAttempts(t, db, card.ID)) != 2 {
				t.Fatal("resuming an ordinary retry changed its route, question, or attempt history")
			}
		})
	}
}

func TestResumeLeavesOrdinaryCardAndNewAutomaticRouteUsable(t *testing.T) {
	ctx := context.Background()
	home, _, db := seedRepairRetry(t, repairRetrySeed{
		pin: "cha", archive: "synthetic archive", legacyNoOffer: true,
	})
	ordinary := mustGetCard(t, db, "dev")
	ordinary.Status = model.CardPaused
	if err := db.UpdateCard(ctx, &ordinary); err != nil {
		t.Fatal(err)
	}
	newRoute := mustGetCard(t, db, "rp")
	newRoute.Status = model.CardPaused
	if err := db.UpdateCard(ctx, &newRoute); err != nil {
		t.Fatal(err)
	}
	// A later generation on the same channel has not yet run. An older
	// failure is not evidence that its ordinary pause is a recovery block.
	if err := db.AppendEvent(ctx, &model.Event{
		Entity: "card:rp", Type: "auto_route",
		Data: []byte(`{"channel":"chb","pin":"cha"}`),
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	mustCLI(t, "--home", home, "card", "resume", "dev")
	mustCLI(t, "--home", home, "card", "resume", "rp")
	db = openHome(t, home)
	defer db.Close()
	if mustGetCard(t, db, "dev").Status != model.CardReady ||
		mustGetCard(t, db, "rp").Status != model.CardReady {
		t.Fatal("ordinary pause/resume was changed outside the recovery scope")
	}
}

type repairRetrySeed struct {
	pin, archive, rework string
	objChannel, objModel string
	cardModel            string
	chaModels            []string
	failBoth             bool
	legacyNoOffer        bool
	legacyNoQuestion     bool
}

func seedRepairRetryHome(t *testing.T, pin, archive, rework string) (string, string, *store.Store) {
	t.Helper()
	return seedRepairRetry(t, repairRetrySeed{pin: pin, archive: archive, rework: rework})
}

func seedRepairRetry(t *testing.T, seed repairRetrySeed) (string, string, *store.Store) {
	t.Helper()
	pin, archive, rework := seed.pin, seed.archive, seed.rework
	home := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	gitCmd(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	gitCmd(t, repo, "checkout", "-b", "cardex/o/c/dev")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("OLD-COMMIT-9f3a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	gitCmd(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "dev")
	gitCmd(t, repo, "checkout", "main")

	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{\"schedules\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	project := model.Project{
		ID: "p", RepoPath: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), "wt"),
		DesignDir: "docs/design/", Sandbox: "off",
	}
	objective := model.Objective{
		ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
		Channel: seed.objChannel, Model: seed.objModel,
	}
	if err := db.CreateProject(ctx, &project); err != nil || db.CreateObjective(ctx, &objective) != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ id, family, modelName string }{
		{"cha", "alpha", "model-a"},
		{"chb", "beta", "model-b"},
	} {
		models := []string{spec.modelName}
		if spec.id == "cha" && len(seed.chaModels) > 0 {
			models = seed.chaModels
		}
		ch := model.Channel{
			ID: spec.id, Runner: model.RunnerFake, Family: spec.family, SourceGroup: spec.family,
			Models: models, Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2,
		}
		pos := model.Positioning{
			ID: "pos-" + spec.id, Model: spec.modelName, Family: spec.family,
			Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 1,
		}
		if err := db.CreateChannel(ctx, &ch); err != nil || db.CreatePositioning(ctx, &pos) != nil {
			t.Fatal(err)
		}
	}
	dev := model.Card{
		ID: "dev", Objective: "o", Kind: model.KindDev, Title: "dev", Tier: model.TierT2,
		Status: model.CardWaiting, WaitReason: "repair rp", Base: "main",
	}
	repair := model.Card{
		ID: "rp", Objective: "o", Kind: model.KindRepair, Title: "repair", Tier: model.TierT2,
		Status: model.CardNeedsInput, Channel: "chb", Model: seed.cardModel, Base: "cardex/o/c/dev",
		Body: archive, ReworkNote: rework, Deps: []string{"dev"}, WaitQuestionID: "q-old",
	}
	if seed.legacyNoQuestion {
		repair.WaitQuestionID = ""
	}
	if err := db.CreateCard(ctx, &dev); err != nil || db.CreateCard(ctx, &repair) != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	raw, _ := json.Marshal(map[string]string{"channel": "chb", "pin": pin})
	auto := model.Event{Entity: "card:rp", Type: "auto_route", TS: now.Add(-2 * time.Minute), Data: raw}
	if err := db.AppendEvent(ctx, &auto); err != nil {
		t.Fatal(err)
	}
	exit := 1
	saveDisposed := func(attempt model.Attempt) {
		t.Helper()
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		if err := db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + attempt.ID, Type: "dev_disposed"}); err != nil {
			t.Fatal(err)
		}
	}
	oldIndex := 0
	if seed.failBoth {
		saveDisposed(model.Attempt{
			ID: "a-cha", Card: "rp", Index: 0, Route: model.Route{Channel: "cha", Model: "model-a"},
			StartedAt: now.Add(-4 * time.Minute), EndedAt: now.Add(-3 * time.Minute),
			Exit: &exit, Outcome: model.OutcomeNoOutput,
		})
		oldIndex++
	}
	saveDisposed(model.Attempt{
		ID: "a-old", Card: "rp", Index: oldIndex, Route: model.Route{Channel: "chb", Model: "model-b"},
		StartedAt: now.Add(-time.Minute), EndedAt: now,
		Exit: &exit, Outcome: model.OutcomeNoOutput,
	})
	q := model.Question{
		ID: "q-old", Objective: "o", Scope: "rp", ScopeKind: "card", Kind: model.QuestionCustom,
		Text: "no output after switching channel", Options: []string{"approve", "reject"},
		Deliveries: []model.Delivery{{At: now, Adapter: "file", Destination: "file:box"}},
	}
	if seed.legacyNoQuestion {
		return home, repo, db
	}
	if err := db.CreateQuestion(ctx, &q); err != nil {
		t.Fatal(err)
	}
	if seed.legacyNoOffer {
		return home, repo, db
	}
	offer, err := json.Marshal(model.CardRetryOffer{Card: "rp", Attempt: "a-old", Reason: model.CardRetryNoOutput})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AppendEvent(ctx, &model.Event{Entity: "question:" + q.ID, Type: model.EventCardRetryOffered, Data: offer}); err != nil {
		t.Fatal(err)
	}
	block, err := route.FallbackRetryOffer("rp", route.FallbackRetryBlock{Question: q.ID, Attempt: "a-old", AutoRoute: auto.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AppendEvent(ctx, &block); err != nil {
		t.Fatal(err)
	}
	return home, repo, db
}

func mustRejectRecovery(t *testing.T, home, verb, id string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	if code := Main([]string{"--home", home, "card", verb, id}, &out, &errBuf); code == 0 {
		t.Fatalf("unapproved %s was accepted", verb)
	}
}

func cardQuestions(t *testing.T, db *store.Store, cardID string) []model.Question {
	t.Helper()
	items, err := db.ListQuestions(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Question
	for _, item := range items {
		if item.Scope == cardID {
			out = append(out, item)
		}
	}
	return out
}

func readAttemptPrompt(t *testing.T, attempt *model.Attempt) string {
	t.Helper()
	body, err := os.ReadFile(attempt.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func ioDiscard() *bytes.Buffer { return &bytes.Buffer{} }
