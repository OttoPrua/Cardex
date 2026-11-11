package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/route"
)

func (f *fx) stepAt(now time.Time) {
	f.t.Helper()
	f.t.Setenv("CARDEX_HOME", f.home)
	if err := DevStep(WithClock(f.ctx, now), f.obj.ID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) stepErr() error {
	f.t.Helper()
	f.t.Setenv("CARDEX_HOME", f.home)
	return DevStep(f.ctx, f.obj.ID)
}

type recNote struct {
	fail error
	got  *[]Notice
}

func (r recNote) Deliver(ctx context.Context, n Notice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.fail != nil {
		return r.fail
	}
	*r.got = append(*r.got, n)
	return nil
}

func countNotices(ns []Notice, kind string) int {
	n := 0
	for _, item := range ns {
		if item.Kind == kind {
			n++
		}
	}
	return n
}

func TestFollowupTransportBackoff(t *testing.T) {
	// Distinct excerpts keep an auto breaker from folding the retries into one
	// repeated signature and pausing the card before the wait can be observed.
	for _, mode := range []model.ObjectiveMode{model.ModeManual, model.ModeAuto} {
		for _, outcome := range []model.Outcome{model.OutcomeTransport, model.OutcomeTimeout} {
			t.Run(string(mode)+"/"+string(outcome), func(t *testing.T) {
				f := newFx(t, mode)
				f.writeConfig(`{"loop":{"retry_backoff_seconds":60}}`)
				a := f.channel("ch-a", "fam-a", "ma")
				b := f.channel("ch-b", "fam-b", "mb")
				f.position("ma", "fam-a", model.TierT2)
				f.position("mb", "fam-b", model.TierT2)
				card := f.card(model.Card{ID: "dev", Channel: a.ID, Tier: model.TierT2})
				now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

				f.attempt(card, 0, outcome, a.ID, "down-0")
				f.stepAt(now)
				held := f.mustCard("dev")
				if held.Status != model.CardWaiting || held.Channel != a.ID || !strings.HasPrefix(held.WaitReason, "backoff:") {
					t.Fatalf("first hold %+v", held)
				}
				until, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(held.WaitReason, "backoff:"))
				if err != nil || !until.Equal(now.Add(time.Minute)) {
					t.Fatalf("deadline %q err %v", held.WaitReason, err)
				}
				if len(f.events("retry_backoff")) != 1 || len(f.events("auto_route")) != 0 {
					t.Fatalf("events backoff %d auto %d", len(f.events("retry_backoff")), len(f.events("auto_route")))
				}

				f.stepAt(now.Add(30 * time.Second))
				if got := f.mustCard("dev"); got.Status != model.CardWaiting || got.Channel != a.ID {
					t.Fatalf("ran before the deadline %+v", got)
				}

				f.stepAt(now.Add(time.Minute))
				if got := f.mustCard("dev"); got.Status != model.CardReady || got.Channel != a.ID || got.WaitReason != "" {
					t.Fatalf("after deadline %+v", got)
				}

				f.attempt(f.mustCard("dev"), 1, outcome, a.ID, "down-1")
				f.stepAt(now.Add(time.Minute))
				held = f.mustCard("dev")
				if held.Status != model.CardWaiting || held.Channel != a.ID {
					t.Fatalf("second hold %+v", held)
				}
				second, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(held.WaitReason, "backoff:"))
				if err != nil || !second.Equal(now.Add(3*time.Minute)) {
					t.Fatalf("second deadline %q err %v", held.WaitReason, err)
				}
				f.stepAt(now.Add(2 * time.Minute))
				if got := f.mustCard("dev"); got.Status != model.CardWaiting || got.Channel != a.ID {
					t.Fatalf("second hold released early %+v", got)
				}
				f.stepAt(now.Add(3 * time.Minute))
				if got := f.mustCard("dev"); got.Status != model.CardReady || got.Channel != a.ID {
					t.Fatalf("second release %+v", got)
				}

				f.attempt(f.mustCard("dev"), 2, outcome, a.ID, "down-2")
				f.stepAt(now.Add(3 * time.Minute))
				got := f.mustCard("dev")
				routed := latestAuto(f, "dev")
				if got.Status != model.CardReady || got.Channel != b.ID || !routed.ok || routed.channel != b.ID || routed.exclude != a.ID {
					t.Fatalf("switch %+v route %+v", got, routed)
				}
				if stored := f.objective(); stored.Channel != "" || stored.Model != "" {
					t.Fatalf("objective route cleared %+v", stored)
				}
			})
		}

		t.Run(string(mode)+"/unset base waits 30s then 60s", func(t *testing.T) {
			f := newFx(t, mode)
			a := f.channel("ch-a", "fam-a")
			b := f.channel("ch-b", "fam-b")
			card := f.card(model.Card{ID: "dev", Channel: a.ID})
			now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
			f.attempt(card, 0, model.OutcomeTransport, a.ID, "down-0")
			f.stepAt(now)
			held := f.mustCard("dev")
			until, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(held.WaitReason, "backoff:"))
			if err != nil || held.Status != model.CardWaiting || held.Channel != a.ID || !until.Equal(now.Add(30*time.Second)) {
				t.Fatalf("first %+v err %v", held, err)
			}
			f.stepAt(now.Add(30 * time.Second))
			if got := f.mustCard("dev"); got.Status != model.CardReady || got.Channel != a.ID {
				t.Fatalf("first release %+v", got)
			}
			f.attempt(f.mustCard("dev"), 1, model.OutcomeTransport, a.ID, "down-1")
			f.stepAt(now.Add(30 * time.Second))
			held = f.mustCard("dev")
			until, err = time.Parse(time.RFC3339Nano, strings.TrimPrefix(held.WaitReason, "backoff:"))
			if err != nil || held.Status != model.CardWaiting || !until.Equal(now.Add(90*time.Second)) {
				t.Fatalf("second %+v err %v", held, err)
			}
			f.attempt(f.mustCard("dev"), 2, model.OutcomeTransport, a.ID, "down-2")
			f.stepAt(now.Add(30 * time.Second))
			got := f.mustCard("dev")
			routed := latestAuto(f, "dev")
			if got.Status != model.CardReady || got.Channel != b.ID || got.WaitReason != "" || !routed.ok || routed.channel != b.ID {
				t.Fatalf("third switch %+v route %+v", got, routed)
			}
		})

		t.Run(string(mode)+"/zero backoff stays ready on the same channel", func(t *testing.T) {
			f := newFx(t, mode)
			f.writeConfig(`{"loop":{"retry_backoff_seconds":0}}`)
			a := f.channel("ch-a", "fam-a")
			card := f.card(model.Card{ID: "dev", Channel: a.ID})
			now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
			f.attempt(card, 0, model.OutcomeTransport, a.ID, "down-0")
			f.stepAt(now)
			got := f.mustCard("dev")
			if got.Status != model.CardReady || got.Channel != a.ID || got.WaitReason != "" {
				t.Fatalf("default hold %+v", got)
			}
			events := f.events("retry_backoff")
			if len(events) != 1 || !strings.Contains(string(events[0].Data), now.Format(time.RFC3339Nano)) {
				t.Fatalf("deadline event %+v", events)
			}
		})
	}
}

func TestFollowupReviewFamily(t *testing.T) {
	cases := []struct {
		name       string
		position   bool
		rowFamily  string
		wantFamily string
	}{
		{name: "positioning row of the successful model", position: true, rowFamily: "anthropic", wantFamily: "anthropic"},
		{name: "channel family when the model has no row", wantFamily: "openai"},
		{name: "empty positioning family uses the successful channel", position: true, rowFamily: "", wantFamily: "openai"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			ch := f.channel("author", "openai", "author-m")
			if tc.position {
				f.position("author-m", tc.rowFamily, model.TierT2)
			}
			card := f.card(model.Card{ID: "dev", Channel: ch.ID, AuthorFamily: "old-family"})
			attempt := f.attempt(card, 0, model.OutcomePassed, ch.ID, "")
			attempt.Route.Model = "author-m"
			if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			f.step()
			got := f.mustCard("dev")
			var review model.Card
			for _, item := range f.cards() {
				if item.Kind == model.KindReview {
					review = item
				}
			}
			if got.AuthorFamily != tc.wantFamily || review.AuthorFamily != tc.wantFamily || !strings.Contains(review.Body, tc.wantFamily) {
				t.Fatalf("dev %q review %+v", got.AuthorFamily, review)
			}
		})
	}
}

func TestFollowupNormalizeViolation(t *testing.T) {
	for _, status := range []model.CardStatus{model.CardWaiting, model.CardReviewing} {
		t.Run(string(status), func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			ch := f.channel("ch-a", "a")
			origin := f.card(model.Card{ID: "dev", Status: status, WaitReason: "normalize:norm"})
			norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{origin.ID}})
			f.attempt(norm, 0, model.OutcomeViolation, ch.ID, "touched forbidden path")
			f.step()
			if got := f.mustCard("dev"); got.Status != status || got.Status == model.CardNeedsInput || got.Status == model.CardFailed {
				t.Fatalf("original %+v", got)
			}
			if f.mustCard("norm").Status != model.CardNeedsInput {
				t.Fatalf("normalize %s", f.mustCard("norm").Status)
			}
			if len(f.events("attempt_voided")) != 1 {
				t.Fatalf("void events %+v", f.events("attempt_voided"))
			}
			found := false
			for _, question := range f.questions() {
				if strings.Contains(question.Text, "violation") {
					found = true
				}
			}
			if !found {
				t.Fatalf("questions %+v", f.questions())
			}
		})
	}
}

func TestFollowupCardListCycles(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		breaks int
	}{
		{
			name:   "one cycle loses one edge",
			raw:    `[{"key":"a","deps":["b"]},{"key":"b","deps":["a"]}]`,
			breaks: 1,
		},
		{
			name:   "two independent cycles each lose one edge",
			raw:    `[{"key":"a","deps":["b"]},{"key":"b","deps":["a"]},{"key":"c","deps":["d"]},{"key":"d","deps":["c"]}]`,
			breaks: 2,
		},
		{
			name:   "a three-card cycle and a separate pair",
			raw:    `[{"key":"a","deps":["b"]},{"key":"b","deps":["c"]},{"key":"c","deps":["a"]},{"key":"d","deps":["e"]},{"key":"e","deps":["d"]}]`,
			breaks: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := ParseCardList(tc.raw, "node", nil)
			if list.Bad || len(list.Broken) != tc.breaks || len(list.Questions) != tc.breaks || len(list.Events) != tc.breaks {
				t.Fatalf("list %+v", list)
			}
			if !listAcyclic(list.Cards) {
				t.Fatalf("still cyclic %+v", list.Cards)
			}
			for _, question := range list.Questions {
				if question.Node != "node" || !strings.Contains(question.Text, "依赖成环") {
					t.Fatalf("question %+v", question)
				}
			}
		})
	}

	t.Run("dev step hangs one question per broken cycle", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		raw := "[{\"key\":\"a\",\"deps\":[\"b\"]},{\"key\":\"b\",\"deps\":[\"a\"]},{\"key\":\"c\",\"deps\":[\"d\"]},{\"key\":\"d\",\"deps\":[\"c\"]}]"
		dir := writeResult(t, raw)
		plan := f.card(model.Card{ID: "plan", Kind: model.KindPlan, Status: model.CardHarvesting})
		attempt := f.attempt(plan, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		node, err := f.db.GetNode(f.ctx, f.node.ID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, question := range node.OpenQuestions {
			if strings.Contains(question.Text, "依赖成环") {
				n++
			}
		}
		if n != 2 || len(f.events("cycle_break")) != 2 {
			t.Fatalf("questions %+v events %d", node.OpenQuestions, len(f.events("cycle_break")))
		}
	})
}

func listAcyclic(cards []ListedCard) bool {
	ids := map[string]bool{}
	deps := map[string][]string{}
	for _, card := range cards {
		if card.Outside || card.Op == "delete" {
			continue
		}
		ids[card.Key] = true
		deps[card.Key] = card.Deps
	}
	color := map[string]int{}
	var walk func(string) bool
	walk = func(n string) bool {
		color[n] = 1
		for _, m := range deps[n] {
			if !ids[m] {
				continue
			}
			if color[m] == 1 {
				return true
			}
			if color[m] == 0 && walk(m) {
				return true
			}
		}
		color[n] = 2
		return false
	}
	for id := range ids {
		if color[id] == 0 && walk(id) {
			return false
		}
	}
	return true
}

func TestFollowupUnknownNode(t *testing.T) {
	rawMissing := `[{"key":"new1","kind":"dev","title":"Work","node":"ghost"}]`
	rawKnown := `[{"key":"new1","kind":"dev","title":"Work","node":"other"}]`
	for _, kind := range []model.CardKind{model.KindPlan, model.KindReplan} {
		t.Run(string(kind)+" missing node", func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			ch := f.channel("ch-a", "a")
			dir := writeResult(t, rawMissing)
			card := f.card(model.Card{ID: "src", Kind: kind, Status: model.CardHarvesting})
			attempt := f.attempt(card, 0, model.OutcomePassed, ch.ID, "")
			attempt.Worktree = dir
			if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			f.step()
			node, err := f.db.GetNode(f.ctx, f.node.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, question := range node.OpenQuestions {
				if strings.Contains(question.Text, "ghost") && strings.Contains(question.Text, "Work") {
					found = true
				}
			}
			if !found {
				t.Fatalf("questions %+v card %+v", node.OpenQuestions, f.mustCard("src"))
			}
		})
		t.Run(string(kind)+" existing node keeps its text", func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			f.nodeID("other", model.NodeFrozen)
			ch := f.channel("ch-a", "a")
			dir := writeResult(t, rawKnown)
			card := f.card(model.Card{ID: "src", Kind: kind, Status: model.CardHarvesting})
			attempt := f.attempt(card, 0, model.OutcomePassed, ch.ID, "")
			attempt.Worktree = dir
			if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			f.step()
			other, err := f.db.GetNode(f.ctx, "other")
			if err != nil {
				t.Fatal(err)
			}
			if len(other.OpenQuestions) != 1 || other.OpenQuestions[0].Text != "设计问题: Work (new1)" {
				t.Fatalf("other %+v", other.OpenQuestions)
			}
			current, err := f.db.GetNode(f.ctx, f.node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(current.OpenQuestions) != 0 {
				t.Fatalf("current %+v", current.OpenQuestions)
			}
		})
	}
}

func TestFollowupRedeliver(t *testing.T) {
	t.Run("auth_failed", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.ctx = WithNotifier(f.ctx, recNote{fail: fmt.Errorf("down")})
		a := f.channel("ch-a", "a")
		b := f.channel("ch-b", "b")
		f.position("fake", "a", model.TierT2)
		f.position("fake-b", "b", model.TierT2)
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		f.attempt(card, 0, model.OutcomeAuthFailed, a.ID, "token rejected")
		if err := f.stepErr(); err == nil {
			t.Fatal("expected delivery failure")
		}
		var got []Notice
		f.ctx = WithNotifier(context.Background(), recNote{got: &got})
		f.step()
		if countNotices(got, "auth_failed") != 1 {
			t.Fatalf("redeliver %+v", got)
		}
		f.step()
		if countNotices(got, "auth_failed") != 1 || f.mustCard("dev").Channel != b.ID {
			t.Fatalf("duplicate %+v channel %s", got, f.mustCard("dev").Channel)
		}
	})

	t.Run("budget_reminder", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.setBudget(100, 10000)
		f.ctx = WithNotifier(f.ctx, recNote{fail: fmt.Errorf("down")})
		ch := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		attempt := f.attempt(card, 0, model.OutcomePassed, ch.ID, "")
		attempt.Usage.Tokens = 80
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		if err := f.stepErr(); err == nil {
			t.Fatal("expected delivery failure")
		}
		var got []Notice
		f.ctx = WithNotifier(context.Background(), recNote{got: &got})
		f.step()
		if countNotices(got, "budget_reminder") != 1 {
			t.Fatalf("redeliver %+v", got)
		}
		f.step()
		if countNotices(got, "budget_reminder") != 1 {
			t.Fatalf("duplicate %+v", got)
		}
	})

	t.Run("budget", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.setBudget(0, 50)
		f.ctx = WithNotifier(f.ctx, recNote{fail: fmt.Errorf("down")})
		ch := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		attempt := f.attempt(card, 0, model.OutcomePassed, ch.ID, "")
		attempt.Usage.Tokens = 50
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		if err := f.stepErr(); err == nil {
			t.Fatal("expected delivery failure")
		}
		var got []Notice
		f.ctx = WithNotifier(context.Background(), recNote{got: &got})
		f.step()
		if countNotices(got, string(model.QuestionBudget)) != 1 {
			t.Fatalf("redeliver %+v", got)
		}
		f.step()
		if countNotices(got, string(model.QuestionBudget)) != 1 {
			t.Fatalf("duplicate %+v", got)
		}
	})

	t.Run("breaker", func(t *testing.T) {
		f := newFx(t, model.ModeAuto)
		f.setBudget(0, 1_000_000)
		f.ctx = WithNotifier(f.ctx, recNote{fail: fmt.Errorf("down")})
		ch := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "boom")
		f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "boom")
		if err := f.stepErr(); err == nil {
			t.Fatal("expected delivery failure")
		}
		var got []Notice
		f.ctx = WithNotifier(context.Background(), recNote{got: &got})
		f.step()
		if countNotices(got, string(model.QuestionBreaker)) != 1 {
			t.Fatalf("redeliver %+v", got)
		}
		f.step()
		if countNotices(got, string(model.QuestionBreaker)) != 1 {
			t.Fatalf("duplicate %+v", got)
		}
	})

	t.Run("done", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.ctx = WithNotifier(f.ctx, recNote{fail: fmt.Errorf("down")})
		f.card(model.Card{ID: "dev", Status: model.CardDone})
		if err := f.stepErr(); err == nil {
			t.Fatal("expected delivery failure")
		}
		var got []Notice
		f.ctx = WithNotifier(context.Background(), recNote{got: &got})
		f.step()
		if countNotices(got, string(model.QuestionDone)) != 1 {
			t.Fatalf("redeliver %+v", got)
		}
		f.step()
		if countNotices(got, string(model.QuestionDone)) != 1 {
			t.Fatalf("duplicate %+v", got)
		}
	})
}

func TestFollowupStallAnchor(t *testing.T) {
	now := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		status        model.CardStatus
		attemptAt     time.Time
		merges        []time.Time
		mergedMissing bool
		wantStall     bool
	}{
		{name: "latest merge_success is the anchor", status: model.CardMerged, attemptAt: now.Add(-8 * time.Hour), merges: []time.Time{now.Add(-7 * time.Hour), now.Add(-time.Hour)}, wantStall: false},
		{name: "an old merge_success stalls a recent attempt", status: model.CardReady, attemptAt: now.Add(-time.Hour), merges: []time.Time{now.Add(-7 * time.Hour)}, wantStall: true},
		{name: "merged without merge_success is unknown", status: model.CardMerged, attemptAt: now.Add(-7 * time.Hour), wantStall: false},
		{name: "never merged uses the first attempt", status: model.CardReady, attemptAt: now.Add(-7 * time.Hour), wantStall: true},
		{name: "a merged card missing its own time stays unknown", status: model.CardMerged, attemptAt: now.Add(-8 * time.Hour), merges: []time.Time{now.Add(-7 * time.Hour)}, mergedMissing: true, wantStall: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ch := breakerWorld(t)
			f.writeConfig(`{"loop":{"breaker_spend_tokens":10}}`)
			card := f.card(model.Card{ID: "spike", Kind: model.KindSpike, Tier: model.TierT2, Channel: ch.ID, Status: tc.status})
			f.backdate(f.attempt(card, 0, model.OutcomePassed, ch.ID, ""), tc.attemptAt, 11)
			for _, at := range tc.merges {
				ev := &model.Event{Entity: "card:" + card.ID, Type: "merge_success", TS: at, Data: []byte(`{}`)}
				if err := f.db.AppendEvent(f.ctx, ev); err != nil {
					t.Fatal(err)
				}
			}
			if tc.mergedMissing {
				f.card(model.Card{ID: "merged-gap", Status: model.CardMerged, Channel: ch.ID})
			}
			f.stepAt(now)
			questions := questionsKind(f, model.QuestionBreaker)
			stalled := len(questions) == 1 && strings.Contains(questions[0].Text, "no merge within stall window")
			if stalled != tc.wantStall {
				t.Fatalf("stall %v questions %+v", tc.wantStall, f.questions())
			}
			if tc.mergedMissing && f.mustCard("sib").Status != model.CardReady {
				t.Fatalf("ready card %s", f.mustCard("sib").Status)
			}
		})
	}

	// A stays merged at seven hours. B's own merge_success is one hour ago.
	// retry, resume, and revive all store ready; pause and cancel store their
	// own statuses; running is B back in progress. The newer event is the clock.
	statuses := []struct {
		name   string
		status model.CardStatus
	}{
		{name: "ready after retry", status: model.CardReady},
		{name: "ready as written by resume and revive", status: model.CardReady},
		{name: "paused", status: model.CardPaused},
		{name: "canceled", status: model.CardCanceled},
		{name: "running", status: model.CardRunning},
	}
	for _, tc := range statuses {
		t.Run(tc.name, func(t *testing.T) {
			f, ch := breakerWorld(t)
			f.writeConfig(`{"loop":{"breaker_spend_tokens":10}}`)
			a := f.card(model.Card{ID: "a", Kind: model.KindSpike, Tier: model.TierT2, Channel: ch.ID, Status: model.CardMerged})
			f.backdate(f.attempt(a, 0, model.OutcomePassed, ch.ID, ""), now.Add(-8*time.Hour), 11)
			b := f.card(model.Card{ID: "b", Kind: model.KindSpike, Tier: model.TierT2, Channel: ch.ID, Status: tc.status})
			f.backdate(f.attempt(b, 0, model.OutcomePassed, ch.ID, ""), now.Add(-2*time.Hour), 1)
			c := f.card(model.Card{ID: "c", Kind: model.KindSpike, Tier: model.TierT2, Channel: ch.ID, Status: model.CardReady})
			for _, ev := range []struct {
				id string
				at time.Time
			}{
				{a.ID, now.Add(-7 * time.Hour)},
				{b.ID, now.Add(-time.Hour)},
			} {
				event := &model.Event{Entity: "card:" + ev.id, Type: "merge_success", TS: ev.at, Data: []byte(`{}`)}
				if err := f.db.AppendEvent(f.ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			f.stepAt(now)
			for _, question := range questionsKind(f, model.QuestionBreaker) {
				if strings.Contains(question.Text, "no merge within stall window") {
					t.Fatalf("stall question %+v", f.questions())
				}
			}
			if got := f.mustCard("b").Status; got != tc.status {
				t.Fatalf("b status %s", got)
			}
			if got := f.mustCard(c.ID).Status; got != model.CardReady {
				t.Fatalf("c status %s", got)
			}
		})
	}
}

func TestFollowupFailedRoutePins(t *testing.T) {
	f := newFx(t, model.ModeManual)
	a := f.channel("ch-a", "fam-a", "ma")
	b := f.channel("ch-b", "fam-b", "mb")
	f.position("ma", "fam-a", model.TierT2)
	f.position("mb", "fam-b", model.TierT2)
	card := f.card(model.Card{ID: "c", Channel: a.ID, Model: "ma", Tier: model.TierT2, Status: model.CardHarvesting})
	obj := f.objective()
	obj.Channel = a.ID
	obj.Model = "ma"
	if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		f.attempt(card, i, model.OutcomeTransport, a.ID, "down")
	}
	f.attempt(card, 2, model.OutcomeTransport, a.ID, "down")
	f.step()
	got := f.mustCard("c")
	stored := f.objective()
	if got.Channel != b.ID || got.Model != "" || stored.Channel != a.ID || stored.Model != "ma" {
		t.Fatalf("card %+v objective %+v", got, stored)
	}
	req, err := routeRequestFrom(f.ctx, f.db, got, stored, time.Now().UTC(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if req.Card.Channel != "" || req.Card.Model != "" || req.Objective.Channel != "" || req.Objective.Model != "" {
		t.Fatalf("event pin leaked %+v objective %+v", req.Card, req.Objective)
	}
	sib := f.card(model.Card{ID: "sib", Tier: model.TierT2, Status: model.CardReady})
	sibReq, err := routeRequestFrom(f.ctx, f.db, sib, f.objective(), time.Now().UTC(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if f.objective().Channel != a.ID || f.objective().Model != "ma" || sibReq.Objective.Channel != a.ID || sibReq.Objective.Model != "ma" {
		t.Fatalf("sibling request %+v objective %+v", sibReq.Objective, f.objective())
	}
	sibPicked, sibReason, err := route.Select(f.ctx, sibReq)
	if err != nil || sibPicked.Channel != a.ID || sibPicked.Model != "ma" || !explicitPinResult(sibReason) {
		t.Fatalf("sibling picked %+v reason %+v err %v", sibPicked, sibReason, err)
	}

	got.Channel = a.ID
	got.Model = "ma"
	if err := f.db.UpdateCard(f.ctx, &got); err != nil {
		t.Fatal(err)
	}
	c := f.channel("ch-c", "fam-c", "mc")
	f.position("mc", "fam-c", model.TierT2)
	c.Redline = 0.5
	c.QuotaWindows[0].UsedTokens = 90
	f.saveChannel(c)
	stored = f.objective()
	stored.Channel = c.ID
	stored.Model = "mc"
	if err := f.db.UpdateObjective(f.ctx, &stored); err != nil {
		t.Fatal(err)
	}
	a.Redline = 0.5
	a.QuotaWindows[0].UsedTokens = 90
	f.saveChannel(a)
	req, err = routeRequestFrom(f.ctx, f.db, got, stored, time.Now().UTC(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if req.Card.Channel != a.ID || req.Card.Model != "ma" || req.Objective.Channel != c.ID || req.Objective.Model != "mc" {
		t.Fatalf("post-event pin card %+v objective %+v", req.Card, req.Objective)
	}
	picked, reason, err := route.Select(f.ctx, req)
	if err != nil {
		t.Fatalf("select %v reason %+v", err, reason)
	}
	if picked.Channel != a.ID || !explicitPinResult(reason) {
		t.Fatalf("picked %+v reason %+v", picked, reason)
	}
}

func TestFollowupNormalizeAbsorb(t *testing.T) {
	t.Run("manual design absorbs open questions and finishes", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		dir := writeResult(t, "## 开放问题\nshould the parser keep comments\n")
		parent := f.card(model.Card{ID: "design", Kind: model.KindDesign, Status: model.CardWaiting, WaitReason: "normalize:norm"})
		norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{parent.ID}})
		attempt := f.attempt(norm, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("design")
		if got.Status != model.CardDone && got.Status != model.CardMergeQueued {
			t.Fatalf("design %+v", got)
		}
		if got.Status == model.CardReady || got.Status == model.CardRunning || got.Status == model.CardWaiting {
			t.Fatalf("design left open %+v", got)
		}
		node, err := f.db.GetNode(f.ctx, f.node.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, question := range node.OpenQuestions {
			if strings.Contains(question.Text, "should the parser keep comments") {
				found = true
			}
		}
		if !found || f.mustCard("norm").Status != model.CardDone {
			t.Fatalf("node %+v norm %s", node.OpenQuestions, f.mustCard("norm").Status)
		}
	})

	for _, kind := range []model.CardKind{model.KindPlan, model.KindReplan} {
		t.Run("manual "+string(kind)+" absorbs the card list and finishes", func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			ch := f.channel("ch-a", "a")
			dir := writeResult(t, `[{"key":"child","kind":"dev","title":"Child work"}]`)
			parent := f.card(model.Card{ID: "src", Kind: kind, Status: model.CardWaiting, WaitReason: "normalize:norm"})
			norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{parent.ID}})
			attempt := f.attempt(norm, 0, model.OutcomePassed, ch.ID, "")
			attempt.Worktree = dir
			if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			f.step()
			got := f.mustCard("src")
			if got.Status != model.CardDone && got.Status != model.CardMergeQueued {
				t.Fatalf("%s %+v", kind, got)
			}
			if cardByTitle(f, "Child work").Title != "Child work" || f.mustCard("norm").Status != model.CardDone {
				t.Fatalf("cards %+v", f.cards())
			}
		})
	}

	t.Run("manual design still running absorbs and finishes", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		dir := writeResult(t, "## 开放问题\nshould the parser keep comments\n")
		parent := f.card(model.Card{ID: "design", Kind: model.KindDesign, Status: model.CardRunning})
		norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{parent.ID}})
		attempt := f.attempt(norm, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("design")
		if got.Status == model.CardRunning || got.Status == model.CardReady || got.Status == model.CardWaiting {
			t.Fatalf("design left open %+v", got)
		}
		if got.Status != model.CardDone && got.Status != model.CardMergeQueued {
			t.Fatalf("design %+v", got)
		}
		node, err := f.db.GetNode(f.ctx, f.node.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, question := range node.OpenQuestions {
			if strings.Contains(question.Text, "should the parser keep comments") {
				found = true
			}
		}
		if !found || f.mustCard("norm").Status != model.CardDone {
			t.Fatalf("node %+v norm %s", node.OpenQuestions, f.mustCard("norm").Status)
		}
	})

	for _, kind := range []model.CardKind{model.KindPlan, model.KindReplan} {
		t.Run("manual "+string(kind)+" still running absorbs and finishes", func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			ch := f.channel("ch-a", "a")
			dir := writeResult(t, `[{"key":"child","kind":"dev","title":"Child work"}]`)
			parent := f.card(model.Card{ID: "src", Kind: kind, Status: model.CardRunning})
			norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{parent.ID}})
			attempt := f.attempt(norm, 0, model.OutcomePassed, ch.ID, "")
			attempt.Worktree = dir
			if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			f.step()
			got := f.mustCard("src")
			if got.Status == model.CardRunning || got.Status == model.CardReady || got.Status == model.CardWaiting {
				t.Fatalf("%s left open %+v", kind, got)
			}
			if got.Status != model.CardDone && got.Status != model.CardMergeQueued {
				t.Fatalf("%s %+v", kind, got)
			}
			if cardByTitle(f, "Child work").Title != "Child work" || f.mustCard("norm").Status != model.CardDone {
				t.Fatalf("cards %+v", f.cards())
			}
		})
	}

	t.Run("dev original is only backfilled", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		dir := writeResult(t, "normalized body\n")
		parent := f.card(model.Card{ID: "dev", Status: model.CardRunning, Body: "old\n"})
		norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{parent.ID}})
		attempt := f.attempt(norm, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("dev")
		if got.Status != model.CardRunning || !strings.Contains(got.Body, "normalized body") || got.Status == model.CardDone {
			t.Fatalf("dev %+v", got)
		}
		if f.mustCard("norm").Status != model.CardDone {
			t.Fatalf("norm %s", f.mustCard("norm").Status)
		}
	})
}
