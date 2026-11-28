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
	attempt := model.Attempt{ID: "review-attempt", Card: card.ID, HeadCommit: "1111111111111111111111111111111111111111", EndedAt: time.Now(), Outcome: model.OutcomePassed}
	path := harvest.SummaryPath(home, attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []struct{ review, want string }{
		{`{"verdict":"pass"}`, "incomplete"},
		{`{"verdict":"pass","p0":[],"p1":null}`, "incomplete"},
		{`{"verdict":"pass","p0":[],"p1":[]}`, "verified"},
	} {
		data := `{"attempt_id":"review-attempt","head_commit":"1111111111111111111111111111111111111111","match":true,"review":` + ch.review + `}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := verifyAttempt(home, card, attempt); got.Result != ch.want {
			t.Fatalf("review %s got %+v, want %s", ch.review, got, ch.want)
		}
	}
}

func TestVerifyAcceptsHarvestedCleanReviewAtExactBase(t *testing.T) {
	ctx, home, repo := t.Context(), t.TempDir(), t.TempDir()
	initGit(t, repo, "main")
	base := gitRev(t, repo, "HEAD")
	gitCmd(t, repo, "checkout", "--detach", base)
	if err := os.WriteFile(filepath.Join(repo, ".cardex-result.md"), []byte("## 复核结论\npass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := openBriefDB(t, home)
	card := model.Card{ID: "clean-review", Kind: model.KindReview, Status: model.CardDone}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	zero := 0
	a := model.Attempt{ID: "review-attempt", Card: card.ID, Worktree: repo, BaseCommit: base, EndedAt: time.Now(), Exit: &zero, Route: model.Route{Channel: "review-channel", Model: "review-model"}}
	result, err := harvest.Harvest(ctx, harvest.Input{Home: home, Card: card, Attempt: a})
	if err != nil || result.EvidenceErr != nil || result.Outcome != model.OutcomePassed {
		t.Fatalf("harvested review: %+v, %v", result, err)
	}
	a.Outcome = result.Outcome
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, diagnostic := runMain(t, "--home", home, "verify", card.ID, "--json")
	var v verifyView
	if code != 0 || diagnostic != "" || json.Unmarshal([]byte(out), &v) != nil || v.Result != "verified" || v.Head != base {
		t.Fatalf("clean review was not verified at its exact base: %d %s %s", code, out, diagnostic)
	}
	path := harvest.SummaryPath(home, a)
	sum, err := harvest.LoadSummary(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*harvest.Summary)
	}{
		{"wrong-head", func(s *harvest.Summary) { s.HeadCommit = strings.Repeat("1", len(base)) }},
		{"wrong-base", func(s *harvest.Summary) { s.BaseCommit = strings.Repeat("1", len(base)) }},
		{"changed-files", func(s *harvest.Summary) { s.Files = []harvest.FileChange{{Path: "README"}} }},
		{"missing-files", func(s *harvest.Summary) { s.Files = nil }},
		{"missing-clean-check", func(s *harvest.Summary) { s.CleanReviewCheckout = nil }},
		{"wrong-attempt", func(s *harvest.Summary) { s.AttemptID = "different" }},
		{"wrong-source", func(s *harvest.Summary) { review := *s.Review; review.Source = "other-result.md"; s.Review = &review }},
	} {
		candidate := sum
		tc.mutate(&candidate)
		raw, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, diagnostic := runMain(t, "--home", home, "verify", card.ID, "--json")
		var result verifyView
		if code == 0 || diagnostic != "" || json.Unmarshal([]byte(out), &result) != nil || result.Result != "failed" {
			t.Fatalf("%s passed: %d %s %s", tc.name, code, out, diagnostic)
		}
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(path), "result.md")); err != nil {
		t.Fatal(err)
	}
	if got := verifyAttempt(home, card, a); got.Result != "failed" {
		t.Fatalf("missing evidence verified: %+v", got)
	}
}

func TestVerifyRejectsUnknownOrPreviouslyDirtyCleanReview(t *testing.T) {
	for _, tc := range []struct {
		name, pre, command   string
		unknown, headMissing bool
	}{
		{name: "tracked-cleaned-by-acceptance", pre: "README", command: "git checkout -- README"},
		{name: "untracked-cleaned-by-acceptance", pre: "extra", command: "git clean -f -- extra"},
		{name: "postcheck-git-error", command: "echo not-a-git-index > .git/index", unknown: true},
		{name: "postcheck-cannot-resolve-head", command: "echo not-a-git-head > .git/HEAD", unknown: true, headMissing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, home, repo := t.Context(), t.TempDir(), t.TempDir()
			initGit(t, repo, "main")
			base := gitRev(t, repo, "HEAD")
			gitCmd(t, repo, "checkout", "--detach", base)
			for _, file := range []struct{ path, text string }{
				{".cardex-result.md", "## 复核结论\npass\n"},
				{tc.pre, "synthetic unverified change\n"},
			} {
				if file.path != "" {
					if err := os.WriteFile(filepath.Join(repo, file.path), []byte(file.text), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			card := model.Card{ID: "failed-review", Kind: model.KindReview, Acceptance: []string{tc.command}}
			zero := 0
			a := model.Attempt{ID: "review-attempt", Card: card.ID, Worktree: repo, BaseCommit: base, EndedAt: time.Now(), Exit: &zero}
			result, err := harvest.Harvest(ctx, harvest.Input{Home: home, Card: card, Attempt: a})
			if err != nil || result.EvidenceErr != nil || result.Outcome != model.OutcomePassed {
				t.Fatalf("harvest: %+v, %v", result, err)
			}
			a.Outcome = result.Outcome
			sum, err := harvest.LoadSummary(harvest.SummaryPath(home, a))
			if err != nil {
				t.Fatal(err)
			}
			wantHead := base
			if tc.headMissing {
				wantHead = ""
			}
			if len(sum.Files) != 0 || sum.HeadCommit != wantHead || len(sum.Acceptance) != 1 || sum.Acceptance[0].Exit != 0 {
				t.Fatalf("old summary distinguishes fixture unexpectedly: %+v", sum)
			}
			if tc.unknown != (sum.CleanReviewCheckout == nil) || (sum.CleanReviewCheckout != nil && *sum.CleanReviewCheckout) {
				t.Fatalf("invalid checkout was certified: %v", sum.CleanReviewCheckout)
			}
			if got := verifyAttempt(home, card, a); got.Result != "failed" {
				t.Fatalf("uncertified checkout verified: %+v", got)
			}
		})
	}
}
