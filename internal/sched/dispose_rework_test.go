package sched

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/loop"
	"cardex/internal/model"
	"cardex/internal/store"
)

func TestDesignFamilyFailureDisposition(t *testing.T) {
	resetAt := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	kinds := []model.CardKind{
		model.KindDesign, model.KindResearch, model.KindPlan, model.KindReplan, model.KindSpike,
	}
	cases := []struct {
		name     string
		class    model.ErrorClass
		accept   bool
		output   string
		rework   int
		priors   []model.Attempt
		want     string
		question string
		same     bool
		branch   bool
	}{
		{name: "tests_failed", accept: true, want: "rework"},
		{name: "tests_failed past limit", accept: true, rework: 2, want: "question", question: "rework_count"},
		{name: "no_output", want: "switch"},
		{name: "no_output again", priors: []model.Attempt{{Index: 0, Outcome: model.OutcomeNoOutput, Route: model.Route{Channel: "cha"}}}, want: "question", question: "no output", same: true},
		{name: "quota_hit", class: model.ErrorQuotaHit, output: "quota hit reset at " + resetAt, want: "switch"},
		{name: "auth_failed", class: model.ErrorAuthFailed, output: "token rejected", want: "auth"},
		{name: "transport", class: model.ErrorTransport, output: "reset", want: "backoff"},
		{name: "transport switch", class: model.ErrorTransport, output: "reset", priors: transportPriors(2), want: "switch"},
		{name: "timeout", class: model.ErrorTimeout, output: "slow", want: "backoff"},
		{name: "timeout switch", class: model.ErrorTimeout, output: "slow", priors: transportPriors(2), want: "switch"},
		{name: "permission_denied", class: model.ErrorPermissionDenied, output: "rm denied", want: "question", question: "rm denied", same: true},
		{name: "refusal", class: model.ErrorRefusal, output: "no", want: "question", question: "refused", same: true},
		{name: "budget_limited", class: model.ErrorBudgetLimited, output: "budget", want: "same"},
		{name: "budget_limited again", class: model.ErrorBudgetLimited, output: "budget", priors: []model.Attempt{{Index: 0, Outcome: model.OutcomeBudgetLimited, Route: model.Route{Channel: "cha"}}}, want: "question", question: "budget", same: true},
		{name: "violation", class: model.ErrorViolation, output: "hard line", want: "void", branch: true},
	}
	for _, kind := range kinds {
		for _, tc := range cases {
			tc := tc
			kind := kind
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				assertDesignFailure(t, kind, tc.accept, tc.class, tc.output, tc.rework, tc.priors, tc.want, tc.question, tc.same, tc.branch, false)
			})
			if kind == model.KindDesign {
				t.Run(string(kind)+"/"+tc.name+"/batch", func(t *testing.T) {
					assertDesignFailure(t, kind, tc.accept, tc.class, tc.output, tc.rework, tc.priors, tc.want, tc.question, tc.same, tc.branch, true)
				})
			}
		}
	}
}

func transportPriors(n int) []model.Attempt {
	out := make([]model.Attempt, n)
	for i := 0; i < n; i++ {
		out[i] = model.Attempt{Index: i, Outcome: model.OutcomeTransport, Route: model.Route{Channel: "cha"}}
	}
	return out
}

func assertDesignFailure(t *testing.T, kind model.CardKind, accept bool, class model.ErrorClass, output string, rework int, priors []model.Attempt, want, question string, same, branch, batch bool) {
	t.Helper()
	ctx, home, db, repo, obj := dispositionHome(t)
	seedRoutes(t, db)
	card := devCard(obj, "c")
	card.Kind = kind
	card.Tier = model.TierT3
	card.Channel = "cha"
	card.ReworkCount = rework
	if accept {
		card.Acceptance = []string{"false"}
	}
	var branchName string
	if branch || want == "rework" {
		branchName = "cardex/" + obj + "/c/c"
		git(t, repo, "checkout", "-b", branchName)
		git(t, repo, "checkout", "main")
	}
	attempt := model.Attempt{Index: len(priors) + 1, Route: model.Route{Channel: "cha"}, ErrorClass: class, Branch: branchName}
	if batch {
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		exit := 0
		for i, prior := range priors {
			if prior.ID == "" {
				prior.ID = "prior-" + string(rune('a'+i))
			}
			prior.Card = card.ID
			prior.Exit = &exit
			prior.EndedAt = time.Now().UTC().Add(-time.Hour)
			if err := db.CreateAttempt(ctx, &prior); err != nil {
				t.Fatal(err)
			}
			if err := db.AppendEvent(ctx, &model.Event{
				Entity: "attempt:" + prior.ID,
				Type:   "dev_disposed",
				Data:   []byte(`{}`),
			}); err != nil {
				t.Fatal(err)
			}
		}
		attempt.ID = "a-c"
		attempt.Card = card.ID
		attempt.Worktree = repo
		attempt.Exit = &exit
		attempt.EndedAt = time.Now().UTC()
		attempt.Outcome = failureOutcome(class, accept)
		attempt.ErrorExcerpt = output
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CARDEX_HOME", home)
		if err := loop.DevStep(ctx, obj); err != nil {
			t.Fatal(err)
		}
	} else {
		finishHarvest(t, db, home, repo, card, attempt, "", output, false, priors)
		tickDefault(t, home)
		if got := mustAttempt(t, db, "a-c").Outcome; got == "" {
			t.Fatal("attempt not consumed")
		}
	}
	got := mustCard(t, db, "c")
	switch want {
	case "rework":
		if got.ReworkCount != rework+1 || !readyOrRunning(got.Status) {
			t.Fatalf("card %+v", got)
		}
	case "switch":
		if got.Channel != "chb" || !readyOrRunning(got.Status) {
			t.Fatalf("card %+v", got)
		}
	case "backoff":
		if got.Channel != "cha" || got.Status != model.CardWaiting || !strings.HasPrefix(got.WaitReason, "backoff:") {
			t.Fatalf("card %+v", got)
		}
		until, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(got.WaitReason, "backoff:"))
		if err != nil || !until.After(time.Now()) {
			t.Fatalf("deadline %s %v", got.WaitReason, err)
		}
		released := got
		released.WaitReason = "backoff:" + time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
		if err := db.UpdateCard(ctx, &released); err != nil {
			t.Fatal(err)
		}
		tickDefault(t, home)
		again := mustCard(t, db, "c")
		if again.Channel != "cha" || !readyOrRunning(again.Status) {
			t.Fatalf("after expiry %+v", again)
		}
	case "same":
		if got.Channel != "cha" || !readyOrRunning(got.Status) {
			t.Fatalf("card %+v", got)
		}
	case "auth":
		if got.Channel != "chb" || !readyOrRunning(got.Status) {
			t.Fatalf("card %+v", got)
		}
		ch, err := db.GetChannel(ctx, "cha")
		if err != nil || ch.Health != model.HealthAuthFailed {
			t.Fatalf("channel %+v %v", ch, err)
		}
		if _, err := os.Stat(filepath.Join(home, "notifications", "auth-cha.json")); err != nil {
			t.Fatal(err)
		}
	case "question":
		if got.Status != model.CardNeedsInput || !strings.Contains(questionText(t, db), question) {
			t.Fatalf("card %+v questions %q", got, questionText(t, db))
		}
		if same && got.Channel != "cha" {
			t.Fatalf("channel switched to %s", got.Channel)
		}
	case "void":
		if got.Status != model.CardNeedsInput || !strings.Contains(questionText(t, db), "voided") {
			t.Fatalf("card %+v questions %q", got, questionText(t, db))
		}
		if !branchExists(repo, branchName) {
			t.Fatal("branch removed")
		}
		events, err := db.ListEvents(ctx, "attempt:a-c")
		if err != nil {
			t.Fatal(err)
		}
		voided := false
		for _, event := range events {
			if event.Type == "attempt_voided" && strings.Contains(string(event.Data), branchName) {
				voided = true
			}
		}
		if !voided {
			t.Fatalf("events %+v", events)
		}
	default:
		t.Fatalf("want %s", want)
	}
	if want == "switch" && class == model.ErrorQuotaHit {
		ch, err := db.GetChannel(ctx, "cha")
		if err != nil || !strings.Contains(ch.LastError, "quota blocked") {
			t.Fatalf("channel %+v %v", ch, err)
		}
	}
	cards, err := db.ListCards(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("extra cards %+v", cards)
	}
}

func failureOutcome(class model.ErrorClass, accept bool) model.Outcome {
	if accept && class == "" {
		return model.OutcomeTestsFailed
	}
	switch class {
	case model.ErrorQuotaHit:
		return model.OutcomeQuotaHit
	case model.ErrorAuthFailed:
		return model.OutcomeAuthFailed
	case model.ErrorTransport:
		return model.OutcomeTransport
	case model.ErrorTimeout:
		return model.OutcomeTimeout
	case model.ErrorPermissionDenied:
		return model.OutcomePermissionDenied
	case model.ErrorRefusal:
		return model.OutcomeRefusal
	case model.ErrorBudgetLimited:
		return model.OutcomeBudgetLimited
	case model.ErrorViolation:
		return model.OutcomeViolation
	default:
		return model.OutcomeNoOutput
	}
}

func TestDesignFailureStaysUnconsumedWhenActionFails(t *testing.T) {
	ctx, home, db, repo, obj := dispositionHome(t)
	card := devCard(obj, "c")
	card.Kind = model.KindDesign
	finishHarvest(t, db, home, repo, card, model.Attempt{
		ErrorClass: model.ErrorQuotaHit, Route: model.Route{Channel: "missing"},
	}, "", "quota hit reset at "+time.Now().UTC().Add(time.Hour).Format(time.RFC3339), false, nil)
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 41, nil }
	if err := r.Tick(ctx); err == nil {
		t.Fatal("missing channel consumed the harvest")
	}
	if mustAttempt(t, db, "a-c").Outcome != "" {
		t.Fatal("attempt consumed")
	}
	if mustCard(t, db, "c").Status != model.CardRunning {
		t.Fatal("card changed")
	}
}

func TestPlanAndReplanListsBothPaths(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "hook"
		if batch {
			name = "batch"
		}
		t.Run("plan/"+name, func(t *testing.T) {
			text := "## 新卡\n```json\n[" +
				`{"key":"skel","kind":"skeleton","title":"Skel","body":"api"},` +
				`{"key":"impl","kind":"dev","title":"Impl","body":"work","deps":["skel"]}` +
				"]\n```\n## 开放问题\n- [decision] 是否拆分\n## 结论\nship\n"
			db, _ := disposeListed(t, model.KindPlan, text, batch, nil)
			skel := cardByTitle(t, db, "Skel")
			impl := cardByTitle(t, db, "Impl")
			if skel.Kind != model.KindSkeleton || impl.Kind != model.KindDev || !hasDep(impl, skel.ID) {
				t.Fatalf("skel %+v impl %+v", skel, impl)
			}
			if !nodeHasText(t, db, "n", "是否拆分") {
				t.Fatal("design question was not absorbed")
			}
			if mustCard(t, db, "plan").Status != model.CardDone {
				t.Fatalf("plan %s", mustCard(t, db, "plan").Status)
			}
			if !batch && mustAttempt(t, db, "a-plan").Outcome != model.OutcomePassed {
				t.Fatal("plan attempt not consumed")
			}
		})
		t.Run("replan/"+name, func(t *testing.T) {
			text := "## 新卡\n```json\n[" +
				`{"key":"keep","op":"modify","kind":"dev","title":"Kept","body":"edited"},` +
				`{"key":"drop","op":"delete"},` +
				`{"key":"extra","kind":"dev","title":"Extra","body":"added"}` +
				"]\n```\n## 开放问题\n- [decision] 调整范围\n## 结论\nship\n"
			ctx, home, db, repo, obj := dispositionHome(t)
			if err := db.CreateNode(ctx, &model.DesignNode{
				ID: "n", Objective: obj, Layer: model.LayerModule, Status: model.NodeDrafting,
			}); err != nil {
				t.Fatal(err)
			}
			for _, spec := range []struct {
				id, title string
				started   bool
			}{
				{"keep", "Old", false},
				{"drop", "Drop", false},
				{"busy", "Busy", true},
			} {
				card := devCard(obj, spec.id)
				card.Status = model.CardReady
				card.Node = "n"
				card.Title = spec.title
				if err := db.CreateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
				if spec.started {
					exit := 0
					prior := model.Attempt{
						ID: "a-busy", Card: spec.id, Outcome: model.OutcomePassed,
						Exit: &exit, EndedAt: time.Now().UTC(), Worktree: repo,
					}
					if err := db.CreateAttempt(ctx, &prior); err != nil {
						t.Fatal(err)
					}
				}
			}
			disposeListed(t, model.KindReplan, text, batch, &listedHome{ctx, home, db, repo, obj})
			kept := mustCard(t, db, "keep")
			if kept.Title != "Kept" || kept.Body != "edited" {
				t.Fatalf("keep %+v", kept)
			}
			if _, err := db.GetCard(ctx, "drop"); err == nil {
				t.Fatal("drop still exists")
			}
			if cardByTitle(t, db, "Extra").Kind != model.KindDev {
				t.Fatal("missing extra")
			}
			if mustCard(t, db, "busy").Title != "Busy" {
				t.Fatal("started card changed")
			}
			if !nodeHasText(t, db, "n", "调整范围") {
				t.Fatal("replan design question was not absorbed")
			}
		})
	}
}

type listedHome struct {
	ctx  context.Context
	home string
	db   *store.Store
	repo string
	obj  string
}

func disposeListed(t *testing.T, kind model.CardKind, text string, batch bool, existing *listedHome) (*store.Store, string) {
	t.Helper()
	var ctx context.Context
	var home, repo, obj string
	var db *store.Store
	if existing == nil {
		ctx, home, db, repo, obj = dispositionHome(t)
		if err := db.CreateNode(ctx, &model.DesignNode{
			ID: "n", Objective: obj, Layer: model.LayerModule, Status: model.NodeDrafting,
		}); err != nil {
			t.Fatal(err)
		}
	} else {
		ctx, home, db, repo, obj = existing.ctx, existing.home, existing.db, existing.repo, existing.obj
	}
	card := devCard(obj, "plan")
	card.Kind = kind
	card.Tier = model.TierT3
	card.Node = "n"
	if batch {
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		wt := filepath.Join(t.TempDir(), "wt")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, ".cardex-result.md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		exit := 0
		attempt := model.Attempt{
			ID: "a-plan", Card: card.ID, Worktree: wt, Outcome: model.OutcomePassed,
			Exit: &exit, EndedAt: time.Now().UTC(),
		}
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CARDEX_HOME", home)
		if err := loop.DevStep(ctx, obj); err != nil {
			t.Fatal(err)
		}
		return db, obj
	}
	finishHarvest(t, db, home, repo, card, model.Attempt{}, text, "", false, nil)
	tickDefault(t, home)
	return db, obj
}

func TestDesignFileEntersIntegration(t *testing.T) {
	cases := []struct {
		kind model.CardKind
		pack bool
		stay model.CardStatus
	}{
		{kind: model.KindDesign},
		{kind: model.KindDesign, pack: true},
		{kind: model.KindPlan},
		{kind: model.KindReplan, pack: true},
		{kind: model.KindResearch, pack: true, stay: model.CardDone},
		{kind: model.KindSpike, stay: model.CardDone},
	}
	for _, tc := range cases {
		name := string(tc.kind)
		if tc.pack {
			name += "/context"
		} else {
			name += "/doc"
		}
		t.Run(name, func(t *testing.T) {
			ctx, home, db, repo, obj := dispositionHome(t)
			doc := "docs/design/" + obj + "/n.md"
			pack := "docs/design/" + obj + "/context.md"
			rel := doc
			if tc.pack {
				rel = pack
			}
			if err := db.CreateNode(ctx, &model.DesignNode{
				ID: "n", Objective: obj, Layer: model.LayerModule, Status: model.NodeDrafting, DocPath: doc,
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0o755); err != nil {
				t.Fatal(err)
			}
			card := devCard(obj, "c")
			card.Kind = tc.kind
			card.Tier = model.TierT3
			card.Node = "n"
			branch(t, repo, obj, card.ID, rel, "design-body\n")
			wt := filepath.Join(t.TempDir(), "wt")
			git(t, repo, "worktree", "add", "--detach", wt, "cardex/"+obj+"/c/"+card.ID)
			text := "## 结论\nship the design\n"
			if tc.kind == model.KindPlan || tc.kind == model.KindReplan {
				text = "## 新卡\n```json\n[]\n```\n## 结论\nship the design\n"
			}
			if err := os.WriteFile(filepath.Join(wt, ".cardex-result.md"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			exit := 0
			attempt := model.Attempt{
				ID: "a-c", Card: card.ID, Worktree: wt, BaseCommit: rev(t, repo, "main"),
				EndedAt: time.Now().UTC(), Exit: &exit,
			}
			if err := db.CreateAttempt(ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			if len(mustCard(t, db, "c").Claims) != 0 {
				t.Fatal("stored card already had claims")
			}
			tickDefault(t, home)
			got := mustCard(t, db, "c")
			show, err := gitShow(repo, "main:"+rel)
			if tc.stay != "" {
				if got.Status != tc.stay || got.Status == model.CardMergeQueued || got.Status == model.CardMerged {
					t.Fatalf("status %s", got.Status)
				}
				if err == nil && strings.Contains(show, "design-body") {
					t.Fatal("research or spike merged the file")
				}
				return
			}
			if got.Status != model.CardMerged {
				t.Fatalf("status %s", got.Status)
			}
			if err != nil || !strings.Contains(show, "design-body") {
				t.Fatalf("integration file %v %q", err, show)
			}
		})
	}
}

func TestNormalizeStartsThenDisposes(t *testing.T) {
	ctx, home, db, repo, obj := dispositionHome(t)
	seedRoutes(t, db)
	finishHarvest(t, db, home, repo, devCard(obj, "c"), model.Attempt{}, "## 其他\nnope\n", "", false, nil)
	tickDefault(t, home)
	parent := mustCard(t, db, "c")
	if parent.Status == model.CardDone || parent.Status == model.CardMerged {
		t.Fatalf("parent finished before normalize %s", parent.Status)
	}
	norm := normalizeOf(t, db, "c")
	attempts, err := db.ListAttempts(ctx, norm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].PID == 0 || attempts[0].Worktree == "" {
		t.Fatalf("normalize did not start %+v", attempts)
	}
	if err := os.WriteFile(filepath.Join(attempts[0].Worktree, ".cardex-result.md"), []byte("## 结论\nEXTRACTED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exit := 0
	attempts[0].Exit = &exit
	attempts[0].EndedAt = time.Now().UTC()
	if err := db.UpdateAttempt(ctx, &attempts[0]); err != nil {
		t.Fatal(err)
	}
	tickDefault(t, home)
	parent = mustCard(t, db, "c")
	if !strings.Contains(parent.Body, "EXTRACTED") || !readyOrRunning(parent.Status) {
		t.Fatalf("parent %+v", parent)
	}
	if mustCard(t, db, norm.ID).Status != model.CardDone {
		t.Fatalf("normalize %s", mustCard(t, db, norm.ID).Status)
	}
	if mustAttempt(t, db, attempts[0].ID).Outcome != model.OutcomePassed {
		t.Fatal("normalize attempt not consumed")
	}
}

func cardByTitle(t *testing.T, db *store.Store, title string) model.Card {
	t.Helper()
	cards, err := db.ListCards(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		if card.Title == title {
			return card
		}
	}
	t.Fatalf("missing %s", title)
	return model.Card{}
}

func hasDep(card model.Card, id string) bool {
	for _, dep := range card.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

func nodeHasText(t *testing.T, db *store.Store, id, text string) bool {
	t.Helper()
	node, err := db.GetNode(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range node.OpenQuestions {
		if strings.Contains(question.Text, text) {
			return true
		}
	}
	return false
}

func gitShow(repo, rev string) (string, error) {
	cmd := exec.Command("git", "show", rev)
	cmd.Dir = repo
	out, err := cmd.Output()
	return string(out), err
}
