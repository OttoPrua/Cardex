package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestBlockersCurrentCardsAndReasons(t *testing.T) {
	home := t.TempDir()
	db := openBriefDB(t, home)
	t.Cleanup(func() { db.Close() })
	ctx := t.Context()
	if err := db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	seedObjective := func(id string, status model.ObjectiveStatus) {
		t.Helper()
		o := model.Objective{ID: id, Project: "p", Title: "Synthetic " + id, Status: status,
			Mode: model.ModeManual, FreezeMode: model.FreezeSafe}
		if err := db.CreateObjective(ctx, &o); err != nil {
			t.Fatal(err)
		}
	}
	seedObjective("o-a", model.ObjectiveActive)
	seedObjective("o-b", model.ObjectivePaused)
	seedObjective("o-c", model.ObjectiveDoneCandidate)
	seedObjective("o-closed", model.ObjectiveDone)
	seedObjective("o-archived", model.ObjectiveCanceled)
	seedCard := func(c model.Card) {
		t.Helper()
		c.Body = "BODY MUST STAY PRIVATE"
		if c.Kind == "" {
			c.Kind = model.KindDev
		}
		if c.Objective == "" {
			c.Objective = "o-a"
		}
		if err := db.CreateCard(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []model.Card{
		{ID: "c-live", Status: model.CardRunning},
		{ID: "c-clear", Status: model.CardReady, Deps: []string{"c-stale"}},
		{ID: "c-stale", Status: model.CardCanceled, WaitReason: "must not leak"},
		{ID: "c-needs", Status: model.CardNeedsInput, WaitQuestionID: "q-z-bound"},
		{ID: "c-answered", Status: model.CardNeedsInput, WaitQuestionID: "q-d-closed"},
		{ID: "c-downstream", Status: model.CardReady, Deps: []string{"c-fail"}},
		{ID: "c-fail", Status: model.CardFailed},
		{ID: "c-paused", Status: model.CardPaused},
		{ID: "c-wait", Status: model.CardWaiting, WaitReason: "blocked by c-live\tfor a claim\nsecond line"},
		{ID: "c-backoff", Status: model.CardWaiting, WaitReason: "backoff:2000-01-01T00:00:00Z"},
		{ID: "c-no-reason", Status: model.CardWaiting},
		{ID: "c-review", Status: model.CardReady, Kind: model.KindReview, Deps: []string{"c-live", "c-live"}},
		{ID: "c-review-extra", Status: model.CardReady, Kind: model.KindReview, Deps: []string{"c-live", "c-fail"}},
		{ID: "c-review-missing", Status: model.CardReady, Kind: model.KindReview, Deps: []string{"absent"}},
		{ID: "c-ordinary-missing", Status: model.CardReady, Deps: []string{"absent"}},
		{ID: "c-closed-failure", Objective: "o-closed", Status: model.CardFailed},
		{ID: "c-archive-failure", Objective: "o-archived", Status: model.CardFailed},
		{ID: "c-on-paused-objective", Objective: "o-b", Status: model.CardReady},
		{ID: "c-candidate-fail", Objective: "o-c", Status: model.CardFailed, Legacy: model.Legacy{Reason: "legacy reason"}},
	} {
		seedCard(c)
	}
	for _, q := range []model.Question{
		{ID: "q-a-unrelated", Objective: "o-a", Scope: "c-needs", ScopeKind: "card", Kind: model.QuestionRefusal, Text: "OLDER QUESTION"},
		{ID: "q-z-bound", Objective: "o-c", Scope: "c-needs", ScopeKind: "card", Kind: model.QuestionRefusal, Text: "Confirm retry?\nnot printed"},
		{ID: "q-d-closed", Objective: "o-a", Scope: "c-answered", ScopeKind: "card", Kind: model.QuestionRefusal, Text: "ANSWERED QUESTION", Answer: "yes"},
		{ID: "q-e-unrelated", Objective: "o-a", Scope: "c-answered", ScopeKind: "card", Kind: model.QuestionRefusal, Text: "UNRELATED QUESTION"},
	} {
		if err := db.CreateQuestion(ctx, &q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	a := model.Attempt{ID: "a-fail", Card: "c-fail", StartedAt: now, EndedAt: now,
		Outcome: model.OutcomeTestsFailed, ErrorExcerpt: "fixture rejected\nnot printed"}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) (int, string, string) {
		t.Helper()
		return callCLI(t, append([]string{"--home", home}, args...))
	}
	code, output, stderr := call("blockers", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("blockers: %d %s", code, stderr)
	}
	var rows []blockerRow
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatal(err)
	}
	want := []blockerRow{
		{ID: "c-answered", Objective: "o-a", Status: model.CardNeedsInput, Reason: "needs input; no reason recorded"},
		{ID: "c-backoff", Objective: "o-a", Status: model.CardWaiting, Reason: "backoff:2000-01-01T00:00:00Z"},
		{ID: "c-downstream", Objective: "o-a", Status: model.CardReady, Reason: "dependency c-fail is failed"},
		{ID: "c-fail", Objective: "o-a", Status: model.CardFailed, Reason: "fixture rejected"},
		{ID: "c-needs", Objective: "o-a", Status: model.CardNeedsInput, Reason: "Confirm retry?", QuestionID: "q-z-bound"},
		{ID: "c-no-reason", Objective: "o-a", Status: model.CardWaiting, Reason: "waiting; no reason recorded"},
		{ID: "c-ordinary-missing", Objective: "o-a", Status: model.CardReady, Reason: "dependency absent does not exist"},
		{ID: "c-paused", Objective: "o-a", Status: model.CardPaused, Reason: "card is paused"},
		{ID: "c-review-extra", Objective: "o-a", Status: model.CardReady, Reason: "dependency c-fail is failed"},
		{ID: "c-review-missing", Objective: "o-a", Status: model.CardReady, Reason: "review subject absent does not exist"},
		{ID: "c-wait", Objective: "o-a", Status: model.CardWaiting, Reason: "blocked by c-live for a claim"},
		{ID: "c-on-paused-objective", Objective: "o-b", Status: model.CardReady, Reason: "objective is paused"},
		{ID: "c-candidate-fail", Objective: "o-c", Status: model.CardFailed, Reason: "legacy reason"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("blockers mismatch:\ngot %+v\nwant %+v", rows, want)
	}
	code, text, stderr := call("blockers", "--objective", "o-a")
	if code != 0 || stderr != "" || len(strings.Split(strings.TrimSpace(text), "\n")) != 11 ||
		!strings.Contains(text, "o-a\tc-needs\tneeds_input\tConfirm retry? (question q-z-bound)\n") {
		t.Fatalf("text: code=%d output=%q stderr=%q", code, text, stderr)
	}

	// Baseline operator workflow: full card list + pending questions + one
	// drill-down to discover the attempt failure on the same stored fixture.
	baselineBytes := 0
	baseline := [][]string{{"card", "list", "--json"}, {"question", "list", "--json"}, {"card", "show", "c-fail", "--json"}}
	for _, args := range baseline {
		code, raw, stderr := call(args...)
		if code != 0 || stderr != "" {
			t.Fatalf("baseline %v: %d %q", args, code, stderr)
		}
		baselineBytes += len(raw)
	}
	t.Logf("operator discovery on synthetic fixture: baseline %d calls / %d bytes; blockers 1 call / %d bytes", len(baseline), baselineBytes, len(output))
}

func TestBlockersEmptyAndInvalidScope(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		args   []string
		code   int
		output string
		err    string
	}{
		{args: []string{"blockers"}, output: "no blockers\n"},
		{args: []string{"blockers", "--json"}, output: "[]\n"},
		{args: []string{"blockers", "--objective", "missing"}, code: 1, err: `objective "missing" not found`},
		{args: []string{"blockers", "unexpected"}, code: 2, err: "blockers [--objective"},
		{args: []string{"--help"}, output: "blockers [--objective <id>]"},
	} {
		code, out, err := callCLI(t, append([]string{"--home", home}, tc.args...))
		if code != tc.code || !strings.Contains(out, tc.output) || !strings.Contains(err, tc.err) {
			t.Errorf("%v => %d %q %q", tc.args, code, out, err)
		}
	}
}
