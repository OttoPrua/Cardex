//go:build !darwin

package sandbox

import (
	"fmt"
	"os/exec"
)

var osErrNil = fmt.Errorf("sandbox: nil command")

func wrapOS(cmd *exec.Cmd, writable []string) (*exec.Cmd, error) {
	return Skip(cmd)
}
