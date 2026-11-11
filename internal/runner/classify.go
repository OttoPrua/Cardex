package runner

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"cardex/internal/model"
)

// Patterns are ported from v1 failure_class.go and wait_error_fact.go.
// input_too_long, executor_crash, in_flight, unknown, and cancellation have
// no v2 ErrorClass and stay unclassified.
// File EPERM alone is not a sandbox refusal; require initialization context.
var (
	authClassRe       = regexp.MustCompile(`(?i)(?:\bhttp(?:/\d(?:\.\d)?)?\s+(?:status\s+)?401\b|\bhttp\s+status\s*[:=]\s*401\b|\bstatus(?:\s+code)?\s*[:=]\s*401\b|\b401\s*(?::|-)?\s*unauthorized\b|\bunauthorized\s*\(\s*401\s*\))|invalid api key|invalid or expired credentials|authentication failed|oauth token (?:has )?(?:expired|invalid|revoked)|please (?:re-?)?login|please log in again|reason\s*=\s*no auth context|请重新登录|token expired`)
	permissionClassRe = regexp.MustCompile(`(?i)403 forbidden|permission denied|access denied by (?:policy|admin|organization)|not authorized to (?:perform|access)|operation not permitted by (?:policy|admin)|(?:sandbox initialization failed|sandbox-exec: sandbox_apply):\s*operation not permitted\b|could not apply the 'workspace' sandbox profile|blocked by (?:policy|admin|organization)`)
	timeoutClassRe    = regexp.MustCompile(`(?i)步骤超时[（(]|远程步骤超时[（(]|远端步骤超时[（(]|context deadline exceeded|deadline exceeded \(`)
	quotaClassRe      = regexp.MustCompile(`(?i)usage limit|limit reached|out of extra usage|hit your (?:\w+ )?limit|limit will reset|out of usage credits|out of credits|/usage-credits|session limit|quota hit|quota exceeded|quota exhausted|用量限额|额度红线|账号限额`)
	transportClassRe  = regexp.MustCompile(`(?i)proxyconnect|proxy unreachable|i/o timeout|dial tcp|no such host|connection reset|connection refused|enotfound|provider unavailable|model unavailable|transport_failed`)
)

// ClassifyOutput maps one explicit diagnostic string onto the v2 error classes.
// It is not a transcript scanner. Callers pass a structured error field or an
// explicit diagnostic line. Text that v1 would call input_too_long,
// executor_crash, in_flight, unknown, or cancellation returns the empty class.
func ClassifyOutput(text string) model.ErrorClass {
	switch {
	case authClassRe.MatchString(text):
		return model.ErrorAuthFailed
	case permissionClassRe.MatchString(text):
		return model.ErrorPermissionDenied
	case quotaClassRe.MatchString(text):
		return model.ErrorQuotaHit
	case strings.Contains(strings.ToLower(text), "budget_limited"):
		return model.ErrorBudgetLimited
	case timeoutClassRe.MatchString(text):
		return model.ErrorTimeout
	case transportClassRe.MatchString(text):
		return model.ErrorTransport
	case strings.Contains(strings.ToLower(text), "refusal"):
		return model.ErrorRefusal
	default:
		return ""
	}
}

// ClassifyAttempt classifies from structured terminal events and explicit
// sandbox diagnostics. Exit 0 still counts when the terminal state is explicit.
// An earlier error that does not map to a class does not hide a later terminal.
// Ordinary task body, including a standalone "error:" line, is not a source.
func ClassifyAttempt(exit int, output []byte) model.ErrorClass {
	_ = exit
	objs := jsonObjects(output)
	if len(objs) == 0 {
		if msg, ok := structuredDiagnostic(output); ok {
			if cls := ClassifyOutput(msg); cls != "" {
				return cls
			}
		}
	} else {
		for _, obj := range objs {
			msg, ok := diagnosticObject(obj)
			if !ok {
				continue
			}
			if cls := ClassifyOutput(msg); cls != "" {
				return cls
			}
		}
	}
	if diag := explicitDiagnostics(output); diag != "" {
		if cls := ClassifyOutput(diag); cls != "" {
			return cls
		}
	}
	return ""
}

func structuredDiagnostic(output []byte) (string, bool) {
	if msg, ok := diagnosticFromObjects(jsonObjects(output)); ok {
		return msg, true
	}
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(trimmed, &obj) != nil {
		return "", false
	}
	return diagnosticObject(obj)
}

func jsonObjects(output []byte) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		out = append(out, obj)
	}
	return out
}

func diagnosticFromObjects(objs []map[string]json.RawMessage) (string, bool) {
	for _, obj := range objs {
		if msg, ok := diagnosticObject(obj); ok {
			return msg, true
		}
	}
	return "", false
}

func diagnosticObject(obj map[string]json.RawMessage) (string, bool) {
	switch jsonString(obj["type"]) {
	case "result":
		if !jsonBool(obj["is_error"]) {
			return "", false
		}
		msg := strings.TrimSpace(strings.Trim(jsonString(obj["subtype"])+": "+jsonString(obj["result"]), ": "))
		if msg == "" {
			msg = "result error"
		}
		return msg, true
	case "turn.failed":
		msg := nestedMessage(obj["error"])
		if msg == "" {
			msg = "turn.failed"
		}
		return msg, true
	case "error":
		msg := nestedMessage(obj["error"])
		if msg == "" {
			msg = "error"
		}
		return msg, true
	case "budget_limited":
		return "budget_limited", true
	default:
		return "", false
	}
}

func explicitDiagnostics(output []byte) string {
	var b strings.Builder
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		// Sandbox denials are a diagnostic source. A body line that merely
		// starts with "error:" is not.
		if strings.HasPrefix(line, "outside write denied:") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func jsonBool(raw json.RawMessage) bool {
	var v bool
	return json.Unmarshal(raw, &v) == nil && v
}

func nestedMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	if msg := jsonString(obj["message"]); msg != "" {
		return msg
	}
	return jsonString(obj["name"])
}
