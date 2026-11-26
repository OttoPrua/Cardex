package loop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// A review card that lives in another objective than the card it reviews must
// still write its verdict back to that card instead of failing "missing card".
func TestCrossObjectiveReviewAbsorbs(t *testing.T) {
	cases := map[string]model.CardStatus{"pass": model.CardMergeQueued, "rework": model.CardReady}
	for verdict, want := range cases {
		t.Run(verdict, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardReviewing})
			devAttempt := model.Attempt{ID: "a-dev", Card: "dev", Index: 1, Branch: "cardex/obj/c/dev", Worktree: f.home, EndedAt: time.Now().UTC()}
			if err := f.db.CreateAttempt(f.ctx, &devAttempt); err != nil {
				t.Fatal(err)
			}
			other := f.obj
			other.ID = "obj2"
			other.IntegrationBranch = "integration2"
			if err := f.db.CreateObjective(f.ctx, &other); err != nil {
				t.Fatal(err)
			}
			rv := model.Card{ID: "rv", Objective: other.ID, Kind: model.KindReview, Tier: model.TierT3, Title: "rv", Status: model.CardRunning, Deps: []string{"dev"}}
			if err := f.db.CreateCard(f.ctx, &rv); err != nil {
				t.Fatal(err)
			}
			text := "## 结论\nok\n\n## 复核结论\n" + verdict + "\n"
			if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			exit := 0
			attempt := model.Attempt{ID: "a-rv", Card: "rv", Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC()}
			if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			card, _ := f.db.GetCard(f.ctx, "rv")
			ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
			if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: text}); err != nil {
				t.Fatalf("dispose: %v", err)
			}
			if got := rvCardStatus(t, f, "rv"); got != model.CardDone {
				t.Fatalf("review status %s, want done", got)
			}
			dev, err := f.db.GetCard(f.ctx, "dev")
			if err != nil {
				t.Fatal(err)
			}
			if dev.Status != want {
				t.Fatalf("dev status %s, want %s", dev.Status, want)
			}
			if dev.Objective != "obj" {
				t.Fatalf("dev moved to objective %s", dev.Objective)
			}
			if verdict == "rework" && dev.Base != "cardex/obj/c/dev" {
				t.Fatalf("rework base %q, want the dev card's latest branch", dev.Base)
			}
			if !hasCardEvent(t, f, "dev", "review_verdict") {
				t.Fatal("verdict not recorded on dev card")
			}
		})
	}
}

func TestCrossObjectiveReworkLimitUsesQuestionID(t *testing.T) {
	f := newFx(t, model.ModeManual)
	f.obj.Events = "on"
	if err := f.db.UpdateObjective(f.ctx, &f.obj); err != nil {
		t.Fatal(err)
	}
	reviewObjective := f.obj
	reviewObjective.ID = "obj-review"
	reviewObjective.Origin = "file:review-origin"
	reviewObjective.IntegrationBranch = "integration-review"
	reviewObjective.Events = "off"
	if err := f.db.CreateObjective(f.ctx, &reviewObjective); err != nil {
		t.Fatal(err)
	}
	dev := f.card(model.Card{ID: "dev", Status: model.CardReviewing, ReworkCount: 2})
	candidate := f.attempt(dev, 0, model.OutcomePassed, "", "")
	candidate.HeadCommit = "dev-sha"
	if err := f.db.UpdateAttempt(f.ctx, &candidate); err != nil {
		t.Fatal(err)
	}

	const result = "## 复核结论\nrework\nstill broken\n"
	worktree := writeResult(t, result)
	readEvents := func() map[string]map[string]int {
		files, err := filepath.Glob(filepath.Join(f.home, "inbox", "lifecycle-*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		events := map[string]map[string]int{"rework_limit": {}, "needs_input": {}}
		for _, file := range files {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			header, payload, _ := strings.Cut(string(raw), "\n\n")
			var event store.Pulse
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(header, "\nobjective: "+f.obj.ID+"\norigin: "+f.obj.Origin) ||
				event.ID != dev.ID || event.Model != candidate.Route.Model || event.SHA != candidate.HeadCommit || events[event.Status] == nil {
				t.Fatalf("unexpected notification %s", raw)
			}
			events[event.Status][event.QuestionID]++
		}
		return events
	}

	var firstID string
	for round := 0; round < 3; round++ {
		review := f.card(model.Card{
			ID: fmt.Sprintf("review-%d", round), Objective: reviewObjective.ID,
			Kind: model.KindReview, Tier: model.TierT3, Status: model.CardHarvesting, Deps: []string{dev.ID},
		})
		attempt := f.attempt(review, 0, model.OutcomePassed, "", "")
		attempt.Worktree = worktree
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
		if err := Dispose(ctx, &review, harvest.Result{Outcome: model.OutcomePassed, ResultText: result}); err != nil {
			t.Fatal(err)
		}
		questions, err := f.db.ListQuestions(f.ctx, reviewObjective.ID)
		if err != nil {
			t.Fatal(err)
		}
		var pending model.Question
		for _, q := range questions {
			if q.Answer == "" && q.Scope == dev.ID {
				if pending.ID != "" {
					t.Fatalf("duplicate pending questions: %+v", questions)
				}
				pending = q
			}
		}
		if pending.ID == "" {
			t.Fatalf("round %d: missing pending review question", round)
		}
		if (round == 1 && pending.ID != firstID) || (round == 2 && pending.ID == firstID) {
			t.Fatalf("round %d: got question %q, first question was %q", round, pending.ID, firstID)
		}
		for status, counts := range readEvents() {
			expected := 1
			if round == 2 {
				expected = 2
			}
			if len(counts) != expected || counts[pending.ID] != 1 || (round == 2 && counts[firstID] != 1) {
				t.Errorf("round %d: %s deliveries %v, current question %s", round, status, counts, pending.ID)
			}
		}
		pulse, err := f.db.CardPulse(f.ctx, f.mustCard(dev.ID))
		if err != nil || pulse.QuestionID != pending.ID {
			t.Errorf("round %d: card pulse %+v, want question %s, error %v", round, pulse, pending.ID, err)
		}
		if round == 0 {
			firstID = pending.ID
		}
		if round == 1 {
			pending.Answer = "reject"
			if err := f.db.UpdateQuestion(f.ctx, &pending); err != nil {
				t.Fatal(err)
			}
			dev = f.mustCard(dev.ID)
			dev.Status = model.CardReviewing
			if err := f.db.UpdateCard(f.ctx, &dev); err != nil {
				t.Fatal(err)
			}
		}
	}
}
