package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/store"
)

func TestImportPlacementDryRunAndIdempotent(t *testing.T) {
	v1 := t.TempDir()
	writeV1(t, v1, "tasks/queued.json", map[string]any{
		"id": "q1", "title": "Queue the lighthouse", "status": "queued", "project": "northwind",
		"dir": "/tmp/northwind", "model": "should-not-pin",
		"frozen_route": map[string]any{"primary": map[string]any{"runner": "grok", "model": "grok-test"}},
	})
	writeV1(t, v1, "tasks/running.json", map[string]any{
		"id": "r1", "title": "Run the buoy", "status": "running", "project": "northwind",
		"runner_pref": "codex", "model": "also-not-a-pin",
	})
	writeV1(t, v1, "tasks/held-a.json", map[string]any{
		"id": "h1", "title": "Hold for a chart", "status": "held", "project": "northwind",
		"hold_reason": "waiting on the harbormaster",
	})
	writeV1(t, v1, "tasks/held-b.json", map[string]any{
		"id": "h2", "title": "Hold for a tide", "status": "HELD", "project": "northwind",
		"last_error": "quota review",
	})
	writeV1(t, v1, "tasks/done.json", map[string]any{
		"id": "d1", "title": "Done chart", "status": "done", "project": "northwind",
	})
	writeV1(t, v1, "archive/failed.json", map[string]any{
		"id": "f1", "title": "Failed sounding", "status": "failed", "project": "northwind",
	})
	writeV1(t, v1, "archive/canceled.json", map[string]any{
		"id": "c1", "title": "Canceled watch", "status": "canceled", "project": "northwind",
	})
	writeV1(t, v1, "workflows/module.json", map[string]any{
		"id": "w1", "goal": "Keep the lamp", "status": "done", "module_id": "northwind", "repo": "/tmp/northwind",
	})
	if err := os.WriteFile(filepath.Join(v1, "tasks", "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, v1)

	v2 := t.TempDir()
	ctx := context.Background()
	report, err := Import(ctx, v2, v1, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun {
		t.Fatal("dry-run flag")
	}
	if _, err := os.Stat(filepath.Join(v2, "cardex.db")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a database: %v", err)
	}
	wantV1 := map[string]int{"queued": 1, "running": 1, "held": 2, "done": 2, "failed": 1, "canceled": 1, "unparsed": 1}
	for status, n := range wantV1 {
		if report.Counts["v1:"+status] != n {
			t.Fatalf("v1 %s = %d, report %#v", status, n, report.Counts)
		}
	}
	wantV2 := map[string]int{"ready": 2, "paused": 2, "done": 2, "failed": 1, "canceled": 1, "history": 1}
	for place, n := range wantV2 {
		if report.Counts["v2:"+place] != n {
			t.Fatalf("v2 %s = %d, report %#v", place, n, report.Counts)
		}
	}
	if len(report.Unparsed) != 1 || !strings.Contains(report.Unparsed[0], "broken.json") || !strings.Contains(report.Unparsed[0], "invalid json") {
		t.Fatalf("unparsed %#v", report.Unparsed)
	}
	if after := snapshotTree(t, v1); after != before {
		t.Fatalf("dry-run changed v1\nbefore %s\nafter %s", before, after)
	}

	if _, err := Import(ctx, v2, v1, false); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, v2)
	cards := cardsOf(t, db)
	objs := objectivesOf(t, db)
	if len(cards) != 9 {
		t.Fatalf("cards %d", len(cards))
	}
	if len(objs) != 2 {
		t.Fatalf("objectives %+v", objs)
	}
	objByID := map[string]model.Objective{}
	for _, obj := range objs {
		objByID[obj.ID] = obj
		if obj.Title != importObjectiveTitle || obj.Mode != model.ModeManual {
			t.Fatalf("objective %+v", obj)
		}
	}
	byID := map[string]model.Card{}
	for _, c := range cards {
		byID[c.ID] = c
		if _, ok := objByID[c.Objective]; !ok {
			t.Fatalf("%s objective %s", c.ID, c.Objective)
		}
		if c.Channel != "" || c.Model != "" {
			t.Fatalf("%s pinned channel %q model %q", c.ID, c.Channel, c.Model)
		}
	}
	if byID["v1-q1"].Objective != byID["v1-h1"].Objective || byID["v1-q1"].Objective != byID["v1-w1"].Objective {
		t.Fatal("northwind was split across objectives")
	}
	assertStatus(t, byID["v1-q1"], model.CardReady, "queued", "")
	assertStatus(t, byID["v1-r1"], model.CardReady, "running", "")
	assertStatus(t, byID["v1-h1"], model.CardPaused, "held", "waiting on the harbormaster")
	assertStatus(t, byID["v1-h2"], model.CardPaused, "held", "quota review")
	assertStatus(t, byID["v1-d1"], model.CardDone, "done", "")
	assertStatus(t, byID["v1-f1"], model.CardFailed, "failed", "")
	assertStatus(t, byID["v1-c1"], model.CardCanceled, "canceled", "")
	assertStatus(t, byID["v1-w1"], model.CardDone, "done", "")
	broken := byID[cardID("tasks/broken.json", "")]
	if broken.Status == model.CardReady || broken.Status == model.CardRunning || broken.Legacy.Status != "unparsed" {
		t.Fatalf("broken %+v", broken)
	}
	if broken.Legacy.SourcePath != "tasks/broken.json" {
		t.Fatalf("source %q", broken.Legacy.SourcePath)
	}
	assertHint(t, db, "v1-q1", true, "")
	assertHint(t, db, "v1-r1", false, "codex")
	assertNoHint(t, db, "v1-d1")
	if byID["v1-q1"].Legacy.SourcePath != "tasks/queued.json" {
		t.Fatalf("queued source %q", byID["v1-q1"].Legacy.SourcePath)
	}

	if _, err := Import(ctx, v2, v1, false); err != nil {
		t.Fatal(err)
	}
	if again := cardsOf(t, db); len(again) != len(cards) {
		t.Fatalf("second import cards %d want %d", len(again), len(cards))
	}
	if again := objectivesOf(t, db); len(again) != len(objs) {
		t.Fatalf("second import objectives %d", len(again))
	}
	hints, err := db.ListEvents(ctx, "card:v1-q1")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range hints {
		if ev.Type == "route_hint" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("hint events %d", n)
	}
	if after := snapshotTree(t, v1); after != before {
		t.Fatal("import changed v1 bytes")
	}
}

func TestImportObjectGoalPlacesByStatus(t *testing.T) {
	v1 := t.TempDir()
	writeV1(t, v1, "tasks/held-goal.json", map[string]any{
		"id": "hg", "title": "Held goal card", "status": "held", "project": "harbor",
		"last_error":  "harvest needs_review: unverified_output",
		"runner_pref": "codex", "model": "should-not-pin",
		"goal": map[string]any{
			"outcome": "land the buoy", "observation": "unknown",
			"last_native_status": "budget_limited",
		},
	})
	writeV1(t, v1, "tasks/running-goal.json", map[string]any{
		"id": "rg", "title": "Running goal card", "status": "running", "project": "harbor",
		"frozen_route": map[string]any{"name": "mixed_daily_development", "legs": []any{map[string]any{"runner": "grok", "model": "grok-test"}}},
		"goal":         map[string]any{"scope": "object goal is not a title"},
	})
	writeV1(t, v1, "archive/done-goal.json", map[string]any{
		"id": "dg", "title": "Done goal card", "status": "done", "project": "harbor",
		"goal": map[string]any{"acceptance": "already finished"},
	})
	writeV1(t, v1, "workflows/string-goal.json", map[string]any{
		"id": "sg", "goal": "Keep the string title", "status": "canceled", "module_id": "harbor",
	})

	v2 := t.TempDir()
	ctx := context.Background()
	report, err := Import(ctx, v2, v1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Unparsed) != 0 {
		t.Fatalf("unparsed %#v", report.Unparsed)
	}
	want := map[string]int{"v1:held": 1, "v1:running": 1, "v1:done": 1, "v1:canceled": 1, "v2:paused": 1, "v2:ready": 1, "v2:done": 1, "v2:canceled": 1}
	for key, n := range want {
		if report.Counts[key] != n {
			t.Fatalf("%s = %d, report %#v", key, report.Counts[key], report.Counts)
		}
	}

	if _, err := Import(ctx, v2, v1, false); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, v2)
	cards := cardsOf(t, db)
	if len(cards) != 4 {
		t.Fatalf("cards %d", len(cards))
	}
	byID := map[string]model.Card{}
	for _, c := range cards {
		byID[c.ID] = c
		if c.Channel != "" || c.Model != "" {
			t.Fatalf("%s pinned channel %q model %q", c.ID, c.Channel, c.Model)
		}
	}
	for _, obj := range objectivesOf(t, db) {
		if obj.Title != importObjectiveTitle || obj.Mode != model.ModeManual {
			t.Fatalf("objective %+v", obj)
		}
	}
	assertStatus(t, byID["v1-hg"], model.CardPaused, "held", "harvest needs_review: unverified_output")
	assertStatus(t, byID["v1-rg"], model.CardReady, "running", "")
	assertStatus(t, byID["v1-dg"], model.CardDone, "done", "")
	assertStatus(t, byID["v1-sg"], model.CardCanceled, "canceled", "")
	if byID["v1-hg"].Title != "Held goal card" || byID["v1-sg"].Title != "Keep the string title" {
		t.Fatalf("titles held %q string-goal %q", byID["v1-hg"].Title, byID["v1-sg"].Title)
	}
	if byID["v1-hg"].Legacy.SourcePath != "tasks/held-goal.json" || byID["v1-dg"].Legacy.SourcePath != "archive/done-goal.json" {
		t.Fatalf("sources %+v %+v", byID["v1-hg"].Legacy, byID["v1-dg"].Legacy)
	}
	assertHint(t, db, "v1-hg", false, "codex")
	assertHint(t, db, "v1-rg", true, "")
	assertNoHint(t, db, "v1-dg")

	if _, err := Import(ctx, v2, v1, false); err != nil {
		t.Fatal(err)
	}
	if again := cardsOf(t, db); len(again) != len(cards) {
		t.Fatalf("second import cards %d want %d", len(again), len(cards))
	}
}

func TestPlaceKeepsHeldReasonsAndRefusesPins(t *testing.T) {
	held := place("tasks/h.json", &v1Record{ID: "h", Status: "held", HoldReason: "a chart", Model: "gpt"}, "")
	other := place("tasks/i.json", &v1Record{ID: "i", Status: "held", LastError: "a tide"}, "")
	if held.Legacy.Reason == other.Legacy.Reason || held.Legacy.Reason != "a chart" || other.Legacy.Reason != "a tide" {
		t.Fatalf("%q %q", held.Legacy.Reason, other.Legacy.Reason)
	}
	if held.Status != model.CardPaused || held.V2Place != "paused" {
		t.Fatalf("%+v", held)
	}
	pinned := place("tasks/q.json", &v1Record{
		ID: "q", Status: "queued", Model: "hidden", RunnerPref: "codex",
		FrozenRoute: json.RawMessage(`{"primary":{"model":"grok-test"}}`),
	}, "")
	if pinned.Status != model.CardReady || pinned.Hint.RunnerPref != "codex" || !frozenPresent(pinned.Hint.FrozenRoute) {
		t.Fatalf("%+v", pinned)
	}
	bad := place("tasks/bad.json", nil, "invalid json")
	if bad.Status == model.CardReady || bad.Status == model.CardRunning || bad.Unparsed == "" {
		t.Fatalf("%+v", bad)
	}
}

func writeV1(t *testing.T, root, rel string, body map[string]any) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var b []byte
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b = append(b, []byte(rel)...)
		b = append(b, '\n')
		if d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b = append(b, body...)
		b = append(b, '\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func openDB(t *testing.T, home string) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func cardsOf(t *testing.T, db *store.Store) []model.Card {
	t.Helper()
	cards, err := db.ListCards(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

func objectivesOf(t *testing.T, db *store.Store) []model.Objective {
	t.Helper()
	objs, err := db.ListObjectives(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func assertStatus(t *testing.T, card model.Card, status model.CardStatus, legacy, reason string) {
	t.Helper()
	if card.ID == "" {
		t.Fatal("missing card")
	}
	if card.Status != status || card.Legacy.Status != legacy || card.Legacy.Reason != reason {
		t.Fatalf("%s status %s legacy %+v", card.ID, card.Status, card.Legacy)
	}
}

func assertHint(t *testing.T, db *store.Store, id string, frozen bool, pref string) {
	t.Helper()
	evs, err := db.ListEvents(context.Background(), "card:"+id)
	if err != nil {
		t.Fatal(err)
	}
	var hint routeHint
	found := false
	for _, ev := range evs {
		if ev.Type != "route_hint" {
			continue
		}
		found = true
		if err := json.Unmarshal(ev.Data, &hint); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatalf("%s has no hint", id)
	}
	if frozenPresent(hint.FrozenRoute) != frozen || hint.RunnerPref != pref {
		t.Fatalf("%s hint %+v", id, hint)
	}
}

func assertNoHint(t *testing.T, db *store.Store, id string) {
	t.Helper()
	evs, err := db.ListEvents(context.Background(), "card:"+id)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == "route_hint" {
			t.Fatalf("%s unexpected hint %s", id, ev.Data)
		}
	}
}

type continuableSpec struct {
	rel, id, status, project, title string
	dir, repo, hold                 string
	prompts                         []string
	step                            int
	deps                            []string
	keep                            bool
}

func TestImportKeepsContinuableBodyDepsAndRepo(t *testing.T) {
	v1 := t.TempDir()
	rows := []continuableSpec{
		{rel: "archive/done.json", id: "d1", status: "done", project: "lumen", title: "Done chart"},
		{rel: "archive/queued.json", id: "aq", status: "queued", project: "lumen", title: "Archive queue", prompts: []string{"archive prompt text"}, step: 4, deps: []string{"q1"}, keep: true},
		{rel: "archive/harbor-done.json", id: "hd", status: "done", project: "harbor", title: "Harbor done", dir: "/tmp/harbor-old"},
		{rel: "tasks/held.json", id: "h1", status: "held", project: "lumen", title: "Hold the lamp", prompts: []string{"lumen held prompt"}, step: 2, deps: []string{"r1"}, hold: "waiting on the lamp", keep: true},
		{rel: "tasks/harbor-queued.json", id: "hq", status: "queued", project: "harbor", title: "Harbor queue", dir: "/tmp/harbor-new", prompts: []string{"harbor prompt"}, step: 5, deps: []string{"hd"}, keep: true},
		{rel: "tasks/keel.json", id: "k1", status: "queued", project: "keel", title: "Keel queue", repo: "/tmp/keel", prompts: []string{"keel prompt"}, step: 6, keep: true},
		{rel: "tasks/queued.json", id: "q1", status: "queued", project: "lumen", title: "Queue the ferry", dir: "/tmp/lumen", prompts: []string{"lumen queued prompt", "lumen second prompt"}, step: 1, deps: []string{"r1", "h1"}, keep: true},
		{rel: "tasks/running.json", id: "r1", status: "running", project: "lumen", title: "Run the buoy", prompts: []string{"lumen running prompt"}, step: 0, keep: true},
		{rel: "workflows/live.json", id: "w1", status: "running", project: "lumen", title: "Workflow live", prompts: []string{"workflow prompt text"}, step: 3, deps: []string{"q1"}, keep: true},
	}
	for _, row := range rows {
		body := map[string]any{
			"id": row.id, "title": row.title, "status": row.status, "project": row.project, "step": row.step,
		}
		if row.dir != "" {
			body["dir"] = row.dir
		}
		if row.repo != "" {
			body["repo"] = row.repo
		}
		if row.hold != "" {
			body["hold_reason"] = row.hold
		}
		if row.prompts != nil {
			body["prompts"] = row.prompts
		}
		if row.deps != nil {
			body["depends_on"] = row.deps
		}
		writeV1(t, v1, row.rel, body)
	}
	before := snapshotTree(t, v1)
	v2 := t.TempDir()
	ctx := context.Background()
	if _, err := Import(ctx, v2, v1, false); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, v2)
	cards := cardsOf(t, db)
	objs := objectivesOf(t, db)
	bySource := map[string]model.Card{}
	for _, card := range cards {
		bySource[card.Legacy.SourcePath] = card
	}
	byV1 := map[string]model.Card{}
	for _, row := range rows {
		card, ok := bySource[row.rel]
		if !ok {
			t.Fatalf("missing %s", row.rel)
		}
		byV1[row.id] = card
	}
	for _, row := range rows {
		if !row.keep {
			continue
		}
		card := byV1[row.id]
		if importedStep(t, card.Body) != row.step {
			t.Fatalf("%s step %d body %q", row.id, row.step, card.Body)
		}
		_, rest, _ := strings.Cut(card.Body, "\n")
		for _, prompt := range row.prompts {
			if !strings.Contains(rest, prompt) {
				t.Fatalf("%s missing prompt %q in %q", row.id, prompt, card.Body)
			}
		}
		var want []string
		for _, dep := range row.deps {
			want = append(want, byV1[dep].ID)
		}
		if strings.Join(card.Deps, ",") != strings.Join(want, ",") {
			t.Fatalf("%s deps %v want %v", row.id, card.Deps, want)
		}
		switch row.status {
		case "queued", "running":
			if card.Status != model.CardReady {
				t.Fatalf("%s status %s", row.id, card.Status)
			}
		case "held":
			if card.Status != model.CardPaused || card.Legacy.Reason != row.hold || card.Legacy.Status != "held" {
				t.Fatalf("%s held %+v", row.id, card)
			}
		}
	}
	lumen, err := db.GetProject(ctx, projectID("lumen"))
	if err != nil {
		t.Fatal(err)
	}
	if lumen.RepoPath != "/tmp/lumen" || lumen.RepoPath == v1 {
		t.Fatalf("lumen repo %q home %q", lumen.RepoPath, v1)
	}
	harbor, err := db.GetProject(ctx, projectID("harbor"))
	if err != nil {
		t.Fatal(err)
	}
	if harbor.RepoPath != "/tmp/harbor-old" && harbor.RepoPath != "/tmp/harbor-new" || harbor.RepoPath == v1 {
		t.Fatalf("harbor repo %q", harbor.RepoPath)
	}
	keel, err := db.GetProject(ctx, projectID("keel"))
	if err != nil {
		t.Fatal(err)
	}
	if keel.RepoPath != "/tmp/keel" || keel.RepoPath == v1 {
		t.Fatalf("keel repo %q", keel.RepoPath)
	}
	if _, err := Import(ctx, v2, v1, false); err != nil {
		t.Fatal(err)
	}
	if again := cardsOf(t, db); len(again) != len(cards) {
		t.Fatalf("second import cards %d want %d", len(again), len(cards))
	}
	if again := objectivesOf(t, db); len(again) != len(objs) {
		t.Fatalf("second import objectives %d want %d", len(again), len(objs))
	}
	afterCards := cardsOf(t, db)
	for _, card := range afterCards {
		if card.ID == byV1["q1"].ID && card.Body != byV1["q1"].Body {
			t.Fatalf("second import rewrote body %q", card.Body)
		}
	}
	if after := snapshotTree(t, v1); after != before {
		t.Fatal("import changed v1 bytes")
	}
}

func importedStep(t *testing.T, body string) int {
	t.Helper()
	line, _, _ := strings.Cut(body, "\n")
	if !strings.HasPrefix(line, "step: ") {
		t.Fatalf("step line %q", body)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(line, "step: "))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportContinuableBodyKeepsStepBoundaries(t *testing.T) {
	type row struct {
		name      string
		rel       string
		status    string
		prompts   []string
		step      int
		omitStep  bool
		deps      []string
		stepLine  string
		completed []string
		remaining []string
	}
	rows := []row{
		{name: "queued-ab", rel: "tasks/queued-ab.json", status: "queued", prompts: []string{"A\nB", "C"}, step: 1, deps: []string{"keep"}, stepLine: "step: 1", completed: []string{"A\nB"}, remaining: []string{"C"}},
		{name: "queued-bc", rel: "tasks/queued-bc.json", status: "queued", prompts: []string{"A", "B\nC"}, step: 1, stepLine: "step: 1", completed: []string{"A"}, remaining: []string{"B\nC"}},
		{name: "running-ab", rel: "tasks/running-ab.json", status: "running", prompts: []string{"A\nB", "C"}, step: 1, stepLine: "step: 1", completed: []string{"A\nB"}, remaining: []string{"C"}},
		{name: "running-bc", rel: "tasks/running-bc.json", status: "running", prompts: []string{"A", "B\nC"}, step: 1, stepLine: "step: 1", completed: []string{"A"}, remaining: []string{"B\nC"}},
		{name: "held-ab", rel: "tasks/held-ab.json", status: "held", prompts: []string{"A\nB", "C"}, step: 1, stepLine: "step: 1", completed: []string{"A\nB"}, remaining: []string{"C"}},
		{name: "held-bc", rel: "tasks/held-bc.json", status: "held", prompts: []string{"A", "B\nC"}, step: 1, stepLine: "step: 1", completed: []string{"A"}, remaining: []string{"B\nC"}},
		{name: "step0", rel: "tasks/step0.json", status: "queued", prompts: []string{"ONLY"}, stepLine: "step: 0", remaining: []string{"ONLY"}},
		{name: "workflow", rel: "workflows/flow.json", status: "running", prompts: []string{"A\nB", "C"}, step: 1, stepLine: "step: 1", completed: []string{"A\nB"}, remaining: []string{"C"}},
		{name: "later", rel: "tasks/later.json", status: "queued", prompts: []string{"A", "B", "C\nD"}, step: 1, stepLine: "step: 1", completed: []string{"A"}, remaining: []string{"B", "C\nD"}},
		{name: "past", rel: "tasks/past.json", status: "held", prompts: []string{"A\nB", "C"}, step: 4, stepLine: "step: 4", completed: []string{"A\nB", "C"}},
		{name: "negative", rel: "tasks/negative.json", status: "running", prompts: []string{"A", "B\nC"}, step: -1, stepLine: "step: -1", remaining: []string{"A", "B\nC"}},
		{name: "negative-queued", rel: "tasks/negative-queued.json", status: "queued", prompts: []string{"A\nB", "C"}, step: -2, stepLine: "step: -2", remaining: []string{"A\nB", "C"}},
		{name: "negative-held", rel: "tasks/negative-held.json", status: "held", prompts: []string{"A", "B\nC"}, step: -3, stepLine: "step: -3", remaining: []string{"A", "B\nC"}},
		{name: "missing", rel: "tasks/missing.json", status: "queued", prompts: []string{"MISS"}, omitStep: true, stepLine: "step: 0", remaining: []string{"MISS"}},
	}
	v1 := t.TempDir()
	for _, row := range rows {
		body := map[string]any{
			"id": row.name, "title": row.name, "status": row.status, "project": "boundary",
			"prompts": row.prompts,
		}
		if !row.omitStep {
			body["step"] = row.step
		}
		if row.deps != nil {
			body["depends_on"] = row.deps
		}
		writeV1(t, v1, row.rel, body)
	}
	v2 := t.TempDir()
	if _, err := Import(context.Background(), v2, v1, false); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, v2)
	bySource := map[string]model.Card{}
	for _, card := range cardsOf(t, db) {
		bySource[card.Legacy.SourcePath] = card
	}
	bodies := map[string]string{}
	remainings := map[string][]string{}
	for _, row := range rows {
		card, ok := bySource[row.rel]
		if !ok {
			t.Fatalf("missing %s", row.rel)
		}
		line, completed, remaining := parseContinuableBody(t, card.Body)
		if line != row.stepLine {
			t.Fatalf("%s first line %q", row.name, line)
		}
		if !sameStrings(completed, row.completed) || !sameStrings(remaining, row.remaining) {
			t.Fatalf("%s completed %q remaining %q", row.name, completed, remaining)
		}
		joined := append(append([]string{}, completed...), remaining...)
		if !sameStrings(joined, row.prompts) {
			t.Fatalf("%s prompts %q became %q", row.name, row.prompts, joined)
		}
		_, pendingRaw := splitContinuableSections(t, card.Body)
		for _, done := range completed {
			if done != "" && strings.Contains(pendingRaw, done) {
				t.Fatalf("%s completed %q inside remaining %q", row.name, done, pendingRaw)
			}
		}
		if row.deps != nil {
			var want []string
			for _, id := range row.deps {
				want = append(want, "v1-"+id)
			}
			if strings.Join(card.Deps, ",") != strings.Join(want, ",") {
				t.Fatalf("%s deps %v want %v", row.name, card.Deps, want)
			}
		}
		if strings.HasPrefix(row.name, "negative") {
			if card.Status != model.CardReady && card.Status != model.CardPaused {
				t.Fatalf("%s status %s", row.name, card.Status)
			}
			if len(remaining) == 0 || len(completed) != 0 {
				t.Fatalf("%s treated as all-complete completed %q remaining %q", row.name, completed, remaining)
			}
		}
		bodies[row.name] = card.Body
		remainings[row.name] = remaining
	}
	for _, status := range []string{"queued", "running", "held"} {
		ab, bc := status+"-ab", status+"-bc"
		if bodies[ab] == bodies[bc] {
			t.Fatalf("%s pair collapsed\n%s", status, bodies[ab])
		}
		if len(remainings[ab]) != 1 || remainings[ab][0] != "C" || len(remainings[bc]) != 1 || remainings[bc][0] != "B\nC" {
			t.Fatalf("%s remaining %q vs %q", status, remainings[ab], remainings[bc])
		}
	}
	if len(remainings["step0"]) != 1 || remainings["step0"][0] != "ONLY" {
		t.Fatalf("step0 remaining %q", remainings["step0"])
	}
}

func splitContinuableSections(t *testing.T, body string) (history, pending string) {
	t.Helper()
	_, rest, ok := strings.Cut(body, "\n")
	if !ok || !strings.HasPrefix(rest, "## 已完成\n") {
		t.Fatalf("completed section %q", body)
	}
	rest = strings.TrimPrefix(rest, "## 已完成\n")
	history, pending, ok = strings.Cut(rest, "## 待执行\n")
	if !ok {
		t.Fatalf("remaining section %q", body)
	}
	return history, pending
}

func parseContinuableBody(t *testing.T, body string) (string, []string, []string) {
	t.Helper()
	line, rest, ok := strings.Cut(body, "\n")
	if !ok || !strings.HasPrefix(line, "step: ") {
		t.Fatalf("step line %q", body)
	}
	const doneLabel = "## 已完成\n"
	const pendLabel = "## 待执行\n"
	if !strings.HasPrefix(rest, doneLabel) {
		t.Fatalf("completed label %q", body)
	}
	rest = rest[len(doneLabel):]
	var completed []string
	for strings.HasPrefix(rest, "<<<step ") {
		var payload string
		payload, rest = takeContinuableStep(t, rest)
		completed = append(completed, payload)
	}
	if !strings.HasPrefix(rest, pendLabel) {
		t.Fatalf("remaining label %q", rest)
	}
	rest = rest[len(pendLabel):]
	var remaining []string
	for strings.HasPrefix(rest, "<<<step ") {
		var payload string
		payload, rest = takeContinuableStep(t, rest)
		remaining = append(remaining, payload)
	}
	if strings.TrimSpace(rest) != "" {
		t.Fatalf("trailing %q", rest)
	}
	return line, completed, remaining
}

func takeContinuableStep(t *testing.T, rest string) (string, string) {
	t.Helper()
	header, after, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(header, "<<<step ") || !strings.HasSuffix(header, ">>>") {
		t.Fatalf("boundary %q", rest)
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(header, "<<<step "), ">>>")
	indexText, lengthText, ok := strings.Cut(mid, " ")
	if !ok {
		t.Fatalf("boundary %q", header)
	}
	if _, err := strconv.Atoi(indexText); err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(lengthText)
	if err != nil || n < 0 || n > len(after) {
		t.Fatalf("boundary %q", header)
	}
	payload := after[:n]
	rest = after[n:]
	if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	return payload, rest
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestImportDistinctStableIDs(t *testing.T) {
	build := func(t *testing.T) (string, string) {
		t.Helper()
		v1 := t.TempDir()
		writeV1(t, v1, "tasks/queued.json", map[string]any{
			"id": "q1", "title": "Queue the ferry", "status": "queued", "project": "lumen",
		})
		writeV1(t, v1, "tasks/han.json", map[string]any{
			"title": "Zhong card", "status": "queued", "project": "中文",
		})
		writeV1(t, v1, "tasks/xiang.json", map[string]any{
			"title": "Xiang card", "status": "queued", "project": "项目",
		})
		writeV1(t, v1, "tasks/a.b.json", map[string]any{
			"title": "Dot file", "status": "done", "project": "lumen",
		})
		writeV1(t, v1, "tasks/a-b.json", map[string]any{
			"title": "Dash file", "status": "done", "project": "lumen",
		})
		return v1, snapshotTree(t, v1)
	}
	importOnce := func(t *testing.T, v1, v2 string) map[string]model.Card {
		t.Helper()
		if _, err := Import(context.Background(), v2, v1, false); err != nil {
			t.Fatal(err)
		}
		db := openDB(t, v2)
		bySource := map[string]model.Card{}
		for _, card := range cardsOf(t, db) {
			bySource[card.Legacy.SourcePath] = card
		}
		return bySource
	}
	v1, before := build(t)
	v2 := t.TempDir()
	bySource := importOnce(t, v1, v2)
	db := openDB(t, v2)
	wantSources := []string{"tasks/queued.json", "tasks/han.json", "tasks/xiang.json", "tasks/a.b.json", "tasks/a-b.json"}
	if len(bySource) != len(wantSources) {
		t.Fatalf("cards %d %#v", len(bySource), bySource)
	}
	for _, rel := range wantSources {
		if _, ok := bySource[rel]; !ok {
			t.Fatalf("missing %s", rel)
		}
	}
	queued := bySource["tasks/queued.json"]
	if queued.ID != "v1-q1" || queued.Objective != "v1-import-lumen" {
		t.Fatalf("explicit id %+v", queued)
	}
	han, xiang := bySource["tasks/han.json"], bySource["tasks/xiang.json"]
	if han.ID == xiang.ID || han.Objective == xiang.Objective {
		t.Fatalf("collapsed cards %s %s objectives %s %s", han.ID, xiang.ID, han.Objective, xiang.Objective)
	}
	hanObj, err := db.GetObjective(context.Background(), han.Objective)
	if err != nil {
		t.Fatal(err)
	}
	xiangObj, err := db.GetObjective(context.Background(), xiang.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if hanObj.Project == xiangObj.Project || hanObj.ID == xiangObj.ID {
		t.Fatalf("projects share an id %s %s", hanObj.Project, xiangObj.Project)
	}
	dot, dash := bySource["tasks/a.b.json"], bySource["tasks/a-b.json"]
	if dot.ID == dash.ID || dot.ID == "" || dash.ID == "" {
		t.Fatalf("filename ids %q %q", dot.ID, dash.ID)
	}
	cardsBefore := cardsOf(t, db)
	objsBefore := objectivesOf(t, db)
	if _, err := Import(context.Background(), v2, v1, false); err != nil {
		t.Fatal(err)
	}
	if again := cardsOf(t, db); len(again) != len(cardsBefore) {
		t.Fatalf("second import cards %d want %d", len(again), len(cardsBefore))
	}
	if again := objectivesOf(t, db); len(again) != len(objsBefore) {
		t.Fatalf("second import objectives %d want %d", len(again), len(objsBefore))
	}
	other := t.TempDir()
	again := importOnce(t, v1, other)
	for _, rel := range wantSources {
		if again[rel].ID != bySource[rel].ID || again[rel].Objective != bySource[rel].Objective {
			t.Fatalf("%s id %s/%s objective %s/%s", rel, again[rel].ID, bySource[rel].ID, again[rel].Objective, bySource[rel].Objective)
		}
	}
	if after := snapshotTree(t, v1); after != before {
		t.Fatal("import changed v1 bytes")
	}
}

func TestImportSourceLocatesFile(t *testing.T) {
	v1 := t.TempDir()
	writeV1(t, v1, "tasks/queued.json", map[string]any{
		"id": "q1", "title": "Queue the ferry", "status": "queued", "project": "lumen",
	})
	brokenPath := filepath.Join(v1, "tasks", "broken.json")
	if err := os.WriteFile(brokenPath, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, v1)
	v2 := t.TempDir()
	if _, err := Import(context.Background(), v2, v1, false); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, v2)
	bySource := map[string]model.Card{}
	for _, card := range cardsOf(t, db) {
		if card.Legacy.SourcePath == "" || strings.Contains(card.Legacy.SourcePath, v1) {
			t.Fatalf("relative source changed %+v", card.Legacy)
		}
		bySource[card.Legacy.SourcePath] = card
	}
	for _, rel := range []string{"tasks/queued.json", "tasks/broken.json"} {
		card, ok := bySource[rel]
		if !ok {
			t.Fatalf("missing %s", rel)
		}
		if card.Legacy.SourcePath != rel {
			t.Fatalf("source %q", card.Legacy.SourcePath)
		}
		home, path := readLegacySource(t, db, card.ID)
		if home != v1 || !strings.HasPrefix(path, home+string(filepath.Separator)) {
			t.Fatalf("location home %q path %q v1 %q", home, path, v1)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(v1, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) || len(got) == 0 && rel == "tasks/queued.json" {
			t.Fatalf("%s bytes %q", rel, got)
		}
	}
	if _, err := Import(context.Background(), v2, v1, false); err != nil {
		t.Fatal(err)
	}
	if n := legacySourceCount(t, db, bySource["tasks/queued.json"].ID); n != 1 {
		t.Fatalf("source events %d", n)
	}
	if after := snapshotTree(t, v1); after != before {
		t.Fatal("import changed v1 bytes")
	}
}

func TestImportSkipsWorkflowSidecars(t *testing.T) {
	const (
		hex32 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		hex20 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	v1 := t.TempDir()
	writeV1(t, v1, "workflows/real.json", map[string]any{
		"id": "w1", "title": "Live workflow", "status": "running", "project": "lumen",
	})
	writeV1(t, v1, "workflows/mod.progress.json", map[string]any{
		"id": "prog1", "title": "Progress sidecar", "status": "queued", "project": "lumen",
	})
	writeV1(t, v1, "tasks/keep.progress.json", map[string]any{
		"id": "taskprog", "title": "Task progress name", "status": "queued", "project": "lumen",
	})
	writeV1(t, v1, "archive/old.progress.json", map[string]any{
		"id": "archprog", "title": "Archive progress name", "status": "done", "project": "lumen",
	})
	writeV1(t, v1, "workflows/claimed.design-receipt.json", map[string]any{
		"id": "claim1", "title": "Not a sidecar", "status": "queued", "project": "lumen",
		"role": "design", "read_only": true,
	})
	receipt := []byte(`{"role":"design","read_only":true,"status":"completed","runner":"grok","actual_runner":"grok","identity":"fixture-identity","session_id":"fixture-session","result_path":"/tmp/fixture-result","digest":"` + hex32 + `","input_identity":"fixture-input"}`)
	preimage := []byte(`{"meaning":"fixture preimage","head":"` + hex20 + `","hashes":{"internal/lamp.go":"` + hex32 + `"}}`)
	writeRaw(t, v1, "workflows/ok.design-receipt.json", receipt)
	writeRaw(t, v1, "workflows/keep.retained-preimage-hashes.json", preimage)
	writeRaw(t, v1, "workflows/broken.json", []byte("{"))
	writeRaw(t, v1, "workflows/bad.design-receipt.json", []byte("{"))
	writeRaw(t, v1, "workflows/notes.txt", []byte("not a record"))
	writeRaw(t, v1, "tasks/readme.txt", []byte("not a record"))
	writeV1(t, v1, "tasks/sub/hidden.json", map[string]any{
		"id": "hidden", "title": "Nested", "status": "queued", "project": "lumen",
	})
	if err := os.MkdirAll(filepath.Join(v1, "workflows", "dir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, v1)
	v2 := t.TempDir()
	report, err := Import(context.Background(), v2, v1, false)
	if err != nil {
		t.Fatal(err)
	}
	db := openDB(t, v2)
	bySource := map[string]model.Card{}
	for _, card := range cardsOf(t, db) {
		bySource[card.Legacy.SourcePath] = card
	}
	imported := []string{
		"workflows/real.json",
		"tasks/keep.progress.json",
		"archive/old.progress.json",
		"workflows/claimed.design-receipt.json",
		"workflows/broken.json",
		"workflows/bad.design-receipt.json",
	}
	skipped := []string{
		"workflows/mod.progress.json",
		"workflows/ok.design-receipt.json",
		"workflows/keep.retained-preimage-hashes.json",
		"workflows/notes.txt",
		"tasks/readme.txt",
		"tasks/sub/hidden.json",
	}
	if len(bySource) != len(imported) {
		t.Fatalf("cards %d %#v", len(bySource), sourceKeys(bySource))
	}
	for _, rel := range imported {
		if _, ok := bySource[rel]; !ok {
			t.Fatalf("missing record %s", rel)
		}
	}
	for _, rel := range skipped {
		if _, ok := bySource[rel]; ok {
			t.Fatalf("imported sidecar %s", rel)
		}
	}
	if _, ok := bySource["workflows/dir.json"]; ok {
		t.Fatal("imported a directory")
	}
	if bySource["workflows/broken.json"].Legacy.Status != "unparsed" || bySource["workflows/bad.design-receipt.json"].Legacy.Status != "unparsed" {
		t.Fatalf("broken %+v %+v", bySource["workflows/broken.json"].Legacy, bySource["workflows/bad.design-receipt.json"].Legacy)
	}
	if bySource["workflows/broken.json"].Status == model.CardReady || bySource["workflows/real.json"].ID != "v1-w1" {
		t.Fatalf("placement broken %+v real %s", bySource["workflows/broken.json"], bySource["workflows/real.json"].ID)
	}
	if report.Counts["v1:unparsed"] != 2 || report.Counts["v1:running"] != 1 || report.Counts["v1:queued"] != 2 || report.Counts["v1:done"] != 1 {
		t.Fatalf("counts %#v", report.Counts)
	}
	for _, rel := range skipped {
		for _, line := range report.Unparsed {
			if strings.Contains(line, rel) {
				t.Fatalf("skipped file counted %s in %q", rel, line)
			}
		}
	}
	if _, err := Import(context.Background(), v2, v1, false); err != nil {
		t.Fatal(err)
	}
	if again := cardsOf(t, db); len(again) != len(imported) {
		t.Fatalf("second import cards %d", len(again))
	}
	if after := snapshotTree(t, v1); after != before {
		t.Fatal("import changed v1 bytes")
	}
}

func TestImportRefusesSourceDatabase(t *testing.T) {
	writeOne := func(t *testing.T, dir string) {
		t.Helper()
		writeV1(t, dir, "tasks/queued.json", map[string]any{
			"id": "q1", "title": "Queue the ferry", "status": "queued", "project": "lumen",
		})
	}
	cases := []struct {
		name  string
		same  bool
		setup func(t *testing.T) (v2, v1, watch string)
	}{
		{
			name: "same path",
			same: true,
			setup: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				writeOne(t, dir)
				return dir, dir, dir
			},
		},
		{
			name: "dot alias",
			same: true,
			setup: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				writeOne(t, dir)
				return filepath.Join(dir, "."), dir, dir
			},
		},
		{
			name: "symlink v2",
			same: true,
			setup: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				writeOne(t, dir)
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				return link, dir, dir
			},
		},
		{
			name: "symlink v1",
			same: true,
			setup: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				writeOne(t, dir)
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				return dir, link, dir
			},
		},
		{
			name: "distinct directories",
			setup: func(t *testing.T) (string, string, string) {
				v1 := t.TempDir()
				writeOne(t, v1)
				return t.TempDir(), v1, v1
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v2, v1, watch := tc.setup(t)
			before := snapshotTree(t, watch)
			_, err := Import(context.Background(), v2, v1, false)
			if tc.same {
				if err == nil {
					t.Fatal("same directory imported")
				}
				if _, statErr := os.Stat(filepath.Join(watch, "cardex.db")); !os.IsNotExist(statErr) {
					t.Fatalf("database in source: %v", statErr)
				}
			} else if err != nil {
				t.Fatal(err)
			} else {
				if _, statErr := os.Stat(filepath.Join(v2, "cardex.db")); err != nil {
					t.Fatal(statErr)
				}
				if _, statErr := os.Stat(filepath.Join(v1, "cardex.db")); !os.IsNotExist(statErr) {
					t.Fatalf("database in v1: %v", statErr)
				}
				db := openDB(t, v2)
				if cards := cardsOf(t, db); len(cards) != 1 || cards[0].ID != "v1-q1" {
					t.Fatalf("cards %+v", cards)
				}
			}
			if after := snapshotTree(t, watch); after != before {
				t.Fatal("source directory changed")
			}
		})
	}
}

func readLegacySource(t *testing.T, db *store.Store, id string) (string, string) {
	t.Helper()
	var home, path string
	var n int
	for _, ev := range eventsOf(t, db, id) {
		if ev.Type != legacySourceEvent {
			continue
		}
		n++
		var body struct {
			Home string `json:"home"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(ev.Data, &body); err != nil {
			t.Fatal(err)
		}
		home, path = body.Home, body.Path
	}
	if n != 1 || home == "" || path == "" {
		t.Fatalf("source events %d home %q path %q", n, home, path)
	}
	return home, path
}

func legacySourceCount(t *testing.T, db *store.Store, id string) int {
	t.Helper()
	n := 0
	for _, ev := range eventsOf(t, db, id) {
		if ev.Type == legacySourceEvent {
			n++
		}
	}
	return n
}

func eventsOf(t *testing.T, db *store.Store, id string) []model.Event {
	t.Helper()
	evs, err := db.ListEvents(context.Background(), "card:"+id)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func writeRaw(t *testing.T, root, rel string, body []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sourceKeys(cards map[string]model.Card) []string {
	out := make([]string, 0, len(cards))
	for rel := range cards {
		out = append(out, rel)
	}
	return out
}
