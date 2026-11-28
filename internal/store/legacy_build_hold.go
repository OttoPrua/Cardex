package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// HoldLegacyClaudeBuild records a local build/configuration block, not an
// executor result or evidence of provider activity. It accepts only the
// current, still-unclaimed ordinary preparation. verify must only recheck the
// captured local condition, not launch a process or touch a checkout.
func (s *Store) HoldLegacyClaudeBuild(ctx context.Context, observedCard model.Card, observedAttempt model.Attempt, objective model.Objective, channel model.Channel, now time.Time, verify func() bool) (bool, error) {
	if verify == nil || now.IsZero() || observedAttempt.StartedAt.IsZero() || observedCard.StrictDeps ||
		observedCard.StrictComparison() || observedAttempt.StrictComparison() {
		return false, nil
	}
	if ctx.Value(txKey{}) != nil {
		return false, errors.New("legacy build hold requires its own writer")
	}
	held := false
	err := s.WithWriteTx(ctx, func(tx context.Context) error {
		a, err := s.GetAttempt(tx, observedAttempt.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !reflect.DeepEqual(a, observedAttempt) || a.Card != observedCard.ID || !pristineLocalPreparation(a) {
			return err
		}
		if d := a.RouteReason.Dispatch; d != nil && (d.CompatibleBindingID != "" || (d.Runner != "" && d.Runner != model.RunnerClaude)) {
			return nil
		}
		ch, err := s.GetChannel(tx, a.Route.Channel)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !reflect.DeepEqual(ch, channel) || ch.Runner != model.RunnerClaude || ch.CompatibleRuntimeHeld() || ch.EngineProfile == "" {
			return err
		}
		card, current, err := s.currentLocalPreparation(tx, a, observedCard, &objective)
		if err != nil || !current {
			return err
		}
		revision, valid, err := s.localPreparationRevision(tx, a, card.Kind, true)
		if err != nil || !valid {
			return err
		}
		var changed string
		if err := s.q(tx).QueryRowContext(tx, `SELECT changed_at FROM cards WHERE id=?`, card.ID).Scan(&changed); err != nil {
			return err
		}
		if changed != revision || !verify() {
			return nil
		}

		if err := s.markOperatorHeldCard(tx, card, model.LegacyClaudeBuildHold); err != nil {
			return err
		}
		a.Outcome, a.ErrorClass, a.ErrorExcerpt = model.OutcomeLaunchFailed, model.ErrorLaunchFailed, model.LegacyClaudeBuildHold
		a.EndedAt = now.UTC()
		if err := s.UpdateAttempt(tx, &a); err != nil {
			return err
		}
		if err := s.AppendEvent(tx, &model.Event{Entity: "attempt:" + a.ID, Type: model.EventLegacyClaudeBuildHeld}); err != nil {
			return err
		}
		held = true
		return nil
	})
	return err == nil && held, err
}

// A PID or exit (including a nil-PID finish), populated usage, checkout
// output, or foreign lifecycle evidence is never cleared to recover launch.
func pristineLocalPreparation(a model.Attempt) bool {
	return a.PID == 0 && a.Outcome == "" && a.Exit == nil && a.EndedAt.IsZero() &&
		a.ErrorClass == "" && a.ErrorExcerpt == "" && a.HeadCommit == "" &&
		a.Usage == (model.Usage{}) && a.RouteReason.ProviderModel == nil
}

// Only the latest ordinary preparation on the same current card and objective
// can request an operator hold. The write lock also fences competing typed
// reservations and any other open attempt for this card.
func (s *Store) currentLocalPreparation(tx context.Context, a model.Attempt, observed model.Card, objective *model.Objective) (model.Card, bool, error) {
	c, err := s.GetCard(tx, a.Card)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil || !reflect.DeepEqual(c, observed) || c.StrictDeps || c.StrictComparison() || a.StrictComparison() ||
		c.Status != model.CardRunning || c.WaitReason != "" || c.WaitQuestionID != "" || objective == nil {
		return c, false, err
	}
	o, err := s.GetObjective(tx, c.Objective)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil || !reflect.DeepEqual(o, *objective) ||
		(o.Status != model.ObjectiveActive && o.Status != model.ObjectiveDoneCandidate) {
		return c, false, err
	}
	channel, modelName := c.Channel, c.Model
	if channel == "" && modelName == "" {
		channel, modelName = o.Channel, o.Model
	}
	if (channel != "" && channel != a.Route.Channel) || (modelName != "" && modelName != a.Route.Model) {
		return c, false, nil
	}
	other, err := s.hasOtherLocalAttemptOwner(tx, a)
	if err != nil || other {
		return c, false, err
	}
	owned, err := s.compatibleTypedAttemptOwner(tx, a.ID)
	return c, err == nil && !owned, err
}

func (s *Store) hasOtherLocalAttemptOwner(tx context.Context, a model.Attempt) (bool, error) {
	var other bool
	err := s.q(tx).QueryRowContext(tx, `SELECT EXISTS(SELECT 1 FROM attempts WHERE card_id=? AND id<>? AND
		(attempt_index>=? OR (outcome='' AND exited=0 AND ended_at='')))`, a.Card, a.ID, a.Index).Scan(&other)
	return other, err
}

// Only the legacy review-subject pin is non-execution history. A historical
// check demands no preparation receipt at all; an invalid, duplicate or
// wrong-timestamp present receipt is not a pre-upgrade record.
func (s *Store) localPreparationRevision(tx context.Context, a model.Attempt, kind model.CardKind, wantReceipt bool) (string, bool, error) {
	events, err := s.ListEvents(tx, "attempt:"+a.ID)
	if err != nil {
		return "", false, err
	}
	var revision string
	pinned := false
	for _, ev := range events {
		switch ev.Type {
		case model.ReviewSubjectEvent:
			var pin model.PinnedReviewSubject
			if pinned || kind != model.KindReview || json.Unmarshal(ev.Data, &pin) != nil || pin.Subject == "" {
				return "", false, nil
			}
			pinned = true
		case model.EventAttemptPrepared:
			if !wantReceipt || revision != "" || !ev.TS.Equal(a.StartedAt) || json.Unmarshal(ev.Data, &revision) != nil || revision == "" {
				return "", false, nil
			}
		default:
			return "", false, nil
		}
	}
	return revision, (revision != "") == wantReceipt, nil
}
