package sched

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

func TestStrictPreparationRejectsExistingWinnerBeforeGitMutation(t *testing.T) {
	for _, tc := range []string{"other scheduler DB attempt", "another home slot", "terminal without Git receipt", "terminal missing head", "terminal null head", "terminal error receipt", "terminal head and error", "unrecorded checkout"} {
		t.Run(tc, func(t *testing.T) {
			home, db, card, obj := seedStrictAdmission(t)
			project, err := db.GetProject(t.Context(), obj.Project)
			if err != nil {
				t.Fatal(err)
			}
			git := workspace.Git{}
			winner, err := git.Create(t.Context(), workspace.CreateRequest{
				CardID: card.ID, AttemptID: "winner", ObjectiveID: obj.ID, RepoPath: project.RepoPath,
				WorktreeRoot: project.WorktreeRoot, IntegrationBranch: obj.IntegrationBranch,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc == "another home slot" {
				// Resolves to the same Git common directory even though the
				// competing scheduler entered through a linked worktree.
				owner, err := git.ReserveStrictPreparation(t.Context(), winner.Path, "winner")
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Release()
			}
			if tc == "other scheduler DB attempt" || strings.HasPrefix(tc, "terminal ") {
				other := model.Card{ID: "winner", Objective: card.Objective, Kind: model.KindDev, Status: model.CardWaiting}
				if err := db.CreateCard(t.Context(), &other); err != nil {
					t.Fatal(err)
				}
				a := model.Attempt{ID: "winner", Card: other.ID, Branch: winner.Branch, Worktree: winner.Path,
					HeadCommit: ""}
				if strings.HasPrefix(tc, "terminal ") {
					code := 0
					a.Exit, a.EndedAt, a.Outcome = &code, time.Now().UTC(), model.OutcomePassed
				}
				if err := db.CreateAttempt(t.Context(), &a); err != nil {
					t.Fatal(err)
				}
				if payload, ok := map[string]string{
					"terminal missing head":   `{}`,
					"terminal null head":      `{"head": null}`,
					"terminal error receipt":  `{"error":"synthetic commit failure"}`,
					"terminal head and error": `{"head":"","error":"synthetic commit failure"}`,
				}[tc]; ok {
					if err := db.AppendEvent(t.Context(), &model.Event{Entity: "attempt:" + a.ID,
						Type: "workspace_commit", Data: []byte(payload)}); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Creation of this missing ref would itself be an observable Git
			// mutation, even if the subsequent checkout correctly failed.
			obj.IntegrationBranch = "strict-integration"
			if err := db.UpdateObjective(t.Context(), &obj); err != nil {
				t.Fatal(err)
			}
			obj, err = db.GetObjective(t.Context(), obj.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeTrees := gitOutput(t, project.RepoPath, "worktree", "list", "--porcelain")
			beforeRefs := gitOutput(t, project.RepoPath, "show-ref")
			orig := addWorktree
			t.Cleanup(func() { addWorktree = orig })
			addWorktree = func(context.Context, workspace.CreateRequest) (workspace.Worktree, error) {
				t.Fatal("loser reached git worktree creation")
				return workspace.Worktree{}, nil
			}
			r := New(home)
			r.Classify = func(context.Context, string, string) (string, error) {
				t.Fatal("loser reached classifier")
				return "", nil
			}
			r.Launch = func(context.Context, string, string) (int, error) {
				t.Fatal("loser reached host")
				return 0, nil
			}
			channels, err := db.ListChannels(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			pos, err := db.ListPositioning(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			channel, err := r.launchCard(t.Context(), db, card, obj, channels, pos, map[string]int{}, nil)
			switch tc {
			case "other scheduler DB attempt":
				if err != nil {
					t.Fatal(err)
				}
			case "another home slot":
				if !errors.Is(err, workspace.ErrStrictPreparationBusy) {
					t.Fatalf("missing busy disposition: %v", err)
				}
			default:
				if !errors.Is(err, workspace.ErrStrictCheckoutOwned) {
					t.Fatalf("missing checkout ownership disposition: %v", err)
				}
			}
			if channel != "" || gitOutput(t, winner.Path, "symbolic-ref", "HEAD") != "refs/heads/"+winner.Branch ||
				gitOutput(t, project.RepoPath, "worktree", "list", "--porcelain") != beforeTrees ||
				gitOutput(t, project.RepoPath, "show-ref") != beforeRefs {
				t.Fatalf("loser mutated Git state or launched on %q", channel)
			}
			rows, err := db.ListAttempts(t.Context(), card.ID)
			if err != nil || len(rows) != 0 {
				t.Fatalf("loser staged an attempt: %+v %v", rows, err)
			}
		})
	}
}

func TestStrictPreparationUsesVerifiedTerminalCheckout(t *testing.T) {
	home, db, card, obj := seedStrictAdmission(t)
	project, err := db.GetProject(t.Context(), obj.Project)
	if err != nil {
		t.Fatal(err)
	}
	git := workspace.Git{}
	previous, err := git.Create(t.Context(), workspace.CreateRequest{
		CardID: card.ID, AttemptID: "prior", ObjectiveID: obj.ID, RepoPath: project.RepoPath,
		WorktreeRoot: project.WorktreeRoot, IntegrationBranch: obj.IntegrationBranch,
	})
	if err != nil {
		t.Fatal(err)
	}
	code := 1
	a := model.Attempt{ID: "prior", Card: card.ID, Branch: previous.Branch, Worktree: previous.Path,
		HeadCommit: previous.BaseCommit, Exit: &code, EndedAt: time.Now().UTC(), Outcome: model.OutcomeTransport}
	if err := db.CreateAttempt(t.Context(), &a); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendEvent(t.Context(), &model.Event{Entity: "attempt:prior", Type: "workspace_commit",
		Data: []byte(`{"head":"` + previous.BaseCommit + `"}`)}); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	launchCount := 0
	r.Launch = func(context.Context, string, string) (int, error) {
		launchCount++
		return 0, nil
	}
	if err := r.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListAttempts(t.Context(), card.ID)
	if err != nil || len(rows) != 2 || launchCount != 1 {
		t.Fatalf("verified retry did not prepare exactly once: %+v %v; launches=%d", rows, err, launchCount)
	}
	if gitOutput(t, previous.Path, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
		t.Fatal("verified checkout remained attached")
	}
	for _, next := range rows {
		if next.ID != a.ID && gitOutput(t, next.Worktree, "symbolic-ref", "HEAD") != "refs/heads/"+previous.Branch {
			t.Fatal("prepared continuation did not own the branch")
		}
	}
}

func TestStrictCleanHostRunCanContinue(t *testing.T) {
	home, db, card, _ := seedStrictAdmission(t)
	r := New(home)
	r.Script = func(model.Card) ([]byte, bool) {
		return []byte(`{"attempts":{"0":{"exit_code":1},"1":{"exit_code":1}}}`), true
	}
	starts := 0
	r.Launch = func(ctx context.Context, home, id string) (int, error) {
		starts++
		// Real host completion and workspace.Git.Commit, synthetic executor.
		// Synchronous for the fixture so no Git subprocess races its teardown.
		return os.Getpid(), host.Run(host.WithWorkspace(ctx, workspace.Git{}), home, id)
	}
	ctx := t.Context()
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListAttempts(ctx, card.ID)
	if err != nil || starts != 1 || len(rows) != 1 {
		t.Fatalf("first host run not complete: launches=%d rows=%+v err=%v", starts, rows, err)
	}
	prior := rows[0]
	if prior.HeadCommit != "" || prior.Exit == nil || prior.EndedAt.IsZero() ||
		gitOutput(t, prior.Worktree, "status", "--porcelain") != "" {
		t.Fatalf("expected a real clean Git completion, got %+v", prior)
	}
	events, err := db.ListEvents(ctx, "attempt:"+prior.ID)
	if err != nil {
		t.Fatal(err)
	}
	var explicit bool
	for _, event := range events {
		if event.Type == "workspace_commit" {
			var receipt map[string]json.RawMessage
			explicit = json.Unmarshal(event.Data, &receipt) == nil && string(receipt["head"]) == `""`
		}
	}
	if !explicit {
		t.Fatalf("missing explicit clean Git receipt: %+v", events)
	}
	// The normal reap/disposition resumes onto the same branch in the next
	// tick. Strict gating must not turn a valid no-change host into a stall.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListAttempts(ctx, card.ID)
	if err != nil || starts != 2 || len(rows) != 2 {
		t.Fatalf("real clean completion did not continue: launches=%d rows=%+v err=%v; card=%+v", starts, rows, err, mustCard(t, db, card.ID))
	}
	if gitOutput(t, prior.Worktree, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
		t.Fatal("previous clean checkout remained attached")
	}
	for _, next := range rows {
		if next.ID == prior.ID {
			if next.Outcome == "" {
				t.Fatal("prior attempt was not settled")
			}
			continue
		}
		if gitOutput(t, next.Worktree, "symbolic-ref", "HEAD") != "refs/heads/"+prior.Branch {
			t.Fatal("continuation did not own the previous branch")
		}
	}
}

func TestStrictWorkspaceCompletionRejectsNonAttributableReceipt(t *testing.T) {
	ended := time.Now().UTC()
	a := model.Attempt{EndedAt: ended, HeadCommit: "expected"}
	event := func(ts time.Time, payload string) model.Event {
		return model.Event{TS: ts, Type: "workspace_commit", Data: []byte(payload)}
	}
	ok := event(ended.Add(time.Second), `{"head":"expected"}`)
	for _, tc := range []struct {
		name   string
		events []model.Event
		want   bool
	}{
		{"exact", []model.Event{ok}, true},
		{"preceding finish", []model.Event{event(ended.Add(-time.Second), `{"head":"expected"}`)}, false},
		{"other head", []model.Event{event(ended.Add(time.Second), `{"head":"other"}`)}, false},
		{"explicit null error", []model.Event{event(ended.Add(time.Second), `{"head":"expected","error":null}`)}, false},
		{"ambiguous", []model.Event{ok, event(ended.Add(2*time.Second), `{"head":"expected"}`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strictWorkspaceCompletion(a, tc.events); got != tc.want {
				t.Fatalf("completion=%t want=%t", got, tc.want)
			}
		})
	}
}

func TestStrictNeverSpawnRetryReleasesExactPriorCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a Unix synthetic non-starting executable")
	}
	ctx := t.Context()
	home, db, card, _ := seedStrictAdmission(t)
	bin := t.TempDir()
	// The runner resolves only this fixture. Its invalid interpreter means
	// Start must fail without running our file or a real installed CLI.
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+filepath.Join(bin, "missing-interpreter")+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	card.Channel, card.Sandbox = "cha", "off"
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	ch, err := db.GetChannel(ctx, "cha")
	if err != nil {
		t.Fatal(err)
	}
	ch.Runner = model.RunnerCodex
	if err := db.UpdateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Script = func(model.Card) ([]byte, bool) {
		return []byte(`{"attempts":{"1":{"exit_code":0,"output":"synthetic retry"}}}`), true
	}
	var launches int
	var firstErr error
	r.Launch = func(ctx context.Context, home, id string) (int, error) {
		launches++
		err := host.Run(host.WithWorkspace(ctx, workspace.Git{}), home, id)
		if launches == 1 {
			firstErr = err
		}
		// Same semantics as production: spawning the host succeeded even
		// when starting the child executor in that host did not.
		return os.Getpid(), nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListAttempts(ctx, card.ID)
	if err != nil || launches != 1 || len(rows) != 1 || firstErr == nil {
		t.Fatalf("expected one concrete spawn failure: starts=%d attempts=%+v start=%v err=%v", launches, rows, firstErr, err)
	}
	prior := rows[0]
	if prior.Exit == nil || *prior.Exit != 127 || prior.PID == 0 || prior.EndedAt.IsZero() {
		t.Fatalf("failed start did not create expected prepared-owned receipt: %+v", prior)
	}
	events, err := db.ListEvents(ctx, "attempt:"+prior.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Type == "executor_pgid" || e.Type == "workspace_commit" {
			t.Fatalf("fixture unexpectedly started executor or committed: %+v", e)
		}
	}
	ch.Runner = model.RunnerFake
	if err := db.UpdateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListAttempts(ctx, card.ID)
	if err != nil || launches != 2 || len(rows) != 2 {
		t.Fatalf("exact no-spawn checkout stalled retry: starts=%d attempts=%+v err=%v; card=%+v", launches, rows, err, mustCard(t, db, card.ID))
	}
	if gitOutput(t, prior.Worktree, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
		t.Fatal("previous checkout was not detached safely")
	}
}

func TestStrictNoExecutorCompletionRequiresExactProvenReceipt(t *testing.T) {
	now, exit := time.Now().UTC(), 127
	a := model.Attempt{ID: "a", Card: "c", Worktree: "/synthetic/w/c-a", Branch: "cardex/o/c/c",
		BaseCommit: "synthetic", StartedAt: now, EndedAt: now.Add(time.Second), PID: 17, Exit: &exit, Outcome: model.OutcomeNoOutput}
	event := func(kind, id string) model.Event {
		return model.Event{Type: kind, Entity: "attempt:" + a.ID, TS: a.EndedAt, Data: []byte(`{"checkout_id":"` + id + `"}`)}
	}
	good := event(model.StrictNoExecutorEvent, a.StrictCheckoutID())
	for _, tt := range []struct {
		name   string
		events []model.Event
		want   bool
	}{
		{"actual no executor receipt", []model.Event{good}, true},
		{"generic failure after process group could be persisted", []model.Event{{Type: "host_finish", Entity: good.Entity, TS: good.TS,
			Data: []byte(`{"exit":127,"started":false}`)}}, false},
		{"wrong owner", []model.Event{event(model.StrictNoExecutorEvent, "another-checkout")}, false},
		{"duplicate claim", []model.Event{good, good}, false},
		{"positive process evidence", []model.Event{good, event("executor_pgid", "")}, false},
		{"Git completion present", []model.Event{good, event("workspace_commit", "")}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := strictNoExecutorCompletion(a, tt.events); got != tt.want {
				t.Fatalf("completion=%t want=%t", got, tt.want)
			}
		})
	}
}
