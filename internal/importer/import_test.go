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
