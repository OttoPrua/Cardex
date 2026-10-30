package runner

import (
	"context"
	"io"

	"cardex/internal/model"
)

// Codex runs codex exec. A normal write run uses --sandbox workspace-write.
type Codex struct{}

func (Codex) Name() string { return string(model.RunnerCodex) }

// Capabilities is empty: v1 does not assign web or goal_mode to codex.
func (Codex) Capabilities() []string { return nil }

func (Codex) NeedsPTY() bool { return false }

func (Codex) Drive(context.Context, io.ReadWriter) error { return nil }

func (Codex) Classify(exit int, output []byte) model.ErrorClass {
	return ClassifyOutput(string(output))
}

func (Codex) ParseUsage(output []byte) model.Usage { return ParseOutputUsage(output) }

// WritablePaths declares the CLI home and cache. It does not read them.
func (Codex) WritablePaths() []string {
	return nonempty(homePath(".codex"), cachePath("codex"))
}

// Build returns the v1 non-interactive codex exec argv for a write run.
func (Codex) Build(spec LaunchSpec) ([]string, []string, error) {
	args := []string{"codex", "exec", "--sandbox", "workspace-write", "--skip-git-repo-check", "--color", "never"}
	if spec.WorkDir != "" {
		args = append(args, "-C", spec.WorkDir)
	}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	return args, nil, nil
}
