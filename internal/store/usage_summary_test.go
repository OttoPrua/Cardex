package store

import (
	"math"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestSummarizeCardUsageCountsPersistedAttemptsOnly(t *testing.T) {
	ctx, db := t.Context(), openDB(t)
	card := model.Card{ID: "c-audit", Kind: model.KindDev, Tier: model.TierT1, Status: model.CardReady}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	exact := func(n int64) model.Usage {
		return model.Usage{Source: "synthetic", Tokens: n, Confidence: 1}
	}
	a := model.Attempt{ID: "a-z", Card: card.ID, Index: 0, Usage: exact(30)}
	b := model.Attempt{ID: "a-y", Card: card.ID, Index: 1, Usage: exact(0)}
	c := model.Attempt{ID: "a-x", Card: card.ID, Index: 2}
	for _, attempt := range []*model.Attempt{&a, &b, &c} {
		if err := db.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpdateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	report, err := db.SummarizeCardUsage(ctx, card.ID)
	if err != nil || report.KnownTotal == nil || *report.KnownTotal != 30 || report.Total != nil ||
		report.UnknownAttempts != 1 || len(report.Attempts) != 3 || report.Attempts[0].ID != a.ID ||
		report.Attempts[1].Total == nil || *report.Attempts[1].Total != 0 || report.Attempts[2].Total != nil {
		t.Fatalf("err=%v report=%+v", err, report)
	}
	c.Usage = exact(40)
	if err := db.UpdateAttempt(ctx, &c); err != nil {
		t.Fatal(err)
	}
	report, err = db.SummarizeCardUsage(ctx, card.ID)
	if err != nil || report.Total == nil || *report.Total != 70 || report.UnknownAttempts != 0 {
		t.Fatalf("err=%v report=%+v", err, report)
	}
	c.Usage = exact(math.MaxInt64)
	if err := db.UpdateAttempt(ctx, &c); err != nil {
		t.Fatal(err)
	}
	report, err = db.SummarizeCardUsage(ctx, card.ID)
	if err != nil || report.Total != nil || report.KnownTotal != nil || !report.Overflow {
		t.Fatalf("err=%v report=%+v", err, report)
	}
	if _, err = db.SummarizeCardUsage(ctx, "no-such-card"); err == nil {
		t.Fatal("missing card reported zero actual usage")
	}
}
