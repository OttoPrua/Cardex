package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/OttoPrua/Cardex/internal/proc"
)

// CompatibleCredentialEnv omits provider env references from ordinary children
// as well as from compatible children. The reserved prefix is always scrubbed,
// even if config is absent; configured older names are scrubbed by exact name.
// This reads only the config reference metadata, never a credential value.
func CompatibleCredentialEnv(home string, env []string) []string {
	return proc.CompatibleChildEnv(WithCompatibleCredentialEnv(context.Background(), home), env)
}

// WithCompatibleCredentialEnv binds an explicit service/home snapshot. It
// does not infer a service from cwd or a process-global CARDEX_HOME setting.
func WithCompatibleCredentialEnv(ctx context.Context, home string) context.Context {
	if strings.TrimSpace(home) == "" {
		return ctx
	}
	_, _, engines, _ := LoadEngineConfig(home)
	return proc.WithCompatibleCredentialNames(ctx, CompatibleConfigCredentialNames(engines))
}

// Reading reference names is less strict than allowing a binding. Unsupported
// or otherwise invalid API profile options must not make a known name inheritable.
func CompatibleConfigCredentialNames(engines map[string]json.RawMessage) []string {
	names := map[string]bool{}
	for _, raw := range engines {
		if !IsCompatibleEngine(raw) {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			continue
		}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				break
			}
			var field json.RawMessage
			if dec.Decode(&field) != nil {
				break
			}
			if name, ok := key.(string); ok && strings.EqualFold(name, "credential") {
				collectCredentialNames(field, names)
			}
		}
	}
	return sortedKeys(names)
}

func collectCredentialNames(raw json.RawMessage, names map[string]bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return
	}
	var envSource bool
	var candidates []string
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return
		}
		var field json.RawMessage
		if dec.Decode(&field) != nil {
			return
		}
		fieldName, ok := key.(string)
		if !ok {
			continue
		}
		switch {
		case strings.EqualFold(fieldName, "source"):
			var source string
			if json.Unmarshal(field, &source) == nil && strings.EqualFold(strings.TrimSpace(source), "env") {
				envSource = true
			}
		case strings.EqualFold(fieldName, "name"):
			var candidate string
			if json.Unmarshal(field, &candidate) == nil && envNameRe.MatchString(candidate) && !permittedChildKey(candidate) {
				candidates = append(candidates, candidate)
			}
		}
	}
	// A genuine env source anywhere in a duplicate/ambiguous record keeps
	// all of its names denied. A well-formed Keychain label is not an env ref.
	if envSource {
		for _, candidate := range candidates {
			names[strings.ToUpper(candidate)] = true
		}
	}
}
