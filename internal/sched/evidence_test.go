package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/store"
)

func TestHarvestReworkEvidence(t *testing.T) {
	t.Run("tests_failed keeps a long prefix out of the excerpt and the event", func(t *testing.T) {
		_, home, db, repo, obj := dispositionHome(t)
		seedRoutes(t, db)
		prefix := "PREFIX-UNIQUE-9f3a"
		payload := prefix + strings.Repeat("q", 2500) + "TAIL-UNIQUE-9f3a"
		card := devCard(obj, "c")
		card.Acceptance = []string{"cat payload.bin; exit 1"}
		finishHarvest(t, db, home, repo, card, model.Attempt{Route: model.Route{Channel: "cha"}}, "## 结论\nRESULT-SECRET-MARKER\n", "OUTPUT-SECRET\n", false, nil)
		if err := os.WriteFile(filepath.Join(repo, "payload.bin"), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		harvestNow(t, home, db)
		got := mustCard(t, db, "c")
		if got.ReworkCount != 1 || got.Status != model.CardReady {
			t.Fatalf("card %+v", got)
		}
		attempt := mustAttempt(t, db, "a-c")
		if attempt.Outcome != model.OutcomeTestsFailed {
			t.Fatalf("outcome %s", attempt.Outcome)
		}
		log := string(mustRead(t, filepath.Join(home, "attempts", "a-c", "acceptance.log")))
		if !strings.Contains(log, prefix) || !strings.Contains(log, "command=\"cat payload.bin; exit 1\" exit=1 duration=") {
			t.Fatal("acceptance.log missing the prefix or the command line")
		}
		if strings.Contains(attempt.ErrorExcerpt, prefix) || !strings.HasSuffix(attempt.ErrorExcerpt, "TAIL-UNIQUE-9f3a") || len(attempt.ErrorExcerpt) != 2000 {
			t.Fatalf("excerpt %q len %d", attempt.ErrorExcerpt, len(attempt.ErrorExcerpt))
		}
		if _, err := os.Stat(filepath.Join(home, "attempts", "a-c", "result.md")); err != nil {
			t.Fatal(err)
		}
		reworks := eventsOfType(t, db, "card:c", "rework")
		if len(reworks) != 1 || len(eventsOfType(t, db, "card:c", "review_verdict")) != 0 {
			t.Fatalf("reworks %d", len(reworks))
		}
		var data reworkEvent
		if err := json.Unmarshal(reworks[0].Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Source != "tests_failed" || data.AttemptID != "a-c" || data.ReworkCount != 1 || data.QuestionOpened || data.AcceptanceLog != "attempts/a-c/acceptance.log" || data.Excerpt != attempt.ErrorExcerpt {
			t.Fatalf("data %+v", data)
		}
		blob := string(reworks[0].Data)
		for _, secret := range []string{prefix, "RESULT-SECRET-MARKER", "OUTPUT-SECRET", "command="} {
			if strings.Contains(blob, secret) {
				t.Fatalf("event data contains %s", secret)
			}
		}
	})

	t.Run("an existing excerpt is kept", func(t *testing.T) {
		_, home, db, repo, obj := dispositionHome(t)
		seedRoutes(t, db)
		card := devCard(obj, "c")
		card.Acceptance = []string{"printf FRESH-OUTPUT; exit 1"}
		finishHarvest(t, db, home, repo, card, model.Attempt{
			Route: model.Route{Channel: "cha"}, ErrorExcerpt: "KEPT-EXCERPT-77",
		}, "## 结论\nRESULT-SECRET-MARKER\n", "OUTPUT-SECRET\n", false, nil)
		harvestNow(t, home, db)
		attempt := mustAttempt(t, db, "a-c")
		if attempt.ErrorExcerpt != "KEPT-EXCERPT-77" || attempt.Outcome != model.OutcomeTestsFailed {
			t.Fatalf("attempt %+v", attempt)
		}
		log := string(mustRead(t, filepath.Join(home, "attempts", "a-c", "acceptance.log")))
		if !strings.Contains(log, "FRESH-OUTPUT") {
			t.Fatalf("log %q", log)
		}
		reworks := eventsOfType(t, db, "card:c", "rework")
		if len(reworks) != 1 {
			t.Fatalf("reworks %d", len(reworks))
		}
		blob := string(reworks[0].Data)
		if !strings.Contains(blob, "KEPT-EXCERPT-77") || strings.Contains(blob, "FRESH-OUTPUT") || strings.Contains(blob, "RESULT-SECRET-MARKER") || strings.Contains(blob, "OUTPUT-SECRET") {
			t.Fatalf("data %s", blob)
		}
	})

	t.Run("review rework records both events without file bodies", func(t *testing.T) {
		_, home, db, repo, obj := dispositionHome(t)
		seedRoutes(t, db)
		plantReview(t, db, home, repo, obj, "NOISE-RESULT-SECRET\n## 复核结论\nrework\nboundary broke\n")
		if err := os.MkdirAll(filepath.Join(home, "attempts", "a-rv"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "attempts", "a-rv", "output.log"), []byte("OUTPUT-SECRET\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		harvestNow(t, home, db)
		parent := mustCard(t, db, "c1")
		if parent.ReworkCount != 1 || parent.Status != model.CardReady || parent.ReworkNote != "boundary broke" {
			t.Fatalf("parent %+v", parent)
		}
		if mustCard(t, db, "rv").Status != model.CardDone {
			t.Fatal("review not done")
		}
		verdicts := eventsOfType(t, db, "card:c1", "review_verdict")
		reworks := eventsOfType(t, db, "card:c1", "rework")
		if len(verdicts) != 1 || len(reworks) != 1 {
			t.Fatalf("verdicts %d reworks %d", len(verdicts), len(reworks))
		}
		var verdict reviewVerdictEvent
		if err := json.Unmarshal(verdicts[0].Data, &verdict); err != nil {
			t.Fatal(err)
		}
		if verdict.ReviewCardID != "rv" || verdict.ReviewAttemptID != "a-rv" || verdict.Verdict != "rework" || verdict.Opinion != "boundary broke" || verdict.Truncated || verdict.ReworkCount != 1 || verdict.QuestionOpened {
			t.Fatalf("verdict %+v", verdict)
		}
		var rework reworkEvent
		if err := json.Unmarshal(reworks[0].Data, &rework); err != nil {
			t.Fatal(err)
		}
		if rework.Source != "review" || rework.AttemptID != "a-rv" || rework.ReworkCount != 1 || rework.QuestionOpened {
			t.Fatalf("rework %+v", rework)
		}
		blob := string(verdicts[0].Data) + string(reworks[0].Data)
		if strings.Contains(blob, "NOISE-RESULT-SECRET") || strings.Contains(blob, "OUTPUT-SECRET") {
			t.Fatalf("event data contains a file body %s", blob)
		}
		acc := mustRead(t, filepath.Join(home, "attempts", "a-rv", "acceptance.log"))
		result := mustRead(t, filepath.Join(home, "attempts", "a-rv", "result.md"))
		if len(acc) != 0 || !strings.Contains(string(result), "NOISE-RESULT-SECRET") || !strings.Contains(string(result), "boundary broke") {
			t.Fatalf("acceptance %q result %q", acc, result)
		}
	})

	t.Run("a failed evidence write records one event and still reworks", func(t *testing.T) {
		_, home, db, repo, obj := dispositionHome(t)
		seedRoutes(t, db)
		card := devCard(obj, "c")
		card.Acceptance = []string{"false"}
		finishHarvest(t, db, home, repo, card, model.Attempt{Route: model.Route{Channel: "cha"}}, "", "OUTPUT-SECRET\n", false, nil)
		attempt := mustAttempt(t, db, "a-c")
		if err := os.WriteFile(filepath.Join(attempt.Worktree, ".cardex-result.md"), []byte("## 结论\nRESULT-SECRET-BLOCK\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(home, "attempts", "a-c")
		if err := os.Mkdir(filepath.Join(dir, "acceptance.log"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "result.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		harvestNow(t, home, db)
		got := mustCard(t, db, "c")
		if got.ReworkCount != 1 || got.Status != model.CardReady {
			t.Fatalf("card %+v", got)
		}
		if mustAttempt(t, db, "a-c").Outcome != model.OutcomeTestsFailed {
			t.Fatalf("outcome %s", mustAttempt(t, db, "a-c").Outcome)
		}
		failed := eventsOfType(t, db, "attempt:a-c", "evidence_write_failed")
		if len(failed) != 1 || len(eventsOfType(t, db, "", "evidence_write_failed")) != 1 {
			t.Fatalf("failed events %d", len(failed))
		}
		var data struct {
			AttemptID string `json:"attempt_id"`
			Path      string `json:"path"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal(failed[0].Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.AttemptID != "a-c" || data.Path != "attempts/a-c" || data.Error == "" {
			t.Fatalf("data %+v", data)
		}
		blob := string(failed[0].Data)
		if strings.Contains(blob, "RESULT-SECRET-BLOCK") || strings.Contains(blob, "OUTPUT-SECRET") {
			t.Fatalf("event data contains a file body %s", blob)
		}
	})

	t.Run("tests_failed over the limit marks the question", func(t *testing.T) {
		_, home, db, repo, obj := dispositionHome(t)
		card := devCard(obj, "c")
		card.Acceptance = []string{"false"}
		card.ReworkCount = 2
		finishHarvest(t, db, home, repo, card, model.Attempt{}, "", "", false, nil)
		harvestNow(t, home, db)
		got := mustCard(t, db, "c")
		if got.ReworkCount != 2 || got.Status != model.CardNeedsInput {
			t.Fatalf("card %+v", got)
		}
		if !strings.Contains(questionText(t, db), "rework_count would exceed ") {
			t.Fatalf("question %q", questionText(t, db))
		}
		reworks := eventsOfType(t, db, "card:c", "rework")
		if len(reworks) != 1 || len(eventsOfType(t, db, "card:c", "review_verdict")) != 0 {
			t.Fatalf("reworks %d", len(reworks))
		}
		var data reworkEvent
		if err := json.Unmarshal(reworks[0].Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Source != "tests_failed" || !data.QuestionOpened || data.ReworkCount != 2 || data.AttemptID != "a-c" || data.AcceptanceLog != "attempts/a-c/acceptance.log" {
			t.Fatalf("data %+v", data)
		}
	})

	t.Run("review over the limit marks both events", func(t *testing.T) {
		ctx, home, db, repo, obj := dispositionHome(t)
		plantReview(t, db, home, repo, obj, "## 复核结论\nrework\nstill broken\n")
		parent := mustCard(t, db, "c1")
		parent.ReworkCount = 2
		if err := db.UpdateCard(ctx, &parent); err != nil {
			t.Fatal(err)
		}
		harvestNow(t, home, db)
		got := mustCard(t, db, "c1")
		if got.ReworkCount != 2 || got.Status != model.CardNeedsInput || got.ReworkNote != "still broken" {
			t.Fatalf("parent %+v", got)
		}
		verdicts := eventsOfType(t, db, "card:c1", "review_verdict")
		reworks := eventsOfType(t, db, "card:c1", "rework")
		if len(verdicts) != 1 || len(reworks) != 1 {
			t.Fatalf("verdicts %d reworks %d", len(verdicts), len(reworks))
		}
		var verdict reviewVerdictEvent
		if err := json.Unmarshal(verdicts[0].Data, &verdict); err != nil {
			t.Fatal(err)
		}
		var rework reworkEvent
		if err := json.Unmarshal(reworks[0].Data, &rework); err != nil {
			t.Fatal(err)
		}
		if !verdict.QuestionOpened || verdict.ReworkCount != 2 || verdict.Opinion != "still broken" || !rework.QuestionOpened || rework.Source != "review" || rework.ReworkCount != 2 || rework.AttemptID != "a-rv" {
			t.Fatalf("verdict %+v rework %+v", verdict, rework)
		}
	})
}

func harvestNow(t *testing.T, home string, db *store.Store) {
	t.Helper()
	t.Setenv("CARDEX_HOME", home)
	r := New(home)
	if r.Dispose == nil {
		t.Fatal("production runner left Dispose nil")
	}
	if err := r.harvest(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func eventsOfType(t *testing.T, db *store.Store, entity, typ string) []model.Event {
	t.Helper()
	events, err := db.ListEvents(context.Background(), entity)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Event
	for _, event := range events {
		if event.Type == typ {
			out = append(out, event)
		}
	}
	return out
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type reviewVerdictEvent struct {
	ReviewCardID    string `json:"review_card_id"`
	ReviewAttemptID string `json:"review_attempt_id"`
	Verdict         string `json:"verdict"`
	Opinion         string `json:"opinion"`
	Truncated       bool   `json:"truncated"`
	ReworkCount     int    `json:"rework_count"`
	QuestionOpened  bool   `json:"question_opened"`
}

type reworkEvent struct {
	Source         string `json:"source"`
	AttemptID      string `json:"attempt_id"`
	ReworkCount    int    `json:"rework_count"`
	QuestionOpened bool   `json:"question_opened"`
	AcceptanceLog  string `json:"acceptance_log"`
	Excerpt        string `json:"excerpt"`
}
