package host

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"time"

	"cardex/internal/store"
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
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
