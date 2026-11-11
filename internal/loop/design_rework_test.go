package loop

import (
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/question"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestAbsorbResultFileNotCardBody(t *testing.T) {
	t.Run("inventory", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		inv := cardsOf(t, st, obj.ID)[0]
		inv.Status = model.CardDone
		inv.Body = "## 事实\n### Q0\n结论：正文结论\n证据：body.go:1\n把握：高\n"
		if err := st.UpdateCard(t.Context(), &inv); err != nil {
			t.Fatal(err)
		}
		plantResult(t, st, inv.ID, "## 事实\n### Q0\n结论：文件结论\n证据：file.go:1\n把握：高\n", "inv-base")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		n := nodesOf(t, st, obj.ID)[0]
		q := findQ(n, "Q0")
		if q.Answer != "文件结论" || q.Status != model.OQAnswered {
			t.Fatalf("Q0 %+v", q)
		}
		if got, err := st.GetCard(t.Context(), inv.ID); err != nil || !find(got.Body, "正文结论") {
			t.Fatalf("body %q %v", got.Body, err)
		}
	})

	for _, kind := range []model.CardKind{model.KindDesign, model.KindPlan, model.KindReplan} {
		t.Run(string(kind), func(t *testing.T) {
			st := openLoop(t)
			obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			card := seedCard(t, st, obj.ID, node.ID, kind, model.CardReady, "待做\n")
			card.Status = model.CardDone
			card.Body = "## 开放问题\n- 正文问题\n"
			if err := st.UpdateCard(t.Context(), &card); err != nil {
				t.Fatal(err)
			}
			plantResult(t, st, card.ID, "## 开放问题\n- 文件问题\n", "card-base")
			if err := loopStep(t, depsOf(st), obj.ID); err != nil {
				t.Fatal(err)
			}
			got := nodeOf(t, st, node.ID)
			if len(got.OpenQuestions) != 1 || got.OpenQuestions[0].Text != "文件问题" {
				t.Fatalf("questions %+v", got.OpenQuestions)
			}
			if body, err := st.GetCard(t.Context(), card.ID); err != nil || !find(body.Body, "正文问题") {
				t.Fatalf("body %+v %v", body.Body, err)
			}
		})
	}

	t.Run("research", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- [fact] 要核实的事实\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		id := readyResearch(t, st, obj.ID, node.ID)
		card, err := st.GetCard(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		card.Status = model.CardDone
		card.Base = "created-base"
		card.Body = "## 事实\n### Q1\n结论：正文答案\n证据：body.go:1\n把握：高\n"
		if err := st.UpdateCard(t.Context(), &card); err != nil {
			t.Fatal(err)
		}
		plantResult(t, st, id, "## 事实\n### Q1\n结论：文件答案\n证据：file.go:1\n把握：高\n", "dispatched-base")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		q := findQ(nodeOf(t, st, node.ID), "Q1")
		if q.Answer != "文件答案" || q.Commit != "dispatched-base" || q.Status != model.OQAnswered {
			t.Fatalf("research %+v", q)
		}
	})

	t.Run("spike", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- [experiment] 缓存能不能撑住\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		var spike model.Card
		for _, c := range cardsOf(t, st, obj.ID) {
			if c.Kind == model.KindSpike {
				spike = c
			}
		}
		if spike.ID == "" {
			t.Fatal("missing spike")
		}
		task := spike.Body
		spike.Status = model.CardDone
		spike.Body = task + "## 结论\n正文结论\n"
		if err := st.UpdateCard(t.Context(), &spike); err != nil {
			t.Fatal(err)
		}
		plantResult(t, st, spike.ID, task+"## 结论\n文件结论\n", "spike-base")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		q := findQ(nodeOf(t, st, node.ID), "Q1")
		if q.Answer != "文件结论" || q.Status != model.OQAnswered {
			t.Fatalf("spike %+v", q)
		}
	})

	t.Run("dev", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		card := seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardReady, "实现功能\n")
		card.Status = model.CardDone
		card.Body = "## 设计问题\n正文问题\n"
		if err := st.UpdateCard(t.Context(), &card); err != nil {
			t.Fatal(err)
		}
		plantResult(t, st, card.ID, "## 设计问题\n文件问题\n", "dev-base")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		var text string
		for _, q := range questionsOf(t, st, obj.ID) {
			if q.Kind == model.QuestionReopen && q.Scope == node.ID {
				text = q.Text
			}
		}
		if text != "文件问题" {
			t.Fatalf("reopen text %q", text)
		}
	})

	t.Run("review", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerDetail)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		id := readyKind(t, st, obj.ID, node.ID, model.KindReview)
		card, err := st.GetCard(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		card.Status = model.CardDone
		card.Body = "## 复核结论\nfail\n"
		if err := st.UpdateCard(t.Context(), &card); err != nil {
			t.Fatal(err)
		}
		plantResult(t, st, id, "## 复核结论\npass\n", "review-base")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
			t.Fatal("file pass did not freeze")
		}
		if got, err := st.GetCard(t.Context(), id); err != nil || !find(got.Body, "fail") {
			t.Fatalf("body %q %v", got.Body, err)
		}
	})
}

func TestUnconvergedNodeKeepsMoving(t *testing.T) {
	t.Run("decision-only", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- 只问一个决定\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if n := readyCount(t, st, obj.ID, node.ID, model.KindDesign); n != 1 {
			t.Fatalf("ready design %d", n)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if n := readyCount(t, st, obj.ID, node.ID, model.KindDesign); n != 1 {
			t.Fatalf("second step ready design %d", n)
		}
	})

	t.Run("spike", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, `
## 开放问题
- [fact] 还在查
- [experiment] 试一下
- 还没决定
`)
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if readyCount(t, st, obj.ID, node.ID, model.KindDesign) != 0 {
			t.Fatal("design while research is unfinished")
		}
		var spikeID string
		for _, c := range cardsOf(t, st, obj.ID) {
			if c.Kind == model.KindSpike && c.Node == node.ID {
				spikeID = c.ID
			}
			if c.Kind == model.KindResearch && c.Node == node.ID && !terminalCard(c.Status) {
				finishCard(t, st, c.ID, "## 事实\n### Q1\n结论：已核实\n证据：keep.go:1\n把握：高\n")
			}
		}
		if spikeID == "" {
			t.Fatal("missing spike")
		}
		spike, err := st.GetCard(t.Context(), spikeID)
		if err != nil {
			t.Fatal(err)
		}
		plantResult(t, st, spikeID, spike.Body+"## 结论\n能撑住\n", "spike-base")
		spike.Status = model.CardDone
		if err := st.UpdateCard(t.Context(), &spike); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if readyCount(t, st, obj.ID, node.ID, model.KindDesign) != 1 {
			t.Fatalf("design after spike %d", readyCount(t, st, obj.ID, node.ID, model.KindDesign))
		}
		if nodeOf(t, st, node.ID).Status == model.NodeFrozen || nodeOf(t, st, node.ID).Status == model.NodeFreezable {
			t.Fatal("spike left the node converged")
		}
	})

	t.Run("second-recheck", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- [fact] 端口是多少\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		finishCard(t, st, readyResearch(t, st, obj.ID, node.ID), "## 事实\n### Q1\n结论：8787\n证据：docs/v2/design.md:1\n把握：高\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		finishCard(t, st, readyKind(t, st, obj.ID, node.ID, model.KindDesign), "## 开放问题\n- [fact] Q1 复查：证据指向了设计文档\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		finishCard(t, st, readyResearch(t, st, obj.ID, node.ID), "## 事实\n### Q1\n结论：仍然是 8787\n证据：docs/v2/design.md:2\n把握：中\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		finishCard(t, st, readyKind(t, st, obj.ID, node.ID, model.KindDesign), "## 开放问题\n- [fact] Q1 复查：还是不对\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		q := findQ(nodeOf(t, st, node.ID), "Q1")
		if q.Type != model.OQDecision || q.Asks != 2 {
			t.Fatalf("decision %+v", q)
		}
		if readyCount(t, st, obj.ID, node.ID, model.KindDesign) != 1 {
			t.Fatal("no design after the question became a decision")
		}
		for _, c := range cardsOf(t, st, obj.ID) {
			if c.Kind == model.KindResearch && c.Node == node.ID && !terminalCard(c.Status) {
				t.Fatalf("research after decision %+v", c)
			}
		}
	})

	t.Run("unanswered-human", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		if _, err := question.New(st, nil).Create(t.Context(), model.Question{
			Objective: obj.ID, Scope: node.ID, ScopeKind: "node", Kind: model.QuestionCustom,
			Text: "等人决定", Options: []string{"同意", "拒绝"},
		}); err != nil {
			t.Fatal(err)
		}
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- 只问一个决定\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if readyCount(t, st, obj.ID, node.ID, model.KindDesign) != 0 {
			t.Fatal("design while a human question is unanswered")
		}
	})
}

func TestReopenRoundConsumesConsent(t *testing.T) {
	t.Run("historical-and-freeze", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		fq := freezeQs(questionsOf(t, st, obj.ID), node.ID)
		if len(fq) != 1 {
			t.Fatal("missing first freeze question")
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
		seedCard(t, st, obj.ID, node.ID, model.KindReplan, model.CardDone, "历史重规划已经结束\n")
		report := seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardReady, "实现功能\n")
		report.Status = model.CardDone
		report.Body = "## 设计问题\n正文问题\n"
		if err := st.UpdateCard(t.Context(), &report); err != nil {
			t.Fatal(err)
		}
		plantResult(t, st, report.ID, "## 设计问题\n接口不一致\n", "dev-base")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status == model.NodeReopened {
			t.Fatal("reopened before consent")
		}
		reopen := reopenQ(t, st, obj.ID, node.ID)
		if reopen.Text != "接口不一致" {
			t.Fatalf("reopen %+v", reopen)
		}
		if _, err := question.New(st, nil).Answer(t.Context(), reopen.ID, reopen.Options[0]); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeReopened {
			t.Fatal("not reopened")
		}
		if n := countKind(t, st, obj.ID, node.ID, model.KindReplan); n != 2 {
			t.Fatalf("replans %d", n)
		}
		if readyCount(t, st, obj.ID, node.ID, model.KindReplan) != 1 {
			t.Fatal("historical replan blocked the new one")
		}
		finishCard(t, st, readyKind(t, st, obj.ID, node.ID, model.KindReplan), "## 可冻结\n是\n")
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFreezable {
			t.Fatalf("status %s", nodeOf(t, st, node.ID).Status)
		}
		fresh := freezeQs(questionsOf(t, st, obj.ID), node.ID)
		var next model.Question
		for _, q := range fresh {
			if q.ID != fq[0].ID && q.Answer == "" {
				next = q
			}
		}
		if next.ID == "" {
			t.Fatalf("no new freeze question %+v", fresh)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFreezable {
			t.Fatal("same consent reopened or the old freeze consent froze the node")
		}
		if _, err := question.New(st, nil).Answer(t.Context(), next.ID, next.Options[0]); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
			t.Fatal("new consent did not freeze")
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
			t.Fatal("consumed reopen consent opened the node again")
		}
	})

	t.Run("in-flight-replan", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		node.Status = model.NodeFrozen
		if err := st.UpdateNode(t.Context(), &node); err != nil {
			t.Fatal(err)
		}
		running := seedCard(t, st, obj.ID, node.ID, model.KindReplan, model.CardRunning, "正在重规划\n")
		seedCard(t, st, obj.ID, node.ID, model.KindDev, model.CardDone, "## 设计问题\n接口不一致\n")
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
		if n := countKind(t, st, obj.ID, node.ID, model.KindReplan); n != 1 {
			t.Fatalf("replans %d", n)
		}
		got, err := st.GetCard(t.Context(), running.ID)
		if err != nil || got.Status != model.CardRunning {
			t.Fatalf("in-flight replan %+v %v", got, err)
		}
	})

	t.Run("old-freeze-does-not-reopen", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		node.Status = model.NodeFrozen
		if err := st.UpdateNode(t.Context(), &node); err != nil {
			t.Fatal(err)
		}
		created, err := question.New(st, nil).Create(t.Context(), model.Question{
			Objective: obj.ID, Scope: node.ID, ScopeKind: "node", Kind: model.QuestionFreeze,
			Text: "旧的冻结确认", Options: []string{"同意", "拒绝"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := question.New(st, nil).Answer(t.Context(), created.ID, created.Options[0]); err != nil {
			t.Fatal(err)
		}
		if err := loopStep(t, depsOf(st), obj.ID); err != nil {
			t.Fatal(err)
		}
		if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
			t.Fatal("old freeze consent reopened the node")
		}
	})
}

func TestCreatedNodesHaveDocPath(t *testing.T) {
	t.Run("project-dir", func(t *testing.T) {
		st := openLoop(t)
		proj := &model.Project{RepoPath: t.TempDir(), DesignDir: "docs/design/"}
		if err := st.CreateProject(t.Context(), proj); err != nil {
			t.Fatal(err)
		}
		obj := seedObjective(t, st, proj.ID, model.ModeAuto, model.FreezeSafe)
		assertDocTree(t, st, obj)
	})
	t.Run("default-dir", func(t *testing.T) {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
		assertDocTree(t, st, obj)
	})
}

func assertDocTree(t *testing.T, st *store.Store, obj model.Objective) {
	t.Helper()
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	inv := cardsOf(t, st, obj.ID)[0]
	finishCard(t, st, inv.ID, "## 事实\n### Q0\n结论：有仓库\n证据：go.mod:1\n把握：高\n")
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	var direction model.DesignNode
	for _, n := range nodesOf(t, st, obj.ID) {
		if n.Layer == model.LayerDirection {
			direction = n
		}
	}
	if direction.ID == "" || direction.DocPath != "docs/design/"+obj.ID+"/"+direction.ID+".md" {
		t.Fatalf("direction path %q id %s", direction.DocPath, direction.ID)
	}
	designID := readyKind(t, st, obj.ID, direction.ID, model.KindDesign)
	finishCard(t, st, designID, "## 子节点\n- [module] 调度\n- [detail] 额度\n")
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	var module, detail model.DesignNode
	for _, n := range nodesOf(t, st, obj.ID) {
		if n.Parent != direction.ID {
			continue
		}
		switch n.Layer {
		case model.LayerModule:
			module = n
		case model.LayerDetail:
			detail = n
		}
	}
	if module.DocPath != "docs/design/"+obj.ID+"/"+module.ID+".md" {
		t.Fatalf("module path %q", module.DocPath)
	}
	if detail.DocPath != "docs/design/"+obj.ID+"/"+detail.ID+".md" {
		t.Fatalf("detail path %q", detail.DocPath)
	}
}

func TestReviewTracksCurrentDesign(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerDetail)
	first := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
	first.AuthorFamily = "family-a"
	first.Body = "任务里写不可冻结\n"
	if err := st.UpdateCard(t.Context(), &first); err != nil {
		t.Fatal(err)
	}
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	reviewA := readyKind(t, st, obj.ID, node.ID, model.KindReview)
	got, err := st.GetCard(t.Context(), reviewA)
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthorFamily != "family-a" {
		t.Fatalf("author family %q", got.AuthorFamily)
	}
	if len(got.Deps) == 0 || got.Deps[0] != first.ID {
		t.Fatalf("review deps %+v want design %s", got.Deps, first.ID)
	}
	if err := st.AppendEvent(t.Context(), &model.Event{Entity: "node:" + node.ID, Type: evReviewPass, Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	other := seedCard(t, st, obj.ID, node.ID, model.KindReview, model.CardReady, "另一张复核\n")
	other.Status = model.CardDone
	other.Body = "## 复核结论\npass\n"
	if err := st.UpdateCard(t.Context(), &other); err != nil {
		t.Fatal(err)
	}
	plantResult(t, st, other.ID, "## 复核结论\npass\n", "other-base")
	second := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 可冻结\n是\n")
	second.AuthorFamily = "family-b"
	if err := st.UpdateCard(t.Context(), &second); err != nil {
		t.Fatal(err)
	}
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, node.ID).Status == model.NodeFrozen {
		t.Fatal("old pass or unrelated review froze the node")
	}
	var reviewB string
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindReview && c.Node == node.ID && c.ID != reviewA && c.ID != other.ID && c.Status == model.CardReady {
			reviewB = c.ID
			if c.AuthorFamily != "family-b" {
				t.Fatalf("second family %q", c.AuthorFamily)
			}
			if len(c.Deps) == 0 || c.Deps[0] != second.ID {
				t.Fatalf("second review deps %+v want %s", c.Deps, second.ID)
			}
		}
	}
	if reviewB == "" {
		t.Fatal("missing review for the current design")
	}
	finishCard(t, st, reviewA, "## 复核结论\npass\n")
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, node.ID).Status == model.NodeFrozen {
		t.Fatal("earlier design review froze the new round")
	}
	finishCard(t, st, reviewB, "## 复核结论\npass\n")
	if err := loopStep(t, depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	if nodeOf(t, st, node.ID).Status != model.NodeFrozen {
		t.Fatal("current design review did not freeze")
	}
}

func readyCount(t *testing.T, st *store.Store, objective, node string, kind model.CardKind) int {
	t.Helper()
	n := 0
	for _, c := range cardsOf(t, st, objective) {
		if c.Node == node && c.Kind == kind && c.Status == model.CardReady {
			n++
		}
	}
	return n
}

func countKind(t *testing.T, st *store.Store, objective, node string, kind model.CardKind) int {
	t.Helper()
	n := 0
	for _, c := range cardsOf(t, st, objective) {
		if c.Node == node && c.Kind == kind {
			n++
		}
	}
	return n
}

func nodesOf(t *testing.T, st *store.Store, objective string) []model.DesignNode {
	t.Helper()
	nodes, err := st.ListNodes(t.Context(), objective)
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func reopenQ(t *testing.T, st *store.Store, objective, node string) model.Question {
	t.Helper()
	for _, q := range questionsOf(t, st, objective) {
		if q.Kind == model.QuestionReopen && q.Scope == node {
			return q
		}
	}
	t.Fatal("missing reopen question")
	return model.Question{}
}
