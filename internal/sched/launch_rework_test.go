package sched

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

func TestLaunchFailedRouteReused(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		health model.Health
		step   string
	}{
		{name: "worktree", mode: "worktree", step: "worktree"},
		{name: "prompt", mode: "prompt", step: "prompt"},
		{name: "host", mode: "host", step: "host"},
		{name: "fill", mode: "fill", step: "host"},
		{name: "fill other channel", mode: "fill-other", step: "host"},
		{name: "channel missing", mode: "missing", step: "host"},
		{name: "health auth_failed", mode: "health", health: model.HealthAuthFailed, step: "host"},
		{name: "health degraded", mode: "health", health: model.HealthDegraded, step: "host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			writeEmptySchedules(t, home)
			_, objID := seedRepo(t, db, repo, "true")
			seedRoutes(t, db)
			now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
			reset := now.Add(30 * time.Minute)
			kind := model.KindDev
			tier := model.TierT2
			if tc.mode == "fill" || tc.mode == "fill-other" {
				kind = model.KindBacklog
				tier = model.TierT1
				setFillWindow(t, db, "cha", reset, true)
			}
			card := model.Card{
				ID: "c", Objective: objID, Kind: kind, Title: "c", Tier: tier, Status: model.CardReady,
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			var hosts int
			r := New(home)
			r.Now = func() time.Time { return now }
			if tc.mode == "prompt" {
				failed := true
				r.Prompt = func(context.Context, prompt.Input) (string, error) {
					if failed {
						failed = false
						return "", errors.New("prompt down")
					}
					return "PROMPT-OK", nil
				}
			}
			r.Launch = func(context.Context, string, string) (int, error) {
				hosts++
				if hosts == 1 && tc.mode != "worktree" && tc.mode != "prompt" {
					return 0, errors.New("host down")
				}
				return 84, nil
			}
			var root string
			if tc.mode == "worktree" {
				root = blockWorktree(t, db, "p")
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			failed := firstLaunchFailed(t, db, "c")
			if failed.Step != tc.step || failed.Channel != "cha" || failed.Model != "model-a" {
				t.Fatalf("launch_failed %+v", failed)
			}
			attempts, err := db.ListAttempts(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			if tc.mode == "worktree" {
				if len(attempts) != 0 {
					t.Fatalf("worktree failure stored %+v", attempts)
				}
				if disposedAttempt(t, db, "c") {
					t.Fatal("worktree failure wrote dev_disposed")
				}
				restoreWorktree(t, db, "p", root)
			} else if len(attempts) != 1 || attempts[0].Outcome != model.OutcomeLaunchFailed {
				t.Fatalf("first attempt %+v", attempts)
			}
			switch tc.mode {
			case "worktree", "prompt", "host", "fill":
				preferChb(t, db)
				occupyChannel(t, db, objID, "cha", now)
				if tc.mode == "fill" {
					setFillWindow(t, db, "chb", reset, true)
				}
			case "fill-other":
				setFillWindow(t, db, "cha", reset, false)
				setFillWindow(t, db, "chb", reset, true)
			case "missing":
				if err := db.DeleteChannel(ctx, "cha"); err != nil {
					t.Fatal(err)
				}
			case "health":
				setHealth(t, db, "cha", tc.health)
			}
			heldHosts := hosts
			now = now.Add(31 * time.Second)
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := db.GetCard(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			if got.Channel != "" || got.Model != "" {
				t.Fatalf("route stored on card %+v", got)
			}
			obj, err := db.GetObjective(ctx, objID)
			if err != nil {
				t.Fatal(err)
			}
			if obj.Channel != "" || obj.Model != "" {
				t.Fatalf("route stored on objective %+v", obj)
			}
			switch tc.mode {
			case "worktree", "prompt", "host", "fill":
				if got.Status != model.CardReady {
					t.Fatalf("full status %+v", got)
				}
				waiting, err := db.ListAttempts(ctx, "c")
				if err != nil {
					t.Fatal(err)
				}
				wantAttempts := 1
				if tc.mode == "worktree" {
					wantAttempts = 0
				}
				if len(waiting) != wantAttempts {
					t.Fatalf("attempts while full %+v", waiting)
				}
				if drop := routeDroppedText(t, db, "c"); drop != "" {
					t.Fatalf("dropped while full %s", drop)
				}
				if hosts != heldHosts {
					t.Fatalf("hosts %d while full, was %d", hosts, heldHosts)
				}
				if n := openRoutes(t, db, "cha"); n != 1 {
					t.Fatalf("occupancy while full %d", n)
				}
				releaseOccupy(t, db)
				now = now.Add(time.Second)
				if err := r.Tick(ctx); err != nil {
					t.Fatal(err)
				}
				got, err = db.GetCard(ctx, "c")
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != model.CardRunning || got.Channel != "" || got.Model != "" {
					t.Fatalf("retry stored %+v", got)
				}
				obj, err = db.GetObjective(ctx, objID)
				if err != nil {
					t.Fatal(err)
				}
				if obj.Channel != "" || obj.Model != "" {
					t.Fatalf("route stored on objective %+v", obj)
				}
				started := startedAttempt(t, db, "c")
				if started.Route.Channel != "cha" || started.Route.Model != "model-a" {
					t.Fatalf("reused route %+v", started.Route)
				}
				if n := openRoutes(t, db, "cha"); n != 1 {
					t.Fatalf("occupancy after start %d", n)
				}
			case "fill-other":
				if got.Status != model.CardReady {
					t.Fatalf("fill switched or held %+v", got)
				}
				if hosts != 1 {
					t.Fatalf("hosts %d", hosts)
				}
				again, err := db.ListAttempts(ctx, "c")
				if err != nil || len(again) != 1 || again[0].Outcome != model.OutcomeLaunchFailed || again[0].Route.Channel != "cha" {
					t.Fatalf("attempts %+v %v", again, err)
				}
				if drop := routeDroppedText(t, db, "c"); drop != "" {
					t.Fatalf("dropped out of window %s", drop)
				}
			case "missing", "health":
				if got.Status != model.CardRunning {
					t.Fatalf("fallback status %+v", got)
				}
				started := startedAttempt(t, db, "c")
				if started.Route.Channel != "chb" || started.Route.Model != "model-b" {
					t.Fatalf("fallback route %+v", started.Route)
				}
				want := "saved route dropped: channel cha is missing"
				if tc.mode == "health" {
					want = "saved route dropped: channel cha health is " + string(tc.health)
				}
				next := eventAfter(t, db, "c", failed.ID)
				if !strings.Contains(string(next.Data), want) {
					t.Fatalf("next event %s %s", next.Type, next.Data)
				}
			}
		})
	}
}

func TestLaunchFailedModelOnlyKeepsRoute(t *testing.T) {
	cases := []struct {
		name   string
		pin    string
		mode   string
		health model.Health
	}{
		{name: "card model dispatch", pin: "card", mode: "reuse"},
		{name: "objective model dispatch", pin: "objective", mode: "reuse"},
		{name: "card model fill keeps saved", pin: "card", mode: "fill-in"},
		{name: "objective model fill keeps saved", pin: "objective", mode: "fill-in"},
		{name: "card model dispatch full", pin: "card", mode: "full"},
		{name: "objective model dispatch full", pin: "objective", mode: "full"},
		{name: "card model fill full", pin: "card", mode: "fill-full"},
		{name: "objective model fill full", pin: "objective", mode: "fill-full"},
		{name: "card model fill other window", pin: "card", mode: "fill-other"},
		{name: "objective model fill other window", pin: "objective", mode: "fill-other"},
		{name: "card model channel missing", pin: "card", mode: "missing"},
		{name: "objective model channel missing", pin: "objective", mode: "missing"},
		{name: "card model health", pin: "card", mode: "health", health: model.HealthAuthFailed},
		{name: "objective model health", pin: "objective", mode: "health", health: model.HealthDegraded},
		{name: "card model M objective channel z dispatch", pin: "override", mode: "reuse"},
		{name: "card model M objective channel z fill", pin: "override", mode: "fill-in"},
		{name: "card model M objective channel z dispatch full", pin: "override", mode: "full"},
		{name: "card model M objective channel z fill full", pin: "override", mode: "fill-full"},
		{name: "card model M objective channel z fill other window", pin: "override", mode: "fill-other"},
		{name: "card channel pin", pin: "card-channel", mode: "pin"},
		{name: "objective channel pin", pin: "obj-channel", mode: "pin"},
		{name: "card channel pin full", pin: "card-channel", mode: "pin-full"},
		{name: "objective channel pin full", pin: "obj-channel", mode: "pin-full"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			writeEmptySchedules(t, home)
			_, objID := seedRepo(t, db, repo, "true")
			seedRoutes(t, db)
			now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
			reset := now.Add(30 * time.Minute)
			fill := tc.mode == "fill-in" || tc.mode == "fill-full" || tc.mode == "fill-other"
			kind := model.KindDev
			tier := model.TierT2
			if fill {
				kind = model.KindBacklog
				tier = model.TierT1
			}
			cardModel := ""
			switch tc.pin {
			case "card":
				cardModel = "model-b"
			case "override":
				cardModel = "M"
			}
			card := model.Card{
				ID: "c", Objective: objID, Kind: kind, Title: "c", Tier: tier,
				Status: model.CardReady, Model: cardModel,
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			if tc.pin == "objective" || tc.pin == "override" {
				obj, err := db.GetObjective(ctx, objID)
				if err != nil {
					t.Fatal(err)
				}
				if tc.pin == "objective" {
					obj.Model = "model-b"
				}
				if tc.pin == "override" {
					obj.Channel = "z"
				}
				if err := db.UpdateObjective(ctx, &obj); err != nil {
					t.Fatal(err)
				}
			}
			savedCh, savedModel := "chb", "model-b"
			if tc.pin == "card-channel" || tc.pin == "obj-channel" {
				savedCh, savedModel = "cha", "model-a"
			}
			if tc.pin == "override" {
				savedCh, savedModel = "b", "M"
				addCarrier(t, db, savedCh, savedModel)
			}
			if fill {
				setFillWindow(t, db, savedCh, reset, true)
			}
			var hosts int
			r := New(home)
			r.Now = func() time.Time { return now }
			r.Launch = func(context.Context, string, string) (int, error) {
				hosts++
				if hosts == 1 {
					return 0, errors.New("host down")
				}
				return 84, nil
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			failed := firstLaunchFailed(t, db, "c")
			if failed.Step != "host" || failed.Channel != savedCh || failed.Model != savedModel {
				t.Fatalf("launch_failed %+v", failed)
			}
			switch tc.mode {
			case "reuse", "fill-in", "full", "fill-full", "fill-other", "missing", "health":
				addCarrier(t, db, "a", savedModel)
				assertEarlierCarrier(t, db, "a", savedCh, savedModel)
			}
			switch tc.mode {
			case "fill-in":
				setFillWindow(t, db, "a", reset, true)
			case "fill-full":
				setFillWindow(t, db, "a", reset, true)
				setConcurrency(t, db, savedCh, 1)
				occupyChannel(t, db, objID, savedCh, now)
			case "fill-other":
				setFillWindow(t, db, savedCh, reset, false)
				setFillWindow(t, db, "a", reset, true)
			case "full":
				setConcurrency(t, db, savedCh, 1)
				occupyChannel(t, db, objID, savedCh, now)
			case "missing":
				if err := db.DeleteChannel(ctx, savedCh); err != nil {
					t.Fatal(err)
				}
			case "health":
				setHealth(t, db, savedCh, tc.health)
			case "pin", "pin-full":
				pinTo := "chb"
				if tc.mode == "pin-full" {
					pinTo = savedCh
					setConcurrency(t, db, savedCh, 1)
					occupyChannel(t, db, objID, savedCh, now)
				}
				if tc.pin == "card-channel" {
					got, err := db.GetCard(ctx, "c")
					if err != nil {
						t.Fatal(err)
					}
					got.Channel = pinTo
					if err := db.UpdateCard(ctx, &got); err != nil {
						t.Fatal(err)
					}
				} else {
					obj, err := db.GetObjective(ctx, objID)
					if err != nil {
						t.Fatal(err)
					}
					obj.Channel = pinTo
					if err := db.UpdateObjective(ctx, &obj); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.mode == "fill-other" && (windowOn(t, db, savedCh) || !windowOn(t, db, "a")) {
				t.Fatal("fill windows were not split")
			}
			if (tc.mode == "fill-in" || tc.mode == "fill-full") && (!windowOn(t, db, savedCh) || !windowOn(t, db, "a")) {
				t.Fatal("saved and earlier channels are not both in the fill window")
			}
			heldHosts := hosts
			now = now.Add(31 * time.Second)
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			wantCh, wantModel := "", ""
			switch tc.pin {
			case "card":
				wantModel = "model-b"
			case "override":
				wantModel = "M"
			case "objective":
				// Stored on the objective, checked below.
			case "card-channel":
				wantCh = "chb"
				if tc.mode == "pin-full" {
					wantCh = savedCh
				}
			case "obj-channel":
				// Stored on the objective, checked below.
			}
			assertCardPin := func() {
				t.Helper()
				got, err := db.GetCard(ctx, "c")
				if err != nil {
					t.Fatal(err)
				}
				if got.Channel != wantCh || got.Model != wantModel {
					t.Fatalf("card pin %+v", got)
				}
				obj, err := db.GetObjective(ctx, objID)
				if err != nil {
					t.Fatal(err)
				}
				objCh, objModel := "", ""
				if tc.pin == "objective" {
					objModel = "model-b"
				}
				if tc.pin == "override" {
					objCh = "z"
				}
				if tc.pin == "obj-channel" {
					objCh = "chb"
					if tc.mode == "pin-full" {
						objCh = savedCh
					}
				}
				if obj.Channel != objCh || obj.Model != objModel {
					t.Fatalf("objective pin %+v", obj)
				}
			}
			assertCardPin()
			switch tc.mode {
			case "full", "fill-full", "fill-other":
				got, err := db.GetCard(ctx, "c")
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != model.CardReady {
					t.Fatalf("status while held %+v", got)
				}
				again, err := db.ListAttempts(ctx, "c")
				if err != nil || len(again) != 1 || again[0].Outcome != model.OutcomeLaunchFailed || again[0].Route.Channel != savedCh {
					t.Fatalf("attempts while held %+v %v", again, err)
				}
				if drop := routeDroppedText(t, db, "c"); drop != "" {
					t.Fatalf("dropped while held %s", drop)
				}
				if hosts != heldHosts {
					t.Fatalf("hosts %d while held", hosts)
				}
				if tc.mode == "fill-other" {
					return
				}
				if n := openRoutes(t, db, savedCh); n != 1 {
					t.Fatalf("occupancy while full %d", n)
				}
				releaseOccupy(t, db)
				now = now.Add(time.Second)
				if err := r.Tick(ctx); err != nil {
					t.Fatal(err)
				}
				assertCardPin()
				got, err = db.GetCard(ctx, "c")
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != model.CardRunning {
					t.Fatalf("status after release %+v", got)
				}
				started := startedAttempt(t, db, "c")
				if started.Route.Channel != failed.Channel || started.Route.Model != failed.Model {
					t.Fatalf("reused %+v saved %s/%s", started.Route, failed.Channel, failed.Model)
				}
				if n := openRoutes(t, db, savedCh); n != 1 {
					t.Fatalf("occupancy after start %d", n)
				}
			case "reuse", "fill-in":
				got, err := db.GetCard(ctx, "c")
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != model.CardRunning {
					t.Fatalf("status %+v", got)
				}
				started := startedAttempt(t, db, "c")
				if started.Route.Channel != failed.Channel || started.Route.Model != failed.Model {
					t.Fatalf("reused %+v saved %s/%s", started.Route, failed.Channel, failed.Model)
				}
				if drop := routeDroppedText(t, db, "c"); drop != "" {
					t.Fatalf("dropped on reuse %s", drop)
				}
			case "missing", "health":
				got, err := db.GetCard(ctx, "c")
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != model.CardRunning {
					t.Fatalf("fallback status %+v", got)
				}
				started := startedAttempt(t, db, "c")
				if started.Route.Channel != "a" || started.Route.Model != savedModel {
					t.Fatalf("fallback route %+v", started.Route)
				}
				want := "saved route dropped: channel " + savedCh + " is missing"
				if tc.mode == "health" {
					want = "saved route dropped: channel " + savedCh + " health is " + string(tc.health)
				}
				next := eventAfter(t, db, "c", failed.ID)
				if !strings.Contains(string(next.Data), want) {
					t.Fatalf("next event %s %s", next.Type, next.Data)
				}
			case "pin", "pin-full":
				got, err := db.GetCard(ctx, "c")
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != model.CardRunning {
					t.Fatalf("pin status %+v", got)
				}
				started := startedAttempt(t, db, "c")
				pinCh := "chb"
				pinModel := "model-b"
				if tc.mode == "pin-full" {
					pinCh = savedCh
					pinModel = savedModel
					if n := openRoutes(t, db, savedCh); n != 2 {
						t.Fatalf("explicit pin occupancy %d", n)
					}
				}
				if started.Route.Channel != pinCh || started.Route.Model != pinModel {
					t.Fatalf("pin route %+v", started.Route)
				}
				if drop := routeDroppedText(t, db, "c"); drop != "" {
					t.Fatalf("dropped on pin %s", drop)
				}
			}
		})
	}
}

func TestAutoNormalizeLaunchFailureRetries(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{\"schedules\":[],\"loop\":{\"retry_backoff_seconds\":3600}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	obj.Mode = model.ModeAuto
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	seedRoutes(t, db)
	parent := model.Card{
		ID: "parent", Objective: objID, Kind: model.KindDev, Title: "parent",
		Tier: model.TierT2, Status: model.CardWaiting, WaitReason: "normalize:norm",
	}
	norm := model.Card{
		ID: "norm", Objective: objID, Kind: model.KindNormalize, Title: "norm",
		Tier: model.TierT1, Status: model.CardReady, Deps: []string{"parent"},
	}
	if err := db.CreateCard(ctx, &parent); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &norm); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	now := start
	var launches []string
	r := New(home)
	r.Now = func() time.Time { return now }
	r.Launch = func(_ context.Context, _ string, id string) (int, error) {
		launches = append(launches, id)
		if len(launches) == 1 {
			return 0, errors.New("host down")
		}
		return 84, nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	assertNormalizeHeld(t, db, objID)
	if len(launches) != 1 {
		t.Fatalf("launches %+v", launches)
	}
	attempt := failedAttempt(t, db, "norm")
	if launches[0] != attempt.ID {
		t.Fatalf("launch %s attempt %s", launches[0], attempt.ID)
	}
	assertDisposed(t, db, attempt)
	now = start.Add(time.Second)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	assertNormalizeHeld(t, db, objID)
	if len(launches) != 1 {
		t.Fatalf("early retry %+v", launches)
	}
	waiting, err := db.GetCard(ctx, "norm")
	if err != nil || waiting.Status != model.CardWaiting || !strings.HasPrefix(waiting.WaitReason, "backoff:") {
		t.Fatalf("before due %+v %v", waiting, err)
	}
	now = start.Add(3601 * time.Second)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	assertNormalizeHeld(t, db, objID)
	if len(launches) != 2 {
		t.Fatalf("due launches %+v", launches)
	}
	got, err := db.GetCard(ctx, "norm")
	if err != nil || got.Status != model.CardRunning {
		t.Fatalf("retried %+v %v", got, err)
	}
	if launches[1] == attempt.ID {
		t.Fatal("due tick reused the failed attempt id")
	}
}

func TestServeLoopLogErrorContinues(t *testing.T) {
	cases := []struct {
		name string
		want string
		prep func(t *testing.T, home string)
	}{
		{
			name: "directory",
			want: "is a directory",
			prep: func(t *testing.T, home string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(home, "serve-loop.log"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "not writable",
			want: "permission denied",
			prep: func(t *testing.T, home string) {
				t.Helper()
				path := filepath.Join(home, "serve-loop.log")
				if err := os.WriteFile(path, []byte("untouched\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o444); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeEmptySchedules(t, home)
			tc.prep(t, home)
			stderr := captureStderr(t)
			var log lockedBuf
			serveCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errCh := make(chan error, 1)
			go func() {
				errCh <- Serve(WithLog(serveCtx, &log), home, "127.0.0.1:0")
			}()
			deadline := time.Now().Add(8 * time.Second)
			for strings.Count(log.String(), "serve loop iteration ") < 2 {
				select {
				case err := <-errCh:
					cancel()
					t.Fatalf("serve exited: %v stderr %q log %q", err, stderr(), log.String())
				default:
				}
				if time.Now().After(deadline) {
					cancel()
					t.Fatalf("log %q stderr %q", log.String(), stderr())
				}
				time.Sleep(20 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("serve did not return")
			}
			text := stderr()
			want := tc.want
			if runtime.GOOS == "windows" && tc.name == "not writable" {
				want = "Access is denied"
			}
			if !strings.Contains(text, "serve-loop.log") || !strings.Contains(text, want) {
				t.Fatalf("stderr %q", text)
			}
			info, err := os.Stat(filepath.Join(home, "serve-loop.log"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "directory" && !info.IsDir() {
				t.Fatal("serve-loop.log is no longer a directory")
			}
			if tc.name == "not writable" {
				body, err := os.ReadFile(filepath.Join(home, "serve-loop.log"))
				if err != nil {
					t.Fatal(err)
				}
				if string(body) != "untouched\n" {
					t.Fatalf("log file %q", body)
				}
			}
		})
	}
}

func TestLaunchBackoffWriteRollsBack(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, objID := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	card := model.Card{
		ID: "c", Objective: objID, Kind: model.KindDev, Title: "c",
		Tier: model.TierT2, Status: model.CardReady, Channel: "cha", Model: "model-a",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	raw := openRawDB(t, home)
	if _, err := raw.Exec(`CREATE TRIGGER abort_card_backoff BEFORE UPDATE ON cards WHEN NEW.status = 'waiting' AND NEW.wait_reason LIKE 'backoff:%' BEGIN SELECT RAISE(ABORT, 'card backoff aborted'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) {
		return 0, errors.New("host down")
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCard(ctx, "c")
	if err != nil || got.Status != model.CardRunning {
		t.Fatalf("after abort %+v %v", got, err)
	}
	attempts, err := db.ListAttempts(ctx, "c")
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != "" || !attempts[0].EndedAt.IsZero() || attempts[0].Exit != nil {
		t.Fatalf("attempt %+v %v", attempts, err)
	}
	events, err := db.ListEvents(ctx, "card:c")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == model.EventLaunchFailed {
			t.Fatalf("committed launch_failed %+v", ev)
		}
	}
	if disposed := listDisposed(t, db, attempts[0].ID); disposed {
		t.Fatal("committed dev_disposed")
	}
	raw = openRawDB(t, home)
	if _, err := raw.Exec(`DROP TRIGGER abort_card_backoff`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetCard(ctx, "c")
	if err != nil || got.Status == model.CardRunning || !strings.HasPrefix(got.WaitReason, "backoff:") {
		t.Fatalf("recovered %+v %v", got, err)
	}
	attempts, err = db.ListAttempts(ctx, "c")
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != model.OutcomeLaunchFailed {
		t.Fatalf("recovered attempt %+v %v", attempts, err)
	}
}

func TestDispatchAfterPrimaryAdvances(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, objID := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	obj.IntegrationBranch = "cardex/n3/main"
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "c", Objective: objID, Kind: model.KindDev, Title: "c", Tier: model.TierT2,
		Status: model.CardReady, Channel: "cha", Model: "model-a",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	h1 := rev(t, repo, "refs/heads/main")
	h2 := strings.TrimSpace(gitOutput(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit-tree", "HEAD^{tree}", "-p", h1, "-m", "advance"))
	installPrimaryAdvanceHook(t, repo, h2)
	stderr := captureStderr(t)
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 84, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if rev(t, repo, "refs/heads/cardex/n3/main") != h1 {
		t.Fatalf("integration %s want %s", rev(t, repo, "refs/heads/cardex/n3/main"), h1)
	}
	if rev(t, repo, "refs/heads/main") != h2 {
		t.Fatalf("main %s want %s", rev(t, repo, "refs/heads/main"), h2)
	}
	got, err := db.GetCard(ctx, "c")
	if err != nil || got.Status != model.CardRunning {
		t.Fatalf("card %+v %v", got, err)
	}
	attempts, err := db.ListAttempts(ctx, "c")
	if err != nil || len(attempts) != 1 || attempts[0].PID != 84 {
		t.Fatalf("attempt %+v %v", attempts, err)
	}
	info, err := os.Stat(attempts[0].Worktree)
	if err != nil || !info.IsDir() {
		t.Fatalf("worktree %s %v", attempts[0].Worktree, err)
	}
	text := stderr()
	blob := allEventText(t, db, "card:c")
	if strings.Contains(text, "moved") || strings.Contains(text, "primary branch") || strings.Contains(blob, "moved") || strings.Contains(blob, "primary branch") {
		t.Fatalf("stderr %q events %s", text, blob)
	}
	tip, err := workspace.Git{}.EnsureIntegrationBranch(ctx, repo, "cardex/n3/main")
	if err != nil || tip != h1 {
		t.Fatalf("second ensure %s %v", tip, err)
	}
	if rev(t, repo, "refs/heads/main") != h2 {
		t.Fatal("second ensure moved main")
	}
}

type failBody struct {
	ID      string
	Step    string
	Channel string
	Model   string
}

func firstLaunchFailed(t *testing.T, db *store.Store, cardID string) failBody {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "card:"+cardID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != model.EventLaunchFailed {
			continue
		}
		var payload struct {
			Step    string `json:"step"`
			Channel string `json:"channel"`
			Model   string `json:"model"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		return failBody{ID: ev.ID, Step: payload.Step, Channel: payload.Channel, Model: payload.Model}
	}
	t.Fatalf("no launch_failed in %+v", events)
	return failBody{}
}

func eventAfter(t *testing.T, db *store.Store, cardID, id string) model.Event {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "card:"+cardID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		if events[i].ID == id && i+1 < len(events) {
			return events[i+1]
		}
	}
	t.Fatalf("no event after %s in %+v", id, events)
	return model.Event{}
}

func startedAttempt(t *testing.T, db *store.Store, cardID string) model.Attempt {
	t.Helper()
	attempts, err := db.ListAttempts(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	var found *model.Attempt
	for i := range attempts {
		if attempts[i].Outcome == "" && attempts[i].PID > 0 {
			if found != nil {
				t.Fatalf("two starts %+v", attempts)
			}
			found = &attempts[i]
		}
	}
	if found == nil {
		t.Fatalf("no start %+v", attempts)
	}
	return *found
}

func failedAttempt(t *testing.T, db *store.Store, cardID string) model.Attempt {
	t.Helper()
	attempts, err := db.ListAttempts(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	var found *model.Attempt
	for i := range attempts {
		if attempts[i].Outcome == model.OutcomeLaunchFailed {
			if found != nil {
				t.Fatalf("two failures %+v", attempts)
			}
			found = &attempts[i]
		}
	}
	if found == nil {
		t.Fatalf("no launch_failed attempt %+v", attempts)
	}
	return *found
}

func setFillWindow(t *testing.T, db *store.Store, id string, reset time.Time, on bool) {
	t.Helper()
	ch, err := db.GetChannel(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if on {
		ch.QuotaWindows = []model.QuotaWindow{windowWithRemaining("5h", reset, 0.9)}
		ch.Redline = 0
	} else {
		ch.QuotaWindows = nil
	}
	if err := db.UpdateChannel(context.Background(), &ch); err != nil {
		t.Fatal(err)
	}
}

func setHealth(t *testing.T, db *store.Store, id string, health model.Health) {
	t.Helper()
	ch, err := db.GetChannel(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ch.Health = health
	if err := db.UpdateChannel(context.Background(), &ch); err != nil {
		t.Fatal(err)
	}
}

func preferChb(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, spec := range []struct {
		id   string
		pref int
	}{
		{"pos-cha", 9},
		{"pos-chb", 0},
	} {
		pos, err := db.GetPositioning(ctx, spec.id)
		if err != nil {
			t.Fatal(err)
		}
		pos.Preference = spec.pref
		if err := db.UpdatePositioning(ctx, &pos); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := db.GetChannel(ctx, "cha")
	if err != nil {
		t.Fatal(err)
	}
	ch.Concurrency = 1
	if err := db.UpdateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
}

func occupyChannel(t *testing.T, db *store.Store, objID, channel string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	holder := model.Card{
		ID: "hold", Objective: objID, Kind: model.KindDev, Title: "hold",
		Tier: model.TierT2, Status: model.CardPaused,
	}
	if err := db.CreateCard(ctx, &holder); err != nil {
		t.Fatal(err)
	}
	attempt := model.Attempt{
		ID: "hold-a", Card: "hold", Route: model.Route{Channel: channel, Model: "model-a"}, StartedAt: now,
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
}

func releaseOccupy(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	attempts, err := db.ListAttempts(ctx, "hold")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("hold attempts %+v %v", attempts, err)
	}
	ended := attempts[0]
	ended.EndedAt = ended.StartedAt.Add(time.Second)
	if ended.EndedAt.IsZero() {
		ended.EndedAt = time.Unix(1, 0).UTC()
	}
	if err := db.UpdateAttempt(ctx, &ended); err != nil {
		t.Fatal(err)
	}
}

func openRoutes(t *testing.T, db *store.Store, channel string) int {
	t.Helper()
	attempts, err := db.ListAttempts(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, attempt := range attempts {
		if attempt.Route.Channel != channel {
			continue
		}
		if attempt.Outcome == "" && attempt.EndedAt.IsZero() && attempt.Exit == nil {
			n++
		}
	}
	return n
}

func routeDroppedText(t *testing.T, db *store.Store, cardID string) string {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "card:"+cardID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == eventLaunchRouteDropped {
			return string(ev.Data)
		}
	}
	return ""
}

func addCarrier(t *testing.T, db *store.Store, id, modelName string) {
	t.Helper()
	ctx := context.Background()
	ch := model.Channel{
		ID: id, Runner: model.RunnerFake, Family: "early", SourceGroup: "early",
		Models: []string{modelName}, Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2,
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{
		ID: "pos-" + id, Model: modelName, Family: "early",
		Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 0,
	}
	if err := db.CreatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
}

func assertEarlierCarrier(t *testing.T, db *store.Store, earlier, saved, modelName string) {
	t.Helper()
	channels, err := db.ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	earlierAt, savedAt := -1, -1
	for i, ch := range channels {
		switch ch.ID {
		case earlier:
			earlierAt = i
			if ch.Health != model.HealthOK {
				t.Fatalf("earlier health %+v", ch)
			}
			offers := false
			for _, name := range ch.Models {
				if name == modelName {
					offers = true
				}
			}
			if !offers {
				t.Fatalf("earlier models %+v", ch.Models)
			}
		case saved:
			savedAt = i
		}
	}
	if earlierAt < 0 || savedAt < 0 || earlierAt >= savedAt {
		t.Fatalf("carrier order earlier %d saved %d", earlierAt, savedAt)
	}
}

func setConcurrency(t *testing.T, db *store.Store, id string, n int) {
	t.Helper()
	ch, err := db.GetChannel(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ch.Concurrency = n
	if err := db.UpdateChannel(context.Background(), &ch); err != nil {
		t.Fatal(err)
	}
}

func windowOn(t *testing.T, db *store.Store, id string) bool {
	t.Helper()
	ch, err := db.GetChannel(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return len(ch.QuotaWindows) > 0
}

func blockWorktree(t *testing.T, db *store.Store, projectID string) string {
	t.Helper()
	ctx := context.Background()
	proj, err := db.GetProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	root := proj.WorktreeRoot
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj.WorktreeRoot = blocked
	if err := db.UpdateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	return root
}

func restoreWorktree(t *testing.T, db *store.Store, projectID, root string) {
	t.Helper()
	ctx := context.Background()
	proj, err := db.GetProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	proj.WorktreeRoot = root
	if err := db.UpdateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
}

func assertNormalizeHeld(t *testing.T, db *store.Store, objID string) {
	t.Helper()
	ctx := context.Background()
	norm, err := db.GetCard(ctx, "norm")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := db.GetCard(ctx, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if norm.Status == model.CardFailed || parent.Status == model.CardNeedsInput {
		t.Fatalf("normalize %+v parent %+v", norm, parent)
	}
	if parent.Status != model.CardWaiting || parent.WaitReason != "normalize:norm" || parent.Objective != objID {
		t.Fatalf("parent %+v", parent)
	}
}

func assertDisposed(t *testing.T, db *store.Store, attempt model.Attempt) {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "attempt:"+attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != "dev_disposed" {
			continue
		}
		var payload struct {
			Outcome string `json:"outcome"`
			Card    string `json:"card"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Outcome == string(model.OutcomeLaunchFailed) && payload.Card == attempt.Card {
			return
		}
	}
	t.Fatalf("missing dev_disposed %+v", events)
}

func disposedAttempt(t *testing.T, db *store.Store, cardID string) bool {
	t.Helper()
	attempts, err := db.ListAttempts(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if listDisposed(t, db, attempt.ID) {
			return true
		}
	}
	return false
}

func listDisposed(t *testing.T, db *store.Store, attemptID string) bool {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "attempt:"+attemptID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == "dev_disposed" {
			return true
		}
	}
	return false
}

func openRawDB(t *testing.T, home string) *sql.DB {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(abs)+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func allEventText(t *testing.T, db *store.Store, entity string) string {
	t.Helper()
	events, err := db.ListEvents(context.Background(), entity)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range events {
		b.WriteString(ev.Type)
		b.WriteString(" ")
		b.Write(ev.Data)
		b.WriteByte('\n')
	}
	return b.String()
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			msg = string(exit.Stderr)
		}
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, msg)
	}
	return strings.TrimSpace(string(out))
}

func installPrimaryAdvanceHook(t *testing.T, repo, h2 string) {
	t.Helper()
	hooks := t.TempDir()
	empty := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" != "prepared" ]; then
  exit 0
fi
while read -r old new ref; do
  case "$ref" in
    refs/heads/cardex/*)
      git -C %q -c core.hooksPath=%q update-ref refs/heads/main %q || exit 1
      ;;
  esac
done
exit 0
`, repo, empty, h2)
	path := filepath.Join(hooks, "reference-transaction")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "core.hooksPath", hooks)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
