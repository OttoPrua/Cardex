package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"cardex/internal/model"
)

// AGY adapts the Antigravity CLI. v1's command name is agy.
// Write-capable runs use --mode accept-edits, not --dangerously-skip-permissions.
// Empty config effort is the v1 default "high"; a thinking model omits --effort.
type AGY struct{}

var _ Runner = AGY{}

func (AGY) Name() string                               { return string(model.RunnerAGY) }
func (AGY) Capabilities() []string                     { return nil }
func (AGY) NeedsPTY() bool                             { return false }
func (AGY) WritablePaths() []string                    { return nil }
func (AGY) Drive(context.Context, io.ReadWriter) error { return nil }

func (AGY) Build(spec LaunchSpec) ([]string, []string, error) {
	mode := "accept-edits"
	args := []string{"agy", "--output-format", "json", "--mode", mode, "--model", spec.Model}
	if !strings.Contains(strings.ToLower(spec.Model), "thinking") {
		args = append(args, "--effort", "high")
	}
	if mode != "plan" {
		args = append(args, "--disable-slash-commands")
	}
	args = append(args, "--print", spec.PromptPath)
	return args, []string{"NO_COLOR=1"}, nil
}

func (AGY) Classify(_ int, output []byte) model.ErrorClass {
	cls := classifyScanned(output, nil, func(text string) model.ErrorClass {
		if limitRe.MatchString(text) || strings.Contains(strings.ToLower(text), "quota exceeded") || strings.Contains(strings.ToLower(text), "quota exhausted") {
			return model.ErrorQuotaHit
		}
		return ""
	})
	if cls != "" {
		return cls
	}
	if agyNeedsInteraction(output) || strings.Contains(string(output), "Antigravity 需要交互：动作被拒绝且没有可验收产物") {
		return model.ErrorPermissionDenied
	}
	trim := bytes.TrimSpace(output)
	if len(trim) > 0 && trim[0] == '{' && !json.Valid(trim) && singleJSONDocument(trim) {
		return model.ErrorResultUnparsable
	}
	return ""
}

func singleJSONDocument(trim []byte) bool {
	lines := bytes.Split(trim, []byte("\n"))
	n := 0
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) > 0 {
			n++
		}
	}
	return n <= 1
}

func (AGY) ParseUsage(output []byte) model.Usage {
	return parseUsageAliases(output, usageSourceAntigravity, agyUsageAliases)
}

func agyNeedsInteraction(output []byte) bool {
	trim := bytes.TrimSpace(output)
	if len(trim) == 0 || trim[0] != '{' {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(trim))
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return false
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return false
	}
	if strings.ToUpper(strings.TrimSpace(jsonString(obj["status"]))) != "SUCCESS" {
		return false
	}
	if strings.TrimSpace(agyBody(obj)) != "" {
		return false
	}
	if raw, ok := obj["error"]; ok {
		s := strings.TrimSpace(string(raw))
		if s != "" && s != "null" {
			return false
		}
	}
	return agyDeniedCount(obj["denied_actions"]) > 0
}

func agyBody(obj map[string]json.RawMessage) string {
	for _, key := range []string{"result", "response", "text"} {
		if s := jsonString(obj[key]); strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func agyDeniedCount(raw json.RawMessage) int {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return 0
	}
	var rows []struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return 0
	}
	return len(rows)
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
