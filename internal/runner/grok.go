package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cardex/internal/model"
)

// Grok adapts the grok-build CLI. GoalMode selects the PTY /goal driver;
// LaunchSpec cannot carry requires, so the host sets this field.
// Prompt is the goal text Drive writes after the literal /goal.
type Grok struct {
	GoalMode bool
	Prompt   string
}

var _ Runner = Grok{}

// Drive terminals. Completion is a nil error. budget_limited is not completion.
var (
	ErrGrokBudgetLimited = errors.New("grok goal budget_limited")
	ErrGrokQuotaHit      = errors.New("grok goal quota_hit")
	ErrGrokCrash         = errors.New("grok goal crash")
	errGrokNoTerminal    = errors.New("grok goal: no terminal")
)

func (Grok) Name() string { return string(model.RunnerGrok) }

func (Grok) Capabilities() []string { return []string{"goal_mode"} }

func (g Grok) NeedsPTY() bool { return g.GoalMode }

func (Grok) WritablePaths() []string { return userPath(".grok") }

// Build returns v1's write-capable argv. Headless matches grokBuildProductArgs
// without --reasoning-effort (LaunchSpec has no effort). Goal mode matches
// manualGrokGoalArgs: interactive, --cwd, no --prompt-file.
func (g Grok) Build(spec LaunchSpec) ([]string, []string, error) {
	if g.GoalMode {
		return g.buildGoal(spec)
	}
	args := []string{
		"grok",
		"--no-auto-update",
		"--model", spec.Model,
		"--output-format", "streaming-json",
		"--sandbox", "workspace",
		"--permission-mode", "auto",
		"--no-memory", "--no-subagents", "--disable-web-search", "--verbatim",
		"--no-plan",
		"--prompt-file", spec.PromptPath,
	}
	if grokPrivilegeWidening(args) {
		return nil, nil, fmt.Errorf("grok: privilege-widening flags")
	}
	return args, nil, nil
}

func (g Grok) buildGoal(spec LaunchSpec) ([]string, []string, error) {
	args := []string{
		"grok",
		"--no-auto-update",
		"--model", spec.Model,
		"--sandbox", "workspace",
		"--permission-mode", "auto",
		"--no-memory", "--no-subagents", "--disable-web-search",
		"--no-plan",
	}
	if spec.WorkDir != "" {
		args = append(args, "--cwd", spec.WorkDir)
	}
	if grokPrivilegeWidening(args) {
		return nil, nil, fmt.Errorf("grok: privilege-widening flags")
	}
	env := []string{"GROK_WORKFLOWS=1", "GROK_DISABLE_AUTOUPDATER=1"}
	if paths := g.WritablePaths(); len(paths) > 0 {
		env = append([]string{"GROK_HOME=" + paths[0]}, env...)
	}
	return args, env, nil
}

func grokPrivilegeWidening(args []string) bool {
	for i, arg := range args {
		switch arg {
		case "--always-approve", "--yolo", "--skip-permissions", "--dangerously-skip-permissions":
			return true
		case "--permission-mode":
			if i+1 < len(args) {
				switch args[i+1] {
				case "dontAsk", "bypassPermissions", "yolo", "always-approve":
					return true
				}
			}
		}
	}
	return false
}

// Drive writes the literal /goal and the prompt, then reads rw until a
// terminal, EOF, or ctx cancel. The four terminals are distinct errors
// (completion is nil).
func (g Grok) Drive(ctx context.Context, rw io.ReadWriter) error {
	if !g.GoalMode {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	line := "/goal"
	if g.Prompt != "" {
		line = "/goal " + strings.TrimRight(g.Prompt, "\r\n")
	}
	if _, err := io.WriteString(rw, line+"\r"); err != nil {
		return err
	}
	buf, err := readGoalOutput(ctx, rw)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if term, ok := grokDriveTerminal(buf); ok {
		return term
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return errGrokNoTerminal
}

func readGoalOutput(ctx context.Context, r io.Reader) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return buf, err
		}
		type got struct {
			n   int
			err error
		}
		ch := make(chan got, 1)
		go func() {
			n, err := r.Read(tmp)
			ch <- got{n, err}
		}()
		select {
		case <-ctx.Done():
			return buf, ctx.Err()
		case res := <-ch:
			if res.n > 0 {
				buf = append(buf, tmp[:res.n]...)
				if _, ok := grokDriveTerminal(buf); ok {
					return buf, nil
				}
			}
			if res.err != nil {
				return buf, res.err
			}
		}
	}
}

func grokDriveTerminal(output []byte) (error, bool) {
	if grokBudgetLimited(output) {
		return ErrGrokBudgetLimited, true
	}
	text := errorScan(output)
	if grokQuotaRe.MatchString(text) {
		return ErrGrokQuotaHit, true
	}
	if crashSignalRe.MatchString(text) {
		return ErrGrokCrash, true
	}
	if grokAchieved(output) {
		return nil, true
	}
	return nil, false
}

func (Grok) Classify(_ int, output []byte) model.ErrorClass {
	if grokBudgetLimited(output) {
		return model.ErrorBudgetLimited
	}
	return classifyScanned(output, grokQuotaRe, func(text string) model.ErrorClass {
		if strings.Contains(text, acceptedBootstrapBeforeProviderRefusal) {
			return model.ErrorRefusal
		}
		return ""
	})
}

func (Grok) ParseUsage(output []byte) model.Usage {
	return parseUsageAliases(output, usageSourceGrokBuildEnd, claudeUsageAliases)
}

// Regexes copied from v1 cmd/cardex (failure_class.go, runner.go, engines.go,
// grok.go, cursor.go, kimi.go, routing_policy.go). Quota is matched before
// the generic failure regexes, matching v1 isLimitHit running before
// classifyFailure. "no such file or directory" is not a crash signal here:
// v1's P0 note is that the phrase appears in ordinary transcripts.
var (
	authClassRe       = regexp.MustCompile(`(?i)(?:\bhttp(?:/\d(?:\.\d)?)?\s+(?:status\s+)?401\b|\bhttp\s+status\s*[:=]\s*401\b|\bstatus(?:\s+code)?\s*[:=]\s*401\b|\b401\s*(?::|-)?\s*unauthorized\b|\bunauthorized\s*\(\s*401\s*\))|invalid api key|invalid or expired credentials|authentication failed|oauth token (?:has )?(?:expired|invalid|revoked)|please (?:re-?)?login|please log in again|reason\s*=\s*no auth context|请重新登录|token expired`)
	permissionClassRe = regexp.MustCompile(`(?i)403 forbidden|permission denied|access denied by (?:policy|admin|organization)|not authorized to (?:perform|access)|operation not permitted by (?:policy|admin)|blocked by (?:policy|admin|organization)`)
	timeoutClassRe    = regexp.MustCompile(`(?i)步骤超时[（(]|远程步骤超时[（(]|远端步骤超时[（(]|context deadline exceeded|deadline exceeded \(`)
	crashSignalRe     = regexp.MustCompile(`(?i)signal: killed|signal: aborted|signal: segmentation fault|signal: quit|signal: bus error|executable file not found|找不到可执行文件`)
	transportRe       = regexp.MustCompile(`(?i)connection (?:reset|refused|closed|error)|error sending request|i/o timeout|stream disconnect|no such host|network is unreachable|temporary failure in name resolution|disconnected before`)
	limitRe           = regexp.MustCompile(`(?i)usage limit|limit reached|out of extra usage|hit your (?:\w+ )?limit|limit will reset|out of usage credits|out of credits|/usage-credits|session limit`)
	engineQuotaRe     = regexp.MustCompile(`(?i)quota (?:exceeded|exhausted)|insufficient quota|exceeded your .{0,24}quota|额度不足|额度已用完|配额不足|配额已用尽|余额不足|欠费`)
	grokQuotaRe       = regexp.MustCompile(`(?i)(?:^|[^0-9])429(?:[^0-9]|$)|too many requests|rate limit|usage limit|quota (?:exceeded|exhausted)|insufficient (?:quota|credits)|out of (?:credits|usage)|额度(?:不足|已用完)|配额(?:不足|已用尽)|限额(?:不足|已用尽)`)
	cursorQuotaRe     = regexp.MustCompile(`(?i)(?:^|[^0-9])429(?:[^0-9]|$)|too many requests|rate limit|usage limit|quota (?:exceeded|exhausted)|insufficient (?:quota|credits)|out of (?:credits|usage)|额度(?:不足|已用完)|配额(?:不足|已用尽)|限额(?:不足|已用尽)`)
	cursorPolicyRe    = regexp.MustCompile(`(?i)actionrequired(?:error)?:.*review data policy|acknowledge .*data retention policy|review data policy`)
	kimiQuotaRe       = regexp.MustCompile(`(?i)(?:^|[^0-9])429(?:[^0-9]|$)|too many requests|rate limit|quota (?:exceeded|exhausted)|insufficient (?:quota|credits)|usage limit|额度(?:不足|已用完)|配额(?:不足|已用尽)|限额(?:不足|已用尽)`)
)

// acceptedBootstrapBeforeProviderRefusal is the exact PTY line v1 records.
const acceptedBootstrapBeforeProviderRefusal = "Grok hook write-deny ensure failed: cannot create managed_config.toml: Operation not permitted; named profile refused with protections missing"

const (
	usageSourceGrokBuildEnd = "grok_build_end"
	usageSourceCursorResult = "cursor_result"
	usageSourceAntigravity  = "antigravity_result"
)

func userPath(name string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	return []string{filepath.Join(home, name)}
}

func classifyScanned(output []byte, quota *regexp.Regexp, before func(string) model.ErrorClass) model.ErrorClass {
	text := errorScan(output)
	if before != nil {
		if cls := before(text); cls != "" {
			return cls
		}
	}
	switch {
	case quota != nil && quota.MatchString(text):
		return model.ErrorQuotaHit
	case authClassRe.MatchString(text):
		return model.ErrorAuthFailed
	case permissionClassRe.MatchString(text):
		return model.ErrorPermissionDenied
	case timeoutClassRe.MatchString(text):
		return model.ErrorTimeout
	case transportRe.MatchString(text):
		return model.ErrorTransport
	default:
		return ""
	}
}

// errorScan is v1's limit/error scan: JSON events contribute only error
// fields; assistant prose is ignored; non-JSON lines count in full.
func errorScan(output []byte) string {
	var parts []string
	sc := bufio.NewScanner(bytes.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) == nil && len(obj) > 0 {
			parts = append(parts, errorFields(obj)...)
			continue
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n")
}

func errorFields(obj map[string]any) []string {
	if !errorObject(obj) {
		return nil
	}
	var parts []string
	for _, key := range []string{"message", "error", "result", "content"} {
		if s, ok := stringValue(obj[key]); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, strings.TrimSpace(s))
		}
	}
	return parts
}

func errorObject(obj map[string]any) bool {
	if s, ok := stringValue(obj["type"]); ok && strings.EqualFold(s, "error") {
		return true
	}
	if s, ok := stringValue(obj["role"]); ok && strings.EqualFold(s, "error") {
		return true
	}
	if b, ok := obj["is_error"].(bool); ok && b {
		return true
	}
	if _, ok := obj["error"]; ok && obj["error"] != nil {
		return true
	}
	return false
}

func stringValue(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func goalFacts(output []byte) (status, verdict string) {
	sc := bufio.NewScanner(bytes.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var v any
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		walkGoal(v, &status, &verdict)
	}
	return status, verdict
}

func walkGoal(v any, status, verdict *string) {
	obj, ok := v.(map[string]any)
	if !ok {
		if list, ok := v.([]any); ok {
			for _, item := range list {
				walkGoal(item, status, verdict)
			}
		}
		return
	}
	if s, ok := stringValue(obj["status"]); ok {
		*status = s
	}
	if s, ok := stringValue(obj["last_classifier_verdict"]); ok {
		*verdict = s
	}
	for _, item := range obj {
		walkGoal(item, status, verdict)
	}
}

func grokBudgetLimited(output []byte) bool {
	status, _ := goalFacts(output)
	if strings.EqualFold(strings.TrimSpace(status), "budget_limited") {
		return true
	}
	return strings.Contains(strings.ToLower(string(output)), "budget_limited")
}

func grokAchieved(output []byte) bool {
	if grokBudgetLimited(output) {
		return false
	}
	status, verdict := goalFacts(output)
	return strings.EqualFold(strings.TrimSpace(status), "complete") && strings.EqualFold(strings.TrimSpace(verdict), "achieved")
}

func parseUsageAliases(output []byte, source string, aliases map[string]string) model.Usage {
	found, tokens, ok := findUsage(output, aliases)
	if !ok {
		return model.Usage{}
	}
	_ = found
	return model.Usage{Tokens: tokens, Source: source, Confidence: 1}
}

func findUsage(output []byte, aliases map[string]string) (raw map[string]json.RawMessage, tokens int64, ok bool) {
	sc := bufio.NewScanner(bytes.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &obj) != nil {
			continue
		}
		usageRaw, exists := obj["usage"]
		if !exists || strings.TrimSpace(string(usageRaw)) == "" || strings.TrimSpace(string(usageRaw)) == "null" {
			continue
		}
		n, parsed := tokensFromUsage(usageRaw, aliases)
		if !parsed {
			continue
		}
		raw, tokens, ok = nil, n, true
	}
	return raw, tokens, ok
}

func tokensFromUsage(raw json.RawMessage, aliases map[string]string) (int64, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || len(obj) == 0 {
		return 0, false
	}
	fields := map[string]int64{}
	seen := map[string]bool{}
	for key, dest := range aliases {
		v, exists := obj[key]
		if !exists {
			continue
		}
		n, ok := nonnegJSONInt(v)
		if !ok {
			continue
		}
		fields[dest] = n
		seen[dest] = true
	}
	if seen["total_tokens"] {
		return fields["total_tokens"], true
	}
	var sum int64
	any := false
	for _, dest := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "reasoning_tokens"} {
		if seen[dest] {
			sum += fields[dest]
			any = true
		}
	}
	return sum, any
}

func nonnegJSONInt(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return 0, false
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		if n < 0 {
			return 0, false
		}
		return n, true
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f < 0 || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}

var claudeUsageAliases = map[string]string{
	"input_tokens":                "input_tokens",
	"output_tokens":               "output_tokens",
	"cache_read_input_tokens":     "cache_read_input_tokens",
	"cache_creation_input_tokens": "cache_creation_input_tokens",
	"reasoning_tokens":            "reasoning_tokens",
	"total_tokens":                "total_tokens",
}

var cursorUsageAliases = map[string]string{
	"inputTokens":      "input_tokens",
	"outputTokens":     "output_tokens",
	"cacheReadTokens":  "cache_read_input_tokens",
	"cacheWriteTokens": "cache_creation_input_tokens",
}

var agyUsageAliases = map[string]string{
	"input_tokens":                "input_tokens",
	"output_tokens":               "output_tokens",
	"thinking_tokens":             "reasoning_tokens",
	"cache_read_tokens":           "cache_read_input_tokens",
	"cache_read_input_tokens":     "cache_read_input_tokens",
	"cache_creation_input_tokens": "cache_creation_input_tokens",
	"total_tokens":                "total_tokens",
}
