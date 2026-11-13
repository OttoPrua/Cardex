package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/OttoPrua/Cardex/internal/store"
)

func reconcile(ctx context.Context, home string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range attempts {
		a := attempts[i]
		if !LiveHost(a) || processAlive(a.PID) {
			continue
		}
		// A dead host is not a finished executor. A group still inside its
		// hard duration stays recorded as running. Once that duration has
		// passed, reap the group and store the attempt only after it is gone.
		if pgid := readExecutorGroup(home, a.ID); processGroupAlive(pgid) {
			if !deadlineReached(home, a.StartedAt, a.ID, now) {
				continue
			}
			killGroup(pgid)
			if !waitGroupGone(pgid, 3*time.Second) {
				continue
			}
		}
		// The executor group is gone, so its browser is an orphan.
		reapLostBrowser(home, a.ID)
		a.EndedAt = now
		if a.ErrorExcerpt == "" {
			a.ErrorExcerpt = "host process exited before recording a result"
		}
		if err := db.UpdateAttempt(ctx, &a); err != nil {
			return err
		}
		appendEvent(ctx, db, a.ID, "host_lost", map[string]any{"pid": a.PID})
	}
	return nil
}

func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	if inOurGroup(pid) {
		return true
	}
	err := SignalProcess(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
