package loop

import (
	"testing"

	"cardex/internal/model"
	"cardex/internal/store"
)

// TestFreezeShortAnswer drives design, plan, and replan absorb, directly and
// through the normalize card, for the same body under ## 可冻结 and ## 结论.
func TestFreezeShortAnswer(t *testing.T) {
	type expect int
	const (
		expectYes expect = iota
		expectNegation
		expectUnclear
		expectNotYes
	)
	bodies := []struct {
		name string
		body string
		want expect
	}{
		{name: "shi", body: "是", want: expectYes},
		{name: "shi-period", body: "是。", want: expectYes},
		{name: "dash-shi", body: "- 是", want: expectYes},
		{name: "keyi", body: "可以冻结", want: expectYes},
		{name: "shi-keyi-period", body: "是，可以冻结。", want: expectYes},
		{name: "yes", body: "yes", want: expectYes},
		{name: "agree", body: "同意", want: expectYes},
		{name: "true", body: "true", want: expectYes},
		{name: "ke", body: "可冻结", want: expectYes},
		{name: "propose", body: "提议", want: expectYes},
		{name: "one", body: "1", want: expectYes},
		{name: "star-shi", body: "* 是", want: expectYes},
		{name: "plus-shi", body: "+ 是", want: expectYes},
		{name: "num-dot-shi", body: "1. 是", want: expectYes},
		{name: "num-enum-shi", body: "1、是", want: expectYes},
		{name: "negation", body: "否，需等接口确认后才可以冻结。", want: expectNegation},
		{name: "agree-plan", body: "同意，采用方案 A。", want: expectNotYes},
		{name: "shi-wait", body: "是，但要等接口确认后再冻结。", want: expectUnclear},
		{name: "shi-keyi-but", body: "是可以冻结，但要等接口确认后再执行。", want: expectUnclear},
		{name: "dash-shi-but", body: "- 是，可以冻结，但要等接口确认后再执行。", want: expectUnclear},
		{name: "suggest", body: "建议可以冻结，但要等接口确认后再执行。", want: expectUnclear},
		{name: "this-node", body: "本节点可以冻结", want: expectUnclear},
		{name: "two-lines", body: "是\n但要等接口确认后再执行。", want: expectNotYes},
	}
	headings := []string{"可冻结", "结论"}
	kinds := []model.CardKind{model.KindDesign, model.KindPlan, model.KindReplan}
	for _, kind := range kinds {
		for _, via := range []bool{false, true} {
			for _, heading := range headings {
				for _, tc := range bodies {
					kind, via, heading, tc := kind, via, heading, tc
					path := "direct"
					if via {
						path = "normalize"
					}
					t.Run(string(kind)+"/"+path+"/"+heading+"/"+tc.name, func(t *testing.T) {
						st := openLoop(t)
						obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
						node := seedNode(t, st, obj.ID, "", model.LayerModule)
						source := seedCard(t, st, obj.ID, node.ID, kind, model.CardRunning, "任务\n")
						target := source
						seeded := 0
						if via {
							target = seedNormalize(t, st, obj.ID, node.ID, source.ID)
							seeded = 1
						}
						absorbText(t, st, target.ID, 0, model.OutcomePassed, "## "+heading+"\n"+tc.body+"\n")
						if tc.want != expectYes {
							if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
								t.Fatal(err)
							}
						}
						freezes := eventCount(t, st, "node:"+node.ID, evFreezable)
						norms := len(normalizeCards(t, st, obj.ID, source.ID))
						frozen := nodeOf(t, st, node.ID).Status == model.NodeFrozen
						plans := freezePlanCount(t, st, obj.ID, node.ID)
						switch tc.want {
						case expectYes:
							if freezes != 1 || norms != seeded {
								t.Fatalf("freezes %d normalize %d", freezes, norms)
							}
						case expectNegation:
							if freezes != 0 || norms != seeded || frozen || plans != 0 {
								t.Fatalf("freezes %d normalize %d frozen %v plans %d", freezes, norms, frozen, plans)
							}
						case expectUnclear:
							if freezes != 0 || norms != 1 || frozen || plans != 0 {
								t.Fatalf("freezes %d normalize %d frozen %v plans %d", freezes, norms, frozen, plans)
							}
							if via && cardStatus(t, st, source.ID) != model.CardNeedsInput {
								t.Fatalf("source %s", cardStatus(t, st, source.ID))
							}
							if !via && cardStatus(t, st, source.ID) == model.CardNeedsInput {
								t.Fatal("needs_input before normalize finished")
							}
						default:
							if freezes != 0 || frozen || plans != 0 {
								t.Fatalf("freezes %d frozen %v plans %d", freezes, frozen, plans)
							}
						}
					})
				}
			}
		}
	}
}

// TestFreezeFastModuleConditional starts from a fast module with no open
// questions. A conditional ## 结论 dispatches one normalize card. After that
// card returns ## 可冻结 / 是, the step freezes the node and dispatches a plan.
func TestFreezeFastModuleConditional(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	if len(node.OpenQuestions) != 0 {
		t.Fatalf("open questions %+v", node.OpenQuestions)
	}
	card := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardRunning, "任务\n")
	absorbText(t, st, card.ID, 0, model.OutcomePassed, "## 结论\n设计完成，可以冻结，但要等接口确认后再执行。\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	norms := normalizeCards(t, st, obj.ID, card.ID)
	if len(norms) != 1 || eventCount(t, st, "node:"+node.ID, evFreezable) != 0 {
		t.Fatalf("normalize %d freezes %d", len(norms), eventCount(t, st, "node:"+node.ID, evFreezable))
	}
	if nodeOf(t, st, node.ID).Status == model.NodeFrozen || freezePlanCount(t, st, obj.ID, node.ID) != 0 || countKind(t, st, obj.ID, node.ID, model.KindPlan) != 0 {
		t.Fatalf("status %s plans %d", nodeOf(t, st, node.ID).Status, countKind(t, st, obj.ID, node.ID, model.KindPlan))
	}
	absorbText(t, st, norms[0].ID, 0, model.OutcomePassed, "## 可冻结\n是\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if got := nodeOf(t, st, node.ID).Status; got != model.NodeFrozen {
		t.Fatalf("status %s", got)
	}
	if countKind(t, st, obj.ID, node.ID, model.KindPlan) != 1 || freezePlanCount(t, st, obj.ID, node.ID) != 1 {
		t.Fatalf("plans %d titled %d", countKind(t, st, obj.ID, node.ID, model.KindPlan), freezePlanCount(t, st, obj.ID, node.ID))
	}
	if len(normalizeCards(t, st, obj.ID, card.ID)) != 1 {
		t.Fatal("second normalize card")
	}
}

func freezePlanCount(t *testing.T, st *store.Store, objective, node string) int {
	t.Helper()
	n := 0
	for _, card := range cardsOf(t, st, objective) {
		if card.Node == node && card.Kind == model.KindPlan && card.Title == "拆卡" {
			n++
		}
	}
	return n
}
