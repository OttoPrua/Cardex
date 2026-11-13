//go:build windows

package host

import (
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// osSignal maps Unix signals onto Windows. Signal 0 probes liveness. Any other
// signal terminates: a negative pid kills the whole process tree via taskkill,
// a positive pid terminates that one process. Windows has no SIGTERM for
// console children: SIGTERM/SIGINT to a single process sends CTRL_BREAK to its
// console process group when it has one, else it is a hard stop.
func osSignal(pid int, sig syscall.Signal) error {
	if pid < 0 {
		if err, ok := signalJob(-pid, sig); ok {
			return err
		}
	}
	if sig == 0 {
		if pid < 0 {
			pid = -pid
		}
		return probe(pid)
	}
	if pid < 0 {
		out, err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(-pid)).CombinedOutput()
		if err != nil {
			if probe(-pid) != nil {
				return syscall.ESRCH
			}
			return &exec.Error{Name: "taskkill " + string(out), Err: err}
		}
		return nil
	}
	if sig == syscall.SIGTERM || sig == syscall.SIGINT {
		// A child started with proc.NewGroup leads its own console process
		// group: CTRL_BREAK reaches it as os.Interrupt, so it can shut down
		// cleanly. Without a shared console this fails and we terminate.
		if probe(pid) != nil {
			return syscall.ESRCH
		}
		if windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid)) == nil {
			return nil
		}
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return syscall.ESRCH
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

const stillActive = 259

func probe(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_ACCESS_DENIED {
			return syscall.EPERM
		}
		return syscall.ESRCH
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return err
	}
	if code != stillActive {
		return syscall.ESRCH
	}
	return nil
}

// Windows has no process groups to protect; refuseTarget still blocks pid<=1
// and this process.
func ownGroup() int          { return -1 }
func inOurGroup(id int) bool { return false }
