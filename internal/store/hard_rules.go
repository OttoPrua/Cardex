package store

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func normalizeHardRules(rules []string) ([]string, error) {
	const maxRules, maxRuleBytes = 16, 512
	out := make([]string, 0, min(len(rules), maxRules))
	seen := make(map[string]bool)
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "" || seen[rule] {
			continue
		}
		if len(rule) > maxRuleBytes || !utf8.ValidString(rule) || strings.IndexFunc(rule, func(r rune) bool {
			return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
		}) != -1 {
			return nil, fmt.Errorf("hard rules must be non-empty single lines of at most %d UTF-8 bytes", maxRuleBytes)
		}
		if len(out) == maxRules {
			return nil, fmt.Errorf("at most %d distinct hard rules per scope", maxRules)
		}
		seen[rule] = true
		out = append(out, rule)
	}
	return out, nil
}
