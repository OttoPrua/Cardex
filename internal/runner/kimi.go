package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Kimi adapts kimi-cli. v1 forbids --auto/--yolo/--plan next to --prompt.
// Write-capable auto-approve is an isolated config.toml: default_permission_mode
// auto and default_plan_mode false. ConfigHome is that directory. Build does not
// read ~/.kimi-code; the host places credentials in ConfigHome.
type Kimi struct {
	ConfigHome string
}

var _ Runner = Kimi{}

func (Kimi) Name() string           { return string(model.RunnerKimi) }
func (Kimi) Capabilities() []string { return nil }
func (Kimi) NeedsPTY() bool         { return false }
func (k Kimi) WritablePaths() []string {
	return appendEnvDirs(
		userPath(".kimi-code"),
		envDir("KIMI_CODE_HOME"),
		envDir("KIMI_CODE_CACHE_DIR"),
		k.ConfigHome,
	)
}

func (Kimi) Drive(context.Context, io.ReadWriter) error { return nil }

func (k Kimi) Build(spec LaunchSpec) ([]string, []string, error) {
	body, err := promptArg(spec.PromptPath)
	if err != nil {
		return nil, nil, err
	}
	home, err := k.kimiConfigHome()
	if err != nil {
		return nil, nil, err
	}
	if err := writeKimiAutoConfig(home); err != nil {
		return nil, nil, err
	}
	args := []string{
		"kimi",
		"--model", spec.Model,
		"--prompt", body,
		"--output-format", "stream-json",
	}
	env := []string{
		"KIMI_CODE_NO_AUTO_UPDATE=1",
		"KIMI_CODE_HOME=" + home,
	}
	return args, env, nil
}

func (k Kimi) kimiConfigHome() (string, error) {
	if strings.TrimSpace(k.ConfigHome) != "" {
		return k.ConfigHome, nil
	}
	return os.MkdirTemp("", "cardex-kimi-")
}

func writeKimiAutoConfig(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	path := filepath.Join(home, "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// v1 overrideKimiTopLevelConfig: drop only these two top-level keys and
	// prepend them. Table bodies, including provider tables, stay as written.
	rendered := overlayKimiPermissionConfig(string(raw))
	return os.WriteFile(path, []byte(rendered), 0o600)
}

func overlayKimiPermissionConfig(raw string) string {
	values := map[string]string{
		"default_permission_mode": `"auto"`,
		"default_plan_mode":       "false",
	}
	lines := strings.Split(raw, "\n")
	inTable := false
	kept := make([]string, 0, len(lines)+len(values))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inTable = true
		}
		if !inTable {
			if key, ok := kimiAssignKey(trimmed); ok {
				if _, drop := values[key]; drop {
					continue
				}
			}
		}
		kept = append(kept, line)
	}
	prefix := []string{
		`default_permission_mode = "auto"`,
		"default_plan_mode = false",
		"",
	}
	return strings.Join(append(prefix, kept...), "\n")
}

// kimiAssignKey is the top-level key of one assignment. Whitespace between the
// key and "=" , including a tab, still names that key.
func kimiAssignKey(line string) (string, bool) {
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	return key, true
}

func (Kimi) Classify(_ int, output []byte) model.ErrorClass {
	return classifyScanned(output, "", nil, func(text string) model.ErrorClass {
		if limitRe.MatchString(text) || engineQuotaRe.MatchString(text) || kimiQuotaRe.MatchString(text) {
			return model.ErrorQuotaHit
		}
		return ""
	})
}

// ParseUsage returns an empty Usage. v1's kimi CLI parser records no token fields.
func (Kimi) ParseUsage([]byte) model.Usage { return model.Usage{} }
