package loop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestDevStepBreaker(t *testing.T) {
	t.Run("consecutive failures pause one node and open one breaker question", func(t *testing.T) {
		f, ch := breakerWorld(t)
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "alpha")
		f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "beta")
		f.attempt(card, 2, model.OutcomeTestsFailed, ch.ID, "gamma")
		f.step()
		assertBreaker(t, f, "consecutive failures")
		if f.mustCard("dev").Status != model.CardNeedsInput || f.mustCard("dev").ReworkCount != 2 {
			t.Fatalf("dev %+v", f.mustCard("dev"))
		}
		if len(questionsKind(f, model.QuestionCustom)) != 1 {
			t.Fatalf("rework questions %+v", f.questions())
		}
		f.step()
		assertBreakerHeld(t, f)
	})

	t.Run("the same error signature twice pauses one node", func(t *testing.T) {
		f, ch := breakerWorld(t)
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "Boom")
		f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "boom")
		f.step()
		assertBreaker(t, f, "repeated error boom")
		if f.mustCard("dev").Status != model.CardPaused || f.mustCard("dev").ReworkCount != 2 {
			t.Fatalf("dev %+v", f.mustCard("dev"))
		}
		if len(questionsKind(f, model.QuestionCustom)) != 0 {
			t.Fatalf("signature case opened a rework question %+v", f.questions())
		}
		f.step()
		assertBreakerHeld(t, f)
	})

	t.Run("a repair streak pauses the parent node", func(t *testing.T) {
		f, ch := breakerWorld(t)
		f.card(model.Card{ID: "parent", Status: model.CardReady, Channel: ch.ID})
		repair := f.card(model.Card{ID: "fix", Kind: model.KindRepair, Status: model.CardHarvesting, Channel: ch.ID, Deps: []string{"parent"}})
		f.attempt(repair, 0, model.OutcomeTestsFailed, ch.ID, "alpha")
		f.attempt(repair, 1, model.OutcomeTestsFailed, ch.ID, "beta")
		f.step()
		assertBreaker(t, f, "repair streak 2 on parent")
		if f.mustCard("parent").Status != model.CardPaused || f.mustCard("fix").Status != model.CardPaused {
			t.Fatalf("parent %s fix %s", f.mustCard("parent").Status, f.mustCard("fix").Status)
		}
		f.step()
		assertBreakerHeld(t, f)
	})

	t.Run("no merge within the stall window pauses the node", func(t *testing.T) {
		f, ch := breakerWorld(t)
		f.writeConfig(`{"loop":{"breaker_spend_tokens":10}}`)
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.backdate(f.attempt(card, 0, model.OutcomePassed, ch.ID, ""), time.Now().Add(-7*time.Hour), 11)
		f.step()
		assertBreaker(t, f, "no merge within stall window")
		if f.objective().Status != model.ObjectiveActive {
			t.Fatal("stall spend paused the objective")
		}
		if kindCount(f.cards(), model.KindReview) != 1 {
			t.Fatal("passed dev did not create the review before the pause")
		}
		f.step()
		assertBreakerHeld(t, f)
	})

	t.Run("a recent attempt does not stall", func(t *testing.T) {
		f, ch := breakerWorld(t)
		f.writeConfig(`{"loop":{"breaker_spend_tokens":10}}`)
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.backdate(f.attempt(card, 0, model.OutcomePassed, ch.ID, ""), time.Now().Add(-time.Hour), 11)
		f.step()
		assertNoBreaker(t, f)
	})

	t.Run("spend at the stall threshold does not fire", func(t *testing.T) {
		f, ch := breakerWorld(t)
		f.writeConfig(`{"loop":{"breaker_spend_tokens":10}}`)
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.backdate(f.attempt(card, 0, model.OutcomePassed, ch.ID, ""), time.Now().Add(-7*time.Hour), 5)
		f.step()
		assertNoBreaker(t, f)
	})

	t.Run("manual mode does not fire the breaker or create a replan", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.setBudget(0, 1_000_000)
		ch := f.channel("ch-a", "a")
		f.card(model.Card{ID: "sib", Status: model.CardReady, Channel: ch.ID})
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "alpha")
		f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "beta")
		f.attempt(card, 2, model.OutcomeTestsFailed, ch.ID, "gamma")
		f.step()
		if len(questionsKind(f, model.QuestionBreaker)) != 0 || kindCount(f.cards(), model.KindReplan) != 0 {
			t.Fatalf("manual created breaker work %+v", f.questions())
		}
		if f.mustCard("sib").Status != model.CardReady {
			t.Fatalf("manual paused %s", f.mustCard("sib").Status)
		}
	})
}

func TestDevStepBudget(t *testing.T) {
	t.Run("under 80 percent does not remind", func(t *testing.T) {
		f := spendWorld(t, 100, 1000, 79)
		f.step()
		if len(f.events("budget_reminder")) != 0 || len(questionsKind(f, model.QuestionBudget)) != 0 || f.objective().Status != model.ObjectiveActive {
			t.Fatalf("status %s events %d questions %+v", f.objective().Status, len(f.events("budget_reminder")), f.questions())
		}
	})

	t.Run("80 percent reminds without pausing", func(t *testing.T) {
		f := spendWorld(t, 100, 1000, 80)
		f.step()
		if len(f.events("budget_reminder")) != 1 || len(questionsKind(f, model.QuestionBudget)) != 0 || f.objective().Status != model.ObjectiveActive {
			t.Fatalf("status %s events %+v questions %+v", f.objective().Status, f.events("budget_reminder"), f.questions())
		}
		f.step()
		if len(f.events("budget_reminder")) != 1 || f.objective().Status != model.ObjectiveActive {
			t.Fatal("second step reminded again")
		}
	})

	t.Run("hard tokens pause and open one budget question", func(t *testing.T) {
		f := spendWorld(t, 100, 1000, 1000)
		f.step()
		got := questionsKind(f, model.QuestionBudget)
		if f.objective().Status != model.ObjectivePaused || len(got) != 1 || got[0].Scope != f.obj.ID || got[0].Answer != "" {
			t.Fatalf("status %s questions %+v", f.objective().Status, f.questions())
		}
		if len(f.events("budget_reminder")) != 0 {
			t.Fatal("hard stop also sent the reminder")
		}
		f.step()
		if f.objective().Status != model.ObjectivePaused || len(questionsKind(f, model.QuestionBudget)) != 1 {
			t.Fatal("second step changed the hard stop")
		}
	})

	t.Run("unset budget lines do not fire", func(t *testing.T) {
		f := spendWorld(t, 0, 0, 5000)
		f.step()
		if len(f.events("budget_reminder")) != 0 || len(questionsKind(f, model.QuestionBudget)) != 0 || f.objective().Status != model.ObjectiveActive {
			t.Fatalf("unset lines fired status %s", f.objective().Status)
		}
	})

	t.Run("research default is one quarter of design", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.zeroCard("research", model.KindResearch)
		f.zeroCard("design", model.KindDesign)
		f.zeroCard("spike", model.KindSpike)
		f.step()
		research, design, spike := f.mustCard("research").Budget, f.mustCard("design").Budget, f.mustCard("spike").Budget
		if research.HardTokens == 0 || research.HardTokens*4 != design.HardTokens || research.SoftTokens*4 != design.SoftTokens {
			t.Fatalf("research %+v design %+v", research, design)
		}
		if spike.SoftTokens != design.SoftTokens || spike.HardTokens != design.HardTokens {
			t.Fatalf("spike default %+v design %+v", spike, design)
		}
		if design.HardTokens != 20000 || research.HardTokens != 5000 {
			t.Fatalf("defaults design %+v research %+v", design, research)
		}
	})

	t.Run("config replaces kind defaults and warns once", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.writeConfig(`{"widget":true,"loop":{"nope":1,"budgets":{"design":{"soft_tokens":1600,"hard_tokens":400}}}}`)
		f.zeroCard("research", model.KindResearch)
		f.zeroCard("design", model.KindDesign)
		explicit := model.Card{
			ID: "explicit", Objective: f.obj.ID, Node: f.node.ID, Kind: model.KindSpike,
			Title: "explicit", Tier: model.TierT2, Status: model.CardReady,
			Budget: model.Budget{SoftTokens: 7, HardTokens: 9},
		}
		if err := f.db.CreateCard(f.ctx, &explicit); err != nil {
			t.Fatal(err)
		}
		f.step()
		if f.mustCard("design").Budget.HardTokens != 400 || f.mustCard("research").Budget.HardTokens != 100 || f.mustCard("research").Budget.SoftTokens != 400 {
			t.Fatalf("design %+v research %+v", f.mustCard("design").Budget, f.mustCard("research").Budget)
		}
		if f.mustCard("explicit").Budget.SoftTokens != 7 || f.mustCard("explicit").Budget.HardTokens != 9 {
			t.Fatalf("explicit %+v", f.mustCard("explicit").Budget)
		}
		warn := f.events("config_warning")
		if len(warn) != 1 || !strings.Contains(string(warn[0].Data), "widget") || !strings.Contains(string(warn[0].Data), "nope") {
			t.Fatalf("warnings %+v", warn)
		}
		f.step()
		if len(f.events("config_warning")) != 1 {
			t.Fatal("second step warned again")
		}
	})

	t.Run("a research config entry replaces the quarter", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.writeConfig(`{"loop":{"budgets":{"research":{"soft_tokens":4,"hard_tokens":9}}}}`)
		f.zeroCard("research", model.KindResearch)
		f.zeroCard("design", model.KindDesign)
		f.step()
		if f.mustCard("research").Budget.HardTokens != 9 || f.mustCard("research").Budget.SoftTokens != 4 {
			t.Fatalf("research %+v", f.mustCard("research").Budget)
		}
		if f.mustCard("design").Budget.HardTokens != 20000 {
			t.Fatalf("design %+v", f.mustCard("design").Budget)
		}
	})
}

func TestDevStepReplan(t *testing.T) {
	t.Run("a finished plan split creates one replan in auto mode", func(t *testing.T) {
		f := newFx(t, model.ModeAuto)
		ch := f.channel("ch-a", "a")
		dir := writeResult(t, `[{"key":"child","title":"Child","kind":"dev"}]`)
		plan := f.card(model.Card{ID: "plan", Kind: model.KindPlan, Tier: model.TierT3, Channel: ch.ID})
		attempt := f.attempt(plan, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		child := cardByTitle(f, "Child")
		if kindCount(f.cards(), model.KindReplan) != 0 || child.Status != model.CardReady || child.ID == "child" || f.mustCard("plan").Status != model.CardDone {
			t.Fatalf("cards %+v", f.cards())
		}
		child.Status = model.CardMerged
		if err := f.db.UpdateCard(f.ctx, &child); err != nil {
			t.Fatal(err)
		}
		f.step()
		replans := cardsKind(f, model.KindReplan)
		if len(replans) != 1 || replans[0].Status != model.CardReady || !hasDepID(replans[0], "plan") || replans[0].Node != f.node.ID {
			t.Fatalf("replan %+v", replans)
		}
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 1 {
			t.Fatal("second step created another replan")
		}
	})

	t.Run("manual mode does not create a replan for a finished split or a reopened node", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		dir := writeResult(t, `[{"key":"child","title":"Child","kind":"dev"}]`)
		plan := f.card(model.Card{ID: "plan", Kind: model.KindPlan, Tier: model.TierT3, Channel: ch.ID})
		attempt := f.attempt(plan, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		child := cardByTitle(f, "Child")
		child.Status = model.CardMerged
		if err := f.db.UpdateCard(f.ctx, &child); err != nil {
			t.Fatal(err)
		}
		f.setNodeStatus(f.node.ID, model.NodeReopened)
		f.step()
		if kindCount(f.cards(), model.KindReplan) != 0 {
			t.Fatal("manual created a replan")
		}
	})

	t.Run("auto mode creates one replan for a reopened node", func(t *testing.T) {
		f := newFx(t, model.ModeAuto)
		f.setNodeStatus(f.node.ID, model.NodeReopened)
		f.step()
		replans := cardsKind(f, model.KindReplan)
		if len(replans) != 1 || replans[0].Status != model.CardReady || replans[0].Node != f.node.ID || len(replans[0].Deps) != 0 {
			t.Fatalf("replan %+v", replans)
		}
		f.step()
		if len(cardsKind(f, model.KindReplan)) != 1 {
			t.Fatal("second step created another replan")
		}
	})

	t.Run("replan absorption only changes not-yet-started cards on the same node", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		other := f.nodeID("other", model.NodeFrozen)
		f.card(model.Card{ID: "editme", Status: model.CardReady, Title: "old", Channel: ch.ID})
		f.card(model.Card{ID: "hold", Status: model.CardPaused, Title: "old hold", Channel: ch.ID})
		f.card(model.Card{ID: "dropme", Status: model.CardReady, Title: "drop", Channel: ch.ID})
		f.card(model.Card{ID: "wait", Status: model.CardWaiting, Title: "wait", Channel: ch.ID})
		started := f.card(model.Card{ID: "started", Status: model.CardRunning, Title: "keep", Channel: ch.ID})
		f.attempt(started, 0, "", ch.ID, "")
		f.card(model.Card{ID: "foreign", Node: other.ID, Status: model.CardReady, Title: "foreign", Channel: ch.ID})
		body := `[
			{"key":"editme","title":"edited","kind":"dev","body":"new body"},
			{"key":"hold","title":"held","kind":"dev"},
			{"key":"dropme","op":"delete"},
			{"key":"wait","op":"delete"},
			{"key":"started","title":"stolen","kind":"dev"},
			{"key":"foreign","title":"stolen","kind":"dev"},
			{"key":"fresh","title":"Fresh","kind":"spike"}
		]`
		dir := writeResult(t, body)
		replan := f.card(model.Card{ID: "replan", Kind: model.KindReplan, Tier: model.TierT3, Channel: ch.ID})
		attempt := f.attempt(replan, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		if f.mustCard("editme").Title != "edited" || f.mustCard("editme").Body != "new body" {
			t.Fatalf("edit %+v", f.mustCard("editme"))
		}
		if f.mustCard("hold").Title != "held" || f.mustCard("hold").Status != model.CardPaused {
			t.Fatalf("hold %+v", f.mustCard("hold"))
		}
		if _, err := f.db.GetCard(f.ctx, "dropme"); err == nil {
			t.Fatal("ready card was not deleted")
		}
		if _, err := f.db.GetCard(f.ctx, "wait"); err == nil {
			t.Fatal("waiting card was not deleted")
		}
		if f.mustCard("started").Title != "keep" || f.mustCard("started").Status != model.CardRunning {
			t.Fatalf("started %+v", f.mustCard("started"))
		}
		if f.mustCard("foreign").Title != "foreign" || f.mustCard("foreign").Node != other.ID {
			t.Fatalf("foreign %+v", f.mustCard("foreign"))
		}
		fresh := cardByTitle(f, "Fresh")
		if fresh.ID == "fresh" || fresh.Node != f.node.ID || fresh.Kind != model.KindSpike || fresh.Status != model.CardReady {
			t.Fatalf("fresh %+v", fresh)
		}
		if f.mustCard("replan").Status != model.CardDone {
			t.Fatalf("replan %s", f.mustCard("replan").Status)
		}
		before := len(f.cards())
		f.step()
		if len(f.cards()) != before || f.mustCard("editme").Title != "edited" {
			t.Fatal("second step absorbed the replan again")
		}
	})
}

func TestDevStepDone(t *testing.T) {
	t.Run("done_candidate requires frozen nodes, finished cards, and passing acceptance", func(t *testing.T) {
		repo, mainSHA := integrationRepo(t)
		f := readyDone(t, repo)
		f.setNodeStatus(f.node.ID, model.NodeDrafting)
		f.step()
		if len(questionsKind(f, model.QuestionDone)) != 0 || f.objective().Status != model.ObjectiveActive {
			t.Fatal("unfrozen node became a done candidate")
		}

		f = readyDone(t, repo)
		f.card(model.Card{ID: "open", Status: model.CardReady})
		f.step()
		if len(questionsKind(f, model.QuestionDone)) != 0 {
			t.Fatal("a ready card became a done candidate")
		}

		f = readyDone(t, repo)
		f.setCommands([]string{"false"})
		f.step()
		if len(questionsKind(f, model.QuestionDone)) != 0 || f.objective().Status != model.ObjectiveActive {
			t.Fatal("failing acceptance became a done candidate")
		}

		f = readyDone(t, repo)
		if err := f.db.DeleteNode(f.ctx, f.node.ID); err != nil {
			t.Fatal(err)
		}
		f.step()
		if len(questionsKind(f, model.QuestionDone)) != 0 {
			t.Fatal("zero nodes became a done candidate")
		}

		f = readyDone(t, repo)
		obj := f.objective()
		obj.Status = model.ObjectivePaused
		if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
			t.Fatal(err)
		}
		f.step()
		if f.objective().Status != model.ObjectivePaused || len(questionsKind(f, model.QuestionDone)) != 0 {
			t.Fatal("a paused objective became a done candidate")
		}

		if gitSHA(t, repo, "main") != mainSHA {
			t.Fatal("a negative completion case moved main")
		}
	})

	t.Run("approval merges the integration branch locally and never pushes", func(t *testing.T) {
		repo, mainSHA := integrationRepo(t)
		f := readyDone(t, repo)
		f.step()
		if f.objective().Status != model.ObjectiveDoneCandidate {
			t.Fatalf("status %s", f.objective().Status)
		}
		got := questionsKind(f, model.QuestionDone)
		if len(got) != 1 || got[0].Answer != "" || got[0].Scope != f.obj.ID {
			t.Fatalf("questions %+v", got)
		}
		if gitSHA(t, repo, "main") != mainSHA {
			t.Fatal("main moved before approval")
		}
		if _, err := os.Stat(filepath.Join(repo, "marker")); !os.IsNotExist(err) {
			t.Fatal("main already contains the integration marker")
		}
		f.step()
		if gitSHA(t, repo, "main") != mainSHA || len(f.events("merged_local")) != 0 || f.objective().Status != model.ObjectiveDoneCandidate {
			t.Fatal("a second unanswered step merged")
		}

		reject := readyDone(t, repo)
		reject.step()
		q := questionsKind(reject, model.QuestionDone)[0]
		q.Answer = "reject"
		if err := reject.db.UpdateQuestion(reject.ctx, &q); err != nil {
			t.Fatal(err)
		}
		reject.step()
		if gitSHA(t, repo, "main") != mainSHA || reject.objective().Status != model.ObjectiveDoneCandidate || len(reject.events("merged_local")) != 0 {
			t.Fatal("reject merged")
		}

		q = got[0]
		q.Answer = "approve"
		if err := f.db.UpdateQuestion(f.ctx, &q); err != nil {
			t.Fatal(err)
		}
		f.step()
		if f.objective().Status != model.ObjectiveDone || len(f.events("merged_local")) != 1 {
			t.Fatalf("status %s events %+v", f.objective().Status, f.events("merged_local"))
		}
		if gitSHA(t, repo, "main") == mainSHA {
			t.Fatal("approved merge left main unchanged")
		}
		marker, err := os.ReadFile(filepath.Join(repo, "marker"))
		if err != nil || !strings.Contains(string(marker), "ok") {
			t.Fatalf("marker %q %v", marker, err)
		}
		if _, err := os.Stat(filepath.Join(repo, "pushed-by-hook")); !os.IsNotExist(err) {
			t.Fatal("push hook ran")
		}
		if remotes := gitOut(t, repo, "remote"); strings.TrimSpace(remotes) != "" {
			t.Fatalf("remotes %q", remotes)
		}
		f.step()
		if len(f.events("merged_local")) != 1 || f.objective().Status != model.ObjectiveDone {
			t.Fatal("second step merged again")
		}
	})
}

func breakerWorld(t *testing.T) (*fx, model.Channel) {
	t.Helper()
	f := newFx(t, model.ModeAuto)
	f.setBudget(0, 1_000_000)
	ch := f.channel("ch-a", "a")
	f.card(model.Card{ID: "sib", Status: model.CardReady, Channel: ch.ID})
	f.card(model.Card{ID: "busy", Status: model.CardRunning, Channel: ch.ID})
	other := f.nodeID("other", model.NodeFrozen)
	f.card(model.Card{ID: "else", Node: other.ID, Status: model.CardReady, Channel: ch.ID})
	return f, ch
}

func assertBreaker(t *testing.T, f *fx, reason string) {
	t.Helper()
	got := questionsKind(f, model.QuestionBreaker)
	if len(got) != 1 || got[0].Scope != f.node.ID || got[0].Answer != "" || !strings.Contains(got[0].Text, reason) {
		t.Fatalf("breaker %+v", f.questions())
	}
	if len(got[0].Deliveries) != 1 || got[0].Deliveries[0].Adapter != "file" || got[0].Deliveries[0].Destination != f.obj.Origin {
		t.Fatalf("delivery %+v", got[0].Deliveries)
	}
	if f.mustCard("sib").Status != model.CardPaused || f.mustCard("busy").Status != model.CardRunning || f.mustCard("else").Status != model.CardReady {
		t.Fatalf("sib %s busy %s else %s", f.mustCard("sib").Status, f.mustCard("busy").Status, f.mustCard("else").Status)
	}
	replans := cardsKind(f, model.KindReplan)
	if len(replans) != 1 || replans[0].Status != model.CardReady || replans[0].Node != f.node.ID {
		t.Fatalf("replan %+v", replans)
	}
	if f.mustCard("else").Node == f.node.ID {
		t.Fatal("other node card moved")
	}
}

func assertBreakerHeld(t *testing.T, f *fx) {
	t.Helper()
	if len(questionsKind(f, model.QuestionBreaker)) != 1 || len(cardsKind(f, model.KindReplan)) != 1 {
		t.Fatal("second step added a breaker question or a replan")
	}
	if f.mustCard("sib").Status != model.CardPaused || f.mustCard("else").Status != model.CardReady {
		t.Fatal("second step changed node pause")
	}
}

func assertNoBreaker(t *testing.T, f *fx) {
	t.Helper()
	if len(questionsKind(f, model.QuestionBreaker)) != 0 || kindCount(f.cards(), model.KindReplan) != 0 {
		t.Fatalf("unexpected breaker %+v cards %+v", f.questions(), f.cards())
	}
	if f.mustCard("sib").Status != model.CardReady || f.mustCard("else").Status != model.CardReady || f.mustCard("busy").Status != model.CardRunning {
		t.Fatalf("sib %s busy %s else %s", f.mustCard("sib").Status, f.mustCard("busy").Status, f.mustCard("else").Status)
	}
}

func spendWorld(t *testing.T, soft, hard, tokens int64) *fx {
	t.Helper()
	f := newFx(t, model.ModeManual)
	f.setBudget(soft, hard)
	ch := f.channel("ch-a", "a")
	card := f.card(model.Card{ID: "spike", Kind: model.KindSpike, Tier: model.TierT2, Channel: ch.ID})
	f.backdate(f.attempt(card, 0, model.OutcomePassed, ch.ID, ""), time.Now().UTC(), tokens)
	return f
}

func (f *fx) setBudget(soft, hard int64) {
	f.t.Helper()
	obj := f.objective()
	obj.Budget = model.Budget{SoftTokens: soft, HardTokens: hard}
	if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) setCommands(commands []string) {
	f.t.Helper()
	obj := f.objective()
	obj.Acceptance.Commands = commands
	if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) setNodeStatus(id string, status model.NodeStatus) {
	f.t.Helper()
	node, err := f.db.GetNode(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	node.Status = status
	if err := f.db.UpdateNode(f.ctx, &node); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) writeConfig(body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.home, "config.json"), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) zeroCard(id string, kind model.CardKind) {
	f.t.Helper()
	card := model.Card{
		ID: id, Objective: f.obj.ID, Node: f.node.ID, Kind: kind,
		Title: id, Tier: model.DefaultTier(kind), Status: model.CardReady,
	}
	if err := f.db.CreateCard(f.ctx, &card); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) backdate(attempt model.Attempt, at time.Time, tokens int64) model.Attempt {
	f.t.Helper()
	attempt.EndedAt = at.UTC()
	attempt.Usage.Tokens = tokens
	if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
		f.t.Fatal(err)
	}
	return attempt
}

func (f *fx) pointRepo(repo string) {
	f.t.Helper()
	project, err := f.db.GetProject(f.ctx, f.obj.Project)
	if err != nil {
		f.t.Fatal(err)
	}
	project.RepoPath = repo
	if err := f.db.UpdateProject(f.ctx, &project); err != nil {
		f.t.Fatal(err)
	}
}

func questionsKind(f *fx, kind model.QuestionKind) []model.Question {
	f.t.Helper()
	var out []model.Question
	for _, question := range f.questions() {
		if question.Kind == kind {
			out = append(out, question)
		}
	}
	return out
}

func cardsKind(f *fx, kind model.CardKind) []model.Card {
	f.t.Helper()
	var out []model.Card
	for _, card := range f.cards() {
		if card.Kind == kind {
			out = append(out, card)
		}
	}
	return out
}

func readyDone(t *testing.T, repo string) *fx {
	t.Helper()
	f := newFx(t, model.ModeManual)
	f.pointRepo(repo)
	f.setCommands([]string{"test -f marker"})
	f.card(model.Card{ID: "finished", Status: model.CardMerged})
	return f
}

func integrationRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README")
	gitRun(t, repo, "commit", "-m", "base")
	gitRun(t, repo, "checkout", "-b", "integration")
	if err := os.WriteFile(filepath.Join(repo, "marker"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "marker")
	gitRun(t, repo, "commit", "-m", "marker")
	gitRun(t, repo, "checkout", "main")
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	script := "#!/bin/sh\nroot=$(cd \"$(dirname \"$0\")/../..\" && pwd)\nprintf 'pushed\\n' > \"$root/pushed-by-hook\"\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, gitSHA(t, repo, "main")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitOut(t, dir, args...)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-C", dir,
		"-c", "user.name=cardex",
		"-c", "user.email=cardex@cardex.invalid",
		"-c", "commit.gpgsign=false",
	}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitSHA(t *testing.T, dir, rev string) string {
	t.Helper()
	return strings.TrimSpace(gitOut(t, dir, "rev-parse", rev))
}
