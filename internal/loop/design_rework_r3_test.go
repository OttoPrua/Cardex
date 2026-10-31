package loop

import (
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/question"
	"cardex/internal/store"
)

func openRework(t *testing.T) *store.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	st, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func countPlans(t *testing.T, st *store.Store, objective, node string) int {
	t.Helper()
	return countKind(t, st, objective, node, model.KindPlan)
}

func reopenCount(t *testing.T, st *store.Store, objective, node string) int {
	t.Helper()
	n := 0
	for _, q := range questionsOf(t, st, objective) {
		if q.Scope == node && q.Kind == model.QuestionReopen {
			n++
		}
	}
	return n
}

func answerFreeze(t *testing.T, st *store.Store, objective, node string) {
	t.Helper()
	var id string
	for _, q := range questionsOf(t, st, objective) {
		if q.Scope == node && q.Kind == model.QuestionFreeze && strings.TrimSpace(q.Answer) == "" {
			id = q.ID
			break
		}
	}
	if id == "" {
		t.Fatal("missing freeze question")
	}
	if _, err := question.New(st, nil).Answer(t.Context(), id, "同意"); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalResearchDoesNotDispatchAnotherPlan(t *testing.T) {
	const result = "## 事实\n### Q1\n结论：8787\n证据：a.go:1\n把握：高\n"
	statuses := []model.CardStatus{
		model.CardRunning,
		model.CardWaiting,
		model.CardHarvesting,
		model.CardReviewing,
		model.CardMergeQueued,
		model.CardReady,
		model.CardDone,
		model.CardMerged,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			st := openRework(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			research := seedFactResearch(t, st, obj.ID, node.ID, "Q1", "端口")
			absorbText(t, st, research.ID, 0, model.OutcomePassed, result)
			if countPlans(t, st, obj.ID, node.ID) != 0 {
				t.Fatal("plan while the node was still drafting")
			}
			if !hasAbsorbed(t, st, research.ID) || hasEventType(t, st, "card:"+research.ID, evContinued) {
				t.Fatal("drafting research was not left as a card-level absorb")
			}
			node = nodeOf(t, st, node.ID)
			node.Status = model.NodeFreezable
			if err := st.UpdateNode(t.Context(), &node); err != nil {
				t.Fatal(err)
			}
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			if countPlans(t, st, obj.ID, node.ID) != 1 {
				t.Fatalf("freeze created %d plans", countPlans(t, st, obj.ID, node.ID))
			}
			var planID string
			for _, c := range cardsOf(t, st, obj.ID) {
				if c.Node == node.ID && c.Kind == model.KindPlan {
					planID = c.ID
				}
			}
			if err := setCardStatus(t, st, planID, status); err != nil {
				t.Fatal(err)
			}
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			if countPlans(t, st, obj.ID, node.ID) != 1 {
				t.Fatalf("status %s dispatched another plan", status)
			}
			if !hasEventType(t, st, "card:"+research.ID, evContinued) {
				t.Fatal("existing plan did not consume the historical research")
			}
		})
	}

	t.Run("none", func(t *testing.T) {
		st := openRework(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		research := seedFactResearch(t, st, obj.ID, node.ID, "Q1", "端口")
		absorbText(t, st, research.ID, 0, model.OutcomePassed, result)
		node = nodeOf(t, st, node.ID)
		node.Status = model.NodeFrozen
		if err := st.UpdateNode(t.Context(), &node); err != nil {
			t.Fatal(err)
		}
		if countPlans(t, st, obj.ID, node.ID) != 0 {
			t.Fatal("plan before the successor step")
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if countPlans(t, st, obj.ID, node.ID) != 1 {
			t.Fatalf("first plan count %d", countPlans(t, st, obj.ID, node.ID))
		}
	})
}

func TestCurrentAttemptNotSealedByCardAbsorb(t *testing.T) {
	cases := []struct {
		kind  model.CardKind
		entry string
	}{
		{model.KindDesign, "step"},
		{model.KindPlan, "step"},
		{model.KindReplan, "step"},
		{model.KindReview, "step"},
		{model.KindDesign, "harvest"},
		{model.KindPlan, "harvest"},
		{model.KindReplan, "harvest"},
		{model.KindReview, "harvest"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind)+"/"+tc.entry, func(t *testing.T) {
			st := openRework(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			card := seedCard(t, st, obj.ID, node.ID, tc.kind, model.CardDone, "任务\n")
			if err := st.AppendEvent(t.Context(), &model.Event{Entity: "card:" + card.ID, Type: evAbsorbed, Data: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			firstText := retryText(tc.kind, "第一次")
			first := plantAttempt(t, st, card.ID, 0, model.OutcomePassed, firstText)
			if tc.entry == "step" {
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
				assertRetryApplied(t, st, obj.ID, node.ID, tc.kind, "第一次", 1)
				return
			}
			if err := AbsorbDisposed(t.Context(), depsOf(st), first.ID, model.OutcomePassed, firstText); err != nil {
				t.Fatal(err)
			}
			assertRetryApplied(t, st, obj.ID, node.ID, tc.kind, "第一次", 1)
			if err := AbsorbDisposed(t.Context(), depsOf(st), first.ID, model.OutcomePassed, firstText); err != nil {
				t.Fatal(err)
			}
			assertRetryApplied(t, st, obj.ID, node.ID, tc.kind, "第一次", 1)
			secondText := retryText(tc.kind, "第二次")
			second := plantAttempt(t, st, card.ID, 1, model.OutcomePassed, secondText)
			if err := AbsorbDisposed(t.Context(), depsOf(st), second.ID, model.OutcomePassed, secondText); err != nil {
				t.Fatal(err)
			}
			assertRetryApplied(t, st, obj.ID, node.ID, tc.kind, "第二次", 2)
		})
	}
}

func retryText(kind model.CardKind, label string) string {
	if kind == model.KindReview {
		return "## 设计问题\n" + label + "设计问题\n"
	}
	return "## 开放问题\n- " + label + "重试\n## 可冻结\n是\n"
}

func assertRetryApplied(t *testing.T, st *store.Store, objective, node string, kind model.CardKind, label string, n int) {
	t.Helper()
	if kind == model.KindReview {
		if reopenCount(t, st, objective, node) != n {
			t.Fatalf("reopen %d", reopenCount(t, st, objective, node))
		}
		found := false
		for _, q := range questionsOf(t, st, objective) {
			if q.Kind == model.QuestionReopen && strings.TrimSpace(q.Text) == label+"设计问题" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s", label)
		}
		return
	}
	if !questionText(nodeOf(t, st, node), label+"重试") {
		t.Fatalf("missing %s questions %+v", label, nodeOf(t, st, node).OpenQuestions)
	}
	if got := eventCount(t, st, "node:"+node, evFreezable); got != n {
		t.Fatalf("freezable events %d", got)
	}
}

func TestFreezeWaitsForMergedDesign(t *testing.T) {
	const proposal = "## 可冻结\n是\n"
	cases := []struct {
		name   string
		layer  model.Layer
		freeze model.FreezeMode
		claim  string
		status model.CardStatus
		arm    string
	}{
		{name: "fast-module-doc", layer: model.LayerModule, freeze: model.FreezeFast, claim: "doc", status: model.CardReady, arm: "now"},
		{name: "fast-module-pack", layer: model.LayerModule, freeze: model.FreezeFast, claim: "pack", status: model.CardReady, arm: "now"},
		{name: "fast-detail-doc", layer: model.LayerDetail, freeze: model.FreezeFast, claim: "doc", status: model.CardReady, arm: "now"},
		{name: "fast-module-queued", layer: model.LayerModule, freeze: model.FreezeFast, claim: "doc", status: model.CardMergeQueued, arm: "now"},
		{name: "direction-consent", layer: model.LayerDirection, freeze: model.FreezeFast, claim: "doc", status: model.CardReady, arm: "consent"},
		{name: "module-consent", layer: model.LayerModule, freeze: model.FreezeSafe, claim: "doc", status: model.CardReady, arm: "consent"},
		{name: "detail-review", layer: model.LayerDetail, freeze: model.FreezeSafe, claim: "doc", status: model.CardMergeQueued, arm: "review"},
		{name: "fast-module-nofile", layer: model.LayerModule, freeze: model.FreezeFast, claim: "", status: model.CardReady, arm: "now"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openRework(t)
			obj := seedObjective(t, st, "", model.ModeAuto, tc.freeze)
			node := seedNode(t, st, obj.ID, "", tc.layer)
			node.DocPath = "docs/design/" + obj.ID + "/" + node.ID + ".md"
			if err := st.UpdateNode(t.Context(), &node); err != nil {
				t.Fatal(err)
			}
			card := seedCard(t, st, obj.ID, node.ID, model.KindDesign, tc.status, "任务\n")
			if tc.claim != "" {
				fresh, err := st.GetCard(t.Context(), card.ID)
				if err != nil {
					t.Fatal(err)
				}
				path := node.DocPath
				if tc.claim == "pack" {
					path = "docs/design/" + obj.ID + "/context.md"
				}
				fresh.Claims = []model.Claim{{Path: path}}
				if err := st.UpdateCard(t.Context(), &fresh); err != nil {
					t.Fatal(err)
				}
			}
			absorbText(t, st, card.ID, 0, model.OutcomePassed, proposal)
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			switch tc.arm {
			case "consent":
				answerFreeze(t, st, obj.ID, node.ID)
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
			case "review":
				var reviewID string
				for _, c := range cardsOf(t, st, obj.ID) {
					if c.Kind == model.KindReview && c.Node == node.ID {
						reviewID = c.ID
					}
				}
				if reviewID == "" {
					t.Fatal("missing review")
				}
				if err := setCardStatus(t, st, reviewID, model.CardDone); err != nil {
					t.Fatal(err)
				}
				plantAttempt(t, st, reviewID, 0, model.OutcomePassed, "## 复核结论\npass\n")
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.claim == "" {
				if countPlans(t, st, obj.ID, node.ID) != 1 {
					t.Fatalf("immediate plans %d", countPlans(t, st, obj.ID, node.ID))
				}
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
				if countPlans(t, st, obj.ID, node.ID) != 1 {
					t.Fatal("immediate freeze dispatched a second plan")
				}
				return
			}
			if countPlans(t, st, obj.ID, node.ID) != 0 {
				t.Fatalf("plan while %s was unmerged", tc.claim)
			}
			got := cardStatus(t, st, card.ID)
			if tc.status == model.CardMergeQueued {
				if got != model.CardMergeQueued {
					t.Fatalf("status %s", got)
				}
			} else if got == model.CardDone || got == model.CardMerged {
				t.Fatalf("status %s", got)
			}
			if err := setCardStatus(t, st, card.ID, model.CardMerged); err != nil {
				t.Fatal(err)
			}
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			if countPlans(t, st, obj.ID, node.ID) != 1 {
				t.Fatalf("plans after merge %d", countPlans(t, st, obj.ID, node.ID))
			}
		})
	}
}

func TestNormalizeHarvestRestoresSource(t *testing.T) {
	kinds := []model.CardKind{model.KindDesign, model.KindPlan, model.KindReplan}
	entries := []string{"step", "harvest"}
	failures := []struct {
		name    string
		status  model.CardStatus
		outcome model.Outcome
		text    string
	}{
		{name: "failed", status: model.CardFailed, outcome: model.OutcomeTestsFailed, text: "## 开放问题\n- 失败不该写入\n"},
		{name: "canceled", status: model.CardCanceled, outcome: "", text: "## 开放问题\n- 取消不该写入\n"},
		{name: "unusable", status: model.CardDone, outcome: model.OutcomePassed, text: ""},
	}
	for _, kind := range kinds {
		for _, entry := range entries {
			t.Run(string(kind)+"/pass/"+entry, func(t *testing.T) {
				st, obj, node, source, norm := harvestingNormalize(t, kind, model.CardDone)
				text := "## 开放问题\n- 整理回填\n"
				attempt := plantAttempt(t, st, norm.ID, 0, model.OutcomePassed, text)
				if entry == "step" {
					if err := loopStep(t, depsOf(st), obj.ID); err != nil {
						t.Fatal(err)
					}
				} else if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, text); err != nil {
					t.Fatal(err)
				}
				if !questionText(nodeOf(t, st, node.ID), "整理回填") {
					t.Fatalf("questions %+v", nodeOf(t, st, node.ID).OpenQuestions)
				}
				if cardStatus(t, st, source.ID) != model.CardHarvesting {
					t.Fatalf("source status %s", cardStatus(t, st, source.ID))
				}
				attempts, err := st.ListAttempts(t.Context(), source.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(attempts) != 0 {
					t.Fatal("harvested the source attempt")
				}
			})
			for _, fail := range failures {
				t.Run(string(kind)+"/"+fail.name+"/"+entry, func(t *testing.T) {
					st, obj, node, source, norm := harvestingNormalize(t, kind, fail.status)
					attempt := plantAttempt(t, st, norm.ID, 0, fail.outcome, fail.text)
					if entry == "step" {
						if err := loopStep(t, depsOf(st), obj.ID); err != nil {
							t.Fatal(err)
						}
					} else if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, fail.outcome, fail.text); err != nil {
						t.Fatal(err)
					}
					if questionText(nodeOf(t, st, node.ID), "失败不该写入") || questionText(nodeOf(t, st, node.ID), "取消不该写入") {
						t.Fatal("applied a failed normalize")
					}
					if cardStatus(t, st, source.ID) != model.CardNeedsInput {
						t.Fatalf("source status %s", cardStatus(t, st, source.ID))
					}
					attempts, err := st.ListAttempts(t.Context(), source.ID)
					if err != nil {
						t.Fatal(err)
					}
					if len(attempts) != 0 {
						t.Fatal("harvested the source attempt")
					}
				})
			}
		}
	}
}

func harvestingNormalize(t *testing.T, kind model.CardKind, normStatus model.CardStatus) (*store.Store, model.Objective, model.DesignNode, model.Card, model.Card) {
	t.Helper()
	st := openRework(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	source := seedCard(t, st, obj.ID, node.ID, kind, model.CardHarvesting, "任务\n")
	norm := &model.Card{
		Objective: obj.ID,
		Node:      node.ID,
		Kind:      model.KindNormalize,
		Title:     "整理",
		Body:      "整理 " + source.ID + "\n",
		Tier:      model.TierT1,
		Status:    normStatus,
		Deps:      []string{source.ID},
	}
	if err := st.CreateCard(t.Context(), norm); err != nil {
		t.Fatal(err)
	}
	return st, obj, node, source, *norm
}

func TestTerminalResearchWithoutAnswerContinues(t *testing.T) {
	voidInventory := "## 事实\n### Q0\n结论：不该写入作废盘点\n证据：void.go:1\n把握：高\n"
	voidNode := "## 事实\n### Q1\n结论：不该写入作废事实\n证据：void.go:2\n把握：高\n"
	skipped := []struct {
		name    string
		status  model.CardStatus
		outcome model.Outcome
	}{
		{name: "failed", status: model.CardFailed, outcome: model.OutcomeTestsFailed},
		{name: "canceled", status: model.CardCanceled, outcome: ""},
		{name: "violation", status: model.CardFailed, outcome: model.OutcomeViolation},
		{name: "transport", status: model.CardFailed, outcome: model.OutcomeTransport},
	}
	for _, tc := range skipped {
		t.Run("inventory/"+tc.name, func(t *testing.T) {
			st := openRework(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
			card := seedCard(t, st, obj.ID, "", model.KindResearch, tc.status, "Q0\n盘点仓库结构。\n")
			plantAttempt(t, st, card.ID, 0, tc.outcome, voidInventory)
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			nodes := nodesOf(t, st, obj.ID)
			if len(nodes) != 1 || nodes[0].Layer != model.LayerDirection {
				t.Fatalf("nodes %+v", nodes)
			}
			q := findQ(nodes[0], "Q0")
			if q.Status != model.OQUnanswered || strings.Contains(q.Answer, "不该写入") || strings.Contains(q.Evidence, "不该写入") {
				t.Fatalf("q0 %+v", q)
			}
			if countKind(t, st, obj.ID, nodes[0].ID, model.KindDesign) != 1 {
				t.Fatal("missing direction design")
			}
		})
		t.Run("node/"+tc.name, func(t *testing.T) {
			st := openRework(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			n := node
			n.OpenQuestions = model.OpenQuestionList{{
				ID: "Q1", Text: "端口", Type: model.OQFact, Status: model.OQResearching, CitedPaths: []string{},
			}}
			if err := st.UpdateNode(t.Context(), &n); err != nil {
				t.Fatal(err)
			}
			if err := st.AppendEvent(t.Context(), &model.Event{Entity: "node:" + node.ID, Type: evFreezable, Data: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			card := seedCard(t, st, obj.ID, node.ID, model.KindResearch, tc.status, "Q1: 端口\n")
			plantAttempt(t, st, card.ID, 0, tc.outcome, voidNode)
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			got := nodeOf(t, st, node.ID)
			q := findQ(got, "Q1")
			if q.Status != model.OQUnanswered || strings.Contains(q.Answer, "不该写入") || strings.Contains(q.Evidence, "不该写入") {
				t.Fatalf("fact %+v", q)
			}
			if got.Status == model.NodeDrafting {
				t.Fatalf("researching still blocked freeze: %+v", got.Status)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		status  model.CardStatus
		outcome model.Outcome
	}{
		{name: "failed", status: model.CardFailed, outcome: model.OutcomeTestsFailed},
		{name: "canceled", status: model.CardCanceled, outcome: ""},
	} {
		t.Run("frozen/"+tc.name, func(t *testing.T) {
			st := openRework(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			node.Status = model.NodeFrozen
			node.OpenQuestions = model.OpenQuestionList{{
				ID: "Q1", Text: "端口", Type: model.OQFact, Status: model.OQResearching, CitedPaths: []string{},
			}}
			if err := st.UpdateNode(t.Context(), &node); err != nil {
				t.Fatal(err)
			}
			card := seedCard(t, st, obj.ID, node.ID, model.KindResearch, tc.status, "Q1: 端口\n")
			plantAttempt(t, st, card.ID, 0, tc.outcome, voidNode)
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			q := findQ(nodeOf(t, st, node.ID), "Q1")
			if q.Status != model.OQUnanswered || strings.Contains(q.Answer, "不该写入") {
				t.Fatalf("fact %+v", q)
			}
			if countPlans(t, st, obj.ID, node.ID) != 1 {
				t.Fatalf("plans %d", countPlans(t, st, obj.ID, node.ID))
			}
			for _, c := range cardsOf(t, st, obj.ID) {
				if c.Kind == model.KindPlan && strings.Contains(c.Body, "不该写入") {
					t.Fatal("plan stored the voided result")
				}
			}
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			if countPlans(t, st, obj.ID, node.ID) != 1 {
				t.Fatal("second step dispatched another plan")
			}
		})
	}

	t.Run("inventory/passed", func(t *testing.T) {
		st := openRework(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
		card := seedCard(t, st, obj.ID, "", model.KindResearch, model.CardDone, "Q0\n盘点仓库结构。\n")
		plantAttempt(t, st, card.ID, 0, model.OutcomePassed, "## 事实\n### Q0\n结论：仓库已有 CLI\n证据：main.go:1\n把握：高\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		nodes := nodesOf(t, st, obj.ID)
		if len(nodes) != 1 {
			t.Fatalf("nodes %+v", nodes)
		}
		q := findQ(nodes[0], "Q0")
		if q.Status != model.OQAnswered || q.Answer != "仓库已有 CLI" {
			t.Fatalf("q0 %+v", q)
		}
		if countKind(t, st, obj.ID, nodes[0].ID, model.KindDesign) != 1 {
			t.Fatal("missing direction design")
		}
	})
}
