package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/question"
	"cardex/internal/runner"
	"cardex/internal/sched"
	"cardex/internal/store"
)

// TestProdWiring drives one auto objective through sched.Serve.
// The test does not assign Engine, Classify, Dispose, Launch, Notify, or Prompt.
func TestProdWiring(t *testing.T) {
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
		Status: model.ObjectiveActive, IntegrationBranch: branch, Origin: "file:box",
	}
	if err := h.DB.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.Home, "config.json"), []byte("{\"schedules\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installRoutes(t, h)
	t.Log("constructor sched.Serve calls sched.New; test left Engine Classify Dispose Launch Notify Prompt unset")

	var logBuf lockWriter
	serveCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- sched.Serve(sched.WithLog(serveCtx, &logBuf), h.Home, "127.0.0.1:0")
	}()
	t.Cleanup(func() { stopProdHosts(h) })

	deadline := time.Now().Add(5 * time.Second)
	var listen string
	for time.Now().Before(deadline) {
		listen = listenLine(logBuf.String())
		if listen != "" {
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("serve ended before listen: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if listen == "" {
		t.Fatalf("missing listen line\n%s", logBuf.String())
	}
	t.Log(listen)
	if path := os.Getenv("CARDEX_SERVE_BIND_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(listen+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(listen, ":8787") || strings.Contains(listen, ":8788") {
		t.Fatalf("reserved port %s", listen)
	}

	order := &mergeOrder{stop: make(chan struct{})}
	order.wg.Add(1)
	go order.poll(h)
	defer func() {
		close(order.stop)
		order.wg.Wait()
	}()

	var stages []string
	seen := map[string]bool{}
	note := func(name string, ok bool) {
		if ok && !seen[name] {
			seen[name] = true
			stages = append(stages, name)
			t.Logf("stage %s", name)
		}
	}
	answered := map[string]bool{}
	flowDeadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(flowDeadline) {
		select {
		case err := <-errCh:
			t.Fatalf("serve: %v\n%s", err, diag(h))
		default:
		}
		cards := cardsOf(t, h, obj.ID)
		plantProdScripts(t, h, cards)
		for _, q := range questionsOf(t, h, obj.ID) {
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
		nodes := nodesOf(t, h, obj.ID)
		note("inventory", hasInventory(cards))
		note("facts", factsLanded(cards, nodes))
		note("frozen", directionFrozen(nodes))
		note("module", hasModule(nodes))
		note("split", skeletonCard(cards) != nil)
		note("skeleton", skeletonBeforeDevs(t, h, cards))
		note("parallel", devsInParallel(t, h, cards))
		reviewAt, queuedAt, mergedAt := order.stamps()
		note("close", reviewAt > 0 && queuedAt > 0 && mergedAt > 0 && reviewAt <= queuedAt && queuedAt < mergedAt && hasDoneQuestion(questionsOf(t, h, obj.ID)))
		if seen["close"] {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
	want := []string{"inventory", "facts", "frozen", "module", "split", "skeleton", "parallel", "close"}
	if strings.Join(stages, ",") != strings.Join(want, ",") {
		t.Fatalf("stages %v\n%s", stages, diag(h))
	}
	entries, err := os.ReadDir(filepath.Join(h.Home, "inbox"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("inbox %v %v", entries, err)
	}
	t.Logf("inbox %s", entries[0].Name())
	attempts, err := os.ReadDir(filepath.Join(h.Home, "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	both := 0
	for _, entry := range attempts {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(h.Home, "attempts", entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "acceptance.log")); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "result.md")); err != nil {
			continue
		}
		both++
	}
	if both == 0 {
		t.Fatal("no harvested attempt has both acceptance.log and result.md")
	}
	events, err := h.DB.ListEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	verdicts := 0
	for _, event := range events {
		if event.Type == "review_verdict" {
			verdicts++
		}
	}
	if verdicts == 0 {
		t.Fatal("production wiring recorded no review_verdict")
	}
}

func TestProdLookupAllRunners(t *testing.T) {
	for _, name := range model.AllRunners() {
		got, ok := runner.Lookup(string(name))
		if !ok || got.Name() != string(name) {
			t.Fatalf("lookup %s ok=%v", name, ok)
		}
	}
}

func TestProdDesignPhaseErrorStoresEvent(t *testing.T) {
	h := New(t)
	ctx := context.Background()
	wt := filepath.Join(h.Root, "inv-wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wt
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	git("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	proj := model.Project{ID: "p", RepoPath: wt, TestCmd: "true", DesignDir: "docs"}
	if err := h.DB.CreateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	obj := model.Objective{
		ID: "auto", Project: proj.ID, Title: "auto", Mode: model.ModeAuto, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main", Origin: "file:box",
	}
	if err := h.DB.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "inv", Objective: obj.ID, Kind: model.KindResearch, Title: "盘点",
		Body: "Q0\n", Tier: model.TierT1, Status: model.CardDone,
	}
	if err := h.DB.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := h.DB.AppendEvent(ctx, &model.Event{Entity: "card:inv", Type: "loop.absorbed", Data: []byte("{")}); err != nil {
		t.Fatal(err)
	}
	if err := h.DB.CreateAttempt(ctx, &model.Attempt{
		ID: "a-open", Card: card.ID, Index: 0, Worktree: wt, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	decoy := t.TempDir()
	t.Setenv("CARDEX_HOME", decoy)
	if err := sched.New(h.Home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := h.DB.ListEvents(ctx, "objective:auto")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range events {
		if event.Type == "design_error" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("design_error events %d", n)
	}
}

func plantProdScripts(t *testing.T, h *Harness, cards []model.Card) {
	t.Helper()
	for _, card := range cards {
		body := prodScript(t, h, card)
		if len(body) == 0 {
			continue
		}
		path := filepath.Join(h.Home, "scripts", card.ID+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func prodScript(t *testing.T, h *Harness, card model.Card) []byte {
	t.Helper()
	switch card.Kind {
	case model.KindReview:
		return []byte(scenarioReviewScript)
	case model.KindResearch:
		if strings.Contains(card.Title, "盘点") || (card.Node == "" && strings.Contains(card.Body, "Q0")) {
			return resultScript(s25Inventory)
		}
		return resultScript(s25ResearchAnswer(card.Body))
	case model.KindDesign:
		switch {
		case card.Title == "设计方向":
			return resultScript(s25FactQuestion)
		case card.Title == "设计" || strings.HasPrefix(card.Title, "设计 module"):
			return resultScript(s25Freezable)
		default:
			return nil
		}
	case model.KindReplan:
		return resultScript("## 新卡\n[]\n## 结论\n不调整\n")
	case model.KindPlan:
		node, err := h.DB.GetNode(context.Background(), card.Node)
		if err != nil {
			return nil
		}
		if node.Layer == model.LayerDirection {
			return resultScript(s25DirectionPlan)
		}
		if node.Layer == model.LayerModule {
			return resultScript(s25ModulePlan())
		}
		return nil
	default:
		return nil
	}
}

func stopProdHosts(h *Harness) {
	attempts, err := h.DB.ListAttempts(context.Background(), "")
	if err != nil {
		return
	}
	for _, attempt := range attempts {
		if attempt.PID > 1 {
			_ = host.SignalProcess(attempt.PID, syscall.SIGKILL)
		}
		if pgid := executorPGID(h.Home, attempt.ID); pgid > 1 {
			_ = host.SignalGroup(pgid, syscall.SIGKILL)
		}
	}
}

func listenLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "serve listening ") {
			return line
		}
	}
	return ""
}

type lockWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// TestProdWiringCLIFirstDispatch appends the production path for a CLI-created
// objective's first card. It does not assign Engine, Classify, Dispose, Launch,
// Notify, or Prompt.
func TestProdWiringCLIFirstDispatch(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	module := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	bin := filepath.Join(t.TempDir(), "cardex")
	build := exec.Command("go", "build", "-o", bin, "./cmd/cardex")
	build.Dir = module
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := t.TempDir()
	repo := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	prodGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prodGit(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "README")
	prodGit(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "seed")
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{\"schedules\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prodCLI(t, bin, home, "project", "add", "--id", "p", "--repo", repo, "--worktree-root", wt, "--test-cmd", "true")
	prodCLI(t, bin, home, "channel", "set", "--id", "ch", "--runner", "grok", "--model", "m", "--concurrency", "1")
	prodCLI(t, bin, home, "channel", "set", "--id", "ch", "--runner", "fake", "--model", "m", "--concurrency", "1")
	prodCLI(t, bin, home, "objective", "add", "--id", "o", "--project", "p", "--title", "o", "--mode", "manual", "--channel", "ch", "--model", "m")
	prodCLI(t, bin, home, "card", "add", "--id", "c", "--objective", "o", "--title", "c", "--kind", "dev", "--tier", "T2")
	mainSHA := prodGit(t, repo, "rev-parse", "refs/heads/main")
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Cleanup(func() {
		attempts, err := db.ListAttempts(context.Background(), "")
		if err != nil {
			return
		}
		for _, attempt := range attempts {
			if attempt.PID > 1 {
				_ = host.SignalProcess(attempt.PID, syscall.SIGKILL)
			}
			if pgid := executorPGID(home, attempt.ID); pgid > 1 {
				_ = host.SignalGroup(pgid, syscall.SIGKILL)
			}
		}
	})
	r := sched.New(home)
	if r.Engine == nil || r.Dispose == nil || r.Classify == nil {
		t.Fatal("production runner is missing a hook")
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prodGit(t, repo, "rev-parse", "refs/heads/cardex/o/main") != mainSHA {
		t.Fatal("integration ref is not main HEAD")
	}
	if prodGit(t, repo, "rev-parse", "refs/heads/main") != mainSHA {
		t.Fatal("main moved")
	}
	card, err := db.GetCard(context.Background(), "c")
	if err != nil || card.Status != model.CardRunning {
		t.Fatalf("card %+v %v", card, err)
	}
	attempts, err := db.ListAttempts(context.Background(), "c")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts %+v %v", attempts, err)
	}
	if _, err := os.Stat(attempts[0].Worktree); err != nil {
		t.Fatal(err)
	}
}

func prodCLI(t *testing.T, bin, home string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"--home", home}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cardex %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func prodGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
