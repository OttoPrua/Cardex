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

	if _, err := db.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>=2`); err != nil {
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
	if err := reopened.CreateNode(ctx, &bad); err != nil {
		t.Fatal(err)
	}
	held, err := reopened.GetNode(ctx, "n4")
	if err != nil || held.OpenQuestions[0].Text != "nope" || held.OpenQuestions[0].Type != model.OQDecision || held.OpenQuestions[0].Status != model.OQOpen {
		t.Fatalf("held type %+v err %v", held.OpenQuestions, err)
	}
}

func TestOpenQuestionIrregularEnumsSoft(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	type row struct {
		name       string
		text       string
		typ        model.OpenQuestionType
		status     model.OpenQuestionStatus
		legacy     bool
		wantType   model.OpenQuestionType
		wantStatus model.OpenQuestionStatus
	}
	rows := []row{
		{name: "FACT OPEN", text: "keep fact", typ: "FACT", status: "OPEN", wantType: model.OQFact, wantStatus: model.OQOpen},
		{name: "whitespace", text: "padded", typ: " fact ", status: " open ", wantType: model.OQFact, wantStatus: model.OQOpen},
		{name: "held type", text: "nope", typ: "held", status: "open", wantType: model.OQDecision, wantStatus: model.OQOpen},
		{name: "unknown status", text: "stat", typ: "fact", status: "held", wantType: model.OQFact, wantStatus: model.OQOpen},
		{name: "unknown both", text: "both", typ: "nope", status: "zzz", wantType: model.OQDecision, wantStatus: model.OQOpen},
		{name: "empty", text: "untagged", wantType: model.OQDecision, wantStatus: model.OQOpen},
		{name: "legacy string", text: "plain", legacy: true, wantType: model.OQDecision, wantStatus: model.OQOpen},
	}
	for _, typ := range model.AllOpenQuestionTypes() {
		rows = append(rows, row{
			name: "type " + string(typ), text: "t-" + string(typ),
			typ: model.OpenQuestionType(" " + strings.ToUpper(string(typ)) + " "), status: " OPEN ",
			wantType: typ, wantStatus: model.OQOpen,
		})
	}
	for _, st := range model.AllOpenQuestionStatuses() {
		rows = append(rows, row{
			name: "status " + string(st), text: "s-" + string(st),
			typ: " FACT ", status: model.OpenQuestionStatus(" " + strings.ToUpper(string(st)) + " "),
			wantType: model.OQFact, wantStatus: st,
		})
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			raw := irregularQuestionJSON(t, r.text, r.typ, r.status, r.legacy)
			parsed, err := model.ParseOpenQuestions(raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed) != 1 || parsed[0].Text != r.text || parsed[0].Type != r.wantType || parsed[0].Status != r.wantStatus {
				t.Fatalf("parse %+v", parsed)
			}

			id := "n-soft-" + strings.ReplaceAll(r.name, " ", "-")
			node := model.DesignNode{
				ID: id, Layer: model.LayerModule, Status: model.NodeDrafting,
				OpenQuestions: model.OpenQuestionList{{Text: r.text, Type: r.typ, Status: r.status}},
			}
			if r.legacy {
				node.OpenQuestions = model.OpenQuestionList{{Text: r.text}}
			}
			if err := db.CreateNode(ctx, &node); err != nil {
				t.Fatal(err)
			}
			got, err := db.GetNode(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			assertQuestion(t, got.OpenQuestions, r.text, r.wantType, r.wantStatus)

			got.OpenQuestions = model.OpenQuestionList{{Text: r.text, Type: r.typ, Status: r.status}}
			if r.legacy {
				got.OpenQuestions = model.OpenQuestionList{{Text: r.text}}
			}
			if err := db.UpdateNode(ctx, &got); err != nil {
				t.Fatal(err)
			}
			updated, err := db.GetNode(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			assertQuestion(t, updated.OpenQuestions, r.text, r.wantType, r.wantStatus)

			if _, err := db.db.ExecContext(ctx, `UPDATE design_nodes SET open_questions=? WHERE id=?`, string(raw), id); err != nil {
				t.Fatal(err)
			}
			readBack, err := db.GetNode(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			assertQuestion(t, readBack.OpenQuestions, r.text, r.wantType, r.wantStatus)

			listed, err := db.ListOpenQuestions(ctx, id, r.status)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed) != 1 || listed[0].Text != r.text || listed[0].Status != r.wantStatus {
				t.Fatalf("list %+v", listed)
			}
		})
	}

	mixed := `["legacy text",{"text":"obj","type":"FACT","status":"OPEN"},{"text":"bad","type":"nope","status":"zzz"}]`
	mixNode := model.DesignNode{ID: "n-mix", Layer: model.LayerModule, Status: model.NodeDrafting}
	if err := db.CreateNode(ctx, &mixNode); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE design_nodes SET open_questions=? WHERE id=?`, mixed, "n-mix"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>=2`); err != nil {
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
	migrated, err := reopened.GetNode(ctx, "n-mix")
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated.OpenQuestions) != 3 {
		t.Fatalf("mixed migration %+v", migrated.OpenQuestions)
	}
	assertQuestion(t, migrated.OpenQuestions[:1], "legacy text", model.OQDecision, model.OQOpen)
	assertQuestion(t, migrated.OpenQuestions[1:2], "obj", model.OQFact, model.OQOpen)
	assertQuestion(t, migrated.OpenQuestions[2:], "bad", model.OQDecision, model.OQOpen)

	if err := reopened.CreateNode(ctx, &model.DesignNode{ID: "bad-layer", Layer: "nope", Status: model.NodeDrafting}); err == nil {
		t.Fatal("invalid layer accepted")
	}
	if err := reopened.CreateNode(ctx, &model.DesignNode{ID: "bad-status", Layer: model.LayerModule, Status: "held"}); err == nil {
		t.Fatal("invalid node status accepted")
	}
}

func irregularQuestionJSON(t *testing.T, text string, typ model.OpenQuestionType, status model.OpenQuestionStatus, legacy bool) []byte {
	t.Helper()
	if legacy {
		item, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		return append(append([]byte{'['}, item...), ']')
	}
	item, err := json.Marshal(map[string]string{"text": text, "type": string(typ), "status": string(status)})
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte{'['}, item...), ']')
}

func assertQuestion(t *testing.T, qs model.OpenQuestionList, text string, typ model.OpenQuestionType, status model.OpenQuestionStatus) {
	t.Helper()
	if len(qs) != 1 || qs[0].Text != text || qs[0].Type != typ || qs[0].Status != status {
		t.Fatalf("question %+v want text %q type %s status %s", qs, text, typ, status)
	}
}
