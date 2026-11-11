package loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/OttoPrua/Cardex/internal/model"
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
		if data.ReviewCardID != round.id || data.ReviewAttemptID != round.id+"-0" || data.Verdict != round.verdict || data.Opinion != round.verdict+"\n"+round.opinion+"\n" || data.Truncated || data.ReworkCount != round.count || data.QuestionOpened {
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
	section := "rework\n" + opinion + "\n"
	if !data.Truncated || data.Opinion != section[:64*1024] || strings.Contains(data.Opinion, "TAILMARK") || data.QuestionOpened || data.ReworkCount != 1 {
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
	if !verdict.QuestionOpened || verdict.ReworkCount != 2 || verdict.Verdict != "rework" || verdict.Opinion != "rework\nstill broken\n" || strings.Contains(verdict.Opinion, "NOISE-RESULT-SECRET") || verdict.ReviewAttemptID != attempt.ID {
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

func TestReviewVerdictStoresSectionText(t *testing.T) {
	sample := func(verdict string) (raw, want string) {
		body := verdict + "\n~~~sh\n# 复现\nfalse\n~~~\n取消后任务仍运行\n"
		return "## 复核结论\n" + body, body
	}
	sampleReworkRaw, sampleReworkWant := sample("rework")
	samplePassRaw, samplePassWant := sample("pass")
	backtickBody := "rework\n```lang\n# inside\nfalse\n```\nafter the fence\n"
	unclosedBody := "rework\n~~~\n# still inside\nlater line\n## 同级\nPEER-INSIDE\n"
	closedBody := "rework\n```sh\n# 复现\nfalse\n```\n取消后任务仍运行\n### 仍在本节\nnested outside"
	// Four-or-more openers keep the fenced body and the line after the closer.
	findingBody := "rework\n~~~~sh\n# 复现\nfalse\n~~~~\n取消后任务仍运行\n"
	backtick4Body := "rework\n````sh\n# 复现\nfalse\n````\n取消后任务仍运行\n"
	pass4Body := "pass\n````sh\n# 复现\nfalse\n````\n取消后任务仍运行\n"
	// A longer same-character closer restores the next section boundary.
	tildeLongerBody := "rework\n~~~sh\n# 复现\nfalse\n~~~~"
	backtickLongerBody := "rework\n```sh\n# 复现\nfalse\n````"
	// A shorter run, the other character, or an info string does not close.
	shorterBody := "rework\n~~~~sh\n# 复现\nfalse\n~~~\n## 同级\nstill inside\n~~~~\n取消后任务仍运行"
	otherBody := "rework\n~~~~sh\n```\n## 同级\nstill inside\n~~~~\n取消后任务仍运行"
	infoBody := "rework\n~~~~sh\n~~~~info\n## 同级\nstill inside\n~~~~\n取消后任务仍运行"
	fiveBody := "rework\n`````sh\n````\n# 复现\nfalse\n`````\n取消后任务仍运行"
	unclosed4Body := "rework\n````\n# still inside\nlater line\n## 同级\nPEER-INSIDE\n"
	nextSection := "\n## 下一节\n其他内容\n"
	cases := []struct {
		name         string
		verdict      string
		count        int
		wantCount    int
		status       model.CardStatus
		question     bool
		reworkEvent  bool
		note         string
		questionText string
		raw          string
		want         string
	}{
		{name: "pass", verdict: "pass", count: 0, wantCount: 0, status: model.CardMergeQueued},
		{name: "rework", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true},
		{name: "over limit", verdict: "rework", count: 2, wantCount: 2, status: model.CardNeedsInput, question: true, reworkEvent: true, questionText: "rework_count would exceed 2 (review rework)"},
		{name: "sample rework", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "~~~sh", raw: sampleReworkRaw, want: sampleReworkWant},
		{name: "sample pass", verdict: "pass", count: 0, wantCount: 0, status: model.CardMergeQueued, raw: samplePassRaw, want: samplePassWant},
		{name: "sample over limit", verdict: "rework", count: 2, wantCount: 2, status: model.CardNeedsInput, question: true, reworkEvent: true, note: "~~~sh", questionText: "rework_count would exceed 2 (review rework): ~~~sh", raw: sampleReworkRaw, want: sampleReworkWant},
		{name: "backtick fence", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "```lang", raw: "## 复核结论\n" + backtickBody, want: backtickBody},
		{name: "unclosed fence", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "~~~", raw: "## 复核结论\n" + unclosedBody, want: unclosedBody},
		{name: "heading after closed fence", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "```sh", raw: "PREAMBLE-STAYS-OUT\n## 复核结论\n" + closedBody + "\n## 下一节\nPEER-STAYS-OUT\n", want: closedBody},
		{name: "finding tilde fence", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "~~~~sh", raw: "## 复核结论\n" + findingBody, want: findingBody},
		{name: "four backtick fence", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "````sh", raw: "## 复核结论\n" + backtick4Body, want: backtick4Body},
		{name: "four backtick pass", verdict: "pass", count: 0, wantCount: 0, status: model.CardMergeQueued, raw: "## 复核结论\n" + pass4Body, want: pass4Body},
		{name: "four backtick over limit", verdict: "rework", count: 2, wantCount: 2, status: model.CardNeedsInput, question: true, reworkEvent: true, note: "````sh", questionText: "rework_count would exceed 2 (review rework): ````sh", raw: "## 复核结论\n" + backtick4Body, want: backtick4Body},
		{name: "tilde longer closer", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "~~~sh", raw: "## 复核结论\n" + tildeLongerBody + nextSection, want: tildeLongerBody},
		{name: "backtick longer closer", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "```sh", raw: "## 复核结论\n" + backtickLongerBody + nextSection, want: backtickLongerBody},
		{name: "shorter run does not close", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "~~~~sh", raw: "## 复核结论\n" + shorterBody + nextSection, want: shorterBody},
		{name: "other character does not close", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "~~~~sh ```", raw: "## 复核结论\n" + otherBody + nextSection, want: otherBody},
		{name: "info string does not close", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "~~~~sh ~~~~info", raw: "## 复核结论\n" + infoBody + nextSection, want: infoBody},
		{name: "five backtick fence", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "`````sh ````", raw: "## 复核结论\n" + fiveBody + nextSection, want: fiveBody},
		{name: "unclosed four backtick fence", verdict: "rework", count: 0, wantCount: 1, status: model.CardReady, reworkEvent: true, note: "````", raw: "## 复核结论\n" + unclosed4Body, want: unclosed4Body},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			ch := f.channel("ch-a", "a")
			f.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch.ID, ReworkCount: tc.count})
			raw := tc.raw
			want := tc.want
			if raw == "" {
				raw = "PREAMBLE-STAYS-OUT\n## 复核结论\n" + tc.verdict + "\n### 阻断项\nnested line\n## 下一节\nPEER-STAYS-OUT\n"
				want = tc.verdict + "\n### 阻断项\nnested line"
			}
			dir := writeResult(t, raw)
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
			if got.Status != tc.status || got.ReworkCount != tc.wantCount || got.ReworkNote != tc.note || strings.Contains(got.ReworkNote, "取消后任务仍运行") || strings.Contains(got.ReworkNote, "# 复现") {
				t.Fatalf("card %+v", got)
			}
			verdicts := f.events("review_verdict")
			if len(verdicts) != 1 || verdicts[0].Entity != "card:dev" {
				t.Fatalf("verdicts %+v", verdicts)
			}
			var data reviewVerdictData
			if err := json.Unmarshal(verdicts[0].Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Verdict != tc.verdict || data.Opinion != want || data.Truncated || data.QuestionOpened != tc.question || data.ReworkCount != tc.wantCount || data.ReviewCardID != review.ID || data.ReviewAttemptID != attempt.ID {
				t.Fatalf("verdict %+v", data)
			}
			if !strings.Contains(data.Opinion, "\n") {
				t.Fatalf("opinion %q", data.Opinion)
			}
			if strings.Contains(raw, "PREAMBLE-STAYS-OUT") && strings.Contains(data.Opinion, "PREAMBLE-STAYS-OUT") {
				t.Fatalf("opinion kept the preamble %q", data.Opinion)
			}
			if strings.Contains(raw, "PEER-STAYS-OUT") && !strings.Contains(want, "PEER-STAYS-OUT") && strings.Contains(data.Opinion, "PEER-STAYS-OUT") {
				t.Fatalf("opinion kept the peer section %q", data.Opinion)
			}
			if strings.Contains(raw, "其他内容") && !strings.Contains(want, "其他内容") && strings.Contains(data.Opinion, "其他内容") {
				t.Fatalf("opinion kept the next section %q", data.Opinion)
			}
			reworks := f.events("rework")
			if tc.reworkEvent {
				if len(reworks) != 1 || reworks[0].Entity != "card:dev" {
					t.Fatalf("reworks %+v", reworks)
				}
				var rework reworkData
				if err := json.Unmarshal(reworks[0].Data, &rework); err != nil {
					t.Fatal(err)
				}
				if rework.Source != "review" || rework.AttemptID != attempt.ID || rework.ReworkCount != tc.wantCount || rework.QuestionOpened != tc.question || rework.AcceptanceLog != "" || rework.Excerpt != "" {
					t.Fatalf("rework %+v", rework)
				}
			} else if len(reworks) != 0 {
				t.Fatalf("reworks %+v", reworks)
			}
			questions := f.questions()
			if tc.question {
				wantQ := tc.questionText
				if wantQ == "" {
					wantQ = "rework_count would exceed 2 (review rework)"
				}
				if len(questions) != 1 || questions[0].Text != wantQ || strings.Contains(questions[0].Text, "取消后任务仍运行") || strings.Contains(questions[0].Text, "# 复现") {
					t.Fatalf("question %+v", questions)
				}
			} else if len(questions) != 0 {
				t.Fatalf("questions %+v", questions)
			}
		})
	}

	t.Run("no section stores the full text", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		f.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch.ID})
		raw := "before the verdict\nrework\nstill\nbroken\n"
		dir := writeResult(t, raw)
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
		if got.ReworkNote != "still broken" || got.ReworkCount != 1 {
			t.Fatalf("card %+v", got)
		}
		verdicts := f.events("review_verdict")
		if len(verdicts) != 1 {
			t.Fatalf("verdicts %d", len(verdicts))
		}
		var data reviewVerdictData
		if err := json.Unmarshal(verdicts[0].Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Verdict != "rework" || data.Opinion != raw || data.Truncated || !strings.Contains(data.Opinion, "before the verdict") || !strings.Contains(data.Opinion, "\n") {
			t.Fatalf("verdict %+v", data)
		}
	})

	t.Run("utf-8 boundary", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		f.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch.ID})
		raw := "rework " + strings.Repeat("A", 65535-len("rework ")) + "中"
		dir := writeResult(t, raw)
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
		if got.ReworkCount != 1 || !strings.Contains(got.ReworkNote, "中") || strings.Contains(got.ReworkNote, "\n") {
			t.Fatalf("note %q", got.ReworkNote)
		}
		verdicts := f.events("review_verdict")
		if len(verdicts) != 1 {
			t.Fatalf("verdicts %d", len(verdicts))
		}
		var data reviewVerdictData
		if err := json.Unmarshal(verdicts[0].Data, &data); err != nil {
			t.Fatal(err)
		}
		if !data.Truncated || data.Verdict != "rework" || len(data.Opinion) > 65536 || !utf8.ValidString(data.Opinion) || strings.ContainsRune(data.Opinion, '\uFFFD') || strings.Contains(data.Opinion, "中") {
			t.Fatalf("truncated %v len %d valid %v opinion tail %q", data.Truncated, len(data.Opinion), utf8.ValidString(data.Opinion), tailRunes(data.Opinion))
		}
		if !utf8.ValidString(string(verdicts[0].Data)) || strings.Contains(strings.ToLower(string(verdicts[0].Data)), `\ufffd`) {
			t.Fatal("stored event is not valid UTF-8")
		}
	})
}

func tailRunes(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[len(s)-16:]
}
