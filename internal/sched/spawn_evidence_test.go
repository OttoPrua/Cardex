package sched

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestSettledLegacyDispatchRequiresActualExecutor(t *testing.T) {
	for _, serve := range []bool{false, true} {
		name := "asynchronous-no-executor"
		if serve {
			name = "synchronous-executor-completed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			home, db, repo := newHome(t)
			_, oid := seedRepo(t, db, repo, "true")
			ch := model.Channel{ID: "fake-fixture", Runner: model.RunnerFake, Health: model.HealthOK, Models: []string{"fake"}}
			if err := db.CreateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			card := model.Card{ID: "fixture", Objective: oid, Kind: model.KindDev, Status: model.CardRunning}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, "attempts", "fixture-attempt")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			a := model.Attempt{ID: "fixture-attempt", Card: card.ID, Route: model.Route{Channel: ch.ID, Model: "fake"}, Worktree: repo, PromptPath: filepath.Join(dir, "prompt.txt")}
			if err := os.WriteFile(filepath.Join(dir, "hard-duration"), []byte("8s"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "script.json"), []byte(`{"attempts":{"0":{"output":"fixture completed","exit_code":0}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if serve {
				if err := os.WriteFile(a.PromptPath, []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.CreateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			var hostErr error
			r.Launch = func(ctx context.Context, home, id string) (int, error) {
				if serve {
					hostErr = host.Run(ctx, home, id)
				} else {
					// Model a detached launcher: successfully spawn the host,
					// but don't forward the later internal executor error.
					finished := make(chan error, 1)
					go func() { finished <- host.Run(ctx, home, id) }()
					hostErr = <-finished // deterministically observe its terminal row.
				}
				return 42, nil
			}
			started, err := r.startAttempt(ctx, db, &a, false)
			if err != nil || started != serve || (hostErr == nil) != serve {
				t.Fatalf("started=%t want=%t scheduler=%v host=%v", started, serve, err, hostErr)
			}
			aa, err := db.GetAttempt(ctx, a.ID)
			if err != nil || aa.PID == 0 || aa.EndedAt.IsZero() || aa.Exit == nil || (!serve && *aa.Exit != 127) || (serve && *aa.Exit != 0) {
				t.Fatalf("attempt: %+v %v", aa, err)
			}
			ev, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil {
				t.Fatal(err)
			}
			var group, noStart bool
			for _, e := range ev {
				group = group || e.Type == "executor_pgid"
				noStart = noStart || (e.Type == "host_finish" && bytes.Contains(e.Data, []byte(`"started":false`)))
			}
			if group != serve || noStart == serve {
				t.Fatalf("group=%t no-start=%t want real child=%t", group, noStart, serve)
			}
		})
	}
}
