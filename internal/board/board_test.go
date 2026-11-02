package board

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/store"
)

func TestViewsHTMLAndJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	db, home := seedCurrent(t)
	before := snapshot(t, db)

	paths := []string{
		"/", "/api/work",
		"/channels", "/api/channels",
		"/cards/c-run", "/api/cards/c-run",
		"/history", "/api/history",
		"/attempts/a-end/output.log",
		"/missing",
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, path := range paths {
			rec := do(t, method, path)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: status %d, want 405", method, path, rec.Code)
			}
		}
	}

	htmlCurrent := do(t, http.MethodGet, "/")
	jsonCurrent := do(t, http.MethodGet, "/api/work")
	if htmlCurrent.Code != http.StatusOK || jsonCurrent.Code != http.StatusOK {
		t.Fatalf("current status html=%d json=%d", htmlCurrent.Code, jsonCurrent.Code)
	}
	if ct := htmlCurrent.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("html content-type %q", ct)
	}
	if ct := jsonCurrent.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("json content-type %q", ct)
	}
	body := htmlCurrent.Body.String()
	assertContains(t, body, "objective o-live", "node n-mod status frozen open_questions 2",
		"node n-dir status drafting open_questions 0", "status waiting count 1", "status paused count 0",
		"waiting c-wait reason blocked by c-merge on calc.go Answer",
		"attempt a-run", "claim calc.go Answer", "merge c-merge", "question q-open", "freeze the direction?")
	if strings.Contains(body, "<script>alert") {
		t.Fatal("objective title script was not escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("escaped script marker missing")
	}
	if strings.Contains(body, "<script src") || strings.Contains(body, "https://") || strings.Contains(body, "http://") {
		t.Fatal("page loads a third-party script URL")
	}
	assertPollerReloads(t, body)
	for _, hidden := range []string{"o-paused", "c-on-paused", "c-paused", "c-held", "c-v1", "o-done", "o-canceled", "o-cand", "q-done", "q-hist", "a-end"} {
		if strings.Contains(body, hidden) {
			t.Fatalf("default html contains %s", hidden)
		}
	}

	current := decode[currentView](t, jsonCurrent)
	if len(current.Objectives) != 1 || current.Objectives[0].ID != "o-live" {
		t.Fatalf("objectives %#v", current.Objectives)
	}
	if current.Objectives[0].Title != "xss <script>alert(1)</script>" {
		t.Fatalf("title %#v", current.Objectives[0].Title)
	}
	if got := nodeByID(current.Objectives[0].Nodes, "n-mod").OpenQuestionCount; got != 2 {
		t.Fatalf("open questions %d", got)
	}
	if got := countOf(current.StatusCounts, "waiting"); got != 1 {
		t.Fatalf("waiting count %d", got)
	}
	if got := countOf(current.StatusCounts, "paused"); got != 0 {
		t.Fatalf("paused count %d", got)
	}
	if got := countOf(current.StatusCounts, "merge_queued"); got != 1 {
		t.Fatalf("merge count %d", got)
	}
	if len(current.Waiting) != 1 || current.Waiting[0].ID != "c-wait" || current.Waiting[0].WaitReason != "blocked by c-merge on calc.go Answer" {
		t.Fatalf("waiting %#v", current.Waiting)
	}
	if len(current.RunningAttempts) != 1 || current.RunningAttempts[0].ID != "a-run" {
		t.Fatalf("running %#v", current.RunningAttempts)
	}
	if len(current.RunningAttempts[0].Claims) != 1 || current.RunningAttempts[0].Claims[0].Path != "calc.go" || current.RunningAttempts[0].Claims[0].Symbol != "Answer" {
		t.Fatalf("claims %#v", current.RunningAttempts[0].Claims)
	}
	if len(current.MergeQueue) != 1 || current.MergeQueue[0].ID != "c-merge" {
		t.Fatalf("merge %#v", current.MergeQueue)
	}
	if len(current.PendingQuestions) != 1 || current.PendingQuestions[0].ID != "q-open" {
		t.Fatalf("questions %#v", current.PendingQuestions)
	}
	raw := jsonCurrent.Body.String()
	for _, hidden := range []string{"o-paused", "c-on-paused", "c-paused", "c-held", "c-v1", "o-done", "q-done", "a-end"} {
		if strings.Contains(raw, hidden) {
			t.Fatalf("default json contains %s", hidden)
		}
	}

	htmlCh := do(t, http.MethodGet, "/channels")
	jsonCh := do(t, http.MethodGet, "/api/channels")
	chBody := htmlCh.Body.String()
	assertContains(t, chBody, "health degraded", "redline 0.25", "occupied 1 concurrency 4",
		"window 5h remaining 0.5 confidence 1 reset 2026-10-31T12:00:00Z",
		"window weekly remaining 0.25 confidence 0.5 reset 2026-11-02T00:00:00Z",
		"occupied 0 concurrency 2")
	assertPollerReloads(t, chBody)
	channels := decode[channelsView](t, jsonCh)
	main := channelByID(channels.Channels, "ch-main")
	if main.Health != "degraded" || main.Redline != 0.25 || main.Occupied != 1 || main.Concurrency != 4 {
		t.Fatalf("channel %#v", main)
	}
	if len(main.Windows) != 2 || main.Windows[0].Name != "5h" || main.Windows[0].Remaining == nil || *main.Windows[0].Remaining != 0.5 || main.Windows[0].Confidence != 1 || main.Windows[0].ResetAt != "2026-10-31T12:00:00Z" {
		t.Fatalf("windows %#v", main.Windows)
	}
	idle := channelByID(channels.Channels, "ch-idle")
	if idle.Occupied != 0 || idle.Concurrency != 2 || idle.Health != "ok" {
		t.Fatalf("idle %#v", idle)
	}

	htmlCard := do(t, http.MethodGet, "/cards/c-run")
	jsonCard := do(t, http.MethodGet, "/api/cards/c-run")
	cardBody := htmlCard.Body.String()
	assertContains(t, cardBody, "attempt a-run", "attempt a-end", "filtered-because-redline", "tests_failed", "boom-excerpt", `href="/attempts/a-end/output.log"`)
	card := decode[cardView](t, jsonCard)
	if card.ID != "c-run" || len(card.Attempts) != 2 {
		t.Fatalf("card %#v", card)
	}
	ended := attemptByID(card.Attempts, "a-end")
	if ended.Outcome != "tests_failed" || ended.ErrorExcerpt != "boom-excerpt" || !ended.Ended {
		t.Fatalf("ended %#v", ended)
	}
	if len(ended.RouteReason.Filtered) != 1 || ended.RouteReason.Filtered[0].Reason != "filtered-because-redline" {
		t.Fatalf("route %#v", ended.RouteReason)
	}
	if ended.LogTailURL != "/attempts/a-end/output.log" {
		t.Fatalf("log url %s", ended.LogTailURL)
	}
	logRec := do(t, http.MethodGet, ended.LogTailURL)
	if logRec.Code != http.StatusOK {
		t.Fatalf("log status %d", logRec.Code)
	}
	logBody := logRec.Body.String()
	if !strings.Contains(logBody, "MARKER_END_PRESENT") || strings.Contains(logBody, "MARKER_EARLY_NOT_IN_TAIL") {
		t.Fatalf("log tail missing end or containing early marker, len=%d", len(logBody))
	}
	assertPollerReloads(t, cardBody)

	htmlHist := do(t, http.MethodGet, "/history")
	jsonHist := do(t, http.MethodGet, "/api/history")
	histBody := htmlHist.Body.String()
	assertContains(t, histBody, "history objective o-done", "history objective o-canceled", "history card c-v1", "history card c-held")
	assertPollerReloads(t, histBody)
	for _, hidden := range []string{"c-run", "o-live", "o-cand", "c-ready"} {
		if strings.Contains(histBody, hidden) {
			t.Fatalf("history html contains %s", hidden)
		}
	}
	hist := decode[historyView](t, jsonHist)
	ids := itemIDs(hist.Items)
	for _, id := range []string{"o-done", "o-canceled", "c-v1", "c-held"} {
		if !ids[id] {
			t.Fatalf("history missing %s in %#v", id, hist.Items)
		}
	}
	for _, id := range []string{"o-live", "o-cand", "c-run", "c-ready"} {
		if ids[id] {
			t.Fatalf("history includes %s", id)
		}
	}
	found := decode[historyView](t, do(t, http.MethodGet, "/api/history?q=ZeBra-Token"))
	if found.Total != 1 || len(found.Items) != 1 || found.Items[0].ID != "c-v1" {
		t.Fatalf("keyword %#v", found)
	}
	held := decode[historyView](t, do(t, http.MethodGet, "/api/history?status=held"))
	if held.Total != 1 || held.Items[0].ID != "c-held" {
		t.Fatalf("status %#v", held)
	}

	after := snapshot(t, db)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("GET requests mutated the store")
	}
	_ = home
}

// TestPendingQuestionsOnOpenObjectives is the regression for unanswered
// questions on active, paused, and done_candidate objectives. It drives
// Handler for GET / and GET /api/work. Paused and done_candidate stay out
// of active work and out of history.
func TestPendingQuestionsOnOpenObjectives(t *testing.T) {
	t.Chdir(t.TempDir())
	db, home := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p1", RepoPath: home}))

	for _, o := range []model.Objective{
		{ID: "o-active", Title: "live work", Status: model.ObjectiveActive},
		{ID: "o-paused", Title: "budget pause", Status: model.ObjectivePaused},
		{ID: "o-cand", Title: "waiting confirm", Status: model.ObjectiveDoneCandidate},
		{ID: "o-done", Title: "finished", Status: model.ObjectiveDone},
		{ID: "o-canceled", Title: "stopped", Status: model.ObjectiveCanceled},
	} {
		o.Project = "p1"
		o.Mode = model.ModeManual
		o.FreezeMode = model.FreezeSafe
		must(t, db.CreateObjective(ctx, &o))
	}

	for _, c := range []*model.Card{
		{ID: "c-active-wait", Objective: "o-active", Title: "active waiting", Kind: model.KindDev, Status: model.CardWaiting, WaitReason: "blocked on review"},
		{ID: "c-active-run", Objective: "o-active", Title: "active running", Kind: model.KindDev, Status: model.CardRunning, Claims: []model.Claim{{Path: "live.go", Symbol: "Run"}}},
		{ID: "c-active-merge", Objective: "o-active", Title: "active merge", Kind: model.KindDev, Status: model.CardMergeQueued},
		{ID: "c-active-paused", Objective: "o-active", Title: "paused card on live", Kind: model.KindDev, Status: model.CardPaused},
		{ID: "c-active-held", Objective: "o-active", Title: "held card on live", Kind: model.KindDev, Status: model.CardPaused, Legacy: model.Legacy{Status: "held", Reason: "operator hold"}},
		{ID: "c-paused-wait", Objective: "o-paused", Title: "waiting on paused", Kind: model.KindDev, Status: model.CardWaiting, WaitReason: "paused objective wait"},
		{ID: "c-paused-run", Objective: "o-paused", Title: "running on paused", Kind: model.KindDev, Status: model.CardRunning, Claims: []model.Claim{{Path: "paused.go", Symbol: "PausedSym"}}},
		{ID: "c-paused-merge", Objective: "o-paused", Title: "merge on paused", Kind: model.KindDev, Status: model.CardMergeQueued},
		{ID: "c-cand-wait", Objective: "o-cand", Title: "waiting on candidate", Kind: model.KindDev, Status: model.CardWaiting, WaitReason: "candidate wait"},
		{ID: "c-cand-run", Objective: "o-cand", Title: "running on candidate", Kind: model.KindDev, Status: model.CardRunning, Claims: []model.Claim{{Path: "cand.go", Symbol: "CandSym"}}},
		{ID: "c-cand-merge", Objective: "o-cand", Title: "merge on candidate", Kind: model.KindDev, Status: model.CardMergeQueued},
	} {
		must(t, db.CreateCard(ctx, c))
	}

	started := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	for _, a := range []model.Attempt{
		{ID: "a-active", Card: "c-active-run"},
		{ID: "a-paused", Card: "c-paused-run"},
		{ID: "a-cand", Card: "c-cand-run"},
	} {
		a.Route = model.Route{Channel: "ch", Model: "fake"}
		a.StartedAt = started
		must(t, db.CreateAttempt(ctx, &a))
	}

	questions := []struct {
		id, objective, text, answer string
		kind                        model.QuestionKind
		keep                        bool
	}{
		{id: "q-active-freeze", objective: "o-active", kind: model.QuestionFreeze, text: "freeze the live module?", keep: true},
		{id: "q-active-reopen", objective: "o-active", kind: model.QuestionReopen, text: "reopen the live node?", keep: true},
		{id: "q-paused-budget", objective: "o-paused", kind: model.QuestionBudget, text: "raise the paused budget?", keep: true},
		{id: "q-paused-refusal", objective: "o-paused", kind: model.QuestionRefusal, text: "accept the paused refusal?", keep: true},
		{id: "q-cand-done", objective: "o-cand", kind: model.QuestionDone, text: "confirm the candidate is done?", keep: true},
		{id: "q-cand-breaker", objective: "o-cand", kind: model.QuestionBreaker, text: "breaker on the candidate node?", keep: true},
		{id: "q-cand-custom", objective: "o-cand", kind: model.QuestionCustom, text: "custom note on the candidate?", keep: true},
		{id: "q-active-answered", objective: "o-active", kind: model.QuestionBudget, text: "settled active budget", answer: "continue", keep: false},
		{id: "q-paused-answered", objective: "o-paused", kind: model.QuestionDone, text: "settled paused done", answer: "no", keep: false},
		{id: "q-cand-answered", objective: "o-cand", kind: model.QuestionCustom, text: "settled candidate custom", answer: "later", keep: false},
		{id: "q-done-open", objective: "o-done", kind: model.QuestionDone, text: "unanswered on a done objective", keep: false},
		{id: "q-canceled-open", objective: "o-canceled", kind: model.QuestionBudget, text: "unanswered on a canceled objective", keep: false},
	}
	for _, q := range questions {
		must(t, db.CreateQuestion(ctx, &model.Question{
			ID: q.id, Objective: q.objective, Kind: q.kind, Text: q.text, Answer: q.answer,
		}))
	}

	hiddenWork := []string{
		"objective o-paused", "objective o-cand", "objective o-done", "objective o-canceled",
		"card c-active-paused", "card c-active-held",
		"card c-paused-wait", "card c-paused-run", "card c-paused-merge",
		"card c-cand-wait", "card c-cand-run", "card c-cand-merge",
		"waiting c-paused-wait", "waiting c-cand-wait",
		"attempt a-paused", "attempt a-cand",
		"merge c-paused-merge", "merge c-cand-merge",
		"PausedSym", "CandSym",
	}

	for _, path := range []string{"/", "/api/work"} {
		rec := do(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d body %s", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, q := range questions {
			marker := "question " + q.id
			if q.keep {
				if path == "/" && !strings.Contains(body, marker+" "+q.text) {
					t.Fatalf("GET %s missing %s %q", path, q.id, q.text)
				}
				continue
			}
			if strings.Contains(body, q.id) || strings.Contains(body, q.text) {
				t.Fatalf("GET %s still shows dropped question %s", path, q.id)
			}
		}
		for _, hidden := range hiddenWork {
			if strings.Contains(body, hidden) {
				t.Fatalf("GET %s contains %s", path, hidden)
			}
		}
		if path == "/" {
			assertContains(t, body,
				"objective o-active",
				"card c-active-wait", "card c-active-run", "card c-active-merge",
				"waiting c-active-wait", "attempt a-active", "merge c-active-merge",
				"claim live.go Run",
			)
			continue
		}

		view := decode[currentView](t, rec)
		if len(view.Objectives) != 1 || view.Objectives[0].ID != "o-active" {
			t.Fatalf("objectives %#v", view.Objectives)
		}
		wantCards := map[string]bool{"c-active-wait": true, "c-active-run": true, "c-active-merge": true}
		if len(view.Cards) != len(wantCards) {
			t.Fatalf("cards %#v", view.Cards)
		}
		for _, c := range view.Cards {
			if !wantCards[c.ID] {
				t.Fatalf("unexpected card %s", c.ID)
			}
		}
		if len(view.Waiting) != 1 || view.Waiting[0].ID != "c-active-wait" {
			t.Fatalf("waiting %#v", view.Waiting)
		}
		if len(view.RunningAttempts) != 1 || view.RunningAttempts[0].ID != "a-active" || view.RunningAttempts[0].Card != "c-active-run" {
			t.Fatalf("running %#v", view.RunningAttempts)
		}
		if len(view.MergeQueue) != 1 || view.MergeQueue[0].ID != "c-active-merge" {
			t.Fatalf("merge %#v", view.MergeQueue)
		}
		if got := countOf(view.StatusCounts, "waiting"); got != 1 {
			t.Fatalf("waiting count %d", got)
		}
		if got := countOf(view.StatusCounts, "paused"); got != 0 {
			t.Fatalf("paused count %d", got)
		}
		if got := countOf(view.StatusCounts, "running"); got != 1 {
			t.Fatalf("running count %d", got)
		}
		if got := countOf(view.StatusCounts, "merge_queued"); got != 1 {
			t.Fatalf("merge count %d", got)
		}
		gotQ := map[string]questionBrief{}
		for _, q := range view.PendingQuestions {
			if _, dup := gotQ[q.ID]; dup {
				t.Fatalf("duplicate pending question %s", q.ID)
			}
			gotQ[q.ID] = q
		}
		for _, q := range questions {
			got, ok := gotQ[q.id]
			if q.keep {
				if !ok || got.Objective != q.objective || got.Text != q.text {
					t.Fatalf("pending %s: ok=%v got=%#v", q.id, ok, got)
				}
				delete(gotQ, q.id)
				continue
			}
			if ok {
				t.Fatalf("dropped question %s is pending", q.id)
			}
		}
		if len(gotQ) != 0 {
			t.Fatalf("extra pending questions %#v", gotQ)
		}
	}

	histBody := do(t, http.MethodGet, "/history").Body.String()
	assertContains(t, histBody, "history objective o-done", "history objective o-canceled", "history card c-active-held")
	for _, hidden := range []string{"history objective o-paused", "history objective o-cand", "history objective o-active", "history card c-active-paused"} {
		if strings.Contains(histBody, hidden) {
			t.Fatalf("history html contains %s", hidden)
		}
	}
	hist := decode[historyView](t, do(t, http.MethodGet, "/api/history"))
	ids := itemIDs(hist.Items)
	for _, id := range []string{"o-done", "o-canceled", "c-active-held"} {
		if !ids[id] {
			t.Fatalf("history missing %s in %#v", id, hist.Items)
		}
	}
	for _, id := range []string{"o-active", "o-paused", "o-cand", "c-active-paused", "c-paused-wait", "c-cand-merge"} {
		if ids[id] {
			t.Fatalf("history includes %s", id)
		}
	}
}

func TestHistoryPages(t *testing.T) {
	db, _ := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: t.TempDir()}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "obj-page-done", Project: "p", Title: "finished objective",
		Status: model.ObjectiveDone, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.WithTx(ctx, func(ctx context.Context) error {
		for i := 0; i < 60; i++ {
			id := pageID(i)
			title := "plain"
			status := model.CardDone
			if i == 3 || i == 57 {
				title = "zebra-token hit"
			}
			if i >= 57 {
				status = model.CardCanceled
			}
			card := &model.Card{
				ID: id, Objective: "obj-page-done", Title: title, Kind: model.KindDev, Status: status,
				Legacy: model.Legacy{SourcePath: "v1/" + id},
			}
			if err := db.CreateCard(ctx, card); err != nil {
				return err
			}
		}
		return nil
	}))

	page1 := decode[historyView](t, do(t, http.MethodGet, "/api/history"))
	page2 := decode[historyView](t, do(t, http.MethodGet, "/api/history?page=2"))
	if page1.PageSize != historyPageSize || page1.Total != 61 || len(page1.Items) != historyPageSize {
		t.Fatalf("page1 total=%d len=%d size=%d", page1.Total, len(page1.Items), page1.PageSize)
	}
	if len(page2.Items) == 0 || len(page2.Items) >= page1.Total {
		t.Fatalf("page2 len=%d total=%d", len(page2.Items), page2.Total)
	}
	seen := itemIDs(page1.Items)
	for _, item := range page2.Items {
		if seen[item.ID] {
			t.Fatalf("page2 repeats %s", item.ID)
		}
	}
	html1 := do(t, http.MethodGet, "/history").Body.String()
	html2 := do(t, http.MethodGet, "/history?page=2").Body.String()
	if strings.Contains(html1, "page-059") || !strings.Contains(html2, "page-059") {
		t.Fatal("html pages did not split page-059 onto the later page")
	}
	if strings.Contains(html2, "page-000") {
		t.Fatal("later html page repeats page-000")
	}

	kw := decode[historyView](t, do(t, http.MethodGet, "/api/history?q=ZeBra-Token"))
	kwIDs := itemIDs(kw.Items)
	if kw.Total != 2 || !kwIDs["page-003"] || !kwIDs["page-057"] {
		t.Fatalf("keyword %#v", kw.Items)
	}
	st := decode[historyView](t, do(t, http.MethodGet, "/api/history?status=canceled"))
	if st.Total != 3 {
		t.Fatalf("canceled total %d %#v", st.Total, st.Items)
	}
	for _, item := range st.Items {
		if item.Status != "canceled" {
			t.Fatalf("status filter returned %s %s", item.ID, item.Status)
		}
	}
}

func TestDefaultExcludes3000History(t *testing.T) {
	db, _ := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: t.TempDir()}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-live", Project: "p", Title: "live work",
		Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-hist", Project: "p", Title: "imported history",
		Status: model.ObjectiveDone, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "live-card", Objective: "o-live", Title: "visible now", Kind: model.KindDev, Status: model.CardReady,
	}))
	must(t, db.WithTx(ctx, func(ctx context.Context) error {
		for i := 0; i < 3000; i++ {
			id := bulkID(i)
			card := &model.Card{
				ID: id, Objective: "o-hist", Title: "old " + id, Kind: model.KindDev, Status: model.CardDone,
				Legacy: model.Legacy{Status: "done", SourcePath: "v1/" + id},
			}
			if err := db.CreateCard(ctx, card); err != nil {
				return err
			}
		}
		return nil
	}))

	for run := 1; run <= 2; run++ {
		for _, path := range []string{"/", "/api/work"} {
			start := time.Now()
			rec := do(t, http.MethodGet, path)
			elapsed := time.Since(start)
			if rec.Code != http.StatusOK {
				t.Fatalf("run %d %s status %d", run, path, rec.Code)
			}
			text := rec.Body.String()
			if strings.Contains(text, "bulk-") {
				t.Fatalf("run %d %s contains a historical card", run, path)
			}
			if !strings.Contains(text, "live-card") {
				t.Fatalf("run %d %s missing live card", run, path)
			}
			if elapsed >= 200*time.Millisecond {
				t.Fatalf("run %d %s took %s", run, path, elapsed)
			}
			excerpt := text
			if len(excerpt) > 180 {
				excerpt = excerpt[:180]
			}
			t.Logf("run=%d path=%s elapsed=%s excerpt=%q", run, path, elapsed, excerpt)
		}
	}
}

func TestHomeFlagOverridesEnv(t *testing.T) {
	envHome := t.TempDir()
	flagHome := t.TempDir()
	seedCard(t, envHome, "only-env")
	seedCard(t, flagHome, "only-flag")
	t.Setenv("CARDEX_HOME", envHome)
	old := os.Args
	os.Args = []string{"cardex", "--home", flagHome, "serve"}
	t.Cleanup(func() { os.Args = old })

	body := do(t, http.MethodGet, "/api/work").Body.String()
	if !strings.Contains(body, "only-flag") || strings.Contains(body, "only-env") {
		t.Fatalf("flag home not selected: %s", body)
	}

	eqHome := t.TempDir()
	seedCard(t, eqHome, "only-eq")
	os.Args = []string{"cardex", "--home=" + eqHome, "serve"}
	body = do(t, http.MethodGet, "/api/work").Body.String()
	if !strings.Contains(body, "only-eq") || strings.Contains(body, "only-flag") {
		t.Fatalf("--home= not selected: %s", body)
	}
}

func TestDesignTreeKeepsParentAndLayer(t *testing.T) {
	db, _ := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p-tree", RepoPath: t.TempDir()}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-tree", Project: "p-tree", Title: "nested design",
		Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateNode(ctx, &model.DesignNode{
		ID: "d-root", Objective: "o-tree", Layer: model.LayerDirection, Status: model.NodeDrafting,
	}))
	must(t, db.CreateNode(ctx, &model.DesignNode{
		ID: "m-mid", Objective: "o-tree", Parent: "d-root", Layer: model.LayerModule, Status: model.NodeFrozen,
	}))
	must(t, db.CreateNode(ctx, &model.DesignNode{
		ID: "det-leaf", Objective: "o-tree", Parent: "m-mid", Layer: model.LayerDetail, Status: model.NodeDrafting,
	}))

	for _, path := range []string{"/", "/api/work"} {
		t.Run(path, func(t *testing.T) {
			rec := do(t, http.MethodGet, path)
			if path == "/" {
				body := rec.Body.String()
				assertContains(t, body,
					"node d-root status drafting open_questions 0 parent  layer direction",
					"node m-mid status frozen open_questions 0 parent d-root layer module",
					"node det-leaf status drafting open_questions 0 parent m-mid layer detail",
				)
				assertNested(t, body, "d-root", "m-mid")
				assertNested(t, body, "m-mid", "det-leaf")
				return
			}
			view := decode[currentView](t, rec)
			if len(view.Objectives) != 1 || len(view.Objectives[0].Nodes) != 1 {
				t.Fatalf("roots %#v", view.Objectives)
			}
			root := view.Objectives[0].Nodes[0]
			if root.ID != "d-root" || root.Parent != "" || root.Layer != "direction" || len(root.Children) != 1 {
				t.Fatalf("direction %#v", root)
			}
			mid := root.Children[0]
			if mid.ID != "m-mid" || mid.Parent != "d-root" || mid.Layer != "module" || len(mid.Children) != 1 {
				t.Fatalf("module %#v", mid)
			}
			leaf := mid.Children[0]
			if leaf.ID != "det-leaf" || leaf.Parent != "m-mid" || leaf.Layer != "detail" || len(leaf.Children) != 0 {
				t.Fatalf("detail %#v", leaf)
			}
		})
	}
}

func TestCardShowsOutcomeEvidence(t *testing.T) {
	db, _ := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p-ev", RepoPath: t.TempDir()}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-ev", Project: "p-ev", Title: "evidence",
		Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-evidence", Objective: "o-ev", Title: "evidence card", Kind: model.KindDev, Status: model.CardRunning,
	}))
	reason := model.RouteReason{
		Filtered: []model.FilteredCandidate{{Channel: "ch", Model: "m", Reason: "evidence-route"}},
	}
	must(t, db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-unset", Card: "c-evidence", Index: 0, RouteReason: reason,
		Outcome: model.OutcomePassed, ErrorExcerpt: "unset-excerpt",
	}))
	must(t, db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-zero", Card: "c-evidence", Index: 1, RouteReason: reason,
		Exit: intPtr(0), BaseCommit: "base-zero", HeadCommit: "head-zero",
		Outcome: model.OutcomePassed, ErrorExcerpt: "zero-excerpt",
	}))
	must(t, db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-seven", Card: "c-evidence", Index: 2, RouteReason: reason,
		Exit: intPtr(7), BaseCommit: "base-seven", HeadCommit: "head-seven",
		Outcome: model.OutcomeTestsFailed, ErrorExcerpt: "seven-excerpt",
	}))
	must(t, db.AppendEvent(ctx, &model.Event{
		ID: "e-zero", Entity: "attempt:a-zero", Type: "zeroed", Data: json.RawMessage(`{"n":0}`),
	}))
	must(t, db.AppendEvent(ctx, &model.Event{
		ID: "e-seven", Entity: "attempt:a-seven", Type: "harvested", Data: json.RawMessage(`{"verdict":"pass-token"}`),
	}))
	must(t, db.AppendEvent(ctx, &model.Event{
		ID: "e-noise", Entity: "card:c-evidence", Type: "noise", Data: json.RawMessage(`{"nope":"card-event-noise"}`),
	}))

	for _, path := range []string{"/cards/c-evidence", "/api/cards/c-evidence"} {
		t.Run(path, func(t *testing.T) {
			rec := do(t, http.MethodGet, path)
			body := rec.Body.String()
			if strings.Contains(body, "card-event-noise") {
				t.Fatalf("unrelated event leaked: %s", body)
			}
			if path == "/cards/c-evidence" {
				unset := attemptSection(t, body, "a-unset")
				if strings.Contains(unset, "exit") {
					t.Fatalf("unset exit invented: %s", unset)
				}
				assertContains(t, unset, "outcome passed", "excerpt unset-excerpt", "evidence-route", `href="/attempts/a-unset/output.log"`)
				zero := attemptSection(t, body, "a-zero")
				assertContains(t, zero, "exit 0", "base base-zero", "head head-zero", "event zeroed", "&#34;n&#34;:0")
				seven := attemptSection(t, body, "a-seven")
				assertContains(t, seven, "outcome tests_failed", "excerpt seven-excerpt", "exit 7", "base base-seven", "head head-seven", "event harvested", "pass-token")
				return
			}
			card := decode[cardView](t, rec)
			unset := attemptByID(card.Attempts, "a-unset")
			if unset.Exit != nil || unset.BaseCommit != "" || unset.HeadCommit != "" || len(unset.Events) != 0 {
				t.Fatalf("unset %#v", unset)
			}
			if unset.Outcome != "passed" || unset.ErrorExcerpt != "unset-excerpt" || unset.LogTailURL != "/attempts/a-unset/output.log" {
				t.Fatalf("unset kept fields %#v", unset)
			}
			if !strings.Contains(body, `"exit":null`) {
				t.Fatalf("unset exit not null: %s", body)
			}
			zero := attemptByID(card.Attempts, "a-zero")
			if zero.Exit == nil || *zero.Exit != 0 || zero.BaseCommit != "base-zero" || zero.HeadCommit != "head-zero" {
				t.Fatalf("zero %#v", zero)
			}
			if len(zero.Events) != 1 || zero.Events[0].Type != "zeroed" || string(zero.Events[0].Data) != `{"n":0}` {
				t.Fatalf("zero events %#v", zero.Events)
			}
			seven := attemptByID(card.Attempts, "a-seven")
			if seven.Exit == nil || *seven.Exit != 7 || seven.BaseCommit != "base-seven" || seven.HeadCommit != "head-seven" {
				t.Fatalf("seven %#v", seven)
			}
			if seven.Outcome != "tests_failed" || seven.ErrorExcerpt != "seven-excerpt" {
				t.Fatalf("seven outcome %#v", seven)
			}
			if len(seven.Events) != 1 || seven.Events[0].Type != "harvested" || !strings.Contains(string(seven.Events[0].Data), "pass-token") {
				t.Fatalf("seven events %#v", seven.Events)
			}
		})
	}
}

func TestHistoryPageClamp(t *testing.T) {
	db, _ := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p-page", RepoPath: t.TempDir()}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-page", Project: "p-page", Title: "paging",
		Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.WithTx(ctx, func(ctx context.Context) error {
		for i := 0; i <= historyPageSize; i++ {
			id := "edge-" + leftPad(i, 3)
			if err := db.CreateCard(ctx, &model.Card{
				ID: id, Objective: "o-page", Title: id, Kind: model.KindDev, Status: model.CardDone,
				Legacy: model.Legacy{SourcePath: "v1/" + id},
			}); err != nil {
				return err
			}
		}
		return nil
	}))

	cases := []struct {
		name     string
		page     string
		wantPage int
		wantID   string
		absent   string
	}{
		{"max int", "9223372036854775807", 2, "edge-050", "edge-000"},
		{"parse overflow", "9223372036854775808", 1, "edge-000", "edge-050"},
		{"negative", "-1", 1, "edge-000", "edge-050"},
		{"min int", "-9223372036854775808", 1, "edge-000", "edge-050"},
	}
	for _, tc := range cases {
		for _, prefix := range []string{"/history", "/api/history"} {
			t.Run(tc.name+" "+prefix, func(t *testing.T) {
				rec := do(t, http.MethodGet, prefix+"?page="+tc.page)
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
				}
				body := rec.Body.String()
				if !strings.Contains(body, tc.wantID) || strings.Contains(body, tc.absent) {
					t.Fatalf("page items want %s absent %s", tc.wantID, tc.absent)
				}
				if prefix == "/api/history" {
					view := decode[historyView](t, rec)
					if view.Page != tc.wantPage || view.Total != historyPageSize+1 {
						t.Fatalf("page=%d total=%d", view.Page, view.Total)
					}
					if tc.wantPage == 2 && len(view.Items) != 1 {
						t.Fatalf("last page len %d", len(view.Items))
					}
					return
				}
				marker := "page " + strconv.Itoa(tc.wantPage) + " page_size"
				if !strings.Contains(body, marker) {
					t.Fatalf("html missing %q", marker)
				}
			})
		}
	}
}

func TestHomeLastWins(t *testing.T) {
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	envHome := t.TempDir()
	homeA := t.TempDir()
	homeB := t.TempDir()
	seedCard(t, envHome, "env-only-card")
	seedCard(t, homeA, "home-a-card")
	seedCard(t, homeB, "home-b-card")
	t.Setenv("CARDEX_HOME", envHome)

	cases := []struct {
		name   string
		args   []string
		want   string
		absent []string
	}{
		{"serve example", []string{"cardex", "--home", homeA, "serve", "--home", homeB}, "home-b-card", []string{"home-a-card", "env-only-card"}},
		{"repeat spaced", []string{"cardex", "--home", homeA, "--home", homeB, "serve"}, "home-b-card", []string{"home-a-card", "env-only-card"}},
		{"repeat equals", []string{"cardex", "--home=" + homeA, "serve", "--home=" + homeB}, "home-b-card", []string{"home-a-card", "env-only-card"}},
		{"mixed spaced then equals", []string{"cardex", "--home", homeA, "serve", "--home=" + homeB}, "home-b-card", []string{"home-a-card", "env-only-card"}},
		{"mixed equals then spaced", []string{"cardex", "--home=" + homeA, "serve", "--home", homeB}, "home-b-card", []string{"home-a-card", "env-only-card"}},
		{"flag over env", []string{"cardex", "--home", homeA, "serve"}, "home-a-card", []string{"env-only-card", "home-b-card"}},
		{"env only", []string{"cardex", "serve"}, "env-only-card", []string{"home-a-card", "home-b-card"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = tc.args
			body := do(t, http.MethodGet, "/api/work").Body.String()
			if !strings.Contains(body, tc.want) {
				t.Fatalf("missing %s in %s", tc.want, body)
			}
			for _, id := range tc.absent {
				if strings.Contains(body, id) {
					t.Fatalf("body has %s, want %s", id, tc.want)
				}
			}
		})
	}
}

func TestQuotaUnknownVersusExhausted(t *testing.T) {
	db, _ := openHome(t)
	ctx := context.Background()
	must(t, db.CreateChannel(ctx, &model.Channel{
		ID: "ch-quota", Runner: model.RunnerFake, Family: "fake", Tier: model.TierT1, Health: model.HealthOK,
		QuotaWindows: []model.QuotaWindow{
			{Name: "uncapped", Remaining: 0.9, Confidence: 0.2, ResetAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
			{Name: "empty", LimitTokens: 100, Remaining: 0, Confidence: 0.3, ResetAt: time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)},
			{Name: "partial", LimitRequests: 10, Remaining: 0.4, Confidence: 0.8, ResetAt: time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC)},
		},
	}))

	for _, path := range []string{"/channels", "/api/channels"} {
		t.Run(path, func(t *testing.T) {
			rec := do(t, http.MethodGet, path)
			body := rec.Body.String()
			if strings.Contains(body, "0.9") {
				t.Fatalf("uncapped remaining was shown: %s", body)
			}
			if path == "/channels" {
				assertContains(t, body,
					"window uncapped remaining 未知 confidence 0.2 reset 2026-12-01T00:00:00Z",
					"window empty remaining 0 confidence 0.3 reset 2026-12-02T00:00:00Z",
					"window partial remaining 0.4 confidence 0.8 reset 2026-12-03T00:00:00Z",
				)
				uncapped := windowLine(t, body, "uncapped")
				if strings.Contains(uncapped, "remaining 0") {
					t.Fatalf("unknown rendered as zero: %s", uncapped)
				}
				empty := windowLine(t, body, "empty")
				if strings.Contains(empty, "未知") {
					t.Fatalf("exhausted rendered as unknown: %s", empty)
				}
				return
			}
			view := decode[channelsView](t, rec)
			ch := channelByID(view.Channels, "ch-quota")
			if len(ch.Windows) != 3 {
				t.Fatalf("windows %#v", ch.Windows)
			}
			uncapped, empty, partial := ch.Windows[0], ch.Windows[1], ch.Windows[2]
			if uncapped.Name != "uncapped" || uncapped.Remaining != nil || uncapped.Confidence != 0.2 || uncapped.ResetAt != "2026-12-01T00:00:00Z" {
				t.Fatalf("unknown %#v", uncapped)
			}
			if empty.Name != "empty" || empty.Remaining == nil || *empty.Remaining != 0 || empty.Confidence != 0.3 {
				t.Fatalf("exhausted %#v", empty)
			}
			if partial.Remaining == nil || *partial.Remaining != 0.4 || partial.Confidence != 0.8 || partial.ResetAt != "2026-12-03T00:00:00Z" {
				t.Fatalf("partial %#v", partial)
			}
			if !strings.Contains(body, `"name":"uncapped"`) || !strings.Contains(body, `"remaining":null`) {
				t.Fatalf("unknown json %s", body)
			}
			if !strings.Contains(body, `"name":"empty"`) || !strings.Contains(body, `"remaining":0`) {
				t.Fatalf("exhausted json %s", body)
			}
		})
	}
}

func TestSafeDotDotIDs(t *testing.T) {
	db, home := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p-id", RepoPath: home}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-id", Project: "p-id", Title: "ids",
		Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c..1", Objective: "o-id", Title: "card-dotdot-title", Kind: model.KindDev, Status: model.CardReady,
	}))
	must(t, db.CreateAttempt(ctx, &model.Attempt{
		ID: "a..1", Card: "c..1", Index: 0, Outcome: model.OutcomePassed,
	}))
	dir := filepath.Join(home, "attempts", "a..1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output.log"), []byte("LOG_DOTDOT_OK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "output.log"), []byte("OUTSIDE_SECRET_HOME"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "attempts", "output.log"), []byte("OUTSIDE_SECRET_ATTEMPTS"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/cards/c..1", "/api/cards/c..1"} {
		rec := doRaw(t, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "card-dotdot-title") {
			t.Fatalf("%s status %d body %s", path, rec.Code, rec.Body.String())
		}
	}
	logRec := doRaw(t, "/attempts/a..1/output.log")
	if logRec.Code != http.StatusOK || logRec.Body.String() != "LOG_DOTDOT_OK" {
		t.Fatalf("log status %d body %q", logRec.Code, logRec.Body.String())
	}

	bad := []string{
		"/cards/", "/cards/.", "/cards/..", "/cards/a/b", "/cards/a\\b",
		"/api/cards/", "/api/cards/.", "/api/cards/..", "/api/cards/a/b", "/api/cards/a\\b",
		"/attempts//output.log", "/attempts/./output.log", "/attempts/../output.log",
		"/attempts/a/b/output.log", "/attempts/a\\b/output.log",
	}
	for _, path := range bad {
		t.Run(path, func(t *testing.T) {
			rec := doRaw(t, path)
			body := rec.Body.String()
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status %d body %s", rec.Code, body)
			}
			for _, secret := range []string{"LOG_DOTDOT_OK", "OUTSIDE_SECRET_HOME", "OUTSIDE_SECRET_ATTEMPTS", "card-dotdot-title"} {
				if strings.Contains(body, secret) {
					t.Fatalf("leaked %s from %s", secret, path)
				}
			}
		})
	}
}

func seedCurrent(t *testing.T) (*store.Store, string) {
	t.Helper()
	db, home := openHome(t)
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p1", RepoPath: home}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-live", Project: "p1", Title: "xss <script>alert(1)</script>",
		Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-paused", Project: "p1", Title: "paused objective hidden",
		Status: model.ObjectivePaused, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-done", Project: "p1", Title: "done objective",
		Status: model.ObjectiveDone, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-canceled", Project: "p1", Title: "canceled objective",
		Status: model.ObjectiveCanceled, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o-cand", Project: "p1", Title: "candidate objective",
		Status: model.ObjectiveDoneCandidate, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateNode(ctx, &model.DesignNode{
		ID: "n-mod", Objective: "o-live", Layer: model.LayerModule, Status: model.NodeFrozen,
		OpenQuestions: model.OpenQuestionList{
			{Text: "settled fact", Type: model.OQFact, Status: model.OQAnswered},
			{Text: "still open", Type: model.OQFact, Status: model.OQOpen},
			{Text: "gone stale", Type: model.OQFact, Status: model.OQStale},
		},
	}))
	must(t, db.CreateNode(ctx, &model.DesignNode{
		ID: "n-dir", Objective: "o-live", Layer: model.LayerDirection, Status: model.NodeDrafting,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-ready", Objective: "o-live", Title: "ready card", Kind: model.KindDev, Status: model.CardReady,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-wait", Objective: "o-live", Title: "waiting card", Kind: model.KindDev, Status: model.CardWaiting,
		WaitReason: "blocked by c-merge on calc.go Answer",
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-run", Objective: "o-live", Title: "running card", Kind: model.KindDev, Status: model.CardRunning,
		Claims: []model.Claim{{Path: "calc.go", Symbol: "Answer"}},
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-merge", Objective: "o-live", Title: "queue me", Kind: model.KindDev, Status: model.CardMergeQueued,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-paused", Objective: "o-live", Title: "paused card", Kind: model.KindDev, Status: model.CardPaused,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-held", Objective: "o-live", Title: "held card", Kind: model.KindDev, Status: model.CardPaused,
		Legacy: model.Legacy{Status: "held", Reason: "operator hold"},
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-v1", Objective: "o-live", Title: "zebra-token imported", Kind: model.KindDev, Status: model.CardReady,
		Legacy: model.Legacy{Status: "queued", SourcePath: "v1/queue/c-v1"},
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: "c-on-paused", Objective: "o-paused", Title: "card on paused objective", Kind: model.KindDev, Status: model.CardReady,
	}))
	reason := model.RouteReason{
		Candidates: []model.RouteCandidate{{Channel: "ch-main", Model: "fake", Remaining: 0.5}},
		Filtered:   []model.FilteredCandidate{{Channel: "ch-other", Model: "slow", Reason: "filtered-because-redline"}},
		Scores:     []model.RouteScore{{Channel: "ch-main", Model: "fake", Score: 1.5}},
	}
	must(t, db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-run", Card: "c-run", Index: 0,
		Route:       model.Route{Channel: "ch-main", Model: "fake"},
		RouteReason: reason,
		StartedAt:   time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC),
	}))
	must(t, db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-end", Card: "c-run", Index: 1,
		Route:        model.Route{Channel: "ch-main", Model: "fake"},
		RouteReason:  reason,
		StartedAt:    time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC),
		EndedAt:      time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC),
		Outcome:      model.OutcomeTestsFailed,
		ErrorClass:   model.ErrorTestsFailed,
		ErrorExcerpt: "boom-excerpt",
	}))
	must(t, db.CreateChannel(ctx, &model.Channel{
		ID: "ch-main", Runner: model.RunnerFake, Family: "fake", Tier: model.TierT2, Health: model.HealthDegraded,
		Concurrency: 4, Redline: 0.25,
		QuotaWindows: []model.QuotaWindow{
			{Name: "5h", LimitTokens: 1000, Remaining: 0.5, Confidence: 1, ResetAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)},
			{Name: "weekly", LimitTokens: 1000, Remaining: 0.25, Confidence: 0.5, ResetAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
		},
	}))
	must(t, db.CreateChannel(ctx, &model.Channel{
		ID: "ch-idle", Runner: model.RunnerFake, Family: "fake", Tier: model.TierT1, Health: model.HealthOK,
		Concurrency: 2, Redline: 0.5,
		QuotaWindows: []model.QuotaWindow{
			{Name: "daily", LimitRequests: 10, Remaining: 1, Confidence: 1, ResetAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		},
	}))
	must(t, db.CreateQuestion(ctx, &model.Question{
		ID: "q-open", Objective: "o-live", Kind: model.QuestionFreeze, Text: "freeze the direction?",
	}))
	must(t, db.CreateQuestion(ctx, &model.Question{
		ID: "q-done", Objective: "o-live", Kind: model.QuestionCustom, Text: "already settled question", Answer: "yes",
	}))
	must(t, db.CreateQuestion(ctx, &model.Question{
		ID: "q-hist", Objective: "o-done", Kind: model.QuestionDone, Text: "close the old objective",
	}))
	writeLog(t, home, "a-end")
	return db, home
}

func seedCard(t *testing.T, home, cardID string) {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	must(t, db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: home}))
	must(t, db.CreateObjective(ctx, &model.Objective{
		ID: "o", Project: "p", Title: cardID, Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}))
	must(t, db.CreateCard(ctx, &model.Card{
		ID: cardID, Objective: "o", Title: cardID, Kind: model.KindDev, Status: model.CardReady,
	}))
}

func openHome(t *testing.T) (*store.Store, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, home
}

func writeLog(t *testing.T, home, attemptID string) {
	t.Helper()
	dir := filepath.Join(home, "attempts", attemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("MARKER_EARLY_NOT_IN_TAIL\n")
	b.WriteString(strings.Repeat("x", 80*1024))
	b.WriteString("\nMARKER_END_PRESENT\n")
	if err := os.WriteFile(filepath.Join(dir, "output.log"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

type stored struct {
	Projects   []model.Project
	Objectives []model.Objective
	Nodes      []model.DesignNode
	Cards      []model.Card
	Attempts   []model.Attempt
	Channels   []model.Channel
	Questions  []model.Question
}

func snapshot(t *testing.T, db *store.Store) stored {
	t.Helper()
	ctx := context.Background()
	var s stored
	var err error
	if s.Projects, err = db.ListProjects(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Objectives, err = db.ListObjectives(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if s.Nodes, err = db.ListNodes(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if s.Cards, err = db.ListCards(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if s.Attempts, err = db.ListAttempts(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if s.Channels, err = db.ListChannels(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Questions, err = db.ListQuestions(ctx, ""); err != nil {
		t.Fatal(err)
	}
	return s
}

func do(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func doRaw(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://board.test/", nil)
	req.URL.Path = path
	Handler().ServeHTTP(rec, req)
	return rec
}

func intPtr(n int) *int {
	v := n
	return &v
}

func assertNested(t *testing.T, html, parent, child string) {
	t.Helper()
	p := strings.Index(html, "node "+parent+" ")
	c := strings.Index(html, "node "+child+" ")
	if p < 0 || c < 0 || c < p {
		t.Fatalf("expected %s inside %s; parent=%d child=%d", child, parent, p, c)
	}
	between := html[p:c]
	if strings.Contains(between, "</li>") {
		t.Fatalf("%s is not nested inside %s\n%s", child, parent, between)
	}
	if !strings.Contains(between, "<ul>") {
		t.Fatalf("no child list between %s and %s\n%s", parent, child, between)
	}
}

func attemptSection(t *testing.T, body, id string) string {
	t.Helper()
	marker := "attempt " + id
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("missing %s", marker)
	}
	rest := body[i:]
	j := strings.Index(rest, "</li>")
	if j < 0 {
		t.Fatalf("missing end of %s", id)
	}
	return rest[:j]
}

func windowLine(t *testing.T, body, name string) string {
	t.Helper()
	marker := "window " + name + " "
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("missing %s", marker)
	return ""
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// assertPollerReloads checks the inline script reloads the HTML document.
// A fetch that ignores its response leaves the server-rendered page stale.
func assertPollerReloads(t *testing.T, body string) {
	t.Helper()
	start := strings.Index(body, "<script>")
	end := strings.Index(body, "</script>")
	if start < 0 || end < start {
		t.Fatal("missing inline poller")
	}
	script := body[start:end]
	if !strings.Contains(script, "setInterval") || !strings.Contains(script, "location.reload()") {
		t.Fatalf("poller does not reload the visible document: %s", script)
	}
	if strings.Contains(script, "fetch(") {
		t.Fatalf("poller fetches without applying the response: %s", script)
	}
}

func assertContains(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(body, part) {
			t.Fatalf("body missing %q", part)
		}
	}
}

func nodeByID(nodes []nodeView, id string) nodeView {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
		if child := nodeByID(n.Children, id); child.ID == id {
			return child
		}
	}
	return nodeView{}
}

func countOf(counts []statusCount, status string) int {
	for _, c := range counts {
		if c.Status == status {
			return c.Count
		}
	}
	return -1
}

func channelByID(channels []channelView, id string) channelView {
	for _, ch := range channels {
		if ch.ID == id {
			return ch
		}
	}
	return channelView{}
}

func attemptByID(attempts []attemptView, id string) attemptView {
	for _, a := range attempts {
		if a.ID == id {
			return a
		}
	}
	return attemptView{}
}

func itemIDs(items []historyItem) map[string]bool {
	out := map[string]bool{}
	for _, item := range items {
		out[item.ID] = true
	}
	return out
}

func pageID(i int) string {
	return "page-" + leftPad(i, 3)
}

func bulkID(i int) string {
	return "bulk-" + leftPad(i, 5)
}

func leftPad(n, width int) string {
	s := ""
	for i := 0; i < width; i++ {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
