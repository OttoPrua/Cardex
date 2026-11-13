//go:build windows

package host

import (
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no process groups. Each executor is put in a named job object
// right after it starts; its descendants inherit the job even after the
// executor itself exits, so a deadline, a cancel or reconcile can still end
// the whole tree. taskkill /T cannot: it walks parent pids, and an orphan's
// parent is gone.

var (
	jobsMu sync.Mutex
	// jobs keeps this host's job handles open for its lifetime. Processes in a
	// job also hold it, so the name still resolves after the host dies.
	jobs = map[int]windows.Handle{}

	procOpenJobObjectW = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW")
)

const jobObjectAllAccess = 0x1F001F

func jobName(pid int) *uint16 {
	p, _ := windows.UTF16PtrFromString(`Local\cardex-exec-` + strconv.Itoa(pid))
	return p
}

// joinGroup creates the executor's job and assigns pid to it. A failure leaves
// the taskkill tree walk as the only way to stop the group.
func joinGroup(pid int) error {
	if pid <= 1 {
		return nil
	}
	job, err := windows.CreateJobObject(nil, jobName(pid))
	if err != nil {
		return err
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	jobsMu.Lock()
	if old, ok := jobs[pid]; ok {
		_ = windows.CloseHandle(old)
	}
	jobs[pid] = job
	jobsMu.Unlock()
	return nil
}

func openJob(pid int) (windows.Handle, bool) {
	r, _, _ := procOpenJobObjectW.Call(jobObjectAllAccess, 0, uintptr(unsafe.Pointer(jobName(pid))))
	if r == 0 {
		return 0, false
	}
	return windows.Handle(r), true
}

type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// signalJob handles a group signal through pid's job. ok is false when pid
// has no job, so the caller falls back to the process tree.
func signalJob(pid int, sig syscall.Signal) (err error, ok bool) {
	job, found := openJob(pid)
	if !found {
		return nil, false
	}
	defer windows.CloseHandle(job)
	if sig == 0 {
		var info jobAccounting
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
			return err, true
		}
		if info.ActiveProcesses == 0 {
			return syscall.ESRCH, true
		}
		return nil, true
	}
	if err := windows.TerminateJobObject(job, 1); err != nil {
		return err, true
	}
	return nil, true
}
