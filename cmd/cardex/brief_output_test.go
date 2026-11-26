package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestCardShowBriefFoldsClaimsAndAttempts(t *testing.T) {
	home := t.TempDir()
	db := openBriefDB(t, home)
	ctx := t.Context()
	if err := db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: home}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "o-brief", Project: "p", Title: "brief objective", Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}); err != nil {
		t.Fatal(err)
	}
	const bodyMarker = "BODYMARKER"
	claims := make([]model.Claim, 0, 8)
	for i := 0; i < 8; i++ {
		claims = append(claims, model.Claim{Path: "claim-path-" + string(rune('0'+i)) + strings.Repeat("x", 40), Symbol: "Sym"})
	}
	card := model.Card{
		ID: "c-brief", Objective: "o-brief", Title: "Brief card", Kind: model.KindDev, Tier: model.TierT2,
		Status: model.CardRunning, Body: bodyMarker + strings.Repeat("B", 8000),
		WaitReason: "blocked by c-x", ReworkCount: 4, Channel: "requested-ch", Model: "requested-model",
		Claims: claims, Base: "base-sha",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC)
	rows := []model.Attempt{
		{ID: "a1", Card: card.ID, Index: 1, Route: model.Route{Channel: "live-ch", Model: "live-m"}, StartedAt: base},
		{ID: "a2", Card: card.ID, Index: 2, Route: model.Route{Channel: "ch-2", Model: "m-2"}, StartedAt: base.Add(time.Hour), EndedAt: base.Add(time.Hour + time.Minute), Outcome: model.OutcomePassed, HeadCommit: "sha-2"},
		{ID: "a3", Card: card.ID, Index: 3, Route: model.Route{Channel: "ch-3", Model: "m-3"}, StartedAt: base.Add(2 * time.Hour), EndedAt: base.Add(2*time.Hour + time.Minute), Outcome: model.OutcomePassed},
		{ID: "a4", Card: card.ID, Index: 4, Route: model.Route{Channel: "ch-4", Model: "m-4"}, StartedAt: base.Add(3 * time.Hour), EndedAt: base.Add(3*time.Hour + time.Minute), Outcome: model.OutcomeTestsFailed, HeadCommit: "sha-4"},
		{ID: "a5", Card: card.ID, Index: 5, Route: model.Route{Channel: "ch-5", Model: "m-5"}, StartedAt: base.Add(4 * time.Hour), EndedAt: base.Add(4*time.Hour + time.Minute), Outcome: model.OutcomePassed, HeadCommit: "sha-5"},
		{ID: "a6", Card: card.ID, Index: 6, Route: model.Route{Channel: "newer-ch", Model: "newer-m"}, StartedAt: base.Add(5 * time.Hour)},
	}
	for i := range rows {
		if err := db.CreateAttempt(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	events := []model.Event{
		{ID: "e-noise", TS: base, Entity: "card:" + card.ID, Type: "card_noise", Data: json.RawMessage(`{"noise":"skip"}`)},
		{ID: "e-review", TS: base.Add(5 * time.Hour), Entity: "card:" + card.ID, Type: "review_verdict", Data: json.RawMessage(`{"opinion":"old"}`)},
		{ID: "e-note", TS: base.Add(6 * time.Hour), Entity: "card:" + card.ID, Type: "note", Data: json.RawMessage(`{"opinion":"ship"}`)},
	}
	for i := range events {
		if err := db.AppendEvent(ctx, &events[i]); err != nil {
			t.Fatal(err)
		}
	}
	questions := []model.Question{
		{ID: "q-0", Objective: card.Objective, Scope: card.ID, ScopeKind: "card", Kind: model.QuestionCustom, Text: "answered\nhidden", Answer: "yes"},
		{ID: "q-a", Objective: card.Objective, Scope: card.ID, ScopeKind: "card", Kind: model.QuestionCustom, Text: "NEED A DECISION\nmore detail here"},
		{ID: "q-b", Objective: card.Objective, Scope: card.ID, ScopeKind: "card", Kind: model.QuestionCustom, Text: "later question\nnope"},
		{ID: "q-other", Objective: card.Objective, Scope: "c-other", ScopeKind: "card", Kind: model.QuestionCustom, Text: "other card"},
	}
	for i := range questions {
		if err := db.CreateQuestion(ctx, &questions[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	full := mustHome(t, home, "--json", "card", "show", card.ID)
	if len(full) < 8000 || !strings.Contains(full, bodyMarker) || !strings.Contains(full, "claim-path-0") || strings.Contains(full, `"claims_count"`) {
		t.Fatalf("default json len %d lost claims or grew a folded field", len(full))
	}
	var shown struct {
		Body     string        `json:"body"`
		Claims   []model.Claim `json:"claims"`
		Attempts []struct {
			ID string `json:"id"`
		} `json:"attempts"`
		History []struct {
			Type string `json:"type"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(full), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Body == "" || len(shown.Claims) != 8 || len(shown.Attempts) != 6 {
		t.Fatalf("default json claims %d attempts %d", len(shown.Claims), len(shown.Attempts))
	}
	if len(shown.History) != 1 || shown.History[0].Type != "review_verdict" {
		t.Fatalf("history %+v", shown.History)
	}
	text := mustHome(t, home, "card", "show", card.ID)
	if text != card.ID+"\tBrief card\n" {
		t.Fatalf("default text %q", text)
	}
	unchanged := mustHome(t, home, "card", "show", card.ID, "--claims", "--attempts", "all")
	if unchanged != text {
		t.Fatalf("flags without --brief changed the one-line text %q", unchanged)
	}

	briefText := mustHome(t, home, "card", "show", card.ID, "--brief")
	briefJSON := mustHome(t, home, "--json", "card", "show", card.ID, "--brief")
	if len(briefText) > 2048 || len(briefJSON) > 4096 || len(briefJSON) >= len(full) {
		t.Fatalf("brief text %d json %d full %d", len(briefText), len(briefJSON), len(full))
	}
	for _, banned := range []string{bodyMarker, "claim-path-0", "more detail here", "requested-ch", "later question", `"history"`} {
		if strings.Contains(briefText, banned) || strings.Contains(briefJSON, banned) {
			t.Fatalf("brief still contains %q\ntext:\n%s\njson:\n%s", banned, briefText, briefJSON)
		}
	}
	for _, want := range []string{
		"id\tc-brief\n",
		"objective\to-brief\n",
		"status\trunning\n",
		"wait_reason\tblocked by c-x\n",
		"tier\tT2\n",
		"rework_count\t4\n",
		"claims_count\t8\n",
		"attempt\ta6\trunning\tnewer-ch\tnewer-m\t2026-11-23T06:00:00Z\t\n",
		"event\t2026-11-23T07:00:00Z\tnote\n",
		"question\tq-a\tNEED A DECISION\n",
		"commit\tsha-5\n",
		"attempts\ta4\t",
		"attempts\ta5\t",
		"attempts\ta6\t",
	} {
		if !strings.Contains(briefText, want) {
			t.Fatalf("brief text missing %q\n%s", want, briefText)
		}
	}
	if strings.Contains(briefText, "attempts\ta1\t") || strings.Contains(briefText, "attempts\ta3\t") {
		t.Fatalf("brief kept more than the latest 3 attempts\n%s", briefText)
	}

	decoded := decodeBrief(t, briefJSON)
	if decoded.ID != card.ID || decoded.Objective != "o-brief" || decoded.Status != "running" || decoded.WaitReason != "blocked by c-x" || decoded.Tier != "T2" || decoded.ReworkCount != 4 || decoded.ClaimsCount != 8 {
		t.Fatalf("brief json %+v", decoded)
	}
	if decoded.Claims != nil || decoded.Attempt == nil || decoded.Attempt.ID != "a6" || decoded.Attempt.Status != "running" || decoded.Attempt.Channel != "newer-ch" || decoded.Attempt.Model != "newer-m" || decoded.Attempt.EndedAt != "" {
		t.Fatalf("attempt %+v claims %+v", decoded.Attempt, decoded.Claims)
	}
	if ids := attemptIDs(decoded.Attempts); strings.Join(ids, ",") != "a4,a5,a6" {
		t.Fatalf("attempts %v", ids)
	}
	if decoded.Event == nil || decoded.Event.Type != "note" || decoded.Event.Data.Opinion != "ship" {
		t.Fatalf("event %+v", decoded.Event)
	}
	if decoded.Question == nil || decoded.Question.ID != "q-a" || decoded.Question.Line != "NEED A DECISION" || decoded.Commit != "sha-5" {
		t.Fatalf("question %+v commit %q", decoded.Question, decoded.Commit)
	}

	claimsJSON := mustHome(t, home, "--json", "card", "show", card.ID, "--brief", "--claims")
	claimsText := mustHome(t, home, "card", "show", card.ID, "--brief", "--claims")
	if len(claimsJSON) <= len(briefJSON) || len(claimsJSON) >= len(full) || strings.Contains(claimsJSON, bodyMarker) || !strings.Contains(claimsJSON, "claim-path-0") {
		t.Fatalf("claims json len %d brief %d full %d", len(claimsJSON), len(briefJSON), len(full))
	}
	if !strings.Contains(claimsText, "claim\tclaim-path-0") || strings.Contains(claimsText, bodyMarker) {
		t.Fatalf("claims text\n%s", claimsText)
	}
	withClaims := decodeBrief(t, claimsJSON)
	if withClaims.ClaimsCount != 8 || len(withClaims.Claims) != 8 || withClaims.Claims[0].Symbol != "Sym" {
		t.Fatalf("expanded claims %+v", withClaims.Claims)
	}

	allJSON := mustHome(t, home, "--json", "card", "show", card.ID, "--brief", "--attempts", "all")
	allText := mustHome(t, home, "card", "show", card.ID, "--brief", "--attempts", "all")
	if len(allJSON) <= len(briefJSON) || len(allJSON) >= len(full) || strings.Contains(allJSON, bodyMarker) {
		t.Fatalf("all attempts json len %d brief %d full %d", len(allJSON), len(briefJSON), len(full))
	}
	all := decodeBrief(t, allJSON)
	if ids := attemptIDs(all.Attempts); strings.Join(ids, ",") != "a1,a2,a3,a4,a5,a6" {
		t.Fatalf("all attempts %v", ids)
	}
	if !strings.Contains(allText, "attempts\ta1\t") || strings.Contains(allText, bodyMarker) || len(allText) > 4096 {
		t.Fatalf("all attempts text len %d\n%s", len(allText), allText)
	}

	code, _, errb := callCLI(t, []string{"--home", home, "card", "show", card.ID, "--attempts", "3"})
	if code != 2 || !strings.Contains(errb, "--attempts") {
		t.Fatalf("bad attempts %d %s", code, errb)
	}
}

func TestCardListChangedSinceAndFields(t *testing.T) {
	home := t.TempDir()
	db := openBriefDB(t, home)
	ctx := t.Context()
	if err := db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: home}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"o-list", "o-other"} {
		if err := db.CreateObjective(ctx, &model.Objective{
			ID: id, Project: "p", Title: id, Status: model.ObjectiveActive,
			Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
		}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	type spec struct {
		id, objective string
		status        model.CardStatus
		start, end    time.Time
	}
	specs := []spec{
		{id: "c-old", objective: "o-list", status: model.CardReady, start: base, end: base.Add(time.Minute)},
		{id: "c-start", objective: "o-list", status: model.CardRunning},
		{id: "c-end", objective: "o-list", status: model.CardFailed, start: base},
		{id: "c-node", objective: "o-list", status: model.CardNeedsInput},
		{id: "c-other", objective: "o-other", status: model.CardRunning},
	}
	for _, item := range specs {
		card := model.Card{ID: item.id, Objective: item.objective, Title: item.id, Kind: model.KindDev, Status: item.status}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		if item.start.IsZero() {
			continue
		}
		attempt := model.Attempt{ID: "a-" + item.id, Card: item.id, StartedAt: item.start, EndedAt: item.end}
		if !item.end.IsZero() {
			attempt.Outcome = model.OutcomePassed
		}
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().UTC()
	for _, id := range []string{"c-start", "c-other"} {
		if err := db.CreateAttempt(ctx, &model.Attempt{Card: id, StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	ended, err := db.GetAttempt(ctx, "a-c-end")
	if err != nil {
		t.Fatal(err)
	}
	ended.EndedAt = cutoff // The attempt's write time is newer than its end time.
	ended.Outcome = model.OutcomePassed
	if err := db.UpdateAttempt(ctx, &ended); err != nil {
		t.Fatal(err)
	}
	card, err := db.GetCard(ctx, "c-node")
	if err != nil {
		t.Fatal(err)
	}
	card.Status = model.CardPaused
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendEvent(ctx, &model.Event{Entity: "card:c-old", Type: "note"}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := db.GetAttempt(ctx, "a-c-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAttempt(ctx, &unchanged); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	since := cutoff.Format(time.RFC3339Nano)
	got := listIDs(t, mustHome(t, home, "--json", "card", "list", "--changed-since", since))
	if strings.Join(got, ",") != "c-end,c-node,c-other,c-start" {
		t.Fatalf("changed-since %v", got)
	}
	scoped := listIDs(t, mustHome(t, home, "--json", "card", "list", "--objective", "o-list", "--changed-since", since))
	if strings.Join(scoped, ",") != "c-end,c-node,c-start" {
		t.Fatalf("objective filter %v", scoped)
	}
	text := mustHome(t, home, "card", "list", "--changed-since", since)
	for _, id := range []string{"c-start", "c-end", "c-node", "c-other"} {
		if !strings.Contains(text, id+"\t"+id+"\n") {
			t.Fatalf("text missing %s\n%s", id, text)
		}
	}
	if strings.Contains(text, "c-old\t") {
		t.Fatalf("text included unchanged card\n%s", text)
	}

	fieldsText := mustHome(t, home, "card", "list", "--objective", "o-list", "--changed-since", since, "--fields", "status,id")
	wantText := "failed\tc-end\npaused\tc-node\nrunning\tc-start\n"
	if fieldsText != wantText {
		t.Fatalf("fields text\n%s", fieldsText)
	}
	fieldsJSON := mustHome(t, home, "--json", "card", "list", "--objective", "o-list", "--changed-since", since, "--fields", "status", "--fields", "id")
	if strings.Contains(fieldsJSON, "title") || strings.Contains(fieldsJSON, "BODY") {
		t.Fatalf("fields json leaked %s", fieldsJSON)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(fieldsJSON), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0]["id"] != "c-end" || rows[0]["status"] != "failed" || len(rows[0]) != 2 {
		t.Fatalf("fields json %+v", rows)
	}
	plain := mustHome(t, home, "card", "list")
	if !strings.Contains(plain, "c-old\tc-old\n") {
		t.Fatalf("unfiltered list\n%s", plain)
	}

	code, _, errb := callCLI(t, []string{"--home", home, "card", "list", "--changed-since", "yesterday"})
	if code != 2 || !strings.Contains(errb, "RFC3339") {
		t.Fatalf("bad time %d %s", code, errb)
	}
	code, _, errb = callCLI(t, []string{"--home", home, "card", "list", "--fields", "nope"})
	if code != 2 || !strings.Contains(errb, "nope") {
		t.Fatalf("bad field %d %s", code, errb)
	}
	code, _, errb = callCLI(t, []string{"--home", home, "card", "list", "--fields", ""})
	if code != 2 {
		t.Fatalf("empty fields %d %s", code, errb)
	}
}

func TestObjectiveShowBriefCountsBlocked(t *testing.T) {
	home := t.TempDir()
	db := openBriefDB(t, home)
	ctx := t.Context()
	if err := db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: home}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "o-b", Project: "p", Title: "TITLETOKEN", Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "o-foreign", Project: "p", Title: "other", Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}); err != nil {
		t.Fatal(err)
	}
	cards := []model.Card{
		{ID: "c-r1", Objective: "o-b", Title: "r1", Kind: model.KindDev, Status: model.CardReady},
		{ID: "c-r2", Objective: "o-b", Title: "r2", Kind: model.KindDev, Status: model.CardReady},
		{ID: "c-run", Objective: "o-b", Title: "run", Kind: model.KindDev, Status: model.CardRunning, Body: "BODYSHOULDNOTAPPEAR" + strings.Repeat("B", 20000)},
		{ID: "c-fail", Objective: "o-b", Title: "fail", Kind: model.KindDev, Status: model.CardFailed, WaitReason: "disk full\nsecond line", Legacy: model.Legacy{Reason: "LEGACYHIDDEN"}},
		{ID: "c-need", Objective: "o-b", Title: "need", Kind: model.KindDev, Status: model.CardNeedsInput},
		{ID: "c-repair", Objective: "o-b", Title: "repair", Kind: model.KindNormalize, Status: model.CardFailed, Base: "main"},
		{ID: "c-foreign", Objective: "o-foreign", Title: "foreign", Kind: model.KindDev, Status: model.CardFailed, WaitReason: "FOREIGN"},
	}
	for i := range cards {
		if err := db.CreateCard(ctx, &cards[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateQuestion(ctx, &model.Question{
		ID: "q-need", Objective: "o-b", Scope: "c-need", ScopeKind: "card",
		Kind: model.QuestionCustom, Text: "ask owner\r\nextra",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-repair", Card: "c-repair", Outcome: model.OutcomeTestsFailed, ErrorExcerpt: "verification failed\nextra",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	full := mustHome(t, home, "--json", "objective", "show", "o-b")
	if !strings.Contains(full, "TITLETOKEN") {
		t.Fatalf("default objective %s", full)
	}
	text := mustHome(t, home, "objective", "show", "o-b", "--brief")
	raw := mustHome(t, home, "--json", "objective", "show", "o-b", "--brief")
	if len(text) > 2000 || len(raw) > 4000 || len(raw) >= len(full)+20000 {
		t.Fatalf("objective brief text %d json %d full %d", len(text), len(raw), len(full))
	}
	for _, banned := range []string{"TITLETOKEN", "second line", "LEGACYHIDDEN", "extra", "BODYSHOULDNOTAPPEAR", "FOREIGN"} {
		if strings.Contains(text, banned) || strings.Contains(raw, banned) {
			t.Fatalf("brief contains %q\n%s\n%s", banned, text, raw)
		}
	}
	for _, want := range []string{
		"id\to-b\n",
		"ready\t2\n",
		"running\t1\n",
		"failed\t2\n",
		"needs_input\t1\n",
		"paused\t0\n",
		"blocked\tc-fail\tfailed\tdisk full\n",
		"blocked\tc-need\tneeds_input\task owner\n",
		"blocked\tc-repair\tfailed\tverification failed\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	var view struct {
		ID     string `json:"id"`
		Counts []struct {
			Status string `json:"status"`
			Count  int    `json:"count"`
		} `json:"counts"`
		Blocked []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if view.ID != "o-b" || len(view.Counts) != len(model.AllCardStatuses()) || len(view.Blocked) != 3 {
		t.Fatalf("view %+v", view)
	}
	sum := 0
	seen := map[string]int{}
	for i, count := range view.Counts {
		if count.Status != string(model.AllCardStatuses()[i]) {
			t.Fatalf("count order %+v", view.Counts)
		}
		sum += count.Count
		seen[count.Status] = count.Count
	}
	if sum != 6 || seen["ready"] != 2 || seen["running"] != 1 || seen["failed"] != 2 || seen["needs_input"] != 1 || seen["paused"] != 0 {
		t.Fatalf("counts %+v", seen)
	}
	if view.Blocked[0].ID != "c-fail" || view.Blocked[0].Reason != "disk full" ||
		view.Blocked[1].ID != "c-need" || view.Blocked[1].Reason != "ask owner" ||
		view.Blocked[2].ID != "c-repair" || view.Blocked[2].Reason != "verification failed" {
		t.Fatalf("blocked %+v", view.Blocked)
	}
	card := decodeBrief(t, mustHome(t, home, "--json", "card", "show", "c-repair", "--brief"))
	if card.Commit != "" {
		t.Fatalf("branch name reported as a commit: %q", card.Commit)
	}
}

func TestBriefReportsHarvestedBlockers(t *testing.T) {
	home := t.TempDir()
	db := openBriefDB(t, home)
	defer db.Close()
	ctx := t.Context()
	if err := db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"o-dev", "o-review"} {
		if err := db.CreateObjective(ctx, &model.Objective{
			ID: id, Project: "p", Title: id, Status: model.ObjectiveActive,
			Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
		}); err != nil {
			t.Fatal(err)
		}
	}
	dev := model.Card{ID: "c-dev", Objective: "o-dev", Title: "dev", Kind: model.KindDev, Status: model.CardReviewing}
	if err := db.CreateCard(ctx, &dev); err != nil {
		t.Fatal(err)
	}
	dispose := func(card model.Card, result string) {
		t.Helper()
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		worktree := t.TempDir()
		if err := os.WriteFile(filepath.Join(worktree, ".cardex-result.md"), []byte(result), 0o644); err != nil {
			t.Fatal(err)
		}
		exit := 0
		attempt := model.Attempt{ID: "a-" + card.ID, Card: card.ID, Worktree: worktree, Exit: &exit, EndedAt: time.Now().UTC()}
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		disposeCtx := loop.WithAttempt(loop.WithStore(loop.WithHome(ctx, home), db), attempt.ID)
		if err := loop.Dispose(disposeCtx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: result}); err != nil {
			t.Fatal(err)
		}
	}
	dispose(model.Card{ID: "c-review", Objective: "o-review", Title: "review", Kind: model.KindReview, Status: model.CardRunning, Deps: []string{dev.ID}},
		"## 结论\nok\n\n## 复核结论\nblocked missing evidence\n")
	dispose(model.Card{ID: "c-plan", Objective: "o-dev", Title: "plan", Kind: model.KindPlan, Status: model.CardRunning},
		"## 新卡\n```json\n{not json\n```\n")
	questions, err := db.ListQuestions(ctx, "o-review")
	if err != nil || len(questions) != 1 || questions[0].Scope != dev.ID || questions[0].Answer != "" {
		t.Fatalf("cross-objective review questions %+v, err %v", questions, err)
	}
	questionID := questions[0].ID

	t.Run("cross-objective review question", func(t *testing.T) {
		const reason = "review blocked: missing evidence"
		view := decodeBrief(t, mustHome(t, home, "--json", "card", "show", dev.ID, "--brief"))
		if view.Status != string(model.CardNeedsInput) || view.WaitReason != "" || view.Question == nil || view.Question.ID != questionID || view.Question.Line != reason {
			t.Fatalf("blocked dev summary %+v (question %+v)", view, view.Question)
		}
		text := mustHome(t, home, "card", "show", dev.ID, "--brief")
		if !strings.Contains(text, "question\t"+questionID+"\t"+reason+"\n") {
			t.Fatalf("missing question in %q", text)
		}
	})
	t.Run("objective blocker reasons", func(t *testing.T) {
		text := mustHome(t, home, "objective", "show", "o-dev", "--brief")
		raw := mustHome(t, home, "--json", "objective", "show", "o-dev", "--brief")
		var view objectiveBriefView
		if err := json.Unmarshal([]byte(raw), &view); err != nil {
			t.Fatal(err)
		}
		want := []objectiveBlocked{
			{ID: "c-dev", Status: "needs_input", Reason: "review blocked: missing evidence"},
			{ID: "c-plan", Status: "needs_input", Reason: "plan_unparsable"},
		}
		if len(view.Blocked) != len(want) {
			t.Fatalf("blocked cards %+v", view.Blocked)
		}
		for i, blocked := range want {
			if view.Blocked[i] != blocked || !strings.Contains(text, "blocked\t"+blocked.ID+"\t"+blocked.Status+"\t"+blocked.Reason+"\n") {
				t.Errorf("missing %+v\ntext:\n%s\nJSON:\n%s", blocked, text, raw)
			}
		}
	})
}

func TestObjectiveBriefFollowsCurrentPlanBlockerAfterRetry(t *testing.T) {
	home := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p", "--repo", t.TempDir())
	mustHome(t, home, "objective", "add", "--id", "o-plan", "--project", "p", "--title", "plan")
	mustHome(t, home, "card", "add", "--id", "c-plan", "--objective", "o-plan", "--kind", "plan", "--title", "plan", "--status", "running")
	db := openBriefDB(t, home)
	defer db.Close()
	ctx := t.Context()
	dispose := func(card model.Card, index int, result harvest.Result) {
		t.Helper()
		worktree := t.TempDir()
		if err := os.WriteFile(filepath.Join(worktree, ".cardex-result.md"), []byte(result.ResultText), 0o644); err != nil {
			t.Fatal(err)
		}
		exit := 0
		attempt := model.Attempt{Card: card.ID, Index: index, Worktree: worktree, Exit: &exit, StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC()}
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		disposeCtx := loop.WithAttempt(loop.WithStore(loop.WithHome(ctx, home), db), attempt.ID)
		if err := loop.Dispose(disposeCtx, &card, result); err != nil {
			t.Fatal(err)
		}
		attempt.Outcome, attempt.ErrorExcerpt = result.Outcome, result.Excerpt
		if err := db.UpdateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
	}
	plan := func() model.Card {
		t.Helper()
		card, err := db.GetCard(ctx, "c-plan")
		if err != nil {
			t.Fatal(err)
		}
		return card
	}
	assertReason := func(reason string) {
		t.Helper()
		args := []string{"objective", "show", "o-plan", "--brief"}
		text := mustHome(t, home, args...)
		raw := mustHome(t, home, append([]string{"--json"}, args...)...)
		var view objectiveBriefView
		if err := json.Unmarshal([]byte(raw), &view); err != nil {
			t.Fatal(err)
		}
		want := objectiveBlocked{ID: "c-plan", Status: "needs_input", Reason: reason}
		if len(view.Blocked) == 0 || view.Blocked[0] != want || !strings.Contains(text, "blocked\tc-plan\tneeds_input\t"+reason+"\n") {
			t.Fatalf("want %+v, got %+v\n%s", want, view.Blocked, text)
		}
	}

	malformed := harvest.Result{Outcome: model.OutcomePassed, ResultText: "## 新卡\n```json\n{not json\n```\n"}
	dispose(plan(), 0, malformed)
	assertReason("plan_unparsable")
	mustHome(t, home, "card", "retry", "c-plan")
	dispose(plan(), 1, harvest.Result{Outcome: model.OutcomeResultUnparsable})
	cards, err := db.ListCards(ctx, "o-plan")
	if err != nil {
		t.Fatal(err)
	}
	var normalize model.Card
	for _, card := range cards {
		if card.Kind == model.KindNormalize && len(card.Deps) == 1 && card.Deps[0] == "c-plan" {
			normalize = card
		}
	}
	if normalize.ID == "" {
		t.Fatalf("retry did not produce a linked normalize card: %+v", cards)
	}
	dispose(normalize, 0, harvest.Result{Outcome: model.OutcomeTimeout, Excerpt: "executor exceeded its deadline\nmore detail"})
	assertReason("normalize " + normalize.ID + ": timeout (executor exceeded its deadline)")

	mustHome(t, home, "card", "retry", "c-plan")
	dispose(plan(), 2, malformed)
	assertReason("plan_unparsable")

	mustHome(t, home, "card", "retry", normalize.ID)
	normalize, err = db.GetCard(ctx, normalize.ID)
	if err != nil {
		t.Fatal(err)
	}
	dispose(normalize, 1, harvest.Result{Outcome: model.OutcomeTimeout, Excerpt: "old normalize timed out again"})
	assertReason("plan_unparsable")

	mustHome(t, home, "card", "retry", "c-plan")
	dispose(plan(), 3, harvest.Result{Outcome: model.OutcomeResultUnparsable})
	assertReason("result_unparsable")

	// With no parse event for this result, the source-attempt link must still
	// prevent a later failure of the old normalizer from replacing the reason.
	mustHome(t, home, "card", "retry", normalize.ID)
	normalize, err = db.GetCard(ctx, normalize.ID)
	if err != nil {
		t.Fatal(err)
	}
	dispose(normalize, 2, harvest.Result{Outcome: model.OutcomeTimeout})
	assertReason("result_unparsable")
}

func TestBriefLatestEventUsesParsedTime(t *testing.T) {
	home := t.TempDir()
	db := openBriefDB(t, home)
	ctx := t.Context()
	if err := db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: home}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "o-time", Project: "p", Title: "time", Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}); err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "c-time", Objective: "o-time", Title: "time", Kind: model.KindDev, Status: model.CardReady}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	whole := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	events := []model.Event{
		{ID: "e-a", TS: whole.Add(500 * time.Millisecond), Entity: "card:" + card.ID, Type: "late", Data: json.RawMessage(`{"opinion":"later"}`)},
		{ID: "e-0", TS: whole.Add(-time.Hour), Entity: "card:" + card.ID, Type: "older", Data: json.RawMessage(`{"opinion":"older"}`)},
		{ID: "e-z", TS: whole, Entity: "card:" + card.ID, Type: "early", Data: json.RawMessage(`{"opinion":"earlier"}`)},
	}
	for i := range events {
		if err := db.AppendEvent(ctx, &events[i]); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := db.ListEvents(ctx, "card:"+card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 || listed[0].Type != "older" || listed[len(listed)-1].Type != "early" {
		t.Fatalf("text order %+v", listed)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	view := decodeBrief(t, mustHome(t, home, "--json", "card", "show", card.ID, "--brief"))
	if view.Event == nil || view.Event.Type != "late" || view.Event.Data.Opinion != "later" {
		t.Fatalf("event %+v", view.Event)
	}
	text := mustHome(t, home, "card", "show", card.ID, "--brief")
	if !strings.Contains(text, "event\t2026-11-24T12:00:00.5Z\tlate\n") {
		t.Fatalf("text\n%s", text)
	}
}

func TestBriefClipsLongEventAndFreeText(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o-long", "--project", "p", "--title", "long")
	mustHome(t, home, "card", "add", "--id", "c-long", "--objective", "o-long", "--title", "long", "--kind", "dev", "--status", "merge_queued")
	note := strings.Repeat("N", 64*1024) + "NOTETAIL"
	mustHome(t, home, "card", "merged", "--note", note, "c-long")

	wait := "等" + strings.Repeat("😀", 4000) + "WAITTAIL"
	ask := strings.Repeat(`"`, 4000) + "QTAIL\nsecond line"
	block := strings.Repeat("阻", 4000) + "BLOCKTAIL"
	legacy := strings.Repeat("遗", 4000) + "LEGACYTAIL"
	db := openBriefDB(t, home)
	ctx := t.Context()
	card, err := db.GetCard(ctx, "c-long")
	if err != nil {
		t.Fatal(err)
	}
	card.WaitReason = wait
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateQuestion(ctx, &model.Question{
		ID: "q-long", Objective: "o-long", Scope: "c-long", ScopeKind: "card",
		Kind: model.QuestionCustom, Text: ask,
	}); err != nil {
		t.Fatal(err)
	}
	extra := []model.Card{
		{ID: "c-block", Objective: "o-long", Title: "block", Kind: model.KindDev, Status: model.CardFailed, WaitReason: block},
		{ID: "c-legacy", Objective: "o-long", Title: "legacy", Kind: model.KindDev, Status: model.CardNeedsInput, Legacy: model.Legacy{Reason: legacy}},
	}
	for i := range extra {
		if err := db.CreateCard(ctx, &extra[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	briefJSON := mustHome(t, home, "--json", "card", "show", "c-long", "--brief")
	briefText := mustHome(t, home, "card", "show", "c-long", "--brief")
	if len(briefJSON) > 4096 || len(briefText) > 2048 {
		t.Fatalf("brief json %d text %d", len(briefJSON), len(briefText))
	}
	for _, tail := range []string{"NOTETAIL", "WAITTAIL", "QTAIL", "second line", "BLOCKTAIL", "LEGACYTAIL"} {
		if strings.Contains(briefJSON, tail) || strings.Contains(briefText, tail) {
			t.Fatalf("brief leaked %s\n%s", tail, briefJSON)
		}
	}
	var shown struct {
		WaitReason string `json:"wait_reason"`
		Event      *struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		} `json:"event"`
		Question *struct {
			ID   string `json:"id"`
			Line string `json:"line"`
		} `json:"question"`
	}
	if err := json.Unmarshal([]byte(briefJSON), &shown); err != nil {
		t.Fatal(err)
	}
	if len(shown.WaitReason) != briefTextLimit-1 || !utf8.ValidString(shown.WaitReason) || !strings.HasPrefix(shown.WaitReason, "等") {
		t.Fatalf("wait %q", shown.WaitReason)
	}
	if shown.Question == nil || shown.Question.ID != "q-long" || len(shown.Question.Line) != briefTextLimit {
		t.Fatalf("question %+v", shown.Question)
	}
	if shown.Event == nil || shown.Event.Type != "card_marked_merged" {
		t.Fatalf("event %+v", shown.Event)
	}
	var payload string
	if err := json.Unmarshal(shown.Event.Data, &payload); err != nil {
		t.Fatalf("long event data stayed an object: %s", shown.Event.Data)
	}
	if utf8.RuneCountInString(payload) != briefTextLimit || !strings.Contains(payload, "N") || strings.Contains(payload, "NOTETAIL") {
		t.Fatalf("payload %q", payload)
	}
	if !strings.Contains(briefText, "wait_reason\t"+shown.WaitReason+"\n") || !strings.Contains(briefText, "question\tq-long\t"+shown.Question.Line+"\n") {
		t.Fatalf("text\n%s", briefText)
	}

	objText := mustHome(t, home, "objective", "show", "o-long", "--brief")
	objJSON := mustHome(t, home, "--json", "objective", "show", "o-long", "--brief")
	if len(objText) > 2000 || len(objJSON) > 4000 {
		t.Fatalf("objective brief text %d json %d", len(objText), len(objJSON))
	}
	for _, tail := range []string{"BLOCKTAIL", "LEGACYTAIL", "NOTETAIL"} {
		if strings.Contains(objText, tail) || strings.Contains(objJSON, tail) {
			t.Fatalf("objective brief leaked %s\n%s", tail, objJSON)
		}
	}
	if !strings.Contains(objText, "blocked\tc-block\tfailed\t"+strings.Repeat("阻", briefTextLimit/len("阻"))+"\n") {
		t.Fatalf("objective text\n%s", objText)
	}
	if !strings.Contains(objText, "blocked\tc-legacy\tneeds_input\t"+strings.Repeat("遗", briefTextLimit/len("遗"))+"\n") {
		t.Fatalf("legacy reason\n%s", objText)
	}
}

func TestCardListChangedSinceSeesPauseAndNewCard(t *testing.T) {
	t.Setenv("CARDEX_ORIGIN", "")
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o-chg", "--project", "p", "--title", "chg")
	db := openBriefDB(t, home)
	ctx := t.Context()
	for _, id := range []string{"c-pause", "c-quiet"} {
		card := model.Card{ID: id, Objective: "o-chg", Title: id, Kind: model.KindDev, Status: model.CardReady, Tier: model.TierT1}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	since := time.Now().UTC().Format(time.RFC3339Nano)
	if got := listIDs(t, mustHome(t, home, "--json", "card", "list", "--changed-since", since)); len(got) != 0 {
		t.Fatalf("unchanged cards %v", got)
	}
	mustHome(t, home, "card", "pause", "c-pause")
	mustHome(t, home, "card", "add", "--id", "c-new", "--objective", "o-chg", "--title", "c-new", "--kind", "dev")
	mustHome(t, home, "card", "add", "--id", "c-origin", "--objective", "o-chg", "--title", "c-origin", "--kind", "dev", "--origin", "file:sess")
	got := listIDs(t, mustHome(t, home, "--json", "card", "list", "--objective", "o-chg", "--changed-since", since))
	if strings.Join(got, ",") != "c-new,c-origin,c-pause" {
		t.Fatalf("changed %v", got)
	}
	text := mustHome(t, home, "card", "list", "--objective", "o-chg", "--changed-since", since)
	if !strings.Contains(text, "c-new\tc-new\n") || !strings.Contains(text, "c-pause\tc-pause\n") || strings.Contains(text, "c-quiet\t") {
		t.Fatalf("text\n%s", text)
	}
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if got := listIDs(t, mustHome(t, home, "--json", "card", "list", "--changed-since", future)); len(got) != 0 {
		t.Fatalf("future cutoff %v", got)
	}

	afterPause := time.Now().UTC().Format(time.RFC3339Nano)
	mustHome(t, home, "card", "pause", "c-pause")
	if got := listIDs(t, mustHome(t, home, "--json", "card", "list", "--changed-since", afterPause)); len(got) != 0 {
		t.Fatalf("repeated pause moved the change time: %v", got)
	}
}

func TestDashDashEndsFlags(t *testing.T) {
	home := t.TempDir()
	db := openBriefDB(t, home)
	ctx := t.Context()
	if err := db.CreateProject(ctx, &model.Project{ID: "p", RepoPath: home}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "--brief", Project: "p", Title: "dash objective", Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}); err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "--brief", Objective: "--brief", Title: "dash card", Kind: model.KindDev, Status: model.CardReady, Tier: model.TierT1}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	card.ID = "--json"
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	text := mustHome(t, home, "card", "show", "--", "--brief")
	if text != "--brief\tdash card\n" {
		t.Fatalf("card show %q", text)
	}
	if got := mustHome(t, home, "card", "show", "--", "--json"); got != "--json\tdash card\n" {
		t.Fatalf("global flag after terminator: %q", got)
	}
	obj := mustHome(t, home, "objective", "show", "--", "--brief")
	if obj != "--brief\tdash objective\n" {
		t.Fatalf("objective show %q", obj)
	}
	brief := mustHome(t, home, "card", "show", "--brief", "--", "--brief")
	if !strings.Contains(brief, "id\t--brief\n") || !strings.Contains(brief, "status\tready\n") {
		t.Fatalf("brief\n%s", brief)
	}
	objBrief := mustHome(t, home, "objective", "show", "--brief", "--", "--brief")
	if !strings.Contains(objBrief, "id\t--brief\n") || !strings.Contains(objBrief, "ready\t2\n") {
		t.Fatalf("objective brief\n%s", objBrief)
	}
	code, _, errb := callCLI(t, []string{"--home", home, "card", "show", "--brief"})
	if code != 2 || !strings.Contains(errb, "card show <id>") {
		t.Fatalf("missing id %d %s", code, errb)
	}
	code, _, errb = callCLI(t, []string{"--home", home, "objective", "show", "--brief"})
	if code != 2 || !strings.Contains(errb, "objective show <id>") {
		t.Fatalf("missing objective id %d %s", code, errb)
	}
}

func openBriefDB(t *testing.T, home string) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

type briefPayload struct {
	ID          string        `json:"id"`
	Objective   string        `json:"objective"`
	Status      string        `json:"status"`
	WaitReason  string        `json:"wait_reason"`
	Tier        string        `json:"tier"`
	ReworkCount int           `json:"rework_count"`
	ClaimsCount int           `json:"claims_count"`
	Claims      []model.Claim `json:"claims"`
	Attempt     *struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Channel   string `json:"channel"`
		Model     string `json:"model"`
		StartedAt string `json:"started_at"`
		EndedAt   string `json:"ended_at"`
	} `json:"attempt"`
	Attempts []struct {
		ID string `json:"id"`
	} `json:"attempts"`
	Event *struct {
		Type string `json:"type"`
		Data struct {
			Opinion string `json:"opinion"`
		} `json:"data"`
	} `json:"event"`
	Question *struct {
		ID   string `json:"id"`
		Line string `json:"line"`
	} `json:"question"`
	Commit string `json:"commit"`
}

func decodeBrief(t *testing.T, raw string) briefPayload {
	t.Helper()
	var view briefPayload
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func attemptIDs(attempts []struct {
	ID string `json:"id"`
}) []string {
	out := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		out = append(out, attempt.ID)
	}
	return out
}

func listIDs(t *testing.T, raw string) []string {
	t.Helper()
	var cards []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &cards); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(cards))
	for _, card := range cards {
		out = append(out, card.ID)
	}
	return out
}
