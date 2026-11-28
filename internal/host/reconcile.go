package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/store"
)

func reconcile(ctx context.Context, home string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return err
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
	return reconcileSnapshot(ctx, db, home, attempts, time.Now().UTC())
}

func reconcileSnapshot(ctx context.Context, db *store.Store, home string, attempts []model.Attempt, now time.Time) error {
	for i := range attempts {
		a := attempts[i]
		if !LiveHost(a) || processAlive(a.PID) {
			continue
		}
		// A saved disposition or lost claim since the scan invalidates
		// this worker even before touching that attempt's cleanup files.
		current, err := db.GetAttempt(ctx, a.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if !sameReconcileClaim(a, current) {
			continue
		}
		// A dead host is not a finished executor. A group still inside its
		// hard duration stays recorded as running. Once that duration has
		// passed, reap the group and store the attempt only after it is gone.
		dir := roots.AttemptDir(home, a.ID, a.PromptPath)
		if pgid := readGroupFile(filepath.Join(dir, executorGroupName)); processGroupAlive(pgid) {
			if !deadlineReached(dir, a.StartedAt, now) {
				continue
			}
			killGroup(pgid)
			if !waitGroupGone(pgid, 3*time.Second) {
				continue
			}
		}
		// The executor group is gone, so its browser is an orphan.
		reapLostBrowser(dir)
		if err := reapRunScratch(dir, a.ID); err != nil {
			appendEvent(ctx, db, a.ID, "scratch_cleanup_failed", map[string]string{"error": err.Error()})
		}
		if err := persistLostHost(ctx, db, a, now); err != nil {
			return err
		}
	}
	return nil
}

func sameReconcileClaim(observed, current model.Attempt) bool {
	return LiveHost(current) && current.Outcome == "" && reflect.DeepEqual(observed, current)
}

// persistLostHost compares the exact live claim under the SQLite writer;
// post-scan terminal receipts and released claims have authority.
func persistLostHost(ctx context.Context, db *store.Store, observed model.Attempt, now time.Time) error {
	return db.WithWriteTx(ctx, func(ctx context.Context) error {
		current, err := db.GetAttempt(ctx, observed.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameReconcileClaim(observed, current) || processAlive(current.PID) {
			return nil
		}
		current.EndedAt = now
		if current.ErrorExcerpt == "" {
			current.ErrorExcerpt = "host process exited before recording a result"
		}
		if err := db.UpdateAttempt(ctx, &current); err != nil {
			return err
		}
		raw, err := json.Marshal(map[string]int{"pid": current.PID})
		if err != nil {
			return err
		}
		return db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + current.ID, Type: "host_lost", TS: now, Data: raw})
	})
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
