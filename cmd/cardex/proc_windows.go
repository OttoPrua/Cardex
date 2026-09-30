//go:build windows

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var openJobObject = windowsKernel.NewProc("OpenJobObjectW")
var errWorkspaceExecutionLeaseBusy = errors.New("workspace execution lease is already held")

var createJobObject = windowsKernel.NewProc("CreateJobObjectW")
var setJobInformation = windowsKernel.NewProc("SetInformationJobObject")
var queryJobInformation = windowsKernel.NewProc("QueryInformationJobObject")
var assignJobProcess = windowsKernel.NewProc("AssignProcessToJobObject")
var terminateJobObject = windowsKernel.NewProc("TerminateJobObject")
var thread32First = windowsKernel.NewProc("Thread32First")
var thread32Next = windowsKernel.NewProc("Thread32Next")
var openThread = windowsKernel.NewProc("OpenThread")
var windowsAfterAnchorStart func(*exec.Cmd) // synthetic crash seam, before job assignment

var resumeThread = windowsKernel.NewProc("ResumeThread")

// Handles are execution resources, not another attempt ledger. Closing the runner's
// last handle kills this job's entire descendant tree, including on runner crash.
var windowsJobsMu sync.Mutex
var windowsJobs = map[int]syscall.Handle{}

type jobBasicLimits struct {
	PerProcessUserTimeLimit, PerJobUserTimeLimit int64
	LimitFlags                                   uint32
	MinimumWorkingSetSize, MaximumWorkingSetSize uintptr
	ActiveProcessLimit                           uint32
	Affinity                                     uintptr
	PriorityClass, SchedulingClass               uint32
}
type jobExtendedLimits struct {
	Basic                                                                        jobBasicLimits
	IO                                                                           [6]uint64
	ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed uintptr
}
type jobAccounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
}
type windowsThreadEntry struct {
	Size, Usage, ThreadID, OwnerProcessID uint32
	BasePriority, DeltaPriority           int32
	Flags                                 uint32
}

// The anchor runs no provider code. EOF retires it even if its owner crashes
// before assignment to the job. The provider then inherits the job atomically
// through Go's supported ParentProcess attribute (including stdio duplication).
func init() {
	if len(os.Args) == 2 && os.Args[1] == "--cardex-windows-job-anchor" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

func startWindowsJobAnchor(job syscall.Handle) (*exec.Cmd, *os.File, syscall.Handle, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, 0, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, 0, err
	}
	anchor := exec.Command(exe, "--cardex-windows-job-anchor")
	anchor.Stdin = reader
	err = anchor.Start()
	reader.Close()
	if err != nil {
		writer.Close()
		return nil, nil, 0, err
	}
	if hook := windowsAfterAnchorStart; hook != nil {
		hook(anchor)
	}
	process, err := syscall.OpenProcess(0x01c1, false, uint32(anchor.Process.Pid)) // CREATE_PROCESS|DUP_HANDLE|SET_QUOTA|TERMINATE
	if err == nil {
		ok, _, callErr := assignJobProcess.Call(uintptr(job), uintptr(process))
		if ok == 0 {
			err = fmt.Errorf("assign job anchor: %w", callErr)
		}
	}
	if err != nil {
		writer.Close()
		anchor.Process.Kill()
		anchor.Wait()
		if process != 0 {
			syscall.CloseHandle(process)
		}
		return nil, nil, 0, err
	}
	return anchor, writer, process, nil
}

func prepareTaskProcessLease(cmd *exec.Cmd, _ string, workspaceDir string) (*taskProcessLease, error) {
	if cmd.SysProcAttr != nil && (cmd.SysProcAttr.ParentProcess != 0 || cmd.SysProcAttr.Token != 0 || cmd.SysProcAttr.CreationFlags&0x01000000 != 0) {
		return nil, fmt.Errorf("execution job cannot override an existing parent, token or breakaway launch")
	}
	name, err := workspaceExecutionJobName(workspaceDir)
	if err != nil {
		return nil, err
	}
	handle, _, err := createJobObject.Call(0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, fmt.Errorf("create execution job: %w", err)
	}
	job := syscall.Handle(handle)
	if name != nil && err == syscall.Errno(183) { // ERROR_ALREADY_EXISTS: never join another writer's job.
		syscall.CloseHandle(job)
		return nil, fmt.Errorf("%w for %s", errWorkspaceExecutionLeaseBusy, workspaceDir)
	}
	limits := jobExtendedLimits{}
	limits.Basic.LimitFlags = 0x2000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE; no breakaway.
	ok, _, err := setJobInformation.Call(handle, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok == 0 {
		syscall.CloseHandle(job)
		return nil, fmt.Errorf("configure execution job: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	anchor, anchorPipe, anchorHandle, err := startWindowsJobAnchor(job)
	if err != nil {
		syscall.CloseHandle(job)
		return nil, err
	}
	cmd.SysProcAttr.ParentProcess = anchorHandle
	var retireOnce sync.Once
	retireAnchor := func() {
		retireOnce.Do(func() {
			anchorPipe.Close()
			// Only the pipe read end is inherited by the anchor; EOF is immediate.
			anchor.Wait()
			syscall.CloseHandle(anchorHandle)
		})
	}
	cmd.SysProcAttr.CreationFlags |= 0x4 // CREATE_SUSPENDED: no provider code before custody.
	closeJob := func() {
		retireAnchor()
		windowsJobsMu.Lock()
		defer windowsJobsMu.Unlock()
		if job != 0 {
			if cmd.Process != nil {
				if windowsJobs[cmd.Process.Pid] == job {
					delete(windowsJobs, cmd.Process.Pid)
				}
			}
			syscall.CloseHandle(job)
			job = 0
		}
	}
	return &taskProcessLease{abort: closeJob, cleanup: closeJob, commit: retireAnchor, finish: func(runErr error) error {
		pid := cmd.Process.Pid
		if !processGroupAlive(pid) {
			if runErr != nil {
				return fmt.Errorf("%w: %w", errProcessExecution, runErr)
			}
			return runErr
		}
		// A clean direct exit is insufficient when descendants retain the job or pipes.
		// Terminate the owned tree, but preserve this as an invalid outcome for the runner.
		_ = killProcGroup(pid)
		deadline := time.Now().Add(2 * time.Second)
		for processGroupAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		return fmt.Errorf("%w: descendants survived direct exit (process error: %v)", errProcessExecution, runErr)
	}, resume: func() error {
		pid := cmd.Process.Pid
		windowsJobsMu.Lock()
		if _, exists := windowsJobs[pid]; exists {
			windowsJobsMu.Unlock()
			return fmt.Errorf("process ID already belongs to an execution job")
		}
		windowsJobs[pid] = job
		windowsJobsMu.Unlock()
		return resumeSuspendedProcess(pid)
	}}, nil
}

func resumeSuspendedProcess(pid int) error {
	snapshot, err := syscall.CreateToolhelp32Snapshot(4, 0) // TH32CS_SNAPTHREAD
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(snapshot)
	entry := windowsThreadEntry{Size: uint32(unsafe.Sizeof(windowsThreadEntry{}))}
	ok, _, err := thread32First.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.OwnerProcessID == uint32(pid) {
			thread, _, err := openThread.Call(2, 0, uintptr(entry.ThreadID)) // THREAD_SUSPEND_RESUME
			if thread == 0 {
				return err
			}
			count, _, resumeErr := resumeThread.Call(thread)
			syscall.CloseHandle(syscall.Handle(thread))
			if count != 1 {
				return fmt.Errorf("unexpected suspended thread count %d: %v", count, resumeErr)
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, err = thread32Next.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	return fmt.Errorf("suspended primary thread unavailable: %v", err)
}

// The kernel owns the workspace lease: named job creation is atomic, and the
// object survives until every associated process is gone. Global names unify
// desktop and SSH sessions without a second on-disk attempt/lock registry.
func workspaceExecutionJobName(dir string) (*uint16, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace is not a directory: %s", dir)
	}
	id := canonicalWorkspaceID(dir)
	sum := sha256.Sum256([]byte(id))
	return syscall.UTF16PtrFromString(fmt.Sprintf(`Global\CardexWorkspace-%x`, sum))
}

func workspaceProcessResidue(dir string) bool {
	name, err := workspaceExecutionJobName(dir)
	if err != nil {
		return true
	}
	if name == nil {
		return false
	}
	handle, _, err := openJobObject.Call(4, 0, uintptr(unsafe.Pointer(name))) // JOB_OBJECT_QUERY
	if handle == 0 {
		return err != syscall.Errno(2)
	} // Only not-found proves no native owner.
	// Even an empty job belongs to its preparing/finishing owner until it closes.
	_ = syscall.CloseHandle(syscall.Handle(handle))
	return true
}

func setupProcGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 10 * time.Second
	// exec.Command without a context cannot have a Cancel callback.
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			return killProcGroup(cmd.Process.Pid)
		}
	}
}

func killProcGroup(pid int) error {
	windowsJobsMu.Lock()
	defer windowsJobsMu.Unlock()
	if job, ok := windowsJobs[pid]; ok {
		return terminateWindowsJob(job)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer p.Release()
	return p.Kill()
}

// TerminateJobObject is asynchronous. Hold exact process handles across termination
// so cancellation does not return while an owned descendant is still shutting down.
func terminateWindowsJob(job syscall.Handle) error {
	var handles []syscall.Handle
	defer func() {
		for _, h := range handles {
			syscall.CloseHandle(h)
		}
	}()
	listErr := fmt.Errorf("execution job process list exceeds observation bound")
	for capacity := 64; capacity <= 65536; capacity *= 2 {
		buf := make([]uintptr, 2+capacity)
		ok, _, err := queryJobInformation.Call(uintptr(job), 3, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))*unsafe.Sizeof(buf[0]), 0)
		if ok == 0 {
			if err == syscall.Errno(234) {
				continue
			} // ERROR_MORE_DATA
			listErr = err
			break
		}
		count := *(*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(&buf[0])) + 4))
		if uintptr(count) > (uintptr(len(buf))*unsafe.Sizeof(buf[0])-8)/unsafe.Sizeof(buf[0]) {
			listErr = fmt.Errorf("execution job process list is truncated")
			break
		}
		listErr = nil
		ids := unsafe.Slice((*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(&buf[0]))+8)), int(count))
		for _, id := range ids {
			h, err := syscall.OpenProcess(0x100000, false, uint32(id))
			if err == nil {
				handles = append(handles, h)
			} else if err != syscall.Errno(87) {
				listErr = err
			}
		}
		break
	}
	ok, _, err := terminateJobObject.Call(uintptr(job), 1)
	if ok == 0 {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for _, h := range handles {
		remaining := time.Until(deadline).Milliseconds()
		if remaining < 1 {
			remaining = 1
		}
		status, err := syscall.WaitForSingleObject(h, uint32(remaining))
		if err != nil {
			return err
		}
		if status != syscall.WAIT_OBJECT_0 {
			return fmt.Errorf("execution job termination did not finish")
		}
	}
	return listErr
}

func processGroupAlive(pid int) bool {
	windowsJobsMu.Lock()
	defer windowsJobsMu.Unlock()
	job, ok := windowsJobs[pid]
	// Unknown process trees cannot authorize a next-engine transition.
	if !ok {
		return processAlive(pid)
	}
	var accounting jobAccounting
	success, _, _ := queryJobInformation.Call(uintptr(job), 1, uintptr(unsafe.Pointer(&accounting)), unsafe.Sizeof(accounting), 0)
	if success == 0 {
		return true
	}
	if accounting.ActiveProcesses != 0 {
		return true
	}
	return false
}

// Named workspace jobs protect native Cardex children. Automatic policy fallback
// remains disabled until its separate Windows transition contract is accepted.
func policyFallbackProcessProofSupported() bool { return false }
