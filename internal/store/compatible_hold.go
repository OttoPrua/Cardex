package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// MarkCompatibleCardHeld updates only hold state. Callers prove the observed
// card and route in the same writer transaction before invoking this helper.
func (s *Store) MarkCompatibleCardHeld(ctx context.Context, current model.Card) error {
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE cards SET status=?, wait_reason=?, changed_at=? WHERE id=?`,
		model.CardNeedsInput, model.CompatibleRuntimeHold, formatTime(time.Now()), current.ID)
	if err := rowsAffected(res, err); err != nil {
		return err
	}
	current.Status = model.CardNeedsInput
	current.WaitReason = model.CompatibleRuntimeHold
	s.noteStatus(ctx, current)
	return nil
}

// HoldCompatibleAttempt consumes a still-matching API1 attempt without trusting
// its execution output. It never changes a newer card or attempt; terminal
// evidence flows through UpdateAttempt so strict runtime revisions advance.
func (s *Store) HoldCompatibleAttempt(ctx context.Context, observedCard model.Card, observedAttempt model.Attempt, observedObjective *model.Objective, now time.Time) error {
	return s.holdCompatibleAttempt(ctx, observedCard, observedAttempt, observedObjective, now, nil)
}

type CompatibleRefusal struct {
	ProfileDigest string
	// Verify runs inside the writer transaction with the currently loaded
	// channel. It must only read the local config and compare exact identity.
	Verify func(model.Channel) bool
}

// HoldCompatibleRefusal seals a no-wire raw-compatible refusal even on an
// untagged legacy row. The host supplies the typed, rechecked config proof.
func (s *Store) HoldCompatibleRefusal(ctx context.Context, card model.Card, attempt model.Attempt, objective *model.Objective, now time.Time, refusal CompatibleRefusal) error {
	if digest, err := hex.DecodeString(refusal.ProfileDigest); err != nil || len(digest) != 32 || refusal.Verify == nil {
		return fmt.Errorf("invalid compatible runtime refusal proof")
	}
	return s.holdCompatibleAttempt(ctx, card, attempt, objective, now, &refusal)
}

func (s *Store) holdCompatibleAttempt(ctx context.Context, observedCard model.Card, observedAttempt model.Attempt, observedObjective *model.Objective, now time.Time, refusal *CompatibleRefusal) error {
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		attempt, err := s.GetAttempt(ctx, observedAttempt.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || attempt.Outcome != "" || !reflect.DeepEqual(attempt, observedAttempt) ||
			attempt.Card != observedCard.ID {
			return err
		}
		if refusal != nil {
			// This record asserts that no execution began for the attempt,
			// not merely that a duplicate invocation refused to launch.
			// Recheck under the writer lock before accepting that assertion.
			if attempt.PID != 0 || !attempt.StartedAt.IsZero() || attempt.Exit != nil || !attempt.EndedAt.IsZero() {
				return nil
			}
			var started bool
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE entity=? AND event_type='host_start')`, "attempt:"+attempt.ID).Scan(&started); err != nil {
				return err
			}
			if started {
				return nil
			}
		}
		proofStillCurrent := true
		if dispatch := attempt.RouteReason.Dispatch; refusal != nil || dispatch == nil || dispatch.CompatibleBindingID == "" {
			channel, err := s.GetChannel(ctx, attempt.Route.Channel)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if refusal != nil {
				// The process already refused this observed profile without
				// launching. A newer profile cannot erase that fact, but it
				// does invalidate the old observation as a current card hold.
				proofStillCurrent = err == nil && refusal.Verify(channel)
			} else if err != nil || !channel.CompatibleRuntimeHeld() {
				return nil
			}
		}
		card, err := s.GetCard(ctx, attempt.Card)
		if err != nil {
			return err
		}
		current := proofStillCurrent && observedObjective != nil && reflect.DeepEqual(card, observedCard) &&
			(card.Status == model.CardRunning || (attempt.EndedAt.IsZero() && card.Status == model.CardReady))
		if current && !attempt.EndedAt.IsZero() {
			var changed string
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT changed_at FROM cards WHERE id=?`, card.ID).Scan(&changed); err != nil {
				return err
			}
			changedAt, err := parseTime(changed)
			if err != nil {
				return err
			}
			current = !changedAt.IsZero() && !changedAt.After(attempt.EndedAt)
		}
		if current && card.Objective != "" {
			obj, err := s.GetObjective(ctx, card.Objective)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			current = err == nil && (obj.Status == model.ObjectiveActive || obj.Status == model.ObjectiveDoneCandidate) &&
				reflect.DeepEqual(obj, *observedObjective)
			if current {
				channel, modelName := card.Channel, card.Model
				if channel == "" && modelName == "" {
					channel, modelName = obj.Channel, obj.Model
				}
				current = (channel == "" || channel == attempt.Route.Channel) && (modelName == "" || modelName == attempt.Route.Model)
			}
		}
		if current {
			var exists bool
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM attempts WHERE card_id=? AND id<>? AND attempt_index>=?)`, card.ID, attempt.ID, attempt.Index).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				if err := s.MarkCompatibleCardHeld(ctx, card); err != nil {
					return err
				}
			}
		}
		attempt.Outcome = model.OutcomeNoOutput
		if attempt.EndedAt.IsZero() {
			attempt.EndedAt = now.UTC()
		}
		attempt.ErrorExcerpt = model.CompatibleRuntimeHold
		if err := s.UpdateAttempt(ctx, &attempt); err != nil {
			return err
		}
		if refusal == nil {
			return nil
		}
		raw, err := json.Marshal(struct {
			Attempt       string `json:"attempt"`
			Channel       string `json:"channel"`
			Model         string `json:"model"`
			ProfileSHA256 string `json:"profile_sha256"`
		}{attempt.ID, attempt.Route.Channel, attempt.Route.Model, refusal.ProfileDigest})
		if err != nil {
			return err
		}
		return s.AppendEvent(ctx, &model.Event{Entity: "attempt:" + attempt.ID, Type: "compatible_runtime_refused_no_wire", Data: raw})
	})
}
