package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cardex/internal/board"
	"cardex/internal/claims"
	"cardex/internal/classify"
	"cardex/internal/harvest"
	"cardex/internal/host"
	"cardex/internal/importer"
	"cardex/internal/loop"
	"cardex/internal/merge"
	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/positioning"
	"cardex/internal/prompt"
	"cardex/internal/pty"
	"cardex/internal/question"
	"cardex/internal/quota"
	"cardex/internal/route"
	"cardex/internal/runner"
	"cardex/internal/sandbox"
	"cardex/internal/sched"
	"cardex/internal/scorecard"
	"cardex/internal/workspace"
)

func TestStubsReturnErrNotImplemented(t *testing.T) {
	ctx := context.Background()
	checks := []struct {
		err      error
		sentinel error
	}{
		{host.Run("a"), host.ErrNotImplemented},
		{host.Reconcile(ctx, t.TempDir()), host.ErrNotImplemented},
		{errOnly(pty.Start(nil)), pty.ErrNotImplemented},
		{errOnly(sandbox.Wrap(nil, nil)), sandbox.ErrNotImplemented},
		{errOnly(workspace.Stub{}.Create(ctx, workspace.CreateRequest{})), workspace.ErrNotImplemented},
		{errOnly(workspace.Stub{}.Commit(ctx, "", "")), workspace.ErrNotImplemented},
		{errOnly(workspace.Stub{}.Diff(ctx, "", "")), workspace.ErrNotImplemented},
		{workspace.Stub{}.Remove(ctx, ""), workspace.ErrNotImplemented},
		{workspace.Stub{}.BlockPush(ctx, ""), workspace.ErrNotImplemented},
		{errOnly(claims.Conflicts(nil)), claims.ErrNotImplemented},
		{errOnly(claims.ChangedSymbols("", "", "")), claims.ErrNotImplemented},
		{errOnly(harvest.Harvest(ctx, harvest.Input{})), harvest.ErrNotImplemented},
		{errOnly3(route.Select(ctx, route.Request{})), route.ErrNotImplemented},
		{errOnly(quota.Stub{}.Remaining(ctx, "c")), quota.ErrNotImplemented},
		{quota.Stub{}.Record(ctx, "c", model.Usage{}, time.Time{}, time.Time{}), quota.ErrNotImplemented},
		{quota.Stub{}.ObserveLimit(ctx, "c", time.Time{}), quota.ErrNotImplemented},
		{errOnly(classify.Classify(ctx, model.Card{})), classify.ErrNotImplemented},
		{errOnly(positioning.Stub{}.Current(ctx)), positioning.ErrNotImplemented},
		{errOnly(positioning.Stub{}.Propose(ctx, nil)), positioning.ErrNotImplemented},
		{positioning.Stub{}.Approve(ctx, "p"), positioning.ErrNotImplemented},
		{positioning.Stub{}.Reject(ctx, "p"), positioning.ErrNotImplemented},
		{loop.Stub{}.Step(ctx, "o"), loop.ErrNotImplemented},
		{loop.DesignStep(ctx, "o"), loop.ErrNotImplemented},
		{loop.DevStep(ctx, "o"), loop.ErrNotImplemented},
		{errOnly(question.Stub{}.Create(ctx, model.Question{})), question.ErrNotImplemented},
		{errOnly(question.Stub{}.Answer(ctx, "q", "yes")), question.ErrNotImplemented},
		{errOnly(question.Stub{}.Pending(ctx, "n")), question.ErrNotImplemented},
		{errOnly(question.Stub{}.Blocked(ctx, "n")), question.ErrNotImplemented},
		{merge.Stub{}.Enqueue(ctx, "c"), merge.ErrNotImplemented},
		{merge.Stub{}.Step(ctx, "o"), merge.ErrNotImplemented},
		{sched.Stub{}.Tick(ctx), sched.ErrNotImplemented},
		{sched.Serve(ctx, t.TempDir(), "127.0.0.1:0"), sched.ErrNotImplemented},
		{errOnly(prompt.Build(ctx, prompt.Input{})), prompt.ErrNotImplemented},
		{notify.Stub{}.Deliver(ctx, notify.Message{}), notify.ErrNotImplemented},
		{errOnly(importer.Import(ctx, t.TempDir(), true)), importer.ErrNotImplemented},
		{errOnly(scorecard.Compute(ctx, t.TempDir())), scorecard.ErrNotImplemented},
		{errOnly3(runner.Stub{}.Build(runner.LaunchSpec{})), runner.ErrNotImplemented},
		{runner.Stub{}.Drive(ctx, nil), runner.ErrNotImplemented},
	}
	for i, check := range checks {
		if !errors.Is(check.err, check.sentinel) {
			t.Fatalf("check %d: %v", i, check.err)
		}
	}
	rec := httptest.NewRecorder()
	board.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), board.ErrNotImplemented.Error()) {
		t.Fatalf("board %d %s", rec.Code, rec.Body.String())
	}
}

func errOnly[T any](_ T, err error) error { return err }

func errOnly3[A, B any](_ A, _ B, err error) error { return err }
