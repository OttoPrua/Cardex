package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
)

const (
	strongSlotPending = "pending"
	strongSkipped     = "skipped"
	strongReserved    = "reserved"
	strongClaimed     = "claimed"
	strongStarting    = "starting"
	strongStarted     = "started"
	strongDone        = "done"
	strongStopped     = "stopped"
)

func (s *Store) ReserveStrongStage(ctx context.Context, key StrongRunKey, name string, route model.StrongApprovedRoute) (StrongDecision, error) {
	var d StrongDecision
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, v, err := s.strongPlan(ctx, key)
		if err != nil {
			return err
		}
		if !p.HasExactRewriteCoverage() {
			d.Reason = StrongReasonRewriteCoverage
			return nil
		}
		slot, ok := p.Slot(name)
		if !ok {
			return model.ErrStrongPlan
		}
		if v.Status != StrongRunning {
			d.Reason = v.Reason
			return nil
		}
		ready, err := s.strongDependencies(ctx, p, key, slot)
		if errors.Is(err, model.ErrStrongArtifact) {
			d.Reason = StrongReasonArtifact
			return s.setStrongRunState(ctx, key, StrongBlocked, d.Reason)
		}
		if err != nil {
			return err
		}
		if !ready {
			d.Reason = StrongReasonDependency
			return nil
		}
		if slot.Phase == model.StrongPhaseCheckpoint {
			d.Reason = StrongReasonST3
			if err := s.setStrongSlotState(ctx, key, name, strongStopped, d.Reason); err != nil {
				return err
			}
			return s.setStrongRunState(ctx, key, StrongCheckpoint, d.Reason)
		}
		var state, conditionReason string
		err = s.q(ctx).QueryRowContext(ctx, `SELECT state,condition_reason FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`, key.Run, key.Revision, name).Scan(&state, &conditionReason)
		if err != nil {
			return err
		}
		if state == strongSkipped || state == strongDone {
			return nil
		}
		if slot.Condition != "" && conditionReason == "" {
			d.Reason = StrongReasonCondition
			return nil
		}
		limit := p.Limits[name]
		if !slices.Contains(limit.Routes, route) {
			d.Reason = StrongReasonRoute
			return s.setStrongRunState(ctx, key, StrongBlocked, d.Reason)
		}
		if route.Runner != string(model.RunnerFake) {
			d.Reason = StrongReasonIsolation
			return s.setStrongRunState(ctx, key, StrongBlocked, d.Reason)
		}
		if why := s.strongST3RouteGate(ctx, p, key, slot, route, false); why != "" {
			d.Reason = why
			return s.setStrongSlotState(ctx, key, name, state, why)
		}

		prior, err := s.strongIntent(ctx, key, name)
		if err == nil {
			if prior.Route != route {
				return model.ErrStrongPlan
			}
			d.Intent = prior
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) || state != strongSlotPending {
			return model.ErrStrongPlan
		}
		if v.Spent+limit.Tokens > p.GlobalTokens {
			d.Reason = StrongReasonBudget
			return s.setStrongRunState(ctx, key, StrongBlocked, d.Reason)
		}
		if v.Spent+v.Reserved+limit.Tokens > p.GlobalTokens {
			d.Reason = "parallel_budget_already_reserved"
			return nil
		}
		id, attempt, card := strongIntentIDs(key, name)
		raw, _ := json.Marshal(route)
		_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_intents(run_id,revision,slot,intent_id,attempt_id,card_id,state,route_json,reserved)
			VALUES(?,?,?,?,?,?,?,?,?)`, key.Run, key.Revision, name, id, attempt, card, strongReserved, raw, limit.Tokens)
		if err != nil {
			return err
		}
		_, err = s.q(ctx).ExecContext(ctx, `UPDATE strong_run_plans SET reserved=reserved+? WHERE run_id=? AND revision=? AND plan_sha=?`,
			limit.Tokens, key.Run, key.Revision, key.SHA)
		if err != nil {
			return err
		}
		if err := s.setStrongSlotState(ctx, key, name, strongReserved, ""); err != nil {
			return err
		}
		d.Intent = StrongIntent{Key: key, Slot: name, ID: id, Attempt: attempt, Card: card, Route: route, Reserved: limit.Tokens, State: strongReserved}
		return s.reserveStrongQuota(ctx, p, d.Intent)
	})
	if err != nil {
		return StrongDecision{}, err
	}
	return d, nil
}

func (s *Store) ConfirmStrongT0(ctx context.Context, request StrongIntent, actor, receipt string) error {
	if !model.StrongKey(actor) || !model.StrongKey(receipt) || !request.Route.T0 {
		return model.ErrStrongPlan
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		_, run, err := s.strongPlan(ctx, request.Key)
		if err != nil || run.Status != StrongRunning {
			return model.ErrStrongPlan
		}
		current, err := s.strongIntent(ctx, request.Key, request.Slot)
		if err != nil || current != request || current.State != strongReserved {
			return model.ErrStrongPlan
		}
		var oldID, oldActor string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT t0_id,t0_approver FROM strong_run_intents WHERE intent_id=?`, current.ID).Scan(&oldID, &oldActor); err != nil {
			return err
		}
		if oldID == receipt && oldActor == actor {
			return nil
		}
		if oldID != "" {
			return model.ErrStrongPlan
		}
		out, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_intents SET t0_id=?,t0_approver=? WHERE intent_id=? AND t0_id='' AND state=?`,
			receipt, actor, current.ID, strongReserved)
		return rowsAffected(out, err)
	})
}

// ClaimStrongIntent is committed before returning a one-time synthetic launch
// lease. An ordinary scheduler/config never calls this method. ST2 issues no
// real or T0 launch lease, even if an exact T0 confirmation was recorded.
func (s *Store) ClaimStrongIntent(ctx context.Context, want StrongIntent) (string, string, error) {
	if _, nested := ctx.Value(txKey{}).(execer); nested {
		return "", "", model.ErrStrongPlan
	}
	var lease, reason string
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, run, err := s.strongPlan(ctx, want.Key)
		if err != nil {
			return err
		}
		if !p.HasExactRewriteCoverage() {
			reason = StrongReasonRewriteCoverage
			return nil
		}
		if run.Status != StrongRunning {
			reason = run.Reason
			return nil
		}
		in, err := s.strongIntent(ctx, want.Key, want.Slot)
		if err != nil {
			return err
		}
		if in != want || in.State != strongReserved {
			reason = StrongReasonUnknownLaunch
			return nil
		}
		slot, ok := p.Slot(in.Slot)
		if !ok || slot.Phase == model.StrongPhaseCheckpoint || !slices.Contains(p.Limits[in.Slot].Routes, in.Route) {
			return model.ErrStrongPlan
		}
		ready, err := s.strongDependencies(ctx, p, want.Key, slot)
		if err != nil {
			return err
		}
		if !ready {
			reason = StrongReasonDependency
			return nil
		}
		if why := s.strongST3RouteGate(ctx, p, in.Key, slot, in.Route, true); why != "" {
			reason = why
			return s.setStrongSlotState(ctx, in.Key, in.Slot, strongReserved, why)
		}

		if in.Route.T0 {
			var receipt string
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT t0_id FROM strong_run_intents WHERE intent_id=?`, in.ID).Scan(&receipt); err != nil {
				return err
			}
			reason = StrongReasonT0
			if receipt != "" {
				reason = "t0_dispatch_disabled_in_st2"
			}
			return s.setStrongSlotState(ctx, in.Key, in.Slot, strongReserved, reason)
		}
		if in.Route.Runner != string(model.RunnerFake) {
			reason = StrongReasonIsolation
			return s.setStrongRunState(ctx, in.Key, StrongBlocked, reason)
		}
		source, ok := strongSource(in.Key, slot)
		if !ok {
			return model.ErrStrongPlan
		}
		refs, err := s.strongInputRefs(ctx, p, in.Key, slot)
		if err != nil {
			reason = StrongReasonArtifact
			return s.setStrongRunState(ctx, in.Key, StrongBlocked, reason)
		}
		expected, err := strongInputsSHA(refs)
		if err != nil {
			return err
		}
		reg, err := s.strongProducer(ctx, in.Attempt)
		if err != nil || reg.Card != in.Card || reg.Source != source {
			reason = "strong_custody_not_prepared"
			return nil
		}
		a, err := s.GetAttempt(ctx, in.Attempt)
		if err != nil || !pendingStrongAttempt(a) || !actualSyntheticStrongRoute(a, in.Route) {
			reason = StrongReasonRoute
			return s.setStrongRunState(ctx, in.Key, StrongBlocked, reason)
		}
		var claimed int
		capture, err := s.captureClaim(ctx, reg, &claimed)
		if err != nil || claimed != 1 || capture.ManifestSHA != expected || capture.TaskSHA != p.TaskSHA || s.checkStrongProducer(ctx, reg, &capture) != nil {
			reason = "strong_custody_not_prepared"
			return nil
		}

		lease, err = strongEntropy()
		if err != nil {
			return err
		}
		r, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_intents SET state=?,lease=?,custody_pin=? WHERE intent_id=? AND state=?`,
			strongClaimed, lease, strongWorkflowCapturePin(capture), in.ID, strongReserved)
		if err := rowsAffected(r, err); err != nil {
			return err
		}
		return s.setStrongSlotState(ctx, in.Key, in.Slot, strongClaimed, "")
	})
	if err != nil {
		return "", "", err
	}
	return lease, reason, nil
}

func (s *Store) MarkStrongStarted(ctx context.Context, want StrongIntent, lease string) error {
	if !model.StrongDigestKey(lease) {
		return model.ErrStrongPlan
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		_, _, err := s.strongPlan(ctx, want.Key)
		if err != nil {
			return model.ErrStrongPlan
		}
		in, err := s.strongIntent(ctx, want.Key, want.Slot)
		if err != nil || !sameStrongIntent(in, want) {
			return model.ErrStrongPlan
		}
		var saved string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT lease FROM strong_run_intents WHERE intent_id=?`, in.ID).Scan(&saved); err != nil || saved != lease {
			return model.ErrStrongPlan
		}
		if in.State == strongStarted {
			return nil
		}
		if in.State != strongStarting {
			return model.ErrStrongPlan
		}
		r, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_intents SET state=? WHERE intent_id=? AND state=?`, strongStarted, in.ID, strongStarting)
		if err := rowsAffected(r, err); err != nil {
			return err
		}
		return s.setStrongSlotState(ctx, in.Key, in.Slot, strongStarted, "")
	})
}

func sameStrongIntent(a, b StrongIntent) bool { a.State, b.State = "", ""; return a == b }

// RecoverStrongRun never claims or requeues an uncertain prior launch.
// A completed, exact ledger/result may still be reconciled via SettleStrong.
func (s *Store) RecoverStrongRun(ctx context.Context, key StrongRunKey) error {
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, v, err := s.strongPlan(ctx, key)
		if err != nil {
			return err
		}
		if v.Status != StrongRunning {
			return nil
		}
		if !p.HasExactRewriteCoverage() {
			return s.setStrongRunState(ctx, key, StrongBlocked, StrongReasonRewriteCoverage)
		}
		var uncertain int
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM strong_run_intents i
			LEFT JOIN strong_producers p ON p.attempt_id=i.attempt_id
			WHERE i.run_id=? AND i.revision=? AND
			(i.state IN (?,?,?) OR (i.state=? AND p.claimed=1))`, key.Run, key.Revision, strongClaimed, strongStarting, strongStarted, strongReserved).Scan(&uncertain); err != nil {
			return err
		}
		if uncertain == 0 {
			return nil
		}
		return s.setStrongRunState(ctx, key, StrongBlocked, StrongReasonUnknownLaunch)
	})
}

func (s *Store) CancelStrongRun(ctx context.Context, key StrongRunKey, actor string) error {
	if !model.StrongKey(actor) {
		return model.ErrStrongPlan
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, v, err := s.strongPlan(ctx, key)
		if err != nil {
			return err
		}
		if v.Status == StrongCanceled || v.Status == StrongCompleted || v.Status == StrongUnresolved {
			return nil
		}
		if v.Status == StrongSuperseded {
			return model.ErrStrongPlan
		}
		// No worker can launch a reserved intent. Release only those holds;
		// any claim/start remains unresolved exposure after cancellation.
		var release int64
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT COALESCE(sum(reserved),0) FROM strong_run_intents WHERE run_id=? AND revision=? AND state=?`, key.Run, key.Revision, strongReserved).Scan(&release); err != nil {
			return err
		}
		r, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_plans SET reserved=reserved-? WHERE run_id=? AND revision=? AND reserved>=?`, release, key.Run, key.Revision, release)
		if err := rowsAffected(r, err); err != nil {
			return err
		}
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_slots SET state=?,reason='owner_canceled' WHERE run_id=? AND revision=? AND state IN (?,?)`, strongStopped, key.Run, key.Revision, strongSlotPending, strongReserved); err != nil {
			return err
		}
		if p.CrossFamilyReview {
			if _, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_quota_holds SET state='released' WHERE state='held' AND intent_id IN
		  (SELECT intent_id FROM strong_run_intents WHERE run_id=? AND revision=? AND state=?)`, key.Run, key.Revision, strongReserved); err != nil {
				return err
			}
		}

		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_intents SET state=? WHERE run_id=? AND revision=? AND state=?`, strongStopped, key.Run, key.Revision, strongReserved); err != nil {
			return err
		}
		return s.setStrongRunState(ctx, key, StrongCanceled, "owner_canceled")
	})
}

// VerifyStrongLaunch consumes the final start authorization immediately
// before the caller starts a synthetic child. A lease cannot start twice.
func (s *Store) VerifyStrongLaunch(ctx context.Context, want StrongIntent, lease string, capture *strongcustody.Capture, path string) error {
	if _, nested := ctx.Value(txKey{}).(execer); nested || !model.StrongDigestKey(lease) {
		return model.ErrStrongPlan
	}
	var denied string
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, run, err := s.strongPlan(ctx, want.Key)
		if err != nil || run.Status != StrongRunning || !p.HasExactRewriteCoverage() {
			return model.ErrStrongPlan
		}
		in, err := s.strongIntent(ctx, want.Key, want.Slot)
		if err != nil || !sameStrongIntent(in, want) || in.State != strongClaimed || in.Route.T0 || in.Route.Runner != string(model.RunnerFake) {
			return model.ErrStrongPlan
		}
		var slotState string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT state FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`, in.Key.Run, in.Key.Revision, in.Slot).Scan(&slotState); err != nil || slotState != strongClaimed {
			return model.ErrStrongPlan
		}

		if !slices.Contains(p.Limits[in.Slot].Routes, in.Route) {
			return model.ErrStrongPlan
		}
		var actual, pin string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT lease,custody_pin FROM strong_run_intents WHERE intent_id=?`, in.ID).Scan(&actual, &pin); err != nil || actual != lease {
			return model.ErrStrongPlan
		}
		slot, ok := p.Slot(in.Slot)
		if !ok || slot.Phase == model.StrongPhaseCheckpoint {
			return model.ErrStrongPlan
		}
		att, err := s.GetAttempt(ctx, in.Attempt)
		if err != nil || att.Card != in.Card || !pendingStrongAttempt(att) {
			return model.ErrStrongPlan
		}
		if err := s.checkStrongWorkflowCapture(ctx, in, pin); err != nil {
			return err
		}
		if why := s.strongST3RouteGate(ctx, p, in.Key, slot, in.Route, true); why != "" {
			denied = why
			return s.releaseStrongNoStart(ctx, in, lease, why)
		}

		refs, err := s.strongInputRefs(ctx, p, in.Key, slot)
		if err != nil {
			return err
		}
		expected, err := strongInputsSHA(refs)
		if err != nil {
			return err
		}
		if !actualSyntheticStrongRoute(att, in.Route) {
			if !p.CrossFamilyReview {
				return model.ErrStrongPlan
			}
			denied = StrongReasonRoute
			return s.releaseStrongNoStart(ctx, in, lease, denied)
		}
		proof, err := capture.ProveLaunch(ctx, att, path)
		if err != nil || proof.ManifestSHA != expected || proof.TaskSHA != p.TaskSHA || strongWorkflowCapturePin(proof) != pin {
			return model.ErrStrongArtifact
		}

		r, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_intents SET state=? WHERE intent_id=? AND state=? AND lease=?`, strongStarting, in.ID, strongClaimed, lease)
		if err := rowsAffected(r, err); err != nil {
			return err
		}
		return s.setStrongSlotState(ctx, in.Key, in.Slot, strongStarting, "")
	})
	if err != nil {
		return err
	}
	if denied != "" {
		return fmt.Errorf("%w: %s", model.ErrStrongPlan, denied)
	}
	return nil
}
