package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestCardShowSummaryOnly(t *testing.T) {
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
	const id = "c-sum"
	card := model.Card{
		ID: id, Objective: "o", Title: "Summary title", Kind: model.KindDev, Tier: model.TierT2,
		Status: model.CardDone, Body: "body",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	early := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	later := early.Add(time.Hour)
	for _, attempt := range []model.Attempt{
		{ID: "a1", Card: id, Index: 1, EndedAt: later},
		{ID: "a2", Card: id, Index: 3, EndedAt: early},
		{ID: "a4", Card: id, Index: 9, EndedAt: later},
	} {
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
	}
	writeSummaryFile(t, home, "a1", harvest.Summary{AttemptID: "a1", Runner: "old"})
	want := harvest.Summary{
		AttemptID: "a2", Runner: "fake", Channel: "cha", Model: "model-a",
		Expected:   harvest.ExpectedRun{Channel: "cha", Model: "model-a", Tier: "T2"},
		Match:      true,
		BaseCommit: "abc", HeadCommit: "def",
		Files:      []harvest.FileChange{{Path: "a.go", Added: 3, Deleted: 1}},
		Acceptance: []harvest.AcceptanceResult{{Command: "go test ./...", Exit: 0, DurationMS: 15}},
	}
	writeSummaryFile(t, home, "a2", want)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var text, errb bytes.Buffer
	if code := Main([]string{"--home", home, "card", "show", id, "--summary"}, &text, &errb); code != 0 {
		t.Fatalf("text %d %s", code, errb.String())
	}
	got := text.String()
	if !strings.Contains(got, "attempt a2") || !strings.Contains(got, "runner fake") || !strings.Contains(got, "match true") || !strings.Contains(got, "a.go +3 -1") {
		t.Fatalf("summary text %q", got)
	}
	if strings.Contains(got, "Summary title") || strings.Contains(got, "history") || strings.Contains(got, "a1") {
		t.Fatalf("summary leaked card fields %q", got)
	}

	var jsonOut, jsonErr bytes.Buffer
	if code := Main([]string{"--home", home, "--json", "card", "show", id, "--summary"}, &jsonOut, &jsonErr); code != 0 {
		t.Fatalf("json %d %s", code, jsonErr.String())
	}
	if strings.Contains(jsonOut.String(), "history") || strings.Contains(jsonOut.String(), "Summary title") {
		t.Fatalf("json leaked %s", jsonOut.String())
	}
	var sum harvest.Summary
	if err := json.Unmarshal(jsonOut.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.AttemptID != "a2" || sum.Runner != "fake" || !sum.Match || len(sum.Acceptance) != 1 || sum.Acceptance[0].DurationMS != 15 {
		t.Fatalf("json %+v", sum)
	}

	plain := runCardShow(t, home, id, false)
	if plain != id+"\tSummary title\n" {
		t.Fatalf("default show %q", plain)
	}
	raw := runCardShow(t, home, id, true)
	if !strings.Contains(raw, `"history"`) || !strings.Contains(raw, "Summary title") || strings.Contains(raw, `"attempt_id"`) {
		t.Fatalf("default json %s", raw)
	}

	none := model.Card{
		ID: "c-none", Objective: "o", Title: "empty", Kind: model.KindDev, Tier: model.TierT1, Status: model.CardReady,
	}
	db, err = store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &none); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var noneOut, noneErr bytes.Buffer
	if code := Main([]string{"--home", home, "card", "show", "c-none", "--summary"}, &noneOut, &noneErr); code != 1 || !strings.Contains(noneErr.String(), "no summary for c-none") {
		t.Fatalf("missing %d %q %q", code, noneOut.String(), noneErr.String())
	}
}

func writeSummaryFile(t *testing.T, home, id string, sum harvest.Summary) {
	t.Helper()
	dir := filepath.Join(home, "attempts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
