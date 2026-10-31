package route

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/quota"
)

func TestSelectPaceStaysInsideSource(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	pick := func(t *testing.T, specs []spec) (model.Route, model.RouteReason) {
		t.Helper()
		led, _, channels, pos := build(t, now, specs)
		rt, reason, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT2}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return rt, reason
	}
	cross := []spec{
		{id: "a", modelName: "a-m", family: "f", source: "alpha", pref: 1, limit: 100, used: 10, anchor: now},
		{id: "b", modelName: "b-m", family: "f", source: "beta", pref: 1, limit: 100, used: 40, anchor: now},
	}
	swappedCross := []spec{
		{id: "a", modelName: "a-m", family: "f", source: "alpha", pref: 1, limit: 100, used: 40, anchor: now},
		{id: "b", modelName: "b-m", family: "f", source: "beta", pref: 1, limit: 100, used: 10, anchor: now},
	}
	led, _, _, _ := build(t, now, cross)
	paceA := expectPace(mustEst(t, led, "a"), now)
	paceB := expectPace(mustEst(t, led, "b"), now)
	if paceA == paceB {
		t.Fatalf("cross-source paces are equal %v", paceA)
	}
	first, _ := pick(t, cross)
	second, _ := pick(t, swappedCross)
	if first.Channel == "" || first.Channel != second.Channel || first.Model != second.Model {
		t.Fatalf("cross-source pace swap changed winner %s/%s -> %s/%s", first.Channel, first.Model, second.Channel, second.Model)
	}

	inside := []spec{
		{id: "roomier", modelName: "roomier-m", family: "f", source: "alpha", pref: 1, limit: 100, used: 10, anchor: now},
		{id: "tighter", modelName: "tighter-m", family: "f", source: "alpha", pref: 1, limit: 100, used: 60, anchor: now},
	}
	swappedInside := []spec{
		{id: "roomier", modelName: "roomier-m", family: "f", source: "alpha", pref: 1, limit: 100, used: 60, anchor: now},
		{id: "tighter", modelName: "tighter-m", family: "f", source: "alpha", pref: 1, limit: 100, used: 10, anchor: now},
	}
	same, _ := pick(t, inside)
	flipped, _ := pick(t, swappedInside)
	if same.Channel != "roomier" || flipped.Channel != "tighter" {
		t.Fatalf("same-source pace order %s then %s", same.Channel, flipped.Channel)
	}
}

func TestSelectFiltersAndOrder(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	t.Run("pace same source", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "fast", modelName: "fast-m", family: "f", source: "sub", pref: 1, limit: 100, used: 20, anchor: now},
			{id: "slow", modelName: "slow-m", family: "f", source: "sub", pref: 1, limit: 100, used: 50, anchor: now},
		})
		rt, reason, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT2}, nil)
		if err != nil || rt.Channel != "fast" || rt.Model != "fast-m" {
			t.Fatalf("route %+v err %v", rt, err)
		}
		if len(reason.Candidates) != 2 || reason.Candidates[0].Channel != "fast" || reason.Candidates[1].Channel != "slow" {
			t.Fatalf("candidates %+v", reason.Candidates)
		}
		if len(reason.Scores) != len(reason.Candidates) {
			t.Fatalf("scores %+v", reason.Scores)
		}
		fast := mustEst(t, led, "fast")
		slow := mustEst(t, led, "slow")
		if fast.Ratio != 0.8 || slow.Ratio != 0.5 {
			t.Fatalf("ratios fast %v slow %v", fast.Ratio, slow.Ratio)
		}
		if !near(reason.Scores[0].Score, expectPace(fast, now)) || !near(reason.Scores[1].Score, expectPace(slow, now)) {
			t.Fatalf("scores %+v want %v %v", reason.Scores, expectPace(fast, now), expectPace(slow, now))
		}
		if reason.Scores[0].Score <= reason.Scores[1].Score {
			t.Fatalf("pace order %+v", reason.Scores)
		}
	})

	t.Run("exactly 90 stays ahead of a higher pace above 90", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "exact", modelName: "exact-m", family: "f", source: "sub", pref: 1, limit: 100, used: 90, anchor: now, window: "weekly"},
			{id: "over", modelName: "over-m", family: "f", source: "sub", pref: 1, limit: 100, used: 91, anchor: now.Add(time.Hour)},
		})
		exact := mustEst(t, led, "exact")
		over := mustEst(t, led, "over")
		if exact.UsedTokens*100 > exact.LimitTokens*90 || over.UsedTokens*100 <= over.LimitTokens*90 {
			t.Fatalf("boundary exact %+v over %+v", exact, over)
		}
		if expectPace(exact, now) >= expectPace(over, now) {
			t.Fatalf("fixture pace exact %v over %v", expectPace(exact, now), expectPace(over, now))
		}
		rt, reason, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT2}, nil)
		if err != nil || rt.Channel != "exact" {
			t.Fatalf("route %+v err %v reason %+v", rt, err, reason)
		}
		if len(reason.Candidates) != 2 || reason.Candidates[1].Channel != "over" {
			t.Fatalf("candidates %+v", reason.Candidates)
		}
		if !near(reason.Scores[0].Score, expectPace(exact, now)) || reason.Scores[0].Score >= reason.Scores[1].Score {
			t.Fatalf("demoted raw pace still higher %+v", reason.Scores)
		}
	})

	t.Run("exactly 98 stays and 99 is cut off", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "room", modelName: "room-m", family: "f", source: "sub", pref: 1, limit: 100, used: 50, anchor: now, window: "weekly"},
			{id: "edge", modelName: "edge-m", family: "f", source: "sub", pref: 1, limit: 100, used: 98, anchor: now.Add(time.Hour)},
			{id: "cut", modelName: "cut-m", family: "f", source: "sub", pref: 1, limit: 100, used: 99, anchor: now},
		})
		edge := mustEst(t, led, "edge")
		cut := mustEst(t, led, "cut")
		if edge.UsedTokens*100 > edge.LimitTokens*98 || cut.UsedTokens*100 <= cut.LimitTokens*98 {
			t.Fatalf("98 boundary edge %+v cut %+v", edge, cut)
		}
		rt, reason, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT2}, nil)
		if err != nil || rt.Channel != "room" {
			t.Fatalf("route %+v err %v", rt, err)
		}
		if len(reason.Candidates) != 2 || reason.Candidates[1].Channel != "edge" {
			t.Fatalf("98 stayed out %+v", reason.Candidates)
		}
		got, ok := oneReason(reason, "cut")
		if !ok || got != ReasonCutoff {
			t.Fatalf("cutoff %+v", reason.Filtered)
		}
	})

	t.Run("claude 5h redline", func(t *testing.T) {
		claude := loadClaude(t)
		claude.Health = model.HealthOK
		if len(claude.QuotaWindows) == 1 {
			claude.QuotaWindows[0].ResetAt = now
		}
		above := model.Channel{
			ID: "above", Runner: model.RunnerGrok, SourceGroup: "other", Family: "other",
			Models: []string{"above-m"}, Concurrency: 2, Health: model.HealthOK, Redline: 0.2,
			QuotaWindows: []model.QuotaWindow{{Name: "5h", ResetRule: "rolling", LimitTokens: 100000, ResetAt: now}},
		}
		led := quota.New()
		led.SetClock(func() time.Time { return now })
		led.Configure(claude)
		led.Configure(above)
		record(t, led, claude.ID, 80000, now)
		record(t, led, above.ID, 79999, now)
		claudeEst := mustEst(t, led, claude.ID)
		aboveEst := mustEst(t, led, above.ID)
		if claudeEst.Ratio > claude.Redline || aboveEst.Ratio <= above.Redline {
			t.Fatalf("ratios claude %+v above %+v", claudeEst, aboveEst)
		}
		channels := []model.Channel{claude, above}
		pos := []model.Positioning{
			{Model: "claude-opus", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Preference: 1},
			{Model: "above-m", Family: "other", Tiers: []model.Tier{model.TierT2}, Preference: 1},
		}
		rt, reason, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT2}, nil)
		if err != nil || rt.Channel != "above" {
			t.Fatalf("route %+v err %v", rt, err)
		}
		got, ok := oneReason(reason, claude.ID)
		if !ok || got != ReasonRedline {
			t.Fatalf("redline %+v", reason.Filtered)
		}
		pinned := model.Card{Kind: model.KindDev, Tier: model.TierT2, Channel: claude.ID}
		rt, reason, err = call(t, now, led, channels, pos, pinned, nil)
		if err != nil || rt.Channel != claude.ID || rt.Model != "claude-opus" {
			t.Fatalf("pin %+v err %v", rt, err)
		}
		if len(reason.Filtered) != 0 || len(reason.Candidates) != 1 || reason.Candidates[0].Channel != claude.ID {
			t.Fatalf("pin reason %+v", reason)
		}
	})

	t.Run("health concurrency blocked", func(t *testing.T) {
		led, clock, channels, pos := build(t, now, []spec{
			{id: "sick", modelName: "sick-m", family: "f", limit: 100, used: 100, redline: 0.5, health: model.HealthDegraded},
			{id: "busy", modelName: "busy-m", family: "f", limit: 100, conc: 1},
			{id: "held", modelName: "held-m", family: "f", limit: 100, used: 10},
			{id: "open", modelName: "open-m", family: "f", limit: 100, used: 10},
		})
		reset := now.Add(2 * time.Hour)
		if err := led.ObserveLimit(ctx, "held", reset); err != nil {
			t.Fatal(err)
		}
		card := model.Card{Kind: model.KindDev, Tier: model.TierT2}
		rt, reason, err := call(t, now, led, channels, pos, card, func(r *Request) {
			r.InUse = map[string]int{"busy": 1}
		})
		if err != nil || rt.Channel != "open" {
			t.Fatalf("route %+v err %v filtered %+v", rt, err, reason.Filtered)
		}
		if got, ok := oneReason(reason, "sick"); !ok || got != ReasonHealth {
			t.Fatalf("health %+v", reason.Filtered)
		}
		if got, ok := oneReason(reason, "busy"); !ok || got != ReasonConcurrency {
			t.Fatalf("concurrency %+v", reason.Filtered)
		}
		if got, ok := oneReason(reason, "held"); !ok || got != ReasonBlocked {
			t.Fatalf("blocked %+v", reason.Filtered)
		}
		*clock = reset.Add(-time.Second)
		rt, reason, err = call(t, reset.Add(-time.Second), led, channels, pos, card, func(r *Request) {
			r.InUse = map[string]int{"busy": 1}
		})
		if err != nil || rt.Channel != "open" {
			t.Fatalf("still blocked route %+v err %v", rt, err)
		}
		if got, _ := oneReason(reason, "held"); got != ReasonBlocked {
			t.Fatalf("early lift %+v", reason.Filtered)
		}
		*clock = reset
		rt, reason, err = call(t, reset, led, channels, pos, card, func(r *Request) {
			r.InUse = map[string]int{"busy": 1}
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := oneReason(reason, "held"); ok {
			t.Fatalf("block remained %+v", reason.Filtered)
		}
		if rt.Channel != "held" && rt.Channel != "open" {
			t.Fatalf("after reset %+v", rt)
		}
	})

	t.Run("unknown quota allow and exclude", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "known", modelName: "known-m", family: "f", pref: 2, limit: 100, used: 50},
			{id: "mystery", modelName: "mystery-m", family: "f", pref: 2},
		})
		card := model.Card{Kind: model.KindDev, Tier: model.TierT2}
		rt, reason, err := call(t, now, led, channels, pos, card, nil)
		if err != nil || rt.Channel != "known" {
			t.Fatalf("allow %+v err %v", rt, err)
		}
		if len(reason.Candidates) != 2 || reason.Candidates[1].Channel != "mystery" {
			t.Fatalf("unknown not last %+v", reason.Candidates)
		}
		rt, reason, err = call(t, now, led, channels, pos, card, func(r *Request) {
			r.UnknownQuota = "exclude"
		})
		if err != nil || rt.Channel != "known" {
			t.Fatalf("exclude route %+v err %v", rt, err)
		}
		got, ok := oneReason(reason, "mystery")
		if !ok || got != ReasonUnknownQuota || len(reason.Candidates) != 1 {
			t.Fatalf("exclude %+v", reason)
		}
		_, reason, err = call(t, now, led, channels, pos, card, func(r *Request) {
			r.Quota = nil
			r.UnknownQuota = "exclude"
		})
		if !errors.Is(err, ErrNoCandidate) {
			t.Fatalf("nil quota exclude err %v", err)
		}
		if got, ok := oneReason(reason, "known"); !ok || got != ReasonUnknownQuota {
			t.Fatalf("nil quota %+v", reason.Filtered)
		}
		rt, _, err = call(t, now, led, channels, pos, card, func(r *Request) {
			r.Quota = quota.Stub{}
		})
		if err != nil || rt.Channel == "" {
			t.Fatalf("stub quota %+v err %v", rt, err)
		}
	})

	t.Run("preference before pace", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "fav", modelName: "fav-m", family: "f", source: "sub", pref: 0, limit: 100, used: 70, anchor: now},
			{id: "other", modelName: "other-m", family: "f", source: "sub", pref: 1, limit: 100, used: 10, anchor: now},
		})
		rt, reason, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT2}, nil)
		if err != nil || rt.Channel != "fav" {
			t.Fatalf("route %+v err %v", rt, err)
		}
		if reason.Scores[0].Score >= reason.Scores[1].Score {
			t.Fatalf("preference lost to pace %+v", reason.Scores)
		}
	})

	t.Run("requires and borrow", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "web", modelName: "web-m", family: "f", tier: model.TierT2, caps: []string{"web"}, limit: 100},
			{id: "plain", modelName: "plain-m", family: "f", tier: model.TierT2, limit: 100},
			{id: "high", modelName: "high-m", family: "f", tier: model.TierT3, pref: 0, limit: 100},
		})
		card := model.Card{Kind: model.KindDev, Tier: model.TierT1, Requires: []string{"web"}}
		rt, reason, err := call(t, now, led, channels, pos, card, nil)
		if err != nil || rt.Channel != "web" {
			t.Fatalf("borrow one tier with requires %+v err %v reason %+v", rt, err, reason)
		}
		if containsChannel(reason, "high") || containsChannel(reason, "plain") {
			t.Fatalf("borrowed too far or ignored requires %+v", reason)
		}

		card.Tier = model.TierT2
		rt, reason, err = call(t, now, led, channels, pos, card, nil)
		if err != nil || rt.Channel != "web" {
			t.Fatalf("requires %+v err %v filtered %+v", rt, err, reason.Filtered)
		}
		if containsChannel(reason, "plain") || containsChannel(reason, "high") {
			t.Fatalf("plain or high included %+v", reason)
		}

		led, _, channels, pos = build(t, now, []spec{
			{id: "mid", modelName: "mid-m", family: "f", tier: model.TierT2, pref: 5, limit: 100},
			{id: "top", modelName: "top-m", family: "f", tier: model.TierT3, pref: 0, limit: 100},
		})
		rt, reason, err = call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT1}, nil)
		if err != nil || rt.Channel != "mid" || reason.Candidates[0].Tier != model.TierT2 {
			t.Fatalf("borrow t2 %+v reason %+v err %v", rt, reason, err)
		}
		if containsChannel(reason, "top") {
			t.Fatalf("borrowed a second step %+v", reason)
		}

		rt, _, err = call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT2}, nil)
		if err != nil || rt.Channel != "mid" {
			t.Fatalf("same tier %+v err %v", rt, err)
		}
		_, _, err = call(t, now, led, []model.Channel{channels[1]}, []model.Positioning{pos[1]}, model.Card{Kind: model.KindDev, Tier: model.TierT1}, nil)
		if !errors.Is(err, ErrNoCandidate) {
			t.Fatalf("t1 to t3 err %v", err)
		}
		rt, _, err = call(t, now, led, []model.Channel{channels[1]}, []model.Positioning{pos[1]}, model.Card{Kind: model.KindDev, Tier: model.TierT2}, nil)
		if err != nil || rt.Channel != "top" || rt.Model != "top-m" {
			t.Fatalf("t2 borrows t3 %+v err %v", rt, err)
		}
		_, _, err = call(t, now, led, []model.Channel{channels[0]}, []model.Positioning{pos[0]}, model.Card{Kind: model.KindDev, Tier: model.TierT3}, nil)
		if !errors.Is(err, ErrNoCandidate) {
			t.Fatalf("downward err %v", err)
		}
	})

	t.Run("unhealthy same tier borrows one", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "sick", modelName: "sick-m", family: "f", tier: model.TierT1, health: model.HealthAuthFailed, limit: 100},
			{id: "up", modelName: "up-m", family: "f", tier: model.TierT2, limit: 100},
		})
		rt, reason, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT1}, nil)
		if err != nil || rt.Channel != "up" || rt.Model != "up-m" {
			t.Fatalf("unhealthy tier did not borrow %+v err %v", rt, err)
		}
		got, ok := oneReason(reason, "sick")
		if !ok || got != ReasonHealth || !containsChannel(reason, "up") {
			t.Fatalf("reason %+v", reason)
		}
	})

	t.Run("review family mismatch", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "author", modelName: "author-m", family: "anthropic", tier: model.TierT3, limit: 100},
			{id: "other", modelName: "other-m", family: "openai", tier: model.TierT3, limit: 100},
		})
		card := model.Card{Kind: model.KindReview, Tier: model.TierT3, AuthorFamily: "anthropic"}
		rt, reason, err := call(t, now, led, channels, pos, card, nil)
		if err != nil || rt.Channel != "other" {
			t.Fatalf("route %+v err %v", rt, err)
		}
		got, ok := oneReason(reason, "author")
		if !ok || got != ReasonFamily {
			t.Fatalf("family %+v", reason.Filtered)
		}
		card.AuthorFamily = ""
		rt, reason, err = call(t, now, led, channels, pos, card, func(r *Request) {
			r.AuthorFamily = "openai"
		})
		if err != nil || rt.Channel != "author" {
			t.Fatalf("request family %+v err %v", rt, err)
		}
		if got, ok := oneReason(reason, "other"); !ok || got != ReasonFamily {
			t.Fatalf("family %+v", reason.Filtered)
		}
	})

	t.Run("tier order", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "low", modelName: "low-m", family: "f", tier: model.TierT1, limit: 100},
			{id: "mid", modelName: "mid-m", family: "f", tier: model.TierT2, limit: 100, used: 40},
		})
		rt, _, err := call(t, now, led, channels, pos, model.Card{Kind: model.KindDev}, func(r *Request) {
			r.Classified = model.TierT1
		})
		if err != nil || rt.Channel != "low" {
			t.Fatalf("classified %+v err %v", rt, err)
		}
		rt, _, err = call(t, now, led, channels, pos, model.Card{Kind: model.KindDev, Tier: model.TierT2}, func(r *Request) {
			r.Classified = model.TierT1
		})
		if err != nil || rt.Channel != "mid" {
			t.Fatalf("card tier %+v err %v", rt, err)
		}
		led, _, channels, pos = build(t, now, []spec{
			{id: "research", modelName: "research-m", family: "f", tier: model.TierT1, limit: 100},
		})
		rt, _, err = call(t, now, led, channels, pos, model.Card{Kind: model.KindResearch}, nil)
		if err != nil || rt.Channel != "research" {
			t.Fatalf("kind default %+v err %v", rt, err)
		}
	})

	t.Run("explicit pin", func(t *testing.T) {
		led, _, channels, pos := build(t, now, []spec{
			{id: "bad", modelName: "bad-m", family: "f", pref: 5, limit: 100, used: 99, redline: 0.2, conc: 1},
			{id: "good", modelName: "good-m", family: "f", pref: 0, limit: 100, used: 10},
			{id: "down", modelName: "down-m", family: "f", health: model.HealthDegraded, limit: 100},
		})
		reset := now.Add(3 * time.Hour)
		if err := led.ObserveLimit(ctx, "bad", reset); err != nil {
			t.Fatal(err)
		}
		card := model.Card{Kind: model.KindDev, Tier: model.TierT2, Channel: "bad"}
		rt, reason, err := call(t, now, led, channels, pos, card, func(r *Request) {
			r.InUse = map[string]int{"bad": 4}
			r.Objective = model.Objective{Channel: "good"}
		})
		if err != nil || rt.Channel != "bad" || rt.Model != "bad-m" {
			t.Fatalf("pin %+v err %v", rt, err)
		}
		if len(reason.Filtered) != 0 {
			t.Fatalf("pin filtered %+v", reason.Filtered)
		}
		card = model.Card{Kind: model.KindDev, Tier: model.TierT2, Channel: "missing"}
		rt, _, err = call(t, now, led, channels, pos, card, nil)
		if !errors.Is(err, ErrPinnedUnavailable) || rt.Channel != "" {
			t.Fatalf("missing %+v err %v", rt, err)
		}
		card = model.Card{Kind: model.KindDev, Tier: model.TierT2, Channel: "down"}
		rt, _, err = call(t, now, led, channels, pos, card, nil)
		if !errors.Is(err, ErrPinnedUnavailable) || rt.Channel != "" {
			t.Fatalf("unhealthy pin fell through %+v err %v", rt, err)
		}
		card = model.Card{Kind: model.KindDev, Tier: model.TierT2, Model: "bad-m"}
		rt, _, err = call(t, now, led, channels, pos, card, nil)
		if err != nil || rt.Channel != "bad" || rt.Model != "bad-m" {
			t.Fatalf("model pin %+v err %v", rt, err)
		}
	})
}

func TestBorrowOnlyAfterTierIsUnusable(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	type want struct {
		channel string
		err     error
		reasons map[string]string
		absent  []string
	}
	cases := []struct {
		name    string
		card    model.Card
		unknown string
		specs   []spec
		block   []string
		inUse   map[string]int
		want    want
	}{
		{
			name: "health", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{
				{id: "sick", modelName: "sick-m", tier: model.TierT1, health: model.HealthAuthFailed, limit: 100},
				{id: "up-sick", modelName: "up-sick-m", tier: model.TierT2, health: model.HealthDegraded, limit: 100},
				{id: "up", modelName: "up-m", tier: model.TierT2, limit: 100},
			},
			want: want{channel: "up", reasons: map[string]string{"sick": ReasonHealth, "up-sick": ReasonHealth}},
		},
		{
			name: "concurrency", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{
				{id: "busy", modelName: "busy-m", tier: model.TierT1, conc: 1, limit: 100},
				{id: "up-busy", modelName: "up-busy-m", tier: model.TierT2, conc: 1, limit: 100},
				{id: "up", modelName: "up-m", tier: model.TierT2, limit: 100},
			},
			inUse: map[string]int{"busy": 1, "up-busy": 1},
			want:  want{channel: "up", reasons: map[string]string{"busy": ReasonConcurrency, "up-busy": ReasonConcurrency}},
		},
		{
			name: "redline", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{
				{id: "low", modelName: "low-m", tier: model.TierT1, limit: 100, used: 90, redline: 0.2},
				{id: "up-low", modelName: "up-low-m", tier: model.TierT2, limit: 100, used: 90, redline: 0.2},
				{id: "up", modelName: "up-m", tier: model.TierT2, limit: 100, used: 10, redline: 0.2},
			},
			want: want{channel: "up", reasons: map[string]string{"low": ReasonRedline, "up-low": ReasonRedline}},
		},
		{
			name: "blocked", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{
				{id: "held", modelName: "held-m", tier: model.TierT1, limit: 100},
				{id: "up-held", modelName: "up-held-m", tier: model.TierT2, limit: 100},
				{id: "up", modelName: "up-m", tier: model.TierT2, limit: 100},
			},
			block: []string{"held", "up-held"},
			want:  want{channel: "up", reasons: map[string]string{"held": ReasonBlocked, "up-held": ReasonBlocked}},
		},
		{
			name: "cutoff", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{
				{id: "hot", modelName: "hot-m", tier: model.TierT1, limit: 100, used: 99},
				{id: "up-hot", modelName: "up-hot-m", tier: model.TierT2, limit: 100, used: 99},
				{id: "up", modelName: "up-m", tier: model.TierT2, limit: 100, used: 10},
			},
			want: want{channel: "up", reasons: map[string]string{"hot": ReasonCutoff, "up-hot": ReasonCutoff}},
		},
		{
			name: "unknown quota", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			unknown: "exclude",
			specs: []spec{
				{id: "mystery", modelName: "mystery-m", tier: model.TierT1},
				{id: "up-mystery", modelName: "up-mystery-m", tier: model.TierT2},
				{id: "up", modelName: "up-m", tier: model.TierT2, limit: 100},
			},
			want: want{channel: "up", reasons: map[string]string{"mystery": ReasonUnknownQuota, "up-mystery": ReasonUnknownQuota}},
		},
		{
			name: "review family", card: model.Card{Kind: model.KindReview, Tier: model.TierT1, AuthorFamily: "anthropic"},
			specs: []spec{
				{id: "author", modelName: "author-m", family: "anthropic", tier: model.TierT1, limit: 100},
				{id: "up-author", modelName: "up-author-m", family: "anthropic", tier: model.TierT2, limit: 100},
				{id: "up", modelName: "up-m", family: "openai", tier: model.TierT2, limit: 100},
			},
			want: want{channel: "up", reasons: map[string]string{"author": ReasonFamily, "up-author": ReasonFamily}},
		},
		{
			name: "request cap", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{
				{id: "hot", modelName: "hot-m", tier: model.TierT1, requests: 100, requestUse: 99},
				{id: "up-hot", modelName: "up-hot-m", tier: model.TierT2, requests: 100, requestUse: 99},
				{id: "up", modelName: "up-m", tier: model.TierT2, requests: 100, requestUse: 1},
			},
			want: want{channel: "up", reasons: map[string]string{"hot": ReasonCutoff, "up-hot": ReasonCutoff}},
		},
		{
			name: "two steps up", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{{id: "top", modelName: "top-m", tier: model.TierT3, limit: 100}},
			want:  want{err: ErrNoCandidate, absent: []string{"top"}},
		},
		{
			name: "unusable across two tiers", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{
				{id: "sick", modelName: "sick-m", tier: model.TierT1, health: model.HealthAuthFailed, limit: 100},
				{id: "sick2", modelName: "sick2-m", tier: model.TierT2, health: model.HealthAuthFailed, limit: 100},
				{id: "top", modelName: "top-m", tier: model.TierT3, limit: 100},
			},
			want: want{err: ErrNoCandidate, reasons: map[string]string{"sick": ReasonHealth, "sick2": ReasonHealth}, absent: []string{"top"}},
		},
		{
			name: "downward", card: model.Card{Kind: model.KindDev, Tier: model.TierT2},
			specs: []spec{{id: "low", modelName: "low-m", tier: model.TierT1, limit: 100}},
			want:  want{err: ErrNoCandidate, absent: []string{"low"}},
		},
		{
			name: "usable tier does not borrow", card: model.Card{Kind: model.KindDev, Tier: model.TierT1},
			specs: []spec{
				{id: "here", modelName: "here-m", tier: model.TierT1, limit: 100, used: 10},
				{id: "up", modelName: "up-m", tier: model.TierT2, limit: 100},
			},
			want: want{channel: "here", absent: []string{"up"}},
		},
		{
			name: "pin does not borrow", card: model.Card{Kind: model.KindDev, Tier: model.TierT1, Channel: "sick"},
			specs: []spec{
				{id: "sick", modelName: "sick-m", tier: model.TierT1, health: model.HealthAuthFailed, limit: 100},
				{id: "up", modelName: "up-m", tier: model.TierT2, limit: 100},
			},
			want: want{err: ErrPinnedUnavailable, absent: []string{"up"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			led, _, channels, pos := build(t, now, tc.specs)
			for _, id := range tc.block {
				if err := led.ObserveLimit(context.Background(), id, now.Add(2*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			rt, reason, err := call(t, now, led, channels, pos, tc.card, func(r *Request) {
				r.UnknownQuota = tc.unknown
				r.InUse = tc.inUse
			})
			if tc.want.err != nil {
				if !errors.Is(err, tc.want.err) || rt.Channel != "" {
					t.Fatalf("route %+v err %v reason %+v", rt, err, reason)
				}
			} else if err != nil || rt.Channel != tc.want.channel {
				t.Fatalf("route %+v err %v reason %+v", rt, err, reason)
			}
			for id, why := range tc.want.reasons {
				got, ok := oneReason(reason, id)
				if !ok || got != why {
					t.Fatalf("%s reason %q filtered %+v", id, got, reason.Filtered)
				}
			}
			for _, id := range tc.want.absent {
				if containsChannel(reason, id) {
					t.Fatalf("%s was considered %+v", id, reason)
				}
			}
		})
	}
}

func TestSelectRecoversAfterMisalignedReset(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	predicted := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	observed := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	led, clock, channels, pos := build(t, now, []spec{
		{id: "hot", modelName: "hot-m", family: "f", window: "5h", rule: "rolling", limit: 100, used: 99, anchor: predicted},
		{id: "spare", modelName: "spare-m", family: "f", window: "5h", rule: "rolling", limit: 100, used: 10, anchor: predicted},
	})
	if err := led.ObserveLimit(context.Background(), "hot", observed); err != nil {
		t.Fatal(err)
	}
	card := model.Card{Kind: model.KindDev, Tier: model.TierT2}
	rt, reason, err := call(t, now, led, channels, pos, card, nil)
	if err != nil || rt.Channel != "spare" {
		t.Fatalf("before reset route %+v err %v filtered %+v", rt, err, reason.Filtered)
	}
	if got, ok := oneReason(reason, "hot"); !ok || got != ReasonBlocked {
		t.Fatalf("before reset hot %+v", reason.Filtered)
	}
	*clock = observed.Add(-time.Second)
	rt, reason, err = call(t, observed.Add(-time.Second), led, channels, pos, card, nil)
	if err != nil || rt.Channel != "spare" {
		t.Fatalf("early lift route %+v err %v", rt, err)
	}
	if got, ok := oneReason(reason, "hot"); !ok || got != ReasonBlocked {
		t.Fatalf("early lift hot %+v", reason.Filtered)
	}
	*clock = observed
	rt, reason, err = call(t, observed, led, channels, pos, card, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := oneReason(reason, "hot"); ok && got == ReasonCutoff {
		t.Fatalf("stale 99/100 still cut off %+v", reason.Filtered)
	}
	if !containsChannel(reason, "hot") {
		t.Fatalf("hot dropped after observed reset route %+v reason %+v", rt, reason)
	}
}

type spec struct {
	id, modelName, family, source, window, rule string
	tier                                        model.Tier
	pref                                        int
	redline                                     float64
	health                                      model.Health
	conc                                        int
	limit, used, requests, requestUse           int64
	anchor                                      time.Time
	caps                                        []string
}

func build(t *testing.T, now time.Time, specs []spec) (*quota.Ledger, *time.Time, []model.Channel, []model.Positioning) {
	t.Helper()
	current := now
	led := quota.New()
	led.SetClock(func() time.Time { return current })
	channels := make([]model.Channel, 0, len(specs))
	pos := make([]model.Positioning, 0, len(specs))
	for _, s := range specs {
		if s.health == "" {
			s.health = model.HealthOK
		}
		if s.conc == 0 {
			s.conc = 2
		}
		if s.tier == "" {
			s.tier = model.TierT2
		}
		if s.window == "" {
			s.window = "5h"
		}
		if s.anchor.IsZero() {
			s.anchor = now
		}
		if s.source == "" {
			s.source = "sub"
		}
		if s.family == "" {
			s.family = "fam"
		}
		ch := model.Channel{
			ID: s.id, Runner: model.RunnerGrok, SourceGroup: s.source, Family: s.family,
			Models: []string{s.modelName}, Concurrency: s.conc, Health: s.health, Redline: s.redline,
		}
		if s.limit > 0 || s.requests > 0 {
			ch.QuotaWindows = []model.QuotaWindow{{
				Name: s.window, ResetRule: s.rule, LimitTokens: s.limit, LimitRequests: s.requests, ResetAt: s.anchor,
			}}
		}
		led.Configure(ch)
		if s.used > 0 {
			record(t, led, s.id, s.used, now)
		}
		extra := int(s.requestUse)
		if s.used > 0 && extra > 0 {
			extra--
		}
		for i := 0; i < extra; i++ {
			if err := led.Record(context.Background(), s.id, model.Usage{Source: "meter"}, now.Add(-time.Minute), now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		channels = append(channels, ch)
		pos = append(pos, model.Positioning{
			Model: s.modelName, Family: s.family, Tiers: []model.Tier{s.tier},
			Capabilities: append([]string(nil), s.caps...), Preference: s.pref,
		})
	}
	return led, &current, channels, pos
}

func call(t *testing.T, now time.Time, led *quota.Ledger, channels []model.Channel, pos []model.Positioning, card model.Card, mutate func(*Request)) (model.Route, model.RouteReason, error) {
	t.Helper()
	req := Request{Card: card, Channels: channels, Positioning: pos, Quota: led, Now: now}
	if mutate != nil {
		mutate(&req)
	}
	return Select(context.Background(), req)
}

func record(t *testing.T, led *quota.Ledger, id string, tokens int64, at time.Time) {
	t.Helper()
	err := led.Record(context.Background(), id, model.Usage{Tokens: tokens, Source: "meter"}, at.Add(-time.Minute), at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
}

func mustEst(t *testing.T, led *quota.Ledger, id string) quota.Estimate {
	t.Helper()
	est, err := led.Remaining(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return est
}

func expectPace(est quota.Estimate, now time.Time) float64 {
	hours := est.Reset.Sub(now).Hours()
	if hours < 1 {
		hours = 1
	}
	return est.Ratio / hours
}

func near(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func oneReason(reason model.RouteReason, id string) (string, bool) {
	var got string
	n := 0
	for _, item := range reason.Filtered {
		if item.Channel == id {
			got = item.Reason
			n++
		}
	}
	return got, n == 1
}

func containsChannel(reason model.RouteReason, id string) bool {
	for _, item := range reason.Candidates {
		if item.Channel == id {
			return true
		}
	}
	for _, item := range reason.Filtered {
		if item.Channel == id {
			return true
		}
	}
	return false
}

func loadClaude(t *testing.T) model.Channel {
	t.Helper()
	b, err := os.ReadFile("../quota/testdata/claude-5h.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Channels []model.Channel `json:"channels"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Channels) != 1 {
		t.Fatalf("channels %d", len(doc.Channels))
	}
	ch := doc.Channels[0]
	if ch.Runner != model.RunnerClaude || ch.ID != "claude-primary" || ch.Redline != 0.2 {
		t.Fatalf("example %+v", ch)
	}
	if len(ch.QuotaWindows) != 1 || ch.QuotaWindows[0].Name != "5h" || ch.QuotaWindows[0].LimitTokens != 100000 {
		t.Fatalf("window %+v", ch.QuotaWindows)
	}
	return ch
}
