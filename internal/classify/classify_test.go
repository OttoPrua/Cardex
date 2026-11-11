package classify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestClassifySkipCacheAndRule(t *testing.T) {
	ctx := context.Background()
	if ClassifyTimeout != 30*time.Second {
		t.Fatalf("timeout %s", ClassifyTimeout)
	}

	t.Run("skip", func(t *testing.T) {
		calls := 0
		card := model.Card{Kind: model.KindDev, Tier: model.TierT3, Body: "already"}
		res, err := Classify(ctx, card, Options{Channel: "classifier", Call: func(context.Context, string, string) (string, error) {
			calls++
			return "", nil
		}})
		if err != nil || calls != 0 || res.Tier != model.TierT3 {
			t.Fatalf("skip res %+v calls %d err %v", res, calls, err)
		}
	})

	t.Run("cache", func(t *testing.T) {
		calls := 0
		cache := NewCache()
		call := func(ctx context.Context, channelID, prompt string) (string, error) {
			calls++
			wantBody := "same body"
			if calls > 1 {
				wantBody = "different body"
			}
			if channelID != "classifier" || !strings.Contains(prompt, PromptTemplate) || !strings.Contains(prompt, wantBody) {
				t.Fatalf("prompt channel %s %q", channelID, prompt)
			}
			dl, ok := ctx.Deadline()
			if !ok {
				t.Fatal("no deadline")
			}
			remain := time.Until(dl)
			if remain < 28*time.Second || remain > 30*time.Second {
				t.Fatalf("deadline %s", remain)
			}
			return "TIER: T1\nREASON: small change\n", nil
		}
		opt := Options{Channel: "classifier", Call: call, Cache: cache}
		first, err := Classify(ctx, model.Card{Kind: model.KindDev, Title: "one", Body: "same body"}, opt)
		if err != nil || first.Tier != model.TierT1 || first.Source != SourceModel || first.Reason != "small change" {
			t.Fatalf("first %+v err %v", first, err)
		}
		second, err := Classify(ctx, model.Card{Kind: model.KindDev, Title: "other title", Body: "same body"}, opt)
		if err != nil || second.Tier != model.TierT1 || second.Source != SourceCache || calls != 1 {
			t.Fatalf("cache %+v calls %d err %v", second, calls, err)
		}
		third, err := Classify(ctx, model.Card{Kind: model.KindDev, Body: "different body"}, opt)
		if err != nil || third.Source != SourceModel || calls != 2 {
			t.Fatalf("miss %+v calls %d err %v", third, calls, err)
		}
	})

	t.Run("timeout unparseable unavailable", func(t *testing.T) {
		timeout, err := Classify(ctx, model.Card{Kind: model.KindDev, Body: "slow"}, Options{
			Channel: "classifier",
			Call: func(ctx context.Context, channelID, prompt string) (string, error) {
				dl, ok := ctx.Deadline()
				if !ok || channelID != "classifier" || !strings.HasPrefix(prompt, PromptTemplate) {
					t.Fatalf("call setup ok %v channel %s", ok, channelID)
				}
				if remain := time.Until(dl); remain < 28*time.Second || remain > 30*time.Second {
					t.Fatalf("deadline %s", remain)
				}
				return "", context.DeadlineExceeded
			},
		})
		if err != nil || timeout.Source != SourceRule || timeout.Tier != model.TierT2 {
			t.Fatalf("timeout %+v err %v", timeout, err)
		}
		bad, err := Classify(ctx, model.Card{Kind: model.KindResearch, Body: "junk"}, Options{
			Channel: "classifier",
			Call:    func(context.Context, string, string) (string, error) { return "not a tier", nil },
		})
		if err != nil || bad.Source != SourceRule || bad.Tier != model.TierT1 {
			t.Fatalf("unparseable %+v err %v", bad, err)
		}
		calls := 0
		down, err := Classify(ctx, model.Card{Kind: model.KindDesign, Body: "x"}, Options{
			Channel: "",
			Call: func(context.Context, string, string) (string, error) {
				calls++
				return "TIER: T1\nREASON: no\n", nil
			},
		})
		if err != nil || calls != 0 || down.Source != SourceRule || down.Tier != model.TierT3 {
			t.Fatalf("unavailable %+v calls %d err %v", down, calls, err)
		}
		none, err := Classify(ctx, model.Card{Kind: model.KindNormalize, Body: "x"}, Options{Channel: "classifier"})
		if err != nil || none.Source != SourceRule || none.Tier != model.TierT1 {
			t.Fatalf("nil call %+v err %v", none, err)
		}
	})

	t.Run("kind defaults", func(t *testing.T) {
		for _, kind := range model.AllCardKinds() {
			res, err := Classify(ctx, model.Card{Kind: kind, Body: string(kind)}, Options{
				Channel: "classifier",
				Call:    func(context.Context, string, string) (string, error) { return "", context.DeadlineExceeded },
			})
			if err != nil || res.Source != SourceRule || res.Tier != model.DefaultTier(kind) {
				t.Fatalf("%s %+v err %v", kind, res, err)
			}
		}
	})
}
