package sched

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cardex/internal/harvest"
	"cardex/internal/loop"
	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
	"cardex/internal/workspace"
)

// productionEngine is sched.New's design phase. It calls loop.New(...).Step
// and then DevStep against r.Home. A design-phase error is stored and the
// open design attempt is handed to the installed §5 disposition. The error
// itself is not returned, so serve keeps ticking.
func (r *Runner) productionEngine(ctx context.Context, objectiveID string) error {
	return withCardexHome(r.Home, func() error {
		db, err := store.Open(filepath.Join(r.Home, "cardex.db"))
		if err != nil {
			return r.noteDesignFailure(ctx, nil, objectiveID, err)
		}
		defer db.Close()
		eng := loop.New(db, workspace.Git{}, r.designDeliverer())
		if err := eng.Step(ctx, objectiveID); err != nil {
			if noteErr := r.noteDesignFailure(ctx, db, objectiveID, err); noteErr != nil {
				return noteErr
			}
		}
		if err := devStep(ctx, objectiveID); err != nil && !errors.Is(err, loop.ErrNotImplemented) {
			return err
		}
		return nil
	})
}

func (r *Runner) designDeliverer() notify.Deliverer {
	if r == nil || r.mail == nil {
		r.ensureMail()
	}
	return homeDeliverer{home: r.Home, inner: r.mail}
}

func (r *Runner) ensureMail() {
	if r.mail != nil {
		return
	}
	r.mail = notify.New(notify.CommandExec, func() time.Time { return r.now() })
}

type homeDeliverer struct {
	home  string
	inner notify.Deliverer
}

func (h homeDeliverer) Deliver(ctx context.Context, msg notify.Message) error {
	if strings.TrimSpace(msg.Home) == "" {
		msg.Home = h.home
	}
	if h.inner == nil {
		return notify.Stub{}.Deliver(ctx, msg)
	}
	return h.inner.Deliver(ctx, msg)
}

func (r *Runner) noteDesignFailure(ctx context.Context, db *store.Store, objectiveID string, cause error) error {
	if cause == nil {
		return nil
	}
	if db == nil {
		opened, err := store.Open(filepath.Join(r.Home, "cardex.db"))
		if err != nil {
			return err
		}
		db = opened
		defer db.Close()
	}
	raw, err := json.Marshal(map[string]string{
		"error":     cause.Error(),
		"objective": objectiveID,
	})
	if err != nil {
		return err
	}
	entity := "objective:" + objectiveID
	if err := db.AppendEvent(ctx, &model.Event{
		Entity: entity,
		Type:   "design_error",
		Data:   raw,
	}); err != nil {
		return err
	}
	return r.disposeOpenDesign(ctx, db, objectiveID)
}

// disposeOpenDesign harvests a design-family attempt that this tick's harvest
// skipped (no exit yet) and passes that result to the installed disposition.
// An attempt that does not exist yet is left for the lane note.
func (r *Runner) disposeOpenDesign(ctx context.Context, db *store.Store, objectiveID string) error {
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return err
	}
	for i := range attempts {
		attempt := attempts[i]
		if attempt.Outcome != "" || !attempt.EndedAt.IsZero() || attempt.Exit != nil {
			continue
		}
		if strings.TrimSpace(attempt.Worktree) == "" {
			continue
		}
		card, err := db.GetCard(ctx, attempt.Card)
		if err != nil || card.Objective != objectiveID || !designFamilyKind(card.Kind) {
			continue
		}
		exitCode := 1
		res, err := harvest.Harvest(ctx, harvest.Input{
			Attempt: attempt,
			Card:    card,
			Exit:    exitCode,
			Repo:    attempt.Worktree,
		})
		if err != nil {
			return err
		}
		if err := r.deliver(ctx, db, &card, &attempt, res); err != nil {
			return err
		}
	}
	return nil
}

func designFamilyKind(kind model.CardKind) bool {
	switch kind {
	case model.KindDesign, model.KindResearch, model.KindPlan, model.KindReplan, model.KindSpike:
		return true
	default:
		return false
	}
}

func withCardexHome(home string, fn func() error) error {
	prev, had := os.LookupEnv("CARDEX_HOME")
	if err := os.Setenv("CARDEX_HOME", home); err != nil {
		return err
	}
	defer func() {
		if had {
			_ = os.Setenv("CARDEX_HOME", prev)
			return
		}
		_ = os.Unsetenv("CARDEX_HOME")
	}()
	return fn()
}
