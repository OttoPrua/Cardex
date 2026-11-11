package runner

import (
	"context"
	"io"
	"os"

	"cardex/internal/model"
)

// OpenCode runs opencode run in non-interactive auto-allow mode.
type OpenCode struct{}

func (OpenCode) Name() string { return string(model.RunnerOpenCode) }

// Capabilities is empty: v1 does not assign web or goal_mode to opencode.
func (OpenCode) Capabilities() []string { return nil }

func (OpenCode) NeedsPTY() bool { return false }

func (OpenCode) Drive(context.Context, io.ReadWriter) error { return nil }

func (OpenCode) Classify(exit int, output []byte) model.ErrorClass {
	return ClassifyAttempt(exit, output)
}

func (OpenCode) ParseUsage(output []byte) model.Usage { return ParseOutputUsage(output) }

// WritablePaths declares the CLI home and cache, plus the parent of a non-empty
// OPENCODE_DB already named by the current process.
func (OpenCode) WritablePaths() []string {
	return appendEnvDirs(
		nonempty(
			homePath(".local", "share", "opencode"),
			homePath(".config", "opencode"),
			cachePath("opencode"),
		),
		envParent("OPENCODE_DB"),
	)
}

// Build returns the v1 non-interactive argv, including --auto inside the sandbox.
func (OpenCode) Build(spec LaunchSpec) ([]string, []string, error) {
	args := []string{"opencode", "run", "--pure", "--format", "json", "--auto"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.WorkDir != "" {
		args = append(args, "--dir", spec.WorkDir)
	}
	// v1 passes the prompt text as the positional message. opencode run does
	// not read that message from stdin.
	if spec.PromptPath != "" {
		body, err := os.ReadFile(spec.PromptPath)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, string(body))
	}
	return args, nil, nil
}
