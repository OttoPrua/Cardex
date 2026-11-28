package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/board"
	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

type reviewProgressFixture struct {
	t    *testing.T
	ctx  context.Context
	db   *store.Store
	home string
	base time.Time
}

func reviewProgressTest(t *testing.T) reviewProgressFixture {
	t.Helper()
	board.CloseDB()
	t.Cleanup(board.CloseDB)
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return reviewProgressFixture{t, context.Background(), db, home, time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)}
}

func (f reviewProgressFixture) card(id, obj string, kind model.CardKind, status model.CardStatus, deps ...string) {
	f.t.Helper()
	c := model.Card{ID: id, Objective: obj, Title: id, Kind: kind, Status: status, Deps: deps}
	if err := f.db.CreateCard(f.ctx, &c); err != nil {
		f.t.Fatal(err)
	}
}

func (f reviewProgressFixture) attempt(id, card string, i, offset int, outcome model.Outcome, running bool) {
	f.t.Helper()
	a := model.Attempt{ID: id, Card: card, Index: i, StartedAt: f.base.Add(time.Duration(offset) * time.Second), Outcome: outcome}
	if running {
		a.PID = 41
	} else {
		a.EndedAt = a.StartedAt.Add(time.Second)
	}
	if err := f.db.CreateAttempt(f.ctx, &a); err != nil {
		f.t.Fatal(err)
	}
}

func (f reviewProgressFixture) event(id, entity, kind string, offset int, data any) {
	f.t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		f.t.Fatal(err)
	}
	e := model.Event{ID: id, Entity: entity, Type: kind, TS: f.base.Add(time.Duration(offset) * time.Second), Data: raw}
	if err := f.db.AppendEvent(f.ctx, &e); err != nil {
		f.t.Fatal(err)
	}
}

func (f reviewProgressFixture) plan(subject, review string, offset int) {
	f.event("plan-"+review, "card:"+subject, "review_planned", offset, map[string]string{"card": subject, "review": review, "mode": "review", "tier": "T3", "reason": "synthetic risk"})
}

func (f reviewProgressFixture) subject(attempt, subject string, offset int, standalone bool) {
	f.event("pin-"+attempt, "attempt:"+attempt, model.ReviewSubjectEvent, offset, model.PinnedReviewSubject{Subject: subject, Standalone: standalone})
}

func (f reviewProgressFixture) verdict(id, subject, review, attempt, v string, offset int) {
	f.event(id, "card:"+subject, "review_verdict", offset, map[string]string{"review_card_id": review, "review_attempt_id": attempt, "verdict": v, "opinion": "NOT-A-PROGRESS-FIELD"})
}

func (f reviewProgressFixture) cli(wantCode int, args ...string) string {
	f.t.Helper()
	var out, err bytes.Buffer
	code := Main(append([]string{"--home", f.home}, args...), &out, &err)
	if code != wantCode {
		f.t.Fatalf("CLI %v code=%d: %s", args, code, err.String())
	}
	return out.String()
}

func (f reviewProgressFixture) get(path string) string {
	f.t.Helper()
	rec := httptest.NewRecorder()
	board.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("%s HTTP %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func progressJSON(t *testing.T, raw string, want map[string]string) map[string]string {
	t.Helper()
	var progress struct {
		Progress map[string]string `json:"review_progress"`
	}
	if err := json.Unmarshal([]byte(raw), &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Progress == nil {
		t.Fatalf("no current review projection in %d-byte payload", len(raw))
	}
	for k, v := range want {
		if got := progress.Progress[k]; got != v {
			t.Errorf("%s: got %q, want %q (progress %v)", k, got, v, progress.Progress)
		}
	}
	return progress.Progress
}

func TestReviewProgressPinnedAttemptAcrossSurfaces(t *testing.T) {
	f := reviewProgressTest(t)
	f.card("subject", "o", model.KindDev, model.CardMergeQueued)
	f.card("other", "o", model.KindDev, model.CardReady)
	f.card("review", "o2", model.KindReview, model.CardDone, "other", "subject")
	f.plan("subject", "review", 0)
	f.attempt("candidate", "subject", 0, -3, model.OutcomePassed, false)
	f.attempt("a-exact", "review", 0, 1, model.OutcomePassed, false)
	f.subject("a-exact", "subject", 1, false) // edited dependencies must not reassign this attempt
	f.verdict("verdict", "subject", "review", "a-exact", "pass", 4)
	raw := f.cli(0, "card", "show", "subject", "--json")
	rawBoard := f.get("/api/cards/subject")
	oldManual := len(raw) + len(f.cli(0, "card", "show", "review", "--json"))
	t.Logf("full CLI parent: %d bytes; manual parent+review: %d bytes; board parent: %d bytes", len(raw), oldManual, len(rawBoard))
	want := map[string]string{"phase": "absorbed", "review_card_id": "review", "review_attempt_id": "a-exact", "verdict": "pass", "relation": "planned", "tier": "T1"}
	a, b := progressJSON(t, raw, want), progressJSON(t, rawBoard, want)
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("board and CLI differ: %v != %v", a, b)
		}
	}
	if a["opinion"] != "" {
		t.Fatal("raw opinion copied to projection")
	}
	html := f.get("/cards/subject")
	if !strings.Contains(html, `id="review-progress"`) || !strings.Contains(html, `href="/cards/review"`) || !strings.Contains(html, "a-exact") {
		t.Fatal("rendered board omitted review progress, link or exact attempt")
	}
	compact := f.cli(0, "card", "show", "subject", "--review-progress", "--json")
	var p map[string]string
	if err := json.Unmarshal([]byte(compact), &p); err != nil {
		t.Fatal(err)
	}
	if p["phase"] != "absorbed" || p["review_attempt_id"] != "a-exact" || strings.Contains(compact, "NOT-A-PROGRESS-FIELD") {
		t.Fatalf("compact %q", compact)
	}
	t.Logf("one compact projection: %d JSON bytes; no event opinion in the output", len(compact))
	if line := f.cli(0, "card", "show", "subject"); line != "subject\tsubject\n" {
		t.Fatalf("existing single line changed: %q", line)
	}
	if text := f.cli(0, "card", "show", "subject", "--review-progress"); !strings.Contains(text, "phase=absorbed") || !strings.Contains(text, "attempt=a-exact") {
		t.Fatalf("text %q", text)
	}
	progressJSON(t, f.cli(0, "card", "show", "other", "--json"), map[string]string{"phase": "not_triggered", "review_card_id": "", "verdict": ""})
}

func TestReviewProgressDoesNotInventVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		reviewStatus           model.CardStatus
		pin                    string
		standalone             bool
		actualOutcome          model.Outcome
		receiptAttempt         string
		authorHead, reviewBase string
		restartAuthor          bool
		conflictingPin         bool
		wantPhase              string
	}{
		{name: "verified latest", actualOutcome: model.OutcomePassed, receiptAttempt: "actual", wantPhase: "absorbed"},
		{name: "stale index not rescued by receipt time", actualOutcome: model.OutcomePassed, receiptAttempt: "old", wantPhase: "unknown"},
		{name: "missing attempt", actualOutcome: model.OutcomePassed, receiptAttempt: "missing", wantPhase: "unknown"},
		{name: "foreign attempt", actualOutcome: model.OutcomePassed, receiptAttempt: "foreign-attempt", wantPhase: "unknown"},
		{name: "anonymous receipt", actualOutcome: model.OutcomePassed, wantPhase: "unknown"},
		{name: "foreign pinned subject", pin: "elsewhere", actualOutcome: model.OutcomePassed, receiptAttempt: "actual", wantPhase: "unknown"},
		{name: "pinned standalone", pin: "rv", standalone: true, actualOutcome: model.OutcomePassed, receiptAttempt: "actual", wantPhase: "unknown"},
		{name: "conflicting pins", conflictingPin: true, actualOutcome: model.OutcomePassed, receiptAttempt: "actual", wantPhase: "unknown"},
		{name: "failed attempt", actualOutcome: model.OutcomeTestsFailed, receiptAttempt: "actual", wantPhase: "unknown"},
		{name: "commit mismatch", actualOutcome: model.OutcomePassed, receiptAttempt: "actual", authorHead: strings.Repeat("a", 40), reviewBase: strings.Repeat("b", 40), wantPhase: "unknown"},
		{name: "exact commit", actualOutcome: model.OutcomePassed, receiptAttempt: "actual", authorHead: strings.Repeat("a", 40), reviewBase: strings.Repeat("a", 40), wantPhase: "absorbed"},
		{name: "later author", actualOutcome: model.OutcomePassed, receiptAttempt: "actual", restartAuthor: true, wantPhase: "not_triggered"},
		{name: "requeued review", reviewStatus: model.CardReady, actualOutcome: model.OutcomePassed, receiptAttempt: "actual", wantPhase: "planned"},
		{name: "canceled review", reviewStatus: model.CardCanceled, actualOutcome: model.OutcomePassed, receiptAttempt: "actual", wantPhase: "closed"},
		{name: "held review", reviewStatus: model.CardNeedsInput, actualOutcome: model.OutcomePassed, receiptAttempt: "actual", wantPhase: "needs_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reviewProgressTest(t)
			status := tc.reviewStatus
			if status == "" {
				status = model.CardDone
			}
			f.card("dev", "o", model.KindDev, model.CardReviewing)
			f.card("elsewhere", "o", model.KindDev, model.CardReviewing)
			f.card("rv", "o", model.KindReview, status, "dev")
			f.card("rv-foreign", "o", model.KindReview, model.CardDone, "elsewhere")
			f.attempt("author", "dev", 0, -6, model.OutcomePassed, false)
			f.plan("dev", "rv", 0)
			f.attempt("old", "rv", 1, 3, model.OutcomePassed, false)
			f.attempt("actual", "rv", 2, 2, tc.actualOutcome, false)
			f.attempt("foreign-attempt", "rv-foreign", 3, 2, model.OutcomePassed, false)
			subject := tc.pin
			if subject == "" {
				subject = "dev"
			}
			f.subject("actual", subject, 2, tc.standalone)
			if tc.conflictingPin {
				f.event("another-pin", "attempt:actual", model.ReviewSubjectEvent, 3, model.PinnedReviewSubject{Subject: "elsewhere"})
			}
			f.verdict("receipt", "dev", "rv", tc.receiptAttempt, "pass", 5)
			if tc.authorHead != "" {
				a, err := f.db.GetAttempt(f.ctx, "author")
				if err != nil {
					t.Fatal(err)
				}
				a.HeadCommit = tc.authorHead
				if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
					t.Fatal(err)
				}
				a, err = f.db.GetAttempt(f.ctx, "actual")
				if err != nil {
					t.Fatal(err)
				}
				a.BaseCommit, a.Route.Channel, a.Route.Model = tc.reviewBase, "synthetic-route", "synthetic-reviewer"
				if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
					t.Fatal(err)
				}
			}
			if tc.restartAuthor {
				f.attempt("new-author", "dev", 1, 6, "", true)
			}
			verdict := ""
			if tc.wantPhase == "absorbed" {
				verdict = "pass"
			}
			progressJSON(t, f.cli(0, "card", "show", "dev", "--json"), map[string]string{"phase": tc.wantPhase, "verdict": verdict})
		})
	}
}

func TestReviewProgressQueueAndEscalation(t *testing.T) {
	f := reviewProgressTest(t)
	f.card("subject", "o", model.KindDev, model.CardReviewing)
	f.card("review", "o", model.KindReview, model.CardReady, "subject")
	f.card("higher", "o", model.KindReview, model.CardReady, "subject")
	f.card("held", "o", model.KindDev, model.CardReady)
	f.plan("subject", "review", -5)
	f.attempt("old", "review", 0, -4, model.OutcomePassed, false)
	f.subject("old", "subject", -4, false)
	// A completion on an older attempt must not keep a ready review in "started".
	check := func(id string, want map[string]string) {
		t.Helper()
		progressJSON(t, f.cli(0, "card", "show", id, "--json"), want)
	}
	check("subject", map[string]string{"phase": "planned", "review_attempt_id": ""})
	f.event("blocked", "card:review", "route_no_candidate", 0, map[string]string{"card": "review", "reason": "no configured candidate", "hint": "configure a channel"})
	check("subject", map[string]string{"phase": "no_route", "route_reason": "no configured candidate", "review_attempt_id": ""})
	change := func(id string, status model.CardStatus) {
		t.Helper()
		c, err := f.db.GetCard(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		c.Status = status
		if err := f.db.UpdateCard(f.ctx, &c); err != nil {
			t.Fatal(err)
		}
	}
	change("review", model.CardPaused)
	check("subject", map[string]string{"phase": "paused", "route_reason": ""})
	change("review", model.CardRunning)
	f.attempt("new", "review", 1, 3, "", true)
	f.subject("new", "subject", 3, false)
	check("subject", map[string]string{"phase": "started", "review_attempt_id": "new", "route_reason": ""})
	change("review", model.CardDone)
	f.verdict("same-tick-verdict", "subject", "review", "old", "escalate", 4)
	f.event("higher-selected", "card:subject", "review_escalated", 4, map[string]string{"card": "subject", "from": "review", "review": "higher", "tier": "T3"})
	check("subject", map[string]string{"phase": "planned", "review_card_id": "higher", "relation": "escalated", "verdict": ""})
	change("higher", model.CardCanceled)
	change("review", model.CardDone)
	f.attempt("retry", "review", 2, 5, model.OutcomePassed, false)
	f.subject("retry", "subject", 5, false)
	f.verdict("retry-verdict", "subject", "review", "retry", "pass", 8)
	check("subject", map[string]string{"phase": "absorbed", "review_card_id": "review", "review_attempt_id": "retry", "verdict": "pass"})
	f.event("skip", "card:held", "review_skipped", 0, map[string]string{"card": "held", "reason": "disabled"})
	check("held", map[string]string{"phase": "skipped", "reason": "disabled"})
	f.attempt("fresh", "held", 0, 1, "", true)
	check("held", map[string]string{"phase": "not_triggered", "verdict": ""})
}

func TestReviewProgressDoesNotAssignAnUnpinnedLiveAttempt(t *testing.T) {
	f := reviewProgressTest(t)
	f.card("parent", "o", model.KindDev, model.CardReviewing)
	f.card("rv-parent", "o", model.KindReview, model.CardRunning)
	f.attempt("unpinned", "rv-parent", 0, 1, "", true)
	progressJSON(t, f.cli(0, "card", "show", "parent", "--json"), map[string]string{"phase": "unknown", "review_attempt_id": "", "verdict": ""})
	f.subject("unpinned", "rv-parent", 1, true)
	progressJSON(t, f.cli(0, "card", "show", "parent", "--json"), map[string]string{"phase": "not_triggered", "review_attempt_id": "", "verdict": ""})
	progressJSON(t, f.cli(0, "card", "show", "rv-parent", "--json"), map[string]string{"phase": "started", "review_attempt_id": "unpinned"})
}

func TestReviewProgressRequiresCurrentAttribution(t *testing.T) {
	for _, tc := range []struct {
		name, plan, pin   string
		conflict, receipt bool
		sameBase          bool
		start             int
		wantPhase, wantID string
	}{
		{name: "fresh plan rejects an older attempt even with late receipt", plan: "review_planned", pin: "dev", receipt: true, sameBase: true, start: -3, wantPhase: "unknown"},
		{name: "explicit reuse with matching pinned base", plan: "review_reused", pin: "dev", receipt: true, sameBase: true, start: -3, wantPhase: "absorbed", wantID: "review-attempt"},
		{name: "reuse does not excuse an unverified earlier base", plan: "review_reused", pin: "dev", receipt: true, start: -3, wantPhase: "unknown"},
		{name: "mutable dependency alone cannot attach completed attempt metadata", start: 1, wantPhase: "unknown"},
		{name: "an exact legacy receipt corroborates the unpinned attempt", receipt: true, sameBase: true, start: 1, wantPhase: "absorbed", wantID: "review-attempt"},
		{name: "manual review with conflicting frozen subjects", pin: "dev", conflict: true, receipt: true, sameBase: true, start: 1, wantPhase: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reviewProgressTest(t)
			f.card("dev", "o", model.KindDev, model.CardReviewing)
			f.card("other", "o", model.KindDev, model.CardReviewing)
			f.card("review", "o", model.KindReview, model.CardDone, "dev")
			f.attempt("author", "dev", 0, -6, model.OutcomePassed, false)
			f.attempt("review-attempt", "review", 0, tc.start, model.OutcomePassed, false)
			if tc.plan != "" {
				f.event("plan", "card:dev", tc.plan, 0, map[string]string{"card": "dev", "review": "review"})
			}
			if tc.pin != "" {
				f.subject("review-attempt", tc.pin, tc.start, false)
			}
			if tc.conflict {
				f.event("second-pin", "attempt:review-attempt", model.ReviewSubjectEvent, tc.start, model.PinnedReviewSubject{Subject: "other"})
			}
			head := strings.Repeat("a", 40)
			for _, id := range []string{"author", "review-attempt"} {
				a, err := f.db.GetAttempt(f.ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if id == "author" && tc.sameBase {
					a.HeadCommit = head
				} else if id == "review-attempt" {
					a.Route = model.Route{Channel: "synthetic-route", Model: "synthetic-reviewer"}
					a.BaseCommit = head
				}
				if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
					t.Fatal(err)
				}
			}
			if tc.receipt {
				f.verdict("receipt", "dev", "review", "review-attempt", "pass", 3)
			}
			want := map[string]string{"phase": tc.wantPhase, "review_attempt_id": tc.wantID, "verdict": "", "actual_channel": "", "actual_model": "", "reviewed_base": ""}
			if tc.wantID != "" {
				want["verdict"], want["actual_channel"], want["actual_model"], want["reviewed_base"] = "pass", "synthetic-route", "synthetic-reviewer", head
			}
			progressJSON(t, f.cli(0, "card", "show", "dev", "--json"), want)
		})
	}
}

func TestReviewProgressReadsActualDispositionReceipt(t *testing.T) {
	f := reviewProgressTest(t)
	if err := f.db.CreateProject(f.ctx, &model.Project{ID: "p", RepoPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"o", "review-objective"} {
		obj := model.Objective{ID: id, Project: "p", Title: "Synthetic", Mode: model.ModeManual, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive}
		if err := f.db.CreateObjective(f.ctx, &obj); err != nil {
			t.Fatal(err)
		}
	}
	f.card("subject", "o", model.KindDev, model.CardReviewing)
	f.card("review", "review-objective", model.KindReview, model.CardHarvesting, "subject")
	f.plan("subject", "review", 0)
	f.attempt("author", "subject", 0, -1, model.OutcomePassed, false)
	f.attempt("actual", "review", 0, 1, model.OutcomePassed, false)
	f.subject("actual", "subject", 1, false)
	head := strings.Repeat("a", 40)
	author, err := f.db.GetAttempt(f.ctx, "author")
	if err != nil {
		t.Fatal(err)
	}
	author.HeadCommit = head
	if err := f.db.UpdateAttempt(f.ctx, &author); err != nil {
		t.Fatal(err)
	}
	actual, err := f.db.GetAttempt(f.ctx, "actual")
	if err != nil {
		t.Fatal(err)
	}
	actual.Route = model.Route{Channel: "synthetic-codex", Model: "synthetic-model"}
	actual.BaseCommit, actual.Worktree = head, t.TempDir()
	if err := f.db.UpdateAttempt(f.ctx, &actual); err != nil {
		t.Fatal(err)
	}
	body := "## 结论\nsynthetic\n\n## 复核结论\npass\n"
	if err := os.WriteFile(filepath.Join(actual.Worktree, ".cardex-result.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	review, err := f.db.GetCard(f.ctx, "review")
	if err != nil {
		t.Fatal(err)
	}
	ctx := loop.WithStore(loop.WithAttempt(loop.WithHome(f.ctx, f.home), actual.ID), f.db)
	if err := loop.Dispose(ctx, &review, harvest.Result{Outcome: model.OutcomePassed, ResultText: body}); err != nil {
		t.Fatal(err)
	}
	progressJSON(t, f.cli(0, "card", "show", "subject", "--json"), map[string]string{
		"phase": "absorbed", "verdict": "pass", "review_attempt_id": actual.ID,
		"actual_channel": actual.Route.Channel, "actual_model": actual.Route.Model, "reviewed_base": head,
	})
}
