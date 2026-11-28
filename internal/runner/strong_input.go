package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/OttoPrua/Cardex/internal/model"
)

var ErrStrongIsolation = errors.New("strong-thinking runner isolation not verified")
var ErrStrongTransport = errors.New("strong-thinking runner file transport unavailable")

// StrongInputCapability describes observed capability, not wishes or a
// runner's prompt-level claim. There is no production strong launch in ST1.
type StrongInputCapability struct {
	Adapter, ArgvVersion, Evidence                                                       string
	FreshProcess, NoSession, NoMemoryOrSubagents, FileInput, HardFilesystem, HardNetwork bool
	MaxBytes                                                                             int
}

// StrongInputCapabilityFor reports only verified provisions. A normal CLI
// adapter's flags or platform default sandbox do not prove hard isolation.
func StrongInputCapabilityFor(r Runner) StrongInputCapability {
	// None of the production adapters has verified the entire boundary.
	// In particular, a read-only worktree on macOS does not deny external reads
	// or network. Do not turn a generic runner.Capabilities value into proof.
	var adapter string
	switch r.(type) {
	case Claude:
		adapter = "claude"
	case Codex:
		adapter = "codex"
	case OpenCode:
		adapter = "opencode"
	case Grok:
		adapter = "grok"
	case Cursor:
		adapter = "cursor"
	case Kimi:
		adapter = "kimi"
	case AGY:
		adapter = "agy"
	case Fake:
		adapter = "fake"
	default:
		adapter = "unknown"
	}
	return StrongInputCapability{Adapter: adapter, Evidence: "no verified hard filesystem/network isolation or qualified strong argv"}
}

// PrepareStrongInput checks capability and material size before file creation
// or process build/start. The returned private file is intended for an argv
// path, never literal contents. No existing LaunchSpec or defaults change.
func PrepareStrongInput(ctx context.Context, r Runner, body []byte) (string, func(), error) {
	return prepareStrongInput(ctx, StrongInputCapabilityFor(r), body)
}

func prepareStrongInput(ctx context.Context, capability StrongInputCapability, body []byte) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if !capability.FreshProcess || !capability.NoSession || !capability.NoMemoryOrSubagents ||
		!capability.HardFilesystem || !capability.HardNetwork {
		return "", nil, ErrStrongIsolation
	}
	if !capability.FileInput || capability.ArgvVersion == "" || len(body) == 0 || len(body) > capability.MaxBytes || len(body) > model.StrongBundleMax {
		return "", nil, ErrStrongTransport
	}
	dir, err := os.MkdirTemp("", "cardex-strong-input-")
	if err != nil {
		return "", nil, fmt.Errorf("strong-thinking input staging: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
