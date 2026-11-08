package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"cardex/internal/loop"
	"cardex/internal/model"
	"cardex/internal/question"
	"cardex/internal/sched"
	"cardex/internal/store"
	"cardex/internal/workspace"
)

// TestS25 drives one auto objective through the real scheduler.
// Stages are appended the first time their own predicate is true.
func TestS25(t *testing.T) {
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	ctx := context.Background()
	branch := gitOut(t, h.Repo, "rev-parse", "--abbrev-ref", "HEAD")
	proj := model.Project{
		ID: "p", RepoPath: h.Repo, WorktreeRoot: filepath.Join(h.Root, "wt"),
		TestCmd: "true", DesignDir: "docs/design/",
	}
	if err := h.DB.CreateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	obj := model.Objective{
		ID: "obj", Project: proj.ID, Title: "账单", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: branch, Origin: "codex:thread-0001",
	}
	if err := h.DB.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	installRoutes(t, h)
	runner := sched.New(h.Home)
	runner.Script = s25Script(h)
	runner.Engine = func(ctx context.Context, id string) error {
		db, err := store.Open(filepath.Join(h.Home, "cardex.db"))
		if err != nil {
			return err
		}
		defer db.Close()
		if err := loop.New(db, workspace.Git{}, quietDeliver{}).Step(ctx, id); err != nil {
			return err
		}
		return loop.DevStep(ctx, id)
	}
	h.Sched = runner
	stopLater(t, h)

	order := &mergeOrder{stop: make(chan struct{})}
	order.wg.Add(1)
	go order.poll(h)
	defer func() {
		close(order.stop)
		order.wg.Wait()
	}()

	var stages []string
	seen := map[string]bool{}
	lastSummary := ""
	note := func(name string, ok bool) {
		if ok && !seen[name] {
			seen[name] = true
			stages = append(stages, name)
			t.Logf("stage %s", name)
		}
	}
	answered := map[string]bool{}
	deadline := time.Now().Add(90 * time.Second)
	busy := 0
	for time.Now().Before(deadline) {
		if err := h.Sched.Tick(ctx); err != nil {
			if strings.Contains(err.Error(), "database is locked") {
				busy++
				if busy > 40 {
					t.Fatalf("tick stayed locked: %v\n%s", err, diag(h))
				}
				time.Sleep(30 * time.Millisecond)
				continue
			}
			t.Fatalf("tick: %v\n%s", err, diag(h))
		}
		busy = 0
		cards := cardsOf(t, h, obj.ID)
		nodes := nodesOf(t, h, obj.ID)
		questions := questionsOf(t, h, obj.ID)
		for _, q := range questions {
			if q.Kind != model.QuestionFreeze || strings.TrimSpace(q.Answer) != "" || answered[q.ID] {
				continue
			}
			node, err := h.DB.GetNode(ctx, q.Scope)
			if err != nil || node.Layer != model.LayerDirection {
				continue
			}
			if len(q.Options) == 0 {
				t.Fatalf("freeze question %s has no options", q.ID)
			}
			if _, err := question.New(h.DB, quietDeliver{}).Answer(ctx, q.ID, q.Options[0]); err != nil {
				t.Fatal(err)
			}
			answered[q.ID] = true
			t.Logf("answered freeze %s with %s", q.ID, q.Options[0])
		}
		note("inventory", hasInventory(cards))
		if summary := factSummaryText(cards); summary != "" && summary != lastSummary {
			lastSummary = summary
			t.Logf("fact summary %s", summary)
		}
		note("facts", factsLanded(cards, nodes))
		note("frozen", directionFrozen(nodes))
		note("module", hasModule(nodes))
		note("split", skeletonCard(cards) != nil)
		note("skeleton", skeletonBeforeDevs(t, h, cards))
		note("parallel", devsInParallel(t, h, cards))
		reviewAt, queuedAt, mergedAt := order.stamps()
		note("close", reviewAt > 0 && queuedAt > 0 && mergedAt > 0 && reviewAt <= queuedAt && queuedAt < mergedAt && hasDoneQuestion(questions))
		if seen["close"] {
			break
		}
	}
	want := []string{"inventory", "facts", "frozen", "module", "split", "skeleton", "parallel", "close"}
	if strings.Join(stages, ",") != strings.Join(want, ",") {
		t.Fatalf("stages %v\n%s", stages, diag(h))
	}
}

func s25Script(h *Harness) func(model.Card) ([]byte, bool) {
	return func(card model.Card) ([]byte, bool) {
		switch card.Kind {
		case model.KindReview:
			return []byte(scenarioReviewScript), true
		case model.KindResearch:
			if strings.Contains(card.Title, "盘点") || (card.Node == "" && strings.Contains(card.Body, "Q0")) {
				return resultScript(s25Inventory), true
			}
			return resultScript(s25ResearchAnswer(card.Body)), true
		case model.KindDesign:
			switch {
			case card.Title == "设计方向":
				return resultScript(s25FactQuestion), true
			case card.Title == "设计" || strings.HasPrefix(card.Title, "设计 module"):
				return resultScript(s25Freezable), true
			default:
				return nil, false
			}
		case model.KindReplan:
			return resultScript("## 新卡\n[]\n## 结论\n不调整\n"), true
		case model.KindPlan:
			node, err := h.DB.GetNode(context.Background(), card.Node)
			if err != nil {
				return nil, false
			}
			if node.Layer == model.LayerDirection {
				return resultScript(s25DirectionPlan), true
			}
			if node.Layer == model.LayerModule {
				return resultScript(s25ModulePlan()), true
			}
			return nil, false
		default:
			return nil, false
		}
	}
}

const (
	s25Inventory     = "## 事实\n### Q0\n结论：仓库里有 answer.go\n证据：answer.go:1\n把握：高\n"
	s25FactQuestion  = "## 开放问题\n- [fact] 模块入口在哪\n## 结论\n需要先查入口\n"
	s25Freezable     = "## 结论\n可以冻结\n## 可冻结\n是\n"
	s25DirectionPlan = "## 新卡\n[]\n## 子节点\n- [module] 账单\n## 结论\n方向先建模块\n"
)

var s25QBody = regexp.MustCompile(`(?m)^Q([A-Za-z0-9_-]+)\s*:`)

func s25ResearchAnswer(body string) string {
	var b strings.Builder
	b.WriteString("## 事实\n")
	ids := s25QBody.FindAllStringSubmatch(body, -1)
	if len(ids) == 0 {
		b.WriteString("### Q1\n结论：入口在 answer.go\n证据：answer.go:1\n把握：高\n")
		return b.String()
	}
	for _, m := range ids {
		b.WriteString("### Q" + m[1] + "\n结论：入口在 answer.go\n证据：answer.go:1\n把握：高\n")
	}
	return b.String()
}

func s25ModulePlan() string {
	skel := fakeScript("1s", []map[string]string{{
		"path": "skel.go", "content": "package fixture\n\nfunc Skel() int { return 1 }\n",
	}}, nil)
	devA := fakeScript("3s", []map[string]string{{
		"path": "a.go", "content": "package fixture\n\nfunc A() int { return 1 }\n",
	}}, nil)
	devB := fakeScript("3s", []map[string]string{{
		"path": "b.go", "content": "package fixture\n\nfunc B() int { return 2 }\n",
	}}, nil)
	cards := []map[string]any{
		{"key": "skel", "kind": "skeleton", "title": "骨架", "body": skel, "claims": []string{"skel.go"}, "acceptance": []string{"true"}},
		{"key": "dev-a", "kind": "dev", "title": "实现甲", "body": devA, "claims": []string{"a.go"}, "deps": []string{"skel"}, "acceptance": []string{"true"}},
		{"key": "dev-b", "kind": "dev", "title": "实现乙", "body": devB, "claims": []string{"b.go"}, "deps": []string{"skel"}, "acceptance": []string{"true"}},
	}
	raw, err := json.Marshal(cards)
	if err != nil {
		panic(err)
	}
	return "## 新卡\n" + string(raw) + "\n## 结论\n骨架和两张实现\n"
}

func resultScript(markdown string) []byte {
	raw, err := json.Marshal(map[string]any{
		"attempts": map[string]any{
			"0": map[string]any{"result": markdown, "exit_code": 0},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func hasInventory(cards []model.Card) bool {
	for _, card := range cards {
		if card.Kind == model.KindResearch && strings.Contains(card.Title, "盘点") {
			return true
		}
	}
	return false
}

func factsLanded(cards []model.Card, nodes []model.DesignNode) bool {
	facts := answeredDirectionFacts(nodes)
	if len(facts) == 0 {
		return false
	}
	for _, card := range cards {
		if card.Kind != model.KindDesign && card.Kind != model.KindPlan {
			continue
		}
		if card.Title == "设计方向" {
			continue
		}
		section, ok := factSection(card.Body)
		if !ok || summaryBlank(section) {
			continue
		}
		if sectionHasFacts(section, facts) && sectionHasQ0(section, nodes) {
			return true
		}
	}
	return false
}

func answeredDirectionFacts(nodes []model.DesignNode) []model.OpenQuestion {
	raised := false
	var out []model.OpenQuestion
	for _, node := range nodes {
		if node.Layer != model.LayerDirection {
			continue
		}
		for _, q := range node.OpenQuestions {
			if q.Type != model.OQFact || q.ID == "" || q.ID == "Q0" {
				continue
			}
			raised = true
			if q.Status == model.OQAnswered && q.Answer != "" && q.Evidence != "" {
				out = append(out, q)
			}
		}
	}
	if !raised {
		return nil
	}
	return out
}

func factSection(body string) (string, bool) {
	const heading = "## 事实摘要"
	i := strings.Index(body, heading)
	if i < 0 {
		return "", false
	}
	rest := body[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest), true
}

func summaryBlank(section string) bool {
	s := strings.TrimSpace(section)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	return s == "" || s == "[]" || s == "null"
}

func sectionHasFacts(section string, facts []model.OpenQuestion) bool {
	if len(facts) == 0 {
		return false
	}
	for _, q := range facts {
		if q.ID == "" || q.Answer == "" || q.Evidence == "" {
			return false
		}
		if !strings.Contains(section, q.ID) || !strings.Contains(section, q.Answer) || !strings.Contains(section, q.Evidence) {
			return false
		}
	}
	return true
}

// sectionHasQ0 requires Q0 when it is a fact. This scenario's inventory is that fact:
// answer 「仓库里有 answer.go」, evidence answer.go:1.
func sectionHasQ0(section string, nodes []model.DesignNode) bool {
	for _, node := range nodes {
		for _, q := range node.OpenQuestions {
			if q.ID != "Q0" || q.Type != model.OQFact {
				continue
			}
			if q.Answer == "" || q.Evidence == "" {
				return false
			}
			return strings.Contains(section, q.ID) && strings.Contains(section, q.Answer) && strings.Contains(section, q.Evidence)
		}
	}
	return strings.Contains(section, "Q0") && strings.Contains(section, "仓库里有 answer.go") && strings.Contains(section, "answer.go:1")
}

func factSummaryText(cards []model.Card) string {
	var b strings.Builder
	for _, card := range cards {
		if card.Kind != model.KindDesign && card.Kind != model.KindPlan {
			continue
		}
		section, ok := factSection(card.Body)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(string(card.Kind))
		b.WriteByte(' ')
		b.WriteString(card.Title)
		b.WriteString(": ")
		b.WriteString(section)
	}
	return b.String()
}

func directionFrozen(nodes []model.DesignNode) bool {
	for _, node := range nodes {
		if node.Layer == model.LayerDirection && node.Parent == "" && node.Status == model.NodeFrozen {
			return true
		}
	}
	return false
}

func hasModule(nodes []model.DesignNode) bool {
	for _, node := range nodes {
		if node.Layer == model.LayerModule {
			return true
		}
	}
	return false
}

func skeletonCard(cards []model.Card) *model.Card {
	for i := range cards {
		if cards[i].Kind == model.KindSkeleton {
			return &cards[i]
		}
	}
	return nil
}

func skeletonBeforeDevs(t *testing.T, h *Harness, cards []model.Card) bool {
	t.Helper()
	skel := skeletonCard(cards)
	if skel == nil {
		return false
	}
	switch skel.Status {
	case model.CardRunning, model.CardHarvesting, model.CardReviewing, model.CardMergeQueued, model.CardMerged, model.CardDone:
	default:
		return false
	}
	devs := devsDependingOn(cards, skel.ID)
	if len(devs) < 2 {
		return false
	}
	for _, dev := range devs {
		if len(attemptsOf(t, h, dev.ID)) != 0 {
			return false
		}
	}
	return true
}

func devsDependingOn(cards []model.Card, id string) []model.Card {
	var out []model.Card
	for _, card := range cards {
		if card.Kind != model.KindDev {
			continue
		}
		for _, dep := range card.Deps {
			if dep == id {
				out = append(out, card)
				break
			}
		}
	}
	return out
}

func devsInParallel(t *testing.T, h *Harness, cards []model.Card) bool {
	t.Helper()
	live := 0
	for _, card := range cards {
		if card.Kind != model.KindDev || card.Status != model.CardRunning {
			continue
		}
		if attemptLive(t, h, card.ID) {
			live++
		}
	}
	return live >= 2
}

func hasDoneQuestion(questions []model.Question) bool {
	for _, q := range questions {
		if q.Kind == model.QuestionDone {
			return true
		}
	}
	return false
}

func nodesOf(t *testing.T, h *Harness, obj string) []model.DesignNode {
	t.Helper()
	nodes, err := h.DB.ListNodes(context.Background(), obj)
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func questionsOf(t *testing.T, h *Harness, obj string) []model.Question {
	t.Helper()
	questions, err := h.DB.ListQuestions(context.Background(), obj)
	if err != nil {
		t.Fatal(err)
	}
	return questions
}

// mergeOrder samples review, merge_queued, and merged from a second connection.
type mergeOrder struct {
	mu       sync.Mutex
	pollN    int
	reviewAt int
	queuedAt int
	mergedAt int
	stop     chan struct{}
	wg       sync.WaitGroup
}

func (m *mergeOrder) stamps() (review, queued, merged int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reviewAt, m.queuedAt, m.mergedAt
}

func (m *mergeOrder) poll(h *Harness) {
	defer m.wg.Done()
	for {
		select {
		case <-m.stop:
			return
		default:
		}
		m.sample(h)
		time.Sleep(100 * time.Microsecond)
	}
}

func (m *mergeOrder) sample(h *Harness) {
	ctx := context.Background()
	cards, err := h.DB.ListCards(ctx, "")
	if err != nil {
		return
	}
	reviewDone := false
	queued := false
	merged := false
	for _, card := range cards {
		if card.Kind == model.KindReview && card.Status == model.CardDone {
			reviewDone = true
		}
		if !scenarioMerges(card.Kind) {
			continue
		}
		switch card.Status {
		case model.CardMergeQueued:
			queued = true
		case model.CardMerged:
			merged = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pollN++
	if reviewDone && m.reviewAt == 0 {
		m.reviewAt = m.pollN
	}
	if queued && m.queuedAt == 0 {
		m.queuedAt = m.pollN
	}
	if merged && m.mergedAt == 0 {
		m.mergedAt = m.pollN
	}
}
