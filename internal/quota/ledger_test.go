package quota

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestResetStylesAndWindowNames(t *testing.T) {
	ctx := context.Background()

	t.Run("rolling 5h", func(t *testing.T) {
		now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
		reset := now.Add(5 * time.Hour)
		l, clock := clocked(now)
		ch := channel("c", "5h", "rolling", reset, 100)
		l.Configure(ch)
		record(t, l, "c", 10, 1, now.Add(-time.Minute), now)
		est := mustRemaining(t, l, "c")
		if est.Unknown || est.UsedTokens != 10 || !est.Reset.Equal(reset) {
			t.Fatalf("inside window %+v", est)
		}
		*clock = reset
		est = mustRemaining(t, l, "c")
		if est.UsedTokens != 0 || !est.Reset.After(reset) {
			t.Fatalf("usage remained after rolling reset %+v", est)
		}
		record(t, l, "c", 4, 1, reset, reset.Add(time.Minute))
		est = mustRemaining(t, l, "c")
		if est.UsedTokens != 4 {
			t.Fatalf("new window %+v", est)
		}
	})

	t.Run("fixed 5h", func(t *testing.T) {
		now := time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC)
		anchor := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
		l, clock := clocked(now)
		l.Configure(channel("c", "5h", "fixed", anchor, 100))
		record(t, l, "c", 7, 1, now.Add(-time.Minute), now)
		est := mustRemaining(t, l, "c")
		if est.UsedTokens != 7 || !est.Reset.Equal(anchor.Add(5*time.Hour)) {
			t.Fatalf("fixed 5h %+v", est)
		}
		*clock = anchor.Add(5 * time.Hour)
		est = mustRemaining(t, l, "c")
		if est.UsedTokens != 0 {
			t.Fatalf("fixed 5h kept usage %+v", est)
		}
	})

	t.Run("fixed daily", func(t *testing.T) {
		now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
		anchor := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
		l, clock := clocked(now)
		l.Configure(channel("c", "daily", "fixed", anchor, 100))
		record(t, l, "c", 10, 1, now.Add(-time.Minute), now)
		est := mustRemaining(t, l, "c")
		if est.UsedTokens != 10 || !est.Reset.Equal(anchor) {
			t.Fatalf("daily before reset %+v", est)
		}
		*clock = anchor
		est = mustRemaining(t, l, "c")
		if est.UsedTokens != 0 || !est.Reset.Equal(anchor.Add(24*time.Hour)) {
			t.Fatalf("daily after reset %+v", est)
		}
	})

	t.Run("weekly and monthly", func(t *testing.T) {
		now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		l, _ := clocked(now)
		l.Configure(model.Channel{ID: "w", QuotaWindows: []model.QuotaWindow{{
			Name: "weekly", ResetRule: "fixed", ResetAt: now, LimitTokens: 70,
		}}})
		record(t, l, "w", 3, 1, now, now)
		est := mustRemaining(t, l, "w")
		if est.Unknown || est.UsedTokens != 3 || est.Reset.Sub(now) != 7*24*time.Hour {
			t.Fatalf("weekly %+v", est)
		}
		l.Configure(model.Channel{ID: "m", QuotaWindows: []model.QuotaWindow{{
			Name: "monthly", ResetRule: "rolling", ResetAt: now.AddDate(0, 0, 10), LimitTokens: 30,
		}}})
		record(t, l, "m", 5, 1, now, now)
		est = mustRemaining(t, l, "m")
		if est.Unknown || est.UsedTokens != 5 || !est.Reset.After(now) {
			t.Fatalf("monthly %+v", est)
		}
		_ = ctx
	})
}

func TestRecordKnownUnknownAndSeveralWindows(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	l, clock := clocked(now)
	fiveEnd := now.Add(time.Hour)
	dailyEnd := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	l.Configure(model.Channel{ID: "mix", QuotaWindows: []model.QuotaWindow{
		{Name: "5h", ResetRule: "rolling", ResetAt: fiveEnd, LimitTokens: 50},
		{Name: "daily", ResetRule: "fixed", ResetAt: dailyEnd, LimitTokens: 100},
	}})
	record(t, l, "mix", 40, 1, now.Add(-time.Minute), now)
	est := mustRemaining(t, l, "mix")
	if est.UsedTokens != 40 || est.LimitTokens != 50 || est.Ratio != 0.2 || !est.Reset.Equal(fiveEnd) {
		t.Fatalf("both windows should count, tightest is 5h: %+v", est)
	}
	*clock = fiveEnd
	est = mustRemaining(t, l, "mix")
	if est.UsedTokens != 40 || est.LimitTokens != 100 || est.Ratio != 0.6 || !est.Reset.Equal(dailyEnd) {
		t.Fatalf("5h reset should drop, daily should remain: %+v", est)
	}

	l.Configure(channel("known", "5h", "rolling", now.Add(time.Hour), 1000))
	l.Configure(channel("short", "5h", "rolling", now.Add(time.Hour), 1000))
	l.Configure(channel("long", "5h", "rolling", now.Add(time.Hour), 1000))
	l.Configure(channel("zero", "5h", "rolling", now.Add(time.Hour), 1000))
	*clock = now
	record(t, l, "known", 100, 1, now.Add(-time.Minute), now)
	if err := l.Record(ctx, "short", model.Usage{}, now, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ctx, "long", model.Usage{}, now, now.Add(100*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ctx, "zero", model.Usage{}, now, now); err != nil {
		t.Fatal(err)
	}
	known := mustRemaining(t, l, "known")
	short := mustRemaining(t, l, "short")
	long := mustRemaining(t, l, "long")
	zero := mustRemaining(t, l, "zero")
	if known.Confidence != 1 || short.Confidence >= known.Confidence || long.Confidence >= known.Confidence {
		t.Fatalf("confidence known %v short %v long %v", known.Confidence, short.Confidence, long.Confidence)
	}
	if short.UsedTokens <= 0 || long.UsedTokens <= short.UsedTokens || zero.UsedTokens <= 0 {
		t.Fatalf("unknown usage short %d long %d zero %d", short.UsedTokens, long.UsedTokens, zero.UsedTokens)
	}

	bare := New()
	bare.Configure(model.Channel{ID: "nolimit", QuotaWindows: []model.QuotaWindow{{Name: "daily", ResetRule: "rolling"}}})
	est, err := bare.Remaining(ctx, "nolimit")
	if err != nil || !est.Unknown {
		t.Fatalf("no limit %+v err %v", est, err)
	}
}

func TestObserveLimitBlockLifts(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	l, clock := clocked(now)
	l.Configure(channel("c", "5h", "rolling", now.Add(5*time.Hour), 100))
	record(t, l, "c", 10, 1, now.Add(-time.Minute), now)
	if err := l.ObserveLimit(context.Background(), "c", reset); err != nil {
		t.Fatal(err)
	}
	est := mustRemaining(t, l, "c")
	if !est.Blocked || !est.Reset.Equal(reset) || est.Ratio <= 0.2 {
		t.Fatalf("blocked %+v", est)
	}
	*clock = reset.Add(-time.Second)
	est = mustRemaining(t, l, "c")
	if !est.Blocked {
		t.Fatal("block lifted early")
	}
	*clock = reset
	est = mustRemaining(t, l, "c")
	if est.Blocked {
		t.Fatalf("block remained at reset %+v", est)
	}
}

func TestClaudeFiveHourRedlineExample(t *testing.T) {
	b, err := os.ReadFile("testdata/claude-5h.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Channels []model.Channel `json:"channels"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Channels) != 1 {
		t.Fatalf("channels %d", len(cfg.Channels))
	}
	ch := cfg.Channels[0]
	if ch.Runner != model.RunnerClaude || ch.ID != "claude-primary" {
		t.Fatalf("channel %+v", ch)
	}
	if len(ch.QuotaWindows) != 1 || ch.QuotaWindows[0].Name != "5h" || ch.Redline != 0.2 {
		t.Fatalf("5h redline window %+v redline %v", ch.QuotaWindows, ch.Redline)
	}
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	l, _ := clocked(now)
	l.Configure(ch)
	record(t, l, ch.ID, 80000, 1, now.Add(-time.Minute), now)
	est := mustRemaining(t, l, ch.ID)
	if est.Unknown || est.LimitTokens != 100000 || est.UsedTokens != 80000 || est.Ratio > ch.Redline {
		t.Fatalf("claude 5h reserve is the redline, remaining %+v redline %v", est, ch.Redline)
	}
}

func TestRequestCapRemainingRatio(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		tokens     int64
		requests   int64
		usage      []int64
		unknown    bool
		ratio      float64
		usedTokens int64
		usedReqs   int64
	}{
		{name: "request only", requests: 4, usage: []int64{0, 0}, ratio: 0.5, usedReqs: 2},
		{name: "tokens tighter", tokens: 100, requests: 10, usage: []int64{50}, ratio: 0.5, usedTokens: 50, usedReqs: 1},
		{name: "requests tighter", tokens: 100, requests: 10, usage: []int64{1, 1, 1, 1, 1, 1, 1, 1}, ratio: 0.2, usedTokens: 8, usedReqs: 8},
		{name: "neither limit", unknown: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := clocked(now)
			l.Configure(model.Channel{ID: "c", QuotaWindows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: now.Add(time.Hour), LimitTokens: tc.tokens, LimitRequests: tc.requests,
			}}})
			for _, tokens := range tc.usage {
				if err := l.Record(context.Background(), "c", model.Usage{Tokens: tokens, Source: "meter"}, now.Add(-time.Minute), now); err != nil {
					t.Fatal(err)
				}
			}
			est := mustRemaining(t, l, "c")
			if est.Unknown != tc.unknown {
				t.Fatalf("unknown %v estimate %+v", est.Unknown, est)
			}
			if tc.unknown {
				return
			}
			if est.Ratio != tc.ratio || est.UsedRequests != tc.usedReqs || est.LimitRequests != tc.requests {
				t.Fatalf("ratio estimate %+v", est)
			}
			if tc.tokens > 0 && (est.UsedTokens != tc.usedTokens || est.LimitTokens != tc.tokens) {
				t.Fatalf("token side %+v", est)
			}
			if tc.tokens == 0 && est.LimitTokens != 0 {
				t.Fatalf("request-only window kept a token limit %+v", est)
			}
		})
	}
}

func TestObserveLimitKeepsOtherWindows(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	shortEnd := now.Add(time.Hour)
	weekEnd := now.Add(7 * 24 * time.Hour)
	cases := []struct {
		name    string
		observe time.Time
		used    int64
	}{
		{name: "short window only", observe: shortEnd, used: 40},
		{name: "no window", observe: now.Add(30 * time.Minute), used: 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, clock := clocked(now)
			l.Configure(model.Channel{ID: "c", QuotaWindows: []model.QuotaWindow{
				{Name: "5h", ResetRule: "rolling", ResetAt: shortEnd, LimitTokens: 1000},
				{Name: "weekly", ResetRule: "rolling", ResetAt: weekEnd, LimitTokens: 100},
			}})
			record(t, l, "c", tc.used, 1, now.Add(-time.Minute), now)
			if err := l.ObserveLimit(context.Background(), "c", tc.observe); err != nil {
				t.Fatal(err)
			}
			*clock = tc.observe
			est := mustRemaining(t, l, "c")
			if est.Blocked || est.Unknown || est.UsedTokens != tc.used || est.LimitTokens != 100 || est.Ratio >= 1 {
				t.Fatalf("longer window lost usage %+v", est)
			}
		})
	}
}

func clocked(now time.Time) (*Ledger, *time.Time) {
	current := now
	l := New()
	l.SetClock(func() time.Time { return current })
	return l, &current
}

func channel(id, name, rule string, anchor time.Time, limit int64) model.Channel {
	return model.Channel{ID: id, QuotaWindows: []model.QuotaWindow{{
		Name: name, ResetRule: rule, ResetAt: anchor, LimitTokens: limit,
	}}}
}

func record(t *testing.T, l *Ledger, id string, tokens int64, conf float64, started, ended time.Time) {
	t.Helper()
	if err := l.Record(context.Background(), id, model.Usage{Tokens: tokens, Source: "measured", Confidence: conf}, started, ended); err != nil {
		t.Fatal(err)
	}
}

func mustRemaining(t *testing.T, l *Ledger, id string) Estimate {
	t.Helper()
	est, err := l.Remaining(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return est
}
