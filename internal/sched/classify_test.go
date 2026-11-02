package sched

import (
	"context"
	"errors"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/prompt"
	"cardex/internal/store"
)

// TestLaunchClassifiesEmptyTier drives Tick, the real dispatch and backlog-fill path.
// An empty tier is classified before the route is chosen. A preset tier does not call Call.
func TestLaunchClassifiesEmptyTier(t *testing.T) {
	t1 := func(context.Context, string, string) (string, error) {
		return "TIER: T1\nREASON: small change\n", nil
	}
	t3 := func(context.Context, string, string) (string, error) {
		return "TIER: T3\nREASON: broad design\n", nil
	}
	bad := func(context.Context, string, string) (string, error) {
		return "", errors.New("classifier down")
	}
	junk := func(context.Context, string, string) (string, error) {
		return "not a tier\n", nil
	}
	cases := []struct {
		name      string
		kind      model.CardKind
		tier      model.Tier
		via       string
		call      func(context.Context, string, string) (string, error)
		want      model.Tier
		wantCalls int
	}{
		{name: "dispatch empty success", kind: model.KindDev, via: "dispatch", call: t1, want: model.TierT1, wantCalls: 1},
		{name: "dispatch call failure", kind: model.KindDev, via: "dispatch", call: bad, want: model.DefaultTier(model.KindDev), wantCalls: 1},
		{name: "dispatch unusable text", kind: model.KindDev, via: "dispatch", call: junk, want: model.DefaultTier(model.KindDev), wantCalls: 1},
		{name: "dispatch nil call", kind: model.KindDev, via: "dispatch", want: model.DefaultTier(model.KindDev)},
		{name: "dispatch tier already set", kind: model.KindDev, tier: model.TierT3, via: "dispatch", call: t1, want: model.TierT3},
		{name: "fill empty success", kind: model.KindBacklog, via: "fill", call: t3, want: model.TierT3, wantCalls: 1},
		{name: "fill call failure", kind: model.KindBacklog, via: "fill", call: bad, want: model.DefaultTier(model.KindBacklog), wantCalls: 1},
		{name: "fill tier already set", kind: model.KindBacklog, tier: model.TierT2, via: "fill", call: t3, want: model.TierT2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			if tc.via == "fill" {
				channelNear(t, db, "ch", now.Add(30*time.Minute), 0.9, 0)
			} else {
				seedRoutes(t, db)
			}
			addClassifierChannel(t, db)
			calls := 0
			var call func(context.Context, string, string) (string, error)
			if tc.call != nil {
				inner := tc.call
				call = func(ctx context.Context, channelID, promptText string) (string, error) {
					calls++
					if channelID != classifierChannelID || promptText == "" {
						t.Errorf("call channel %q prompt %q", channelID, promptText)
					}
					return inner(ctx, channelID, promptText)
				}
			}
			card := model.Card{
				ID: "c1", Objective: obj, Kind: tc.kind, Title: tc.name, Tier: tc.tier,
				Status: model.CardReady,
				Body:   `{"attempts":{"0":{"hold":"3s","write_files":[{"path":"note.txt","content":"x\n"}]}}}`,
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			r.Classify = call
			r.Prompt = func(context.Context, prompt.Input) (string, error) { return "PROMPT", nil }
			if tc.via == "fill" {
				r.Now = func() time.Time { return now }
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(readLog(home, err))
			}
			t.Cleanup(func() {
				attempts, err := db.ListAttempts(context.Background(), "")
				if err != nil {
					return
				}
				for _, attempt := range attempts {
					killHost(home, attempt.ID, attempt.PID)
				}
			})
			got, err := db.GetCard(ctx, "c1")
			if err != nil {
				t.Fatal(err)
			}
			if got.Tier != tc.want || got.Tier == model.TierNone {
				t.Fatalf("stored tier %q, want %q", got.Tier, tc.want)
			}
			if calls != tc.wantCalls {
				t.Fatalf("Call count %d, want %d", calls, tc.wantCalls)
			}
			attempts, err := db.ListAttempts(ctx, "c1")
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 {
				t.Fatalf("attempts %d, want the launch path to route once", len(attempts))
			}
			if attempts[0].Route.Channel == "" {
				t.Fatal("route channel empty")
			}
			if len(attempts[0].RouteReason.Candidates) == 0 {
				t.Fatal("route reason has no candidate")
			}
			for _, cand := range attempts[0].RouteReason.Candidates {
				if cand.Tier != tc.want {
					t.Fatalf("candidate tier %q, want %q in %+v", cand.Tier, tc.want, attempts[0].RouteReason.Candidates)
				}
			}
		})
	}
}

func addClassifierChannel(t *testing.T, db *store.Store) {
	t.Helper()
	ch := model.Channel{
		ID: classifierChannelID, Runner: model.RunnerFake, Family: "classifier",
		Health: model.HealthOK, Concurrency: 1,
	}
	if err := db.CreateChannel(context.Background(), &ch); err != nil {
		t.Fatal(err)
	}
}
