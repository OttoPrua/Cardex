package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	attemptDurationName = "hard-duration"
	executorGroupName   = "executor.pgid"
)

// attemptDeadline reads this attempt's wall-clock limit.
// model.Budget has no duration field; the dispatcher writes the text file
// until that field exists. Token counts are not durations.
func attemptDeadline(attemptDir string) (time.Duration, string) {
	data, err := os.ReadFile(filepath.Join(attemptDir, attemptDurationName))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "missing: budget hard duration is not on the attempt"
		}
		return 0, "unparseable: " + err.Error()
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, "missing: budget hard duration is empty"
	}
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 {
		return 0, "unparseable: budget hard duration " + strconv.Quote(text)
	}
	return d, ""
}

func persistExecutorGroup(attemptDir string, pgid int) error {
	path := filepath.Join(attemptDir, executorGroupName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(strconv.Itoa(pgid) + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if serr != nil {
		return serr
	}
	return cerr
}

func readExecutorGroup(home, attemptID string) int {
	data, err := os.ReadFile(filepath.Join(home, "attempts", attemptID, executorGroupName))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n <= 1 {
		return 0
	}
	return n
}

func processGroupAlive(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// armGroup starts the per-attempt deadline as soon as the executor exists.
// Stop cancels the timer without killing, and only after the process group
// is gone. The killed channel closes after the group has been signalled.
// Closing or redirecting the output pipe must not call stop.
func armGroup(ctx context.Context, pgid int, limit time.Duration, release func()) (stop func(), killed <-chan struct{}, fired *atomic.Bool) {
	flag := &atomic.Bool{}
	noop := func() {}
	cancellable := ctx != nil && ctx.Done() != nil
	if pgid <= 1 || (limit <= 0 && !cancellable) {
		return noop, nil, flag
	}
	ch := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once
	stop = func() { stopOnce.Do(func() { close(done) }) }
	go func() {
		var timer <-chan time.Time
		if limit > 0 {
			t := time.NewTimer(limit)
			defer t.Stop()
			timer = t.C
		}
		select {
		case <-done:
			return
		case <-timer:
		case <-ctx.Done():
		}
		flag.Store(true)
		killGroup(pgid)
		if release != nil {
			release()
		}
		close(ch)
	}()
	return stop, ch, flag
}

// waitCopy drains the capture goroutine, then keeps the deadline armed until
// pgid is gone. A closed pipe is not the end of the process group.
func waitCopy(copied <-chan struct{}, stop func(), killed <-chan struct{}, pgid int) {
	if stop == nil {
		stop = func() {}
	}
	if copied != nil {
		if killed == nil {
			<-copied
		} else {
			select {
			case <-copied:
			case <-killed:
				<-copied
			}
		}
	}
	if pgid > 1 {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for processGroupAlive(pgid) {
			select {
			case <-killed:
			case <-ticker.C:
			}
		}
	}
	stop()
}

// waitGroupGone reports whether pgid is gone before bound elapses.
func waitGroupGone(pgid int, bound time.Duration) bool {
	if pgid <= 1 || !processGroupAlive(pgid) {
		return true
	}
	deadline := time.Now().Add(bound)
	for processGroupAlive(pgid) {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// deadlineReached reports that this attempt's own hard duration has elapsed
// since it started. A missing or unusable file has not elapsed.
func deadlineReached(home string, started time.Time, attemptID string, now time.Time) bool {
	if started.IsZero() {
		return false
	}
	limit, reason := attemptDeadline(filepath.Join(home, "attempts", attemptID))
	if reason != "" || limit <= 0 {
		return false
	}
	return !now.Before(started.Add(limit))
}

func killGroup(pid int) {
	if pid <= 1 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
