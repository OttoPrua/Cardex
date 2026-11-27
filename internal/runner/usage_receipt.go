package runner

import (
	"bufio"
	"bytes"
	"encoding/json"

	"github.com/OttoPrua/Cardex/internal/model"
)

// UsageReceipt is numerical evidence only. nil is unknown, not zero.
type UsageReceipt struct {
	Source        string `json:"source,omitempty"`
	Total         *int64 `json:"total"`
	Input         *int64 `json:"input"`
	Output        *int64 `json:"output"`
	CacheRead     *int64 `json:"cache_read"`
	CacheWrite    *int64 `json:"cache_write"`
	Reasoning     *int64 `json:"reasoning"`
	ProviderTurns *int64 `json:"provider_turns"`
	ToolCalls     *int64 `json:"tool_calls"`
	Terminal      bool   `json:"terminal"`
	Disagreement  bool   `json:"disagreement"`
}

// DescribeUsage exposes only known provider fields. A provider turn count is
// not evidence that a tool ran; tool calls stay unknown until a trusted
// structured receipt can distinguish them from authored output.
func DescribeUsage(r Runner, output []byte, parsed model.Usage) UsageReceipt {
	u := UsageReceipt{Source: parsed.Source}
	if parsed.Source != "" && parsed.Confidence > 0 && parsed.Tokens >= 0 {
		u.Total = &parsed.Tokens
	}
	if _, grok := r.(Grok); !grok || u.Total == nil {
		return u
	}
	var candidate map[string]json.RawMessage
	inspect := func(line []byte) {
		var doc map[string]json.RawMessage
		if json.Unmarshal(line, &doc) != nil {
			return
		}
		var kind string
		if json.Unmarshal(doc["type"], &kind) != nil || kind != "end" {
			return
		}
		if _, ok := usageTokens(string(bytes.TrimSpace(line)), claudeUsageAliases); ok {
			candidate = doc
		}
	}
	if completeJSONObject(output) {
		inspect(output)
	} else {
		sc := bufio.NewScanner(bytes.NewReader(output))
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			inspect(sc.Bytes())
		}
		if sc.Err() != nil {
			return u
		}
	}
	if candidate == nil {
		return u
	}
	u.Terminal = true
	n, ok := tokensFromUsage(candidate["usage"], claudeUsageAliases)
	if !ok || n != *u.Total {
		u.Disagreement = true
		return u
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(candidate["usage"], &fields) != nil {
		return u
	}
	exact := func(raw json.RawMessage) *int64 {
		if n, ok := nonnegJSONInt(raw); ok {
			return &n
		}
		return nil
	}
	u.Input = exact(fields["input_tokens"])
	u.Output = exact(fields["output_tokens"])
	u.CacheRead = exact(fields["cache_read_input_tokens"])
	u.CacheWrite = exact(fields["cache_creation_input_tokens"])
	u.Reasoning = exact(fields["reasoning_tokens"])
	u.ProviderTurns = exact(candidate["num_turns"])
	return u
}
