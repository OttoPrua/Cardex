package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

func parseProviderComparisonAnswer(raw []byte, stream bool, secret string) string {
	_, answer := parseStrictComparisonResponse(raw, stream, secret)
	return answer
}

// parseStrictComparisonResponse accepts exactly one complete Messages
// response. In SSE, text is valid only in an ordered text block inside the
// same start/stop span that supplied the authenticated model identity.
func parseStrictComparisonResponse(raw []byte, stream bool, secret string) (reported, answer string) {
	if len(raw) > maxComparisonResponse {
		return "", ""
	}
	var out strings.Builder
	addText := func(raw json.RawMessage) bool {
		var text string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &text) != nil ||
			len(text) > model.ComparisonMaxAnswer-out.Len() {
			return false
		}
		out.WriteString(text)
		return true
	}
	if !stream {
		fields, ok := providerIdentityFields(raw)
		if !ok {
			return "", ""
		}
		reported = safeReportedModel(fields, secret)
		if reported == "" {
			return "", ""
		}
		var blocks []json.RawMessage
		if json.Unmarshal(fields["content"], &blocks) != nil {
			return reported, ""
		}
		for _, raw := range blocks {
			block, ok := providerIdentityFields(raw)
			if !ok || providerIdentityString(block["type"]) != "text" || !addText(block["text"]) {
				return reported, ""
			}
		}
	} else {
		lines := bufio.NewScanner(bytes.NewReader(raw))
		lines.Buffer(make([]byte, 4096), 1<<20)
		declared := ""
		hadData, closed, openBlock := false, false, false
		blockIndex := 0
		seenMessageDelta := false
		for lines.Scan() {
			line := lines.Bytes()
			if len(line) == 0 {
				if declared != "" && !hadData {
					return "", ""
				}
				declared, hadData = "", false
				continue
			}
			if line[0] == ':' {
				continue
			}
			if name, yes := bytes.CutPrefix(line, []byte("event:")); yes {
				if declared != "" || hadData {
					return "", ""
				}
				declared = strings.TrimSpace(string(name))
				if declared == "" {
					return "", ""
				}
				continue
			}
			data, isData := bytes.CutPrefix(line, []byte("data:"))
			if !isData || hadData {
				// The supported contract is one JSON object per SSE event.
				return "", ""
			}
			hadData = true
			data = bytes.TrimSpace(data)
			if bytes.Equal(data, []byte("[DONE]")) && closed && declared == "" {
				continue
			}
			fields, ok := providerIdentityFields(data)
			if !ok {
				return "", ""
			}
			kind := providerIdentityString(fields["type"])
			if declared != "" && declared != kind {
				return "", ""
			}
			indexMatches := func() bool {
				var index int
				raw := bytes.TrimSpace(fields["index"])
				return len(raw) != 0 && raw[0] >= '0' && raw[0] <= '9' && json.Unmarshal(raw, &index) == nil && index == blockIndex
			}
			switch kind {
			case "ping":
				continue
			case "message_start":
				if reported != "" || closed {
					return "", ""
				}
				message, ok := providerIdentityFields(fields["message"])
				if !ok {
					return "", ""
				}
				reported = safeReportedModel(message, secret)
				if reported == "" {
					return "", ""
				}
				if raw, present := message["content"]; present {
					var content []json.RawMessage
					if json.Unmarshal(raw, &content) != nil || len(content) != 0 {
						return "", ""
					}
				}
			case "content_block_start":
				block, ok := providerIdentityFields(fields["content_block"])
				if reported == "" || closed || openBlock || seenMessageDelta || !indexMatches() ||
					!ok || providerIdentityString(block["type"]) != "text" || !addText(block["text"]) {
					return "", ""
				}
				openBlock = true
			case "content_block_delta":
				delta, ok := providerIdentityFields(fields["delta"])
				if !openBlock || closed || !indexMatches() ||
					!ok || providerIdentityString(delta["type"]) != "text_delta" || !addText(delta["text"]) {
					return "", ""
				}
			case "content_block_stop":
				if !openBlock || closed || !indexMatches() {
					return "", ""
				}
				openBlock = false
				blockIndex++
			case "message_delta":
				if reported == "" || closed || openBlock || blockIndex == 0 {
					return "", ""
				}
				seenMessageDelta = true
			case "message_stop":
				if reported == "" || closed || openBlock || blockIndex == 0 {
					return "", ""
				}
				closed = true
			default:
				return "", ""
			}
		}
		if lines.Err() != nil || !closed || (declared != "" && !hadData) {
			return "", ""
		}
	}
	answer = strings.TrimSpace(out.String())
	if strings.ContainsRune(answer, '\x00') || (secret != "" && strings.Contains(answer, secret)) {
		return reported, ""
	}
	return reported, answer
}
