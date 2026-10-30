package runner

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"

	"cardex/internal/model"
)

var fakeUsagePattern = regexp.MustCompile(`tokens=(\d+)`)

// ParseOutputUsage reads claude, codex, and opencode usage documents.
// A tokens=N marker is the fake runner's own signal and is ignored here.
func ParseOutputUsage(output []byte) model.Usage {
	var (
		best   model.Usage
		claude int64
		codex  int64
		open   int64
		sawC   bool
		sawX   bool
		sawO   bool
	)
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		if n, ok := claudeUsage(obj); ok {
			claude += n
			sawC = true
		}
		if n, ok := codexUsage(obj); ok {
			codex += n
			sawX = true
		}
		if n, ok := openCodeUsage(obj); ok {
			open += n
			sawO = true
		}
	}
	// A single JSON document may be the whole output, not one line.
	if !sawC && !sawX && !sawO {
		var obj map[string]json.RawMessage
		if json.Unmarshal(bytes.TrimSpace(output), &obj) == nil {
			if n, ok := claudeUsage(obj); ok {
				claude, sawC = n, true
			}
			if n, ok := codexUsage(obj); ok {
				codex, sawX = n, true
			}
			if n, ok := openCodeUsage(obj); ok {
				open, sawO = n, true
			}
		}
	}
	switch {
	case sawC:
		best = model.Usage{Tokens: claude, Source: "claude_result", Confidence: 1}
	case sawO:
		best = model.Usage{Tokens: open, Source: "opencode_step_finish", Confidence: 1}
	case sawX:
		best = model.Usage{Tokens: codex, Source: "codex", Confidence: 1}
	default:
		return model.Usage{}
	}
	return best
}

func parseFakeTokens(output []byte) (model.Usage, bool) {
	m := fakeUsagePattern.FindSubmatch(output)
	if m == nil {
		return model.Usage{}, false
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return model.Usage{}, false
	}
	return model.Usage{Tokens: n, Source: "fake", Confidence: 1}, true
}

func claudeUsage(obj map[string]json.RawMessage) (int64, bool) {
	raw, ok := obj["usage"]
	if !ok {
		return 0, false
	}
	kind := jsonString(obj["type"])
	if kind != "" && kind != "result" {
		return 0, false
	}
	n, ok := sumTokenFields(raw, map[string]bool{
		"input_tokens": true, "output_tokens": true,
		"cache_read_input_tokens": true, "cache_creation_input_tokens": true,
		"reasoning_tokens": true,
	}, "total_tokens")
	return n, ok
}

func codexUsage(obj map[string]json.RawMessage) (int64, bool) {
	raw, ok := obj["usage"]
	if !ok {
		return 0, false
	}
	kind := jsonString(obj["type"])
	if kind != "" && kind != "turn.completed" && kind != "result" {
		return 0, false
	}
	if kind == "result" {
		return 0, false
	}
	return sumTokenFields(raw, map[string]bool{
		"input_tokens": true, "output_tokens": true,
		"cache_read_input_tokens": true, "cache_creation_input_tokens": true,
		"reasoning_tokens": true,
	}, "total_tokens")
}

func openCodeUsage(obj map[string]json.RawMessage) (int64, bool) {
	if jsonString(obj["type"]) != "step_finish" {
		return 0, false
	}
	partRaw, ok := obj["part"]
	if !ok {
		return 0, false
	}
	var part map[string]json.RawMessage
	if json.Unmarshal(partRaw, &part) != nil {
		return 0, false
	}
	raw, ok := part["tokens"]
	if !ok {
		return 0, false
	}
	return sumTokenFields(raw, map[string]bool{
		"input": true, "output": true, "reasoning": true, "cache": true,
	}, "total")
}

func sumTokenFields(raw json.RawMessage, fields map[string]bool, totalKey string) (int64, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return 0, false
	}
	if totalRaw, ok := obj[totalKey]; ok {
		if n, ok := jsonInt(totalRaw); ok {
			return n, true
		}
	}
	var sum int64
	var saw bool
	for key := range fields {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		n, ok := jsonInt(raw)
		if !ok {
			continue
		}
		sum += n
		saw = true
	}
	return sum, saw
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func jsonInt(raw json.RawMessage) (int64, bool) {
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n, true
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int64(f), true
	}
	return 0, false
}
