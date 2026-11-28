package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

const EventComparisonRefusedNoWire = "strict_comparison_refused_no_wire"
const StrictComparisonDisabled = "strict comparison requires independently authenticated operator approval; no live request enabled"

// RefuseDisabledComparison records only a still-current, pristine API2
// dispatch. This is a no-wire refusal, not a host or generation claim; a
// comparison marker on a historical or differently owned attempt does not
// authorize changing either record.
func (s *Store) RefuseDisabledComparison(ctx context.Context, observedCard model.Card, observed model.Attempt, objective model.Objective, now time.Time) error {
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		a, err := s.GetAttempt(ctx, observed.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !reflect.DeepEqual(a, observed) ||
			!a.StrictComparison() || a.Card != observedCard.ID || a.PID != 0 ||
			!a.StartedAt.IsZero() || !a.EndedAt.IsZero() || a.Exit != nil || a.HeadCommit != "" ||
			a.Outcome != "" || a.ErrorClass != "" || a.ErrorExcerpt != "" ||
			a.Usage != (model.Usage{}) || a.RouteReason.ProviderModel != nil {
			return err
		}
		c, err := s.GetCard(ctx, a.Card)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !reflect.DeepEqual(c, observedCard) || !c.ValidStrictComparison() ||
			(c.Status != model.CardReady && c.Status != model.CardRunning) {
			return err
		}
		obj, err := s.GetObjective(ctx, c.Objective)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !reflect.DeepEqual(obj, objective) ||
			(obj.Status != model.ObjectiveActive && obj.Status != model.ObjectiveDoneCandidate) {
			return err
		}
		d := a.RouteReason.Dispatch
		_, digest := c.ComparisonInput()
		if digest == "" || digest != d.ComparisonInputDigest || d.Runner != model.RunnerClaude ||
			d.Channel != c.Channel || d.Model != c.Model || a.Route != (model.Route{Channel: c.Channel, Model: c.Model}) {
			return nil
		}
		ch, err := s.GetChannel(ctx, a.Route.Channel)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || ch.Runner != model.RunnerClaude || !slices.Contains(ch.Models, d.Model) ||
			d.CompatibleBindingID == "" || d.CompatibleBindingID != runner.CompatibleBindingID(ch.EngineProfile) {
			return err
		}
		// Prepared timestamps or raw metadata do not prove a launch didn't
		// happen. Refuse only a pristine attempt with no lifecycle evidence,
		// typed workflow ownership, or competing attempt. Under the writer,
		// the refusal and a competing CAS cannot both get this clean phase.
		var outsideOwnership bool
		err = s.q(ctx).QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM events WHERE entity=?) OR
			EXISTS(SELECT 1 FROM attempts WHERE card_id=? AND id<>? AND
				(attempt_index>=? OR (pid<>0 AND outcome='' AND exited=0 AND ended_at='')))`,
			"attempt:"+a.ID, c.ID, a.ID, a.Index).Scan(&outsideOwnership)
		if err != nil || outsideOwnership {
			return err
		}
		if owned, err := s.strictComparisonTypedOwner(ctx, a.ID); err != nil || owned {
			return err
		}
		a.EndedAt, a.Outcome, a.ErrorClass = now.UTC(), model.OutcomeUnavailable, model.ErrorUnavailable
		a.ErrorExcerpt = StrictComparisonDisabled
		c.Status, c.WaitReason = model.CardNeedsInput, StrictComparisonDisabled
		if err := s.UpdateAttempt(ctx, &a); err != nil {
			return err
		}
		if err := s.UpdateCard(ctx, &c); err != nil {
			return err
		}
		return s.AppendEvent(ctx, &model.Event{
			ID: comparisonEventID(EventComparisonRefusedNoWire, a.ID), Entity: "attempt:" + a.ID,
			Type: EventComparisonRefusedNoWire, Data: []byte(`{"status":"approval_unavailable","attempted":false}`),
		})
	})
}

// Both strong custody schemas are additive. Ordinary homes must not install
// either one just to record a refusal, and an existing table that cannot be
// queried must fail closed. Caller holds the same writer throughout.
func (s *Store) strictComparisonTypedOwner(ctx context.Context, attempt string) (bool, error) {
	for _, table := range []struct {
		name, lookup string
	}{
		{"strong_producers", `SELECT EXISTS(SELECT 1 FROM strong_producers WHERE attempt_id=?)`},
		{"strong_run_intents", `SELECT EXISTS(SELECT 1 FROM strong_run_intents WHERE attempt_id=?)`},
	} {
		var kind string
		err := s.q(ctx).QueryRowContext(ctx, "SELECT type FROM sqlite_schema WHERE name=?", table.name).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil || kind != "table" {
			return true, err
		}
		var owned bool
		if err := s.q(ctx).QueryRowContext(ctx, table.lookup, attempt).Scan(&owned); err != nil || owned {
			return owned, err
		}
	}
	return false, nil
}
