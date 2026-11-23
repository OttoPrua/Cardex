package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return callCLI(t, args)
}

func TestWaitStatusesTimeoutAndReads(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Obj", "--origin", "file:manager")
	for _, status := range []string{"merged", "needs_input", "failed", "canceled", "reviewing"} {
		id := "c-" + status
		mustHome(t, home, "card", "add", "--id", id, "--objective", "o1", "--title", status, "--status", status)
		code, out, errb := runCLI(t, "--home", home, "--json", "wait", id, "--until", "merged,needs_input,failed,canceled,reviewing", "--timeout", "1s")
		if code != 0 || errb != "" {
			t.Fatalf("%s exit %d\n%s\n%s", status, code, out, errb)
		}
		pulse := onePulse(t, out)
		if pulse.ID != id || pulse.Status != status {
			t.Fatalf("%s pulse %+v", status, pulse)
		}
	}

	mustHome(t, home, "card", "add", "--id", "c-wait", "--objective", "o1", "--title", "waiting", "--status", "ready")
	withDB(t, home, func(db *store.Store) {
		ctx := context.Background()
		if err := db.CreateAttempt(ctx, &model.Attempt{
			ID: "a-wait", Card: "c-wait", Route: model.Route{Model: "fake-model"}, HeadCommit: "abc123",
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateQuestion(ctx, &model.Question{
			ID: "q-wait", Objective: "o1", Scope: "c-wait", ScopeKind: "card", Kind: model.QuestionCustom, Text: "choose",
		}); err != nil {
			t.Fatal(err)
		}
	})
	started := time.Now()
	code, out, errb := runCLI(t, "--home", home, "--json", "wait", "c-wait", "--until", "merged", "--timeout", "250ms", "--origin", "file:manager")
	if code == 0 {
		t.Fatalf("timeout returned success\n%s\n%s", out, errb)
	}
	if time.Since(started) < 150*time.Millisecond {
		t.Fatalf("timeout returned too fast: %s", time.Since(started))
	}
	pulse := onePulse(t, out)
	if pulse.ID != "c-wait" || pulse.Status != "ready" || pulse.Reason != "timeout" || pulse.Model != "fake-model" || pulse.SHA != "abc123" || pulse.QuestionID != "q-wait" {
		t.Fatalf("timeout pulse %+v", pulse)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(80 * time.Millisecond)
		withDB(t, home, func(db *store.Store) {
			ctx := context.Background()
			card, err := db.GetCard(ctx, "c-wait")
			if err != nil {
				t.Error(err)
				return
			}
			card.Status = model.CardMerged
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Error(err)
			}
		})
	}()
	code, out, errb = runCLI(t, "--home", home, "--json", "wait", "c-wait", "--until", "merged", "--timeout", "2s")
	<-done
	if code != 0 {
		t.Fatalf("merged wait exit %d\n%s\n%s", code, out, errb)
	}
	pulse = onePulse(t, out)
	if pulse.Status != "merged" || pulse.Reason == "timeout" {
		t.Fatalf("merged pulse %+v", pulse)
	}

	code, out, errb = runCLI(t, "--home", home, "--json", "wait", "o1", "--until", "done", "--timeout", "300ms")
	if code == 0 {
		t.Fatalf("objective wait succeeded early\n%s", out)
	}
	withDB(t, home, func(db *store.Store) {
		ctx := context.Background()
		obj, err := db.GetObjective(ctx, "o1")
		if err != nil {
			t.Fatal(err)
		}
		obj.Status = model.ObjectiveDone
		if err := db.UpdateObjective(ctx, &obj); err != nil {
			t.Fatal(err)
		}
	})
	code, out, errb = runCLI(t, "--home", home, "--json", "wait", "o1", "--until", "done", "--timeout", "1s")
	if code != 0 {
		t.Fatalf("objective done exit %d\n%s\n%s", code, out, errb)
	}
	pulse = onePulse(t, out)
	if pulse.ID != "o1" || pulse.Status != "done" {
		t.Fatalf("objective pulse %+v", pulse)
	}

	code, out, errb = runCLI(t, "--home", home, "--json", "card", "show", "c-wait")
	if code != 0 {
		t.Fatal(errb)
	}
	t.Setenv("CARDEX_ORIGIN", "envkind:env-session")
	code, out, errb = runCLI(t, "--home", home, "--json", "card", "show", "c-wait")
	if code != 0 {
		t.Fatal(errb)
	}
	code, out, errb = runCLI(t, "--home", home, "--json", "card", "list", "--objective", "o1", "--origin", "file:manager")
	if code != 0 {
		t.Fatal(errb)
	}
	code, out, errb = runCLI(t, "--home", home, "--json", "objective", "show", "o1", "--origin", "file:manager")
	if code != 0 {
		t.Fatal(errb)
	}
	code, out, errb = runCLI(t, "--home", home, "--json", "reads")
	if code != 0 {
		t.Fatal(errb)
	}
	var rows []readView
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err, out)
	}
	got := map[string]int{}
	for _, row := range rows {
		got[row.Origin+"|"+row.CardID+"|"+row.Command] = row.Count
	}
	if got["file:manager|c-wait|wait"] != 1 || got["envkind:env-session|c-wait|card-show"] != 1 || got["file:manager|c-wait|card-list"] < 1 || got["file:manager|c-wait|objective-show"] != 1 {
		t.Fatalf("reads %+v", got)
	}
	if _, ok := got["|c-wait|card-show"]; ok {
		t.Fatal("a show without origin was counted")
	}
}

func TestReadCountFailureDoesNotFailShow(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Obj")
	mustHome(t, home, "card", "add", "--id", "c1", "--objective", "o1", "--title", "Card")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(home, "cardex.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE mgmt_reads`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE mgmt_reads (origin TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	code, out, errb := runCLI(t, "--home", home, "--json", "card", "show", "c1", "--origin", "file:manager")
	if code != 0 || !strings.Contains(out, `"id": "c1"`) {
		t.Fatalf("show exit %d\n%s\n%s", code, out, errb)
	}
}

func onePulse(t *testing.T, out string) notifyPulse {
	t.Helper()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("want one line, got %q", out)
	}
	var pulse notifyPulse
	if err := json.Unmarshal([]byte(out), &pulse); err != nil {
		t.Fatal(err, out)
	}
	return pulse
}

type notifyPulse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Model      string `json:"model"`
	SHA        string `json:"sha"`
	QuestionID string `json:"question_id"`
}
