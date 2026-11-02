package merge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/store"
)

func TestQueueSerialFastForwardAcrossObjectives(t *testing.T) {
	ctx := context.Background()
	db, repo := openFixture(t)
	mark := filepath.Join(t.TempDir(), "marks")
	slow := writeCmd(t, mark, "A")
	fast := writeCmd(t, mark, "B")
	projA := seedProject(t, db, repo, "pa", slow)
	projB := seedProject(t, db, repo, "pb", fast)
	objA := seedObjective(t, db, repo, projA, "oa", "integ-a")
	objB := seedObjective(t, db, repo, projB, "ob", "integ-b")
	c1 := commitCard(t, db, repo, objA, "integ-a", "c1", "a.txt", "one\n")
	c2 := commitCard(t, db, repo, objA, "integ-a", "c2", "b.txt", "two\n")
	c3 := commitCard(t, db, repo, objB, "integ-b", "c3", "c.txt", "three\n")
	q := New(db)
	if err := q.Enqueue(ctx, c1.ID); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, c1.ID); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, c2.ID); err != nil || q.Enqueue(ctx, c3.ID) != nil {
		t.Fatal("enqueue")
	}
	old, err := gitRev(repo, "integ-a")
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if err := q.Step(ctx, objA); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		if err := q.Step(ctx, objB); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("steps blocked each other")
	}
	raw, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	bEnd := strings.Index(text, "B-end")
	aEnd := strings.Index(text, "A-end")
	if bEnd < 0 || aEnd < 0 || bEnd > aEnd {
		t.Fatalf("objective B waited on A:\n%s", text)
	}
	head, err := gitRev(repo, "integ-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ancestor(t, repo, old, head) {
		t.Fatalf("%s is not an ancestor of %s", old, head)
	}
	headB, err := gitRev(repo, "integ-b")
	if err != nil || headB == old {
		t.Fatalf("objective B integration %s did not move from %s", headB, old)
	}
	got3, err := db.GetCard(ctx, c3.ID)
	if err != nil || got3.Status != model.CardMerged {
		t.Fatalf("other objective card %+v %v", got3, err)
	}
	queued, err := db.GetCard(ctx, c2.ID)
	if err != nil || queued.Status != model.CardMergeQueued {
		t.Fatalf("second card of A should still be queued: %+v %v", queued, err)
	}
	if _, err := os.Stat(c1.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree %s still present (%v)", c1.Worktree, err)
	}
	if !branchExists(t, repo, CardBranch(objA, c1.ID)) {
		t.Fatal("card branch deleted")
	}
	mid := head
	if err := q.Step(ctx, objA); err != nil {
		t.Fatal(err)
	}
	head, err = gitRev(repo, "integ-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ancestor(t, repo, mid, head) || head == mid {
		t.Fatalf("second merge did not fast-forward %s -> %s", mid, head)
	}
	show, err := gitCmd(repo, "show", head+":b.txt")
	if err != nil || strings.TrimSpace(show) != "two" {
		t.Fatalf("merged b.txt %q %v", show, err)
	}
	if err := q.Step(ctx, objA); err != nil {
		t.Fatal(err)
	}
	again, err := gitRev(repo, "integ-a")
	if err != nil || again != head {
		t.Fatalf("second step moved HEAD %s -> %s", head, again)
	}
}

func TestQueueConflictEnqueuesRebase(t *testing.T) {
	ctx := context.Background()
	db, repo := openFixture(t)
	proj := seedProject(t, db, repo, "p", "true")
	obj := seedObjective(t, db, repo, proj, "o", "main")
	c1 := commitCard(t, db, repo, obj, "main", "c1", "a.txt", "left\n")
	c2 := commitCard(t, db, repo, obj, "main", "c2", "a.txt", "right\n")
	q := New(db)
	if err := q.Enqueue(ctx, c1.ID); err != nil || q.Enqueue(ctx, c2.ID) != nil {
		t.Fatal("enqueue")
	}
	if err := q.Step(ctx, obj); err != nil {
		t.Fatal(err)
	}
	head, err := gitRev(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Step(ctx, obj); err != nil {
		t.Fatal(err)
	}
	got, err := gitRev(repo, "main")
	if err != nil || got != head {
		t.Fatalf("conflict moved HEAD %s -> %s (%v)", head, got, err)
	}
	follow, err := db.GetCard(ctx, "rb-"+c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if follow.Kind != model.KindRebase || follow.Status != model.CardReady {
		t.Fatalf("rebase card %+v", follow)
	}
	if follow.Base != CardBranch(obj, c2.ID) || len(follow.Deps) != 1 || follow.Deps[0] != c2.ID {
		t.Fatalf("rebase does not continue on the card branch: %+v", follow)
	}
	if !strings.Contains(follow.Body, "file: a.txt") || !strings.Contains(follow.Body, "<<<<<<<") || !strings.Contains(follow.ReworkNote, ">>>>>>>") {
		t.Fatalf("conflict detail missing:\n%s", follow.Body)
	}
	blocked, err := db.GetCard(ctx, c2.ID)
	if err != nil || blocked.Status == model.CardMerged || blocked.Status == model.CardMergeQueued {
		t.Fatalf("conflicted card %+v", blocked)
	}
	if !branchExists(t, repo, CardBranch(obj, c2.ID)) {
		t.Fatal("card branch deleted")
	}
	if err := q.Step(ctx, obj); err != nil {
		t.Fatal(err)
	}
	again, err := db.GetCard(ctx, "rb2-"+c2.ID)
	if err == nil {
		t.Fatalf("duplicate rebase %+v", again)
	}
}

func TestQueueRedTestEnqueuesRepair(t *testing.T) {
	ctx := context.Background()
	db, repo := openFixture(t)
	proj := seedProject(t, db, repo, "p", "echo RED_TEST; exit 1")
	obj := seedObjective(t, db, repo, proj, "o", "main")
	card := commitCard(t, db, repo, obj, "main", "c1", "a.txt", "changed\n")
	card.Card.Acceptance = []string{"true"}
	if err := db.UpdateCard(ctx, &card.Card); err != nil {
		t.Fatal(err)
	}
	q := New(db)
	if err := q.Enqueue(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	before, err := gitRev(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Step(ctx, obj); err != nil {
		t.Fatal(err)
	}
	after, err := gitRev(repo, "main")
	if err != nil || after != before {
		t.Fatalf("red test moved HEAD %s -> %s", before, after)
	}
	repair, err := db.GetCard(ctx, "rp-"+card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repair.Kind != model.KindRepair || repair.Base != CardBranch(obj, card.ID) {
		t.Fatalf("repair %+v", repair)
	}
	if !strings.Contains(repair.Body, "RED_TEST") || !strings.Contains(repair.ReworkNote, "RED_TEST") {
		t.Fatalf("failure output missing:\n%s", repair.Body)
	}
	orig, err := db.GetCard(ctx, card.ID)
	if err != nil || orig.Status == model.CardMerged {
		t.Fatalf("original %+v", orig)
	}
}

type seededCard struct {
	model.Card
	Worktree string
}

func openFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=cardex", "GIT_AUTHOR_EMAIL=cardex@cardex.invalid", "GIT_COMMITTER_NAME=cardex", "GIT_COMMITTER_EMAIL=cardex@cardex.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("-c", "commit.gpgsign=false", "add", "a.txt")
	git("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	db, err := store.Open(filepath.Join(root, "home", "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, repo
}

func seedProject(t *testing.T, db *store.Store, repo, id, testCmd string) string {
	t.Helper()
	p := model.Project{ID: id, RepoPath: repo, WorktreeRoot: filepath.Join(repo, "wt"), TestCmd: testCmd, DesignDir: "docs/design/"}
	if err := db.CreateProject(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func seedObjective(t *testing.T, db *store.Store, repo, project, id, branch string) string {
	t.Helper()
	if branch != "main" {
		cmd := exec.Command("git", "branch", branch, "main")
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("branch %s: %v %s", branch, err, out)
		}
	}
	o := model.Objective{
		ID: id, Project: project, Title: id, Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: branch,
	}
	if err := db.CreateObjective(context.Background(), &o); err != nil {
		t.Fatal(err)
	}
	return o.ID
}

func commitCard(t *testing.T, db *store.Store, repo, obj, base, id, file, body string) seededCard {
	t.Helper()
	card := model.Card{
		ID: id, Objective: obj, Kind: model.KindDev, Title: id, Tier: model.TierT2, Status: model.CardReady,
	}
	if err := db.CreateCard(context.Background(), &card); err != nil {
		t.Fatal(err)
	}
	branch := CardBranch(obj, id)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("checkout", base)
	git("checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(repo, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	git("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", file)
	git("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", id)
	git("checkout", "main")
	wt := filepath.Join(t.TempDir(), id)
	git("worktree", "add", wt, branch)
	attempt := model.Attempt{ID: "a-" + id, Card: id, Worktree: wt, Branch: branch}
	if err := db.CreateAttempt(context.Background(), &attempt); err != nil {
		t.Fatal(err)
	}
	return seededCard{Card: card, Worktree: wt}
}

func TestQueueRepairChainClosesCompletedCards(t *testing.T) {
	ctx := context.Background()
	db, repo := openFixture(t)
	proj := seedProject(t, db, repo, "p", "true")
	obj := seedObjective(t, db, repo, proj, "o", "main")
	skel := model.Card{ID: "skel", Objective: obj, Kind: model.KindSkeleton, Title: "skel", Tier: model.TierT2, Status: model.CardReady}
	if err := db.CreateCard(ctx, &skel); err != nil {
		t.Fatal(err)
	}
	c := commitCard(t, db, repo, obj, "main", "c", "a.txt", "shipped\n")
	c.Card.Status = model.CardWaiting
	c.Card.Deps = []string{"skel"}
	if err := db.UpdateCard(ctx, &c.Card); err != nil {
		t.Fatal(err)
	}
	rpWT := filepath.Join(t.TempDir(), "rp")
	rp2WT := filepath.Join(t.TempDir(), "rp2")
	detachWorktree(t, c.Worktree)
	addWorktree(t, repo, CardBranch(obj, "c"), rpWT)
	detachWorktree(t, rpWT)
	addWorktree(t, repo, CardBranch(obj, "c"), rp2WT)
	rp := model.Card{
		ID: "rp-c", Objective: obj, Kind: model.KindRepair, Title: "rp", Tier: model.TierT2,
		Status: model.CardWaiting, Deps: []string{"c"},
	}
	rp2 := model.Card{
		ID: "rp-rp-c", Objective: obj, Kind: model.KindRepair, Title: "rp2", Tier: model.TierT2,
		Status: model.CardMergeQueued, Deps: []string{"rp-c"},
	}
	if err := db.CreateCard(ctx, &rp); err != nil || db.CreateCard(ctx, &rp2) != nil {
		t.Fatal("cards")
	}
	for _, item := range []struct{ id, path string }{{"rp-c", rpWT}, {"rp-rp-c", rp2WT}} {
		attempt := model.Attempt{ID: "a-" + item.id, Card: item.id, Worktree: item.path, Branch: CardBranch(obj, "c")}
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
	}
	before, err := gitRev(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	q := New(db)
	if err := q.Step(ctx, obj); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c", "rp-c", "rp-rp-c"} {
		got, err := db.GetCard(ctx, id)
		if err != nil || got.Status != model.CardMerged {
			t.Fatalf("%s %+v %v", id, got, err)
		}
	}
	left, err := db.GetCard(ctx, "skel")
	if err != nil || left.Status != model.CardReady {
		t.Fatalf("ordinary dep %+v %v", left, err)
	}
	for _, path := range []string{c.Worktree, rpWT, rp2WT} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("worktree %s still present (%v)", path, err)
		}
	}
	head, err := gitRev(repo, "main")
	if err != nil || head == before {
		t.Fatalf("integration did not move %s -> %s", before, head)
	}
	if err := q.Step(ctx, obj); err != nil {
		t.Fatal(err)
	}
	again, err := gitRev(repo, "main")
	if err != nil || again != head {
		t.Fatalf("second closeout moved HEAD %s -> %s", head, again)
	}
	for _, id := range []string{"c", "rp-c", "rp-rp-c"} {
		got, err := db.GetCard(ctx, id)
		if err != nil || got.Status != model.CardMerged {
			t.Fatalf("second closeout %s %+v %v", id, got, err)
		}
	}
	cards, err := db.ListCards(ctx, obj)
	if err != nil || len(cards) != 4 {
		t.Fatalf("cards %+v %v", cards, err)
	}
}

func TestRepairOfRepairLinksImmediatePredecessor(t *testing.T) {
	ctx := context.Background()
	db, repo := openFixture(t)
	proj := seedProject(t, db, repo, "p", "echo RED_TEST; exit 1")
	obj := seedObjective(t, db, repo, proj, "o", "main")
	c := commitCard(t, db, repo, obj, "main", "c", "a.txt", "shipped\n")
	c.Card.Status = model.CardWaiting
	if err := db.UpdateCard(ctx, &c.Card); err != nil {
		t.Fatal(err)
	}
	rp := model.Card{
		ID: "rp-c", Objective: obj, Kind: model.KindRepair, Title: "rp", Tier: model.TierT2,
		Status: model.CardMergeQueued, Deps: []string{"c"}, Acceptance: []string{"true"},
	}
	if err := db.CreateCard(ctx, &rp); err != nil {
		t.Fatal(err)
	}
	q := New(db)
	if err := q.Step(ctx, obj); err != nil {
		t.Fatal(err)
	}
	follow, err := db.GetCard(ctx, "rp-rp-c")
	if err != nil {
		t.Fatal(err)
	}
	if len(follow.Deps) != 1 || follow.Deps[0] != "rp-c" {
		t.Fatalf("predecessor %+v", follow.Deps)
	}
	if follow.Base != CardBranch(obj, "c") {
		t.Fatalf("base %s", follow.Base)
	}
	mid, err := db.GetCard(ctx, "rp-c")
	if err != nil || mid.Status == model.CardMerged {
		t.Fatalf("intermediate %+v %v", mid, err)
	}
}

func TestInterruptedRepairChainStillCloses(t *testing.T) {
	cases := []struct {
		name     string
		original model.CardStatus
	}{
		{name: "original waiting", original: model.CardWaiting},
		{name: "predecessor already merged", original: model.CardMerged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, repo := openFixture(t)
			proj := seedProject(t, db, repo, "p", "true")
			obj := seedObjective(t, db, repo, proj, "o", "main")
			skel := model.Card{
				ID: "skel", Objective: obj, Kind: model.KindSkeleton, Title: "skel",
				Tier: model.TierT2, Status: model.CardReady,
			}
			if err := db.CreateCard(ctx, &skel); err != nil {
				t.Fatal(err)
			}
			c := commitCard(t, db, repo, obj, "main", "c", "a.txt", "shipped\n")
			c.Card.Status = tc.original
			c.Card.Deps = []string{"skel"}
			if err := db.UpdateCard(ctx, &c.Card); err != nil {
				t.Fatal(err)
			}
			rpWT := filepath.Join(t.TempDir(), "rp")
			rp2WT := filepath.Join(t.TempDir(), "rp2")
			detachWorktree(t, c.Worktree)
			addWorktree(t, repo, CardBranch(obj, "c"), rpWT)
			detachWorktree(t, rpWT)
			addWorktree(t, repo, CardBranch(obj, "c"), rp2WT)
			for _, item := range []struct {
				id, path, dep string
			}{
				{id: "rp-c", path: rpWT, dep: "c"},
				{id: "rp-rp-c", path: rp2WT, dep: "rp-c"},
			} {
				card := model.Card{
					ID: item.id, Objective: obj, Kind: model.KindRepair, Title: item.id,
					Tier: model.TierT2, Status: model.CardMerged, Deps: []string{item.dep},
				}
				if err := db.CreateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
				attempt := model.Attempt{
					ID: "a-" + item.id, Card: item.id, Worktree: item.path,
					Branch: CardBranch(obj, "c"),
				}
				if err := db.CreateAttempt(ctx, &attempt); err != nil {
					t.Fatal(err)
				}
			}
			before, err := gitRev(repo, "main")
			if err != nil {
				t.Fatal(err)
			}
			q := New(db)
			if err := q.Step(ctx, obj); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"c", "rp-c", "rp-rp-c"} {
				got, err := db.GetCard(ctx, id)
				if err != nil || got.Status != model.CardMerged {
					t.Fatalf("%s %+v %v", id, got, err)
				}
			}
			left, err := db.GetCard(ctx, "skel")
			if err != nil || left.Status != model.CardReady {
				t.Fatalf("ordinary dep %+v %v", left, err)
			}
			for _, path := range []string{c.Worktree, rpWT, rp2WT} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("worktree %s still present (%v)", path, err)
				}
			}
			head, err := gitRev(repo, "main")
			if err != nil || head != before {
				t.Fatalf("closeout moved HEAD %s -> %s", before, head)
			}
			if err := q.Step(ctx, obj); err != nil {
				t.Fatal(err)
			}
			again, err := gitRev(repo, "main")
			if err != nil || again != head {
				t.Fatalf("second step moved HEAD %s -> %s", head, again)
			}
		})
	}
}

func TestQueueSameTargetAcceptanceIntervalsDoNotOverlap(t *testing.T) {
	ctx := context.Background()
	db, repo := openFixture(t)
	mark := filepath.Join(t.TempDir(), "marks")
	py := filepath.Join(t.TempDir(), "mark.py")
	body := "import time\np = " + strconv.Quote(mark) + "\n" +
		"def line(kind):\n" +
		"    with open(p, 'a') as f:\n" +
		"        f.write('%s %.6f\\n' % (kind, time.time()))\n" +
		"line('start')\n" +
		"time.sleep(0.5)\n" +
		"line('end')\n"
	if err := os.WriteFile(py, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := seedProject(t, db, repo, "ps", "python3 "+py)
	obj := seedObjective(t, db, repo, proj, "os", "integ")
	c1 := commitCard(t, db, repo, obj, "integ", "c1", "a.txt", "one\n")
	c2 := commitCard(t, db, repo, obj, "integ", "c2", "b.txt", "two\n")
	q := New(db)
	if err := q.Enqueue(ctx, c1.ID); err != nil || q.Enqueue(ctx, c2.ID) != nil {
		t.Fatal("enqueue")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if err := q.Step(ctx, obj); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		if err := q.Step(ctx, obj); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("same-target steps did not finish")
	}
	raw, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	var starts, ends []float64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			t.Fatal(err)
		}
		switch fields[0] {
		case "start":
			starts = append(starts, value)
		case "end":
			ends = append(ends, value)
		}
	}
	if len(starts) != 2 || len(ends) != 2 {
		t.Fatalf("marks %q", raw)
	}
	if starts[1] < ends[0] {
		t.Fatalf("acceptance intervals overlap %q", raw)
	}
	for _, id := range []string{c1.ID, c2.ID} {
		got, err := db.GetCard(ctx, id)
		if err != nil || got.Status != model.CardMerged {
			t.Fatalf("%s %+v %v", id, got, err)
		}
	}
}

func detachWorktree(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("git", "-C", path, "checkout", "--detach")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("detach %s: %v %s", path, err, out)
	}
}

func addWorktree(t *testing.T, repo, branch, path string) {
	t.Helper()
	cmd := exec.Command("git", "worktree", "add", path, branch)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add %s: %v %s", path, err, out)
	}
}

func writeCmd(t *testing.T, mark, label string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cmd-"+label+".sh")
	body := "#!/bin/sh\nprintf '%s-start\\n' " + label + " >> " + shellQuote(mark) + "\nsleep 0.6\nprintf '%s-end\\n' " + label + " >> " + shellQuote(mark) + "\n"
	if label == "B" {
		body = "#!/bin/sh\nprintf 'B-start\\n' >> " + shellQuote(mark) + "\nprintf 'B-end\\n' >> " + shellQuote(mark) + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func gitRev(repo, rev string) (string, error) {
	return gitCmd(repo, "rev-parse", rev)
}

func gitCmd(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func ancestor(t *testing.T, repo, old, newRev string) bool {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", old, newRev)
	cmd.Dir = repo
	return cmd.Run() == nil
}

func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repo
	return cmd.Run() == nil
}
