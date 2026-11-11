package loop

import (
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// Terminal unanswered backfill is one-shot per attempt. A later recheck that
// sets the same question back to researching must survive further Steps and
// another AbsorbDisposed of the old attempt.
func TestTerminalUnansweredBackfillIsOneShot(t *testing.T) {
	const voidText = "## 事实\n### Q1\n结论：不该写入作废事实\n证据：void.go:9\n把握：高\n"
	const recheck = "## 开放问题\n- [fact] Q1 复查：证据不足\n## 可冻结\n是\n"
	skipped := []struct {
		name    string
		status  model.CardStatus
		outcome model.Outcome
	}{
		{name: "failed", status: model.CardFailed, outcome: model.OutcomeTestsFailed},
		{name: "canceled", status: model.CardCanceled, outcome: ""},
		{name: "failed-empty", status: model.CardFailed, outcome: ""},
		{name: "violation", status: model.CardFailed, outcome: model.OutcomeViolation},
		{name: "transport", status: model.CardFailed, outcome: model.OutcomeTransport},
	}
	entries := []string{"step", "absorb"}
	layers := []model.Layer{model.LayerModule, model.LayerDetail}
	for _, tc := range skipped {
		for _, entry := range entries {
			for _, layer := range layers {
				t.Run(tc.name+"/"+entry+"/"+string(layer), func(t *testing.T) {
					st := openRework(t)
					obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
					node := seedNode(t, st, obj.ID, "", layer)
					node.OpenQuestions = model.OpenQuestionList{
						{ID: "Q1", Text: "端口", Type: model.OQFact, Status: model.OQResearching, CitedPaths: []string{}},
						{ID: "Q2", Text: "协议", Type: model.OQFact, Status: model.OQResearching, CitedPaths: []string{}},
					}
					if err := st.UpdateNode(t.Context(), &node); err != nil {
						t.Fatal(err)
					}
					research := seedCard(t, st, obj.ID, node.ID, model.KindResearch, tc.status, "Q1: 端口\nQ2: 协议\n")
					attempt := plantAttempt(t, st, research.ID, 0, tc.outcome, voidText)
					if entry == "step" {
						if err := loopStep(t, depsOf(st), obj.ID); err != nil {
							t.Fatal(err)
						}
					} else if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, tc.outcome, voidText); err != nil {
						t.Fatal(err)
					}
					assertStillUnanswered(t, st, node.ID)
					if err := loopStep(t, depsOf(st), obj.ID); err != nil {
						t.Fatal(err)
					}
					if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, tc.outcome, voidText); err != nil {
						t.Fatal(err)
					}
					assertStillUnanswered(t, st, node.ID)

					designs := readyCards(t, st, obj.ID, node.ID, model.KindDesign)
					if len(designs) != 1 {
						t.Fatalf("design cards %d", len(designs))
					}
					absorbText(t, st, designs[0].ID, 0, model.OutcomePassed, recheck)
					ok, err := depsOf(st).hasEvent(t.Context(), "node:"+node.ID, evFreezable)
					if err != nil || !ok {
						t.Fatalf("freezable recorded ok=%v err=%v", ok, err)
					}
					if q := findQ(nodeOf(t, st, node.ID), "Q1"); q.Status != model.OQOpen || q.Asks != 1 || q.Type != model.OQFact {
						t.Fatalf("recheck %+v", q)
					}
					if err := loopStep(t, depsOf(st), obj.ID); err != nil {
						t.Fatal(err)
					}
					fresh := recheckResearch(t, st, obj.ID, node.ID, research.ID)
					assertRecheckHolds(t, st, node.ID, fresh.ID)
					if err := loopStep(t, depsOf(st), obj.ID); err != nil {
						t.Fatal(err)
					}
					if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, tc.outcome, voidText); err != nil {
						t.Fatal(err)
					}
					if err := loopStep(t, depsOf(st), obj.ID); err != nil {
						t.Fatal(err)
					}
					assertRecheckHolds(t, st, node.ID, fresh.ID)
					if again := recheckResearch(t, st, obj.ID, node.ID, research.ID); again.ID != fresh.ID {
						t.Fatalf("research changed %s -> %s", fresh.ID, again.ID)
					}
				})
			}
		}
	}
}

// A superseded research attempt must not rewrite the current review.
// Consumed-while-non-terminal and late-unconsumed callbacks both reach the
// terminal unanswered write; only the card's current attempt may apply it.
func TestSupersededAttemptDoesNotRewriteRecheck(t *testing.T) {
	const voidText = "## 事实\n### Q1\n结论：不该写入作废事实\n证据：void.go:9\n把握：高\n"
	const recheck = "## 开放问题\n- [fact] Q1 复查：证据不足\n## 可冻结\n是\n"
	retries := []model.Outcome{
		model.OutcomeTransport,
		model.OutcomeTimeout,
		model.OutcomeQuotaHit,
		model.OutcomeAuthFailed,
		model.OutcomeNoOutput,
	}
	shapes := []string{"consumed", "late"}
	layers := []model.Layer{model.LayerModule, model.LayerDetail}
	for _, outcome := range retries {
		for _, shape := range shapes {
			for _, layer := range layers {
				t.Run(string(outcome)+"/"+shape+"/"+string(layer), func(t *testing.T) {
					st := openRework(t)
					obj := seedObjective(t, st, "", model.ModeAuto, model.FreezeFast)
					node := seedNode(t, st, obj.ID, "", layer)
					node.OpenQuestions = model.OpenQuestionList{
						{ID: "Q1", Text: "端口", Type: model.OQFact, Status: model.OQResearching, CitedPaths: []string{}},
						{ID: "Q2", Text: "协议", Type: model.OQFact, Status: model.OQResearching, CitedPaths: []string{}},
					}
					if err := st.UpdateNode(t.Context(), &node); err != nil {
						t.Fatal(err)
					}
					research := seedCard(t, st, obj.ID, node.ID, model.KindResearch, model.CardHarvesting, "Q1: 端口\nQ2: 协议\n")
					a0 := plantAttempt(t, st, research.ID, 0, outcome, voidText)
					if shape == "consumed" {
						if err := AbsorbDisposed(t.Context(), depsOf(st), a0.ID, outcome, voidText); err != nil {
							t.Fatal(err)
						}
						assertStillResearching(t, st, node.ID)
						if n := eventCount(t, st, attemptKey(a0.ID), evUnanswered); n != 0 {
							t.Fatalf("non-terminal skip wrote unanswered %d", n)
						}
					}
					a1 := plantAttempt(t, st, research.ID, 1, model.OutcomeTestsFailed, voidText)
					if err := setCardStatus(t, st, research.ID, model.CardFailed); err != nil {
						t.Fatal(err)
					}
					if err := New(st, nil, nil).Step(t.Context(), obj.ID); err != nil {
						t.Fatal(err)
					}
					assertStillUnanswered(t, st, node.ID)
					if n := eventCount(t, st, attemptKey(a1.ID), evUnanswered); n != 1 {
						t.Fatalf("current unanswered %d", n)
					}
					if n := eventCount(t, st, attemptKey(a0.ID), evUnanswered); n != 0 {
						t.Fatalf("historical unanswered before recheck %d", n)
					}
					if shape == "late" && eventCount(t, st, attemptKey(a0.ID), evAbsorbed) != 0 {
						t.Fatal("late attempt was consumed before replay")
					}
					designs := readyCards(t, st, obj.ID, node.ID, model.KindDesign)
					if len(designs) != 1 {
						t.Fatalf("design cards %d", len(designs))
					}
					absorbText(t, st, designs[0].ID, 0, model.OutcomePassed, recheck)
					ok, err := depsOf(st).hasEvent(t.Context(), "node:"+node.ID, evFreezable)
					if err != nil || !ok {
						t.Fatalf("freezable recorded ok=%v err=%v", ok, err)
					}
					if q := findQ(nodeOf(t, st, node.ID), "Q1"); q.Status != model.OQOpen || q.Asks != 1 || q.Type != model.OQFact {
						t.Fatalf("recheck %+v", q)
					}
					if err := New(st, nil, nil).Step(t.Context(), obj.ID); err != nil {
						t.Fatal(err)
					}
					fresh := recheckResearch(t, st, obj.ID, node.ID, research.ID)
					assertRecheckHolds(t, st, node.ID, fresh.ID)
					asks := findQ(nodeOf(t, st, node.ID), "Q1").Asks
					if err := AbsorbDisposed(t.Context(), depsOf(st), a0.ID, outcome, voidText); err != nil {
						t.Fatal(err)
					}
					if err := New(st, nil, nil).Step(t.Context(), obj.ID); err != nil {
						t.Fatal(err)
					}
					assertRecheckHolds(t, st, node.ID, fresh.ID)
					again := recheckResearch(t, st, obj.ID, node.ID, research.ID)
					if again.ID != fresh.ID || again.Status != fresh.Status || again.Tier != fresh.Tier || again.Body != fresh.Body {
						t.Fatalf("research changed %+v -> %+v", fresh, again)
					}
					got := nodeOf(t, st, node.ID)
					q1 := findQ(got, "Q1")
					if got.Status != model.NodeDrafting || q1.Status != model.OQResearching || q1.Asks != asks {
						t.Fatalf("after replay node %s q1 %+v", got.Status, q1)
					}
					if n := eventCount(t, st, attemptKey(a0.ID), evUnanswered); n != 0 {
						t.Fatalf("historical unanswered %d", n)
					}
					if n := eventCount(t, st, attemptKey(a1.ID), evUnanswered); n != 1 {
						t.Fatalf("current unanswered after replay %d", n)
					}
				})
			}
		}
	}
}

func assertStillResearching(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	got := nodeOf(t, st, nodeID)
	if got.Status != model.NodeDrafting {
		t.Fatalf("node %s", got.Status)
	}
	for _, id := range []string{"Q1", "Q2"} {
		q := findQ(got, id)
		if q.Status != model.OQResearching || q.Asks != 0 || strings.Contains(q.Answer, "不该写入") || strings.Contains(q.Evidence, "不该写入") {
			t.Fatalf("%s %+v", id, q)
		}
	}
}

func assertStillUnanswered(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	got := nodeOf(t, st, nodeID)
	for _, id := range []string{"Q1", "Q2"} {
		q := findQ(got, id)
		if q.Status != model.OQUnanswered || strings.Contains(q.Answer, "不该写入") || strings.Contains(q.Evidence, "不该写入") {
			t.Fatalf("%s %+v", id, q)
		}
	}
}

func assertRecheckHolds(t *testing.T, st *store.Store, nodeID, researchID string) {
	t.Helper()
	got := nodeOf(t, st, nodeID)
	q1 := findQ(got, "Q1")
	q2 := findQ(got, "Q2")
	card, err := st.GetCard(t.Context(), researchID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.NodeDrafting || q1.Status != model.OQResearching || q1.Asks != 1 || q2.Status != model.OQUnanswered || card.Status != model.CardReady || strings.Contains(q1.Answer, "不该写入") || strings.Contains(q1.Evidence, "不该写入") || strings.Contains(q2.Answer, "不该写入") || strings.Contains(q2.Evidence, "不该写入") {
		t.Fatalf("node %s q1 %+v q2 %+v research %s", got.Status, q1, q2, card.Status)
	}
}

func recheckResearch(t *testing.T, st *store.Store, objective, node, old string) model.Card {
	t.Helper()
	var found *model.Card
	for _, c := range cardsOf(t, st, objective) {
		if c.Node != node || c.Kind != model.KindResearch || c.ID == old {
			continue
		}
		if found != nil {
			t.Fatalf("extra research %+v %+v", *found, c)
		}
		cp := c
		found = &cp
	}
	if found == nil || found.Tier != model.TierT2 || found.Status != model.CardReady || !strings.Contains(found.Body, "Q1:") || strings.Contains(found.Body, "Q2:") {
		if found == nil {
			t.Fatal("missing recheck research")
		}
		t.Fatalf("recheck research tier %s status %s body %q", found.Tier, found.Status, found.Body)
	}
	return *found
}
