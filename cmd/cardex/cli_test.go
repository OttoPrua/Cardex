package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/store"
)

func TestHelpListsCommands(t *testing.T) {
	var out bytes.Buffer
	if code := Main([]string{"--help"}, &out, io.Discard); code != 0 {
		t.Fatal(code)
	}
	for _, name := range []string{
		"serve", "project", "objective", "node", "card", "question", "channel",
		"route", "positioning", "schedule", "scorecard", "import-legacy", "doctor", "host",
		"add", "list", "show", "set", "pause", "resume", "cancel", "freeze", "reopen",
		"retry", "revive", "answer", "check", "explain", "proposals", "approve", "reject", "run",
	} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("help missing %s\n%s", name, out.String())
		}
	}
}

func TestProjectObjectiveCardRoundTrip(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	var out, errb bytes.Buffer
	code := Main([]string{"--home", home, "--json", "project", "add", "--id", "p1", "--repo", repo, "--test-cmd", "go test ./...", "--design-dir", "docs/design/"}, &out, &errb)
	if code != 0 {
		t.Fatalf("add project %d %s", code, errb.String())
	}
	var project model.Project
	if err := json.Unmarshal(out.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID != "p1" || project.RepoPath != repo || project.TestCmd != "go test ./..." || project.DesignDir != "docs/design/" {
		t.Fatalf("project %+v", project)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "project", "list"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var projects []model.Project
	if err := json.Unmarshal(out.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].RepoPath != repo {
		t.Fatalf("list %+v", projects)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "project", "show", "--json", "p1"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var shown model.Project
	if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.ID != project.ID || shown.RepoPath != project.RepoPath || shown.WorktreeRoot != project.WorktreeRoot {
		t.Fatalf("show %+v add %+v", shown, project)
	}

	out.Reset()
	errb.Reset()
	code = Main([]string{"--home", home, "--json", "objective", "add", "--id", "o1", "--project", "p1", "--title", "Ship it", "--acceptance", "go test ./...", "--mode", "manual", "--freeze-mode", "fast"}, &out, &errb)
	if code != 0 {
		t.Fatalf("add objective %d %s", code, errb.String())
	}
	var objective model.Objective
	if err := json.Unmarshal(out.Bytes(), &objective); err != nil {
		t.Fatal(err)
	}
	if objective.Title != "Ship it" || objective.Project != "p1" || objective.Mode != model.ModeManual || objective.FreezeMode != model.FreezeFast {
		t.Fatalf("objective %+v", objective)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "objective", "list"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var objectives []model.Objective
	if err := json.Unmarshal(out.Bytes(), &objectives); err != nil {
		t.Fatal(err)
	}
	if len(objectives) != 1 || objectives[0].Title != "Ship it" {
		t.Fatalf("objectives %+v", objectives)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "objective", "show", "o1"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var oshown model.Objective
	if err := json.Unmarshal(out.Bytes(), &oshown); err != nil {
		t.Fatal(err)
	}
	if oshown.Title != "Ship it" || oshown.IntegrationBranch != "cardex/o1/main" {
		t.Fatalf("show objective %+v", oshown)
	}

	out.Reset()
	errb.Reset()
	code = Main([]string{"--home", home, "--json", "card", "add", "--id", "c1", "--objective", "o1", "--title", "Write parser", "--kind", "dev", "--body", "parse it", "--claim", "calc.go=Answer"}, &out, &errb)
	if code != 0 {
		t.Fatalf("add card %d %s", code, errb.String())
	}
	var card model.Card
	if err := json.Unmarshal(out.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.Title != "Write parser" || card.Kind != model.KindDev || card.Body != "parse it" || len(card.Claims) != 1 || card.Claims[0].Path != "calc.go" || card.Claims[0].Symbol != "Answer" {
		t.Fatalf("card %+v", card)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "card", "list", "--objective", "o1"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var cards []model.Card
	if err := json.Unmarshal(out.Bytes(), &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].Title != "Write parser" || cards[0].Claims[0].Symbol != "Answer" {
		t.Fatalf("cards %+v", cards)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "card", "show", "c1"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var cshown model.Card
	if err := json.Unmarshal(out.Bytes(), &cshown); err != nil {
		t.Fatal(err)
	}
	if cshown.Body != "parse it" || cshown.Objective != "o1" {
		t.Fatalf("show card %+v", cshown)
	}
}

func TestCardAddSoftInputs(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		want  func(t *testing.T, card model.Card, raw string)
	}{
		{
			name:  "missing title",
			extra: nil,
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Status != model.CardNeedsInput {
					t.Fatalf("status %s", card.Status)
				}
				if card.Title == "" {
					t.Fatal("missing placeholder title")
				}
			},
		},
		{
			name:  "tier t1",
			extra: []string{"--title", "Tier one", "--tier", "t1"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Tier != model.TierT1 || card.Status != model.CardReady {
					t.Fatalf("tier %s status %s", card.Tier, card.Status)
				}
			},
		},
		{
			name:  "tier t2",
			extra: []string{"--title", "Tier card", "--tier", "t2"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Tier != model.TierT2 || card.Status != model.CardReady {
					t.Fatalf("tier %s status %s", card.Tier, card.Status)
				}
			},
		},
		{
			name:  "tier t3",
			extra: []string{"--title", "Tier three", "--tier", "t3"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Tier != model.TierT3 || card.Status != model.CardReady {
					t.Fatalf("tier %s status %s", card.Tier, card.Status)
				}
			},
		},
		{
			name:  "empty kind",
			extra: []string{"--title", "Kind card", "--kind", ""},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Kind != model.KindDev || card.Status != model.CardReady {
					t.Fatalf("kind %s status %s", card.Kind, card.Status)
				}
			},
		},
		{
			name:  "kind DEV",
			extra: []string{"--title", "Dev card", "--kind", "DEV"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Kind != model.KindDev || card.Status != model.CardReady {
					t.Fatalf("kind %s status %s", card.Kind, card.Status)
				}
				if card.Title != "Dev card" {
					t.Fatalf("title %q", card.Title)
				}
			},
		},
		{
			name:  "empty status",
			extra: []string{"--title", "Empty status", "--status", ""},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Status != model.CardReady {
					t.Fatalf("status %s", card.Status)
				}
				if card.Title != "Empty status" {
					t.Fatalf("title %q", card.Title)
				}
			},
		},
		{
			name:  "status READY",
			extra: []string{"--title", "Ready card", "--status", "READY"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Status != model.CardReady {
					t.Fatalf("status %s", card.Status)
				}
			},
		},
		{
			name:  "empty claim",
			extra: []string{"--title", "Claim card", "--claim", ""},
			want: func(t *testing.T, card model.Card, _ string) {
				if len(card.Claims) != 0 {
					t.Fatalf("claims %+v", card.Claims)
				}
				if card.Status != model.CardReady {
					t.Fatalf("status %s", card.Status)
				}
			},
		},
		{
			name:  "tier T4",
			extra: []string{"--title", "Bad tier", "--tier", "T4"},
			want: func(t *testing.T, card model.Card, raw string) {
				assertNeedsInput(t, card, "Bad tier")
				if card.Tier != model.TierNone {
					t.Fatalf("tier %q", card.Tier)
				}
				if !strings.Contains(raw, "T4") || !strings.Contains(card.WaitReason, "T4") {
					t.Fatalf("json %s wait %q", raw, card.WaitReason)
				}
			},
		},
		{
			name:  "unknown kind",
			extra: []string{"--title", "Odd kind", "--kind", "WIDGET"},
			want: func(t *testing.T, card model.Card, raw string) {
				assertNeedsInput(t, card, "Odd kind")
				if card.Kind != model.KindDev {
					t.Fatalf("kind %s", card.Kind)
				}
				if !strings.Contains(raw, "WIDGET") || !strings.Contains(card.WaitReason, "WIDGET") {
					t.Fatalf("json %s wait %q", raw, card.WaitReason)
				}
			},
		},
		{
			name:  "unknown status",
			extra: []string{"--title", "Odd status", "--status", "BOGUS"},
			want: func(t *testing.T, card model.Card, raw string) {
				assertNeedsInput(t, card, "Odd status")
				if !strings.Contains(raw, "BOGUS") || !strings.Contains(card.WaitReason, "BOGUS") {
					t.Fatalf("json %s wait %q", raw, card.WaitReason)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			repo := t.TempDir()
			var out, errb bytes.Buffer
			code := Main([]string{"--home", home, "project", "add", "--id", "p1", "--repo", repo, "--test-cmd", "go test ./...", "--design-dir", "docs/design/"}, &out, &errb)
			if code != 0 {
				t.Fatalf("project %d %s", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			code = Main([]string{"--home", home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Ship it", "--acceptance", "go test ./..."}, &out, &errb)
			if code != 0 {
				t.Fatalf("objective %d %s", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			args := []string{"--home", home, "card", "add", "--objective", "o1"}
			args = append(args, tc.extra...)
			code = Main(args, &out, &errb)
			if code != 0 {
				t.Fatalf("add %d %s", code, errb.String())
			}
			if strings.Contains(errb.String(), "claim path required") {
				t.Fatalf("stderr %s", errb.String())
			}
			out.Reset()
			errb.Reset()
			code = Main([]string{"--home", home, "card", "list", "--json"}, &out, &errb)
			if code != 0 {
				t.Fatalf("list %d %s", code, errb.String())
			}
			var cards []model.Card
			if err := json.Unmarshal(out.Bytes(), &cards); err != nil {
				t.Fatal(err)
			}
			if len(cards) != 1 || cards[0].Objective != "o1" {
				t.Fatalf("list %s", out.String())
			}
			tc.want(t, cards[0], out.String())
			t.Logf("exit 0 status=%s tier=%s kind=%s claims=%d title=%q wait=%q", cards[0].Status, cards[0].Tier, cards[0].Kind, len(cards[0].Claims), cards[0].Title, cards[0].WaitReason)
		})
	}
}

func TestObjectiveAddSoftInputs(t *testing.T) {
	t.Run("missing project", func(t *testing.T) {
		home := t.TempDir()
		var out, errb bytes.Buffer
		code := Main([]string{"--home", home, "objective", "add", "--title", "Ship"}, &out, &errb)
		if code != 2 {
			t.Fatalf("exit %d %s", code, errb.String())
		}
		out.Reset()
		errb.Reset()
		if code := Main([]string{"--home", home, "objective", "list", "--json"}, &out, &errb); code != 0 {
			t.Fatalf("list %d %s", code, errb.String())
		}
		var items []model.Objective
		if err := json.Unmarshal(out.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Fatalf("list %s", out.String())
		}
	})
	t.Run("unknown flag", func(t *testing.T) {
		home := t.TempDir()
		repo := t.TempDir()
		var out, errb bytes.Buffer
		code := Main([]string{"--home", home, "project", "add", "--id", "p1", "--repo", repo}, &out, &errb)
		if code != 0 {
			t.Fatalf("project %d %s", code, errb.String())
		}
		out.Reset()
		errb.Reset()
		code = Main([]string{"--home", home, "objective", "add", "--project", "p1", "--title", "Ship", "--not-a-flag", "x"}, &out, &errb)
		if code != 2 {
			t.Fatalf("exit %d %s", code, errb.String())
		}
		out.Reset()
		errb.Reset()
		if code := Main([]string{"--home", home, "objective", "list", "--json"}, &out, &errb); code != 0 {
			t.Fatalf("list %d %s", code, errb.String())
		}
		var items []model.Objective
		if err := json.Unmarshal(out.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Fatalf("list %s", out.String())
		}
	})
	t.Run("unknown verb", func(t *testing.T) {
		home := t.TempDir()
		var out, errb bytes.Buffer
		if code := Main([]string{"--home", home, "objective", "explode"}, &out, &errb); code != 2 {
			t.Fatalf("exit %d %s", code, errb.String())
		}
	})

	cases := []struct {
		name  string
		extra []string
		want  func(t *testing.T, obj model.Objective, questions []model.Question)
	}{
		{
			name: "missing title",
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				if obj.Title == "" {
					t.Fatal("missing placeholder title")
				}
				if obj.Mode != model.ModeAuto || obj.FreezeMode != model.FreezeSafe || obj.Status != model.ObjectivePaused {
					t.Fatalf("mode %s freeze %s status %s", obj.Mode, obj.FreezeMode, obj.Status)
				}
				assertObjectiveQuestion(t, obj, questions, "missing title")
			},
		},
		{
			name:  "mode AUTO",
			extra: []string{"--title", "Mode auto", "--mode", "AUTO"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Mode auto", model.ModeAuto, model.FreezeSafe, model.ObjectiveActive)
			},
		},
		{
			name:  "mode empty",
			extra: []string{"--title", "Mode empty", "--mode", ""},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Mode empty", model.ModeAuto, model.FreezeSafe, model.ObjectiveActive)
			},
		},
		{
			name:  "mode MANUAL",
			extra: []string{"--title", "Mode manual", "--mode", "MANUAL"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Mode manual", model.ModeManual, model.FreezeSafe, model.ObjectiveActive)
			},
		},
		{
			name:  "mode manual",
			extra: []string{"--title", "Mode manual lower", "--mode", "manual"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Mode manual lower", model.ModeManual, model.FreezeSafe, model.ObjectiveActive)
			},
		},
		{
			name:  "freeze-mode SAFE",
			extra: []string{"--title", "Freeze safe", "--freeze-mode", "SAFE"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Freeze safe", model.ModeAuto, model.FreezeSafe, model.ObjectiveActive)
			},
		},
		{
			name:  "freeze-mode empty",
			extra: []string{"--title", "Freeze empty", "--freeze-mode", ""},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Freeze empty", model.ModeAuto, model.FreezeSafe, model.ObjectiveActive)
			},
		},
		{
			name:  "freeze-mode FAST",
			extra: []string{"--title", "Freeze fast", "--freeze-mode", "FAST"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Freeze fast", model.ModeAuto, model.FreezeFast, model.ObjectiveActive)
			},
		},
		{
			name:  "freeze-mode fast",
			extra: []string{"--title", "Freeze fast lower", "--freeze-mode", "fast"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Freeze fast lower", model.ModeAuto, model.FreezeFast, model.ObjectiveActive)
			},
		},
		{
			name:  "status ACTIVE",
			extra: []string{"--title", "Status active", "--status", "ACTIVE"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Status active", model.ModeAuto, model.FreezeSafe, model.ObjectiveActive)
			},
		},
		{
			name:  "status empty",
			extra: []string{"--title", "Status empty", "--status", ""},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Status empty", model.ModeAuto, model.FreezeSafe, model.ObjectiveActive)
			},
		},
		{
			name:  "status PAUSED",
			extra: []string{"--title", "Status paused", "--status", "PAUSED"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Status paused", model.ModeAuto, model.FreezeSafe, model.ObjectivePaused)
			},
		},
		{
			name:  "status DONE_CANDIDATE",
			extra: []string{"--title", "Status candidate", "--status", "DONE_CANDIDATE"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Status candidate", model.ModeAuto, model.FreezeSafe, model.ObjectiveDoneCandidate)
			},
		},
		{
			name:  "status DONE",
			extra: []string{"--title", "Status done", "--status", "DONE"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Status done", model.ModeAuto, model.FreezeSafe, model.ObjectiveDone)
			},
		},
		{
			name:  "status CANCELED",
			extra: []string{"--title", "Status canceled", "--status", "CANCELED"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				assertCanonicalObjective(t, obj, questions, "Status canceled", model.ModeAuto, model.FreezeSafe, model.ObjectiveCanceled)
			},
		},
		{
			name:  "unrecognized mode",
			extra: []string{"--title", "Bad mode", "--mode", "NOPE"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				if obj.Title != "Bad mode" || obj.Mode != model.ModeAuto || obj.FreezeMode != model.FreezeSafe {
					t.Fatalf("title %q mode %s freeze %s", obj.Title, obj.Mode, obj.FreezeMode)
				}
				assertObjectiveQuestion(t, obj, questions, "NOPE")
				if !strings.Contains(questions[0].Text, "unrecognized mode") {
					t.Fatalf("question %q", questions[0].Text)
				}
			},
		},
		{
			name:  "unrecognized freeze-mode",
			extra: []string{"--title", "Bad freeze", "--freeze-mode", "LOOSE"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				if obj.Title != "Bad freeze" || obj.Mode != model.ModeAuto || obj.FreezeMode != model.FreezeSafe {
					t.Fatalf("title %q mode %s freeze %s", obj.Title, obj.Mode, obj.FreezeMode)
				}
				assertObjectiveQuestion(t, obj, questions, "LOOSE")
				if !strings.Contains(questions[0].Text, "unrecognized freeze-mode") {
					t.Fatalf("question %q", questions[0].Text)
				}
			},
		},
		{
			name:  "unrecognized status",
			extra: []string{"--title", "Bad status", "--status", "BOGUS"},
			want: func(t *testing.T, obj model.Objective, questions []model.Question) {
				if obj.Title != "Bad status" || obj.Mode != model.ModeAuto || obj.FreezeMode != model.FreezeSafe {
					t.Fatalf("title %q mode %s freeze %s", obj.Title, obj.Mode, obj.FreezeMode)
				}
				assertObjectiveQuestion(t, obj, questions, "BOGUS")
				if !strings.Contains(questions[0].Text, "unrecognized status") {
					t.Fatalf("question %q", questions[0].Text)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			repo := t.TempDir()
			var out, errb bytes.Buffer
			code := Main([]string{"--home", home, "project", "add", "--id", "p1", "--repo", repo}, &out, &errb)
			if code != 0 {
				t.Fatalf("project %d %s", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			args := []string{"--home", home, "objective", "add", "--id", "o1", "--project", "p1"}
			args = append(args, tc.extra...)
			code = Main(args, &out, &errb)
			if code != 0 {
				t.Fatalf("add %d %s", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			code = Main([]string{"--home", home, "objective", "list", "--json"}, &out, &errb)
			if code != 0 {
				t.Fatalf("list %d %s", code, errb.String())
			}
			var objectives []model.Objective
			if err := json.Unmarshal(out.Bytes(), &objectives); err != nil {
				t.Fatal(err)
			}
			if len(objectives) != 1 || objectives[0].ID != "o1" || objectives[0].Project != "p1" {
				t.Fatalf("list %s", out.String())
			}
			out.Reset()
			errb.Reset()
			code = Main([]string{"--home", home, "objective", "show", "--json", "o1"}, &out, &errb)
			if code != 0 {
				t.Fatalf("show %d %s", code, errb.String())
			}
			var shown model.Objective
			if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
				t.Fatal(err)
			}
			if shown.Title != objectives[0].Title || shown.Mode != objectives[0].Mode || shown.FreezeMode != objectives[0].FreezeMode || shown.Status != objectives[0].Status {
				t.Fatalf("show %+v list %+v", shown, objectives[0])
			}
			db, err := openStore(home)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			questions, err := db.ListQuestions(context.Background(), "o1")
			if err != nil {
				t.Fatal(err)
			}
			tc.want(t, objectives[0], questions)
			t.Logf("exit 0 mode=%s freeze=%s status=%s title=%q questions=%d", objectives[0].Mode, objectives[0].FreezeMode, objectives[0].Status, objectives[0].Title, len(questions))
		})
	}
}

func assertCanonicalObjective(t *testing.T, obj model.Objective, questions []model.Question, title string, mode model.ObjectiveMode, freeze model.FreezeMode, status model.ObjectiveStatus) {
	t.Helper()
	if !obj.Mode.Valid() || !obj.FreezeMode.Valid() || !obj.Status.Valid() {
		t.Fatalf("invalid mode=%s freeze=%s status=%s", obj.Mode, obj.FreezeMode, obj.Status)
	}
	if obj.Title != title || obj.Mode != mode || obj.FreezeMode != freeze || obj.Status != status {
		t.Fatalf("title %q mode %s freeze %s status %s", obj.Title, obj.Mode, obj.FreezeMode, obj.Status)
	}
	if len(questions) != 0 {
		t.Fatalf("questions %+v", questions)
	}
}

func assertObjectiveQuestion(t *testing.T, obj model.Objective, questions []model.Question, token string) {
	t.Helper()
	if !obj.Mode.Valid() || !obj.FreezeMode.Valid() || !obj.Status.Valid() {
		t.Fatalf("invalid mode=%s freeze=%s status=%s", obj.Mode, obj.FreezeMode, obj.Status)
	}
	if obj.Status != model.ObjectivePaused {
		t.Fatalf("status %s", obj.Status)
	}
	if len(questions) != 1 {
		t.Fatalf("questions %+v", questions)
	}
	q := questions[0]
	if q.Kind != model.QuestionCustom || q.Objective != obj.ID || q.Text == "" || !strings.Contains(q.Text, token) {
		t.Fatalf("question %+v", q)
	}
}

func TestObjectiveAddQuestionCommit(t *testing.T) {
	cases := []struct {
		name   string
		extra  []string
		token  string
		reason string
	}{
		{name: "missing title", reason: "missing title"},
		{name: "unrecognized mode", extra: []string{"--title", "Bad mode", "--mode", "NOPE"}, token: "NOPE", reason: "unrecognized mode"},
		{name: "unrecognized freeze-mode", extra: []string{"--title", "Bad freeze", "--freeze-mode", "LOOSE"}, token: "LOOSE", reason: "unrecognized freeze-mode"},
		{name: "unrecognized status", extra: []string{"--title", "Bad status", "--status", "BOGUS"}, token: "BOGUS", reason: "unrecognized status"},
	}
	for _, tc := range cases {
		t.Run(tc.name+" success", func(t *testing.T) {
			objectiveQuestionWrite = nil
			home := addFixtureProject(t)
			var out, errb bytes.Buffer
			args := []string{"--home", home, "objective", "add", "--id", "o1", "--project", "p1"}
			args = append(args, tc.extra...)
			if code := Main(args, &out, &errb); code != 0 {
				t.Fatalf("add %d %s", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			if code := Main([]string{"--home", home, "objective", "list", "--json"}, &out, &errb); code != 0 {
				t.Fatalf("list %d %s", code, errb.String())
			}
			var objectives []model.Objective
			if err := json.Unmarshal(out.Bytes(), &objectives); err != nil {
				t.Fatal(err)
			}
			if len(objectives) != 1 || objectives[0].ID != "o1" || objectives[0].Status != model.ObjectivePaused {
				t.Fatalf("list %s", out.String())
			}
			out.Reset()
			errb.Reset()
			if code := Main([]string{"--home", home, "objective", "show", "--json", "o1"}, &out, &errb); code != 0 {
				t.Fatalf("show %d %s", code, errb.String())
			}
			var shown model.Objective
			if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
				t.Fatal(err)
			}
			if shown.ID != "o1" || shown.Status != model.ObjectivePaused {
				t.Fatalf("show %+v", shown)
			}
			db, err := openStore(home)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			questions, err := db.ListQuestions(context.Background(), "o1")
			if err != nil {
				t.Fatal(err)
			}
			if len(questions) != 1 {
				t.Fatalf("questions %+v", questions)
			}
			q := questions[0]
			if q.Kind != model.QuestionCustom || q.Objective != "o1" || !strings.Contains(q.Text, tc.reason) {
				t.Fatalf("question %+v", q)
			}
			if tc.token != "" && !strings.Contains(q.Text, tc.token) {
				t.Fatalf("question %q missing token %q", q.Text, tc.token)
			}
			t.Logf("input %s success status=%s question=%q", tc.name, shown.Status, q.Text)
		})
		t.Run(tc.name+" rollback", func(t *testing.T) {
			home := addFixtureProject(t)
			t.Cleanup(func() { objectiveQuestionWrite = nil })
			objectiveQuestionWrite = func(ctx context.Context, db *store.Store, q *model.Question) error {
				got, err := db.GetObjective(ctx, "o1")
				if err != nil {
					t.Errorf("in-tx objective: %v", err)
					return err
				}
				if got.ID != "o1" || got.Status != model.ObjectivePaused {
					t.Errorf("in-tx objective id %s status %s", got.ID, got.Status)
				}
				if q.Kind != model.QuestionCustom || q.Objective != "o1" || !strings.Contains(q.Text, tc.reason) {
					t.Errorf("in-tx question %+v", q)
				}
				if tc.token != "" && !strings.Contains(q.Text, tc.token) {
					t.Errorf("in-tx question %q missing token %q", q.Text, tc.token)
				}
				return errors.New("forced question write failure")
			}
			var out, errb bytes.Buffer
			args := []string{"--home", home, "objective", "add", "--id", "o1", "--project", "p1"}
			args = append(args, tc.extra...)
			if code := Main(args, &out, &errb); code == 0 {
				t.Fatalf("add succeeded, stdout %s", out.String())
			}
			if !strings.Contains(errb.String(), "forced question write failure") {
				t.Fatalf("stderr %s", errb.String())
			}
			out.Reset()
			errb.Reset()
			if code := Main([]string{"--home", home, "objective", "list", "--json"}, &out, &errb); code != 0 {
				t.Fatalf("list %d %s", code, errb.String())
			}
			var objectives []model.Objective
			if err := json.Unmarshal(out.Bytes(), &objectives); err != nil {
				t.Fatal(err)
			}
			if len(objectives) != 0 {
				t.Fatalf("list after rollback %s", out.String())
			}
			out.Reset()
			errb.Reset()
			if code := Main([]string{"--home", home, "objective", "show", "--json", "o1"}, &out, &errb); code == 0 {
				t.Fatalf("show after rollback %s", out.String())
			}
			db, err := openStore(home)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			questions, err := db.ListQuestions(context.Background(), "o1")
			if err != nil {
				t.Fatal(err)
			}
			if len(questions) != 0 {
				t.Fatalf("questions after rollback %+v", questions)
			}
			all, err := db.ListQuestions(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(all) != 0 {
				t.Fatalf("orphan questions %+v", all)
			}
			t.Logf("input %s rollback objective absent", tc.name)
		})
	}
}

func addFixtureProject(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	var out, errb bytes.Buffer
	if code := Main([]string{"--home", home, "project", "add", "--id", "p1", "--repo", repo}, &out, &errb); code != 0 {
		t.Fatalf("project %d %s", code, errb.String())
	}
	return home
}

func assertNeedsInput(t *testing.T, card model.Card, title string) {
	t.Helper()
	if !card.Kind.Valid() || !card.Tier.Valid() || !card.Status.Valid() {
		t.Fatalf("illegal kind=%s tier=%q status=%s", card.Kind, card.Tier, card.Status)
	}
	if card.Status != model.CardNeedsInput {
		t.Fatalf("status %s", card.Status)
	}
	if card.Title != title {
		t.Fatalf("title %q", card.Title)
	}
}

func TestServeLoopsUntilStopped(t *testing.T) {
	// A v1 home fails in resolveHome, so these calls never reach sched.Serve.
	for _, addr := range []string{"127.0.0.1:8787", "127.0.0.1:8788"} {
		home := makeV1(t)
		var errb strings.Builder
		code := runServe(t.Context(), home, addr, io.Discard, &errb)
		if code == 0 || !strings.Contains(errb.String(), "import-legacy") {
			t.Fatalf("%s code %d stderr %s", addr, code, errb.String())
		}
		if strings.Contains(errb.String(), "reserved") {
			t.Fatalf("%s port refusal %s", addr, errb.String())
		}
		if _, err := os.Stat(filepath.Join(home, "serve-addr")); !os.IsNotExist(err) {
			t.Fatalf("%s wrote serve-addr: %v", addr, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var buf lockBuf
	done := make(chan int, 1)
	go func() {
		done <- runServe(ctx, t.TempDir(), "127.0.0.1:0", &buf, io.Discard)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(buf.String(), "loop iteration") < 2 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("serve output:\n%s", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(buf.String(), "fatal") {
		t.Fatalf("fatal in output:\n%s", buf.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit %d\n%s", code, buf.String())
		}
		t.Logf("ephemeral serve exit %d\n%s", code, buf.String())
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return")
	}
}

func TestRefusesUserCardexHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CARDEX_HOME", "")
	dir := filepath.Join(root, ".cardex")
	writeTaskFile(t, dir)
	var errb bytes.Buffer
	code := Main([]string{"--home", dir, "project", "list"}, io.Discard, &errb)
	if code == 0 || !strings.Contains(errb.String(), "import-legacy") {
		t.Fatalf("code %d err %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "cardex.db")); !os.IsNotExist(err) {
		t.Fatalf("created cardex.db: %v", err)
	}
}

type lockBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
