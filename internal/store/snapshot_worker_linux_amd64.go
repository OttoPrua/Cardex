package store

import (
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func verifySnapshotWorkerDescriptors() error {
	// Must run under the kernel guard and before touching any source path.
	// A mere FIFO mode would also accept a named pipe; procfs identifies
	// the anonymous pipe explicitly. Only these two FDs may emit output.
	proc, err := os.Open("/proc/self/fd")
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer proc.Close()
	for _, fd := range []int{1, 2} {
		var st unix.Stat_t
		mode, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil || mode&unix.O_ACCMODE != unix.O_WRONLY ||
			unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
			return ErrSnapshotUnavailable
		}
		target, err := os.Readlink(filepath.Join("/proc/self/fd", strconv.Itoa(fd)))
		if err != nil || target != "pipe:["+strconv.FormatUint(st.Ino, 10)+"]" {
			return ErrSnapshotUnavailable
		}
	}

	// An exec closes every inherited FD marked CLOEXEC. The runtime's own
	// epoll/event/pipe descriptors use it, so reject unknown inherited FDs
	// without closing any descriptor the Go runtime may already be using.
	entries, err := proc.ReadDir(-1)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			return ErrSnapshotUnavailable
		}
		if fd <= 2 || uintptr(fd) == proc.Fd() {
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err == unix.EBADF {
			continue // An ephemeral runtime descriptor has already been closed.
		}
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			return ErrSnapshotUnavailable
		}
	}
	return nil
}
