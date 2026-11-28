package sched

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
)

func TestAPI2TickResumesOnlyExactMarkedComparisonApproval(t *testing.T) {
	for _, marked := range []bool{true, false} {
		name := "marked-exact-approval"
		if !marked {
			name = "historical-unmarked"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			home, db, repo := newHome(t)
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"schedules":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			_, oid := seedRepo(t, db, repo, "true")
			main := rev(t, repo, "main")
			git(t, repo, "checkout", "-b", "comparison-integration")
			if err := os.WriteFile(filepath.Join(repo, "completion-marker"), []byte("fixture\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", "completion-marker")
			git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "local completion fixture")
			git(t, repo, "checkout", "main")
			if marked {
				git(t, repo, "checkout", "--detach", "main")
				// Fixture config uses a literal main repository path.
				literal, err := filepath.EvalSymlinks(repo)
				if err != nil {
					t.Fatal(err)
				}
				project, err := db.GetProject(ctx, "p")
				if err != nil {
					t.Fatal(err)
				}
				project.RepoPath = literal
				if err := db.UpdateProject(ctx, &project); err != nil {
					t.Fatal(err)
				}
			}
			r := New(home)
			r.Notify = func(context.Context, notify.Message) error { return notify.ErrNotImplemented }
			r.Launch = func(context.Context, string, string) (int, error) {
				t.Fatal("comparison or gated approval dispatched a child")
				return 0, nil
			}
			r.Classify = func(context.Context, string, string) (string, error) {
				t.Fatal("comparison or gated approval sent a classifier request")
				return "", nil
			}
			const comparison = "optional-comparison"
			if marked {
				c := model.Card{ID: comparison, Objective: oid, Kind: model.KindResearch, Status: model.CardReady,
					Title: "fixture comparison", Body: "explicit synthetic question", Channel: "fixture-channel", Model: "fixture-model",
					Requires: []string{model.RequireStrictComparison}}
				if err := db.CreateCard(ctx, &c); err != nil {
					t.Fatal(err)
				}
				// The native runner establishes the actual runtime hold,
				// independent of the development-loop candidate fixture.
				if err := r.Tick(ctx); err != nil {
					t.Fatal(readLog(home, err))
				}
				held, err := db.GetCard(ctx, comparison)
				if err != nil || held.Status != model.CardNeedsInput || held.WaitReason != model.CompatibleRuntimeHold {
					t.Fatalf("runner didn't establish comparison hold: %+v %v", held, err)
				}
			}
			obj, err := db.GetObjective(ctx, oid)
			if err != nil {
				t.Fatal(err)
			}
			obj.Mode, obj.IntegrationBranch = model.ModeAuto, "comparison-integration"
			obj.Acceptance.Commands = []string{"test -f completion-marker"}
			if err := db.UpdateObjective(ctx, &obj); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateNode(ctx, &model.DesignNode{ID: "frozen-completion-scope", Objective: oid,
				Layer: model.LayerDetail, Status: model.NodeFrozen}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CARDEX_HOME", home)
			if err := loop.DevStep(ctx, oid); err != nil {
				t.Fatal(err)
			}
			questions, err := db.ListQuestions(ctx, oid)
			if err != nil || len(questions) != 1 || questions[0].Kind != model.QuestionDone {
				t.Fatalf("unexpected candidate questions: %v %+v", err, questions)
			}
			approval := questions[0]
			r.Engine = func(context.Context, string) error {
				t.Fatal("completion retry used a custom engine")
				return nil
			}
			// Resume must use the scheduler's already-open database and
			// must not rely on the shell's unrelated CARDEX_HOME value.
			t.Setenv("CARDEX_HOME", filepath.Join(t.TempDir(), "must-not-be-created"))
			if marked {
				other := model.Question{ID: "unrelated-done", Objective: oid, Scope: "not-the-candidate", ScopeKind: "objective",
					Kind: model.QuestionDone, Options: []string{"approve"}}
				if err := db.CreateQuestion(ctx, &other); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Questions(db).Answer(ctx, other.ID, "approve"); err != nil {
					t.Fatal(err)
				}
				if err := r.Tick(ctx); err != nil {
					t.Fatal(readLog(home, err))
				}
				if rev(t, repo, "main") != main {
					t.Fatal("a different approved question authorized the candidate")
				}
			}
			if _, err := r.Questions(db).Answer(ctx, approval.ID, "approve"); err != nil {
				t.Fatal(err)
			}
			if marked {
				late := model.Card{ID: "late-ordinary-research", Objective: oid, Kind: model.KindResearch,
					Status: model.CardNeedsInput, Title: "real outstanding work", WaitReason: "owner review requested"}
				if err := db.CreateCard(ctx, &late); err != nil {
					t.Fatal(err)
				}
				if err := r.Tick(ctx); err != nil {
					t.Fatal(readLog(home, err))
				}
				if rev(t, repo, "main") != main {
					t.Fatal("native tick ignored late ordinary unfinished work")
				}
				late.Status, late.WaitReason = model.CardDone, ""
				if err := db.UpdateCard(ctx, &late); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(readLog(home, err))
			}
			current, err := db.GetObjective(ctx, oid)
			if err != nil {
				t.Fatal(err)
			}
			if !marked {
				if current.Status != model.ObjectiveDoneCandidate || rev(t, repo, "main") != main {
					t.Fatal("native tick changed the legacy unmarked completion behavior")
				}
				return
			}
			if current.Status != model.ObjectiveDone || rev(t, repo, "main") == main {
				t.Fatal("native tick did not handle the exact marked approval")
			}
			for i := 0; i < 2; i++ {
				if err := r.Tick(ctx); err != nil {
					t.Fatal(readLog(home, err))
				}
			}
			events, err := db.ListEvents(ctx, "objective:"+oid)
			if err != nil {
				t.Fatal(err)
			}
			merged := 0
			for _, e := range events {
				if e.Type == "merged_local" {
					merged++
				}
			}
			c, err := db.GetCard(ctx, comparison)
			if err != nil || merged != 1 || c.Status != model.CardNeedsInput {
				t.Fatalf("repeated ticks merged again or changed the disabled comparison: %d %+v %v", merged, c, err)
			}
			if attempts, err := db.ListAttempts(ctx, comparison); err != nil || len(attempts) != 0 {
				t.Fatalf("approval unexpectedly dispatched: %d %v", len(attempts), err)
			}
		})
	}
}
