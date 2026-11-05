package loop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
)

type retryBox struct {
	msgs []notify.Message
	fail error
}

func (b *retryBox) Deliver(_ context.Context, msg notify.Message) error {
	b.msgs = append(b.msgs, msg)
	return b.fail
}

func TestQuestionDeliveryRetryOnAbsorb(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	card := seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardRunning, "正在做\n")
	box := &retryBox{fail: errors.New("notify down")}
	deps := Deps{Store: st, Notify: box}
	text := "## 设计问题\n接口不一致\n"
	attempt := plantAttempt(t, st, card.ID, 0, model.OutcomePassed, text)
	if err := AbsorbDisposed(t.Context(), deps, attempt.ID, model.OutcomePassed, text); err == nil {
		t.Fatal("expected delivery failure")
	}
	if n := len(reopenQuestions(t, st, obj.ID)); n != 1 || len(box.msgs) != 1 {
		t.Fatalf("questions %d deliveries %d", n, len(box.msgs))
	}
	if err := AbsorbDisposed(t.Context(), deps, attempt.ID, model.OutcomePassed, text); err == nil {
		t.Fatal("delivery still down")
	}
	rows := reopenQuestions(t, st, obj.ID)
	if len(rows) != 1 || len(box.msgs) != 2 || box.msgs[0].QuestionID != box.msgs[1].QuestionID || box.msgs[1].QuestionID != rows[0].ID {
		t.Fatalf("retry rows %+v deliveries %+v", rows, box.msgs)
	}
	box.fail = nil
	if err := AbsorbDisposed(t.Context(), deps, attempt.ID, model.OutcomePassed, text); err != nil {
		t.Fatal(err)
	}
	rows = reopenQuestions(t, st, obj.ID)
	if len(rows) != 1 || len(box.msgs) != 3 || box.msgs[2].Origin != obj.Origin {
		t.Fatalf("success rows %+v deliveries %+v", rows, box.msgs)
	}
	if err := AbsorbDisposed(t.Context(), deps, attempt.ID, model.OutcomePassed, text); err != nil {
		t.Fatal(err)
	}
	if len(reopenQuestions(t, st, obj.ID)) != 1 || len(box.msgs) != 3 {
		t.Fatalf("duplicate after success questions %d msgs %d", len(reopenQuestions(t, st, obj.ID)), len(box.msgs))
	}
	other := seedCard(t, st, obj.ID, node.ID, model.KindReview, model.CardRunning, "复核\n")
	otherText := "## 设计问题\n另一处设计问题\n"
	otherAttempt := plantAttempt(t, st, other.ID, 0, model.OutcomePassed, otherText)
	if err := AbsorbDisposed(t.Context(), deps, otherAttempt.ID, model.OutcomePassed, otherText); err != nil {
		t.Fatal(err)
	}
	if n := len(reopenQuestions(t, st, obj.ID)); n != 2 {
		t.Fatalf("different text questions %d", n)
	}
}

func TestFreezeQuestionRetriesDelivery(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerDirection)
	node.Status = model.NodeFreezable
	if err := st.UpdateNode(t.Context(), &node); err != nil {
		t.Fatal(err)
	}
	box := &retryBox{fail: errors.New("notify down")}
	deps := Deps{Store: st, Notify: box}
	if err := designStep(t.Context(), deps, obj.ID); err == nil {
		t.Fatal("expected delivery failure")
	}
	if n := len(freezeQuestions(t, st, obj.ID)); n != 1 || len(box.msgs) != 1 {
		t.Fatalf("questions %d deliveries %d", n, len(box.msgs))
	}
	if err := designStep(t.Context(), deps, obj.ID); err == nil {
		t.Fatal("delivery still down")
	}
	rows := freezeQuestions(t, st, obj.ID)
	if len(rows) != 1 || len(box.msgs) != 2 || box.msgs[0].QuestionID != rows[0].ID || box.msgs[1].QuestionID != rows[0].ID {
		t.Fatalf("retry rows %+v deliveries %+v", rows, box.msgs)
	}
	box.fail = nil
	if err := designStep(t.Context(), deps, obj.ID); err != nil {
		t.Fatal(err)
	}
	if len(freezeQuestions(t, st, obj.ID)) != 1 || len(box.msgs) != 3 || box.msgs[2].Origin != obj.Origin {
		t.Fatalf("success questions %d deliveries %+v", len(freezeQuestions(t, st, obj.ID)), box.msgs)
	}
	if err := designStep(t.Context(), deps, obj.ID); err != nil {
		t.Fatal(err)
	}
	if len(freezeQuestions(t, st, obj.ID)) != 1 || len(box.msgs) != 3 {
		t.Fatalf("duplicate after success questions %d msgs %d", len(freezeQuestions(t, st, obj.ID)), len(box.msgs))
	}
}

func TestFreezeWording(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "section-yes", text: "## 可冻结\n是\n"},
		{name: "keyi", text: "## 可冻结\n可以冻结\n"},
		{name: "shi-comma", text: "## 可冻结\n是，可以冻结\n"},
		{name: "conclusion", text: "## 开放问题\n- 要不要留\n## 结论\n可以冻结\n"},
		{name: "conclusion-comma", text: "## 结论\n是，可以冻结\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openLoop(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			card := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardRunning, "任务\n")
			absorbText(t, st, card.ID, 0, model.OutcomePassed, tc.text)
			if eventCount(t, st, "node:"+node.ID, evFreezable) != 1 {
				t.Fatal("missing freezable proposal")
			}
			if strings.Contains(tc.text, "要不要留") && !questionText(nodeOf(t, st, node.ID), "要不要留") {
				t.Fatal("open question dropped")
			}
			if len(normalizeCards(t, st, obj.ID, card.ID)) != 0 {
				t.Fatal("clear proposal created a normalize card")
			}
		})
	}

	t.Run("spike-conclusion-is-not-a-proposal", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		node.OpenQuestions = append(node.OpenQuestions, model.OpenQuestion{
			ID: "Q1", Text: "缓存能不能撑住", Type: model.OQExperiment, Status: model.OQOpen, CitedPaths: []string{},
		})
		if err := st.UpdateNode(t.Context(), &node); err != nil {
			t.Fatal(err)
		}
		card := seedCard(t, st, obj.ID, node.ID, model.KindSpike, model.CardRunning, "Q1: 缓存能不能撑住\n")
		absorbText(t, st, card.ID, 0, model.OutcomePassed, "## 结论\n能撑住\n")
		got := nodeOf(t, st, node.ID)
		if !questionText(got, "缓存能不能撑住") {
			t.Fatalf("questions %+v", got.OpenQuestions)
		}
		var answered string
		for _, q := range got.OpenQuestions {
			if q.ID == "Q1" {
				answered = q.Answer
			}
		}
		if answered != "能撑住" {
			t.Fatalf("answer %q", answered)
		}
		if eventCount(t, st, "node:"+node.ID, evFreezable) != 0 || len(normalizeCards(t, st, obj.ID, card.ID)) != 0 {
			t.Fatal("spike conclusion recorded a freeze proposal")
		}
	})

	t.Run("unjudgeable-becomes-one-normalize", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		card := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardRunning, "任务\n")
		text := "## 开放问题\n- 不该先写入\n## 可冻结\n再议一下\n"
		absorbText(t, st, card.ID, 0, model.OutcomePassed, text)
		if len(normalizeCards(t, st, obj.ID, card.ID)) != 1 {
			t.Fatalf("normalize cards %d", len(normalizeCards(t, st, obj.ID, card.ID)))
		}
		if questionText(nodeOf(t, st, node.ID), "不该先写入") || eventCount(t, st, "node:"+node.ID, evFreezable) != 0 {
			t.Fatal("unjudgeable text was applied")
		}
		if cardStatus(t, st, card.ID) == model.CardNeedsInput {
			t.Fatal("needs_input before normalize finished")
		}
		absorbText(t, st, card.ID, 1, model.OutcomePassed, text)
		if len(normalizeCards(t, st, obj.ID, card.ID)) != 1 {
			t.Fatal("created another normalize")
		}
	})

	t.Run("unjudgeable-normalize-needs-input", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeManual, model.FreezeFast)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		source := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardRunning, "任务\n")
		norm := seedNormalize(t, st, obj.ID, node.ID, source.ID)
		absorbText(t, st, norm.ID, 0, model.OutcomePassed, "## 可冻结\n再议一下\n")
		if cardStatus(t, st, source.ID) != model.CardNeedsInput {
			t.Fatalf("source %s", cardStatus(t, st, source.ID))
		}
		if eventCount(t, st, "node:"+node.ID, evFreezable) != 0 {
			t.Fatal("unclear normalize recorded a proposal")
		}
		if len(normalizeCards(t, st, obj.ID, source.ID)) != 1 {
			t.Fatal("created another normalize")
		}
	})
}

func TestNormalizeAbsorbFinishesManualSource(t *testing.T) {
	cases := []struct {
		name     string
		kind     model.CardKind
		text     string
		question string
		title    string
		freeze   bool
	}{
		{
			name:     "design",
			kind:     model.KindDesign,
			text:     "## 开放问题\n- 要不要加缓存\n## 子节点\n- [module] 子模块\n## 可冻结\n可以冻结\n",
			question: "要不要加缓存",
			freeze:   true,
		},
		{
			name:     "plan",
			kind:     model.KindPlan,
			text:     "## 开放问题\n- [otto] 拆开做\n## 新卡\n[{\"key\":\"impl\",\"kind\":\"dev\",\"title\":\"实现接口\"}]\n## 可冻结\n是，可以冻结\n",
			question: "拆开做",
			title:    "实现接口",
			freeze:   true,
		},
		{
			name:     "replan",
			kind:     model.KindReplan,
			text:     "## 开放问题\n- 改尚未开始的卡\n## 新卡\n[{\"key\":\"fix\",\"kind\":\"dev\",\"title\":\"补测试\"}]\n## 结论\n可以冻结\n",
			question: "改尚未开始的卡",
			title:    "补测试",
			freeze:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openLoop(t)
			obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			source := seedCard(t, st, obj.ID, node.ID, tc.kind, model.CardRunning, "任务\n")
			norm := seedNormalize(t, st, obj.ID, node.ID, source.ID)
			absorbText(t, st, norm.ID, 0, model.OutcomePassed, tc.text)
			if cardStatus(t, st, source.ID) != model.CardDone {
				t.Fatalf("source %s", cardStatus(t, st, source.ID))
			}
			if !questionText(nodeOf(t, st, node.ID), tc.question) {
				t.Fatalf("questions %+v", nodeOf(t, st, node.ID).OpenQuestions)
			}
			if tc.freeze && eventCount(t, st, "node:"+node.ID, evFreezable) != 1 {
				t.Fatal("missing freezable proposal")
			}
			if tc.title != "" && !cardTitled(t, st, obj.ID, tc.title) {
				t.Fatalf("missing card %s in %+v", tc.title, cardsOf(t, st, obj.ID))
			}
			if countKind(t, st, obj.ID, node.ID, model.KindDesign) != boolCount(tc.kind == model.KindDesign) {
				t.Fatalf("design cards %d", countKind(t, st, obj.ID, node.ID, model.KindDesign))
			}
			if countKind(t, st, obj.ID, node.ID, model.KindResearch) != 0 || countKind(t, st, obj.ID, node.ID, model.KindSpike) != 0 {
				t.Fatal("manual normalize created a research or spike card")
			}
			if len(nodesOf(t, st, obj.ID)) != 1 {
				t.Fatalf("nodes %d", len(nodesOf(t, st, obj.ID)))
			}
		})
	}

	t.Run("dev-original-unchanged", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		source := seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardRunning, "正在做\n")
		norm := seedNormalize(t, st, obj.ID, node.ID, source.ID)
		text := "## 开放问题\n- 不该写到开发卡\n## 新卡\n[{\"key\":\"x\",\"kind\":\"dev\",\"title\":\"不该出现\"}]\n## 可冻结\n是\n"
		absorbText(t, st, norm.ID, 0, model.OutcomePassed, text)
		if cardStatus(t, st, source.ID) != model.CardRunning {
			t.Fatalf("source %s", cardStatus(t, st, source.ID))
		}
		if questionText(nodeOf(t, st, node.ID), "不该写到开发卡") || cardTitled(t, st, obj.ID, "不该出现") {
			t.Fatal("dev original was absorbed as design output")
		}
		if eventCount(t, st, "node:"+node.ID, evFreezable) != 0 {
			t.Fatal("dev original recorded a freeze proposal")
		}
	})
}

func TestFreezeCurrentSense(t *testing.T) {
	type sense int
	const (
		senseNo sense = iota
		senseUnclear
		senseYes
	)
	texts := []struct {
		name  string
		text  string
		sense sense
	}{
		{name: "negative-later-fragment", text: "## 可冻结\n否，需等接口确认后才可以冻结\n", sense: senseNo},
		{name: "bare-no", text: "## 可冻结\n否\n", sense: senseNo},
		{name: "conditional", text: "## 开放问题\n- 不该写入\n## 可冻结\n等接口确认后才可以冻结\n", sense: senseUnclear},
		{name: "section-yes", text: "## 可冻结\n是\n", sense: senseYes},
		{name: "keyi", text: "## 可冻结\n可以冻结\n", sense: senseYes},
		{name: "shi-comma", text: "## 可冻结\n是，可以冻结\n", sense: senseYes},
		{name: "conclusion", text: "## 开放问题\n- 要不要留\n## 结论\n可以冻结\n", sense: senseYes},
		{name: "conclusion-comma", text: "## 结论\n是，可以冻结\n", sense: senseYes},
	}
	entries := []struct {
		name         string
		kind         model.CardKind
		layer        model.Layer
		viaNormalize bool
	}{
		{name: "design", kind: model.KindDesign, layer: model.LayerModule},
		{name: "detail", kind: model.KindDesign, layer: model.LayerDetail},
		{name: "plan", kind: model.KindPlan, layer: model.LayerModule},
		{name: "replan", kind: model.KindReplan, layer: model.LayerModule},
		{name: "normalize", kind: model.KindDesign, layer: model.LayerModule, viaNormalize: true},
	}
	for _, entry := range entries {
		for _, tc := range texts {
			t.Run(entry.name+"/"+tc.name, func(t *testing.T) {
				st := openLoop(t)
				obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
				node := seedNode(t, st, obj.ID, "", entry.layer)
				source := seedCard(t, st, obj.ID, node.ID, entry.kind, model.CardRunning, "任务\n")
				target := source
				seededNorm := 0
				if entry.viaNormalize {
					target = seedNormalize(t, st, obj.ID, node.ID, source.ID)
					seededNorm = 1
				}
				absorbText(t, st, target.ID, 0, model.OutcomePassed, tc.text)
				if tc.sense != senseYes {
					if err := loopStep(t, depsOf(st), obj.ID); err != nil {
						t.Fatal(err)
					}
				}
				gotNode := nodeOf(t, st, node.ID)
				freezes := eventCount(t, st, "node:"+node.ID, evFreezable)
				norms := len(normalizeCards(t, st, obj.ID, source.ID))
				plans := countKind(t, st, obj.ID, node.ID, model.KindPlan)
				wantPlans := 0
				if entry.kind == model.KindPlan {
					wantPlans = 1
				}
				switch tc.sense {
				case senseYes:
					if freezes != 1 {
						t.Fatalf("freezable events %d", freezes)
					}
					if norms != seededNorm {
						t.Fatalf("normalize cards %d", norms)
					}
					if strings.Contains(tc.text, "要不要留") && !questionText(gotNode, "要不要留") {
						t.Fatal("open question dropped")
					}
				case senseNo:
					if freezes != 0 || gotNode.Status == model.NodeFrozen || plans != wantPlans || norms != seededNorm {
						t.Fatalf("freezes %d status %s plans %d norms %d", freezes, gotNode.Status, plans, norms)
					}
				default:
					if freezes != 0 || gotNode.Status == model.NodeFrozen || plans != wantPlans || norms != 1 {
						t.Fatalf("freezes %d status %s plans %d norms %d", freezes, gotNode.Status, plans, norms)
					}
					if questionText(gotNode, "不该写入") {
						t.Fatal("conditional proposal was applied")
					}
				}
			})
		}
	}
}

func TestNormalizePlanSplitRecordsBatch(t *testing.T) {
	const listed = "## 新卡\n[{\"key\":\"impl\",\"kind\":\"dev\",\"title\":\"实现接口\"},{\"key\":\"cover\",\"kind\":\"dev\",\"title\":\"补测试\"},{\"key\":\"else\",\"kind\":\"dev\",\"title\":\"别的节点\",\"node\":\"n-other\"}]\n## 可冻结\n是\n"

	t.Run("plan", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CARDEX_HOME", home)
		st, err := store.Open(filepath.Join(home, "cardex.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		plan := seedCard(t, st, obj.ID, node.ID, model.KindPlan, model.CardRunning, "任务\n")
		norm := seedNormalize(t, st, obj.ID, node.ID, plan.ID)
		attempt := plantAttempt(t, st, norm.ID, 0, model.OutcomePassed, listed)
		if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, listed); err != nil {
			t.Fatal(err)
		}
		created := cardsKindOn(t, st, obj.ID, node.ID, model.KindDev)
		body, ok := planSplitOf(t, st, plan.ID)
		if !ok || body.Node != node.ID || !sameIDs(body.Cards, cardIDs(created)) || len(created) != 2 {
			t.Fatalf("split %+v created %+v", body, created)
		}
		before := len(cardsOf(t, st, obj.ID))
		if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, listed); err != nil {
			t.Fatal(err)
		}
		if len(cardsOf(t, st, obj.ID)) != before || len(cardsKindOn(t, st, obj.ID, node.ID, model.KindDev)) != len(created) {
			t.Fatal("second absorb created cards")
		}
		for _, card := range created {
			if err := setCardStatus(t, st, card.ID, model.CardDone); err != nil {
				t.Fatal(err)
			}
		}
		if err := DevStep(t.Context(), obj.ID); err != nil {
			t.Fatal(err)
		}
		replans := cardsKindOn(t, st, obj.ID, node.ID, model.KindReplan)
		if len(replans) != 1 || !hasDepID(replans[0], plan.ID) {
			t.Fatalf("replan %+v", replans)
		}
		if err := DevStep(t.Context(), obj.ID); err != nil {
			t.Fatal(err)
		}
		if again := cardsKindOn(t, st, obj.ID, node.ID, model.KindReplan); len(again) != 1 {
			t.Fatalf("second step replans %+v", again)
		}
	})

	t.Run("replan-unrecorded", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		replan := seedCard(t, st, obj.ID, node.ID, model.KindReplan, model.CardRunning, "任务\n")
		norm := seedNormalize(t, st, obj.ID, node.ID, replan.ID)
		attempt := plantAttempt(t, st, norm.ID, 0, model.OutcomePassed, listed)
		if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, listed); err != nil {
			t.Fatal(err)
		}
		created := cardsKindOn(t, st, obj.ID, node.ID, model.KindDev)
		if len(created) != 2 {
			t.Fatalf("created %+v", created)
		}
		if _, ok := planSplitOf(t, st, replan.ID); ok || eventCount(t, st, "", "plan_split") != 0 {
			t.Fatal("replan absorb recorded a plan split")
		}
		before := len(cardsOf(t, st, obj.ID))
		if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, listed); err != nil {
			t.Fatal(err)
		}
		if len(cardsOf(t, st, obj.ID)) != before {
			t.Fatal("second absorb created cards")
		}
	})
}

func planSplitOf(t *testing.T, st *store.Store, planID string) (planSplitBody, bool) {
	t.Helper()
	evs, err := st.ListEvents(t.Context(), "card:"+planID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type != "plan_split" {
			continue
		}
		var body planSplitBody
		if err := json.Unmarshal(ev.Data, &body); err != nil {
			t.Fatal(err)
		}
		return body, true
	}
	return planSplitBody{}, false
}

func cardsKindOn(t *testing.T, st *store.Store, objective, node string, kind model.CardKind) []model.Card {
	t.Helper()
	var out []model.Card
	for _, card := range cardsOf(t, st, objective) {
		if card.Node == node && card.Kind == kind {
			out = append(out, card)
		}
	}
	return out
}

func cardIDs(cards []model.Card) []string {
	ids := make([]string, 0, len(cards))
	for _, card := range cards {
		ids = append(ids, card.ID)
	}
	return ids
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, id := range a {
		if id == "" {
			return false
		}
		seen[id]++
	}
	for _, id := range b {
		seen[id]--
		if seen[id] < 0 {
			return false
		}
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func seedNormalize(t *testing.T, st *store.Store, objective, node, sourceID string) model.Card {
	t.Helper()
	card := &model.Card{
		Objective: objective, Node: node, Kind: model.KindNormalize, Title: "整理",
		Body: "整理 " + sourceID + "\n", Tier: model.TierT1, Status: model.CardDone,
		Deps: []string{sourceID},
	}
	if err := st.CreateCard(t.Context(), card); err != nil {
		t.Fatal(err)
	}
	return *card
}

func reopenQuestions(t *testing.T, st *store.Store, objective string) []model.Question {
	t.Helper()
	return questionsOfKind(t, st, objective, model.QuestionReopen)
}

func freezeQuestions(t *testing.T, st *store.Store, objective string) []model.Question {
	t.Helper()
	return questionsOfKind(t, st, objective, model.QuestionFreeze)
}

func questionsOfKind(t *testing.T, st *store.Store, objective string, kind model.QuestionKind) []model.Question {
	t.Helper()
	all, err := st.ListQuestions(t.Context(), objective)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Question
	for _, q := range all {
		if q.Kind == kind {
			out = append(out, q)
		}
	}
	return out
}

func cardTitled(t *testing.T, st *store.Store, objective, title string) bool {
	t.Helper()
	for _, card := range cardsOf(t, st, objective) {
		if card.Title == title {
			return true
		}
	}
	return false
}

func boolCount(ok bool) int {
	if ok {
		return 1
	}
	return 0
}
