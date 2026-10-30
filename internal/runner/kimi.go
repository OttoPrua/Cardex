package runner

import (
	"context"
	"io"

	"cardex/internal/model"
)

// Kimi adapts kimi-cli. v1 forbids --auto/--yolo/--plan next to --prompt;
// write-capable auto-approve lives in an isolated config.toml, which this
// adapter does not read or write.
type Kimi struct{}

var _ Runner = Kimi{}

func (Kimi) Name() string            { return string(model.RunnerKimi) }
func (Kimi) Capabilities() []string  { return nil }
func (Kimi) NeedsPTY() bool          { return false }
func (Kimi) WritablePaths() []string { return userPath(".kimi-code") }

func (Kimi) Drive(context.Context, io.ReadWriter) error { return nil }

func (Kimi) Build(spec LaunchSpec) ([]string, []string, error) {
	args := []string{
		"kimi",
		"--model", spec.Model,
		"--prompt", spec.PromptPath,
		"--output-format", "stream-json",
	}
	return args, []string{"KIMI_CODE_NO_AUTO_UPDATE=1"}, nil
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
