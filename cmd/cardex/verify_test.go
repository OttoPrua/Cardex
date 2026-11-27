package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestVerifyCLIUsesNewestRecordedEvidenceOnly(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db := openBriefDB(t, home)
	c := model.Card{ID: "synthetic", Kind: model.KindDev, Status: model.CardReviewing, Acceptance: []string{"  never execute this command  ", "another required command"}}
	if err := db.CreateCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	a := model.Attempt{ID: "attempt-1", Card: c.ID, Index: 1, StartedAt: time.Now().Add(-time.Hour), EndedAt: time.Now(), Outcome: model.OutcomePassed}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	sum := harvest.Summary{AttemptID: a.ID, Match: true, Acceptance: []harvest.AcceptanceResult{{Command: "never execute this command", Exit: 0}, {Command: "another required command", Exit: 0}}}
	path := harvest.SummaryPath(home, a)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(sum)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	call := func() (int, verifyView, string) {
		t.Helper()
		code, out, errText := runMain(t, "--home", home, "verify", c.ID, "--json")
		var row verifyView
		if errText != "" {
			t.Fatal(errText)
		}
		if err := json.Unmarshal([]byte(out), &row); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "never execute") {
			t.Fatal("raw command leaked into verify output")
		}
		return code, row, out
	}
	journal := filepath.Join(home, "cardex.db-journal")
	if err := os.WriteFile(journal, []byte("synthetic rollback journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errText := runMain(t, "--home", home, "verify", c.ID, "--json"); code == 0 || out != "" ||
		!strings.Contains(errText, "rollback journal exists") {
		t.Fatalf("rollback journal ignored: code=%d out=%q err=%q", code, out, errText)
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if code, row, out := call(); code != 0 || row.Result != "verified" {
		t.Fatalf("code=%d %s", code, out)
	} else {
		oldCode, oldOut, oldErr := runMain(t, "--home", home, "card", "show", c.ID, "--summary", "--json")
		if oldCode != 0 || oldErr != "" {
			t.Fatalf("card show baseline returned %d: %s", oldCode, oldErr)
		}
		t.Logf("one CLI call in both cases: summary display %d JSON bytes; verification %d JSON bytes", len(oldOut), len(out))
	}
	complete := sum.Acceptance
	sum.Acceptance = complete[:1]
	write()
	if code, row, out := call(); code == 0 || row.AcceptanceMissing != 1 || row.Result != "incomplete" {
		t.Fatalf("truncated acceptance verified: code=%d %s", code, out)
	}
	sum.Acceptance = []harvest.AcceptanceResult{complete[1], complete[0]}
	write()
	if code, row, out := call(); code == 0 || row.Result != "failed" {
		t.Fatalf("reordered acceptance verified: code=%d %s", code, out)
	}
	sum.Acceptance = complete
	sum.AttemptID = "another-attempt"
	write()
	if code, row, out := call(); code == 0 || row.Result != "failed" {
		t.Fatalf("code=%d %s", code, out)
	}
	sum.AttemptID = a.ID
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(raw, []byte(`,"exit":0`), nil, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, row, out := call(); code == 0 || row.AcceptanceMissing != 1 || row.Result != "incomplete" {
		t.Fatalf("omitted exit code verified: code=%d %s", code, out)
	}
	sum.Acceptance[0].Exit = 1
	write()
	if code, row, out := call(); code == 0 || row.AcceptanceFailed != 1 {
		t.Fatalf("code=%d %s", code, out)
	}
	db = openBriefDB(t, home)
	newer := model.Attempt{ID: "attempt-2", Card: c.ID, Index: 2, StartedAt: time.Now()}
	if err := db.CreateAttempt(ctx, &newer); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "cardex.db-wal")); err != nil {
		t.Fatalf("synthetic live WAL missing: %v", err)
	}
	if code, out, errText := runMain(t, "--home", home, "verify", c.ID, "--json"); code == 0 || out != "" ||
		!strings.Contains(errText, "while database WAL exists") {
		t.Fatalf("live WAL tried to certify a stale checkpoint: code=%d out=%q err=%q", code, out, errText)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if code, row, out := call(); code == 0 || row.Attempt != newer.ID || row.Result != "incomplete" {
		t.Fatalf("code=%d %s", code, out)
	}
}

func TestVerifyDoesNotCreateMissingHome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "unused")
	code, _, _ := runMain(t, "--home", dir, "verify", "missing")
	if code == 0 {
		t.Fatal("a missing DB was verified")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("verify touched missing home: %v", err)
	}
}

func TestVerifyRequiresExplicitReviewFindings(t *testing.T) {
	home := t.TempDir()
	card := model.Card{ID: "synthetic", Kind: model.KindReview}
	attempt := model.Attempt{ID: "review-attempt", Card: card.ID, EndedAt: time.Now(), Outcome: model.OutcomePassed}
	path := harvest.SummaryPath(home, attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []struct{ review, want string }{
		{`{"verdict":"pass"}`, "incomplete"},
		{`{"verdict":"pass","p0":[],"p1":null}`, "incomplete"},
		{`{"verdict":"pass","p0":[],"p1":[]}`, "verified"},
	} {
		data := `{"attempt_id":"review-attempt","match":true,"review":` + ch.review + `}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := verifyAttempt(home, card, attempt); got.Result != ch.want {
			t.Fatalf("review %s got %+v, want %s", ch.review, got, ch.want)
		}
	}
}
