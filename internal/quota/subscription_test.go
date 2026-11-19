package quota

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCodexBarSubscriptionProviders(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ provider, source string }{
		{"codex", "codex-cli"}, {"codex", "oauth"}, {"claude", "oauth"},
		{"cursor", "web"}, {"grok", "grok-cli"}, {"grok", "grok-cli-proxy"},
		{"gemini", "oauth-api"}, {"kimi", "Kimi Code API key"}, {"kimi", "Kimi Code CLI"},
	} {
		t.Run(tc.provider+"/"+tc.source, func(t *testing.T) {
			payload := subscriptionPayload(now)
			payload["provider"], payload["source"] = tc.provider, tc.source
			b, _ := json.Marshal([]any{payload})
			s, err := ParseCodexBar(strings.NewReader(string(b)), tc.provider, now)
			if err != nil || s.State != "known" || len(s.Windows) != 3 || *s.Windows[0].Remaining != .75 || *s.Windows[1].Remaining != 0 || s.Windows[2].Remaining != nil {
				t.Fatalf("%+v: %v", s, err)
			}
			if s.Windows[0].Name != "5 hours" || s.Windows[1].Name != "Weekly" || s.Windows[2].Name != "Monthly" {
				t.Fatalf("labels lost: %+v", s.Windows)
			}
		})
	}
}

func TestCodexBarUnknownNeverBecomesZero(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing-percent", func(p map[string]any) { delete(primary(p), "usedPercent") }},
		{"null-percent", func(p map[string]any) { primary(p)["usedPercent"] = nil }},
		{"negative-percent", func(p map[string]any) { primary(p)["usedPercent"] = -1 }},
		{"over-100-percent", func(p map[string]any) { primary(p)["usedPercent"] = 101 }},
		{"placeholder", func(p map[string]any) { primary(p)["isSyntheticPlaceholder"] = true }},
		{"expired-reset", func(p map[string]any) { primary(p)["resetsAt"] = now }},
		{"estimated", func(p map[string]any) { usage(p)["dataConfidence"] = "estimated" }},
		{"unknown-confidence", func(p map[string]any) { usage(p)["dataConfidence"] = "unknown" }},
		{"future-confidence", func(p map[string]any) { usage(p)["dataConfidence"] = "new-value" }},
		{"old", func(p map[string]any) { usage(p)["updatedAt"] = now.Add(-SubscriptionMaxAge - time.Second) }},
		{"future", func(p map[string]any) { usage(p)["updatedAt"] = now.Add(2 * time.Minute) }},
		{"no-time", func(p map[string]any) { delete(usage(p), "updatedAt") }},
		{"error-with-cached-usage", func(p map[string]any) { p["error"] = map[string]any{"message": "secret-sentinel"} }},
		{"unsupported-source", func(p map[string]any) { p["source"] = "openai-web" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := subscriptionPayload(now)
			tc.edit(p)
			b, _ := json.Marshal([]any{p})
			s, err := ParseCodexBar(strings.NewReader(string(b)), "codex", now)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Windows) > 0 && s.Windows[0].Remaining != nil {
				t.Fatalf("invalid usage became known: %+v", s)
			}
		})
	}
	for _, raw := range []string{"[]", "{}", "null", "[{},{}]", "[", strings.Repeat(" ", subscriptionMaxBytes+1)} {
		if _, err := ParseCodexBar(strings.NewReader(raw), "codex", now); err == nil {
			t.Fatal("accepted malformed/ambiguous input")
		}
	}
	for _, percent := range []string{"NaN", "Infinity", "1e1000", `"secret-sentinel"`} {
		raw := `[{"provider":"codex","source":"codex-cli","usage":{"primary":{"usedPercent":` + percent + `}}}]`
		if _, err := ParseCodexBar(strings.NewReader(raw), "codex", now); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatal("accepted non-numeric usage or leaked its value")
		}
	}
	for _, pair := range [][2]string{{"claude", "admin-api"}, {"claude", "web"}, {"grok", "grok-web"}, {"gemini", "Gemini app"}, {"kimi", "Kimi web cookie"}} {
		if subscriptionSource(pair[0], pair[1]) {
			t.Fatalf("accepted unsupported subscription source %v", pair)
		}
	}
}

func TestSubscriptionPrivacyFreshnessAndGroups(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	p := subscriptionPayload(now)
	p["account"] = "secret-sentinel"
	p["diagnostic"] = "secret-sentinel"
	p["cacheAccountKey"] = "secret-sentinel"
	p["access_token"] = "secret-sentinel"
	usage(p)["identity"] = map[string]any{"email": "secret-sentinel"}
	p["credits"] = map[string]any{"remaining": 0, "updatedAt": now, "balanceReadSucceeded": true, "events": []any{"secret-sentinel"}}
	b, _ := json.Marshal([]any{p})
	s, err := ParseCodexBar(strings.NewReader(string(b)), "codex", now)
	if err != nil || s.Credits == nil || *s.Credits != 0 {
		t.Fatalf("zero credits lost: %+v %v", s, err)
	}
	home := t.TempDir()
	if err := SaveSubscription(home, "shared", "a", s); err != nil {
		t.Fatal(err)
	}
	path := subscriptionPath(home, "shared", "a")
	stored, _ := os.ReadFile(path)
	if strings.Contains(string(stored), "secret-sentinel") {
		t.Fatal("non-allowlisted fields retained")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatal("snapshot is not private")
	}
	shared, err := ReadSubscription(home, "shared", "b", now)
	if err != nil || shared == nil || *shared.Windows[0].Remaining != .75 {
		t.Fatal("subscription group was not shared")
	}
	for _, scope := range [][2]string{{"other", "a"}, {"", "a"}, {"", "b"}} {
		other, err := ReadSubscription(home, scope[0], scope[1], now)
		if err != nil || other != nil {
			t.Fatal("subscription crossed group/channel boundary")
		}
	}
	old, err := ReadSubscription(home, "shared", "b", now.Add(SubscriptionMaxAge+time.Second))
	if err != nil || old.State != "stale" || old.Windows[0].Remaining != nil || old.Credits != nil {
		t.Fatalf("stale snapshot remained known: %+v %v", old, err)
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), -.1, 1.1} {
		s.Windows[0].Remaining = &value
		if s.current(now).Windows[0].Remaining != nil {
			t.Fatal("invalid persisted ratio was accepted")
		}
	}
}

func TestCodexCreditsMustBeFreshAndConfirmed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		at time.Time
		ok bool
	}{
		{now.Add(-SubscriptionMaxAge - time.Second), true},
		{now.Add(2 * time.Minute), true},
		{now, false},
	} {
		p := subscriptionPayload(now)
		p["credits"] = map[string]any{"remaining": 5, "updatedAt": tc.at, "balanceReadSucceeded": tc.ok}
		b, _ := json.Marshal([]any{p})
		s, err := ParseCodexBar(strings.NewReader(string(b)), "codex", now)
		if err != nil || s.Credits != nil {
			t.Fatal("unconfirmed or stale credits were accepted")
		}
	}
}

func subscriptionPayload(now time.Time) map[string]any {
	return map[string]any{
		"provider": "codex", "source": "codex-cli",
		"rateWindowLabels": map[string]any{"primary": "5 hours", "secondary": "Weekly"},
		"usage": map[string]any{
			"updatedAt":        now,
			"primary":          map[string]any{"usedPercent": 25, "resetsAt": now.Add(time.Hour)},
			"secondary":        map[string]any{"usedPercent": 100},
			"extraRateWindows": []any{map[string]any{"title": "Monthly", "usageKnown": false, "window": map[string]any{"usedPercent": 100}}},
		},
	}
}

func usage(p map[string]any) map[string]any   { return p["usage"].(map[string]any) }
func primary(p map[string]any) map[string]any { return usage(p)["primary"].(map[string]any) }
