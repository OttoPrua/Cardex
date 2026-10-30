package runner

import (
	"regexp"
	"strings"

	"cardex/internal/model"
)

// Patterns are ported from v1 failure_class.go and wait_error_fact.go.
// input_too_long, executor_crash, in_flight, unknown, and cancellation have
// no v2 ErrorClass and stay unclassified.
var (
	authClassRe       = regexp.MustCompile(`(?i)(?:\bhttp(?:/\d(?:\.\d)?)?\s+(?:status\s+)?401\b|\bhttp\s+status\s*[:=]\s*401\b|\bstatus(?:\s+code)?\s*[:=]\s*401\b|\b401\s*(?::|-)?\s*unauthorized\b|\bunauthorized\s*\(\s*401\s*\))|invalid api key|invalid or expired credentials|authentication failed|oauth token (?:has )?(?:expired|invalid|revoked)|please (?:re-?)?login|please log in again|reason\s*=\s*no auth context|请重新登录|token expired`)
	permissionClassRe = regexp.MustCompile(`(?i)403 forbidden|permission denied|access denied by (?:policy|admin|organization)|not authorized to (?:perform|access)|operation not permitted by (?:policy|admin)|blocked by (?:policy|admin|organization)`)
	timeoutClassRe    = regexp.MustCompile(`(?i)步骤超时[（(]|远程步骤超时[（(]|远端步骤超时[（(]|context deadline exceeded|deadline exceeded \(`)
	quotaClassRe      = regexp.MustCompile(`(?i)usage limit|limit reached|out of extra usage|hit your (?:\w+ )?limit|limit will reset|out of usage credits|out of credits|/usage-credits|session limit|quota hit|quota exceeded|quota exhausted|用量限额|额度红线|账号限额`)
	transportClassRe  = regexp.MustCompile(`(?i)proxyconnect|proxy unreachable|i/o timeout|dial tcp|no such host|connection reset|connection refused|enotfound|provider unavailable|model unavailable|transport_failed`)
)

// ClassifyOutput maps executor text onto the v2 error classes.
// Text that v1 would call input_too_long, executor_crash, in_flight, unknown,
// or cancellation returns the empty class.
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
