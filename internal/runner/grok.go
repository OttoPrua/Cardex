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
	"time"
	"unicode/utf8"

	"cardex/internal/model"
)

// Grok adapts the grok-build CLI. GoalMode selects the PTY /goal driver;
// LaunchSpec cannot carry requires, so the host sets this field.
// Prompt is the goal text Drive writes after the literal /goal.
// SessionDir is the grok session directory that holds goal/state.json and
// updates.jsonl. Completion and budget_limited are read from those files.
// The PTY ReadWriter does not carry completion or budget_limited: goal-mode
// Build is an interactive TUI. v1 observeNativeGrokGoal reads goal/state.json
// status and does not name a quota status. The quota terminal is the TUI's own
// rate-limit card, which v1 runHostedGrokGoal places on this PTY. Headless
// quota text is grokBuildLimitScanText, not that card. Crash is
// GrokProcessExit, supplied by the host after Wait; Drive cannot see it.
type Grok struct {
	GoalMode   bool
	Prompt     string
	SessionDir string
}

var _ Runner = Grok{}

// Drive terminals. Completion is a nil error. budget_limited is not completion.
var (
	ErrGrokBudgetLimited = errors.New("grok goal budget_limited")
	ErrGrokQuotaHit      = errors.New("grok goal quota_hit")
	ErrGrokCrash         = errors.New("grok goal crash")
	errGrokNoTerminal    = errors.New("grok goal: no terminal")
)

// GrokProcessExit is the host process-exit fact. Signaled and Signal come from
// WaitStatus after Wait (v1 grok.go process). A PTY line "signal: killed" is
// not this fact. Drive's ReadWriter cannot carry it.
type GrokProcessExit struct {
	Signaled bool
	Signal   int
}

// ConsumeGrokProcessExit reports a crash when WaitStatus was signaled with one
// of the signals whose Go names are v1's crash strings: quit, aborted, killed,
// bus error, segmentation fault. Other signals, including SIGTERM, are not.
func ConsumeGrokProcessExit(exit GrokProcessExit) (error, bool) {
	if !exit.Signaled {
		return nil, false
	}
	switch exit.Signal {
	case 3, 6, 9, 10, 11:
		return ErrGrokCrash, true
	default:
		return nil, false
	}
}

func (Grok) Name() string { return string(model.RunnerGrok) }

func (Grok) Capabilities() []string { return []string{"goal_mode"} }

func (g Grok) NeedsPTY() bool { return g.GoalMode }

func (Grok) WritablePaths() []string { return userPath(".grok") }

// promptArg is the prompt file's text. Cursor, Kimi, and AGY pass it on argv.
// Headless grok keeps the path on --prompt-file. An empty path is an empty prompt.
func promptArg(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

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

// grokGoalObserveEvery is how often Drive re-reads the session while a PTY
// Read is blocked. v1's hosted goal loop uses this same 100ms interval.
const grokGoalObserveEvery = 100 * time.Millisecond

// Drive writes the literal /goal and the prompt. Completion and budget_limited
// come from SessionDir, because goal-mode Build does not emit session JSON on
// the PTY. The session is polled while Read is blocked and checked again after
// EOF. A finished PTY line that is the interactive rate-limit card is
// ErrGrokQuotaHit. Headless stderr and headless error JSON are not that card.
// Crash is ConsumeGrokProcessExit.
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
	var pty []byte
	tmp := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if term, ok := ReadGrokGoalSession(g.SessionDir); ok {
			return term
		}
		n, err, term, termOK := readGoalChunk(ctx, rw, tmp, g.SessionDir)
		if termOK {
			return term
		}
		if n > 0 {
			pty = append(pty, tmp[:n]...)
			if grokInteractiveQuotaTerminal(pty, g.Prompt, errors.Is(err, io.EOF)) {
				return ErrGrokQuotaHit
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if term, ok := ReadGrokGoalSession(g.SessionDir); ok {
		return term
	}
	return errGrokNoTerminal
}

// readGoalChunk waits for one Read. While that Read is blocked it polls the
// session and, on a terminal, returns termOK without waiting for Read.
func readGoalChunk(ctx context.Context, r io.Reader, buf []byte, sessionDir string) (int, error, error, bool) {
	type got struct {
		n   int
		err error
	}
	ch := make(chan got, 1)
	go func() {
		n, err := r.Read(buf)
		ch <- got{n, err}
	}()
	tick := time.NewTicker(grokGoalObserveEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err(), nil, false
		case res := <-ch:
			return res.n, res.err, nil, false
		case <-tick.C:
			if term, ok := ReadGrokGoalSession(sessionDir); ok {
				return 0, nil, term, true
			}
		}
	}
}

func (g Grok) Classify(_ int, output []byte) model.ErrorClass {
	if grokBudgetLimited(output, g.Prompt) {
		return model.ErrorBudgetLimited
	}
	if grokInteractiveQuotaTerminal(output, g.Prompt, true) {
		return model.ErrorQuotaHit
	}
	return classifyScanned(output, g.Prompt, grokQuotaRe, func(text string) model.ErrorClass {
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
// classifyFailure. Crash is not a scanned phrase: "signal: killed" is v1's
// ExitError text, and ordinary transcripts contain lookalike words.
// ConsumeGrokProcessExit takes the host WaitStatus fact instead.
var (
	authClassRe       = regexp.MustCompile(`(?i)(?:\bhttp(?:/\d(?:\.\d)?)?\s+(?:status\s+)?401\b|\bhttp\s+status\s*[:=]\s*401\b|\bstatus(?:\s+code)?\s*[:=]\s*401\b|\b401\s*(?::|-)?\s*unauthorized\b|\bunauthorized\s*\(\s*401\s*\))|invalid api key|invalid or expired credentials|authentication failed|oauth token (?:has )?(?:expired|invalid|revoked)|please (?:re-?)?login|please log in again|reason\s*=\s*no auth context|请重新登录|token expired`)
	permissionClassRe = regexp.MustCompile(`(?i)403 forbidden|permission denied|access denied by (?:policy|admin|organization)|not authorized to (?:perform|access)|operation not permitted by (?:policy|admin)|blocked by (?:policy|admin|organization)`)
	timeoutClassRe    = regexp.MustCompile(`(?i)步骤超时[（(]|远程步骤超时[（(]|远端步骤超时[（(]|context deadline exceeded|deadline exceeded \(`)
	transportRe       = regexp.MustCompile(`(?i)connection (?:reset|refused|closed|error)|error sending request|i/o timeout|stream disconnect|no such host|network is unreachable|temporary failure in name resolution|disconnected before`)
	limitRe           = regexp.MustCompile(`(?i)usage limit|limit reached|out of extra usage|hit your (?:\w+ )?limit|limit will reset|out of usage credits|out of credits|/usage-credits|session limit`)
	engineQuotaRe     = regexp.MustCompile(`(?i)quota (?:exceeded|exhausted)|insufficient quota|exceeded your .{0,24}quota|额度不足|额度已用完|配额不足|配额已用尽|余额不足|欠费`)
	grokQuotaRe       = regexp.MustCompile(`(?i)(?:^|[^0-9])429(?:[^0-9]|$)|too many requests|rate limit|usage limit|quota (?:exceeded|exhausted)|insufficient (?:quota|credits)|out of (?:credits|usage)|额度(?:不足|已用完)|配额(?:不足|已用尽)|限额(?:不足|已用尽)`)
	cursorQuotaRe     = regexp.MustCompile(`(?i)(?:^|[^0-9])429(?:[^0-9]|$)|too many requests|rate limit|usage limit|quota (?:exceeded|exhausted)|insufficient (?:quota|credits)|out of (?:credits|usage)|额度(?:不足|已用完)|配额(?:不足|已用尽)|限额(?:不足|已用尽)`)
	cursorPolicyRe    = regexp.MustCompile(`(?i)actionrequired(?:error)?:.*review data policy|acknowledge .*data retention policy|review data policy`)
	kimiQuotaRe       = regexp.MustCompile(`(?i)(?:^|[^0-9])429(?:[^0-9]|$)|too many requests|rate limit|quota (?:exceeded|exhausted)|insufficient (?:quota|credits)|usage limit|额度(?:不足|已用完)|配额(?:不足|已用尽)|限额(?:不足|已用尽)`)
	// plainQuotaLine is v1 policyPlainQuotaDiagnosticRe: a whole-line presemantic
	// quota diagnostic. A sentence that merely contains the words is not a line.
	plainQuotaLine = regexp.MustCompile(`(?i)^(?:error:\s*)?(?:(?:http(?:/\d(?:\.\d)?)?\s+)?429(?::\s*(?:too many requests|rate limit(?:ed)?|usage limit(?: reached)?|quota (?:exceeded|exhausted)))?|too many requests|rate limit(?:ed)?|usage limit(?: reached)?|quota (?:exceeded|exhausted)|insufficient (?:quota|credits)|out of (?:credits|usage)|额度(?:不足|已用完)|配额(?:不足|已用尽)|限额(?:不足|已用尽))$`)
	// plainTransportLine is v1 policyPlainTransportDiagnosticRe plus whole lines
	// transportRe already accepts (connection error/reset, disconnected before).
	plainTransportLine = regexp.MustCompile(`(?i)^(?:error:\s*)?(?:connection (?:reset(?: by peer)?|refused|closed|error)|socket (?:closed|error)|error sending request|tls (?:handshake|error)|unexpected eof|disconnected before(?: (?:receiving )?(?:a )?response)?|i/o timeout|econn(?:reset|refused)|stream disconnect|no route to host|network is unreachable|host is unreachable|temporary failure in name resolution|no such host|dns (?:lookup|resolution) (?:failed|error)|getaddrinfo\s+(?:enotfound|eai_again)(?:\s+[a-z0-9._:-]+)?|dial tcp:\s*lookup\s+[a-z0-9._-]+(?:\s+on\s+[^:;]+)?:\s*no such host|(?:read|write) tcp [^;]+:\s*(?:connection reset by peer|i/o timeout))$`)
	plainTimeoutLine   = regexp.MustCompile(`(?i)^(?:(?:error|grok_build_process_error|codex_error|remote_codex_error):\s*)?(?:(?:步骤超时|远程步骤超时|远端步骤超时)[（(][^)）]*[)）]|context deadline exceeded|deadline exceeded \()$`)
	// grokRateRetryStatusLine is the grok 1.0.40 interactive status row painted
	// after an empty HTTP 429. The trailing class is that row's chrome
	// (timers, ⇣token count, [stop]). A sentence that continues after the
	// status is not the row.
	grokRateRetryStatusLine = regexp.MustCompile(`^Rate limited \| Retrying \(attempt [0-9]+\)\.\.\.[0-9.s ⇣k\[\]top]*$`)
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

func classifyScanned(output []byte, prompt string, quota *regexp.Regexp, before func(string) model.ErrorClass) model.ErrorClass {
	text := errorScan(output, prompt)
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

// errorScan collects explicit error-record fields and whole-line diagnostics.
// Body and the /goal echo are not error facts. The echo is Grok.Prompt when
// that text is present, otherwise the /goal line and the plain lines after it.
// A blank line does not end that span, and neither does a fixed line count.
// A line already consumed as that known prompt is never a diagnostic, JSON
// included. A complete JSON error record outside that span is still scanned.
// Assistant JSON and an unfinished record are not error facts. The caller
// passes the whole capture, so the last line is at EOF even without a newline.
// Nested fields are not walked.
func errorScan(output []byte, prompt string) string {
	return scanOutput(output, prompt)
}

func scanOutput(output []byte, prompt string) string {
	var parts []string
	walkNonEchoLines(output, prompt, func(line string) {
		if heldJSONRecord(line) {
			return
		}
		var obj map[string]any
		if line[0] == '{' && json.Unmarshal([]byte(line), &obj) == nil && len(obj) > 0 {
			parts = append(parts, errorFields(obj)...)
			return
		}
		if plainDiagnostic(line) {
			parts = append(parts, line)
		}
	})
	return strings.Join(parts, "\n")
}

// goalPromptEchoLines is the echo Drive writes for a known prompt:
// "/goal" or "/goal " plus the prompt, with internal newlines kept.
// An empty prompt is not a span; the caller then uses the /goal line itself.
func goalPromptEchoLines(prompt string) []string {
	if prompt == "" {
		return nil
	}
	written := "/goal " + strings.TrimRight(prompt, "\r\n")
	written = strings.ReplaceAll(written, "\r\n", "\n")
	written = strings.TrimRight(written, "\r")
	parts := strings.Split(written, "\n")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// jsonErrorRecord is one complete JSON object that carries an error field.
// It is a diagnostic source, not prompt text.
func jsonErrorRecord(line string) bool {
	if line == "" || line[0] != '{' || !json.Valid([]byte(line)) {
		return false
	}
	var obj map[string]any
	if json.Unmarshal([]byte(line), &obj) != nil || len(obj) == 0 {
		return false
	}
	return errorObject(obj)
}

// walkNonEchoLines yields finished lines that are not the echo.
// When prompt is the text Drive wrote, that span is the echo, including
// blank lines inside it. Otherwise a /goal line opens an echo that runs
// through later plain lines; a blank line does not close it, and the walk
// does not stop after a fixed number of lines. A line consumed as the known
// prompt is not yielded, even when it is a JSON error record or any other
// JSON object. A complete JSON error record outside that span is yielded.
// The last line is finished: Classify sees the whole capture.
func walkNonEchoLines(output []byte, prompt string, fn func(line string)) {
	echoLines := goalPromptEchoLines(prompt)
	echoIdx := 0
	matchedPrompt := false
	inEcho := false
	rest := output
	for len(rest) > 0 {
		raw := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			raw = rest[:i]
			rest = rest[i+1:]
		} else {
			rest = nil
		}
		line := strings.TrimSpace(strings.TrimRight(string(raw), "\r"))
		if echoIdx < len(echoLines) && line == echoLines[echoIdx] {
			echoIdx++
			inEcho = echoIdx < len(echoLines)
			matchedPrompt = echoIdx == len(echoLines)
			continue
		}
		if !matchedPrompt && echoIdx == 0 && goalEchoLine(line) {
			inEcho = true
			continue
		}
		if jsonErrorRecord(line) {
			inEcho = false
			fn(line)
			continue
		}
		if inEcho && !matchedPrompt {
			continue
		}
		if line == "" {
			continue
		}
		fn(line)
	}
}

func plainDiagnostic(line string) bool {
	switch {
	case plainQuotaLine.MatchString(line), plainTransportLine.MatchString(line), plainTimeoutLine.MatchString(line):
		return true
	case line == acceptedBootstrapBeforeProviderRefusal:
		return true
	case labeledDiagnostic(line, authClassRe), labeledDiagnostic(line, permissionClassRe), labeledDiagnostic(line, cursorPolicyRe):
		return true
	default:
		return false
	}
}

func labeledDiagnostic(line string, re *regexp.Regexp) bool {
	loc := re.FindStringIndex(line)
	if loc == nil || !allowedLabelPrefix(line[:loc[0]]) {
		return false
	}
	prefix, suffix := line[:loc[0]], line[loc[1]:]
	matched := line[loc[0]:loc[1]]
	if hasCJK(matched) && strings.TrimSpace(suffix) != "" {
		return false
	}
	if strings.TrimSpace(prefix) != "" && !whitelistedLabel(prefix) && strings.TrimSpace(suffix) != "" {
		return false
	}
	if hasCJK(suffix) && !hasCJK(matched) {
		return false
	}
	return true
}

func allowedLabelPrefix(s string) bool {
	if strings.TrimSpace(s) == "" {
		return true
	}
	return knownDiagnosticLabel(s)
}

// knownDiagnosticLabel is a source label v1 already emits on a diagnostic
// line. An arbitrary token such as 示例 is body text.
func knownDiagnosticLabel(s string) bool {
	s = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ":"))
	switch strings.ToLower(s) {
	case "error", "grok_build_process_error", "codex_error", "remote_codex_error", "认证错误":
		return true
	default:
		return false
	}
}

func whitelistedLabel(s string) bool {
	s = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ":"))
	switch strings.ToLower(s) {
	case "error", "grok_build_process_error", "codex_error", "remote_codex_error":
		return true
	default:
		return false
	}
}

func hasCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

// goalEchoLine is the /goal command the driver writes, echoed by the TUI.
func goalEchoLine(line string) bool {
	s := strings.TrimSpace(line)
	return s == "/goal" || strings.HasPrefix(s, "/goal ")
}

func cutScanLine(rest []byte) (line string, next []byte, last bool) {
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		return string(rest[:i]), rest[i+1:], false
	}
	return string(rest), nil, true
}

// heldJSONRecord is an unfinished or invalid JSON value. It is not an error fact.
func heldJSONRecord(line string) bool {
	if line == "" {
		return false
	}
	switch line[0] {
	case '{', '[':
		return !json.Valid([]byte(line))
	default:
		return false
	}
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

func grokBudgetLimited(output []byte, prompt string) bool {
	status, ok := goalUpdatedStatus(output, prompt)
	return ok && strings.EqualFold(strings.TrimSpace(status), "budget_limited")
}

// grokInteractiveQuotaLine is the grok TUI rate-limit card, not a headless
// stderr line. The sentences are the pager and shell string table in the grok
// CLI (crates/codegen/xai-grok-pager/src/app/mod.rs next to "Rate limited",
// and the shell billing sentences beside it). v1 runHostedGrokGoal puts that
// TUI on the PTY. Apostrophes in the longer sentences are U+2019.
func grokInteractiveQuotaLine(line string) bool {
	switch strings.TrimSpace(line) {
	case "You've hit the rate limit for your plan.",
		"You\u2019ve hit the rate limit for your plan. Upgrade your account or try again later.",
		"You\u2019ve hit your team\u2019s API rate limit. Ask a team admin to purchase more credits for higher limits, or try again later. See https://docs.x.ai/developers/rate-limits#rate-limit-tiers",
		"You\u2019ve reached your free Grok Build usage limit for now. Get SuperGrok for much higher limits, or try again later: https://grok.com/supergrok?referrer=grok-build",
		"You\u2019ve hit the credit limit for your plan.",
		"You\u2019ve hit your spending cap.",
		"You hit your free usage limit.",
		"You hit your weekly limit.",
		"out of credits or over your spending limit. Add credits and retry.":
		return true
	default:
		return false
	}
}

// grokInteractiveQuotaTerminal reports the interactive quota card on a
// finished line that is not the known prompt echo, or on a committed TUI
// frame. finishTail treats a capture's last line and the screen's current
// frame as finished; a streaming read leaves an unfinished tail unmatched.
// Newline sentences are the pager card. grok 1.0.40 also paints the status
// with cursor addressing and commits it at ESC[?2026l. That transcript is
// testdata/grok_goal_quota_pty_raw.bin.
func grokInteractiveQuotaTerminal(output []byte, prompt string, finishTail bool) bool {
	return grokInteractiveQuotaLines(output, prompt, finishTail) ||
		grokInteractiveQuotaScreen(output, prompt, finishTail)
}

func grokInteractiveQuotaLines(output []byte, prompt string, finishTail bool) bool {
	if !finishTail {
		i := bytes.LastIndexByte(output, '\n')
		if i < 0 {
			return false
		}
		output = output[:i+1]
	}
	hit := false
	walkNonEchoLines(output, prompt, func(line string) {
		if grokInteractiveQuotaLine(line) || grokRateRetryStatus(line) {
			hit = true
		}
	})
	return hit
}

// grokRateRetryStatus is the cursor-addressed "Rate limited | Retrying" row
// after a leading spinner. The row is the whole status, not prose that
// mentions those words.
func grokRateRetryStatus(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	r, size := utf8.DecodeRuneInString(s)
	if size > 0 && r >= 0x2800 && r <= 0x28FF {
		s = strings.TrimSpace(s[size:])
	}
	return grokRateRetryStatusLine.MatchString(s)
}

const (
	grokGoalScreenRows = 24
	grokGoalScreenCols = 80
)

// grokScreen is the 24×80 frame grok 1.0.40 used for the quota transcript.
// One rune is one cell. Writes past the frame are dropped. A synchronized
// update end (ESC[?2026l) commits the frame.
type grokScreen struct {
	cell []rune
	r, c int
}

func newGrokScreen() *grokScreen {
	s := &grokScreen{cell: make([]rune, grokGoalScreenRows*grokGoalScreenCols)}
	for i := range s.cell {
		s.cell[i] = ' '
	}
	return s
}

func (s *grokScreen) put(ch rune) {
	switch ch {
	case '\n':
		s.r++
		s.c = 0
		if s.r >= grokGoalScreenRows {
			copy(s.cell, s.cell[grokGoalScreenCols:])
			for i := (grokGoalScreenRows - 1) * grokGoalScreenCols; i < len(s.cell); i++ {
				s.cell[i] = ' '
			}
			s.r = grokGoalScreenRows - 1
		}
		return
	case '\r':
		s.c = 0
		return
	}
	if ch < ' ' || s.r < 0 || s.c < 0 || s.r >= grokGoalScreenRows || s.c >= grokGoalScreenCols {
		return
	}
	s.cell[s.r*grokGoalScreenCols+s.c] = ch
	s.c++
}

func (s *grokScreen) cup(body string) {
	row, col := 1, 1
	if body != "" {
		parts := strings.Split(body, ";")
		row = vtParam(parts[0])
		if len(parts) > 1 {
			col = vtParam(parts[1])
		}
	}
	s.r = row - 1
	s.c = col - 1
}

func vtParam(s string) int {
	if s == "" {
		return 1
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 1
		}
		n = n*10 + int(ch-'0')
	}
	if n == 0 {
		return 1
	}
	return n
}

func (s *grokScreen) rowString(row int) string {
	raw := s.cell[row*grokGoalScreenCols : (row+1)*grokGoalScreenCols]
	end := len(raw)
	for end > 0 && raw[end-1] == ' ' {
		end--
	}
	if end == 0 {
		return ""
	}
	return string(raw[:end])
}

func (s *grokScreen) showsInteractiveQuota(prompt string) bool {
	echo := goalPromptEchoLines(prompt)
	for row := 0; row < grokGoalScreenRows; row++ {
		line := s.rowString(row)
		if line == "" || grokQuotaEcho(line, echo) {
			continue
		}
		if grokInteractiveQuotaLine(line) || grokRateRetryStatus(line) {
			return true
		}
	}
	return false
}

func grokQuotaEcho(line string, echo []string) bool {
	line = strings.TrimSpace(line)
	for _, e := range echo {
		if line == e {
			return true
		}
	}
	return false
}

// grokInteractiveQuotaScreen replays PTY bytes onto a 24×80 frame. A
// committed synchronized update can carry the quota status. finishTail also
// reads the frame left on screen at EOF. Cursor-addressed redraws stay in
// the byte stream; this walk is what consumes them.
func grokInteractiveQuotaScreen(output []byte, prompt string, finishTail bool) bool {
	s := newGrokScreen()
	hit := false
	commit := func() {
		if !hit && s.showsInteractiveQuota(prompt) {
			hit = true
		}
	}
	i := 0
	for i < len(output) && !hit {
		if output[i] != 0x1b {
			r, size := utf8.DecodeRune(output[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			s.put(r)
			i += size
			continue
		}
		if i+1 >= len(output) {
			break
		}
		switch output[i+1] {
		case ']':
			j := i + 2
			for j < len(output) && output[j] != 0x07 && output[j] != 0x1b {
				j++
			}
			if j < len(output) && output[j] == 0x07 {
				j++
			} else if j+1 < len(output) && output[j] == 0x1b && output[j+1] == '\\' {
				j += 2
			}
			i = j
		case '[':
			j := i + 2
			for j < len(output) && (output[j] < 0x40 || output[j] > 0x7e) {
				j++
			}
			if j >= len(output) {
				i = len(output)
				break
			}
			body := string(output[i+2 : j])
			final := output[j]
			i = j + 1
			if final == 'H' && !strings.HasPrefix(body, "?") {
				s.cup(body)
			} else if final == 'l' && body == "?2026" {
				commit()
			}
		default:
			i += 2
		}
	}
	if finishTail {
		commit()
	}
	return hit
}

// goalUpdatedStatus reads status only from a complete session update whose
// sessionUpdate is goal_updated, at params.update.status. An arbitrary or
// nested status field is not that position. An unfinished record is not read.
// A line consumed as the known prompt is not that update, JSON included.
func goalUpdatedStatus(output []byte, prompt string) (string, bool) {
	echoLines := goalPromptEchoLines(prompt)
	echoIdx := 0
	rest := output
	var status string
	found := false
	for len(rest) > 0 {
		line, next, _ := cutScanLine(rest)
		rest = next
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if echoIdx < len(echoLines) && line == echoLines[echoIdx] {
			echoIdx++
			continue
		}
		if line == "" || heldJSONRecord(line) || line[0] != '{' {
			continue
		}
		var rec struct {
			Params struct {
				Update struct {
					SessionUpdate string `json:"sessionUpdate"`
					Status        string `json:"status"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.Params.Update.SessionUpdate != "goal_updated" {
			continue
		}
		found = true
		status = rec.Params.Update.Status
	}
	return status, found
}

// ReadGrokGoalSession reports a goal terminal from v1's session channel:
// goal/state.json agreed with the last goal_updated line in updates.jsonl.
// Completion is a nil error. A session/update line that is not in these files
// is not a terminal. dir empty means the host has not supplied the session.
func ReadGrokGoalSession(dir string) (error, bool) {
	if strings.TrimSpace(dir) == "" {
		return nil, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, "goal", "state.json"))
	if err != nil {
		return nil, false
	}
	var st struct {
		GoalID                string `json:"goal_id"`
		Status                string `json:"status"`
		LastClassifierVerdict string `json:"last_classifier_verdict"`
	}
	if json.Unmarshal(raw, &st) != nil || strings.TrimSpace(st.GoalID) == "" {
		return nil, false
	}
	finalStatus, finalVerdict, sessionID, ok := finalGoalUpdate(filepath.Join(dir, "updates.jsonl"), st.GoalID)
	if !ok || !strings.EqualFold(strings.TrimSpace(finalStatus), strings.TrimSpace(st.Status)) {
		return nil, false
	}
	stateVerdict := strings.TrimSpace(st.LastClassifierVerdict)
	if stateVerdict != "" && finalVerdict != "" && !strings.EqualFold(stateVerdict, finalVerdict) {
		return nil, false
	}
	if !summaryAgrees(filepath.Join(dir, "summary.json"), sessionID) {
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(st.Status)) {
	case "budget_limited":
		return ErrGrokBudgetLimited, true
	case "complete":
		if strings.EqualFold(stateVerdict, "achieved") && strings.EqualFold(finalVerdict, "achieved") {
			return nil, true
		}
		return nil, false
	default:
		return nil, false
	}
}

func finalGoalUpdate(path, goalID string) (status, verdict, sessionID string, ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", false
	}
	rest := raw
	matched := false
	for len(rest) > 0 {
		line, next, _ := cutScanLine(rest)
		rest = next
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if heldJSONRecord(line) {
			return "", "", "", false
		}
		var rec struct {
			Params struct {
				SessionID string `json:"sessionId"`
				Update    struct {
					SessionUpdate         string `json:"sessionUpdate"`
					GoalID                string `json:"goal_id"`
					Status                string `json:"status"`
					LastClassifierVerdict string `json:"last_classifier_verdict"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			return "", "", "", false
		}
		if rec.Params.Update.SessionUpdate != "goal_updated" {
			continue
		}
		if strings.TrimSpace(rec.Params.Update.GoalID) != goalID {
			continue
		}
		matched = true
		sessionID = rec.Params.SessionID
		status = rec.Params.Update.Status
		verdict = rec.Params.Update.LastClassifierVerdict
	}
	return status, verdict, sessionID, matched
}

func summaryAgrees(path, sessionID string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true
		}
		return false
	}
	var sum struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	if json.Unmarshal(raw, &sum) != nil {
		return false
	}
	id := strings.TrimSpace(sum.Info.ID)
	if id == "" || sessionID == "" {
		return false
	}
	return id == sessionID
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
