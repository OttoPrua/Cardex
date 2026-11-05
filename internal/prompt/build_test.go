package prompt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/model"
)

func TestBuildRevisionStableSegments(t *testing.T) {
	repo := t.TempDir()
	const (
		objective   = "obj-r3d"
		contextPath = "docs/design/obj-r3d/context.md"
		nodePath    = "docs/design/obj-r3d/module.md"
		committedC  = "COMMITTED_CONTEXT_R3D_9f3a\n"
		dirtyC      = "DIRTY_CONTEXT_R3D_9f3a\n"
		committedN  = "COMMITTED_NODE_R3D_9f3a\n"
		dirtyN      = "DIRTY_NODE_R3D_9f3a\n"
	)
	writeRepoFile(t, repo, contextPath, committedC)
	writeRepoFile(t, repo, nodePath, committedN)
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.email", "r3d@example.com")
	git(t, repo, "config", "user.name", "r3d")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "docs")
	rev := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	writeRepoFile(t, repo, contextPath, dirtyC)
	writeRepoFile(t, repo, nodePath, dirtyN)

	factsA := []model.OpenQuestion{factQ("Q0", model.OQAnswered, "盘点结论", "repo:1", "高")}
	factsB := []model.OpenQuestion{factQ("Q9", model.OQStale, "过期结论", "repo:2", "低")}
	nodes := []model.DesignNode{{ID: "n1", Objective: objective, DocPath: nodePath}}
	base := Input{
		Objective: model.Objective{ID: objective},
		Nodes:     nodes,
		Repo:      repo,
		Revision:  rev,
	}

	designA := base
	designA.Card = card(model.KindDesign, "body-design-a")
	designA.Facts = factsA
	designB := base
	designB.Card = card(model.KindDesign, "body-design-b")
	designB.Facts = factsB
	dev := base
	dev.Card = card(model.KindDev, "body-dev")
	dev.Card.Claims = []model.Claim{{Path: "internal/prompt/build.go", Symbol: "Build"}}
	dev.Card.Acceptance = []string{"go test ./internal/prompt/"}
	dev.ReworkNote = "返工：补上边界"
	dev.Facts = factsB

	ctx := context.Background()
	outA := mustBuild(t, ctx, designA)
	outB := mustBuild(t, ctx, designB)
	outDev := mustBuild(t, ctx, dev)

	for _, out := range []string{outA, outB, outDev} {
		if strings.Contains(out, strings.TrimSpace(dirtyC)) || strings.Contains(out, strings.TrimSpace(dirtyN)) {
			t.Fatalf("prompt contains dirty worktree bytes:\n%s", out)
		}
		if !strings.Contains(out, strings.TrimSpace(committedC)) || !strings.Contains(out, strings.TrimSpace(committedN)) {
			t.Fatalf("prompt missing committed bytes:\n%s", out)
		}
	}

	if throughContext(outA) != throughContext(outB) {
		t.Fatalf("same kind prefix diverged\nA:\n%s\nB:\n%s", throughContext(outA), throughContext(outB))
	}
	prefix := throughContext(outA)
	if strings.Contains(prefix, "body-design-a") || strings.Contains(prefix, "body-design-b") {
		t.Fatal("stable prefix includes the card body")
	}
	if strings.Contains(prefix, "过期结论") || strings.Contains(prefix, "可能过期") || strings.Contains(prefix, factsStart) {
		t.Fatal("fact summary is inside the stable prefix")
	}
	preface, err := Preface(model.KindDesign)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prefix, preface) {
		t.Fatal("prefix does not start with the design preface")
	}
	if segment(t, outA, contextStart, contextEnd) != committedC {
		t.Fatalf("context segment %q", segment(t, outA, contextStart, contextEnd))
	}
	if segment(t, outA, contextStart, contextEnd) != segment(t, outDev, contextStart, contextEnd) {
		t.Fatal("context segment differs across kinds")
	}
	if segment(t, outA, nodesStart, nodesEnd) != segment(t, outB, nodesStart, nodesEnd) {
		t.Fatal("node segment differs across builds of the same revision")
	}
	if !strings.Contains(segment(t, outA, nodesStart, nodesEnd), committedN) {
		t.Fatal("node segment missing committed document")
	}

	ctxIdx := strings.Index(outA, committedC)
	nodeIdx := strings.Index(outA, committedN)
	factIdx := strings.Index(outA, "盘点结论")
	bodyIdx := strings.Index(outA, "body-design-a")
	if !(ctxIdx >= 0 && ctxIdx < nodeIdx && nodeIdx < factIdx && factIdx < bodyIdx) {
		t.Fatalf("order ctx %d node %d fact %d body %d", ctxIdx, nodeIdx, factIdx, bodyIdx)
	}
	if strings.Contains(outDev, factsStart) || strings.Contains(outDev, "过期结论") || strings.Contains(outDev, "可能过期") {
		t.Fatal("dev prompt includes the fact summary")
	}
	for _, want := range []string{"body-dev", "internal/prompt/build.go", "Build", "go test ./internal/prompt/", "返工：补上边界"} {
		if !strings.Contains(outDev, want) {
			t.Fatalf("dev prompt missing %q\n%s", want, outDev)
		}
	}
}

func TestFactSummaryStatuses(t *testing.T) {
	facts := []model.OpenQuestion{
		factQ("Q0", model.OQAnswered, "仓库有 internal", "internal/model/types.go:59", "高"),
		factQ("Q1", model.OQStale, "旧接口还在", "internal/notify/api.go:1", "中"),
		factQ("Q2", model.OQUnanswered, "", "没有输出", "低"),
	}
	facts[0].Text = "目标盘点了哪些目录？"
	facts[1].Text = "notify 的入口在哪？"
	facts[2].Text = "外部命令怎么注入？"
	in := Input{
		Card:      card(model.KindPlan, "plan-body"),
		Objective: model.Objective{ID: "obj"},
		Context:   "INJECTED_CONTEXT\n",
		Facts:     facts,
	}
	out := mustBuild(t, context.Background(), in)
	got := segment(t, out, factsStart, factsEnd)
	blocks := map[string]string{}
	for _, part := range strings.Split(got, "问题编号：") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, _, _ := strings.Cut(part, "\n")
		blocks[id] = part
	}
	if _, ok := blocks["Q0"]; !ok {
		t.Fatalf("Q0 dropped:\n%s", got)
	}
	assertFact(t, blocks["Q0"], "Q0", "目标盘点了哪些目录？", "仓库有 internal", "internal/model/types.go:59", "高", false, false)
	assertFact(t, blocks["Q1"], "Q1", "notify 的入口在哪？", "旧接口还在", "internal/notify/api.go:1", "中", true, false)
	assertFact(t, blocks["Q2"], "Q2", "外部命令怎么注入？", "", "没有输出", "低", false, true)

	dev := in
	dev.Card = card(model.KindResearch, "research-body")
	devOut := mustBuild(t, context.Background(), dev)
	if strings.Contains(devOut, factsStart) || strings.Contains(devOut, "目标盘点了哪些目录？") {
		t.Fatal("research prompt rendered the fact summary")
	}
}

func TestPrefacesRender(t *testing.T) {
	shared := []string{
		"[fact]", "[fact web]", "[experiment]", "[decision]", "[otto]",
		"不要自己大范围阅读", "把缺的事实列成 `[fact]` 问题后结束本卡", "定点核对几处文件可以自己做",
		"问题编号", "[fact] Q<id> 复查：<原因>",
	}
	planExtra := []string{
		"context.md", "key", "kind", "title", "body", "tier?", "claims?", "deps?", "acceptance?",
		"骨架卡", "尚不存在的接口", "让这些开发卡依赖它",
	}
	checks := map[model.CardKind][]string{
		model.KindDesign:    append([]string{"## 结论", "## 开放问题", "## 设计问题", "## 可冻结"}, shared...),
		model.KindPlan:      append(append([]string{"## 结论", "## 开放问题", "## 新卡"}, shared...), planExtra...),
		model.KindReplan:    append(append([]string{"## 结论", "## 开放问题", "## 新卡"}, shared...), []string{"context.md", "同步维护", "不要只调整卡清单"}...),
		model.KindResearch:  {"## 事实", "### Q<id>", "只回答卡里列出的问题", "不下设计结论", "不改代码", "结论", "证据", "文件:行号", "命令及输出片段", "URL", "把握", "高", "中", "低", "未能核实", "不确定时写明，不要猜"},
		model.KindReview:    {"## 复核结论", "pass", "rework", "只检查是否达成卡的目标、是否越界", "不得提出新需求"},
		model.KindSpike:     {"## 结论", "实做验证"},
		model.KindSkeleton:  {"## 结论", "接口和桩"},
		model.KindDev:       {"## 结论", "实现"},
		model.KindRepair:    {"## 结论", "修测试变红"},
		model.KindRebase:    {"## 结论", "合并冲突"},
		model.KindNormalize: {"## 结论", "提取结构", "## 可冻结"},
		model.KindBacklog:   {"## backlog", "低风险积压"},
	}
	ctx := context.Background()
	for kind, phrases := range checks {
		t.Run(string(kind), func(t *testing.T) {
			out := mustBuild(t, ctx, Input{
				Card:      card(kind, "body-"+string(kind)),
				Objective: model.Objective{ID: "obj"},
				Context:   "CTX\n",
			})
			preface, err := Preface(kind)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(preface) == "" {
				t.Fatalf("%s empty preface", kind)
			}
			if !strings.HasPrefix(out, preface) {
				t.Fatalf("%s preface not rendered at the start", kind)
			}
			if strings.Contains(preface, "回执") {
				t.Fatalf("%s preface requires a receipt", kind)
			}
			if !strings.Contains(preface, ".cardex-result.md") {
				t.Fatalf("%s preface missing result file", kind)
			}
			for _, phrase := range phrases {
				if !strings.Contains(preface, phrase) {
					t.Fatalf("%s preface missing %q", kind, phrase)
				}
			}
		})
	}
}

func TestBuildSoftKind(t *testing.T) {
	ctx := context.Background()
	const (
		title = "SOFT-TITLE-9f3a"
		body  = "SOFT-CARD-BODY-9f3a"
	)
	dev, err := Preface(model.KindDev)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dev, ".cardex-result.md") {
		t.Fatal("dev preface is not the embedded template")
	}
	rank, err := Preface(model.KindRanking)
	if err != nil {
		t.Fatal(err)
	}
	rankingTemplate := strings.Contains(rank, ".cardex-result.md")
	cases := []struct {
		name   string
		kind   model.CardKind
		prefix string
		review bool
	}{
		{name: "empty", kind: "", prefix: dev},
		{name: "unknown", kind: "not-a-real-kind", review: true},
		{name: "ranking", kind: model.KindRanking, prefix: rank, review: !rankingTemplate},
		{name: "dev", kind: model.KindDev, prefix: dev},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Build(ctx, Input{
				Card: model.Card{
					Kind:       tc.kind,
					Title:      title,
					Body:       body,
					Claims:     []model.Claim{{Path: "internal/prompt/build.go", Symbol: "Build"}},
					Acceptance: []string{"go test ./internal/prompt/"},
				},
				Objective: model.Objective{ID: "obj"},
				Context:   "CTX\n",
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{title, body, "internal/prompt/build.go", "Build", "go test ./internal/prompt/"} {
				if !strings.Contains(out, want) {
					t.Fatalf("missing card material %q\n%s", want, out)
				}
			}
			if tc.review {
				if !strings.Contains(out, "待复核") {
					t.Fatalf("missing review mark\n%s", out)
				}
				if strings.HasPrefix(out, dev) {
					t.Fatal("missing template used the dev preface")
				}
				return
			}
			if !strings.HasPrefix(out, tc.prefix) {
				t.Fatal("preface was not rendered first")
			}
			if strings.Contains(out, "待复核") {
				t.Fatal("templated kind was marked for review")
			}
		})
	}
}

func TestMissingContextIsEmpty(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "README.md", "readme\n")
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.email", "r3d@example.com")
	git(t, repo, "config", "user.name", "r3d")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "init")
	rev := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	out := mustBuild(t, context.Background(), Input{
		Card:      card(model.KindDev, "only-body"),
		Objective: model.Objective{ID: "missing"},
		Repo:      repo,
		Revision:  rev,
	})
	if segment(t, out, contextStart, contextEnd) != "" {
		t.Fatalf("missing context %q", segment(t, out, contextStart, contextEnd))
	}
}

func factQ(id string, status model.OpenQuestionStatus, answer, evidence, confidence string) model.OpenQuestion {
	return model.OpenQuestion{
		ID: id, Text: "问题" + id, Type: model.OQFact, Status: status,
		Answer: answer, Evidence: evidence, Confidence: confidence,
	}
}

func assertFact(t *testing.T, block, id, text, conclusion, evidence, confidence string, stale, unanswered bool) {
	t.Helper()
	for _, want := range []string{id, text, "结论：" + conclusion, "证据：" + evidence, "把握：" + confidence} {
		if !strings.Contains(block, want) {
			t.Fatalf("block %s missing %q\n%s", id, want, block)
		}
	}
	if strings.Contains(block, "可能过期") != stale {
		t.Fatalf("block %s stale=%v\n%s", id, stale, block)
	}
	if strings.Contains(block, "未能核实") != unanswered {
		t.Fatalf("block %s unanswered=%v\n%s", id, unanswered, block)
	}
}

func card(kind model.CardKind, body string) model.Card {
	return model.Card{Kind: kind, Title: string(kind) + "-title", Body: body, Objective: "obj-r3d"}
}

func mustBuild(t *testing.T, ctx context.Context, in Input) string {
	t.Helper()
	out, err := Build(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func throughContext(s string) string {
	i := strings.Index(s, contextEnd)
	if i < 0 {
		return s
	}
	return s[:i+len(contextEnd)]
}

func segment(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("missing %q", start)
	}
	i += len(start)
	j := strings.Index(s[i:], end)
	if j < 0 {
		t.Fatalf("missing %q", end)
	}
	return s[i : i+j]
}

func writeRepoFile(t *testing.T, repo, rel, body string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
