package sched

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

func editStrictFixture(t *testing.T, db *store.Store, id, edit string) {
	t.Helper()
	fresh := mustCard(t, db, id)
	if edit == "cancel" {
		fresh.Status = model.CardCanceled
	} else {
		fresh.Deps = []string{"new-gate"}
	}
	if err := db.UpdateCard(t.Context(), &fresh); err != nil {
		t.Fatal(err)
	}
}

func assertStrictFixtureEdit(t *testing.T, db *store.Store, id, edit string) {
	t.Helper()
	got := mustCard(t, db, id)
	if !got.StrictDeps || (edit == "cancel" && got.Status != model.CardCanceled) ||
		(edit == "dependency" && !slices.Equal(got.Deps, []string{"new-gate"})) {
		t.Fatalf("strict admission overwrote operator edit %s: %+v", edit, got)
	}
}

func seedStrictAdmission(t *testing.T) (string, *store.Store, model.Card, model.Objective) {
	t.Helper()
	home, db, repo := newHome(t)
	_, oid := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	addClassifierChannel(t, db)
	card := model.Card{ID: "strict", Objective: oid, Kind: model.KindDev, Status: model.CardReady, StrictDeps: true, Deps: []string{"gate"}}
	for _, c := range []model.Card{
		{ID: "gate", Objective: oid, Kind: model.KindDev, Status: model.CardDone},
		{ID: "new-gate", Objective: oid, Kind: model.KindDev, Status: model.CardDone},
		card,
	} {
		if err := db.CreateCard(t.Context(), &c); err != nil {
			t.Fatal(err)
		}
	}
	obj, err := db.GetObjective(t.Context(), oid)
	if err != nil {
		t.Fatal(err)
	}
	return home, db, mustCard(t, db, card.ID), obj
}

func TestStrictTierCASDoesNotOverwriteInterleavedCard(t *testing.T) {
	for _, edit := range []string{"cancel", "dependency"} {
		t.Run(edit, func(t *testing.T) {
			home, db, observed, _ := seedStrictAdmission(t)
			r := New(home)
			r.Classify = func(context.Context, string, string) (string, error) {
				t.Fatal("strict tier resolution reached a classifier")
				return "", nil
			}
			// A writer commits between the original scheduler read and tier write.
			editStrictFixture(t, db, observed.ID, edit)
			if _, _, err := r.classifyBeforeRoute(t.Context(), db, observed); err == nil {
				t.Fatal("stale strict tier attempt was accepted")
			}
			assertStrictFixtureEdit(t, db, observed.ID, edit)
			if got := mustCard(t, db, observed.ID); got.Tier != model.TierNone {
				t.Fatalf("lost CAS still wrote tier: %+v", got)
			}
		})
	}
}

func TestStrictPreparationCASDropsOwnedCheckoutWithoutStaging(t *testing.T) {
	for _, edit := range []string{"cancel", "dependency", "newer-attempt"} {
		t.Run(edit, func(t *testing.T) {
			home, db, card, _ := seedStrictAdmission(t)
			r := New(home)
			r.Classify = func(context.Context, string, string) (string, error) {
				t.Fatal("strict preparation reached a classifier")
				return "", nil
			}
			r.Launch = func(context.Context, string, string) (int, error) {
				t.Fatal("lost strict preparation reached a host")
				return 0, nil
			}
			original := addWorktree
			t.Cleanup(func() { addWorktree = original })
			var checkout, repo, branch string
			addWorktree = func(ctx context.Context, req workspace.CreateRequest) (workspace.Worktree, error) {
				wt, err := original(ctx, req)
				if err != nil {
					return wt, err
				}
				checkout, repo, branch = wt.Path, req.RepoPath, wt.Branch
				// Barrier after Git preparation, before the attempt / Running write.
				if edit == "newer-attempt" {
					newer := model.Attempt{ID: "other-attempt", Card: card.ID, Index: 1}
					if err := db.CreateAttempt(ctx, &newer); err != nil {
						t.Fatal(err)
					}
				} else {
					editStrictFixture(t, db, card.ID, edit)
				}
				return wt, nil
			}
			if err := r.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if checkout == "" {
				t.Fatal("did not exercise the Git barrier")
			}
			if _, err := os.Stat(checkout); !os.IsNotExist(err) {
				t.Fatalf("lost preparation leaked owned worktree: %v", err)
			}
			attempts, err := db.ListAttempts(t.Context(), card.ID)
			if err != nil {
				t.Fatal(err)
			}
			if edit == "newer-attempt" {
				if !gitRefExists(t.Context(), repo, "refs/heads/"+branch) {
					t.Fatal("lost admission deleted the shared branch")
				}
				if len(attempts) != 1 || attempts[0].ID != "other-attempt" {
					t.Fatalf("another preparation or state was overwritten: %+v", attempts)
				}
			} else {
				assertStrictFixtureEdit(t, db, card.ID, edit)
				if len(attempts) != 0 {
					t.Fatalf("lost preparation created an attempt: %+v", attempts)
				}
			}
		})
	}
}

func TestStrictResumeRechecksBeforePromptOrHost(t *testing.T) {
	for _, edit := range []string{"cancel", "dependency", "newer-attempt"} {
		t.Run(edit, func(t *testing.T) {
			home, db, observed, obj := seedStrictAdmission(t)
			a := model.Attempt{ID: "old", Card: observed.ID, Index: 0, Worktree: t.TempDir(), Route: model.Route{Channel: "cha"}}
			if err := db.CreateAttempt(t.Context(), &a); err != nil {
				t.Fatal(err)
			}
			var err error
			a, err = db.GetAttempt(t.Context(), a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if edit == "newer-attempt" {
				if err := db.CreateAttempt(t.Context(), &model.Attempt{ID: "other", Card: observed.ID, Index: 1}); err != nil {
					t.Fatal(err)
				}
			} else {
				editStrictFixture(t, db, observed.ID, edit)
			}
			r := New(home)
			r.Prompt = func(context.Context, prompt.Input) (string, error) {
				t.Fatal("stale strict resume reached prompt generation")
				return "", nil
			}
			r.Launch = func(context.Context, string, string) (int, error) {
				t.Fatal("stale strict resume reached the host")
				return 0, nil
			}
			if ch, err := r.resumeAttempt(t.Context(), db, observed, obj, &a); err != nil || ch != "" {
				t.Fatalf("stale resume channel %q: %v", ch, err)
			}
			if edit == "newer-attempt" {
				if got := mustCard(t, db, observed.ID); got.Status != model.CardReady {
					t.Fatalf("stale lineage marked running: %+v", got)
				}
			} else {
				assertStrictFixtureEdit(t, db, observed.ID, edit)
			}
			if got := mustAttempt(t, db, a.ID); got.PID != 0 || got.Outcome != "" {
				t.Fatalf("stale resume altered the prepared attempt: %+v", got)
			}
		})
	}
}

func TestStrictPromptBarrierRechecksBeforeCallingHost(t *testing.T) {
	for _, promptFails := range []bool{false, true} {
		name := "prompt succeeded"
		if promptFails {
			name = "prompt failed"
		}
		t.Run(name, func(t *testing.T) {
			home, db, card, _ := seedStrictAdmission(t)
			r := New(home)
			r.Prompt = func(_ context.Context, in prompt.Input) (string, error) {
				if in.Card.ID != card.ID {
					t.Fatalf("unexpected prompt: %s", in.Card.ID)
				}
				editStrictFixture(t, db, card.ID, "cancel")
				if promptFails {
					return "", errors.New("synthetic prompt failure")
				}
				return "synthetic prompt", nil
			}
			r.Launch = func(context.Context, string, string) (int, error) {
				t.Fatal("cancellation during prompt generation still called the host")
				return 0, nil
			}
			if err := r.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertStrictFixtureEdit(t, db, card.ID, "cancel")
			attempts, err := db.ListAttempts(t.Context(), card.ID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("one authorized preparation expected: %v; %v", attempts, err)
			}
			if got := attempts[0]; got.PID != 0 || got.Exit != nil || got.Outcome != "" {
				t.Fatalf("unstarted preparation unexpectedly got a result: %v", got)
			}
			if !promptFails {
				if _, err := os.Stat(filepath.Join(home, "attempts", attempts[0].ID, "prompt.txt")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
