package loop

import (
	"context"
	"os"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/store"
	"cardex/internal/workspace"
)

func TestResearchGroupsFiveAndWeb(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerDirection)
	var b strings.Builder
	b.WriteString("## 开放问题\n")
	for i := 0; i < 6; i++ {
		b.WriteString("- [fact] 事实")
		b.WriteByte(byte('0' + i))
		b.WriteByte('\n')
	}
	b.WriteString("- [fact web] 网页问题\n")
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, b.String())
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	var research, readyDesign int
	var web, plain []model.Card
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindResearch && c.Node == node.ID {
			research++
			if containsReq(c.Requires, "web") {
				web = append(web, c)
			} else {
				plain = append(plain, c)
			}
			if c.Status != model.CardReady || c.Tier != model.TierT1 {
				t.Fatalf("research card %+v", c)
			}
		}
		if c.Kind == model.KindDesign && c.Node == node.ID && c.Status == model.CardReady {
			readyDesign++
		}
	}
	if research != 3 || len(web) != 1 || len(plain) != 2 {
		t.Fatalf("research=%d web=%d plain=%d", research, len(web), len(plain))
	}
	if readyDesign != 0 {
		t.Fatal("design dispatched while research is unfinished")
	}
	counts := []int{questionLineCount(plain[0].Body), questionLineCount(plain[1].Body)}
	if !sameSet(counts, []int{5, 1}) {
		t.Fatalf("group sizes %v", counts)
	}
	if questionLineCount(web[0].Body) != 1 || !find(web[0].Body, "网页问题") {
		t.Fatalf("web body %s", web[0].Body)
	}
	node = nodeOf(t, st, node.ID)
	for _, q := range node.OpenQuestions {
		if q.Type == model.OQFact && q.Status != model.OQResearching {
			t.Fatalf("fact not researching %+v", q)
		}
	}
}

func TestIncompleteResearchParse(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, `
## 开放问题
- [fact] 有结论没证据
- [fact] 只有证据
- [fact] 完全没写
`)
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	var researchID string
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindResearch && c.Node == node.ID {
			researchID = c.ID
		}
	}
	if researchID == "" {
		t.Fatal("missing research card")
	}
	finishCard(t, st, researchID, `
## 事实
### Q1
结论：有
把握：高
### Q2
证据：readme.md:3
把握：高
`)
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	node = nodeOf(t, st, node.ID)
	byID := map[string]model.OpenQuestion{}
	for _, q := range node.OpenQuestions {
		byID[q.ID] = q
	}
	if byID["Q1"].Status != model.OQAnswered || byID["Q1"].Confidence != "低" || byID["Q1"].Answer != "有" {
		t.Fatalf("Q1 %+v", byID["Q1"])
	}
	if byID["Q2"].Status != model.OQUnanswered || byID["Q3"].Status != model.OQUnanswered {
		t.Fatalf("unanswered %+v %+v", byID["Q2"], byID["Q3"])
	}
	var summary string
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindDesign && c.Node == node.ID && c.Status == model.CardReady {
			summary = c.Body
		}
	}
	if !find(summary, "未能核实") || !find(summary, "有结论没证据") {
		t.Fatalf("summary %s", summary)
	}
}

func TestStaleUsesAttemptBaseAgainstIntegrationHead(t *testing.T) {
	repo := t.TempDir()
	st := openLoop(t)
	proj := &model.Project{RepoPath: repo, TestCmd: "true", DesignDir: "docs"}
	if err := st.CreateProject(t.Context(), proj); err != nil {
		t.Fatal(err)
	}
	obj := seedObjective(t, st, proj.ID, model.ModeAuto, model.FreezeSafe)
	obj.IntegrationBranch = "cardex/test/main"
	if err := st.UpdateObjective(t.Context(), &obj); err != nil {
		t.Fatal(err)
	}
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, `
## 开放问题
- [fact] 只在集成分支上变了
- [fact] 只在主 checkout 里脏
- [fact] 保持不变的文件
`)
	fake := &compareFake{
		head: "head-now",
		files: map[string][]string{
			"attempt-base→head-now": {"only-on-integration.go"},
		},
		dirty: []string{"dirty-main.go"},
	}
	d := Deps{Store: st, WS: fake}
	if err := loopStep(t, d, obj.ID); err != nil {
		t.Fatal(err)
	}
	var researchID string
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindResearch && c.Node == node.ID {
			researchID = c.ID
			c.Base = "created-base"
			if err := st.UpdateCard(t.Context(), &c); err != nil {
				t.Fatal(err)
			}
		}
	}
	if researchID == "" {
		t.Fatal("missing research card")
	}
	card, err := st.GetCard(t.Context(), researchID)
	if err != nil {
		t.Fatal(err)
	}
	card.Status = model.CardDone
	card.Body = "任务提示，与结果不同\n"
	if err := st.UpdateCard(t.Context(), &card); err != nil {
		t.Fatal(err)
	}
	plantResult(t, st, researchID, `
## 事实
### Q1
结论：旧内容
证据：only-on-integration.go:1
把握：高
### Q2
结论：脏文件
证据：dirty-main.go:1
把握：高
### Q3
结论：还在
证据：keep.go:1
把握：高
`, "attempt-base")
	if err := loopStep(t, d, obj.ID); err != nil {
		t.Fatal(err)
	}
	node = nodeOf(t, st, node.ID)
	byID := map[string]model.OpenQuestion{}
	for _, q := range node.OpenQuestions {
		byID[q.ID] = q
	}
	if byID["Q1"].Commit != "attempt-base" || byID["Q1"].Commit == "created-base" || byID["Q1"].Status != model.OQStale {
		t.Fatalf("integration change %+v", byID["Q1"])
	}
	if byID["Q2"].Status != model.OQAnswered || byID["Q3"].Status != model.OQAnswered {
		t.Fatalf("dirty %+v keep %+v", byID["Q2"], byID["Q3"])
	}
	if fake.diffCalls != 0 {
		t.Fatalf("worktree diff calls %d", fake.diffCalls)
	}
	if len(fake.pairs) == 0 || fake.ref != obj.IntegrationBranch {
		t.Fatalf("compare pairs %v ref %q", fake.pairs, fake.ref)
	}
	for _, pair := range fake.pairs {
		if pair[0] != "attempt-base" || pair[1] != "head-now" {
			t.Fatalf("compare pair %v", pair)
		}
	}
	var summary string
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindDesign && c.Status == model.CardReady && c.Node == node.ID {
			summary = c.Body
		}
	}
	if !find(summary, "可能过期") {
		t.Fatalf("summary %s", summary)
	}

	dir := seedNode(t, st, obj.ID, "", model.LayerDirection)
	dir.OpenQuestions = model.OpenQuestionList{{
		ID: "Q0", Text: "盘点 目标", Type: model.OQFact, Status: model.OQAnswered,
		Answer: "旧盘点", Evidence: "only-on-integration.go:1", Confidence: "高",
		Commit: "attempt-base", CitedPaths: []string{"only-on-integration.go"},
	}}
	if err := st.UpdateNode(t.Context(), &dir); err != nil {
		t.Fatal(err)
	}
	seedCard(t, st, obj.ID, dir.ID, model.KindDesign, model.CardDone, "## 子节点\n- [module] 账单\n")
	if err := loopStep(t, d, obj.ID); err != nil {
		t.Fatal(err)
	}
	dir = nodeOf(t, st, dir.ID)
	if q := findQ(dir, "Q0"); q.Status != model.OQStale {
		t.Fatalf("stored Q0 %+v", q)
	}
	nodes, err := st.ListNodes(t.Context(), obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	var childBody string
	for _, n := range nodes {
		if n.Parent != dir.ID || n.Layer != model.LayerModule {
			continue
		}
		for _, c := range cardsOf(t, st, obj.ID) {
			if c.Node == n.ID && c.Kind == model.KindDesign && c.Status == model.CardReady {
				childBody = c.Body
			}
		}
	}
	if !find(childBody, "可能过期") || !find(childBody, "盘点 目标") {
		t.Fatalf("child summary %s", childBody)
	}
}

func loopStep(t *testing.T, d Deps, objectiveID string) error {
	t.Helper()
	return New(d.Store, d.WS, d.Notify).Step(t.Context(), objectiveID)
}

type compareFake struct {
	head      string
	ref       string
	pairs     [][2]string
	files     map[string][]string
	dirty     []string
	diffCalls int
}

func (f *compareFake) Head(_ context.Context, _, ref string) (string, error) {
	f.ref = ref
	return f.head, nil
}

func (f *compareFake) CompareCommits(_ context.Context, _, from, to string) (workspace.Diff, error) {
	f.pairs = append(f.pairs, [2]string{from, to})
	return workspace.Diff{Files: f.files[from+"→"+to]}, nil
}

func (f *compareFake) Create(context.Context, workspace.CreateRequest) (workspace.Worktree, error) {
	return workspace.Worktree{}, workspace.ErrNotImplemented
}
func (f *compareFake) Commit(context.Context, string, string) (string, error) {
	return "", workspace.ErrNotImplemented
}
func (f *compareFake) Diff(context.Context, string, string) (workspace.Diff, error) {
	f.diffCalls++
	return workspace.Diff{Files: append([]string(nil), f.dirty...)}, nil
}
func (f *compareFake) Remove(context.Context, string) error    { return workspace.ErrNotImplemented }
func (f *compareFake) BlockPush(context.Context, string) error { return workspace.ErrNotImplemented }

func TestDesignPhaseDoesNotReadGitMetadata(t *testing.T) {
	for _, name := range []string{"design.go", "design_parse.go", "research.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, bad := range []string{"packed-refs", "commondir", "readGitHEAD", "gitdir:", "exec.Command"} {
			if strings.Contains(text, bad) {
				t.Fatalf("%s contains %s", name, bad)
			}
		}
	}
}

func TestRecheckThenDecision(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, "## 开放问题\n- [fact] 端口是多少\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	researchID := readyResearch(t, st, obj.ID, node.ID)
	finishCard(t, st, researchID, "## 事实\n### Q1\n结论：8787\n证据：docs/v2/design.md:1\n把握：高\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	designID := readyKind(t, st, obj.ID, node.ID, model.KindDesign)
	finishCard(t, st, designID, "## 开放问题\n- [fact] Q1 复查：证据指向了设计文档\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	again := readyResearch(t, st, obj.ID, node.ID)
	card, err := st.GetCard(t.Context(), again)
	if err != nil {
		t.Fatal(err)
	}
	if card.Tier != model.TierT2 || !find(card.Body, "Q1") {
		t.Fatalf("recheck card %+v", card)
	}
	node = nodeOf(t, st, node.ID)
	if q := findQ(node, "Q1"); q.Asks != 1 || q.Status != model.OQResearching || q.Type != model.OQFact {
		t.Fatalf("after first recheck %+v", q)
	}
	finishCard(t, st, again, "## 事实\n### Q1\n结论：仍然是 8787\n证据：docs/v2/design.md:2\n把握：中\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	designID = readyKind(t, st, obj.ID, node.ID, model.KindDesign)
	finishCard(t, st, designID, "## 开放问题\n- [fact] Q1 复查：还是不对\n")
	if err := designStep(t.Context(), depsOf(st), obj.ID); err != nil {
		t.Fatal(err)
	}
	node = nodeOf(t, st, node.ID)
	q := findQ(node, "Q1")
	if q.Type != model.OQDecision || q.Asks != 2 {
		t.Fatalf("second recheck %+v", q)
	}
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindResearch && c.Node == node.ID && !terminalCard(c.Status) {
			t.Fatalf("research dispatched after decision %+v", c)
		}
	}
}

func readyResearch(t *testing.T, st *store.Store, objective, node string) string {
	t.Helper()
	return readyKind(t, st, objective, node, model.KindResearch)
}

func readyKind(t *testing.T, st *store.Store, objective, node string, kind model.CardKind) string {
	t.Helper()
	for _, c := range cardsOf(t, st, objective) {
		if c.Node == node && c.Kind == kind && !terminalCard(c.Status) {
			return c.ID
		}
	}
	t.Fatalf("no ready %s on %s", kind, node)
	return ""
}

func findQ(n model.DesignNode, id string) model.OpenQuestion {
	for _, q := range n.OpenQuestions {
		if q.ID == id {
			return q
		}
	}
	return model.OpenQuestion{}
}

func questionLineCount(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if qBodyRE.MatchString(line) {
			n++
		}
	}
	return n
}

func containsReq(req []string, want string) bool {
	for _, r := range req {
		if r == want {
			return true
		}
	}
	return false
}

func sameSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[int]int{}
	for _, n := range a {
		seen[n]++
	}
	for _, n := range b {
		seen[n]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
