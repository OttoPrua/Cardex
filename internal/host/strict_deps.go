package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

const EventDependencyDeferred = "host_dependency_deferred"

type strictLaunchStage uint8

const (
	strictProbe strictLaunchStage = iota
	strictClaim
	strictExec
)

// strictLaunchGate reads the actual card and prerequisites at each launch
// boundary. A prepared attempt can be reused, but a running or closed one
// must never be relaunched by invoking 'host' directly.
func strictLaunchGate(ctx context.Context, db *store.Store, attempt *model.Attempt, stage strictLaunchStage) (bool, error) {
	allowed := false
	err := db.WithTx(ctx, func(ctx context.Context) error {
		a, err := db.GetAttempt(ctx, attempt.ID)
		if err != nil {
			return err
		}
		if a.Exit != nil || !a.EndedAt.IsZero() || a.Outcome != "" {
			return errors.New("host: strict dependency attempt is already closed")
		}
		if stage == strictExec {
			if a.PID != os.Getpid() {
				return errors.New("host: strict dependency attempt is not owned by this host")
			}
		} else if a.PID != 0 {
			return errors.New("host: strict dependency attempt already has a host")
		}
		card, err := db.GetCard(ctx, a.Card)
		if err != nil {
			return err
		}
		var reason string
		attempts, err := db.ListAttempts(ctx, card.ID)
		if err != nil {
			return err
		}
		for _, other := range attempts {
			if other.ID == a.ID {
				continue
			}
			active := other.PID != 0 && other.Outcome == "" && other.Exit == nil && other.EndedAt.IsZero()
			if other.Index >= a.Index || active {
				reason = "strict preparation is no longer the current attempt"
				break
			}
		}
		if reason == "" && card.Status != model.CardReady && card.Status != model.CardRunning {
			reason = fmt.Sprintf("card %s is %s", card.ID, card.Status)
		}
		if reason == "" && card.StrictDeps {
			if card.Kind == model.KindReview {
				if subject, explicit := model.ReviewSubject(card); explicit {
					if _, err := db.GetCard(ctx, subject); errors.Is(err, sql.ErrNoRows) {
						reason = "review subject " + subject + " does not exist"
					} else if err != nil {
						return err
					}
				}
			}
			if reason == "" {
				reason, err = db.ExecutionDependencyBlocker(ctx, card)
				if err != nil {
					return err
				}
			}
		}
		if reason != "" {
			if stage == strictExec {
				a.PID = 0
				a.StartedAt = time.Time{}
				if err := db.UpdateAttempt(ctx, &a); err != nil {
					return err
				}
			}
			if card.Status == model.CardRunning {
				// Dispatch set running while preparing this attempt. Only
				// undo that provisional state if no other host owns work.
				newerOrOwned := false
				for _, other := range attempts {
					if other.ID == a.ID {
						continue
					}
					active := other.PID != 0 && other.Outcome == "" && other.Exit == nil && other.EndedAt.IsZero()
					if other.Index >= a.Index || active {
						newerOrOwned = true
						break
					}
				}
				if !newerOrOwned {
					card.Status, card.WaitReason = model.CardReady, ""
					if err := db.UpdateCard(ctx, &card); err != nil {
						return err
					}
				}
			}
			raw, err := json.Marshal(map[string]string{"card": card.ID, "reason": reason})
			if err != nil {
				return err
			}
			return db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: EventDependencyDeferred, Data: raw})
		}
		if stage == strictClaim {
			a.PID = os.Getpid()
			// The previous timestamp may be from a much earlier preparation.
			// Reconciliation measures the real host's deadline from this claim.
			a.StartedAt = time.Now().UTC()
			if err := db.UpdateAttempt(ctx, &a); err != nil {
				return err
			}
			*attempt = a
		}
		allowed = true
		return nil
	})
	return allowed, err
}
