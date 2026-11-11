package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

// AGY adapts the Antigravity CLI. v1's command name is agy.
// Write-capable runs use --mode accept-edits plus --dangerously-skip-permissions.
// accept-edits without that flag is the interactive shape v1 marks as needing a person.
// Empty config effort is the v1 default "high". A thinking model, or a model
// that already ends in -low, -medium, or -high, omits --effort.
type AGY struct{}

var _ Runner = AGY{}

func (AGY) Name() string           { return string(model.RunnerAGY) }
func (AGY) Capabilities() []string { return nil }
func (AGY) NeedsPTY() bool         { return false }
func (AGY) WritablePaths() []string {
	return appendEnvDirs(
		nonempty(homePath(".gemini"), cachePath("gemini")),
		envDir("GEMINI_CLI_HOME"),
	)
}
func (AGY) Drive(context.Context, io.ReadWriter) error { return nil }

func (AGY) Build(spec LaunchSpec) ([]string, []string, error) {
	body, err := promptArg(spec.PromptPath)
	if err != nil {
		return nil, nil, err
	}
	mode := "accept-edits"
	args := []string{"agy", "--output-format", "json", "--mode", mode, "--model", spec.Model}
	if !agyOmitsEffort(spec.Model) {
		args = append(args, "--effort", "high")
	}
	if mode != "plan" {
		args = append(args, "--disable-slash-commands")
	}
	args = append(args, "--dangerously-skip-permissions", "--print", body)
	return args, []string{"NO_COLOR=1"}, nil
}

func (AGY) Classify(_ int, output []byte) model.ErrorClass {
	cls := classifyScanned(output, "", nil, func(text string) model.ErrorClass {
		if limitRe.MatchString(text) || strings.Contains(strings.ToLower(text), "quota exceeded") || strings.Contains(strings.ToLower(text), "quota exhausted") {
			return model.ErrorQuotaHit
		}
		return ""
	})
	if cls != "" {
		return cls
	}
	if agyNeedsInteraction(output) || agyPermissionPhrase(output) {
		return model.ErrorPermissionDenied
	}
	trim := bytes.TrimSpace(output)
	if len(trim) > 0 && trim[0] == '{' && !json.Valid(trim) && singleJSONDocument(trim) {
		return model.ErrorResultUnparsable
	}
	return ""
}

// agyOmitsEffort skips the default --effort high when the model already names
// a tier or is a thinking model.
func agyOmitsEffort(model string) bool {
	m := strings.ToLower(model)
	if strings.Contains(m, "thinking") {
		return true
	}
	for _, suffix := range []string{"-low", "-medium", "-high"} {
		if strings.HasSuffix(m, suffix) {
			return true
		}
	}
	return false
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

func agyPermissionPhrase(output []byte) bool {
	const phrase = "Antigravity 需要交互：动作被拒绝且没有可验收产物"
	found := false
	walkNonEchoLines(output, "", func(line string) {
		if line == phrase {
			found = true
		}
	})
	return found
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
