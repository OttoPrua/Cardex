package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/question"
)

func TestObjectiveResumeBriefCLI(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	db := openBriefDB(t, home)
	o := model.Objective{ID: "o-demo", Project: "p-demo", Title: "Synthetic project recovery", Mode: model.ModeAuto, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive}
	if err := db.CreateProject(ctx, &model.Project{ID: o.Project}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &o); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		n := model.DesignNode{ID: fmt.Sprintf("frozen-%d", i), Objective: o.ID, Layer: model.LayerDirection, DocPath: fmt.Sprintf("design/part-%d.md", i), Status: model.NodeFrozen}
		if err := db.CreateNode(ctx, &n); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateNode(ctx, &model.DesignNode{ID: "reopened", Objective: o.ID, Layer: model.LayerDirection, Status: model.NodeReopened}); err != nil {
		t.Fatal(err)
	}
	svc := question.New(db, nil)
	for i := 0; i < 12; i++ {
		q := model.Question{ID: fmt.Sprintf("q-%02d", i), Objective: o.ID, Scope: "frozen-0", ScopeKind: "node", Kind: model.QuestionCustom, Text: fmt.Sprintf("Synthetic decision %d\nDO NOT PRINT THIS LINE %s", i, strings.Repeat("x", 300)), Options: []string{"approve", "decline"}}
		if i == 0 {
			q.Answer = "approve"
		}
		if err := db.CreateQuestion(ctx, &q); err != nil {
			t.Fatal(err)
		}
	}
	// Answer out of ID order; repeated answer must not move the earlier decision.
	for _, id := range []string{"q-05", "q-03", "q-02", "q-01", "q-05"} {
		if _, err := svc.Answer(ctx, id, "approve"); err != nil {
			t.Fatal(err)
		}
	}
	code, out, stderr := runMain(t, "--home", home, "objective", "brief", o.ID)
	if code != 0 || stderr != "" {
		t.Fatalf("code %d: %s", code, stderr)
	}
	for _, want := range []string{"awaiting_approval", "frozen-0", "q-04", "q-01", "approve", "4 dated / 5 total"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	for _, unwanted := range []string{"DO NOT PRINT", "reopened", "frozen-5", "q-05"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in %s", unwanted, out)
		}
	}
	if strings.Count(out, "\n") > 15 {
		t.Fatalf("brief is too tall: %s", out)
	}
	code, jout, stderr := runMain(t, "--home", home, "objective", "brief", o.ID, "--json")
	var v objectiveResumeView
	if code != 0 || stderr != "" {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(jout), &v); err != nil {
		t.Fatal(err)
	}
	if v.ScopeTotal != 6 || v.PendingTotal != 7 || v.DecisionTotal != 5 || v.UndatedDecisions != 1 || len(v.Scope) != 3 || len(v.Pending) != 3 || len(v.Recent) != 3 || v.Recent[0].ID != "q-01" || v.Recent[2].ID != "q-03" {
		t.Fatalf("unexpected brief: %+v", v)
	}
	var legacyBytes int
	for _, args := range [][]string{
		{"objective", "show", o.ID, "--json"},
		{"node", "list", "--objective", o.ID, "--json"},
		{"question", "list", "--objective", o.ID, "--json"},
	} {
		code, legacy, errText := runMain(t, append([]string{"--home", home}, args...)...)
		if code != 0 || errText != "" {
			t.Fatalf("legacy CLI %v returned %d: %s", args, code, errText)
		}
		legacyBytes += len(legacy)
	}
	t.Logf("synthetic recovery: existing 3 CLI outputs %d bytes; objective brief 1 output %d bytes (text), %d bytes (JSON)", legacyBytes, len(out), len(jout))
}
