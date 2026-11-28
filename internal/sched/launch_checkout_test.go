package sched

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestOrdinaryLaunchCleanupRetainsReboundCheckoutAndSharedRef(t *testing.T) {
	for _, mode := range []string{"exact-owned", "same-tip-path-rebound", "zero-backoff-retry", "assume-unchanged", "skip-worktree"} {
		t.Run(mode, func(t *testing.T) {
			home, db, repo := newHome(t)
			parent, err := filepath.EvalSymlinks(filepath.Dir(repo))
			if err != nil {
				t.Fatal(err)
			}
			repo = filepath.Join(parent, filepath.Base(repo))
			_, objective := seedRepo(t, db, repo, "true")
			seedRoutes(t, db)
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"schedules":[],"loop":{"retry_backoff_seconds":0}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			card := model.Card{ID: "c", Objective: objective, Kind: model.KindDev, Title: "fixture", Tier: model.TierT2,
				Status: model.CardReady, Channel: "cha", Model: "model-a"}
			if err := db.CreateCard(t.Context(), &card); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			var before model.Attempt
			var retryPath string
			var intervened bool
			r.Launch = func(ctx context.Context, _, id string) (int, error) {
				if before.ID != "" {
					return 1987, nil // only the first scheduler operation fails
				}
				before, err = db.GetAttempt(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				return 0, errors.New("fixture host did not start")
			}
			ctx := context.WithValue(t.Context(), serveEventKey{}, &serveReporter{fault: func(serveFault) {}, event: func(event string) {
				if event != "transition state=launch_backoff" || intervened {
					return
				}
				intervened = true
				got, err := db.GetCard(t.Context(), card.ID)
				if err != nil || got.Status != model.CardReady {
					t.Fatalf("expected zero-backoff ready before cleanup: %+v %v", got, err)
				}
				switch mode {
				case "same-tip-path-rebound":
					git(t, repo, "worktree", "remove", before.Worktree)
					git(t, repo, "worktree", "add", before.Worktree, before.Branch)
				case "assume-unchanged", "skip-worktree":
					git(t, before.Worktree, "update-index", "--"+mode, "a.txt")
					if err := os.WriteFile(filepath.Join(before.Worktree, "a.txt"), []byte("hidden operator bytes\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					if status := gitOutput(t, before.Worktree, "status", "--porcelain=v1"); status != "" {
						t.Fatalf("fixture must hide the dirty tracked file from Git status: %q", status)
					}
				case "zero-backoff-retry":
					retryPath = filepath.Join(filepath.Dir(before.Worktree), "fixture-new-owner")
					git(t, before.Worktree, "checkout", "--detach")
					git(t, repo, "worktree", "add", retryPath, before.Branch)
					a := model.Attempt{ID: "a-new-owner", Card: card.ID, Index: before.Index + 1, Worktree: retryPath,
						Branch: before.Branch, BaseCommit: before.BaseCommit, Route: before.Route}
					if err := db.CreateAttempt(t.Context(), &a); err != nil {
						t.Fatal(err)
					}
				}
			}})
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if !intervened || before.Worktree == "" || before.Branch == "" {
				t.Fatalf("test missed failed disposition: %+v", before)
			}
			git(t, repo, "show-ref", "--verify", "refs/heads/"+before.Branch)
			_, err = os.Stat(before.Worktree)
			if mode == "exact-owned" {
				if !os.IsNotExist(err) {
					t.Fatalf("owned clean checkout not released: %v", err)
				}
			} else if err != nil {
				t.Fatalf("lost/rebound checkout was removed: %v", err)
			}
			if mode == "assume-unchanged" || mode == "skip-worktree" {
				raw, err := os.ReadFile(filepath.Join(before.Worktree, "a.txt"))
				if err != nil || string(raw) != "hidden operator bytes\n" {
					t.Fatalf("hidden file was not retained: %q %v", raw, err)
				}
			}
			if retryPath != "" {
				if _, err := os.Stat(retryPath); err != nil {
					t.Fatalf("retry checkout lost: %v", err)
				}
			}
		})
	}
}
