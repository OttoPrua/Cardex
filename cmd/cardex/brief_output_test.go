package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	base := time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC)
	cutoff := base.Add(30 * time.Minute)
	later := cutoff.Add(time.Second)
	type spec struct {
		id, objective string
		status        model.CardStatus
		start, end    time.Time
		eventAt       time.Time
		eventEntity   string
	}
	specs := []spec{
		{id: "c-old", objective: "o-list", status: model.CardReady, start: base, end: base.Add(time.Minute)},
		{id: "c-edge", objective: "o-list", status: model.CardReady, start: base, end: cutoff},
		{id: "c-equal", objective: "o-list", status: model.CardReady, start: cutoff, eventAt: cutoff, eventEntity: "card"},
		{id: "c-start", objective: "o-list", status: model.CardRunning, start: later},
		{id: "c-end", objective: "o-list", status: model.CardFailed, start: base, end: later},
		{id: "c-event", objective: "o-list", status: model.CardNeedsInput, start: base, end: base.Add(time.Minute), eventAt: later, eventEntity: "card"},
		{id: "c-attev", objective: "o-list", status: model.CardReady, start: base, end: base.Add(time.Minute), eventAt: later, eventEntity: "attempt"},
		{id: "c-other", objective: "o-other", status: model.CardRunning, start: later},
	}
	for _, item := range specs {
		card := model.Card{ID: item.id, Objective: item.objective, Title: item.id, Kind: model.KindDev, Status: item.status}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		attempt := model.Attempt{ID: "a-" + item.id, Card: item.id, StartedAt: item.start, EndedAt: item.end}
		if !item.end.IsZero() {
			attempt.Outcome = model.OutcomePassed
		}
		if err := db.CreateAttempt(ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		if !item.eventAt.IsZero() {
			entity := "card:" + item.id
			if item.eventEntity == "attempt" {
				entity = "attempt:" + attempt.ID
			}
			if err := db.AppendEvent(ctx, &model.Event{ID: "e-" + item.id, TS: item.eventAt, Entity: entity, Type: "note", Data: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	since := cutoff.Format(time.RFC3339Nano)
	got := listIDs(t, mustHome(t, home, "--json", "card", "list", "--changed-since", since))
	if strings.Join(got, ",") != "c-attev,c-end,c-event,c-other,c-start" {
		t.Fatalf("changed-since %v", got)
	}
	scoped := listIDs(t, mustHome(t, home, "--json", "card", "list", "--objective", "o-list", "--changed-since", since))
	if strings.Join(scoped, ",") != "c-attev,c-end,c-event,c-start" {
		t.Fatalf("objective filter %v", scoped)
	}
	text := mustHome(t, home, "card", "list", "--changed-since", cutoff.Format(time.RFC3339))
	for _, id := range []string{"c-start", "c-end", "c-event", "c-attev", "c-other"} {
		if !strings.Contains(text, id+"\t"+id+"\n") {
			t.Fatalf("text missing %s\n%s", id, text)
		}
	}
	for _, id := range []string{"c-old", "c-edge", "c-equal"} {
		if strings.Contains(text, id+"\t") {
			t.Fatalf("text included %s\n%s", id, text)
		}
	}

	fieldsText := mustHome(t, home, "card", "list", "--objective", "o-list", "--changed-since", since, "--fields", "status,id")
	wantText := "ready\tc-attev\nfailed\tc-end\nneeds_input\tc-event\nrunning\tc-start\n"
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
	if len(rows) != 4 || rows[0]["id"] != "c-attev" || rows[0]["status"] != "ready" || len(rows[0]) != 2 {
		t.Fatalf("fields json %+v", rows)
	}
	plain := mustHome(t, home, "card", "list")
	if !strings.Contains(plain, "c-old\tc-old\n") || !strings.Contains(plain, "c-equal\tc-equal\n") {
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
		{ID: "c-need", Objective: "o-b", Title: "need", Kind: model.KindDev, Status: model.CardNeedsInput, Legacy: model.Legacy{Reason: "ask owner\r\nextra"}},
		{ID: "c-foreign", Objective: "o-foreign", Title: "foreign", Kind: model.KindDev, Status: model.CardFailed, WaitReason: "FOREIGN"},
	}
	for i := range cards {
		if err := db.CreateCard(ctx, &cards[i]); err != nil {
			t.Fatal(err)
		}
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
		"failed\t1\n",
		"needs_input\t1\n",
		"paused\t0\n",
		"blocked\tc-fail\tfailed\tdisk full\n",
		"blocked\tc-need\tneeds_input\task owner\n",
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
	if view.ID != "o-b" || len(view.Counts) != len(model.AllCardStatuses()) || len(view.Blocked) != 2 {
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
	if sum != 5 || seen["ready"] != 2 || seen["running"] != 1 || seen["failed"] != 1 || seen["needs_input"] != 1 || seen["paused"] != 0 {
		t.Fatalf("counts %+v", seen)
	}
	if view.Blocked[0].ID != "c-fail" || view.Blocked[0].Reason != "disk full" || view.Blocked[1].ID != "c-need" || view.Blocked[1].Reason != "ask owner" {
		t.Fatalf("blocked %+v", view.Blocked)
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
