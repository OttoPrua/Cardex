//go:build windows

package runner

import (
	"os"

	"golang.org/x/sys/windows"
)

// stdoutIsTTY reports whether stdout is a console (a ConPTY counts).
func stdoutIsTTY() bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(os.Stdout.Fd()), &mode) == nil
}
