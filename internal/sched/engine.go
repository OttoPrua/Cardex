package sched

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

// productionEngine is sched.New's design phase. It calls loop.New(...).Step
// and then DevStep against r.Home. A design-phase error is stored and retried
// next tick. §5 runs only for the design-family attempt that error names when
// that attempt already has a real exit. The error itself is not returned, so
// serve keeps ticking.
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
	return homeDeliverer{runner: r, home: r.Home, inner: r.mail}
}

func (r *Runner) ensureMail() {
	if r.mail != nil {
		return
	}
	r.mail = notify.New(notify.CommandExec, func() time.Time { return r.now() })
}

type homeDeliverer struct {
	runner *Runner
	home   string
	inner  notify.Deliverer
}

func (h homeDeliverer) Deliver(ctx context.Context, msg notify.Message) error {
	db, err := h.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if h.runner != nil {
		if err := h.runner.fillDelivery(ctx, db, &msg); err != nil {
			return err
		}
	} else if strings.TrimSpace(msg.Home) == "" {
		msg.Home = h.home
	}
	var deliverErr error
	if h.inner == nil {
		deliverErr = notify.Stub{}.Deliver(ctx, msg)
	} else {
		deliverErr = h.inner.Deliver(ctx, msg)
	}
	if h.runner == nil {
		return deliverErr
	}
	return h.runner.handoffDelivery(ctx, db, msg, deliverErr)
}

func (h homeDeliverer) openDB() (*store.Store, error) {
	home := h.home
	if h.runner != nil && strings.TrimSpace(h.runner.Home) != "" {
		home = h.runner.Home
	}
	if strings.TrimSpace(home) == "" {
		return nil, errors.New("sched: delivery home is empty")
	}
	return store.Open(filepath.Join(home, "cardex.db"))
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
	return r.disposeOpenDesign(ctx, db, objectiveID, cause)
}

// disposeOpenDesign runs the existing §5 path only for the single design-family
// attempt this error names that already exited. A still-running attempt (no
// exit) stays running, and an exit code is never invented. Zero or several
// matches record the error and leave the attempts for the next tick.
func (r *Runner) disposeOpenDesign(ctx context.Context, db *store.Store, objectiveID string, cause error) error {
	if cause == nil {
		return nil
	}
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return err
	}
	msg := cause.Error()
	var hit *model.Attempt
	var hitCard model.Card
	for i := range attempts {
		attempt := attempts[i]
		if attempt.Exit == nil || attempt.Outcome != "" || host.LiveHost(attempt) {
			continue
		}
		if strings.TrimSpace(attempt.Worktree) == "" || !namesAttempt(msg, attempt.ID) {
			continue
		}
		card, err := db.GetCard(ctx, attempt.Card)
		if err != nil || card.Objective != objectiveID || !designFamilyKind(card.Kind) {
			continue
		}
		if hit != nil {
			return nil
		}
		cp := attempt
		hit = &cp
		hitCard = card
	}
	if hit == nil {
		return nil
	}
	res, err := harvest.Harvest(ctx, harvest.Input{
		Attempt:    *hit,
		Card:       hitCard,
		Exit:       *hit.Exit,
		ErrorClass: hit.ErrorClass,
		Repo:       hit.Worktree,
		Home:       r.Home,
	})
	if err != nil {
		return err
	}
	return r.deliver(ctx, db, &hitCard, hit, res)
}

// namesAttempt reports whether msg contains id as its own token. A longer id
// that merely starts with id does not count.
func namesAttempt(msg, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || msg == "" {
		return false
	}
	for from := 0; from < len(msg); {
		i := strings.Index(msg[from:], id)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(id)
		if (i == 0 || !attemptIDChar(msg[i-1])) && (end == len(msg) || !attemptIDChar(msg[end])) {
			return true
		}
		from = i + 1
	}
	return false
}

func attemptIDChar(b byte) bool {
	return b == '-' || b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
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
