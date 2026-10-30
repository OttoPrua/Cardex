package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"cardex/internal/model"
)

// Kimi adapts kimi-cli. v1 forbids --auto/--yolo/--plan next to --prompt.
// Write-capable auto-approve is an isolated config.toml: default_permission_mode
// auto and default_plan_mode false. ConfigHome is that directory. Build does not
// read ~/.kimi-code; the host places credentials in ConfigHome.
type Kimi struct {
	ConfigHome string
}

var _ Runner = Kimi{}

func (Kimi) Name() string            { return string(model.RunnerKimi) }
func (Kimi) Capabilities() []string  { return nil }
func (Kimi) NeedsPTY() bool          { return false }
func (Kimi) WritablePaths() []string { return userPath(".kimi-code") }

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
	const cfg = "default_permission_mode = \"auto\"\ndefault_plan_mode = false\n"
	return os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600)
}

func (Kimi) Classify(_ int, output []byte) model.ErrorClass {
	return classifyScanned(output, nil, func(text string) model.ErrorClass {
		if limitRe.MatchString(text) || engineQuotaRe.MatchString(text) || kimiQuotaRe.MatchString(text) {
			return model.ErrorQuotaHit
		}
		return ""
	})
}

// ParseUsage returns an empty Usage. v1's kimi CLI parser records no token fields.
func (Kimi) ParseUsage([]byte) model.Usage { return model.Usage{} }
