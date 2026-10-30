package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// EngineProfile is a claude engines profile ported from v1 engines.go.
// Secrets are resolved into the child environment and are not returned elsewhere.
type EngineProfile struct {
	BaseURL      string            `json:"base_url"`
	AuthEnv      string            `json:"auth_env"`
	AuthVar      string            `json:"auth_var"`
	AuthFile     string            `json:"auth_file"`
	AuthValue    string            `json:"auth_value"`
	Models       map[string]string `json:"models"`
	ExtraEnv     map[string]string `json:"extra_env"`
	DefaultModel string            `json:"default_model"`
}

// managedEnvKeys are stripped before a profile is injected so one profile
// cannot leave another's ANTHROPIC_* variables in place.
var managedEnvKeys = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

var engineTierOrder = []string{"fable", "opus", "sonnet", "haiku"}

var tierEnvKeys = map[string]string{
	"fable":  "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"opus":   "ANTHROPIC_DEFAULT_OPUS_MODEL",
	"sonnet": "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"haiku":  "ANTHROPIC_DEFAULT_HAIKU_MODEL",
}

// EnginePresets are the v1 built-in profiles. They carry endpoints, not secrets.
func EnginePresets() map[string]EngineProfile {
	return map[string]EngineProfile{
		"kimi": {
			BaseURL: "https://api.kimi.com/coding/",
			AuthEnv: "KIMI_API_KEY",
			AuthVar: "ANTHROPIC_API_KEY",
			Models:  map[string]string{"opus": "k3", "sonnet": "kimi-for-coding", "haiku": "kimi-for-coding"},
		},
		"glm-cn": {
			BaseURL:  "https://open.bigmodel.cn/api/anthropic",
			AuthEnv:  "GLM_API_KEY",
			Models:   map[string]string{"opus": "glm-5.2", "sonnet": "glm-5.2", "haiku": "glm-4.5-air"},
			ExtraEnv: map[string]string{"API_TIMEOUT_MS": "3000000"},
		},
		"glm-global": {
			BaseURL:  "https://api.z.ai/api/anthropic",
			AuthEnv:  "GLM_API_KEY",
			Models:   map[string]string{"opus": "glm-5.2", "sonnet": "glm-5.2", "haiku": "glm-4.5-air"},
			ExtraEnv: map[string]string{"API_TIMEOUT_MS": "3000000"},
		},
		"minimax-cn": {
			BaseURL: "https://api.minimaxi.com/anthropic",
			AuthEnv: "MINIMAX_API_KEY",
			Models:  map[string]string{"opus": "MiniMax-M3", "sonnet": "MiniMax-M3", "haiku": "MiniMax-M3"},
		},
		"minimax-global": {
			BaseURL: "https://api.minimax.io/anthropic",
			AuthEnv: "MINIMAX_API_KEY",
			Models:  map[string]string{"opus": "MiniMax-M3", "sonnet": "MiniMax-M3", "haiku": "MiniMax-M3"},
		},
		"mimo": {
			BaseURL: "https://api.xiaomimimo.com/anthropic",
			AuthEnv: "MIMO_API_KEY",
			Models:  map[string]string{"opus": "mimo-v2.5-pro", "sonnet": "mimo-v2.5-pro", "haiku": "mimo-v2-flash"},
		},
		"opencode-go": {
			BaseURL: "https://opencode.ai/zen/go/v1",
			AuthEnv: "OPENCODE_API_KEY",
			Models:  map[string]string{"opus": "kimi-k3", "sonnet": "glm-5.2", "haiku": "deepseek-v4-flash"},
		},
		"ollama": {
			BaseURL: "https://ollama.com",
			AuthEnv: "OLLAMA_API_KEY",
			Models:  map[string]string{"opus": "glm-4.7:cloud", "sonnet": "glm-4.7:cloud", "haiku": "minimax-m2.1:cloud"},
		},
	}
}

// EnginePreset returns one built-in profile.
func EnginePreset(name string) (EngineProfile, bool) {
	p, ok := EnginePresets()[name]
	return p, ok
}

// ApplyEngineEnv injects profile into base and drops managed ANTHROPIC_* keys
// that the profile does not set. The auth secret is placed only in the returned
// environment.
func ApplyEngineEnv(base []string, profile EngineProfile) ([]string, error) {
	token, err := resolveEngineAuth(profile)
	if err != nil {
		return nil, err
	}
	drop := map[string]bool{}
	for _, k := range managedEnvKeys {
		drop[k] = true
	}
	for k := range profile.ExtraEnv {
		drop[k] = true
	}
	var env []string
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 && drop[kv[:i]] {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "ANTHROPIC_BASE_URL="+strings.TrimSpace(profile.BaseURL))
	env = append(env, engineAuthVar(profile)+"="+token)
	for _, tier := range engineTierOrder {
		model := profile.Models[tier]
		if model == "" {
			continue
		}
		env = append(env, tierEnvKeys[tier]+"="+model)
	}
	if m := profile.Models["haiku"]; m != "" {
		env = append(env, "CLAUDE_CODE_SUBAGENT_MODEL="+m)
	}
	extraKeys := make([]string, 0, len(profile.ExtraEnv))
	for k := range profile.ExtraEnv {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		env = append(env, k+"="+profile.ExtraEnv[k])
	}
	return env, nil
}

func resolveEngineAuth(p EngineProfile) (string, error) {
	if p.AuthEnv != "" {
		if v := strings.TrimSpace(os.Getenv(p.AuthEnv)); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("auth_env %s is empty", p.AuthEnv)
	}
	if p.AuthFile != "" {
		data, err := os.ReadFile(p.AuthFile)
		if err != nil {
			return "", fmt.Errorf("read auth_file: %w", err)
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("auth_file %s is empty", p.AuthFile)
	}
	if v := strings.TrimSpace(p.AuthValue); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("engine profile has no auth")
}

func engineAuthVar(p EngineProfile) string {
	if p.AuthVar != "" {
		return p.AuthVar
	}
	return "ANTHROPIC_AUTH_TOKEN"
}

// MapEngineModel maps a tier alias through the profile. A native model id is unchanged.
func MapEngineModel(profile EngineProfile, taskModel string) string {
	if taskModel == "" {
		return profile.DefaultModel
	}
	alias := modelAlias(taskModel)
	if alias == "" {
		return taskModel
	}
	start := 0
	for i, a := range engineTierOrder {
		if a == alias {
			start = i
			break
		}
	}
	for _, a := range engineTierOrder[start:] {
		if m := profile.Models[a]; m != "" {
			return m
		}
	}
	if profile.DefaultModel != "" {
		return profile.DefaultModel
	}
	return taskModel
}

func modelAlias(model string) string {
	lm := strings.ToLower(model)
	for _, a := range engineTierOrder {
		if strings.Contains(lm, a) {
			return a
		}
	}
	return ""
}

// ParseEngineProfile decodes a profile document. An empty buffer is not a profile.
func ParseEngineProfile(raw []byte) (EngineProfile, bool) {
	if len(bytesTrim(raw)) == 0 {
		return EngineProfile{}, false
	}
	var p EngineProfile
	if json.Unmarshal(raw, &p) != nil {
		return EngineProfile{}, false
	}
	if strings.TrimSpace(p.BaseURL) == "" && p.AuthEnv == "" && p.AuthValue == "" && p.AuthFile == "" && len(p.Models) == 0 {
		return EngineProfile{}, false
	}
	return p, true
}

func bytesTrim(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}
