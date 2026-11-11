package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
)

func TestImportOwnerChoiceKeepsOtherPlacements(t *testing.T) {
	v1 := t.TempDir()
	writeV1(t, v1, "tasks/queued.json", map[string]any{
		"id": "q1", "title": "Queue the ferry", "status": "queued", "project": "lumen",
	})
	writeV1(t, v1, "workflows/choice.json", map[string]any{
		"id": "own1", "title": "Pick the name", "status": "owner_choice", "project": "lumen",
	})
	writeV1(t, v1, "workflows/writing.json", map[string]any{
		"id": "w1", "title": "Still writing", "status": "writing", "project": "lumen",
	})
	writeV1(t, v1, "workflows/integrated.json", map[string]any{
		"id": "i1", "title": "Already integrated", "status": "locally_integrated", "project": "lumen",
	})
	rawCfg, err := json.Marshal(map[string]any{
		"manager_wake": map[string]any{
			"subscriptions": []map[string]any{
				{
					"id": "example-manager", "provider": "hermes", "profile": "profile-a",
					"thread_id": "example-manager", "event_types": []string{"needs_owner"}, "enabled": true,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1, "config.json"), rawCfg, 0o644); err != nil {
		t.Fatal(err)
	}

	v2 := t.TempDir()
	report, err := Import(context.Background(), v2, v1, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["v1:queued"] != 1 || report.Counts["v1:owner_choice"] != 1 {
		t.Fatalf("counts %+v", report.Counts)
	}
	joined := strings.Join(report.Unparsed, "\n")
	if strings.Contains(joined, "owner_choice") {
		t.Fatalf("owner class dropped as unrecognized\n%s", joined)
	}
	for _, status := range []string{"writing", "locally_integrated"} {
		if !strings.Contains(joined, "unrecognized status "+status) {
			t.Fatalf("missing unrecognized %s\n%s", status, joined)
		}
	}

	db := openDB(t, v2)
	bySource := map[string]model.Card{}
	for _, card := range cardsOf(t, db) {
		bySource[card.Legacy.SourcePath] = card
	}
	queued := bySource["tasks/queued.json"]
	if queued.Status != model.CardReady || queued.Legacy.Status != "queued" {
		t.Fatalf("queued %+v", queued)
	}
	for _, rel := range []string{"workflows/writing.json", "workflows/integrated.json"} {
		card := bySource[rel]
		if card.Status != model.CardCanceled || card.Legacy.Status != "unparsed" {
			t.Fatalf("%s %+v", rel, card)
		}
		if card.Status == model.CardReady || card.Status == model.CardRunning {
			t.Fatalf("%s dispatchable", rel)
		}
	}
	choice := bySource["workflows/choice.json"]
	if choice.Status == model.CardReady || choice.Status == model.CardRunning || choice.Legacy.Status != "owner_choice" {
		t.Fatalf("choice %+v", choice)
	}

	nodes, err := db.ListNodes(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var found model.OpenQuestion
	for _, node := range nodes {
		for _, q := range node.OpenQuestions {
			if q.Text == "Pick the name" {
				found = q
			}
			if q.Type != model.OQOwner && q.Type != "" {
				t.Fatalf("unexpected question %+v", q)
			}
		}
	}
	if found.Type != model.OQOwner || found.Text != "Pick the name" {
		t.Fatalf("imported question %+v", found)
	}

	loaded, warnings, err := notify.LoadReceivers(v2)
	if err != nil || len(warnings) != 0 || len(loaded) != 1 {
		t.Fatalf("receivers %+v %v", warnings, err)
	}
	have := map[string]bool{}
	for _, kind := range loaded[0].Events {
		have[kind] = true
	}
	if have[string(model.QuestionDone)] {
		t.Fatalf("needs_owner included done: %+v", loaded[0].Events)
	}
	for _, kind := range model.AllQuestionKinds() {
		if kind == model.QuestionDone {
			continue
		}
		if !have[string(kind)] {
			t.Fatalf("needs_owner missing %s in %+v", kind, loaded[0].Events)
		}
	}
}
