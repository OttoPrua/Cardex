package loop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/question"
	"cardex/internal/store"
)

func openLoop(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/cardex.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func depsOf(st *store.Store) Deps {
	return Deps{Store: st}
}

func seedObjective(t *testing.T, st *store.Store, project string, mode model.ObjectiveMode, freeze model.FreezeMode) model.Objective {
	t.Helper()
	o := &model.Objective{
		Project: project, Title: "目标", Mode: mode, FreezeMode: freeze,
		Status: model.ObjectiveActive, Origin: "codex:t",
	}
	if err := st.CreateObjective(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	return *o
}

func seedNode(t *testing.T, st *store.Store, objective, parent string, layer model.Layer) model.DesignNode {
	t.Helper()
	n := &model.DesignNode{
		Objective: objective, Parent: parent, Layer: layer,
		Status: model.NodeDrafting, OpenQuestions: model.OpenQuestionList{},
	}
	if err := st.CreateNode(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	return *n
}

func seedCard(t *testing.T, st *store.Store, objective, node string, kind model.CardKind, status model.CardStatus, body string) model.Card {
	t.Helper()
	prompt := body
	result := ""
	if terminalCard(status) && strings.Contains(body, "## ") {
		result = body
		prompt = "任务提示，与结果不同\n"
	}
	c := &model.Card{
		Objective: objective, Node: node, Kind: kind, Title: "卡", Body: prompt,
		Tier: model.DefaultTier(kind), Status: status,
	}
	if err := st.CreateCard(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if result != "" {
		plantResult(t, st, c.ID, result, "seed-base")
	}
	return *c
}

func finishCard(t *testing.T, st *store.Store, id, result string) {
	t.Helper()
	finishCardBase(t, st, id, result, "finish-base")
}

func finishCardBase(t *testing.T, st *store.Store, id, result, base string) {
	t.Helper()
	c, err := st.GetCard(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	c.Status = model.CardDone
	if strings.TrimSpace(c.Body) == "" || c.Body == result {
		c.Body = "任务提示，与结果不同\n"
	}
	if err := st.UpdateCard(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	plantResult(t, st, c.ID, result, base)
}

func plantResult(t *testing.T, st *store.Store, cardID, result, base string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".cardex-result.md"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAttempt(t.Context(), &model.Attempt{Card: cardID, Worktree: dir, BaseCommit: base}); err != nil {
		t.Fatal(err)
	}
}

func cardsOf(t *testing.T, st *store.Store, objective string) []model.Card {
	t.Helper()
	cards, err := st.ListCards(t.Context(), objective)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

func nodeOf(t *testing.T, st *store.Store, id string) model.DesignNode {
	t.Helper()
	n, err := st.GetNode(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func questionsOf(t *testing.T, st *store.Store, objective string) []model.Question {
	t.Helper()
	qs, err := st.ListQuestions(t.Context(), objective)
	if err != nil {
		t.Fatal(err)
	}
	return qs
}

func TestDesignStepNeedsStoreAndDevStaysOpen(t *testing.T) {
	if err := DesignStep(t.Context(), "o"); err == nil {
		t.Fatal("DesignStep without a store returned nil")
	}
	if !errors.Is(DevStep(t.Context(), "o"), ErrNotImplemented) {
		t.Fatal("DevStep changed")
	}
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	eng := New(st, nil, nil)
	if err := eng.Step(t.Context(), obj.ID); err != nil {
		t.Fatal(err)
	}
	cards := cardsOf(t, st, obj.ID)
	if len(cards) != 1 || cards[0].Kind != model.KindResearch || cards[0].Title != "盘点" {
		t.Fatalf("inventory %+v", cards)
	}
	if err := eng.Step(t.Context(), obj.ID); err != nil {
		t.Fatal(err)
	}
	if got := cardsOf(t, st, obj.ID); len(got) != 1 {
		t.Fatalf("second step cards %d", len(got))
	}
	nodes, err := st.ListNodes(t.Context(), obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("direction before inventory finished: %+v", nodes)
	}
	finishCard(t, st, cards[0].ID, "## 事实\n### Q0\n结论：仓库是 Go 模块\n证据：go.mod:1\n把握：高\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	nodes, err = st.ListNodes(t.Context(), obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Layer != model.LayerDirection {
		t.Fatalf("direction %+v", nodes)
	}
	var q0 model.OpenQuestion
	for _, q := range nodes[0].OpenQuestions {
		if q.ID == "Q0" {
			q0 = q
		}
	}
	if q0.Status != model.OQAnswered || q0.Answer != "仓库是 Go 模块" || q0.Type != model.OQFact {
		t.Fatalf("Q0 %+v", q0)
	}
	var design *model.Card
	for i := range cardsOf(t, st, obj.ID) {
		c := cardsOf(t, st, obj.ID)[i]
		if c.Kind == model.KindDesign && c.Status == model.CardReady {
			design = &c
		}
	}
	if design == nil || !contains(design.Body, "Q0") || !contains(design.Body, "仓库是 Go 模块") {
		t.Fatalf("direction design %+v", design)
	}
}

func TestManualCreatesNothing(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeManual, model.FreezeFast)
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if cards := cardsOf(t, st, obj.ID); len(cards) != 0 {
		t.Fatalf("manual cards %+v", cards)
	}
	nodes, err := st.ListNodes(t.Context(), obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("manual nodes %+v", nodes)
	}
}

func TestFreezeTable(t *testing.T) {
	cases := []struct {
		layer model.Layer
		mode  model.FreezeMode
		kind  freezeKind
	}{
		{model.LayerDirection, model.FreezeSafe, freezeAsk},
		{model.LayerDirection, model.FreezeFast, freezeAsk},
		{model.LayerModule, model.FreezeSafe, freezeAsk},
		{model.LayerModule, model.FreezeFast, freezeNow},
		{model.LayerDetail, model.FreezeSafe, freezeReview},
		{model.LayerDetail, model.FreezeFast, freezeNow},
	}
	for _, tc := range cases {
		t.Run(string(tc.layer)+"/"+string(tc.mode), func(t *testing.T) {
			st := openLoop(t)
			obj := seedObjective(t, st, "", model.ModeAuto, tc.mode)
			node := seedNode(t, st, obj.ID, "", tc.layer)
			if tc.layer == model.LayerModule && tc.mode == model.FreezeFast {
				node.OpenQuestions = model.OpenQuestionList{{
					ID: "otto", Text: "留给 Otto", Type: model.OQOtto, Status: model.OQOpen, CitedPaths: []string{},
				}}
				if err := st.UpdateNode(t.Context(), &node); err != nil {
					t.Fatal(err)
				}
			}
			seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
			if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			node = nodeOf(t, st, node.ID)
			qs := questionsOf(t, st, obj.ID)
			switch tc.kind {
			case freezeNow:
				if node.Status != model.NodeFrozen {
					t.Fatalf("status %s", node.Status)
				}
				if len(freezeQs(qs, node.ID)) != 0 {
					t.Fatalf("unexpected freeze question %+v", qs)
				}
				if !hasCardKind(cardsOf(t, st, obj.ID), node.ID, model.KindPlan) {
					t.Fatal("missing plan")
				}
			case freezeAsk:
				if node.Status != model.NodeFreezable {
					t.Fatalf("status %s", node.Status)
				}
				fq := freezeQs(qs, node.ID)
				if len(fq) != 1 || len(fq[0].Options) == 0 {
					t.Fatalf("freeze question %+v", fq)
				}
				svc := question.New(st, nil)
				if _, err := svc.Answer(t.Context(), fq[0].ID, ""); !errors.Is(err, errEmptyAnswer(st)) && err == nil {
					t.Fatal("empty answer accepted")
				}
				if _, err := svc.Answer(t.Context(), fq[0].ID, ""); err == nil {
					t.Fatal("empty answer accepted")
				}
				if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
				if nodeOf(t, st, node.ID).Status != model.NodeFreezable {
					t.Fatal("empty answer froze the node")
				}
				if _, err := svc.Answer(t.Context(), fq[0].ID, fq[0].Options[0]); err != nil {
					t.Fatal(err)
				}
				if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
				if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
					t.Fatal("consent did not freeze")
				}
				if !hasCardKind(cardsOf(t, st, obj.ID), node.ID, model.KindPlan) {
					t.Fatal("missing plan after consent")
				}
			case freezeReview:
				if node.Status != model.NodeFreezable {
					t.Fatalf("status %s", node.Status)
				}
				if len(freezeQs(qs, node.ID)) != 0 {
					t.Fatal("detail safe created a freeze question")
				}
				var reviewID string
				for _, c := range cardsOf(t, st, obj.ID) {
					if c.Kind == model.KindReview && c.Node == node.ID && c.Status == model.CardReady {
						reviewID = c.ID
					}
				}
				if reviewID == "" {
					t.Fatal("missing review card")
				}
				finishCard(t, st, reviewID, "## 复核结论\npass\n")
				if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
					t.Fatal(err)
				}
				if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
					t.Fatal("passed review did not freeze")
				}
				if !hasCardKind(cardsOf(t, st, obj.ID), node.ID, model.KindPlan) {
					t.Fatal("missing plan after review")
				}
			}
		})
	}
}

func errEmptyAnswer(st *store.Store) error { return nil }

func freezeQs(qs []model.Question, nodeID string) []model.Question {
	var out []model.Question
	for _, q := range qs {
		if q.Kind == model.QuestionFreeze && q.Scope == nodeID {
			out = append(out, q)
		}
	}
	return out
}

func hasCardKind(cards []model.Card, nodeID string, kind model.CardKind) bool {
	for _, c := range cards {
		if c.Node == nodeID && c.Kind == kind {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && (find(s, sub))))
}

func find(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestOpenDecisionBlocksFreeze(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	node.OpenQuestions = model.OpenQuestionList{{
		ID: "D1", Text: "还没定", Type: model.OQDecision, Status: model.OQOpen, CitedPaths: []string{},
	}}
	if err := st.UpdateNode(t.Context(), &node); err != nil {
		t.Fatal(err)
	}
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if got := nodeOf(t, st, node.ID).Status; got != model.NodeDrafting {
		t.Fatalf("status %s", got)
	}
}

func TestFreezeModeSwitchDoesNotThaw(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
	frozen := seedNode(t, st, obj.ID, "", model.LayerModule)
	seedCard(t, st, obj.ID, frozen.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, frozen.ID).Status != model.NodeFrozen {
		t.Fatal("fast module did not freeze")
	}
	obj.FreezeMode = model.FreezeSafe
	if err := st.UpdateObjective(t.Context(), &obj); err != nil {
		t.Fatal(err)
	}
	waiting := seedNode(t, st, obj.ID, "", model.LayerDetail)
	seedCard(t, st, obj.ID, waiting.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, frozen.ID).Status != model.NodeFrozen {
		t.Fatal("frozen node changed after freeze_mode switch")
	}
	if nodeOf(t, st, waiting.ID).Status != model.NodeFreezable {
		t.Fatal("safe detail should wait for review")
	}
	obj.FreezeMode = model.FreezeFast
	if err := st.UpdateObjective(t.Context(), &obj); err != nil {
		t.Fatal(err)
	}
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, waiting.ID).Status != model.NodeFrozen {
		t.Fatal("switching to fast did not freeze the waiting node")
	}
	if nodeOf(t, st, frozen.ID).Status != model.NodeFrozen {
		t.Fatal("already frozen node moved")
	}
}

func TestParallelChildrenAndDedup(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	parent := seedNode(t, st, obj.ID, "", model.LayerDirection)
	seedCard(t, st, obj.ID, parent.ID, model.KindDesign, model.CardDone, `
## 开放问题
- 要不要拆包
## 子节点
- [module] 调度
- [detail] 额度
- [module] 调度
`)
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(t.Context(), obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	var modules, details int
	var childCards []model.Card
	for _, n := range nodes {
		if n.Parent != parent.ID {
			continue
		}
		switch n.Layer {
		case model.LayerModule:
			modules++
		case model.LayerDetail:
			details++
		}
		for _, c := range cardsOf(t, st, obj.ID) {
			if c.Node == n.ID && c.Kind == model.KindDesign {
				childCards = append(childCards, c)
			}
		}
	}
	if modules != 1 || details != 1 {
		t.Fatalf("children modules=%d details=%d nodes=%d", modules, details, len(nodes))
	}
	if len(childCards) != 2 {
		t.Fatalf("child design cards %+v", childCards)
	}
	for _, c := range childCards {
		if c.Status != model.CardReady || len(c.Deps) != 0 {
			t.Fatalf("child card not independently ready %+v", c)
		}
	}
	parent = nodeOf(t, st, parent.ID)
	if len(parent.OpenQuestions) != 1 || parent.OpenQuestions[0].Type != model.OQDecision {
		t.Fatalf("question %+v", parent.OpenQuestions)
	}
	seedCard(t, st, obj.ID, parent.ID, model.KindDesign, model.CardDone, `
## 开放问题
- 要不要拆包
- [decision] 要不要拆包
## 子节点
- [module] 调度
`)
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	parent = nodeOf(t, st, parent.ID)
	if len(parent.OpenQuestions) != 1 {
		t.Fatalf("dedup failed %+v", parent.OpenQuestions)
	}
	modules = 0
	nodes, err = st.ListNodes(t.Context(), obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Parent == parent.ID && n.Layer == model.LayerModule {
			modules++
		}
	}
	if modules != 1 {
		t.Fatalf("duplicate module %d", modules)
	}
}

func TestTagsSpikeAndReopen(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, `
## 开放问题
- [fact] 仓库测试命令是什么
- [fact web] 上游文档怎么说
- [experiment] 缓存能不能撑住
- [decision] 要不要拆包
- [otto] 是否继续
- 没有标签的这条
`)
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	node = nodeOf(t, st, node.ID)
	got := map[string]model.OpenQuestion{}
	for _, q := range node.OpenQuestions {
		got[dedupKey(q.Text)] = q
	}
	if got["仓库测试命令是什么"].Type != model.OQFact || got["上游文档怎么说"].Type != model.OQFact || !isWebText(got["上游文档怎么说"].Text) {
		t.Fatalf("facts %+v", node.OpenQuestions)
	}
	if got["缓存能不能撑住"].Type != model.OQExperiment || got["要不要拆包"].Type != model.OQDecision || got["是否继续"].Type != model.OQOtto {
		t.Fatalf("kinds %+v", node.OpenQuestions)
	}
	if got["没有标签的这条"].Type != model.OQDecision {
		t.Fatalf("missing tag %+v", got["没有标签的这条"])
	}
	var spike *model.Card
	for i := range cardsOf(t, st, obj.ID) {
		c := cardsOf(t, st, obj.ID)[i]
		if c.Kind == model.KindSpike {
			spike = &c
		}
	}
	if spike == nil || spike.Status == model.CardMergeQueued {
		t.Fatalf("spike %+v", spike)
	}
	task := spike.Body
	spike.Status = model.CardMergeQueued
	spike.Body = task + "## 结论\n不能撑住\n"
	if err := st.UpdateCard(t.Context(), spike); err != nil {
		t.Fatal(err)
	}
	plantResult(t, st, spike.ID, task+"## 结论\n能撑住\n", "spike-base")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	spike2, err := st.GetCard(t.Context(), spike.ID)
	if err != nil {
		t.Fatal(err)
	}
	if spike2.Status != model.CardDone {
		t.Fatalf("spike status %s", spike2.Status)
	}
	node = nodeOf(t, st, node.ID)
	var exp model.OpenQuestion
	for _, q := range node.OpenQuestions {
		if q.Type == model.OQExperiment || dedupKey(q.Text) == "缓存能不能撑住" {
			exp = q
		}
	}
	if exp.Status != model.OQAnswered || exp.Answer != "能撑住" {
		t.Fatalf("spike write-back %+v", exp)
	}
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindResearch && c.Node == node.ID && !terminalCard(c.Status) {
			finishCard(t, st, c.ID, "## 事实\n")
		}
	}
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}

	ready := seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardReady, "还没开始")
	running := seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardRunning, "正在跑")
	seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardDone, "## 设计问题\n接口不一致\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, node.ID).Status == model.NodeReopened {
		t.Fatal("reopened before consent")
	}
	var reopen model.Question
	for _, q := range questionsOf(t, st, obj.ID) {
		if q.Kind == model.QuestionReopen && q.Scope == node.ID {
			reopen = q
		}
	}
	if reopen.ID == "" || len(reopen.Options) == 0 {
		t.Fatal("missing reopen question")
	}
	if got, err := st.GetCard(t.Context(), ready.ID); err != nil || got.Status != model.CardReady {
		t.Fatalf("ready changed %+v %v", got, err)
	}
	if got, err := st.GetCard(t.Context(), running.ID); err != nil || got.Status != model.CardRunning {
		t.Fatalf("running changed %+v %v", got, err)
	}
	if _, err := question.New(st, nil).Answer(t.Context(), reopen.ID, reopen.Options[0]); err != nil {
		t.Fatal(err)
	}
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, node.ID).Status != model.NodeReopened {
		t.Fatal("not reopened")
	}
	if got, err := st.GetCard(t.Context(), ready.ID); err != nil || got.Status != model.CardPaused {
		t.Fatalf("ready not paused %+v %v", got, err)
	}
	if got, err := st.GetCard(t.Context(), running.ID); err != nil || got.Status != model.CardRunning {
		t.Fatalf("running paused %+v %v", got, err)
	}
	if !hasCardKind(cardsOf(t, st, obj.ID), node.ID, model.KindReplan) {
		t.Fatal("missing replan")
	}
}

func TestFollowResearchDeliversFactSummary(t *testing.T) {
	for _, tc := range []struct {
		status model.NodeStatus
		kind   model.CardKind
		fact   string
	}{
		{model.NodeFrozen, model.KindPlan, "拆卡后才发现的事实"},
		{model.NodeReopened, model.KindReplan, "重规划后才发现的事实"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			st := openLoop(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			node.Status = tc.status
			if err := st.UpdateNode(t.Context(), &node); err != nil {
				t.Fatal(err)
			}
			first := seedCard(t, st, obj.ID, node.ID, tc.kind, model.CardDone, "## 开放问题\n- [fact] "+tc.fact+"\n")
			if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			researchID := readyKind(t, st, obj.ID, node.ID, model.KindResearch)
			finishCard(t, st, researchID, "## 事实\n### Q1\n结论：已核实\n证据：keep.go:1\n把握：高\n")
			if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			var follow model.Card
			n := 0
			for _, c := range cardsOf(t, st, obj.ID) {
				if c.Node != node.ID || c.Kind != tc.kind || c.Status != model.CardReady {
					continue
				}
				n++
				follow = c
			}
			if n != 1 || follow.ID == first.ID || !find(follow.Body, "## 事实摘要") || !find(follow.Body, tc.fact) {
				t.Fatalf("follow-up %+v count %d", follow, n)
			}
			if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			again := 0
			for _, c := range cardsOf(t, st, obj.ID) {
				if c.Node == node.ID && c.Kind == tc.kind && c.Status == model.CardReady {
					again++
				}
			}
			if again != 1 {
				t.Fatalf("ready %s cards %d", tc.kind, again)
			}
		})
	}
}

func TestPausedPlanDoesNotBlockReplanSummary(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	var fq model.Question
	for _, q := range questionsOf(t, st, obj.ID) {
		if q.Kind == model.QuestionFreeze && q.Scope == node.ID {
			fq = q
		}
	}
	if fq.ID == "" || len(fq.Options) == 0 {
		t.Fatal("missing freeze question")
	}
	if _, err := question.New(st, nil).Answer(t.Context(), fq.ID, fq.Options[0]); err != nil {
		t.Fatal(err)
	}
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
		t.Fatal("module did not freeze")
	}
	planID := readyKind(t, st, obj.ID, node.ID, model.KindPlan)
	seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardDone, "## 设计问题\n接口不一致\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, node.ID).Status == model.NodeReopened {
		t.Fatal("reopened before consent")
	}
	var reopen model.Question
	for _, q := range questionsOf(t, st, obj.ID) {
		if q.Kind == model.QuestionReopen && q.Scope == node.ID {
			reopen = q
		}
	}
	if reopen.ID == "" || len(reopen.Options) == 0 {
		t.Fatal("missing reopen question")
	}
	if _, err := question.New(st, nil).Answer(t.Context(), reopen.ID, reopen.Options[0]); err != nil {
		t.Fatal(err)
	}
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, node.ID).Status != model.NodeReopened {
		t.Fatal("not reopened")
	}
	if got, err := st.GetCard(t.Context(), planID); err != nil || got.Status != model.CardPaused {
		t.Fatalf("plan not paused %+v %v", got, err)
	}
	replanID := readyKind(t, st, obj.ID, node.ID, model.KindReplan)
	const fact = "重开后要核实的事实"
	finishCard(t, st, replanID, "## 开放问题\n- [fact] "+fact+"\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	researchID := readyKind(t, st, obj.ID, node.ID, model.KindResearch)
	finishCard(t, st, researchID, "## 事实\n### Q1\n结论：已核实\n证据：keep.go:1\n把握：高\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	var follow model.Card
	n := 0
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Node != node.ID || c.Kind != model.KindReplan || c.Status != model.CardReady {
			continue
		}
		n++
		follow = c
	}
	if n != 1 || follow.ID == replanID || !find(follow.Body, "## 事实摘要") || !find(follow.Body, fact) {
		t.Fatalf("replan summary %+v count %d", follow, n)
	}
	if got, err := st.GetCard(t.Context(), planID); err != nil || got.Status != model.CardPaused {
		t.Fatalf("paused plan changed %+v %v", got, err)
	}
}

func TestConsentDoesNotFreezeWhileBlocked(t *testing.T) {
	t.Run("consent", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
		if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFreezable {
			t.Fatal("module did not become freezable")
		}
		var fq model.Question
		for _, q := range questionsOf(t, st, obj.ID) {
			if q.Kind == model.QuestionFreeze && q.Scope == node.ID {
				fq = q
			}
		}
		if fq.ID == "" || len(fq.Options) == 0 {
			t.Fatal("missing freeze question")
		}
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- 接口要不要改\n")
		if _, err := question.New(st, nil).Answer(t.Context(), fq.ID, fq.Options[0]); err != nil {
			t.Fatal(err)
		}
		if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		got := nodeOf(t, st, node.ID)
		if got.Status == model.NodeFrozen {
			t.Fatal("froze while a decision was open")
		}
		open := false
		for _, q := range got.OpenQuestions {
			if q.Type == model.OQDecision && q.Status == model.OQOpen {
				open = true
			}
		}
		if !open {
			t.Fatalf("decision missing %+v", got.OpenQuestions)
		}
	})
	t.Run("fast", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
		if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFreezable {
			t.Fatal("module did not become freezable")
		}
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- [fact] 还在查的事实\n")
		obj.FreezeMode = model.FreezeFast
		if err := st.UpdateObjective(t.Context(), &obj); err != nil {
			t.Fatal(err)
		}
		if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		got := nodeOf(t, st, node.ID)
		if got.Status == model.NodeFrozen {
			t.Fatal("fast froze while a fact was researching")
		}
		researching := false
		for _, q := range got.OpenQuestions {
			if q.Type == model.OQFact && q.Status == model.OQResearching {
				researching = true
			}
		}
		if !researching {
			t.Fatalf("fact not researching %+v", got.OpenQuestions)
		}
	})
	t.Run("review", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerDetail)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
		if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFreezable {
			t.Fatal("detail did not become freezable")
		}
		reviewID := readyKind(t, st, obj.ID, node.ID, model.KindReview)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- 接口要不要改\n")
		finishCard(t, st, reviewID, "## 复核结论\npass\n")
		if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status == model.NodeFrozen {
			t.Fatal("review pass froze while a decision was open")
		}
	})
}
