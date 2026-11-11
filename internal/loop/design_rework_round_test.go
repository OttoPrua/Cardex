package loop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestFrozenResearchSuccessorPlan(t *testing.T) {
	const fact = "端口是 8787"
	result := "## 事实\n### Q1\n结论：8787\n证据：a.go:1\n把握：高\n"
	for _, entry := range []string{"harvest", "step", "harvest-then-step"} {
		t.Run(entry, func(t *testing.T) {
			st, obj, node, research := frozenFactResearch(t, fact)
			switch entry {
			case "harvest":
				absorbText(t, st, research.ID, 0, model.OutcomePassed, result)
			case "step":
				finishCard(t, st, research.ID, result)
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
			default:
				absorbText(t, st, research.ID, 0, model.OutcomePassed, result)
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
			}
			if entry != "step" && cardStatus(t, st, research.ID) != model.CardDone {
				t.Fatalf("stored status %s", cardStatus(t, st, research.ID))
			}
			plans := readyCards(t, st, obj.ID, node.ID, model.KindPlan)
			if len(plans) != 1 || summaryAnswers(t, plans[0].Body)["Q1"] != "8787" {
				t.Fatalf("plans %+v", plans)
			}
			if !hasEventType(t, st, "card:"+research.ID, evContinued) {
				t.Fatal("research completion was not consumed after the plan existed")
			}
		})
	}

	t.Run("consume only after the plan exists", func(t *testing.T) {
		st, obj, node := frozenNode(t)
		first := seedFactResearch(t, st, obj.ID, node.ID, "Q1", "第一件事实")
		second := seedFactResearch(t, st, obj.ID, node.ID, "Q2", "第二件事实")
		absorbText(t, st, first.ID, 0, model.OutcomePassed, "## 事实\n### Q1\n结论：甲\n证据：a.go:1\n把握：高\n")
		if readyCards(t, st, obj.ID, node.ID, model.KindPlan) != nil {
			t.Fatal("plan before every research card finished")
		}
		if hasEventType(t, st, "card:"+first.ID, evContinued) || hasEventType(t, st, "card:"+second.ID, evContinued) {
			t.Fatal("consumed before the successor plan existed")
		}
		finishCard(t, st, second.ID, "## 事实\n### Q2\n结论：乙\n证据：b.go:1\n把握：高\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		plans := readyCards(t, st, obj.ID, node.ID, model.KindPlan)
		answers := map[string]string{}
		if len(plans) == 1 {
			answers = summaryAnswers(t, plans[0].Body)
		}
		if len(plans) != 1 || answers["Q1"] != "甲" || answers["Q2"] != "乙" {
			t.Fatalf("plans %+v", plans)
		}
		if !hasEventType(t, st, "card:"+first.ID, evContinued) || !hasEventType(t, st, "card:"+second.ID, evContinued) {
			t.Fatal("completion was not consumed once the plan existed")
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if len(readyCards(t, st, obj.ID, node.ID, model.KindPlan)) != 1 {
			t.Fatal("step created a second plan")
		}
	})

	t.Run("step waits until every done research is absorbed", func(t *testing.T) {
		st, obj, node := frozenNode(t)
		first := seedFactResearch(t, st, obj.ID, node.ID, "Q1", "第一件事实")
		second := seedFactResearch(t, st, obj.ID, node.ID, "Q2", "第二件事实")
		finishCard(t, st, first.ID, "## 事实\n### Q1\n结论：甲\n证据：a.go:1\n把握：高\n")
		finishCard(t, st, second.ID, "## 事实\n### Q2\n结论：乙\n证据：b.go:1\n把握：高\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		plans := readyCards(t, st, obj.ID, node.ID, model.KindPlan)
		answers := map[string]string{}
		if len(plans) == 1 {
			answers = summaryAnswers(t, plans[0].Body)
		}
		if len(plans) != 1 || answers["Q1"] != "甲" || answers["Q2"] != "乙" {
			t.Fatalf("plans %+v", plans)
		}
	})

	for _, tc := range []struct {
		name    string
		status  model.CardStatus
		outcome model.Outcome
	}{
		{name: "canceled", status: model.CardCanceled},
		{name: "failed", status: model.CardFailed, outcome: model.OutcomeTestsFailed},
	} {
		t.Run("void harvest finishes research/"+tc.name, func(t *testing.T) {
			st, obj, node := frozenNode(t)
			first := seedFactResearch(t, st, obj.ID, node.ID, "Q1", "第一件事实")
			second := seedFactResearch(t, st, obj.ID, node.ID, "Q2", "第二件事实")
			absorbText(t, st, first.ID, 0, model.OutcomePassed, "## 事实\n### Q1\n结论：甲\n证据：a.go:1\n把握：高\n")
			if readyCards(t, st, obj.ID, node.ID, model.KindPlan) != nil {
				t.Fatal("plan before the last research attempt was harvested")
			}
			if err := setCardStatus(t, st, second.ID, tc.status); err != nil {
				t.Fatal(err)
			}
			absorbText(t, st, second.ID, 0, tc.outcome, "## 事实\n### Q2\n结论：不该写入\n证据：b.go:1\n把握：高\n")
			plans := readyCards(t, st, obj.ID, node.ID, model.KindPlan)
			answers := map[string]string{}
			if len(plans) == 1 {
				answers = summaryAnswers(t, plans[0].Body)
			}
			if len(plans) != 1 || answers["Q1"] != "甲" || answers["Q2"] == "不该写入" {
				t.Fatalf("plans %+v", plans)
			}
			if findQ(nodeOf(t, st, node.ID), "Q2").Answer != "" {
				t.Fatal("void attempt wrote a fact answer")
			}
			if !hasEventType(t, st, "card:"+first.ID, evContinued) || !hasEventType(t, st, "card:"+second.ID, evContinued) {
				t.Fatal("research completion was not consumed after the plan existed")
			}
		})
	}
}

func TestAttemptScopedConsumption(t *testing.T) {
	voids := []struct {
		name    string
		outcome model.Outcome
		status  model.CardStatus
	}{
		{name: "transport", outcome: model.OutcomeTransport, status: model.CardReady},
		{name: "failed", outcome: model.OutcomeTestsFailed, status: model.CardReady},
		{name: "canceled", outcome: "", status: model.CardCanceled},
		{name: "violation", outcome: model.OutcomeViolation, status: model.CardReady},
	}
	kinds := []model.CardKind{model.KindResearch, model.KindSpike, model.KindDesign, model.KindReview}
	for _, kind := range kinds {
		for _, voided := range voids {
			for _, entry := range []string{"harvest", "step"} {
				t.Run(string(kind)+"/"+voided.name+"/"+entry, func(t *testing.T) {
					st, obj, node, card := consumptionCard(t, kind)
					status := voided.status
					if entry == "step" && !terminalCard(status) {
						status = model.CardFailed
					}
					if err := setCardStatus(t, st, card.ID, status); err != nil {
						t.Fatal(err)
					}
					voidText := successText(kind)
					first := plantAttempt(t, st, card.ID, 0, voided.outcome, voidText)
					if entry == "step" {
						if err := loopStep(t, depsOf(st), obj.ID); err != nil {
							t.Fatal(err)
						}
					} else if err := AbsorbDisposed(t.Context(), depsOf(st), first.ID, voided.outcome, voidText); err != nil {
						t.Fatal(err)
					}
					assertNoSuccess(t, st, obj.ID, node.ID, card.ID, kind)
					passed := plantAttempt(t, st, card.ID, 1, model.OutcomePassed, voidText)
					if entry == "step" {
						if err := loopStep(t, depsOf(st), obj.ID); err != nil {
							t.Fatal(err)
						}
					} else if err := AbsorbDisposed(t.Context(), depsOf(st), passed.ID, model.OutcomePassed, voidText); err != nil {
						t.Fatal(err)
					}
					assertSuccess(t, st, obj.ID, node.ID, kind)
					mutateSuccess(t, st, node.ID, kind)
					if entry == "step" {
						if err := loopStep(t, depsOf(st), obj.ID); err != nil {
							t.Fatal(err)
						}
					} else if err := AbsorbDisposed(t.Context(), depsOf(st), passed.ID, model.OutcomePassed, voidText); err != nil {
						t.Fatal(err)
					}
					assertNotReapplied(t, st, obj.ID, node.ID, kind)
				})
			}
		}
	}
}

func TestParsedOutputWaitsForMerge(t *testing.T) {
	for _, kind := range []model.CardKind{model.KindDesign, model.KindPlan, model.KindReplan} {
		t.Run(string(kind)+"/doc", func(t *testing.T) {
			st, obj, node, card := fileCard(t, kind, "doc")
			absorbText(t, st, card.ID, 0, model.OutcomePassed, "## 开放问题\n- 文档还在卡分支\n")
			if got := cardStatus(t, st, card.ID); got == model.CardDone || got == model.CardMerged {
				t.Fatalf("status %s", got)
			}
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			if countKind(t, st, obj.ID, node.ID, kind) != 1 {
				t.Fatal("started the next card while the document was unmerged")
			}
		})
		t.Run(string(kind)+"/context", func(t *testing.T) {
			st, obj, node, card := fileCard(t, kind, "pack")
			absorbText(t, st, card.ID, 0, model.OutcomePassed, "## 开放问题\n- 上下文包还在卡分支\n")
			if got := cardStatus(t, st, card.ID); got == model.CardDone {
				t.Fatalf("status %s", got)
			}
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			if countKind(t, st, obj.ID, node.ID, kind) != 1 {
				t.Fatal("started the next card while the context pack was unmerged")
			}
		})
		t.Run(string(kind)+"/merge-queued", func(t *testing.T) {
			st, _, _, card := fileCard(t, kind, "doc")
			if err := setCardStatus(t, st, card.ID, model.CardMergeQueued); err != nil {
				t.Fatal(err)
			}
			absorbText(t, st, card.ID, 0, model.OutcomePassed, "## 开放问题\n- 已入队\n")
			if got := cardStatus(t, st, card.ID); got != model.CardMergeQueued {
				t.Fatalf("status %s", got)
			}
		})
		t.Run(string(kind)+"/merged", func(t *testing.T) {
			st, _, _, card := fileCard(t, kind, "")
			if err := setCardStatus(t, st, card.ID, model.CardMerged); err != nil {
				t.Fatal(err)
			}
			absorbText(t, st, card.ID, 0, model.OutcomePassed, "## 开放问题\n- 已合入\n")
			if got := cardStatus(t, st, card.ID); got != model.CardMerged {
				t.Fatalf("status %s", got)
			}
		})
		t.Run(string(kind)+"/no-file", func(t *testing.T) {
			st, _, _, card := fileCard(t, kind, "")
			absorbText(t, st, card.ID, 0, model.OutcomePassed, "## 开放问题\n- 没有文件改动\n")
			if got := cardStatus(t, st, card.ID); got != model.CardDone {
				t.Fatalf("status %s", got)
			}
		})
		t.Run(string(kind)+"/done-but-unmerged", func(t *testing.T) {
			st, obj, node, card := fileCard(t, kind, "doc")
			switch kind {
			case model.KindPlan:
				// A frozen node receives its next plan only after research is finished.
				node.Status = model.NodeFrozen
				if err := st.UpdateNode(t.Context(), &node); err != nil {
					t.Fatal(err)
				}
				seedCard(t, st, obj.ID, node.ID, model.KindResearch, model.CardDone, "Q1: 已研究\n")
			case model.KindReplan:
				node.Status = model.NodeReopened
				if err := st.UpdateNode(t.Context(), &node); err != nil {
					t.Fatal(err)
				}
			}
			if err := setCardStatus(t, st, card.ID, model.CardDone); err != nil {
				t.Fatal(err)
			}
			plantAttempt(t, st, card.ID, 0, model.OutcomePassed, "## 开放问题\n- 文件还没进集成分支\n")
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			if countKind(t, st, obj.ID, node.ID, kind) != 1 {
				t.Fatal("step started the next card from an unmerged file")
			}
			if err := setCardStatus(t, st, card.ID, model.CardMerged); err != nil {
				t.Fatal(err)
			}
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			if countKind(t, st, obj.ID, node.ID, kind) < 2 {
				t.Fatal("merged file did not unblock the next card")
			}
		})
	}
}

func TestNormalizeRecovery(t *testing.T) {
	cases := []struct {
		name    string
		outcome model.Outcome
		text    string
	}{
		{name: "empty", outcome: model.OutcomePassed, text: ""},
		{name: "unparsable", outcome: model.OutcomeResultUnparsable, text: "## 开放问题\n- 原文不该吸收\n"},
	}
	for _, tc := range cases {
		for _, entry := range []string{"harvest", "step"} {
			t.Run(tc.name+"/"+entry, func(t *testing.T) {
				st, obj, node, card := designWithOlderFile(t)
				if entry == "step" {
					if err := setCardStatus(t, st, card.ID, model.CardDone); err != nil {
						t.Fatal(err)
					}
					plantAttempt(t, st, card.ID, 1, tc.outcome, tc.text)
					if err := loopStep(t, depsOf(st), obj.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					attempt := plantAttempt(t, st, card.ID, 1, tc.outcome, tc.text)
					if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, tc.outcome, tc.text); err != nil {
						t.Fatal(err)
					}
				}
				if normalizeOf(t, st, obj.ID, card.ID) == nil || cardStatus(t, st, card.ID) == model.CardNeedsInput || hasAbsorbed(t, st, card.ID) {
					t.Fatalf("status %s normalize %+v absorbed %v", cardStatus(t, st, card.ID), normalizeOf(t, st, obj.ID, card.ID), hasAbsorbed(t, st, card.ID))
				}
				if questionText(nodeOf(t, st, node.ID), "原文不该吸收") || questionText(nodeOf(t, st, node.ID), "旧问题不该出现") {
					t.Fatal("absorbed the unusable result")
				}
				if entry == "step" {
					if err := loopStep(t, depsOf(st), obj.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					attempt, err := st.GetAttempt(t.Context(), currentAttemptID(t, st, card.ID))
					if err != nil {
						t.Fatal(err)
					}
					if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, tc.outcome, tc.text); err != nil {
						t.Fatal(err)
					}
				}
				norms := normalizeCards(t, st, obj.ID, card.ID)
				if len(norms) != 1 || cardStatus(t, st, card.ID) == model.CardNeedsInput || hasAbsorbed(t, st, card.ID) {
					t.Fatalf("repeat status %s norms %d absorbed %v", cardStatus(t, st, card.ID), len(norms), hasAbsorbed(t, st, card.ID))
				}
			})
		}
	}

	for _, entry := range []string{"harvest", "step"} {
		t.Run("failed/"+entry, func(t *testing.T) {
			st, obj, _, card := designWithOlderFile(t)
			attempt := plantAttempt(t, st, card.ID, 1, model.OutcomeResultUnparsable, "")
			if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomeResultUnparsable, ""); err != nil {
				t.Fatal(err)
			}
			norm := normalizeOf(t, st, obj.ID, card.ID)
			if norm == nil {
				t.Fatal("missing normalize")
			}
			if err := setCardStatus(t, st, norm.ID, model.CardFailed); err != nil {
				t.Fatal(err)
			}
			if entry == "step" {
				if err := setCardStatus(t, st, card.ID, model.CardDone); err != nil {
					t.Fatal(err)
				}
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
			} else if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomeResultUnparsable, ""); err != nil {
				t.Fatal(err)
			}
			if cardStatus(t, st, card.ID) != model.CardNeedsInput || !hasAbsorbed(t, st, card.ID) {
				t.Fatalf("status %s absorbed %v", cardStatus(t, st, card.ID), hasAbsorbed(t, st, card.ID))
			}
		})
		t.Run("extraction/"+entry, func(t *testing.T) {
			st, obj, node, card := designWithOlderFile(t)
			attempt := plantAttempt(t, st, card.ID, 1, model.OutcomePassed, "")
			if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, ""); err != nil {
				t.Fatal(err)
			}
			norm := normalizeOf(t, st, obj.ID, card.ID)
			if norm == nil {
				t.Fatal("missing normalize")
			}
			finishCard(t, st, norm.ID, "## 开放问题\n- 整理出来的问题\n")
			if entry == "step" {
				if err := setCardStatus(t, st, card.ID, model.CardDone); err != nil {
					t.Fatal(err)
				}
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
			} else if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, ""); err != nil {
				t.Fatal(err)
			}
			if !questionText(nodeOf(t, st, node.ID), "整理出来的问题") {
				t.Fatalf("questions %+v", nodeOf(t, st, node.ID).OpenQuestions)
			}
			if questionText(nodeOf(t, st, node.ID), "旧问题不该出现") {
				t.Fatal("absorbed the older file")
			}
			if cardStatus(t, st, card.ID) != model.CardDone {
				t.Fatalf("status %s", cardStatus(t, st, card.ID))
			}
		})
	}
}

func TestManualHarvestRefusesDesignPhase(t *testing.T) {
	text := "## 开放问题\n- [experiment] 要做了才知道\n## 子节点\n- [module] 子模块\n- [detail] 细节\n## 可冻结\n是\n"
	for _, entry := range []string{"harvest", "step"} {
		t.Run(entry, func(t *testing.T) {
			st := openLoop(t)
			obj := seedObjective(t, st, "", model.ModeManual, model.FreezeFast)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			card := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardReady, "任务\n")
			if entry == "step" {
				if err := setCardStatus(t, st, card.ID, model.CardDone); err != nil {
					t.Fatal(err)
				}
				plantAttempt(t, st, card.ID, 0, model.OutcomePassed, text)
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				absorbText(t, st, card.ID, 0, model.OutcomePassed, text)
				if err := loopStep(t, depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
			}
			if nodes := nodesOf(t, st, obj.ID); len(nodes) != 1 {
				t.Fatalf("nodes %+v", nodes)
			}
			for _, kind := range []model.CardKind{model.KindDesign, model.KindPlan, model.KindReplan, model.KindResearch, model.KindSpike} {
				n := countKind(t, st, obj.ID, "", kind)
				if kind == model.KindDesign {
					n = countKind(t, st, obj.ID, node.ID, kind)
				}
				want := 0
				if kind == model.KindDesign {
					want = 1
				}
				if n != want {
					t.Fatalf("%s cards %d", kind, n)
				}
			}
		})
	}

	t.Run("review problem still recorded", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeManual, model.FreezeFast)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		card := seedCard(t, st, obj.ID, node.ID, model.KindReview, model.CardReady, "复核\n")
		absorbText(t, st, card.ID, 0, model.OutcomePassed, "## 设计问题\n接口不一致\n")
		found := false
		for _, q := range questionsOf(t, st, obj.ID) {
			if q.Kind == model.QuestionReopen && strings.TrimSpace(q.Text) == "接口不一致" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing reopen question")
		}
		if countKind(t, st, obj.ID, node.ID, model.KindReplan) != 0 {
			t.Fatal("manual created a replan")
		}
	})

	t.Run("normalize still created", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeManual, model.FreezeFast)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		card := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardReady, "任务\n")
		absorbText(t, st, card.ID, 0, model.OutcomeResultUnparsable, "")
		if normalizeOf(t, st, obj.ID, card.ID) == nil {
			t.Fatal("missing normalize")
		}
	})
}

func frozenFactResearch(t *testing.T, fact string) (*store.Store, model.Objective, model.DesignNode, model.Card) {
	t.Helper()
	st, obj, node := frozenNode(t)
	return st, obj, node, seedFactResearch(t, st, obj.ID, node.ID, "Q1", fact)
}

func frozenNode(t *testing.T) (*store.Store, model.Objective, model.DesignNode) {
	t.Helper()
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	node.Status = model.NodeFrozen
	if err := st.UpdateNode(t.Context(), &node); err != nil {
		t.Fatal(err)
	}
	return st, obj, node
}

func seedFactResearch(t *testing.T, st *store.Store, objective, node, id, fact string) model.Card {
	t.Helper()
	n, err := st.GetNode(t.Context(), node)
	if err != nil {
		t.Fatal(err)
	}
	n.OpenQuestions = append(n.OpenQuestions, model.OpenQuestion{
		ID: id, Text: fact, Type: model.OQFact, Status: model.OQResearching, CitedPaths: []string{},
	})
	if err := st.UpdateNode(t.Context(), &n); err != nil {
		t.Fatal(err)
	}
	return seedCard(t, st, objective, node, model.KindResearch, model.CardReady, id+": "+fact+"\n")
}

func consumptionCard(t *testing.T, kind model.CardKind) (*store.Store, model.Objective, model.DesignNode, model.Card) {
	t.Helper()
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	body := "任务\n"
	if kind == model.KindResearch || kind == model.KindSpike {
		qtype := model.OQFact
		if kind == model.KindSpike {
			qtype = model.OQExperiment
		}
		n := node
		n.OpenQuestions = model.OpenQuestionList{{
			ID: "Q1", Text: "待答", Type: qtype, Status: model.OQOpen, CitedPaths: []string{},
		}}
		if err := st.UpdateNode(t.Context(), &n); err != nil {
			t.Fatal(err)
		}
		body = "Q1: 待答\n"
	}
	return st, obj, node, seedCard(t, st, obj.ID, node.ID, kind, model.CardReady, body)
}

func successText(kind model.CardKind) string {
	switch kind {
	case model.KindResearch:
		return "## 事实\n### Q1\n结论：8787\n证据：a.go:1\n把握：高\n"
	case model.KindSpike:
		return "## 结论\n能撑住\n"
	case model.KindReview:
		return "## 复核结论\npass\n"
	default:
		return "## 开放问题\n- 要不要拆开\n- [experiment] 缓存能不能撑住\n## 子节点\n- [module] 计费\n## 可冻结\n是\n"
	}
}

func assertNoSuccess(t *testing.T, st *store.Store, objective, nodeID, cardID string, kind model.CardKind) {
	t.Helper()
	node := nodeOf(t, st, nodeID)
	switch kind {
	case model.KindResearch, model.KindSpike:
		q := findQ(node, "Q1")
		if q.Status == model.OQAnswered || q.Answer != "" {
			t.Fatalf("answer %+v", q)
		}
		if cardStatus(t, st, cardID) == model.CardDone {
			t.Fatal("void attempt marked the card done")
		}
	case model.KindReview:
		if hasReviewPass(t, st, cardID) {
			t.Fatal("recorded a freeze pass")
		}
	default:
		if questionText(node, "要不要拆开") || questionText(node, "缓存能不能撑住") {
			t.Fatalf("questions %+v", node.OpenQuestions)
		}
		if len(nodesOf(t, st, objective)) != 1 || countKind(t, st, objective, nodeID, model.KindSpike) != 0 {
			t.Fatal("void attempt created a node or spike")
		}
		if eventCount(t, st, "node:"+nodeID, evFreezable) != 0 {
			t.Fatal("recorded a freeze proposal")
		}
		if cardStatus(t, st, cardID) == model.CardDone {
			t.Fatal("void attempt marked the card done")
		}
	}
}

func assertSuccess(t *testing.T, st *store.Store, objective, nodeID string, kind model.CardKind) {
	t.Helper()
	node := nodeOf(t, st, nodeID)
	switch kind {
	case model.KindResearch:
		q := findQ(node, "Q1")
		if q.Status != model.OQAnswered || q.Answer != "8787" {
			t.Fatalf("fact %+v", q)
		}
	case model.KindSpike:
		q := findQ(node, "Q1")
		if q.Status != model.OQAnswered || q.Answer != "能撑住" {
			t.Fatalf("spike %+v", q)
		}
	case model.KindReview:
		cards := cardsOf(t, st, objective)
		var review string
		for _, c := range cards {
			if c.Kind == model.KindReview && c.Node == nodeID {
				review = c.ID
			}
		}
		if !hasReviewPass(t, st, review) {
			t.Fatal("missing freeze pass")
		}
	default:
		if !questionText(node, "要不要拆开") || len(nodesOf(t, st, objective)) != 2 || countKind(t, st, objective, nodeID, model.KindSpike) != 1 {
			t.Fatalf("design nodes %d questions %+v", len(nodesOf(t, st, objective)), node.OpenQuestions)
		}
		if eventCount(t, st, "node:"+nodeID, evFreezable) != 1 {
			t.Fatal("freeze proposal count")
		}
	}
}

func mutateSuccess(t *testing.T, st *store.Store, nodeID string, kind model.CardKind) {
	t.Helper()
	if kind != model.KindResearch && kind != model.KindSpike {
		return
	}
	node := nodeOf(t, st, nodeID)
	for i := range node.OpenQuestions {
		if node.OpenQuestions[i].ID == "Q1" {
			node.OpenQuestions[i].Answer = "mutated"
		}
	}
	if err := st.UpdateNode(t.Context(), &node); err != nil {
		t.Fatal(err)
	}
}

func assertNotReapplied(t *testing.T, st *store.Store, objective, nodeID string, kind model.CardKind) {
	t.Helper()
	switch kind {
	case model.KindResearch, model.KindSpike:
		q := findQ(nodeOf(t, st, nodeID), "Q1")
		if q.Answer != "mutated" {
			t.Fatalf("reapplied answer %+v", q)
		}
	case model.KindReview:
		cards := cardsOf(t, st, objective)
		for _, c := range cards {
			if c.Kind == model.KindReview && c.Node == nodeID && eventCount(t, st, "card:"+c.ID, evReviewPass) != 1 {
				t.Fatalf("review events %d", eventCount(t, st, "card:"+c.ID, evReviewPass))
			}
		}
	default:
		if len(nodesOf(t, st, objective)) != 2 || eventCount(t, st, "node:"+nodeID, evFreezable) != 1 || countKind(t, st, objective, nodeID, model.KindSpike) != 1 {
			t.Fatal("design result was applied twice")
		}
	}
}

func fileCard(t *testing.T, kind model.CardKind, claim string) (*store.Store, model.Objective, model.DesignNode, model.Card) {
	t.Helper()
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	node.DocPath = "docs/design/" + obj.ID + "/" + node.ID + ".md"
	if err := st.UpdateNode(t.Context(), &node); err != nil {
		t.Fatal(err)
	}
	card := seedCard(t, st, obj.ID, node.ID, kind, model.CardReady, "任务\n")
	fresh, err := st.GetCard(t.Context(), card.ID)
	if err != nil {
		t.Fatal(err)
	}
	switch claim {
	case "doc":
		fresh.Claims = []model.Claim{{Path: node.DocPath}}
	case "pack":
		fresh.Claims = []model.Claim{{Path: "docs/design/" + obj.ID + "/context.md"}}
	}
	if claim != "" {
		if err := st.UpdateCard(t.Context(), &fresh); err != nil {
			t.Fatal(err)
		}
	}
	return st, obj, node, fresh
}

func absorbText(t *testing.T, st *store.Store, cardID string, index int, outcome model.Outcome, text string) {
	t.Helper()
	attempt := plantAttempt(t, st, cardID, index, outcome, text)
	if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, outcome, text); err != nil {
		t.Fatal(err)
	}
}

func plantAttempt(t *testing.T, st *store.Store, cardID string, index int, outcome model.Outcome, text string) model.Attempt {
	t.Helper()
	dir := t.TempDir()
	writeResultFile(t, dir, text)
	return makeAttempt(t, st, cardID, index, "base", dir, outcome)
}

func setCardStatus(t *testing.T, st *store.Store, id string, status model.CardStatus) error {
	t.Helper()
	card, err := st.GetCard(t.Context(), id)
	if err != nil {
		return err
	}
	card.Status = status
	return st.UpdateCard(t.Context(), &card)
}

func summaryAnswers(t *testing.T, body string) map[string]string {
	t.Helper()
	const heading = "## 事实摘要\n"
	i := strings.Index(body, heading)
	if i < 0 {
		t.Fatalf("no fact summary in %s", body)
	}
	var facts []model.OpenQuestion
	if err := json.Unmarshal([]byte(strings.TrimSpace(body[i+len(heading):])), &facts); err != nil {
		t.Fatalf("summary json: %v\n%s", err, body)
	}
	out := map[string]string{}
	for _, q := range facts {
		out[q.ID] = q.Answer
	}
	return out
}

func readyCards(t *testing.T, st *store.Store, objective, node string, kind model.CardKind) []model.Card {
	t.Helper()
	var out []model.Card
	for _, c := range cardsOf(t, st, objective) {
		if c.Node == node && c.Kind == kind && c.Status == model.CardReady {
			out = append(out, c)
		}
	}
	return out
}

func normalizeCards(t *testing.T, st *store.Store, objective, cardID string) []model.Card {
	t.Helper()
	var out []model.Card
	for _, c := range cardsOf(t, st, objective) {
		if c.Kind == model.KindNormalize && len(c.Deps) > 0 && c.Deps[0] == cardID {
			out = append(out, c)
		}
	}
	return out
}

func currentAttemptID(t *testing.T, st *store.Store, cardID string) string {
	t.Helper()
	attempts, err := st.ListAttempts(t.Context(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	cur := currentAttempt(attempts)
	if cur == nil {
		t.Fatal("missing attempt")
	}
	return cur.ID
}

func eventCount(t *testing.T, st *store.Store, entity, typ string) int {
	t.Helper()
	ev, err := st.ListEvents(t.Context(), entity)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ev {
		if e.Type == typ {
			n++
		}
	}
	return n
}
