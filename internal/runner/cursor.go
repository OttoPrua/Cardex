package runner

import (
	"context"
	"fmt"
	"io"

	"cardex/internal/model"
)

// Cursor adapts the Cursor Agent CLI. v1's command name is cursor-agent.
type Cursor struct{}

var _ Runner = Cursor{}

func (Cursor) Name() string            { return string(model.RunnerCursor) }
func (Cursor) Capabilities() []string  { return nil }
func (Cursor) NeedsPTY() bool          { return false }
func (Cursor) WritablePaths() []string { return userPath(".cursor") }

func (Cursor) Drive(context.Context, io.ReadWriter) error { return nil }

// Build is v1 invokeCursor for a write-capable sequence: --sandbox enabled,
// no --mode ask, and no --auto-review / --force / --yolo.
func (Cursor) Build(spec LaunchSpec) ([]string, []string, error) {
	body, err := promptArg(spec.PromptPath)
	if err != nil {
		return nil, nil, err
	}
	args := []string{
		"cursor-agent",
		"--print", "--trust", "--output-format", "stream-json",
		"--model", spec.Model, "--workspace", spec.WorkDir, "--sandbox", "enabled",
		body,
	}
	if cursorPrivilegeWidening(args) {
		return nil, nil, fmt.Errorf("cursor: privilege-widening flags")
	}
	return args, nil, nil
}

func cursorPrivilegeWidening(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--auto-review", "--force", "--yolo":
			return true
		}
	}
	return false
}

func (Cursor) Classify(_ int, output []byte) model.ErrorClass {
	return classifyScanned(output, "", cursorQuotaRe, func(text string) model.ErrorClass {
		if cursorPolicyRe.MatchString(text) {
			return model.ErrorPermissionDenied
		}
		return ""
	})
}

func (Cursor) ParseUsage(output []byte) model.Usage {
	return parseUsageAliases(output, usageSourceCursorResult, cursorUsageAliases)
}
