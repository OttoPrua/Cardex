package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Claude runs the claude CLI non-interactively, including an engines profile.
type Claude struct{}

func (Claude) Name() string { return string(model.RunnerClaude) }

// Capabilities is empty: v1 does not assign web or goal_mode to claude.
func (Claude) Capabilities() []string { return nil }

func (Claude) NeedsPTY() bool { return false }

func (Claude) Drive(context.Context, io.ReadWriter) error { return nil }

func (Claude) Classify(exit int, output []byte) model.ErrorClass {
	return ClassifyAttempt(exit, output)
}

func (Claude) ParseUsage(output []byte) model.Usage { return ParseOutputUsage(output) }

// WritablePaths declares the CLI home and cache, plus directories the current
// process already names in CLAUDE_CONFIG_DIR and CLAUDE_CODE_PLUGIN_CACHE_DIR.
func (Claude) WritablePaths() []string {
	return appendEnvDirs(
		nonempty(homePath(".claude"), cachePath("claude")),
		envDir("CLAUDE_CONFIG_DIR"),
		envDir("CLAUDE_CODE_PLUGIN_CACHE_DIR"),
	)
}

// Build returns the v1 non-interactive argv and, when spec.Script is an engines
// profile, the injected environment. LaunchSpec has no profile field; the host
// passes the profile JSON through Script.
func (Claude) Build(spec LaunchSpec) ([]string, []string, error) {
	args := []string{"claude", "-p", "--output-format", "json", "--dangerously-skip-permissions"}
	modelName := spec.Model
	var env []string
	// An empty script means no profile was selected. A selected document that
	// is not usable must not fall through to the default channel.
	if len(bytes.TrimSpace(spec.Script)) > 0 {
		profile, ok := ParseEngineProfile(spec.Script)
		if !ok {
			return nil, nil, errors.New("engines profile is not usable")
		}
		modelName = MapEngineModel(profile, spec.Model)
		var err error
		env, err = ApplyEngineEnv(os.Environ(), profile)
		if err != nil {
			return nil, nil, err
		}
	}
	if modelName != "" {
		args = append(args, "--model", modelName)
	}
	return args, env, nil
}
