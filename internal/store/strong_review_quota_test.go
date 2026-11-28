package store

import (
	"context"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStrongQuotaKnownResetAndUnresolvedHold(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()
	if err := db.InstallStrongWorkflowPrototype(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	reset := now.Add(time.Hour)
	quote := model.QuotaWindow{Name: "synthetic-hour", LimitTokens: 25, Confidence: 1, Remaining: 1, ResetAt: reset}
	p := model.StrongPlan{CrossFamilyReview: true}
	makeChannel := func(id, group string) model.Channel {
		return model.Channel{ID: id, Runner: model.RunnerFake, SourceGroup: group, Health: model.HealthOK, QuotaWindows: []model.QuotaWindow{quote}}
	}
	one, two := makeChannel("first-handle", "shared-account"), makeChannel("second-handle", "shared-account")
	for _, c := range []*model.Channel{&one, &two} {
		if err := db.CreateChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	reserve := func(id string, reservation int64) StrongIntent {
		in := StrongIntent{ID: id, Route: model.StrongApprovedRoute{Channel: one.ID}, Reserved: reservation}
		if err := db.WithWriteTx(ctx, func(ctx context.Context) error { return db.reserveStrongQuota(ctx, p, in) }); err != nil {
			t.Fatal(err)
		}
		return in
	}
	reserved := reserve("first-intent", 20)
	if db.strongQuotaAvailable(ctx, two, "other-run", 20, false, now) {
		t.Fatal("other channel alias ignored an active hold")
	}
	if ok, err := db.settleStrongQuota(ctx, p, reserved, 17); err != nil || !ok {
		t.Fatalf("charge known receipt %t %v", ok, err)
	}
	if db.strongQuotaAvailable(ctx, two, "other-run", 20, false, now) {
		t.Fatal("same window ignored settled charge")
	}
	after := two
	after.QuotaWindows = []model.QuotaWindow{quote}
	after.QuotaWindows[0].ResetAt = reset.Add(time.Hour)
	if db.strongQuotaAvailable(ctx, after, "next-run", 20, false, now) {
		t.Fatal("claimed reset before known boundary")
	}
	if !db.strongQuotaAvailable(ctx, after, "next-run", 20, false, reset.Add(time.Nanosecond)) {
		t.Fatal("verified next window inherited known old spend")
	}
	reserve("unresolved-intent", 5)
	if db.strongQuotaAvailable(ctx, after, "next-run", 20, false, reset.Add(time.Nanosecond)) {
		t.Fatal("unknown old usage was silently released at reset")
	}
	renamed := two
	renamed.QuotaWindows = []model.QuotaWindow{quote}
	renamed.QuotaWindows[0].Name = "relabelled"
	if db.strongQuotaAvailable(ctx, renamed, "other-run", 20, false, now) {
		t.Fatal("changing window identity bypassed known spend/holds")
	}
}

func TestStrongQuotaUsesLowerOfRatioAndTokenCapacity(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()
	if err := db.InstallStrongWorkflowPrototype(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ch := model.Channel{ID: "one-channel", SourceGroup: "one-account", Runner: model.RunnerFake, Health: model.HealthOK,
		QuotaWindows: []model.QuotaWindow{{Name: "hour", LimitTokens: 100, UsedTokens: 0, Remaining: .10, Confidence: 1, ResetAt: now.Add(time.Hour)}}}
	if db.strongQuotaAvailable(ctx, ch, "intent", 20, false, now) || !db.strongQuotaAvailable(ctx, ch, "intent", 10, false, now) {
		t.Fatal("explicit remaining ratio must cap token estimate")
	}
	ch.QuotaWindows[0].Remaining = .75
	ch.QuotaWindows[0].UsedTokens = 96
	if db.strongQuotaAvailable(ctx, ch, "intent", 5, false, now) || !db.strongQuotaAvailable(ctx, ch, "intent", 4, false, now) {
		t.Fatal("explicit used tokens must cap ratio estimate")
	}
}
