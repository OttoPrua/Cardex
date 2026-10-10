package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	antigravityRunnerName              = "agy"
	antigravityCooldownName            = "agy"
	antigravitySubtypeNeedsInteraction = "antigravity_needs_interaction"
	antigravitySubtypeError            = "antigravity_error"
	antigravitySubtypeQuota            = "antigravity_quota"
	antigravitySubtypeAuth             = "antigravity_auth"
	antigravitySubtypeInputTooLong     = "antigravity_input_too_long"
	antigravitySubtypeUnknownTerminal  = "antigravity_unknown_terminal"
	antigravitySubtypeInvalidTerminal  = "antigravity_invalid_terminal"
	antigravitySubtypeEmptySuccess     = "antigravity_empty_success"
	antigravitySubtypeProcessError     = "antigravity_process_error"
	antigravityCapabilityUnknownVer    = "agy_unknown_version"
	antigravityCapabilityNoInput       = "agy_no_input_capability"
	antigravityUsageSource             = "antigravity_result"
	antigravityStatusSuccess           = "success"
	antigravityStatusError             = "error"
	antigravityStatusUnknown           = "unknown"
	antigravityDeniedCommand           = "command"
	antigravityDeniedWebSearch         = "web_search"
	antigravityDeniedUnknown           = "unknown"
)

var antigravityUsageAliases = map[string]string{
	"input_tokens":                rawFieldInput,
	"output_tokens":               rawFieldOutput,
	"thinking_tokens":             rawFieldReasoning,
	"cache_read_tokens":           rawFieldCacheRead,
	"cache_read_input_tokens":     rawFieldCacheRead,
	"cache_creation_input_tokens": rawFieldCacheCreation,
	"total_tokens":                rawFieldTotal,
}

func antigravityVia(via string) bool { return via == antigravityRunnerName }

func antigravityEnabled(cfg *Config) bool {
	return cfg != nil && cfg.Antigravity != nil && cfg.Antigravity.Enabled &&
		strings.TrimSpace(cfg.AntigravityBin) != ""
}

func resolveAntigravityModel(_ *Config, t *Task) string {
	if t != nil && strings.TrimSpace(t.AgyModel) != "" {
		return strings.TrimSpace(t.AgyModel)
	}
	return ""
}

func resolveAntigravityEffort(cfg *Config) string {
	if cfg == nil || cfg.Antigravity == nil || strings.TrimSpace(cfg.Antigravity.Effort) == "" {
		return "high"
	}
	return strings.ToLower(strings.TrimSpace(cfg.Antigravity.Effort))
}

func resolveAntigravityTaskEffort(cfg *Config, t *Task) string {
	if mixedOwnerTask(t) && t.EffortExplicit {
		return t.Effort
	}
	return resolveAntigravityEffort(cfg)
}

type antigravityDeniedAction struct {
	Action      string `json:"action"`
	DisplayName string `json:"display_name"`
}

// antigravityDiagnostics is the safe projection persisted on the route attempt.
// Status and denied actions are finite known categories, never raw provider values.
type antigravityDiagnostics struct {
	StatusCategory     string   `json:"status_category,omitempty"`
	DeniedCategories   []string `json:"denied_categories,omitempty"`
	DeniedCount        int      `json:"denied_count,omitempty"`
	NeedsInteraction   bool     `json:"needs_interaction,omitempty"`
	Headless           bool     `json:"headless,omitempty"`
	InteractiveCapable bool     `json:"interactive_capable,omitempty"`
	CapabilityReason   string   `json:"capability_reason,omitempty"`
	ConversationSeen   bool     `json:"conversation_id_present,omitempty"`
	UsageSeen          bool     `json:"usage_present,omitempty"`
	DurationSeen       bool     `json:"duration_present,omitempty"`
	PlanSlashConflict  bool     `json:"plan_slash_conflict,omitempty"`
	StderrBytes        int      `json:"stderr_bytes,omitempty"`
}

func (d *antigravityDiagnostics) safeSummary() string {
	if d == nil {
		return ""
	}
	parts := []string{"agy"}
	if d.StatusCategory != "" {
		parts = append(parts, "status="+d.StatusCategory)
	}
	if d.CapabilityReason != "" {
		parts = append(parts, "capability="+d.CapabilityReason)
	}
	if d.NeedsInteraction {
		parts = append(parts, "needs_interaction")
	}
	if len(d.DeniedCategories) > 0 {
		parts = append(parts, "denied="+strings.Join(d.DeniedCategories, ","))
	}
	if d.PlanSlashConflict {
		parts = append(parts, "plan_slash_conflict")
	}
	return strings.Join(parts, " ")
}

func antigravityStatusCategory(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCESS":
		return antigravityStatusSuccess
	case "ERROR":
		return antigravityStatusError
	case "":
		return ""
	default:
		return antigravityStatusUnknown
	}
}

func antigravityDeniedCategory(action, display string) string {
	joined := strings.ToLower(strings.TrimSpace(action) + " " + strings.TrimSpace(display))
	compact := strings.NewReplacer("_", "", "-", "", " ", "").Replace(joined)
	switch {
	case strings.Contains(compact, "runcommand") || strings.Contains(compact, "command") ||
		strings.Contains(compact, "bash") || strings.Contains(compact, "shell"):
		return antigravityDeniedCommand
	case strings.Contains(compact, "websearch"):
		return antigravityDeniedWebSearch
	default:
		return antigravityDeniedUnknown
	}
}

func antigravityMode(t *Task) string {
	if t != nil && t.Type == typeSequence && !(mixedOwnerTask(t) && t.WorkClass == "management") {
		return "accept-edits"
	}
	return "plan"
}

func antigravityCapabilityReason(version string, headless, interactiveNeeded bool) string {
	if headless && interactiveNeeded {
		return antigravityCapabilityNoInput
	}
	if strings.TrimSpace(version) == "" {
		return antigravityCapabilityUnknownVer
	}
	return ""
}

func antigravityArgs(cfg *Config, t *Task, model, prompt string) []string {
	mode := antigravityMode(t)
	args := []string{"--output-format", "json", "--mode", mode, "--model", model}
	if !strings.Contains(strings.ToLower(model), "thinking") {
		effort := resolveAntigravityTaskEffort(cfg, t)
		args = append(args, "--effort", effort)
	}
	if mode != "plan" {
		args = append(args, "--disable-slash-commands")
	}
	if t != nil && t.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return append(args, "--print", prompt)
}

func parseAntigravityJSON(raw []byte) *claudeResult {
	res := &claudeResult{Type: "result"}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		res.IsError = true
		res.Subtype = antigravitySubtypeUnknownTerminal
		res.Result = "Antigravity 未返回 JSON 终局"
		return res
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var object map[string]json.RawMessage
	if err := dec.Decode(&object); err != nil || object == nil {
		res.IsError = true
		res.Subtype = antigravitySubtypeInvalidTerminal
		res.Result = "Antigravity 未返回有效 JSON 终局"
		return res
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		res.IsError = true
		res.Subtype = antigravitySubtypeUnknownTerminal
		res.Result = "Antigravity JSON 不是单一 print 终局"
		return res
	}
	res.ObservationComplete = true
	d := &antigravityDiagnostics{Headless: true}
	res.AntigravityDiagnostics = d
	res.UsageSource = antigravityUsageSource

	if rawKeyPresent(object, "conversation_id") {
		var id string
		if json.Unmarshal(object["conversation_id"], &id) == nil && strings.TrimSpace(id) != "" {
			res.SessionID = strings.TrimSpace(id)
			d.ConversationSeen = true
		}
	}
	if raw, ok := object["usage"]; ok && rawKeyPresent(object, "usage") {
		res.UsageFields = usageFieldsFromJSON(raw, antigravityUsageAliases)
		if !fieldSetEmpty(res.UsageFields) {
			d.UsageSeen = true
			res.Usage = usageInfoFromFieldSet(res.UsageFields)
		}
	}
	if rawKeyPresent(object, "num_turns") {
		var turns int
		if json.Unmarshal(object["num_turns"], &turns) == nil {
			res.NumTurns = turns
			res.TurnsPresent = true
		}
	}
	if rawKeyPresent(object, "duration_seconds") {
		if f, ok := parseOptionalJSONFloat64(object["duration_seconds"]); ok {
			res.DurationMS = int64(*f * 1000)
			res.DurationPresent = true
			d.DurationSeen = true
		}
	} else if rawKeyPresent(object, "duration_ms") {
		if n, ok := parseOptionalJSONInt64(object["duration_ms"]); ok {
			res.DurationMS = *n
			res.DurationPresent = true
			d.DurationSeen = true
		}
	}

	rawStatus := ""
	if rawKeyPresent(object, "status") {
		_ = json.Unmarshal(object["status"], &rawStatus)
	}
	d.StatusCategory = antigravityStatusCategory(rawStatus)
	denied := parseAntigravityDenied(object["denied_actions"])
	seen := map[string]bool{}
	for _, a := range denied {
		cat := antigravityDeniedCategory(a.Action, a.DisplayName)
		if !seen[cat] {
			d.DeniedCategories = append(d.DeniedCategories, cat)
			seen[cat] = true
		}
	}
	d.DeniedCount = len(denied)
	body := antigravityBody(object)
	hasBody := strings.TrimSpace(body) != ""
	errText, hasErr := antigravityErrorText(object)

	if hasErr {
		res.IsError = true
		res.TerminalEvents = 1
		if d.StatusCategory == "" {
			d.StatusCategory = antigravityStatusError
		}
		switch {
		case antigravityQuotaText(errText):
			res.Subtype = antigravitySubtypeQuota
			res.Result = "Antigravity 用量限额"
		case antigravityAuthText(errText):
			res.Subtype = antigravitySubtypeAuth
			res.Result = "Antigravity 认证失败"
		case antigravityInputTooLongText(errText):
			res.Subtype = antigravitySubtypeInputTooLong
			res.Result = "Antigravity 输入超长"
		default:
			res.Subtype = antigravitySubtypeError
			res.Result = "Antigravity 返回结构化错误"
		}
		return res
	}
	if d.StatusCategory == antigravityStatusError || d.StatusCategory == antigravityStatusUnknown {
		res.IsError = true
		res.TerminalEvents = 1
		if antigravityQuotaText(rawStatus) {
			res.Subtype = antigravitySubtypeQuota
			res.Result = "Antigravity 用量限额"
		} else {
			res.Subtype = antigravitySubtypeError
			res.Result = "Antigravity 返回非成功状态"
		}
		return res
	}

	if d.StatusCategory == antigravityStatusSuccess && hasBody {
		res.Result = strings.TrimSpace(body)
		res.TerminalEvents = 1
		res.SemanticEvents = 1
		res.ModelEvents = 1
		return res
	}
	if d.StatusCategory == antigravityStatusSuccess && !hasBody && d.DeniedCount > 0 {
		d.NeedsInteraction = true
		res.IsError = true
		res.TerminalEvents = 1
		res.Subtype = antigravitySubtypeNeedsInteraction
		res.Result = "Antigravity 需要交互：动作被拒绝且没有可验收产物"
		return res
	}
	if d.StatusCategory == antigravityStatusSuccess && !hasBody {
		res.IsError = true
		res.TerminalEvents = 1
		res.Subtype = antigravitySubtypeEmptySuccess
		res.Result = "Antigravity SUCCESS 包装不是已完成工作"
		return res
	}
	res.IsError = true
	res.Subtype = antigravitySubtypeUnknownTerminal
	res.Result = "Antigravity JSON 缺少已知终态"
	return res
}

func antigravityBody(object map[string]json.RawMessage) string {
	for _, key := range []string{"result", "response", "text"} {
		var value string
		if json.Unmarshal(object[key], &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseAntigravityDenied(raw json.RawMessage) []antigravityDeniedAction {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var out []antigravityDeniedAction
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func antigravityErrorText(object map[string]json.RawMessage) (string, bool) {
	raw, ok := object["error"]
	if !ok || strings.TrimSpace(string(raw)) == "null" || len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		text = strings.TrimSpace(text)
		return text, text != "" || string(raw) != "null"
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		for _, key := range []string{"message", "msg", "text", "type"} {
			var v string
			if json.Unmarshal(obj[key], &v) == nil && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v), true
			}
		}
		return "Antigravity 返回结构化错误", true
	}
	return "Antigravity 返回结构化错误", true
}

func antigravityQuotaText(s string) bool {
	return limitRe.MatchString(s) || strings.Contains(strings.ToLower(s), "quota")
}

func antigravityAuthText(s string) bool {
	return authClassRe.MatchString(s)
}

func antigravityInputTooLongText(s string) bool {
	return inputTooLongClassRe.MatchString(s)
}

func usageInfoFromFieldSet(f usageFieldSet) *usageInfo {
	u := &usageInfo{}
	if f.InputTokens != nil {
		u.InputTokens = int(*f.InputTokens)
	}
	if f.OutputTokens != nil {
		u.OutputTokens = int(*f.OutputTokens)
	}
	if f.CacheReadInputTokens != nil {
		u.CacheReadInputTokens = int(*f.CacheReadInputTokens)
	}
	if f.CacheCreationInputTokens != nil {
		u.CacheCreationInputTokens = int(*f.CacheCreationInputTokens)
	}
	return u
}

func classifyAntigravityStderr(stderr string, d *antigravityDiagnostics) {
	if d == nil {
		return
	}
	d.StderrBytes = len(stderr)
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "mode plan has no effect") && strings.Contains(lower, "slash command") {
		d.PlanSlashConflict = true
	}
	if strings.Contains(lower, "headless") && strings.Contains(lower, "cannot prompt") {
		if d.CapabilityReason == "" {
			d.CapabilityReason = antigravityCapabilityNoInput
		}
	}
}

func attachAntigravityInvokeFacts(res *claudeResult, t *Task, stderr string, runErr error) {
	if res == nil {
		return
	}
	if res.AntigravityDiagnostics == nil {
		res.AntigravityDiagnostics = &antigravityDiagnostics{}
	}
	d := res.AntigravityDiagnostics
	d.Headless = true
	d.InteractiveCapable = false
	interactiveNeeded := antigravityMode(t) == "accept-edits" && t != nil && !t.SkipPermissions
	if d.CapabilityReason == "" {
		d.CapabilityReason = antigravityCapabilityReason(res.NativeVersion, true, interactiveNeeded)
	}
	classifyAntigravityStderr(stderr, d)
	if runErr != nil && (res.Subtype == "" || res.Subtype == antigravitySubtypeUnknownTerminal) {
		res.IsError = true
		res.Subtype = antigravitySubtypeProcessError
		if strings.TrimSpace(res.Result) == "" {
			res.Result = "Antigravity 进程未正常完成"
		}
	}
}

func invokeAntigravity(ctx context.Context, cfg *Config, t *Task, prompt string) (*claudeResult, string, error) {
	if !antigravityEnabled(cfg) {
		return nil, "", fmt.Errorf("antigravity/antigravity_bin 未启用")
	}
	if !providerPreflightReady(t, antigravityRunnerName) {
		return nil, "", fmt.Errorf("Antigravity 未通过当前派发的 provider preflight")
	}
	model := resolveAntigravityModel(cfg, t)
	if strings.TrimSpace(model) == "" {
		return nil, "", fmt.Errorf("未解析出 Antigravity 模型")
	}
	args := antigravityArgs(cfg, t, model, prompt)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.StepTimeoutMin)*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, cfg.AntigravityBin, args...)
	setupProcGroup(cmd)
	cmd.Dir = t.Dir
	home, _ := os.UserHomeDir()
	cmd.Env = providerChildEnv(home, map[string]string{"NO_COLOR": "1"})
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := runCmdRegisteredForTask(cmd, t.ID)
	if runCtx.Err() == context.DeadlineExceeded {
		runErr = fmt.Errorf("步骤超时（%d 分钟）", cfg.StepTimeoutMin)
	}
	res := parseAntigravityJSON(stdout.Bytes())
	attachAntigravityInvokeFacts(res, t, stderr.String(), runErr)
	if res.IsError && runErr == nil {
		runErr = fmt.Errorf("Antigravity 返回错误: %s", summarizeResult(res.Result))
	}
	return res, res.AntigravityDiagnostics.safeSummary(), runErr
}

func observationFromAntigravity(t *Task, res *claudeResult, runErr error) executionObservation {
	obs := executionObservation{}
	if t != nil && t.Dir != "" {
		files := nativeWorktreeRelFiles(t.Dir)
		obs.WorkspaceHasDiff = nativeWorktreeHasDiff(t.Dir, files)
	}
	if res == nil {
		obs.Incomplete = true
		obs.Reason = "missing_result"
		if runErr != nil {
			obs.FailureClass = classifyFailure(runErr.Error(), "", nil, runErr)
		}
		return obs
	}
	d := res.AntigravityDiagnostics
	obs.Complete = res.ObservationComplete
	obs.Incomplete = !res.ObservationComplete
	obs.HasTerminal = res.TerminalEvents > 0
	obs.ToolEvents = res.ToolEvents
	obs.SemanticEvents = res.SemanticEvents
	obs.ModelEvents = res.ModelEvents
	if d != nil {
		obs.NeedsInteraction = d.NeedsInteraction || res.Subtype == antigravitySubtypeNeedsInteraction
	}
	switch res.Subtype {
	case antigravitySubtypeNeedsInteraction:
		obs.NeedsInteraction = true
		obs.NecessaryDenied = true
		obs.HasTerminal = true
		obs.Reason = firstNonBlank(obs.Reason, "needs_interaction")
	case antigravitySubtypeQuota:
		obs.QuotaHit = true
		obs.HasTerminal = true
		obs.Reason = firstNonBlank(obs.Reason, "quota")
	case antigravitySubtypeAuth:
		obs.AuthFailed = true
		obs.HasTerminal = true
		obs.Reason = firstNonBlank(obs.Reason, "auth")
	case antigravitySubtypeInputTooLong:
		obs.InputTooLong = true
		obs.HasTerminal = true
		obs.Reason = firstNonBlank(obs.Reason, "input_too_long")
	case antigravitySubtypeEmptySuccess:
		obs.HasTerminal = true
		obs.Reason = firstNonBlank(obs.Reason, "empty_success")
	case antigravitySubtypeUnknownTerminal, antigravitySubtypeInvalidTerminal:
		obs.Reason = firstNonBlank(obs.Reason, res.Subtype)
		if res.Subtype == antigravitySubtypeInvalidTerminal {
			obs.Incomplete = true
			obs.Complete = false
		}
	}
	if !res.IsError && res.TerminalEvents == 1 && strings.TrimSpace(res.Result) != "" && runErr == nil {
		obs.SuccessStructure = true
		obs.HasTerminal = true
	}
	if runErr != nil && !obs.NeedsInteraction && !obs.QuotaHit && !obs.AuthFailed && !obs.InputTooLong && !obs.SuccessStructure {
		cls := classifyFailure(runErr.Error(), "", res, runErr)
		obs.FailureClass = cls
		if cls == failureTimeout || cls == failureExecutorCrash {
			obs.Reason = firstNonBlank(obs.Reason, string(cls))
		}
	}
	return obs
}

func applyAntigravityExecutionDecision(ctx context.Context, root string, cfg *Config, t *Task, via, prompt string, res *claudeResult, runErr error, lg *os.File, now time.Time) (cont, handled bool, err error) {
	if t != nil && root != "" && diskCanceled(root, t.ID) {
		return false, true, finalizeCanceled(root, t, lg)
	}
	obs := observationFromAntigravity(t, res, runErr)
	maxAttempts := 0
	if cfg != nil {
		maxAttempts = cfg.MaxAttempts
	}
	if t != nil && t.MaxAttempts > 0 {
		maxAttempts = t.MaxAttempts
	}
	attempts := 0
	if t != nil {
		attempts = t.Attempts
	}
	dec := decideExecutionOutcome(obs, executionAttemptFacts{Attempts: attempts, MaxAttempts: maxAttempts})
	if t != nil && t.LastRouteAttempt != nil {
		t.LastRouteAttempt.FailureKind = dec.Reason
		if dec.FailureClass != "" {
			t.LastRouteAttempt.FailureClass = string(dec.FailureClass)
		}
	}
	msg := dec.Reason
	if res != nil && strings.TrimSpace(res.Result) != "" {
		msg = errorSummary(res, "", runErr)
	} else if runErr != nil {
		msg = runErr.Error()
	}
	switch dec.Kind {
	case executionDecisionCancel:
		return false, true, finalizeCanceled(root, t, lg)
	case executionDecisionWaiting:
		return false, true, nil
	case executionDecisionSuccess:
		if res != nil && res.SessionID != "" {
			t.SessionID = res.SessionID
		}
		if d := antigravityResultDiagnostics(res); d != nil && d.DeniedCount > 0 && !completionVerifyOptedIn(t) {
			if res != nil {
				applyProviderResultUsage(root, t, res)
			}
			return false, true, holdNativeExecution(root, t, via, "denied_unverified", string(failureUnknown))
		}
		cont, err = finishProviderSuccess(ctx, root, cfg, t, via, prompt, res, lg, false, false, false, false, false, false, false, antigravityRunnerName)
		return cont, true, err
	case executionDecisionLimitPause:
		return false, true, pauseAntigravityLimit(root, cfg, t, res, lg, now)
	case executionDecisionRetry, executionDecisionFailed:
		if res != nil {
			applyProviderResultUsage(root, t, res)
		}
		return false, true, applyFailureDisposition(root, cfg, t, lg, msg, dec.FailureClass, now, false)
	default:
		if res != nil {
			applyProviderResultUsage(root, t, res)
			if res.SessionID != "" {
				t.SessionID = res.SessionID
			}
		}
		if dec.FailureClass == failureAuth || dec.FailureClass == failurePermission {
			return false, true, applyFailureDisposition(root, cfg, t, lg, msg, dec.FailureClass, now, false)
		}
		kind := dec.Reason
		if kind == "" {
			kind = antigravitySubtypeUnknownTerminal
		}
		if res != nil && (res.Subtype == antigravitySubtypeNeedsInteraction || res.Subtype == antigravitySubtypeEmptySuccess) {
			kind = res.Subtype
		}
		return false, true, holdNativeExecution(root, t, via, kind, string(dec.FailureClass))
	}
}

func antigravityResultDiagnostics(res *claudeResult) *antigravityDiagnostics {
	if res == nil {
		return nil
	}
	return res.AntigravityDiagnostics
}

func pauseAntigravityLimit(root string, cfg *Config, t *Task, res *claudeResult, lg *os.File, now time.Time) error {
	if res != nil {
		applyProviderResultUsage(root, t, res)
		if res.SessionID != "" {
			t.SessionID = res.SessionID
		}
	}
	until := parseResetEpoch(resultText(res), cfg, now)
	reason := "Antigravity 用量限额"
	setEngineCooldown(root, antigravityCooldownName, until, reason)
	t.Status = statusLimitPaused
	t.ResumeAtEpoch = until
	t.MidStep = t.SessionID != ""
	t.LastError = "Antigravity 车道用量限额: " + reason
	t.touch()
	if lg != nil {
		logBlock(lg, "LIMIT", fmt.Sprintf("Antigravity 车道命中限额，%s 后恢复（%s）", fmtIn(until, now), fmtClock(until)))
	}
	return finishIfStopped(persistTaskEvent(root, t, evLimitPaused, "runner:agy", statusLimitPaused, t.Step, map[string]any{
		"engine": antigravityRunnerName, "resume_at": until, "mid_step": t.MidStep,
	}))
}
