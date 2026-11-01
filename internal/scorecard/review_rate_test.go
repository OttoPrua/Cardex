package scorecard_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"cardex/internal/loop"
	"cardex/internal/model"
	"cardex/internal/scorecard"
	"cardex/internal/store"
)

// Reviewed kinds each grow a review card from the shipped development step.
// The pass is recorded by the shipped absorb on the reviewer's attempt.
var reviewedKinds = []model.CardKind{
	model.KindDev,
	model.KindSkeleton,
	model.KindRepair,
	model.KindRebase,
	model.KindBacklog,
}

func TestReviewPassFollowsReviewedCard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	db := openScoreDB(t, home)
	ctx := context.Background()
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "obj", Title: "score", Mode: model.ModeManual,
		FreezeMode: model.FreezeFast, Status: model.ObjectiveActive,
	}); err != nil {
		t.Fatal(err)
	}
	const authorModel = "model-a"
	const reviewerModel = "model-b"
	for _, kind := range reviewedKinds {
		author := &model.Card{
			ID: "author-" + string(kind), Objective: "obj", Node: "node-" + string(kind),
			Kind: kind, Tier: model.TierT2, Title: string(kind), Status: model.CardHarvesting,
		}
		idle := &model.Card{
			ID: "idle-" + string(kind), Objective: "obj", Node: "node-idle-" + string(kind),
			Kind: kind, Tier: model.TierT2, Title: "idle " + string(kind), Status: model.CardReady,
		}
		if err := db.CreateCard(ctx, author); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateCard(ctx, idle); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateAttempt(ctx, &model.Attempt{
			ID: "att-" + string(kind), Card: author.ID, Index: 0,
			Route: model.Route{Channel: "ch-a", Model: authorModel}, Outcome: model.OutcomePassed,
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateAttempt(ctx, &model.Attempt{
			ID: "idle-att-" + string(kind), Card: idle.ID, Index: 0,
			Route: model.Route{Channel: "ch-a", Model: authorModel},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := loop.DevStep(ctx, "obj"); err != nil {
		t.Fatal(err)
	}
	reviews := map[string]model.Card{}
	cards, err := db.ListCards(ctx, "obj")
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		if card.Kind != model.KindReview {
			continue
		}
		if len(card.Deps) != 1 {
			t.Fatalf("review %+v", card)
		}
		reviews[card.Deps[0]] = card
	}
	if len(reviews) != len(reviewedKinds) {
		t.Fatalf("reviews %d", len(reviews))
	}
	passText := "## 复核结论\npass\n"
	for _, kind := range reviewedKinds {
		authorID := "author-" + string(kind)
		review, ok := reviews[authorID]
		if !ok || review.Node == "" {
			t.Fatalf("review for %s %+v", kind, review)
		}
		attemptID := "rev-" + string(kind)
		if err := db.CreateAttempt(ctx, &model.Attempt{
			ID: attemptID, Card: review.ID, Index: 0,
			Route: model.Route{Channel: "ch-b", Model: reviewerModel},
		}); err != nil {
			t.Fatal(err)
		}
		if err := loop.AbsorbDisposed(ctx, loop.Deps{Store: db}, attemptID, model.OutcomePassed, passText); err != nil {
			t.Fatal(err)
		}
	}
	assertPassEvents(t, db, reviews, 1)
	rows := mustRows(t, home)
	for _, kind := range reviewedKinds {
		row := findScoreRow(t, rows, authorModel, kind, model.TierT2)
		if !nearRate(row.ReviewPassRate, 0.5) {
			t.Fatalf("%s author rate %v row %+v", kind, row.ReviewPassRate, row)
		}
	}
	reviewer := findScoreRow(t, rows, reviewerModel, model.KindReview, model.TierT3)
	if reviewer.ReviewPassRate != 0 {
		t.Fatalf("reviewer bucket includes the pass %+v", reviewer)
	}
	for _, kind := range reviewedKinds {
		review := reviews["author-"+string(kind)]
		attemptID := "rev2-" + string(kind)
		if err := db.CreateAttempt(ctx, &model.Attempt{
			ID: attemptID, Card: review.ID, Index: 1,
			Route: model.Route{Channel: "ch-b", Model: reviewerModel},
		}); err != nil {
			t.Fatal(err)
		}
		if err := loop.AbsorbDisposed(ctx, loop.Deps{Store: db}, attemptID, model.OutcomePassed, passText); err != nil {
			t.Fatal(err)
		}
	}
	assertPassEvents(t, db, reviews, 2)
	again := mustRows(t, home)
	for _, kind := range reviewedKinds {
		row := findScoreRow(t, again, authorModel, kind, model.TierT2)
		if !nearRate(row.ReviewPassRate, 0.5) {
			t.Fatalf("%s second pass counted twice: %v", kind, row.ReviewPassRate)
		}
	}
	if row := findScoreRow(t, again, reviewerModel, model.KindReview, model.TierT3); row.ReviewPassRate != 0 {
		t.Fatalf("reviewer bucket after second pass %+v", row)
	}
}

func TestReviewPassUsesBindNotPayload(t *testing.T) {
	home := t.TempDir()
	db := openScoreDB(t, home)
	ctx := context.Background()
	author := &model.Card{ID: "author", Kind: model.KindDev, Tier: model.TierT2, Title: "author", Status: model.CardDone}
	decoy := &model.Card{ID: "missing-target", Kind: model.KindDev, Tier: model.TierT1, Title: "decoy", Status: model.CardDone}
	review := &model.Card{
		ID: "review", Kind: model.KindReview, Tier: model.TierT3, Title: "review", Status: model.CardDone,
		Deps: []string{"gone"},
	}
	for _, card := range []*model.Card{author, decoy, review} {
		if err := db.CreateCard(ctx, card); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "a", Card: author.ID, Route: model.Route{Model: "model-a"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "d", Card: decoy.ID, Route: model.Route{Model: "model-d"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "b", Card: review.ID, Route: model.Route{Model: "model-b"},
	}); err != nil {
		t.Fatal(err)
	}
	bind, err := json.Marshal(map[string]string{"design": author.ID})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"design": review.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateEvent(ctx, &model.Event{Entity: "card:" + review.ID, Type: "loop.review_bind", Data: bind}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateEvent(ctx, &model.Event{Entity: "card:" + review.ID, Type: "loop.review_pass", Data: payload}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateEvent(ctx, &model.Event{Entity: "card:" + review.ID, Type: "loop.review_pass", Data: payload}); err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, home)
	authorRow := findScoreRow(t, rows, "model-a", model.KindDev, model.TierT2)
	if !nearRate(authorRow.ReviewPassRate, 1) {
		t.Fatalf("bind did not credit the reviewed card %+v", authorRow)
	}
	if row := findScoreRow(t, rows, "model-b", model.KindReview, model.TierT3); row.ReviewPassRate != 0 {
		t.Fatalf("payload credited the reviewer %+v", row)
	}
	if row := findScoreRow(t, rows, "model-d", model.KindDev, model.TierT1); row.ReviewPassRate != 0 {
		t.Fatalf("unrelated card credited %+v", row)
	}
}

func assertPassEvents(t *testing.T, db *store.Store, reviews map[string]model.Card, want int) {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for authorID, review := range reviews {
		n := 0
		for _, ev := range events {
			if ev.Type != "loop.review_pass" || ev.Entity != "card:"+review.ID {
				if ev.Type == "loop.review_pass" && ev.Entity == "card:"+authorID {
					t.Fatalf("pass stored on author %s", authorID)
				}
				continue
			}
			var payload struct {
				Design string `json:"design"`
			}
			if json.Unmarshal(ev.Data, &payload) != nil || payload.Design != review.ID {
				t.Fatalf("pass payload %s on %s", ev.Data, review.ID)
			}
			n++
		}
		if n != want {
			t.Fatalf("review %s pass events %d want %d", review.ID, n, want)
		}
	}
}

func openScoreDB(t *testing.T, home string) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustRows(t *testing.T, home string) []scorecard.Row {
	t.Helper()
	rows, err := scorecard.Compute(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func findScoreRow(t *testing.T, rows []scorecard.Row, modelName string, kind model.CardKind, tier model.Tier) scorecard.Row {
	t.Helper()
	for _, row := range rows {
		if row.Model == modelName && row.Kind == kind && row.Tier == tier {
			return row
		}
	}
	t.Fatalf("missing %s %s %s in %+v", modelName, kind, tier, rows)
	return scorecard.Row{}
}

func nearRate(got, want float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
