package loop

import (
	"os"
	"os/exec"
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

func TestStaleUsesWorkspaceDiff(t *testing.T) {
	repo := gitRepo(t)
	st := openLoop(t)
	proj := &model.Project{RepoPath: repo, TestCmd: "true", DesignDir: "docs"}
	if err := st.CreateProject(t.Context(), proj); err != nil {
		t.Fatal(err)
	}
	obj := seedObjective(t, st, proj.ID, model.ModeAuto, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardDone, `
## 开放问题
- [fact] 文件现在写了什么
- [fact] 保持不变的文件
`)
	d := Deps{Store: st, WS: workspace.Git{}}
	if err := designStep(t.Context(), d, obj.ID); err != nil {
		t.Fatal(err)
	}
	var researchID, base string
	for _, c := range cardsOf(t, st, obj.ID) {
		if c.Kind == model.KindResearch && c.Node == node.ID {
			researchID = c.ID
			base = c.Base
		}
	}
	if researchID == "" || base == "" {
		t.Fatalf("research base %q id %q", base, researchID)
	}
	if err := os.WriteFile(repo+"/file.go", []byte("package a\nfunc Changed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "file.go")
	gitRun(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "change")
	head := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	if head == base {
		t.Fatal("HEAD did not move")
	}
	finishCard(t, st, researchID, `
## 事实
### Q1
结论：旧内容
证据：file.go:1
把握：高
### Q2
结论：还在
证据：keep.go:1
把握：高
`)
	if err := designStep(t.Context(), d, obj.ID); err != nil {
		t.Fatal(err)
	}
	node = nodeOf(t, st, node.ID)
	var changed, kept model.OpenQuestion
	for _, q := range node.OpenQuestions {
		switch q.ID {
		case "Q1":
			changed = q
		case "Q2":
			kept = q
		}
	}
	if changed.Commit != base || changed.Commit == head || changed.Status != model.OQStale {
		t.Fatalf("stale %+v head %s", changed, head)
	}
	if kept.Status != model.OQAnswered {
		t.Fatalf("unchanged fact %+v", kept)
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

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	if err := os.WriteFile(dir+"/file.go", []byte("package a\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/keep.go", []byte("package a\nfunc Keep() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "file.go", "keep.go")
	gitRun(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOut(t, dir, args...)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
