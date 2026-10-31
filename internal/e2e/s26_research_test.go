package e2e

import (
	"context"
	"strings"
	"testing"

	"cardex/internal/loop"
	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/workspace"
)

// quietDeliver is the notify stand-in for design-phase scenarios.
// Delivery is recorded by question.Service tests; these scenarios only need Create to succeed.
type quietDeliver struct{}

func (quietDeliver) Deliver(context.Context, notify.Message) error { return nil }

func e2eNode(t *testing.T, h *Harness, id string) model.DesignNode {
	t.Helper()
	n, err := h.DB.GetNode(t.Context(), id)
	h.NoError(err)
	return n
}

func e2eCard(t *testing.T, h *Harness, id string) model.Card {
	t.Helper()
	c, err := h.DB.GetCard(t.Context(), id)
	h.NoError(err)
	return c
}

func e2eCards(t *testing.T, h *Harness, objective string) []model.Card {
	t.Helper()
	cards, err := h.DB.ListCards(t.Context(), objective)
	h.NoError(err)
	return cards
}

func e2eNodes(t *testing.T, h *Harness, objective string) []model.DesignNode {
	t.Helper()
	nodes, err := h.DB.ListNodes(t.Context(), objective)
	h.NoError(err)
	return nodes
}

func e2eHasKind(t *testing.T, h *Harness, objective, node string, kind model.CardKind) bool {
	t.Helper()
	for _, c := range e2eCards(t, h, objective) {
		if c.Node == node && c.Kind == kind {
			return true
		}
	}
	return false
}

func e2eQuestionKind(t *testing.T, h *Harness, objective, scope string, kind model.QuestionKind) model.Question {
	t.Helper()
	qs, err := h.DB.ListQuestions(t.Context(), objective)
	h.NoError(err)
	for _, q := range qs {
		if q.Scope == scope && q.Kind == kind {
			return q
		}
	}
	return model.Question{}
}

func e2eFreezeQuestion(t *testing.T, h *Harness, objective, node string) model.Question {
	t.Helper()
	return e2eQuestionKind(t, h, objective, node, model.QuestionFreeze)
}

func e2eReady(t *testing.T, h *Harness, objective, node string, kind model.CardKind) []model.Card {
	t.Helper()
	var out []model.Card
	for _, c := range e2eCards(t, h, objective) {
		if c.Node == node && c.Kind == kind && c.Status == model.CardReady {
			out = append(out, c)
		}
	}
	return out
}

func e2eFinish(t *testing.T, h *Harness, id, body string) {
	t.Helper()
	c := e2eCard(t, h, id)
	c.Status = model.CardDone
	c.Body = body
	h.NoError(h.DB.UpdateCard(t.Context(), &c))
}

// e2eFinishResearch replaces a research card body with a ## 事实 block for every Q<id> it listed.
func e2eFinishResearch(t *testing.T, h *Harness, id string) {
	t.Helper()
	c := e2eCard(t, h, id)
	var b strings.Builder
	b.WriteString("## 事实\n")
	n := 0
	for _, line := range strings.Split(c.Body, "\n") {
		line = strings.TrimSpace(line)
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		qid := strings.TrimSpace(line[:colon])
		if !strings.HasPrefix(qid, "Q") {
			continue
		}
		b.WriteString("### " + qid + "\n")
		b.WriteString("结论：已核实 " + qid + "\n")
		b.WriteString("证据：answer.go:1\n")
		b.WriteString("把握：高\n")
		n++
	}
	if n == 0 {
		t.Fatalf("research card %s lists no questions:\n%s", id, c.Body)
	}
	e2eFinish(t, h, id, b.String())
}

func TestS26(t *testing.T) {
	// 盘点卡给出 Q0，设计卡提出事实，研究卡并行吸收后再复查，方向节点才可冻结。
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	ctx := t.Context()
	project := &model.Project{RepoPath: h.Repo, TestCmd: h.TestCmd}
	h.NoError(h.DB.CreateProject(ctx, project))
	obj := &model.Objective{
		Project: project.ID, Title: "S26", Mode: model.ModeAuto, FreezeMode: model.FreezeSafe,
		Status: model.ObjectiveActive, Origin: "codex:s26",
	}
	h.NoError(h.DB.CreateObjective(ctx, obj))
	eng := loop.New(h.DB, workspace.Git{}, quietDeliver{})

	h.NoError(eng.Step(ctx, obj.ID))
	h.NoError(eng.Step(ctx, obj.ID))
	if len(e2eNodes(t, h, obj.ID)) != 0 {
		t.Fatal("direction node exists before the inventory card is terminal")
	}
	cards := e2eCards(t, h, obj.ID)
	if len(cards) != 1 || cards[0].Kind != model.KindResearch || cards[0].Title != "盘点" || cards[0].Node != "" || cards[0].Status != model.CardReady {
		t.Fatalf("inventory card %+v", cards)
	}
	if !strings.Contains(cards[0].Body, "Q0") {
		t.Fatalf("inventory body %s", cards[0].Body)
	}

	e2eFinish(t, h, cards[0].ID, "## 事实\n### Q0\n结论：仓库里有 answer.go\n证据：answer.go:1\n把握：高\n")
	h.NoError(eng.Step(ctx, obj.ID))
	nodes := e2eNodes(t, h, obj.ID)
	if len(nodes) != 1 || nodes[0].Layer != model.LayerDirection {
		t.Fatalf("nodes %+v", nodes)
	}
	direction := nodes[0]
	var q0 model.OpenQuestion
	for _, q := range direction.OpenQuestions {
		if q.ID == "Q0" {
			q0 = q
		}
	}
	if q0.Status != model.OQAnswered || !strings.Contains(q0.Answer, "仓库里有 answer.go") {
		t.Fatalf("Q0 %+v", q0)
	}
	designs := e2eReady(t, h, obj.ID, direction.ID, model.KindDesign)
	if len(designs) != 1 || designs[0].Title != "设计方向" {
		t.Fatalf("direction design %+v", designs)
	}
	if !strings.Contains(designs[0].Body, "## 事实摘要") || !strings.Contains(designs[0].Body, "仓库里有 answer.go") {
		t.Fatalf("direction design body %s", designs[0].Body)
	}

	e2eFinish(t, h, designs[0].ID, `## 开放问题
- [fact] 模块入口在哪
- [fact] 测试命令是什么
- [fact] 现有文档在哪
- [fact web] 上游接口是否变更
`)
	h.NoError(eng.Step(ctx, obj.ID))
	research := e2eReady(t, h, obj.ID, direction.ID, model.KindResearch)
	if len(research) != 2 {
		t.Fatalf("research cards %+v", research)
	}
	var web, plain int
	for _, c := range research {
		if c.Tier != model.TierT1 || len(c.Deps) != 0 {
			t.Fatalf("research card %+v", c)
		}
		if containsReq(c.Requires, "web") {
			web++
		} else {
			plain++
		}
	}
	if web != 1 || plain != 1 {
		t.Fatalf("web=%d plain=%d cards %+v", web, plain, research)
	}
	direction = e2eNode(t, h, direction.ID)
	if direction.Status == model.NodeFreezable || direction.Status == model.NodeFrozen {
		t.Fatalf("node %s before research finished", direction.Status)
	}
	if len(e2eReady(t, h, obj.ID, direction.ID, model.KindDesign)) != 0 {
		t.Fatal("design card while research is unfinished")
	}

	e2eFinishResearch(t, h, research[0].ID)
	h.NoError(eng.Step(ctx, obj.ID))
	if len(e2eReady(t, h, obj.ID, direction.ID, model.KindDesign)) != 0 {
		t.Fatal("follow-up design while one research card is still open")
	}
	if e2eCard(t, h, research[1].ID).Status != model.CardReady {
		t.Fatal("second research card did not stay ready")
	}

	e2eFinishResearch(t, h, research[1].ID)
	h.NoError(eng.Step(ctx, obj.ID))
	follow := e2eReady(t, h, obj.ID, direction.ID, model.KindDesign)
	if len(follow) != 1 {
		t.Fatalf("follow-up design %+v", follow)
	}
	body := follow[0].Body
	for _, text := range []string{"## 事实摘要", "仓库里有 answer.go", "模块入口在哪", "测试命令是什么", "现有文档在哪", "上游接口是否变更"} {
		if !strings.Contains(body, text) {
			t.Fatalf("fact summary missing %q\n%s", text, body)
		}
	}
	direction = e2eNode(t, h, direction.ID)
	if direction.Status == model.NodeFreezable {
		t.Fatal("freezable before a design card proposes it")
	}
	var recheck model.OpenQuestion
	for _, q := range direction.OpenQuestions {
		if q.ID == "Q0" || q.Type != model.OQFact || strings.HasPrefix(strings.ToLower(strings.TrimSpace(q.Text)), "[fact web]") {
			continue
		}
		recheck = q
		break
	}
	if recheck.ID == "" {
		t.Fatal("no fact to recheck")
	}

	e2eFinish(t, h, follow[0].ID, "## 开放问题\n- [fact] "+recheck.ID+" 复查：需要更高一级再查\n## 可冻结\n是\n")
	h.NoError(eng.Step(ctx, obj.ID))
	again := e2eReady(t, h, obj.ID, direction.ID, model.KindResearch)
	if len(again) != 1 || again[0].Tier != model.TierT2 || !strings.Contains(again[0].Body, recheck.ID+":") {
		t.Fatalf("recheck card %+v", again)
	}
	direction = e2eNode(t, h, direction.ID)
	if direction.Status == model.NodeFreezable || direction.Status == model.NodeFrozen {
		t.Fatalf("node %s while recheck is still researching", direction.Status)
	}
	var found bool
	for _, q := range direction.OpenQuestions {
		if q.ID != recheck.ID {
			continue
		}
		found = true
		if q.Asks != 1 || q.Type != model.OQFact || q.Status != model.OQResearching {
			t.Fatalf("recheck question %+v", q)
		}
	}
	if !found {
		t.Fatal("recheck question disappeared")
	}

	e2eFinishResearch(t, h, again[0].ID)
	h.NoError(eng.Step(ctx, obj.ID))
	direction = e2eNode(t, h, direction.ID)
	h.Equal(direction.Status, model.NodeFreezable)
	if e2eFreezeQuestion(t, h, obj.ID, direction.ID).ID == "" {
		t.Fatal("direction became freezable without a freeze question")
	}
}

func containsReq(reqs []string, want string) bool {
	for _, r := range reqs {
		if r == want {
			return true
		}
	}
	return false
}
