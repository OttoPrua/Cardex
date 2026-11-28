package runner

import (
	"bytes"
	"encoding/json"
)

const comparisonLocalMessageID = "msg_cardex_comparison"

// childReply rebuilds a minimal allowlisted compatibility envelope. It does
// not forward provider ids, diagnostics, metadata, usage, stop reasons or
// arbitrary fields. Text and model identity are the only provider values.
func (c *compatibleComparison) childReply(stream bool) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.evidence.Status != "verified" || c.answer == "" {
		return nil
	}
	type textBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		ID      string      `json:"id"`
		Type    string      `json:"type"`
		Role    string      `json:"role"`
		Model   string      `json:"model"`
		Content []textBlock `json:"content"`
	}
	msg := message{comparisonLocalMessageID, "message", "assistant", c.evidence.Returned, []textBlock{}}
	if !stream {
		msg.Content = append(msg.Content, textBlock{"text", c.answer})
		data, _ := json.Marshal(msg)
		return data
	}
	var wire bytes.Buffer
	frame := func(event string, fields map[string]any) {
		if fields == nil {
			fields = make(map[string]any)
		}
		fields["type"] = event
		raw, _ := json.Marshal(fields)
		wire.WriteString("event: " + event + "\ndata: ")
		wire.Write(raw)
		wire.WriteString("\n\n")
	}
	frame("message_start", map[string]any{"message": msg})
	frame("content_block_start", map[string]any{"index": 0, "content_block": textBlock{"text", ""}})
	frame("content_block_delta", map[string]any{"index": 0, "delta": textBlock{"text_delta", c.answer}})
	frame("content_block_stop", map[string]any{"index": 0})
	frame("message_stop", nil)
	return wire.Bytes()
}
