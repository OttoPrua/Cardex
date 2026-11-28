package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// HostPreclaim binds the actual card and attempt the host is about to use to
// their durable revisions and the exact objective tuple from the same view.
// Card/attempt revisions detect reverted edits; an objective has no separate
// revision column, so it is rechecked by its full stored values.
type HostPreclaim struct {
	Attempt model.Attempt
	Card    model.Card

	attemptRevision string
	cardRevision    string
	objective       *model.Objective
}

// ReadHostPreclaim obtains a single view before any local preflight work.
func (s *Store) ReadHostPreclaim(ctx context.Context, id string) (HostPreclaim, error) {
	var view HostPreclaim
	if ctx.Value(txKey{}) != nil {
		return view, errors.New("host preclaim requires its own read view")
	}
	err := s.WithTx(ctx, func(tx context.Context) error {
		var err error
		view, err = s.hostPreclaim(tx, id)
		return err
	})
	return view, err
}

func (s *Store) hostPreclaim(tx context.Context, id string) (HostPreclaim, error) {
	var view HostPreclaim
	a, err := s.GetAttempt(tx, id)
	if err != nil {
		return view, fmt.Errorf("host: load attempt: %w", err)
	}
	c, err := s.GetCard(tx, a.Card)
	if err != nil {
		return view, fmt.Errorf("host: load card: %w", err)
	}
	row := s.q(tx).QueryRowContext(tx, `SELECT a.changed_at, c.changed_at
		FROM attempts a JOIN cards c ON c.id=a.card_id WHERE a.id=? AND c.id=?`, a.ID, c.ID)
	if err := row.Scan(&view.attemptRevision, &view.cardRevision); err != nil {
		return view, err
	}
	if c.Objective != "" {
		obj, err := s.GetObjective(tx, c.Objective)
		if err == nil {
			view.objective = &obj
		} else if !errors.Is(err, sql.ErrNoRows) {
			return view, fmt.Errorf("host: load objective: %w", err)
		}
	}
	view.Attempt, view.Card = a, c
	return view, nil
}

func (v HostPreclaim) objectiveAllowsHandoff() bool {
	if v.objective == nil {
		// Direct legacy attempts have historically been allowed without an
		// objective row. A new/deleted row still changes the exact view.
		return !v.Card.StrictDeps
	}
	return v.objective.Status == model.ObjectiveActive || v.objective.Status == model.ObjectiveDoneCandidate
}

// RecordHostPreclaimHandoff preserves the ordinary legacy needs_review
// behavior only while the host still sees the exact unowned preparation. It
// does not act on a historical/typed hold or create executor/no-wire evidence.
func (s *Store) RecordHostPreclaimHandoff(ctx context.Context, observed HostPreclaim, channel model.Channel, reason, excerpt string, now time.Time) error {
	if ctx.Value(txKey{}) != nil {
		return errors.New("host preclaim handoff requires its own writer")
	}
	return s.WithWriteTx(ctx, func(tx context.Context) error {
		current, err := s.hostPreclaim(tx, observed.Attempt.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		a, c := current.Attempt, current.Card
		if !reflect.DeepEqual(current, observed) || !current.objectiveAllowsHandoff() || !pristineLocalPreparation(a) ||
			a.StrictComparison() || c.StrictComparison() ||
			(c.Status != model.CardRunning && c.Status != model.CardReady) ||
			c.WaitReason == model.CompatibleRuntimeHold || c.WaitReason == model.CompatibleHistoricalRecoveryHold ||
			c.WaitReason == model.LegacyClaudeBuildHold {
			return nil
		}
		freshChannel, err := s.GetChannel(tx, a.Route.Channel)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !reflect.DeepEqual(freshChannel, channel) {
			return err
		}
		other, err := s.hasOtherLocalAttemptOwner(tx, a)
		if err != nil || other {
			return err
		}
		owned, err := s.compatibleTypedAttemptOwner(tx, a.ID)
		if err != nil || owned {
			return err
		}
		events, err := s.ListEvents(tx, "attempt:"+a.ID)
		if err != nil {
			return err
		}
		for _, event := range events {
			switch event.Type {
			case model.EventAttemptPrepared, model.ReviewSubjectEvent, "host_config":
				continue
			case model.EventHostDependencyDeferred:
				// A later strict admission may already have started a
				// side effect. Old/untyped deferrals do not identify a
				// phase and cannot establish a benign preclaim history.
				var receipt model.HostDependencyDeferral
				if c.StrictDeps && json.Unmarshal(event.Data, &receipt) == nil &&
					receipt.Card == c.ID && receipt.Attempt == a.ID &&
					receipt.Phase == model.HostDependencyPreclaimProbe && receipt.Reason != "" {
					continue
				}
			}
			return nil
		}

		raw, err := json.Marshal(map[string]string{"reason": reason})
		if err != nil {
			return err
		}
		if err := s.markOperatorHeldCard(tx, c, excerpt); err != nil {
			return err
		}
		a.EndedAt, a.ErrorExcerpt = now.UTC(), excerpt
		if err := s.UpdateAttempt(tx, &a); err != nil {
			return err
		}
		return s.AppendEvent(tx, &model.Event{Entity: "attempt:" + a.ID, Type: "needs_review", Data: raw})
	})
}
