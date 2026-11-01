package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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


