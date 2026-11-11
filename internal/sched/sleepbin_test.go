package sched

import "os/exec"

// sleepBin is /bin/sleep on Unix, else whatever sleep is on PATH (Git for
// Windows ships one in usr\bin).
func sleepBin() string {
	if p, err := exec.LookPath("/bin/sleep"); err == nil {
		return p
	}
	if p, err := exec.LookPath("sleep"); err == nil {
		return p
	}
	return "/bin/sleep"
}
