package runner

import (
	"fmt"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Cursor adapts the Cursor Agent CLI. v1's command name is cursor-agent.
type Cursor struct {
	// GoalMode launches the interactive TUI and drives its /goal command.
	GoalMode bool
	Prompt   string
	WorkDir  string
	// DataDir overrides CURSOR_DATA_DIR (tests).
	DataDir string
}

var _ Runner = Cursor{}

func (Cursor) Name() string           { return string(model.RunnerCursor) }
func (Cursor) Capabilities() []string { return []string{"goal_mode"} }
func (c Cursor) NeedsPTY() bool       { return c.GoalMode }
func (Cursor) WritablePaths() []string {
	return appendEnvDirs(
		userPath(".cursor"),
		envDir("CURSOR_CONFIG_DIR"),
		envDir("CURSOR_DATA_DIR"),
	)
}

// Build is v1 invokeCursor for a write-capable sequence: --sandbox enabled,
// no --mode ask, and no --auto-review / --force / --yolo.
func (c Cursor) Build(spec LaunchSpec) ([]string, []string, error) {
	if c.GoalMode {
		return c.buildGoal(spec)
	}
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
	return privilegeWidening(args, cursorPrivilegeFlag, true, false)
}

func cursorPrivilegeFlag(arg string) bool {
	switch arg {
	case "--auto-review", "--force", "--yolo":
		return true
	default:
		return false
	}
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
