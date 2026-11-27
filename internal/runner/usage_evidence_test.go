package runner

import "testing"

func TestGrokUsageSamplesAreNotAddedToTerminalUsage(t *testing.T) {
	raw := []byte(`{"type":"progress","usage":{"total_tokens":40}}
{"type":"progress","usage":{"total_tokens":85}}
{"type":"end","usage":{"total_tokens":100,"input_tokens":60,"output_tokens":40}}
{"type":"end","usage":{"total_tokens":100,"input_tokens":60,"output_tokens":40}}`)
	u := (Grok{}).ParseUsage(raw)
	if u.Tokens != 100 || u.Source == "" {
		t.Fatalf("terminal snapshot was not used once: %+v", u)
	}
}

func TestGrokUsageMissingVersusExplicitZero(t *testing.T) {
	missing := (Grok{}).ParseUsage([]byte(`{"type":"end","message":"no metering"}`))
	zero := (Grok{}).ParseUsage([]byte(`{"type":"end","usage":{"total_tokens":0}}`))
	if missing.Tokens != 0 || missing.Source != "" || zero.Tokens != 0 || zero.Source == "" {
		t.Fatalf("missing %+v explicit zero %+v", missing, zero)
	}
}

func TestUsageComponentsCannotOverflowToNegative(t *testing.T) {
	u := (Grok{}).ParseUsage([]byte(`{"type":"end","usage":{"input_tokens":9223372036854775807,"output_tokens":1}}`))
	if u.Tokens != 0 || u.Source != "" {
		t.Fatalf("invalid overflow was reported as real usage: %+v", u)
	}
}

func TestGrokUsageReceiptSeparatesUnknownAndTurns(t *testing.T) {
	g := Grok{}
	raw := []byte(`{"type":"progress","usage":{"total_tokens":35}}
{"type":"end","num_turns":4,"usage":{"total_tokens":55,"input_tokens":0,"output_tokens":5}}
{"type":"end","num_turns":4,"usage":{"total_tokens":55,"input_tokens":0,"output_tokens":5}}`)
	u := DescribeUsage(g, raw, g.ParseUsage(raw))
	if !u.Terminal || u.Disagreement || u.Total == nil || *u.Total != 55 || u.Input == nil || *u.Input != 0 || u.Output == nil || *u.Output != 5 || u.CacheRead != nil || u.ToolCalls != nil || u.ProviderTurns == nil || *u.ProviderTurns != 4 {
		t.Fatalf("unexpected receipt: %+v", u)
	}
	raw = append(raw, []byte("\n{\"type\":\"progress\",\"usage\":{\"total_tokens\":56}}")...)
	u = DescribeUsage(g, raw, g.ParseUsage(raw))
	if !u.Terminal || !u.Disagreement || u.Input != nil || *u.Total != 56 {
		t.Fatalf("mixed samples passed off as a terminal breakdown: %+v", u)
	}
}
