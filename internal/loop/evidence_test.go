package loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestReviewRoundsKeepVerdictsAndNote(t *testing.T) {
	f := newFx(t, model.ModeManual)
	ch := f.channel("ch-a", "a")
	dev := f.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch.ID})
	f.attempt(dev, 0, "", ch.ID, "")
	rounds := []struct {
		id, verdict, opinion string
		count                int
		rework               bool
	}{
		{"rev0", "rework", "round-one opinion", 1, true},
		{"rev1", "rework", "round-two opinion", 2, true},
		{"rev2", "pass", "round-three opinion", 2, false},
	}
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for i, round := range rounds {
		dir := writeResult(t, "## 复核结论\n"+round.verdict+"\n"+round.opinion+"\n")
		review := f.card(model.Card{
			ID: round.id, Kind: model.KindReview, Tier: model.TierT3,
			Status: model.CardHarvesting, Deps: []string{"dev"},
		})
		attempt := f.attempt(review, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.ctx = WithClock(f.ctx, base.Add(time.Duration(i)*time.Second))
		f.step()
		got := f.mustCard("dev")
		if got.ReworkCount != round.count {
			t.Fatalf("round %d count %d", i, got.ReworkCount)
		}
	}
	got := f.mustCard("dev")
	if got.Status != model.CardMergeQueued || got.ReworkNote != "round-two opinion" {
		t.Fatalf("card %+v", got)
	}
	verdicts := f.events("review_verdict")
	if len(verdicts) != 3 {
		t.Fatalf("verdicts %+v", verdicts)
	}
	for i, round := range rounds {
		ev := verdicts[i]
		if ev.Entity != "card:dev" {
			t.Fatalf("entity %s", ev.Entity)
		}
		var data reviewVerdictData
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.ReviewCardID != round.id || data.ReviewAttemptID != round.id+"-0" || data.Verdict != round.verdict || data.Opinion != round.opinion || data.Truncated || data.ReworkCount != round.count || data.QuestionOpened {
			t.Fatalf("round %d %+v", i, data)
		}
	}
	reworks := f.events("rework")
	if len(reworks) != 2 {
		t.Fatalf("reworks %+v", reworks)
	}
	for i, ev := range reworks {
		var data reworkData
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		if ev.Entity != "card:dev" || data.Source != "review" || data.AttemptID != rounds[i].id+"-0" || data.ReworkCount != i+1 || data.QuestionOpened || data.AcceptanceLog != "" || data.Excerpt != "" {
			t.Fatalf("rework %d %+v", i, data)
		}
	}
}

func TestReviewOpinionTruncatesEventOnly(t *testing.T) {
	f := newFx(t, model.ModeManual)
	ch := f.channel("ch-a", "a")
	f.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch.ID})
	opinion := strings.Repeat("Z", 64*1024) + " TAILMARK"
	dir := writeResult(t, "## 复核结论\nrework\n"+opinion+"\n")
	review := f.card(model.Card{
		ID: "rev", Kind: model.KindReview, Tier: model.TierT3,
		Status: model.CardHarvesting, Deps: []string{"dev"},
	})
	attempt := f.attempt(review, 0, model.OutcomePassed, ch.ID, "")
	attempt.Worktree = dir
	if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	f.step()
	got := f.mustCard("dev")
	if got.ReworkCount != 1 || !strings.Contains(got.ReworkNote, "TAILMARK") || !strings.HasPrefix(got.ReworkNote, strings.Repeat("Z", 64*1024)) {
		t.Fatalf("note len %d", len(got.ReworkNote))
	}
	verdicts := f.events("review_verdict")
	if len(verdicts) != 1 {
		t.Fatalf("verdicts %d", len(verdicts))
	}
	var data reviewVerdictData
	if err := json.Unmarshal(verdicts[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Truncated || data.Opinion != strings.Repeat("Z", 64*1024) || strings.Contains(data.Opinion, "TAILMARK") || data.QuestionOpened || data.ReworkCount != 1 {
		t.Fatalf("data truncated %v len %d", data.Truncated, len(data.Opinion))
	}
}

func TestReworkLimitMarksBothEvents(t *testing.T) {
	f := newFx(t, model.ModeManual)
	ch := f.channel("ch-a", "a")
	f.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch.ID, ReworkCount: 2})
	dir := writeResult(t, "NOISE-RESULT-SECRET\n## 复核结论\nrework\nstill broken\n")
	review := f.card(model.Card{
		ID: "rev", Kind: model.KindReview, Tier: model.TierT3,
		Status: model.CardHarvesting, Deps: []string{"dev"},
	})
	attempt := f.attempt(review, 0, model.OutcomePassed, ch.ID, "")
	attempt.Worktree = dir
	if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	f.step()
	got := f.mustCard("dev")
	if got.ReworkCount != 2 || got.Status != model.CardNeedsInput || got.ReworkNote != "still broken" || len(f.questions()) != 1 {
		t.Fatalf("card %+v questions %+v", got, f.questions())
	}
	verdicts := f.events("review_verdict")
	reworks := f.events("rework")
	if len(verdicts) != 1 || len(reworks) != 1 {
		t.Fatalf("verdicts %d reworks %d", len(verdicts), len(reworks))
	}
	var verdict reviewVerdictData
	if err := json.Unmarshal(verdicts[0].Data, &verdict); err != nil {
		t.Fatal(err)
	}
	if !verdict.QuestionOpened || verdict.ReworkCount != 2 || verdict.Verdict != "rework" || verdict.Opinion != "still broken" || verdict.ReviewAttemptID != attempt.ID {
		t.Fatalf("verdict %+v", verdict)
	}
	var rework reworkData
	if err := json.Unmarshal(reworks[0].Data, &rework); err != nil {
		t.Fatal(err)
	}
	if rework.Source != "review" || !rework.QuestionOpened || rework.ReworkCount != 2 || rework.AttemptID != attempt.ID {
		t.Fatalf("rework %+v", rework)
	}
	blob := string(verdicts[0].Data) + string(reworks[0].Data)
	if strings.Contains(blob, "NOISE-RESULT-SECRET") {
		t.Fatalf("event data contains the result body %s", blob)
	}
}

func TestTestsFailedReworkEvent(t *testing.T) {
	t.Run("under the limit", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		excerpt := strings.Repeat("e", 2000)
		card := f.card(model.Card{ID: "dev", Status: model.CardRunning, Channel: ch.ID})
		attempt := f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, excerpt)
		f.step()
		got := f.mustCard("dev")
		if got.ReworkCount != 1 || got.Status != model.CardReady {
			t.Fatalf("card %+v", got)
		}
		reworks := f.events("rework")
		if len(reworks) != 1 || len(f.events("review_verdict")) != 0 {
			t.Fatalf("reworks %+v", reworks)
		}
		var data reworkData
		if err := json.Unmarshal(reworks[0].Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Source != "tests_failed" || data.AttemptID != attempt.ID || data.ReworkCount != 1 || data.QuestionOpened || data.AcceptanceLog != "attempts/"+attempt.ID+"/acceptance.log" || data.Excerpt != excerpt {
			t.Fatalf("data %+v", data)
		}
		if strings.Contains(string(reworks[0].Data), strings.Repeat("e", 2001)) {
			t.Fatal("event contains more than the excerpt")
		}
	})

	t.Run("over the limit", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Status: model.CardRunning, Channel: ch.ID, ReworkCount: 2})
		attempt := f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "kept-excerpt")
		f.step()
		got := f.mustCard("dev")
		if got.ReworkCount != 2 || got.Status != model.CardNeedsInput || len(f.questions()) != 1 {
			t.Fatalf("card %+v questions %+v", got, f.questions())
		}
		reworks := f.events("rework")
		if len(reworks) != 1 {
			t.Fatalf("reworks %+v", reworks)
		}
		var data reworkData
		if err := json.Unmarshal(reworks[0].Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Source != "tests_failed" || !data.QuestionOpened || data.ReworkCount != 2 || data.AttemptID != attempt.ID || data.Excerpt != "kept-excerpt" || data.AcceptanceLog != "attempts/"+attempt.ID+"/acceptance.log" {
			t.Fatalf("data %+v", data)
		}
	})

	t.Run("refusal opens a question without a rework event", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Status: model.CardRunning, Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeRefusal, ch.ID, "no")
		f.step()
		if f.mustCard("dev").ReworkCount != 0 || len(f.events("rework")) != 0 || len(f.questions()) != 1 {
			t.Fatalf("card %+v events %+v", f.mustCard("dev"), f.events("rework"))
		}
	})
}

type reviewVerdictData struct {
	ReviewCardID    string `json:"review_card_id"`
	ReviewAttemptID string `json:"review_attempt_id"`
	Verdict         string `json:"verdict"`
	Opinion         string `json:"opinion"`
	Truncated       bool   `json:"truncated"`
	ReworkCount     int    `json:"rework_count"`
	QuestionOpened  bool   `json:"question_opened"`
}

type reworkData struct {
	Source         string `json:"source"`
	AttemptID      string `json:"attempt_id"`
	ReworkCount    int    `json:"rework_count"`
	QuestionOpened bool   `json:"question_opened"`
	AcceptanceLog  string `json:"acceptance_log"`
	Excerpt        string `json:"excerpt"`
}
