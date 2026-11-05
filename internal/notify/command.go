package notify

import (
	"context"
	"fmt"
	"os/exec"
)

// AcceptSubscriptionID reports whether id matches the hermes subscription token.
// The pattern lives with the hermes adapter; callers use this instead of a new config key.
func AcceptSubscriptionID(id string) bool {
	return validSubscriptionID(id)
}

// CommandExec runs argv in a child process. ctx cancels the child.
// Signaling a process stays in internal/host/signal.go.
func CommandExec(ctx context.Context, argv []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return fmt.Errorf("notify: empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	return cmd.Run()
}
