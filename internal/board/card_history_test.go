package board

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestCardJSONHistoryAndEvidence(t *testing.T) {
	t.Chdir(t.TempDir())
	db, home := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: home}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o", Project: "p", Title: "history", Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-hist", Objective: "o", Title: "history card", Kind: model.KindDev, Status: model.CardReady,
	}))
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		must(t, db.CreateAttempt(ctx, &model.Attempt{ID: id, Card: "c-hist"}))
	}
	base := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	rows := []model.Event{
		{ID: "e1", TS: base, Entity: "card:c-hist", Type: "review_verdict", Data: json.RawMessage(`{"opinion":"one","verdict":"rework"}`)},
		{ID: "e2", TS: base.Add(time.Second), Entity: "card:c-hist", Type: "card_noise", Data: json.RawMessage(`{"noise":"skip-me"}`)},
		{ID: "e3", TS: base.Add(2 * time.Second), Entity: "card:c-hist", Type: "rework", Data: json.RawMessage(`{"source":"review"}`)},
		{ID: "e4", TS: base.Add(3 * time.Second), Entity: "card:c-hist", Type: "review_verdict", Data: json.RawMessage(`{"opinion":"two","verdict":"pass"}`)},
		{ID: "e5", TS: base.Add(4 * time.Second), Entity: "attempt:a1", Type: "evidence_write_failed", Data: json.RawMessage(`{"error":"nope"}`)},
	}
	for i := range rows {
		must(t, db.AppendEvent(ctx, &rows[i]))
	}
	writeEvidenceFile(t, home, "a1", "output.log", "output-a1")
	writeEvidenceFile(t, home, "a1", "acceptance.log", "accept-a1")
	writeEvidenceFile(t, home, "a2", "result.md", "result-a2")
	writeEvidenceFile(t, home, "a4", "acceptance.log", "accept-a4")
	if err := os.MkdirAll(filepath.Join(home, "attempts", "a4", "output.log"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := do(t, http.MethodGet, "/api/cards/c-hist")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ID      string `json:"id"`
		History []struct {
			TS   string          `json:"ts"`
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		} `json:"history"`
		Attempts []struct {
			ID       string   `json:"id"`
			Evidence []string `json:"evidence"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "c-hist" || len(body.History) != 3 {
		t.Fatalf("history %+v", body.History)
	}
	wantType := []string{"review_verdict", "rework", "review_verdict"}
	wantTS := []time.Time{base, base.Add(2 * time.Second), base.Add(3 * time.Second)}
	for i := range wantType {
		if body.History[i].Type != wantType[i] || body.History[i].TS != wantTS[i].UTC().Format(time.RFC3339Nano) {
			t.Fatalf("history[%d] %+v", i, body.History[i])
		}
		if string(body.History[i].Data) == "" || string(body.History[i].Data) == "null" {
			t.Fatalf("empty data %s", body.History[i].Data)
		}
	}
	if len(body.Attempts) != 4 {
		t.Fatalf("attempts %+v", body.Attempts)
	}
	byID := map[string][]string{}
	for _, attempt := range body.Attempts {
		if attempt.Evidence == nil {
			t.Fatalf("null evidence on %s", attempt.ID)
		}
		byID[attempt.ID] = attempt.Evidence
	}
	assertEvidence(t, byID["a1"], "attempts/a1/output.log", "attempts/a1/acceptance.log")
	assertEvidence(t, byID["a2"], "attempts/a2/result.md")
	assertEvidence(t, byID["a3"])
	assertEvidence(t, byID["a4"], "attempts/a4/acceptance.log")
}

func writeEvidenceFile(t *testing.T, home, id, name, body string) {
	t.Helper()
	dir := filepath.Join(home, "attempts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertEvidence(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("evidence %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("evidence %v want %v", got, want)
		}
	}
}
