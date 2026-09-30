//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
)

const syscallOpenNonblock = 0

func runHostedGrokGoal(root string, cfg *Config, wf *WorkflowRecord, t *Task, ctx context.Context, args []string, grokHome, contractPath, digest string) error {
	return fmt.Errorf("%s: hosted PTY Goal is not supported on windows", goalFailControlUnsupported)
}

func injectPTYMaster(master *os.File, line string) error {
	return fmt.Errorf("%s: no PTY master on windows", goalFailPTYMissing)
}
