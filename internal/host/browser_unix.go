//go:build !windows

package host

import (
	"os/exec"
	"strconv"
	"strings"
)

// groupHasArg reports whether a process in group pgid has arg on its command
// line. `ps -g` means different things on macOS and Linux, so list every
// process with its pgid and filter here; -ww keeps long Chrome args whole.
func groupHasArg(pgid int, arg string) bool {
	out, err := exec.Command("ps", "-A", "-ww", "-o", "pgid=,args=").Output()
	if err != nil {
		return false
	}
	want := strconv.Itoa(pgid)
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == want && strings.Contains(line, arg) {
			return true
		}
	}
	return false
}
