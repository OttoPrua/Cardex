package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestMain(m *testing.M) {
	if os.Getenv("CARDEX_STORE_CHILD") == "1" {
		if err := runChild(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runChild() error {
	db, err := Open(os.Getenv("CARDEX_STORE_DB"))
	if err != nil {
		return err
	}
	defer db.Close()
	pid := strconv.Itoa(os.Getpid())
	for i := 0; i < 20; i++ {
		p := model.Project{
			ID:             fmt.Sprintf("child-%d", i),
			RepoPath:       "/child/repo",
			WorktreeRoot:   "/child/worktrees/cardex",
			TestCmd:        pid,
			DesignDir:      "docs/design/",
			ForbiddenPaths: []string{},
		}
		if err := db.CreateProject(context.Background(), &p); err != nil {
			return err
		}
	}
	return nil
}

func TestOpenWALAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ver, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ver < 1 {
		t.Fatalf("schema version %d", ver)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var mode string
	if err := raw.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode %q", mode)
	}
}

func TestRoundTripAndEnums(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 10, 30, 3, 4, 5, 6, time.UTC)
	exitCode := 7

	project := model.Project{
		ID:             "p1",
		RepoPath:       "/repo",
		WorktreeRoot:   "/repo/../worktrees/cardex",
		TestCmd:        "go test ./...",
		DesignDir:      "docs/design/",
		ForbiddenPaths: []string{"secrets"},
	}
	if err := db.CreateProject(ctx, &project); err != nil {
		t.Fatal(err)
	}
	gotProject, err := db.GetProject(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotProject, project) {
		t.Fatalf("project %+v", gotProject)
	}

	objective := model.Objective{
		ID:                "o1",
		Project:           "p1",
		Title:             "ship",
		Acceptance:        model.Acceptance{Commands: []string{"go test"}, Checklist: []string{"docs"}},
		Budget:            model.Budget{SoftTokens: 10, HardTokens: 20},
		Priority:          3,
		Mode:              model.ModeManual,
		FreezeMode:        model.FreezeFast,
		Origin:            "codex:thread",
		Status:            model.ObjectiveActive,
		IntegrationBranch: "cardex/o1/main",
		Channel:           "ch-explicit",
		Model:             "gpt",
	}
	if err := db.CreateObjective(ctx, &objective); err != nil {
		t.Fatal(err)
	}
	gotObjective, err := db.GetObjective(ctx, "o1")
	if err != nil || !reflect.DeepEqual(gotObjective, objective) {
		t.Fatalf("objective %+v err %v", gotObjective, err)
	}

	node := model.DesignNode{
		ID:            "n1",
		Objective:     "o1",
		Parent:        "",
		Layer:         model.LayerModule,
		DocPath:       "docs/design/o1/n1.md",
		OpenQuestions: []string{"q"},
		Status:        model.NodeFreezable,
	}
	if err := db.CreateNode(ctx, &node); err != nil {
		t.Fatal(err)
	}
	gotNode, err := db.GetNode(ctx, "n1")
	if err != nil || !reflect.DeepEqual(gotNode, node) {
		t.Fatalf("node %+v err %v", gotNode, err)
	}

	card := model.Card{
		ID:           "c1",
		Objective:    "o1",
		Node:         "n1",
		Kind:         model.KindDev,
		Title:        "implement",
		Body:         "body",
		Tier:         model.TierT2,
		Requires:     []string{"web"},
		Claims:       []model.Claim{{ID: "cl1", Card: "c1", Path: "a.go", Symbol: "Foo"}},
		Deps:         []string{"c0"},
		Acceptance:   []string{"go test ./pkg"},
		Base:         "abc",
		Status:       model.CardReady,
		WaitReason:   "",
		ReworkCount:  1,
		Legacy:       model.Legacy{Status: "held", SourcePath: "v1/tasks/c1", Reason: "review"},
		Channel:      "ch1",
		Model:        "m1",
		ReworkNote:   "fix names",
		AuthorFamily: "fam",
		Budget:       model.Budget{SoftTokens: 1, HardTokens: 2},
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	gotCard, err := db.GetCard(ctx, "c1")
	if err != nil || !reflect.DeepEqual(gotCard, card) {
		t.Fatalf("card\n got %#v\nwant %#v\nerr %v", gotCard, card, err)
	}

	attempt := model.Attempt{
		ID:    "a1",
		Card:  "c1",
		Index: 2,
		Route: model.Route{Channel: "ch1", Model: "m1"},
		RouteReason: model.RouteReason{
			Candidates: []model.RouteCandidate{{Channel: "ch1", Model: "m1", Family: "fam", Tier: model.TierT2, Preference: 1, Remaining: 0.5}},
			Filtered:   []model.FilteredCandidate{{Channel: "ch2", Model: "m2", Reason: "redline"}},
			Scores:     []model.RouteScore{{Channel: "ch1", Model: "m1", Score: 0.4}},
		},
		Worktree:     "/wt",
		Branch:       "cardex/o1/c/c1",
		BaseCommit:   "base",
		HeadCommit:   "head",
		PID:          42,
		StartedAt:    ts,
		EndedAt:      ts.Add(time.Second),
		Exit:         &exitCode,
		Usage:        model.Usage{Tokens: 9, Source: "event", Confidence: 0.8},
		Outcome:      model.OutcomePassed,
		ErrorClass:   model.ErrorNone,
		ErrorExcerpt: "",
		PromptPath:   "/tmp/prompt.md",
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	gotAttempt, err := db.GetAttempt(ctx, "a1")
	if err != nil || !reflect.DeepEqual(gotAttempt, attempt) {
		t.Fatalf("attempt\n got %#v\nwant %#v\nerr %v", gotAttempt, attempt, err)
	}

	channel := model.Channel{
		ID:            "ch1",
		Runner:        model.RunnerClaude,
		EngineProfile: "opus",
		SourceGroup:   "sub",
		Family:        "anthropic",
		Tier:          model.TierT3,
		Models:        []string{"opus"},
		Concurrency:   2,
		QuotaWindows: []model.QuotaWindow{{
			Name: "5h", ResetRule: "rolling", ResetAt: ts, LimitTokens: 100, LimitRequests: 10,
			UsedTokens: 20, Remaining: 0.8, Confidence: 0.6,
		}},
		Redline:   0.2,
		Health:    model.HealthOK,
		LastError: "",
	}
	if err := db.CreateChannel(ctx, &channel); err != nil {
		t.Fatal(err)
	}
	gotChannel, err := db.GetChannel(ctx, "ch1")
	if err != nil || !reflect.DeepEqual(gotChannel, channel) {
		t.Fatalf("channel\n got %#v\nwant %#v\nerr %v", gotChannel, channel, err)
	}

	pos := model.Positioning{
		ID: "pos1", Version: 3, Model: "opus", Family: "anthropic",
		Tiers: []model.Tier{model.TierT2, model.TierT3}, Capabilities: []string{"web"}, Preference: 2,
	}
	if err := db.CreatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
	gotPos, err := db.GetPositioning(ctx, "pos1")
	if err != nil || !reflect.DeepEqual(gotPos, pos) {
		t.Fatalf("positioning %#v err %v", gotPos, err)
	}

	question := model.Question{
		ID: "q1", Objective: "o1", Scope: "n1", ScopeKind: "node", Kind: model.QuestionFreeze,
		Text: "freeze?", Options: []string{"yes", "no"}, Answer: "",
		Deliveries: []model.Delivery{{At: ts, Adapter: "file", Destination: "inbox"}},
	}
	if err := db.CreateQuestion(ctx, &question); err != nil {
		t.Fatal(err)
	}
	gotQ, err := db.GetQuestion(ctx, "q1")
	if err != nil || !reflect.DeepEqual(gotQ, question) {
		t.Fatalf("question %#v err %v", gotQ, err)
	}

	event := model.Event{ID: "e1", TS: ts, Entity: "card:c1", Type: "created", Data: json.RawMessage(`{"k":"v"}`)}
	if err := db.AppendEvent(ctx, &event); err != nil {
		t.Fatal(err)
	}
	gotEvent, err := db.GetEvent(ctx, "e1")
	if err != nil || !reflect.DeepEqual(gotEvent, event) {
		t.Fatalf("event %#v err %v", gotEvent, err)
	}
	if err := db.UpdateEvent(ctx, &event); !errors.Is(err, ErrAppendOnly) {
		t.Fatalf("update event: %v", err)
	}
	if err := db.DeleteEvent(ctx, "e1"); !errors.Is(err, ErrAppendOnly) {
		t.Fatalf("delete event: %v", err)
	}
	again, err := db.GetEvent(ctx, "e1")
	if err != nil || !reflect.DeepEqual(again, event) {
		t.Fatal("event changed after rejected update")
	}

	claim := model.Claim{ID: "cl2", Card: "c1", Path: "b.go", Symbol: ""}
	if err := db.CreateClaim(ctx, &claim); err != nil {
		t.Fatal(err)
	}
	gotClaim, err := db.GetClaim(ctx, "cl2")
	if err != nil || !reflect.DeepEqual(gotClaim, claim) {
		t.Fatalf("claim %#v", gotClaim)
	}

	for _, kind := range model.AllCardKinds() {
		c := card
		c.ID = "k-" + string(kind)
		c.Kind = kind
		c.Claims = []model.Claim{}
		if err := db.CreateCard(ctx, &c); err != nil {
			t.Fatalf("kind %s: %v", kind, err)
		}
	}
	for _, tier := range model.AllTiers() {
		c := card
		c.ID = "t-" + string(tier)
		c.Tier = tier
		c.Claims = []model.Claim{}
		if err := db.CreateCard(ctx, &c); err != nil {
			t.Fatalf("tier %s: %v", tier, err)
		}
	}
	for _, status := range model.AllCardStatuses() {
		c := card
		c.ID = "s-" + string(status)
		c.Status = status
		c.Claims = []model.Claim{}
		if err := db.CreateCard(ctx, &c); err != nil {
			t.Fatalf("status %s: %v", status, err)
		}
	}
	for _, outcome := range model.AllOutcomes() {
		a := attempt
		a.ID = "o-" + string(outcome)
		a.Outcome = outcome
		a.ErrorClass = model.ErrorClass(outcome)
		if err := db.CreateAttempt(ctx, &a); err != nil {
			t.Fatalf("outcome %s: %v", outcome, err)
		}
		got, err := db.GetAttempt(ctx, a.ID)
		if err != nil || got.Outcome != outcome || got.ErrorClass != model.ErrorClass(outcome) {
			t.Fatalf("stored %s got %s/%s err %v", outcome, got.Outcome, got.ErrorClass, err)
		}
	}
	for _, layer := range model.AllLayers() {
		n := node
		n.ID = "l-" + string(layer)
		n.Layer = layer
		if err := db.CreateNode(ctx, &n); err != nil {
			t.Fatalf("layer %s: %v", layer, err)
		}
	}
	for _, st := range model.AllNodeStatuses() {
		n := node
		n.ID = "ns-" + string(st)
		n.Status = st
		if err := db.CreateNode(ctx, &n); err != nil {
			t.Fatalf("node status %s: %v", st, err)
		}
	}
	for _, mode := range model.AllObjectiveModes() {
		o := objective
		o.ID = "m-" + string(mode)
		o.Mode = mode
		if err := db.CreateObjective(ctx, &o); err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
	for _, fm := range model.AllFreezeModes() {
		o := objective
		o.ID = "f-" + string(fm)
		o.FreezeMode = fm
		if err := db.CreateObjective(ctx, &o); err != nil {
			t.Fatalf("freeze %s: %v", fm, err)
		}
	}
	for _, st := range model.AllObjectiveStatuses() {
		o := objective
		o.ID = "os-" + string(st)
		o.Status = st
		if err := db.CreateObjective(ctx, &o); err != nil {
			t.Fatalf("objective status %s: %v", st, err)
		}
	}
	for _, h := range model.AllHealth() {
		ch := channel
		ch.ID = "h-" + string(h)
		ch.Health = h
		if err := db.CreateChannel(ctx, &ch); err != nil {
			t.Fatalf("health %s: %v", h, err)
		}
	}
	for _, r := range model.AllRunners() {
		ch := channel
		ch.ID = "r-" + string(r)
		ch.Runner = r
		if err := db.CreateChannel(ctx, &ch); err != nil {
			t.Fatalf("runner %s: %v", r, err)
		}
	}
	for _, k := range model.AllQuestionKinds() {
		q := question
		q.ID = "qk-" + string(k)
		q.Kind = k
		if err := db.CreateQuestion(ctx, &q); err != nil {
			t.Fatalf("question %s: %v", k, err)
		}
	}
	bad := card
	bad.ID = "bad"
	bad.Kind = "held"
	bad.Claims = nil
	if err := db.CreateCard(ctx, &bad); err == nil {
		t.Fatal("held kind accepted")
	}

	project.RepoPath = "/repo2"
	if err := db.UpdateProject(ctx, &project); err != nil {
		t.Fatal(err)
	}
	gotProject, err = db.GetProject(ctx, "p1")
	if err != nil || gotProject.RepoPath != "/repo2" {
		t.Fatalf("update project %+v %v", gotProject, err)
	}
	if err := db.DeleteProject(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetProject(ctx, "p1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted project err %v", err)
	}
}

func TestTransactionRollsBack(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	p := model.Project{ID: "px", RepoPath: "/r", WorktreeRoot: "/w", TestCmd: "go test", DesignDir: "docs", ForbiddenPaths: []string{}}
	err := db.WithTx(ctx, func(ctx context.Context) error {
		if err := db.CreateProject(ctx, &p); err != nil {
			return err
		}
		return errors.New("rollback")
	})
	if err == nil {
		t.Fatal("expected rollback")
	}
	if _, err := db.GetProject(ctx, "px"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rolled back row visible: %v", err)
	}
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		return db.CreateProject(ctx, &p)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetProject(ctx, "px"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cardex.db")
	db := openAt(t, path)
	ctx := context.Background()

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	start := make(chan struct{})
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < 20; i++ {
				p := model.Project{
					ID:             fmt.Sprintf("g%d-%d", g, i),
					RepoPath:       "/repo",
					WorktreeRoot:   "/wt",
					TestCmd:        strconv.Itoa(os.Getpid()),
					DesignDir:      "docs/design/",
					ForbiddenPaths: []string{},
				}
				if err := db.CreateProject(context.Background(), &p); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}
	var buf bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "CARDEX_STORE_CHILD=1", "CARDEX_STORE_DB="+path)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("goroutine: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v\n%s", err, buf.String())
	}
	parent := strconv.Itoa(os.Getpid())
	childPID := ""
	for i := 0; i < 20; i++ {
		for _, id := range []string{fmt.Sprintf("g0-%d", i), fmt.Sprintf("g1-%d", i), fmt.Sprintf("child-%d", i)} {
			got, err := db.GetProject(ctx, id)
			if err != nil {
				t.Fatalf("missing %s: %v", id, err)
			}
			if id == "child-0" {
				childPID = got.TestCmd
			}
		}
	}
	if childPID == "" || childPID == parent {
		t.Fatalf("child pid %q parent %s", childPID, parent)
	}
	listed, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 60 {
		t.Fatalf("listed %d", len(listed))
	}
}

func openDB(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "cardex.db"))
}

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
