package runner

import (
	"context"
	"io"
	"os"

	"cardex/internal/model"
)

// Claude runs the claude CLI non-interactively, including an engines profile.
type Claude struct{}

func (Claude) Name() string { return string(model.RunnerClaude) }

// Capabilities is empty: v1 does not assign web or goal_mode to claude.
func (Claude) Capabilities() []string { return nil }

func (Claude) NeedsPTY() bool { return false }

func (Claude) Drive(context.Context, io.ReadWriter) error { return nil }

func (Claude) Classify(exit int, output []byte) model.ErrorClass {
	return ClassifyOutput(string(output))
}

func (Claude) ParseUsage(output []byte) model.Usage { return ParseOutputUsage(output) }

// WritablePaths declares the CLI home and cache. It does not read them.
func (Claude) WritablePaths() []string {
	return nonempty(homePath(".claude"), cachePath("claude"))
}

// Build returns the v1 non-interactive argv and, when spec.Script is an engines
// profile, the injected environment. LaunchSpec has no profile field; the host
// passes the profile JSON through Script.
func (Claude) Build(spec LaunchSpec) ([]string, []string, error) {
	args := []string{"claude", "-p", "--output-format", "json", "--dangerously-skip-permissions"}
	profile, ok := ParseEngineProfile(spec.Script)
	modelName := spec.Model
	if ok {
		modelName = MapEngineModel(profile, spec.Model)
	}
	if modelName != "" {
		args = append(args, "--model", modelName)
	}
	var env []string
	var err error
	if ok {
		env, err = ApplyEngineEnv(os.Environ(), profile)
		if err != nil {
			return nil, nil, err
		}
	}
	return args, env, nil
}
