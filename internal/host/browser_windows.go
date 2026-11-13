//go:build windows

package host

import (
	"os/exec"
	"strconv"
	"strings"
)

// groupHasArg reports whether process pgid has arg on its command line. On
// Windows the recorded "group" is the Chrome root pid, and Stop kills its
// whole tree with taskkill /T, so checking the root process is enough.
func groupHasArg(pgid int, arg string) bool {
	q := "(Get-CimInstance Win32_Process -Filter 'ProcessId=" + strconv.Itoa(pgid) + "').CommandLine"
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", q).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), arg)
}
