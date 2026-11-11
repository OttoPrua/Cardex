package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/board"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestCardShowJSONMatchesBoard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: home}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "o", Project: "p", Title: "show", Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}); err != nil {
		t.Fatal(err)
	}
	const id = "c-show"
	const title = "Show title"
	card := model.Card{
		ID: id, Objective: "o", Title: title, Kind: model.KindDev, Tier: model.TierT2,
		Status: model.CardReady, Body: "body",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := saveCardOrigin(ctx, db, id, "file:origin-src"); err != nil {
		t.Fatal(err)
	}
	for _, attemptID := range []string{"a1", "a2", "a3", "a4"} {
		if err := db.CreateAttempt(ctx, &model.Attempt{ID: attemptID, Card: id}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 11, 8, 1, 2, 3, 4, time.UTC)
	rows := []model.Event{
		{ID: "e1", TS: base, Entity: "card:" + id, Type: "review_verdict", Data: json.RawMessage(`{"opinion":"one","verdict":"rework"}`)},
		{ID: "e2", TS: base.Add(time.Second), Entity: "card:" + id, Type: "card_noise", Data: json.RawMessage(`{"noise":"skip-me"}`)},
		{ID: "e3", TS: base.Add(2 * time.Second), Entity: "card:" + id, Type: "rework", Data: json.RawMessage(`{"source":"review"}`)},
		{ID: "e-other", TS: base.Add(2 * time.Second), Entity: "card:other", Type: "review_verdict", Data: json.RawMessage(`{"opinion":"other"}`)},
		{ID: "e4", TS: base.Add(3 * time.Second), Entity: "card:" + id, Type: "review_verdict", Data: json.RawMessage(`{"opinion":"two","verdict":"pass"}`)},
	}
	for i := range rows {
		if err := db.AppendEvent(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	writeShowFile(t, home, "a1", "output.log", "output-a1")
	writeShowFile(t, home, "a1", "acceptance.log", "accept-a1")
	writeShowFile(t, home, "a1", "notes.txt", "not-evidence")
	writeShowFile(t, home, "a2", "result.md", "result-a2")
	writeShowFile(t, home, "a4", "acceptance.log", "accept-a4")
	if err := os.MkdirAll(filepath.Join(home, "attempts", "a4", "output.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var help bytes.Buffer
	if code := Main([]string{"--help"}, &help, io.Discard); code != 0 || strings.Contains(help.String(), "history") {
		t.Fatalf("help %d\n%s", code, help.String())
	}

	json1 := runCardShow(t, home, id, true)
	json2 := runCardShow(t, home, id, true)
	if json1 != json2 {
		t.Fatalf("json runs differ\n%s\n%s", json1, json2)
	}
	var cli showPayload
	if err := json.Unmarshal([]byte(json1), &cli); err != nil {
		t.Fatal(err)
	}
	if cli.ID != id || cli.Title != title || cli.Kind != string(model.KindDev) || cli.Origin != "file:origin-src" {
		t.Fatalf("card %+v", cli)
	}
	if len(cli.History) != 3 {
		t.Fatalf("history %+v", cli.History)
	}
	wantType := []string{"review_verdict", "rework", "review_verdict"}
	wantTS := []time.Time{base, base.Add(2 * time.Second), base.Add(3 * time.Second)}
	for i := range wantType {
		if cli.History[i].Type != wantType[i] || cli.History[i].TS != wantTS[i].UTC().Format(time.RFC3339Nano) {
			t.Fatalf("history[%d] %+v", i, cli.History[i])
		}
	}
	if len(cli.Attempts) != 4 {
		t.Fatalf("attempts %+v", cli.Attempts)
	}
	evidence := map[string][]string{}
	for _, attempt := range cli.Attempts {
		if attempt.Evidence == nil {
			t.Fatalf("null evidence on %s", attempt.ID)
		}
		evidence[attempt.ID] = attempt.Evidence
	}
	assertShowEvidence(t, evidence["a1"], "attempts/a1/output.log", "attempts/a1/acceptance.log")
	assertShowEvidence(t, evidence["a2"], "attempts/a2/result.md")
	assertShowEvidence(t, evidence["a3"])
	assertShowEvidence(t, evidence["a4"], "attempts/a4/acceptance.log")

	req := httptest.NewRequest(http.MethodGet, "/api/cards/"+id, nil)
	rec := httptest.NewRecorder()
	board.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("board %d %s", rec.Code, rec.Body.String())
	}
	var boardBody showPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &boardBody); err != nil {
		t.Fatal(err)
	}
	if len(boardBody.History) != len(cli.History) {
		t.Fatalf("board history %+v cli %+v", boardBody.History, cli.History)
	}
	for i := range cli.History {
		if boardBody.History[i].TS != cli.History[i].TS || boardBody.History[i].Type != cli.History[i].Type || compactJSON(t, boardBody.History[i].Data) != compactJSON(t, cli.History[i].Data) {
			t.Fatalf("history[%d] board %+v cli %+v", i, boardBody.History[i], cli.History[i])
		}
	}
	boardEvidence := map[string][]string{}
	for _, attempt := range boardBody.Attempts {
		boardEvidence[attempt.ID] = attempt.Evidence
	}
	for attemptID, want := range evidence {
		assertShowEvidence(t, boardEvidence[attemptID], want...)
	}

	var text, errb bytes.Buffer
	if code := Main([]string{"--home", home, "card", "show", id}, &text, &errb); code != 0 {
		t.Fatalf("text %d %s", code, errb.String())
	}
	got := text.String()
	if got != id+"\t"+title+"\tfile:origin-src\n" || strings.Contains(got, "history") || strings.Count(got, "\n") != 1 {
		t.Fatalf("text %q", got)
	}
}

type showPayload struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Origin  string `json:"origin"`
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

func runCardShow(t *testing.T, home, id string, asJSON bool) string {
	t.Helper()
	var out, errb bytes.Buffer
	args := []string{"--home", home, "card", "show", id}
	if asJSON {
		args = append(args, "--json")
	}
	if code := Main(args, &out, &errb); code != 0 {
		t.Fatalf("card show %d %s", code, errb.String())
	}
	return out.String()
}

func writeShowFile(t *testing.T, home, id, name, body string) {
	t.Helper()
	dir := filepath.Join(home, "attempts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func assertShowEvidence(t *testing.T, got []string, want ...string) {
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
