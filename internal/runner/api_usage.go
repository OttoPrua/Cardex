package runner

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/OttoPrua/Cardex/internal/apiusage"
)

// ParseSyntheticMessagesUsage accepts one explicitly synthetic response,
// including its exact fixture model. It never runs a request or retains an
// answer, headers, arbitrary labels, or a monetary estimate from the input.
func ParseSyntheticMessagesUsage(raw []byte, expectedModel string) (apiusage.Measurement, error) {
	doc, ok := meteringDocument(raw, expectedModel, "message")
	if !ok || providerIdentityString(doc["model"]) != expectedModel {
		return apiusage.Measurement{}, apiusage.ErrEvidence
	}
	m, ok := readUsageNumbers(doc["usage"], false)
	if !ok {
		return apiusage.Measurement{}, apiusage.ErrEvidence
	}
	m.Source = apiusage.FixtureResponse
	return m, nil
}

// ParseRecordedClaudeMetering reads one archived CLI terminal JSON object.
// Its modelUsage labels are not provider identity or billing attestation.
// Prefer the single top-level aggregate; never sum it with modelUsage.
func ParseRecordedClaudeMetering(raw []byte, expectedModel string) (apiusage.Measurement, error) {
	doc, ok := meteringDocument(raw, expectedModel, "result")
	if !ok {
		return apiusage.Measurement{}, apiusage.ErrEvidence
	}
	m, ok := readUsageNumbers(doc["usage"], false)
	if !ok {
		return apiusage.Measurement{}, apiusage.ErrEvidence
	}
	if !nullMeter(doc["modelUsage"]) {
		children, ok := providerIdentityFields(doc["modelUsage"])
		if !ok || len(children) != 1 || children[expectedModel] == nil || nullMeter(children[expectedModel]) {
			return apiusage.Measurement{}, apiusage.ErrEvidence
		}
		other, ok := readUsageNumbers(children[expectedModel], true)
		if !ok {
			return apiusage.Measurement{}, apiusage.ErrEvidence
		}
		if nullMeter(doc["usage"]) {
			m = other
		} else if meterDisagrees(m.Counts, other.Counts) {
			// Keep the one reported aggregate, explicitly marked disputed.
			m.Disagreement = true
		}
	}
	m.Source = apiusage.RecordedCLI
	return m, nil
}

func meteringDocument(raw []byte, expectedModel, kind string) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || len(raw) > apiusage.MaxEvidenceBytes || !apiusage.ValidModel(expectedModel) {
		return nil, false
	}
	doc, ok := providerIdentityFields(raw)
	return doc, ok && providerIdentityString(doc["type"]) == kind
}

func nullMeter(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// exactMeterNumber deliberately does not use legacy nonnegJSONInt: that
// helper accepts an integral float converted through float64 for legacy quota.
func exactMeterNumber(raw json.RawMessage) (*int64, bool) {
	if nullMeter(raw) {
		return nil, true
	}
	d := bytes.TrimSpace(raw)
	for _, b := range d {
		if b < '0' || b > '9' {
			return nil, false
		}
	}
	n, err := strconv.ParseInt(string(d), 10, 64)
	if err != nil {
		return nil, false
	}
	return &n, true
}

func readUsageNumbers(raw json.RawMessage, camel bool) (apiusage.Measurement, bool) {
	var m apiusage.Measurement
	if nullMeter(raw) {
		return m, true
	}
	u, ok := providerIdentityFields(raw)
	if !ok {
		return m, false
	}
	targets := []struct {
		snake, camel string
		value        **int64
	}{
		{"input_tokens", "inputTokens", &m.Counts.Input},
		{"output_tokens", "outputTokens", &m.Counts.Output},
		{"cache_read_input_tokens", "cacheReadInputTokens", &m.Counts.CacheRead},
		{"cache_creation_input_tokens", "cacheCreationInputTokens", &m.Counts.CacheWrite},
		{"reasoning_tokens", "reasoningTokens", &m.Counts.Reasoning},
		{"total_tokens", "totalTokens", &m.Counts.ReportedTotal},
	}
	for _, field := range targets {
		key := field.snake
		if camel {
			key = field.camel
		}
		if *field.value, ok = exactMeterNumber(u[key]); !ok {
			return m, false
		}
	}
	if camel {
		// Ignore costUSD and provider/canonicalModel labels even if the
		// original CLI document claims a dollar value or an identity.
		m.Counts.WebSearch, ok = exactMeterNumber(u["webSearchRequests"])
		if !ok {
			return m, false
		}
		m.Counts.WebFetch, ok = exactMeterNumber(u["webFetchRequests"])
		if !ok {
			return m, false
		}
		return m, m.Counts.Validate() == nil
	}
	if !readMeteringSubfields(u["cache_creation"], map[string]**int64{
		"ephemeral_5m_input_tokens": &m.Counts.CacheWrite5m,
		"ephemeral_1h_input_tokens": &m.Counts.CacheWrite1h,
	}) || !readMeteringSubfields(u["server_tool_use"], map[string]**int64{
		"web_search_requests": &m.Counts.WebSearch,
		"web_fetch_requests":  &m.Counts.WebFetch,
	}) {
		return m, false
	}
	var nestedReasoning *int64
	if !readMeteringSubfields(u["output_tokens_details"], map[string]**int64{"reasoning_tokens": &nestedReasoning}) {
		return m, false
	}
	if m.Counts.Reasoning == nil {
		m.Counts.Reasoning = nestedReasoning
	} else if differentMeter(m.Counts.Reasoning, nestedReasoning) {
		return m, false
	}
	m.ServiceTier = apiusage.Category(providerIdentityString(u["service_tier"]))
	m.Speed = apiusage.Category(providerIdentityString(u["speed"]))
	return m, m.Counts.Validate() == nil
}

func readMeteringSubfields(raw json.RawMessage, targets map[string]**int64) bool {
	if nullMeter(raw) {
		return true
	}
	fields, ok := providerIdentityFields(raw)
	if !ok {
		return false
	}
	for k, out := range targets {
		if *out, ok = exactMeterNumber(fields[k]); !ok {
			return false
		}
	}
	return true
}

func differentMeter(a, b *int64) bool { return a != nil && b != nil && *a != *b }

func meterDisagrees(a, b apiusage.Counts) bool {
	return differentMeter(a.Input, b.Input) || differentMeter(a.Output, b.Output) ||
		differentMeter(a.CacheRead, b.CacheRead) || differentMeter(a.CacheWrite, b.CacheWrite) ||
		differentMeter(a.Reasoning, b.Reasoning) || differentMeter(a.WebSearch, b.WebSearch) ||
		differentMeter(a.WebFetch, b.WebFetch) || differentMeter(a.ReportedTotal, b.ReportedTotal)
}
