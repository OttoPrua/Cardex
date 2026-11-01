package scorecard

import (
	"context"
	"path/filepath"
	"testing"

	"cardex/internal/model"
	"cardex/internal/store"
)

type seedCard struct {
	id     string
	kind   model.CardKind
	tier   model.Tier
	status model.CardStatus
	rework int
	review bool
	tokens []int64
	model  string
}

func TestComputeRatesAndInsufficient(t *testing.T) {
	home := t.TempDir()
	db := openDB(t, home)
	ctx := context.Background()

	big := []seedCard{
		{id: "b0", kind: model.KindDev, tier: model.TierT2, status: model.CardDone, model: "m-big", tokens: []int64{0, 0}},
		{id: "b1", kind: model.KindDev, tier: model.TierT2, status: model.CardDone, model: "m-big", tokens: []int64{0}},
		{id: "b2", kind: model.KindDev, tier: model.TierT2, status: model.CardDone, model: "m-big", tokens: []int64{0}},
		{id: "b3", kind: model.KindDev, tier: model.TierT2, status: model.CardDone, model: "m-big", tokens: []int64{0}},
	}
	small := []seedCard{
		{id: "s0", kind: model.KindDev, tier: model.TierT1, status: model.CardMerged, rework: 1, review: true, model: "m-small", tokens: []int64{8}},
		{id: "s1", kind: model.KindDev, tier: model.TierT1, status: model.CardDone, rework: 3, model: "m-small", tokens: []int64{100, 5}},
		{id: "s2", kind: model.KindDev, tier: model.TierT1, status: model.CardMerged, model: "m-small", tokens: []int64{2}},
	}
	plant(t, db, append(append([]seedCard{}, big...), small...))
	if err := db.CreateCard(ctx, &model.Card{
		ID: "lonely", Kind: model.KindSpike, Tier: model.TierT2, Title: "no attempts", Status: model.CardReady,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "orphan", Card: "missing-card", Route: model.Route{Model: "ghost"},
	}); err != nil {
		t.Fatal(err)
	}

	rows := mustCompute(t, home)
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	assertRow(t, findRow(t, rows, "m-big", model.KindDev, model.TierT2), big)
	assertRow(t, findRow(t, rows, "m-small", model.KindDev, model.TierT1), small)
	for _, row := range rows {
		if row.Kind == model.KindSpike || row.Model == "ghost" {
			t.Fatalf("unexpected row %+v", row)
		}
	}

	before := findRow(t, rows, "m-big", model.KindDev, model.TierT2)
	if err := db.CreateEvent(ctx, &model.Event{Entity: "card:b0", Type: reviewPass}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateEvent(ctx, &model.Event{Entity: "card:b0", Type: reviewPass}); err != nil {
		t.Fatal(err)
	}
	afterReview := findRow(t, mustCompute(t, home), "m-big", model.KindDev, model.TierT2)
	if afterReview.ReviewPassRate == before.ReviewPassRate {
		t.Fatal("review pass did not change the rate")
	}
	if afterReview.MergeRate != before.MergeRate || afterReview.AvgRework != before.AvgRework || afterReview.TokensPerMerge != before.TokensPerMerge {
		t.Fatalf("review changed other metrics %+v", afterReview)
	}
	if !near(afterReview.ReviewPassRate, 1.0/4.0) {
		t.Fatalf("review rate %v", afterReview.ReviewPassRate)
	}

	card, err := db.GetCard(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	card.Status = model.CardMerged
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	afterMerge := findRow(t, mustCompute(t, home), "m-big", model.KindDev, model.TierT2)
	if afterMerge.MergeRate == afterReview.MergeRate {
		t.Fatal("merged status did not change the rate")
	}
	if afterMerge.ReviewPassRate != afterReview.ReviewPassRate || afterMerge.AvgRework != afterReview.AvgRework || afterMerge.TokensPerMerge != 0 {
		t.Fatalf("merge changed other metrics %+v", afterMerge)
	}

	card, err = db.GetCard(ctx, "b2")
	if err != nil {
		t.Fatal(err)
	}
	card.ReworkCount = 4
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	afterRework := findRow(t, mustCompute(t, home), "m-big", model.KindDev, model.TierT2)
	if afterRework.AvgRework == afterMerge.AvgRework {
		t.Fatal("rework count did not change the mean")
	}
	if afterRework.ReviewPassRate != afterMerge.ReviewPassRate || afterRework.MergeRate != afterMerge.MergeRate || afterRework.TokensPerMerge != afterMerge.TokensPerMerge {
		t.Fatalf("rework changed other metrics %+v", afterRework)
	}

	attempts, err := db.ListAttempts(ctx, "b1")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts %+v %v", attempts, err)
	}
	attempts[0].Usage.Tokens = 25
	if err := db.UpdateAttempt(ctx, &attempts[0]); err != nil {
		t.Fatal(err)
	}
	idle, err := db.ListAttempts(ctx, "b0")
	if err != nil || len(idle) == 0 {
		t.Fatal(err)
	}
	idle[0].Usage.Tokens = 100
	if err := db.UpdateAttempt(ctx, &idle[0]); err != nil {
		t.Fatal(err)
	}
	afterTokens := findRow(t, mustCompute(t, home), "m-big", model.KindDev, model.TierT2)
	if afterTokens.TokensPerMerge == afterRework.TokensPerMerge {
		t.Fatal("token usage did not change tokens per merged card")
	}
	if !near(afterTokens.TokensPerMerge, 25) {
		t.Fatalf("tokens per merge %v", afterTokens.TokensPerMerge)
	}
	if afterTokens.ReviewPassRate != afterRework.ReviewPassRate || afterTokens.MergeRate != afterRework.MergeRate || afterTokens.AvgRework != afterRework.AvgRework {
		t.Fatalf("tokens changed other metrics %+v", afterTokens)
	}
	if afterTokens.Sample != before.Sample || afterTokens.Insufficient {
		t.Fatalf("sample changed %+v", afterTokens)
	}
}

func plant(t *testing.T, db *store.Store, cards []seedCard) {
	t.Helper()
	ctx := context.Background()
	for _, item := range cards {
		card := model.Card{
			ID: item.id, Kind: item.kind, Tier: item.tier, Title: item.id,
			Status: item.status, ReworkCount: item.rework,
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		if item.review {
			if err := db.CreateEvent(ctx, &model.Event{Entity: "card:" + item.id, Type: reviewPass}); err != nil {
				t.Fatal(err)
			}
		}
		for i, tokens := range item.tokens {
			if err := db.CreateAttempt(ctx, &model.Attempt{
				ID: item.id + "-" + string(rune('a'+i)), Card: item.id,
				Route: model.Route{Channel: "ch", Model: item.model},
				Usage: model.Usage{Tokens: tokens, Source: "test", Confidence: 1},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func assertRow(t *testing.T, row Row, cards []seedCard) {
	t.Helper()
	attempts := 0
	passed := 0
	merged := 0
	rework := 0
	var tokenSum int64
	for _, card := range cards {
		attempts += len(card.tokens)
		rework += card.rework
		if card.review {
			passed++
		}
		if card.status == model.CardMerged {
			merged++
			for _, n := range card.tokens {
				tokenSum += n
			}
		}
	}
	n := len(cards)
	if row.Sample != attempts || row.Insufficient != (attempts < 5) {
		t.Fatalf("sample %+v attempts %d", row, attempts)
	}
	if !near(row.ReviewPassRate, float64(passed)/float64(n)) || !near(row.MergeRate, float64(merged)/float64(n)) || !near(row.AvgRework, float64(rework)/float64(n)) {
		t.Fatalf("rates %+v passed %d merged %d rework %d cards %d", row, passed, merged, rework, n)
	}
	wantTokens := 0.0
	if merged > 0 {
		wantTokens = float64(tokenSum) / float64(merged)
	}
	if !near(row.TokensPerMerge, wantTokens) {
		t.Fatalf("tokens %+v want %v", row, wantTokens)
	}
}

func findRow(t *testing.T, rows []Row, modelName string, kind model.CardKind, tier model.Tier) Row {
	t.Helper()
	for _, row := range rows {
		if row.Model == modelName && row.Kind == kind && row.Tier == tier {
			return row
		}
	}
	t.Fatalf("missing %s %s %s in %+v", modelName, kind, tier, rows)
	return Row{}
}

func mustCompute(t *testing.T, home string) []Row {
	t.Helper()
	rows, err := Compute(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func openDB(t *testing.T, home string) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func near(got, want float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
