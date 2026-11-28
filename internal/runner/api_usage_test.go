package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/apiusage"
)

func TestAPI3RecordedClaudeNumbersNotExecutorCost(t *testing.T) {
	raw := []byte(`{"type":"result","result":"synthetic answer canary","usage":{"input_tokens":2413,"output_tokens":22593,
	  "cache_creation_input_tokens":0,"cache_read_input_tokens":0,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},
	  "cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"service_tier":"standard","speed":"standard"},
	  "modelUsage":{"fixture-a":{"inputTokens":2413,"outputTokens":22593,"cacheReadInputTokens":0,
	  "cacheCreationInputTokens":0,"webSearchRequests":0,"costUSD":0.57689,"canonicalModel":"forged-model","provider":"firstParty"}}}`)
	m, err := ParseRecordedClaudeMetering(raw, "fixture-a")
	if err != nil || m.Source != apiusage.RecordedCLI || m.Disagreement || m.ServiceTier != "standard" || m.Counts.CacheWrite == nil || *m.Counts.CacheWrite != 0 {
		t.Fatalf("metering missing explicit fields: %+v %v", m, err)
	}
	total, overflow := m.Counts.Total()
	if total == nil || *total != 25006 || overflow {
		t.Fatal("overlapping CLI summaries were added twice or lost")
	}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"0.57689", "costUSD", "canary", "forged-model", "firstParty"} {
		if strings.Contains(string(got), forbidden) {
			t.Fatal("executor cost, identity label, or raw content escaped the boundary")
		}
	}
	raw = []byte(strings.Replace(string(raw), `"inputTokens":2413`, `"inputTokens":2418`, 1))
	m, err = ParseRecordedClaudeMetering(raw, "fixture-a")
	if err != nil || !m.Disagreement {
		t.Fatal("competing summaries did not mark ambiguity")
	}
}

func TestAPI3FixtureUnknownAndInvalidMetering(t *testing.T) {
	raw := []byte(`{"type":"message","model":"fixture-a","content":[{"text":"raw answer canary"}],
	  "usage":{"input_tokens":0,"cache_read_input_tokens":null,"output_tokens":2,"output_tokens_details":{"reasoning_tokens":1}}}`)
	m, err := ParseSyntheticMessagesUsage(raw, "fixture-a")
	if err != nil || m.Counts.Input == nil || *m.Counts.Input != 0 || m.Counts.CacheRead != nil ||
		m.Counts.Reasoning == nil || *m.Counts.Reasoning != 1 {
		t.Fatalf("unknown and zero not preserved: %+v %v", m, err)
	}
	if total, _ := m.Counts.Total(); total != nil {
		t.Fatal("partial token total became known")
	}
	if _, err := ParseSyntheticMessagesUsage(raw, "wrong-model"); err == nil {
		t.Fatal("model mismatch accepted")
	}
	for _, raw := range []string{
		`{"type":"message","model":"fixture-a","usage":{"input_tokens":-1}}`,
		`{"type":"message","model":"fixture-a","usage":{"input_tokens":1.0}}`,
		`{"type":"message","model":"fixture-a","usage":{"input_tokens":1e3}}`,
		`{"type":"message","model":"fixture-a","usage":{"input_tokens":"1"}}`,
		`{"type":"message","model":"fixture-a","usage":{"input_tokens":9223372036854775808}}`,
		`{"type":"message","model":"fixture-a","usage":{"input_tokens":9223372036854775807,"output_tokens":2}}`,
		`{"type":"message","model":"fixture-a","usage":{"input_tokens":1,"input_tokens":0}}`,
		`{"type":"message","model":"fixture-a","usage":{"input_tokens":1,"total_tokens":0}}`,
		`{"type":"message","model":"fixture-a","usage":{"cache_creation_input_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":1}}}`,
		`{"type":"message","model":"fixture-a","usage":{"output_tokens":1,"reasoning_tokens":2}}`,
		`{"type":"message","model":"fixture-a","usage":{}}{}`,
	} {
		if _, err := ParseSyntheticMessagesUsage([]byte(raw), "fixture-a"); err == nil {
			t.Fatal("accepted malformed/unsafe measurement")
		}
	}
	ambiguous := []byte(`{"type":"result","usage":{"input_tokens":4},"modelUsage":{"fixture-a":{"inputTokens":3},"fixture-b":{"inputTokens":1}}}`)
	if _, err := ParseRecordedClaudeMetering(ambiguous, "fixture-a"); err == nil {
		t.Fatal("multi-model CLI total was misattributed")
	}
}

func TestAPI3RecordedCLIChoosesOneSummary(t *testing.T) {
	raw := []byte(`{"type":"result","usage":{"input_tokens":2},"modelUsage":{"fixture-a":{"inputTokens":2,"outputTokens":3,"costUSD":17}}}`)
	m, err := ParseRecordedClaudeMetering(raw, "fixture-a")
	if err != nil || m.Counts.Output != nil || m.Counts.Input == nil || *m.Counts.Input != 2 {
		t.Fatalf("secondary summary improperly filled primary holes: %+v %v", m, err)
	}
	raw = []byte(`{"type":"result","modelUsage":{"fixture-a":{"inputTokens":0,"cacheReadInputTokens":2,"costUSD":17}}}`)
	m, err = ParseRecordedClaudeMetering(raw, "fixture-a")
	if err != nil || m.Source != apiusage.RecordedCLI || m.Counts.Input == nil || *m.Counts.Input != 0 ||
		m.Counts.CacheRead == nil || *m.Counts.CacheRead != 2 {
		t.Fatalf("single CLI fallback was not represented: %+v %v", m, err)
	}
}

func TestAPI3PartialMeteringContributesOnlyDisjointKnownLowerBound(t *testing.T) {
	parse := func(inner string) (apiusage.Measurement, error) {
		return ParseSyntheticMessagesUsage([]byte(`{"type":"message","model":"fixture-model","usage":`+inner+`}`), "fixture-model")
	}
	m, err := parse(`{"input_tokens":2,"output_tokens":null,"output_tokens_details":{"reasoning_tokens":3},
		"cache_read_input_tokens":0,"cache_creation_input_tokens":null,
		"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":5},"total_tokens":14}`)
	if err != nil {
		t.Fatal(err)
	}
	v := (apiusage.Evidence{Metering: m}).View()
	if v.Total != nil || v.LowerBound == nil || *v.LowerBound != 14 || m.Counts.Output != nil || m.Counts.CacheWrite != nil {
		t.Fatalf("partial parents were synthesized or disjoint known fields omitted: %+v", v)
	}
	for _, bad := range []string{
		`{"reasoning_tokens":7,"total_tokens":6}`,
		`{"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":5},"total_tokens":8}`,
		`{"reasoning_tokens":9223372036854775807,"cache_creation":{"ephemeral_5m_input_tokens":1}}`,
		`{"cache_creation":{"ephemeral_5m_input_tokens":9223372036854775807,"ephemeral_1h_input_tokens":1}}`,
	} {
		if _, err := parse(bad); err == nil {
			t.Errorf("partial lower bound failed to reject %s", bad)
		}
	}
	m, err = parse(`{"output_tokens":8,"reasoning_tokens":3,"cache_creation_input_tokens":9,
		"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":5},"total_tokens":17}`)
	v = (apiusage.Evidence{Metering: m}).View()
	if err != nil || v.Total != nil || v.LowerBound == nil || *v.LowerBound != 17 {
		t.Fatalf("known parent buckets double-counted their subsets: %+v %v", v, err)
	}
	m, err = parse(`{"total_tokens":0,"server_tool_use":{"web_search_requests":0}}`)
	v = (apiusage.Evidence{Metering: m}).View()
	if err != nil || v.Total != nil || v.LowerBound != nil {
		t.Fatalf("separate reported total or request count fabricated a known token bucket: %+v %v", v, err)
	}
}
