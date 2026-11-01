package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/question"
	"cardex/internal/store"
)

func TestAbsorbDisposedHarvest(t *testing.T) {
	t.Run("inventory", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		cards := cardsOf(t, st, obj.ID)
		if len(cards) != 1 || cards[0].Kind != model.KindResearch || cards[0].Node != "" {
			t.Fatalf("inventory %+v", cards)
		}
		card := cards[0]
		if card.Status == model.CardDone || card.Status == model.CardMerged {
			t.Fatalf("status %s before harvest", card.Status)
		}
		oldDir := t.TempDir()
		writeResult(t, oldDir, "## 事实\n### Q0\n结论：旧盘点\n证据：old.go:1\n把握：高\n")
		makeAttempt(t, st, card.ID, 0, "old-base", oldDir, "")
		gone := goneWorktree(t)
		later := makeAttempt(t, st, card.ID, 1, "inv-base", gone, model.OutcomePassed)
		text := "## 事实\n### Q0\n结论：仓库是 Go 模块\n证据：go.mod:1\n把握：高\n"
		if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, model.OutcomePassed, text); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		var direction *model.DesignNode
		for _, n := range nodesOf(t, st, obj.ID) {
			if n.Layer == model.LayerDirection {
				cp := n
				direction = &cp
			}
		}
		if direction == nil {
			t.Fatal("no direction node")
		}
		if len(direction.OpenQuestions) == 0 || direction.OpenQuestions[0].Answer != "仓库是 Go 模块" {
			t.Fatalf("q0 %+v", direction.OpenQuestions)
		}
		if direction.OpenQuestions[0].Answer == "旧盘点" || direction.OpenQuestions[0].Commit == "old-base" {
			t.Fatalf("used older attempt %+v", direction.OpenQuestions[0])
		}
	})

	cases := []struct {
		name   string
		kind   model.CardKind
		body   string
		result string
		want   string
	}{
		{name: "design", kind: model.KindDesign, body: "任务\n", result: "## 开放问题\n- 收割带来的问题\n", want: "收割带来的问题"},
		{name: "plan", kind: model.KindPlan, body: "任务\n", result: "## 开放问题\n- 拆卡收割的问题\n", want: "拆卡收割的问题"},
		{name: "replan", kind: model.KindReplan, body: "任务\n", result: "## 开放问题\n- 重规划收割的问题\n", want: "重规划收割的问题"},
		{name: "research", kind: model.KindResearch, body: "Q1: 端口是多少\n", result: "## 事实\n### Q1\n结论：来自收割\n证据：a.go:1\n把握：高\n", want: "来自收割"},
		{name: "spike", kind: model.KindSpike, body: "Q1: 缓存能不能撑住\n", result: "## 结论\n能撑住\n", want: "能撑住"},
		{name: "dev", kind: model.KindDev, body: "实现\n", result: "## 设计问题\n接口不一致\n", want: "接口不一致"},
		{name: "review", kind: model.KindReview, body: "复核\n", result: "## 设计问题\n复核发现的问题\n", want: "复核发现的问题"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openLoop(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			if tc.kind == model.KindResearch || tc.kind == model.KindSpike {
				qtype := model.OQFact
				if tc.kind == model.KindSpike {
					qtype = model.OQExperiment
				}
				n := node
				n.OpenQuestions = model.OpenQuestionList{{
					ID: "Q1", Text: "待答", Type: qtype, Status: model.OQOpen, CitedPaths: []string{},
				}}
				if err := st.UpdateNode(t.Context(), &n); err != nil {
					t.Fatal(err)
				}
			}
			card := seedCard(t, st, obj.ID, node.ID, tc.kind, model.CardReady, tc.body)
			if card.Status == model.CardDone || card.Status == model.CardMerged {
				t.Fatalf("status %s before harvest", card.Status)
			}
			oldDir := t.TempDir()
			writeResult(t, oldDir, "## 开放问题\n- 旧文件不该吸收\n## 事实\n### Q1\n结论：旧答案\n证据：old.go:1\n把握：高\n## 结论\n旧结论\n## 设计问题\n旧设计问题\n")
			makeAttempt(t, st, card.ID, 0, "old-base", oldDir, "")
			later := makeAttempt(t, st, card.ID, 1, "new-base", goneWorktree(t), model.OutcomePassed)
			if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, model.OutcomePassed, tc.result); err != nil {
				t.Fatal(err)
			}
			assertHarvestEffect(t, st, obj.ID, node.ID, tc.kind, tc.want)
			switch tc.kind {
			case model.KindDesign, model.KindPlan, model.KindReplan:
				if got := cardStatus(t, st, card.ID); got != model.CardDone {
					t.Fatalf("status %s", got)
				}
			case model.KindDev, model.KindReview:
				if got := cardStatus(t, st, card.ID); got == model.CardDone || got == model.CardMerged {
					t.Fatalf("status %s", got)
				}
			}
		})
	}
}

func TestAbsorbDisposedUnblocksNext(t *testing.T) {
	t.Run("design after research", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		design := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardReady, "任务\n")
		if design.Status == model.CardDone || design.Status == model.CardMerged {
			t.Fatalf("status %s before harvest", design.Status)
		}
		later := makeAttempt(t, st, design.ID, 0, "design-base", goneWorktree(t), model.OutcomePassed)
		if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, model.OutcomePassed, "## 开放问题\n- [fact] 端口是多少\n"); err != nil {
			t.Fatal(err)
		}
		if got := cardStatus(t, st, design.ID); got != model.CardDone {
			t.Fatalf("design status %s", got)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if readyCount(t, st, obj.ID, node.ID, model.KindDesign) != 0 {
			t.Fatal("next design while research is unfinished")
		}
		researchID := readyResearch(t, st, obj.ID, node.ID)
		if cardStatus(t, st, researchID) == model.CardDone || cardStatus(t, st, researchID) == model.CardMerged {
			t.Fatalf("research status %s before harvest", cardStatus(t, st, researchID))
		}
		research := makeAttempt(t, st, researchID, 0, "research-base", goneWorktree(t), model.OutcomePassed)
		fact := "## 事实\n### Q1\n结论：8787\n证据：a.go:1\n把握：高\n"
		if err := AbsorbDisposed(t.Context(), depsOf(st), research.ID, model.OutcomePassed, fact); err != nil {
			t.Fatal(err)
		}
		if got := cardStatus(t, st, researchID); got != model.CardDone {
			t.Fatalf("research status %s", got)
		}
		if q := findQ(nodeOf(t, st, node.ID), "Q1"); q.Status != model.OQAnswered || q.Answer != "8787" {
			t.Fatalf("fact %+v", q)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if readyCount(t, st, obj.ID, node.ID, model.KindDesign) != 1 {
			t.Fatalf("ready design %d", readyCount(t, st, obj.ID, node.ID, model.KindDesign))
		}
		if n := countKind(t, st, obj.ID, node.ID, model.KindDesign); n != 2 {
			t.Fatalf("design cards %d", n)
		}
		next := readyKind(t, st, obj.ID, node.ID, model.KindDesign)
		if next == design.ID || cardStatus(t, st, design.ID) != model.CardDone {
			t.Fatalf("next %s original %s", next, design.ID)
		}
	})

	t.Run("replan promotes", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		design := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardReady, "任务\n")
		if design.Status == model.CardDone || design.Status == model.CardMerged {
			t.Fatalf("status %s before harvest", design.Status)
		}
		frozen := makeAttempt(t, st, design.ID, 0, "design-base", goneWorktree(t), model.OutcomePassed)
		if err := AbsorbDisposed(t.Context(), depsOf(st), frozen.ID, model.OutcomePassed, "## 可冻结\n是\n"); err != nil {
			t.Fatal(err)
		}
		if got := cardStatus(t, st, design.ID); got != model.CardDone {
			t.Fatalf("design status %s", got)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		fq := freezeQs(questionsOf(t, st, obj.ID), node.ID)
		if len(fq) != 1 {
			t.Fatal("missing freeze question")
		}
		if _, err := question.New(st, nil).Answer(t.Context(), fq[0].ID, fq[0].Options[0]); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
			t.Fatal("module did not freeze")
		}
		report := seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardReady, "实现功能\n")
		if report.Status == model.CardDone || report.Status == model.CardMerged {
			t.Fatalf("status %s before harvest", report.Status)
		}
		dev := makeAttempt(t, st, report.ID, 0, "dev-base", goneWorktree(t), model.OutcomePassed)
		if err := AbsorbDisposed(t.Context(), depsOf(st), dev.ID, model.OutcomePassed, "## 设计问题\n接口不一致\n"); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		reopen := reopenQ(t, st, obj.ID, node.ID)
		if _, err := question.New(st, nil).Answer(t.Context(), reopen.ID, reopen.Options[0]); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeReopened {
			t.Fatal("not reopened")
		}
		replanID := readyKind(t, st, obj.ID, node.ID, model.KindReplan)
		if cardStatus(t, st, replanID) == model.CardDone || cardStatus(t, st, replanID) == model.CardMerged {
			t.Fatalf("status %s before harvest", cardStatus(t, st, replanID))
		}
		later := makeAttempt(t, st, replanID, 0, "replan-base", goneWorktree(t), model.OutcomePassed)
		if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, model.OutcomePassed, "## 可冻结\n是\n"); err != nil {
			t.Fatal(err)
		}
		if got := cardStatus(t, st, replanID); got != model.CardDone {
			t.Fatalf("replan status %s", got)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFreezable {
			t.Fatalf("status %s", nodeOf(t, st, node.ID).Status)
		}
	})
}

func TestTerminalCurrentAttemptStillAbsorbed(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- 终态文件里的问题\n")
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if !questionText(nodeOf(t, st, node.ID), "终态文件里的问题") {
		t.Fatalf("questions %+v", nodeOf(t, st, node.ID).OpenQuestions)
	}
}

func TestAbsorbBindsDisposedAttempt(t *testing.T) {
	t.Run("research missing ignores older file", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		n := node
		n.OpenQuestions = model.OpenQuestionList{{
			ID: "Q1", Text: "端口是多少", Type: model.OQFact, Status: model.OQResearching, CitedPaths: []string{},
		}}
		if err := st.UpdateNode(t.Context(), &n); err != nil {
			t.Fatal(err)
		}
		card := seedCard(t, st, obj.ID, node.ID, model.KindResearch, model.CardReady, "Q1: 端口是多少\n")
		oldDir := t.TempDir()
		writeResult(t, oldDir, "## 事实\n### Q1\n结论：旧答案\n证据：old.go:1\n把握：高\n")
		makeAttempt(t, st, card.ID, 0, "old-base", oldDir, model.OutcomePassed)
		later := makeAttempt(t, st, card.ID, 1, "new-base", goneWorktree(t), "")
		if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, model.OutcomePassed, ""); err != nil {
			t.Fatal(err)
		}
		q := findQ(nodeOf(t, st, node.ID), "Q1")
		if q.Status != model.OQUnanswered || q.Answer == "旧答案" || q.Commit == "old-base" || q.Commit != "new-base" {
			t.Fatalf("fact %+v", q)
		}
	})

	t.Run("design missing becomes normalize", func(t *testing.T) {
		st, obj, node, card := designWithOlderFile(t)
		later := makeAttempt(t, st, card.ID, 1, "new-base", goneWorktree(t), "")
		if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, model.OutcomePassed, ""); err != nil {
			t.Fatal(err)
		}
		if questionText(nodeOf(t, st, node.ID), "旧问题不该出现") {
			t.Fatal("absorbed the older design file")
		}
		if norm := normalizeOf(t, st, obj.ID, card.ID); norm == nil {
			t.Fatal("missing normalize card")
		}
		if got := cardStatus(t, st, card.ID); got == model.CardNeedsInput || got == model.CardDone {
			t.Fatalf("status %s", got)
		}
	})

	t.Run("ready normalize waits", func(t *testing.T) {
		st, obj, node, card := designWithOlderFile(t)
		if err := st.CreateCard(t.Context(), &model.Card{
			Objective: obj.ID, Node: node.ID, Kind: model.KindNormalize, Title: "整理",
			Body: "整理\n", Tier: model.TierT1, Status: model.CardReady, Deps: []string{card.ID},
		}); err != nil {
			t.Fatal(err)
		}
		later := makeAttempt(t, st, card.ID, 1, "new-base", goneWorktree(t), model.OutcomeResultUnparsable)
		text := "## 开放问题\n- 解析失败的问题\n"
		if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, model.OutcomeResultUnparsable, text); err != nil {
			t.Fatal(err)
		}
		n := nodeOf(t, st, node.ID)
		if questionText(n, "旧问题不该出现") || questionText(n, "解析失败的问题") {
			t.Fatalf("absorbed unparsable design %+v", n.OpenQuestions)
		}
		if got := cardStatus(t, st, card.ID); got == model.CardNeedsInput || got == model.CardDone {
			t.Fatalf("status %s", got)
		}
		if hasAbsorbed(t, st, card.ID) {
			t.Fatal("consumed while normalize is still ready")
		}
	})

	negatives := []struct {
		name    string
		status  model.CardStatus
		outcome model.Outcome
		viaStep bool
	}{
		{name: "failed", status: model.CardFailed, viaStep: true},
		{name: "canceled", status: model.CardCanceled, viaStep: true},
		{name: "violation", status: model.CardReady, outcome: model.OutcomeViolation},
	}
	for _, tc := range negatives {
		t.Run(tc.name, func(t *testing.T) {
			st, _, node, _, review := detailReview(t)
			rev, err := st.GetCard(t.Context(), review)
			if err != nil {
				t.Fatal(err)
			}
			rev.Status = tc.status
			if err := st.UpdateCard(t.Context(), &rev); err != nil {
				t.Fatal(err)
			}
			oldDir := t.TempDir()
			writeResult(t, oldDir, "## 复核结论\npass\n")
			makeAttempt(t, st, review, 0, "old-base", oldDir, model.OutcomePassed)
			if tc.viaStep {
				fileDir := t.TempDir()
				writeResult(t, fileDir, "## 复核结论\npass\n")
				makeAttempt(t, st, review, 1, "new-base", fileDir, tc.outcome)
				if err := loopStep(t, depsOf(st), rev.Objective); err != nil {
					t.Fatal(err)
				}
			} else {
				later := makeAttempt(t, st, review, 1, "new-base", goneWorktree(t), tc.outcome)
				if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, tc.outcome, "## 复核结论\npass\n"); err != nil {
					t.Fatal(err)
				}
				if err := loopStep(t, depsOf(st), rev.Objective); err != nil {
					t.Fatal(err)
				}
			}
			if hasReviewPass(t, st, review) {
				t.Fatal("recorded a freeze pass")
			}
			if nodeOf(t, st, node.ID).Status == model.NodeFrozen {
				t.Fatal("node froze")
			}
		})
	}

	t.Run("passed bound review freezes", func(t *testing.T) {
		st, _, node, design, review := detailReview(t)
		got, err := st.GetCard(t.Context(), review)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Deps) == 0 || got.Deps[0] != design {
			t.Fatalf("deps %+v want %s", got.Deps, design)
		}
		later := makeAttempt(t, st, review, 1, "rev-base", goneWorktree(t), model.OutcomePassed)
		if err := AbsorbDisposed(t.Context(), depsOf(st), later.ID, model.OutcomePassed, "## 复核结论\npass\n"); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), got.Objective); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
			t.Fatal("bound review did not freeze")
		}
	})
}

func assertHarvestEffect(t *testing.T, st *store.Store, objective, nodeID string, kind model.CardKind, want string) {
	t.Helper()
	switch kind {
	case model.KindDev, model.KindReview:
		for _, q := range questionsOf(t, st, objective) {
			if q.Kind == model.QuestionReopen && strings.TrimSpace(q.Text) == want {
				return
			}
		}
		t.Fatalf("reopen %q missing in %+v", want, questionsOf(t, st, objective))
	case model.KindResearch:
		q := findQ(nodeOf(t, st, nodeID), "Q1")
		if q.Status != model.OQAnswered || q.Answer != want || q.Commit == "old-base" {
			t.Fatalf("research %+v", q)
		}
	case model.KindSpike:
		q := findQ(nodeOf(t, st, nodeID), "Q1")
		if q.Status != model.OQAnswered || q.Answer != want {
			t.Fatalf("spike %+v", q)
		}
	default:
		if !questionText(nodeOf(t, st, nodeID), want) {
			t.Fatalf("questions %+v", nodeOf(t, st, nodeID).OpenQuestions)
		}
		if questionText(nodeOf(t, st, nodeID), "旧文件不该吸收") {
			t.Fatal("absorbed an older attempt")
		}
	}
}

func designWithOlderFile(t *testing.T) (*store.Store, model.Objective, model.DesignNode, model.Card) {
	t.Helper()
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	card := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardReady, "任务\n")
	oldDir := t.TempDir()
	writeResult(t, oldDir, "## 开放问题\n- 旧问题不该出现\n")
	makeAttempt(t, st, card.ID, 0, "old-base", oldDir, model.OutcomePassed)
	return st, obj, node, card
}

func detailReview(t *testing.T) (*store.Store, model.Objective, model.DesignNode, string, string) {
	t.Helper()
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerDetail)
	design := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
	design.AuthorFamily = "family-a"
	if err := st.UpdateCard(t.Context(), &design); err != nil {
		t.Fatal(err)
	}
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	review := readyKind(t, st, obj.ID, node.ID, model.KindReview)
	if review == "" {
		t.Fatal("missing review")
	}
	return st, obj, node, design.ID, review
}

func makeAttempt(t *testing.T, st *store.Store, cardID string, index int, base, worktree string, outcome model.Outcome) model.Attempt {
	t.Helper()
	a := &model.Attempt{Card: cardID, Index: index, BaseCommit: base, Worktree: worktree, Outcome: outcome}
	if err := st.CreateAttempt(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	return *a
}

func writeResult(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".cardex-result.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func goneWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func questionText(n model.DesignNode, text string) bool {
	for _, q := range n.OpenQuestions {
		if strings.Contains(q.Text, text) {
			return true
		}
	}
	return false
}

func normalizeOf(t *testing.T, st *store.Store, objective, cardID string) *model.Card {
	t.Helper()
	for _, c := range cardsOf(t, st, objective) {
		if c.Kind == model.KindNormalize && len(c.Deps) > 0 && c.Deps[0] == cardID {
			cp := c
			return &cp
		}
	}
	return nil
}

func cardStatus(t *testing.T, st *store.Store, id string) model.CardStatus {
	t.Helper()
	c, err := st.GetCard(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return c.Status
}

func hasReviewPass(t *testing.T, st *store.Store, cardID string) bool {
	t.Helper()
	return hasEventType(t, st, "card:"+cardID, evReviewPass)
}

func hasAbsorbed(t *testing.T, st *store.Store, cardID string) bool {
	t.Helper()
	return hasEventType(t, st, "card:"+cardID, evAbsorbed)
}

func hasEventType(t *testing.T, st *store.Store, entity, typ string) bool {
	t.Helper()
	ev, err := st.ListEvents(t.Context(), entity)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ev {
		if e.Type == typ {
			return true
		}
	}
	return false
}
