package main

import (
	"fmt"
	"strings"
)

// Frozen Grok read-only review/crosscheck allow rules. These are the exact
// type-default strings already authorized for design-review and crosscheck.
// Mapping is identity: a supported rule is forwarded as the same --allow value.
// Unknown grammar fails closed before a product process starts.
var grokFrozenAllowRules = map[string]struct{}{
	"Read":               {},
	"Grep":               {},
	"Glob":               {},
	"Bash(git log:*)":    {},
	"Bash(git diff:*)":   {},
	"Bash(git show:*)":   {},
	"Bash(git status:*)": {},
	"Bash(ls:*)":         {},
}

type grokAllowPolicy struct {
	Requested []string `json:"requested"`
	Consumed  []string `json:"consumed"`
}

func consumeGrokFrozenAllowRules(requested []string) (grokAllowPolicy, error) {
	policy := grokAllowPolicy{
		Requested: append([]string(nil), requested...),
		Consumed:  make([]string, 0, len(requested)),
	}
	var unsupported []string
	seen := make(map[string]struct{}, len(requested))
	for _, raw := range requested {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			unsupported = append(unsupported, raw)
			continue
		}
		if rule != raw {
			// Leading/trailing whitespace is not the frozen grammar.
			unsupported = append(unsupported, raw)
			continue
		}
		if _, ok := grokFrozenAllowRules[rule]; !ok {
			unsupported = append(unsupported, rule)
			continue
		}
		if _, dup := seen[rule]; dup {
			continue
		}
		seen[rule] = struct{}{}
		policy.Consumed = append(policy.Consumed, rule)
	}
	if len(unsupported) > 0 {
		return policy, fmt.Errorf("unsupported allow-rule grammar: %s", strings.Join(unsupported, ", "))
	}
	return policy, nil
}

func grokBuildUsesNativePermissionChannel(t *Task) bool {
	return t != nil && !grokBuildWriteCapable(t)
}

func grokBuildProductArgs(cfg *Config, t *Task, model, effort, promptPath string) (args []string, acp bool, policy grokAllowPolicy, err error) {
	if t == nil {
		return nil, false, policy, fmt.Errorf("Grok invoke requires a task")
	}
	writeCapable := grokBuildWriteCapable(t)
	sandbox, permission := resolvedGrokBuildReadOnlySandbox(cfg), "plan"
	if writeCapable {
		sandbox, permission = "workspace", "auto"
	}
	if grokBuildUsesNativePermissionChannel(t) {
		policy, err = consumeGrokFrozenAllowRules(t.AllowedTools)
		if err != nil {
			return nil, true, policy, fmt.Errorf("Grok frozen allow-rule grammar unsupported (fail closed before semantic work): %w", err)
		}
		args = []string{
			"--no-auto-update",
			"--model", model,
			"--reasoning-effort", effort,
			"--sandbox", sandbox,
			"--permission-mode", permission,
		}
		for _, rule := range policy.Consumed {
			args = append(args, "--allow", rule)
		}
		args = append(args, "--no-memory", "--no-subagents", "--disable-web-search", "--verbatim",
			"agent", "--no-leader", "stdio")
		return args, true, policy, nil
	}
	args = []string{
		"--no-auto-update",
		"--model", model,
		"--reasoning-effort", effort,
		"--output-format", "streaming-json",
		"--sandbox", sandbox,
		"--permission-mode", permission,
		"--no-memory", "--no-subagents", "--disable-web-search", "--verbatim",
	}
	if writeCapable {
		args = append(args, "--no-plan")
	}
	if t.SessionID != "" {
		args = append(args, "--resume", t.SessionID)
	}
	args = append(args, "--prompt-file", promptPath)
	return args, false, policy, nil
}

func grokBuildArgvHasPrivilegeWidening(args []string) bool {
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
