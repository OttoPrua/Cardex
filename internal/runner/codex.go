package runner

import (
	"context"
	"io"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Codex runs codex exec inside the Cardex seatbelt with its own sandbox off.
type Codex struct{}

func (Codex) Name() string { return string(model.RunnerCodex) }

// Capabilities is empty: v1 does not assign web or goal_mode to codex.
func (Codex) Capabilities() []string { return nil }

func (Codex) NeedsPTY() bool { return false }

func (Codex) Drive(context.Context, io.ReadWriter) error { return nil }

func (Codex) Classify(exit int, output []byte) model.ErrorClass {
	return ClassifyAttempt(exit, output)
}

func (Codex) ParseUsage(output []byte) model.Usage { return ParseOutputUsage(output) }

// WritablePaths declares the CLI home and cache. It does not read them.
func (Codex) WritablePaths() []string {
	return nonempty(homePath(".codex"), cachePath("codex"))
}

// Build returns the non-interactive codex exec argv for a write run.
//
// Cardex already runs every executor inside its own seatbelt profile (writes
// limited to the worktree, CLI home, and temp dirs). macOS refuses to apply a
// second seatbelt inside a restricted one (sandbox_apply: Operation not
// permitted), so codex's own workspace-write sandbox fails every command.
// codex therefore runs with its sandbox off and Cardex's profile as the one
// boundary, the same as claude, agy, and grok (--sandbox none).
func (Codex) Build(spec LaunchSpec) ([]string, []string, error) {
	args := []string{"codex", "exec", "--sandbox", "danger-full-access", "--skip-git-repo-check", "--color", "never", "--json"}
	if spec.WorkDir != "" {
		args = append(args, "-C", spec.WorkDir)
	}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	return args, nil, nil
}
