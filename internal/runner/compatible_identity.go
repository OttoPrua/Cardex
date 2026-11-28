package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/OttoPrua/Cardex/internal/model"
)

const maxComparisonResponse = 8 << 20

// model evidence never comes from argv, CLI summaries or answer text.
func providerIdentityFields(raw []byte) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, err := dec.Token()
	if err != nil || open != json.Delim('{') {
		return nil, false
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		token, err := dec.Token()
		name, stringKey := token.(string)
		if err != nil || !stringKey {
			return nil, false
		}
		if _, duplicate := out[name]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		out[name] = value
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return out, true
}

func providerIdentityString(raw []byte) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func safeReportedModel(fields map[string]json.RawMessage, secret string) string {
	reported := providerIdentityString(fields["model"])
	if providerIdentityString(fields["type"]) != "message" || validModelID(reported) != nil ||
		strings.EqualFold(reported, model.UnknownProviderModel) ||
		(secret != "" && strings.Contains(reported, secret)) {
		return ""
	}
	return reported
}

func parseProviderModel(raw []byte, stream bool, secret string) string {
	if stream {
		reported, _ := parseStrictComparisonResponse(raw, true, secret)
		return reported
	}
	fields, ok := providerIdentityFields(raw)
	if !ok {
		return ""
	}
	return safeReportedModel(fields, secret)
}

type compatibleComparison struct {
	mu         sync.Mutex
	expected   string
	prompt     string
	answer     string
	ready      func(context.Context, bool) bool
	generation bool
	evidence   model.ProviderModelEvidence
}

func (c *compatibleComparison) setup(expected, prompt string, ready ...func(context.Context, bool) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expected, c.prompt = expected, prompt
	if len(ready) != 0 {
		c.ready = ready[0]
	}
	c.evidence = model.ProviderModelEvidence{
		Requested: expected, Returned: model.UnknownProviderModel,
		Source: model.ModelSourceUnknown, Status: "request_missing",
	}
}

func (c *compatibleComparison) snapshot() model.ProviderModelEvidence {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evidence
}

func (c *compatibleComparison) authorizeRequest(ctx context.Context, raw []byte, generation bool) (strict bool, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expected == "" {
		return false, raw
	}
	if !generation {
		// Never forward unconstrained count_tokens input; it can contain
		// arbitrary prompt, tools or data not covered by this approval.
		return true, nil
	}
	if c.evidence.Status != "request_missing" && c.evidence.Status != "pending" && c.evidence.Status != "verified" {
		return true, nil
	}
	fields, ok := providerIdentityFields(raw)
	if !ok || providerIdentityString(fields["model"]) != c.expected {
		c.evidence.Status = "request_model_mismatch"
		return true, nil
	}
	if !noComparisonTools(fields) {
		c.evidence.Status = "request_tools_refused"
		return true, nil
	}
	if c.generation {
		c.evidence.Status = "request_repeated"
		return true, nil
	}
	if c.prompt == "" || (c.ready != nil && !c.ready(ctx, true)) {
		c.evidence.Status = "target_unavailable"
		return true, nil
	}
	var stream bool
	_ = json.Unmarshal(fields["stream"], &stream)
	// Build the exact approved wire question. Do not forward arbitrary
	// Claude/system/session metadata, worktree content, or tools.
	body, _ = json.Marshal(comparisonWireRequest{
		Model: c.expected, MaxTokens: 512, Stream: stream,
		Messages: []comparisonWireMessage{{Role: "user", Content: c.prompt}},
	})
	c.generation = true
	c.evidence.Status = "pending"
	return true, body
}

type comparisonWireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type comparisonWireRequest struct {
	Model     string                  `json:"model"`
	MaxTokens int                     `json:"max_tokens"`
	Stream    bool                    `json:"stream"`
	Messages  []comparisonWireMessage `json:"messages"`
}

func (c *compatibleComparison) verifiedAnswer() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.evidence.Status != "verified" {
		return ""
	}
	return c.answer
}

func noComparisonTools(fields map[string]json.RawMessage) bool {
	for _, key := range []string{"tools", "mcp_servers"} {
		if raw, ok := fields[key]; ok {
			var list []json.RawMessage
			if json.Unmarshal(raw, &list) != nil || len(list) != 0 {
				return false
			}
		}
	}
	// A tool-free comparison must not opt into a provider execution
	// container or reuse a container from another conversation.
	if raw, ok := fields["container"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	return true
}

func (c *compatibleComparison) observe(raw []byte, stream bool, secret string) bool {
	reported, answer := parseStrictComparisonResponse(raw, stream, secret)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.evidence.Status != "pending" {
		return false
	}
	if reported == "" {
		c.evidence.Status = "identity_unknown"
		return false
	}
	c.evidence.Returned, c.evidence.Source = reported, model.ModelSourceProvider
	if reported != c.expected {
		c.evidence.Status = "identity_mismatch"
		return false
	}
	if answer == "" {
		c.evidence.Status = "answer_invalid"
		return false
	}
	c.answer = answer
	c.evidence.Status = "verified"
	return true
}
