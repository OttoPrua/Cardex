package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/model"
)

func TestOpenQuestionRoundTripLegacyAndQuery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	full := model.OpenQuestion{
		ID: "oq-full", Text: "need a fact", Type: model.OQFact, Status: model.OQOpen,
		Asks: 2, Answer: "found", Evidence: "main.go:3", Confidence: "高",
		Commit: "deadbeef", CitedPaths: []string{"main.go"},
	}
	other := model.OpenQuestion{
		ID: "oq-done", Text: "settled", Type: model.OQDecision, Status: model.OQAnswered,
		CitedPaths: []string{},
	}
	node := model.DesignNode{
		ID: "n1", Objective: "o1", Layer: model.LayerModule, Status: model.NodeDrafting,
		DocPath: "docs/design/o1/n1.md", OpenQuestions: model.OpenQuestionList{full, other},
	}
	if err := db.CreateNode(ctx, &node); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetNode(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.OpenQuestions) != 2 || got.OpenQuestions[0].ID != "oq-full" || got.OpenQuestions[0].Confidence != "高" || got.OpenQuestions[0].Commit != "deadbeef" {
		t.Fatalf("round trip %+v", got.OpenQuestions)
	}
	if got.OpenQuestions[0].CitedPaths[0] != "main.go" {
		t.Fatalf("cited paths %+v", got.OpenQuestions[0].CitedPaths)
	}

	openOnly, err := db.ListOpenQuestions(ctx, "n1", model.OQOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(openOnly) != 1 || openOnly[0].ID != "oq-full" || openOnly[0].Status != model.OQOpen {
		t.Fatalf("node+status query %+v", openOnly)
	}
	answered, err := db.ListOpenQuestions(ctx, "n1", model.OQAnswered)
	if err != nil || len(answered) != 1 || answered[0].ID != "oq-done" {
		t.Fatalf("answered query %+v err %v", answered, err)
	}

	second := model.DesignNode{
		ID: "n2", Layer: model.LayerDetail, Status: model.NodeDrafting,
		OpenQuestions: model.OpenQuestionList{{
			ID: "oq-other", Text: "elsewhere", Type: model.OQFact, Status: model.OQOpen, CitedPaths: []string{},
		}},
	}
	if err := db.CreateNode(ctx, &second); err != nil {
		t.Fatal(err)
	}
	again, err := db.ListOpenQuestions(ctx, "n1", model.OQOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].ID != "oq-full" {
		t.Fatalf("query leaked other node %+v", again)
	}

	if _, err := db.db.ExecContext(ctx, `UPDATE design_nodes SET open_questions=? WHERE id=?`, `["q"]`, "n1"); err != nil {
		t.Fatal(err)
	}
	legacy, err := db.GetNode(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.OpenQuestions) != 1 || legacy.OpenQuestions[0].Text != "q" || legacy.OpenQuestions[0].Type != model.OQDecision || legacy.OpenQuestions[0].Status != model.OQOpen {
		t.Fatalf("legacy load %+v", legacy.OpenQuestions)
	}

	if _, err := db.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	ver, err := reopened.SchemaVersion(ctx)
	if err != nil || ver < 2 {
		t.Fatalf("schema %d err %v", ver, err)
	}
	migrated, err := reopened.GetNode(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated.OpenQuestions) != 1 || migrated.OpenQuestions[0].Text != "q" || migrated.OpenQuestions[0].Type != model.OQDecision || migrated.OpenQuestions[0].Status != model.OQOpen {
		t.Fatalf("migrated %+v", migrated.OpenQuestions)
	}
	var raw string
	if err := reopened.db.QueryRowContext(ctx, `SELECT open_questions FROM design_nodes WHERE id=?`, "n1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, `["q"]`) || !strings.Contains(raw, `"decision"`) || !strings.Contains(raw, `"open"`) {
		t.Fatalf("stored form %s", raw)
	}
	var decoded model.OpenQuestionList
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0].Text != "q" {
		t.Fatalf("rewritten json %+v", decoded)
	}

	blank := model.DesignNode{
		ID: "n3", Layer: model.LayerModule, Status: model.NodeDrafting,
		OpenQuestions: model.OpenQuestionList{{Text: "untagged"}},
	}
	if err := reopened.CreateNode(ctx, &blank); err != nil {
		t.Fatal(err)
	}
	stored, err := reopened.GetNode(ctx, "n3")
	if err != nil || stored.OpenQuestions[0].Type != model.OQDecision || stored.OpenQuestions[0].Status != model.OQOpen {
		t.Fatalf("defaulted %+v err %v", stored.OpenQuestions, err)
	}
	bad := model.DesignNode{
		ID: "n4", Layer: model.LayerModule, Status: model.NodeDrafting,
		OpenQuestions: model.OpenQuestionList{{Text: "nope", Type: "held"}},
	}
	if err := reopened.CreateNode(ctx, &bad); err == nil {
		t.Fatal("invalid open question type accepted")
	}
}
