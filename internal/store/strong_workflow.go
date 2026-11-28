package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

const (
	StrongAwaiting   = "awaiting_approval"
	StrongRunning    = "running"
	StrongBlocked    = "blocked"
	StrongCanceled   = "canceled"
	StrongSuperseded = "superseded"
	StrongCheckpoint = "st3_required"
)
const (
	StrongReasonApproval      = "approval_required"
	StrongReasonDependency    = "dependencies_unfinished"
	StrongReasonCondition     = "condition_decision_required"
	StrongReasonBudget        = "global_budget_unavailable"
	StrongReasonRoute         = "route_not_approved"
	StrongReasonT0            = "t0_confirmation_required"
	StrongReasonIsolation     = "no_verified_real_runner"
	StrongReasonUnknownLaunch = "launch_state_unresolved"
	StrongReasonUnknownUsage  = "usage_unknown"
	StrongReasonOverrun       = "budget_overrun"
	StrongReasonArtifact      = "artifact_invalid_or_missing"
	StrongReasonST3           = "st3_checkpoint_disabled"
)

type StrongRunKey struct {
	Run      string
	Revision int
	SHA      string
}

type StrongRunView struct {
	Key                  StrongRunKey
	Status, Reason       string
	Reserved, Spent      int64
	ApprovalID, Approver string
}

type StrongSlotView struct {
	Key, State, Reason string
	AcceptedAttempt    string
	Ref                model.StrongArtifactRef
}

type StrongIntent struct {
	Key                            StrongRunKey
	Slot, ID, Attempt, Card, State string
	Route                          model.StrongApprovedRoute
	Reserved                       int64
}

type StrongDecision struct {
	Launch bool
	Reason string
	Intent StrongIntent
}

func (s *Store) SaveStrongPlan(ctx context.Context, plan model.StrongPlan) (StrongRunKey, error) {
	body, sha, err := plan.Canonical()
	if err != nil {
		return StrongRunKey{}, err
	}
	key := StrongRunKey{plan.Run, plan.Revision, sha}
	err = s.WithWriteTx(ctx, func(ctx context.Context) error {
		var revision int
		var oldSHA, status string
		var priorSpent, priorReserved int64
		err := s.q(ctx).QueryRowContext(ctx, `SELECT revision,plan_sha,status,spent,reserved FROM strong_run_plans WHERE run_id=? ORDER BY revision DESC LIMIT 1`, plan.Run).Scan(&revision, &oldSHA, &status, &priorSpent, &priorReserved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if revision == plan.Revision && oldSHA == sha && status == StrongAwaiting {
			return nil
		}
		if revision+1 != plan.Revision || (revision > 0 && status != StrongAwaiting && status != StrongCanceled) || priorReserved != 0 || priorSpent > plan.GlobalTokens {
			return model.ErrStrongPlan
		}
		if status == StrongAwaiting {
			if _, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_plans SET status=? WHERE run_id=? AND revision=?`, StrongSuperseded, plan.Run, revision); err != nil {
				return err
			}
		}
		_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_plans(run_id,revision,plan_sha,plan_json,status,reason,spent) VALUES(?,?,?,?,?,?,?)`,
			plan.Run, plan.Revision, sha, body, StrongAwaiting, StrongReasonApproval, priorSpent)
		return err
	})
	if err != nil {
		return StrongRunKey{}, err
	}
	return key, nil
}

func (s *Store) strongPlan(ctx context.Context, key StrongRunKey) (model.StrongPlan, StrongRunView, error) {
	var p model.StrongPlan
	v := StrongRunView{Key: key}
	var raw []byte
	err := s.q(ctx).QueryRowContext(ctx, `SELECT CASE WHEN length(plan_json)<=65536 THEN plan_json END,status,reason,reserved,spent,approval_id,approver
		FROM strong_run_plans WHERE run_id=? AND revision=? AND plan_sha=?`, key.Run, key.Revision, key.SHA).
		Scan(&raw, &v.Status, &v.Reason, &v.Reserved, &v.Spent, &v.ApprovalID, &v.Approver)
	if err != nil || json.Unmarshal(raw, &p) != nil {
		return p, v, model.ErrStrongPlan
	}
	body, sha, err := p.Canonical()
	if err != nil || !bytes.Equal(body, raw) || sha != key.SHA || p.Run != key.Run || p.Revision != key.Revision {
		return p, v, model.ErrStrongPlan
	}
	return p, v, nil
}

func (s *Store) StrongRun(ctx context.Context, key StrongRunKey) (StrongRunView, []StrongSlotView, error) {
	var view StrongRunView
	var slots []StrongSlotView
	err := s.WithTx(ctx, func(ctx context.Context) error {
		var err error
		_, view, err = s.strongPlan(ctx, key)
		if err != nil {
			return err
		}
		rows, err := s.q(ctx).QueryContext(ctx, `SELECT slot,state,reason,accepted_attempt,accepted_ref FROM strong_run_slots WHERE run_id=? AND revision=? ORDER BY ordinal`, key.Run, key.Revision)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x StrongSlotView
			var ref []byte
			if err := rows.Scan(&x.Key, &x.State, &x.Reason, &x.AcceptedAttempt, &ref); err != nil {
				return err
			}
			if len(ref) > 0 && json.Unmarshal(ref, &x.Ref) != nil {
				return model.ErrStrongPlan
			}
			slots = append(slots, x)
		}
		return rows.Err()
	})
	return view, slots, err
}

func (s *Store) ApproveStrongPlan(ctx context.Context, key StrongRunKey, actor, receipt string, at time.Time) error {
	if !model.StrongKey(actor) || !model.StrongKey(receipt) || at.IsZero() {
		return model.ErrStrongPlan
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		plan, view, err := s.strongPlan(ctx, key)
		if err != nil {
			return err
		}
		if view.Status == StrongRunning && view.ApprovalID == receipt && view.Approver == actor {
			return nil
		}
		if view.Status != StrongAwaiting || view.ApprovalID != "" {
			return model.ErrStrongPlan
		}
		if _, err := s.StrongTask(ctx, key.Run, plan.TaskSHA); err != nil {
			return model.ErrStrongPlan
		}
		if _, err := s.GetProject(ctx, plan.Project); err != nil {
			return model.ErrStrongPlan
		}
		if _, err := s.strongEvidence(ctx, plan, key); err != nil {
			return model.ErrStrongArtifact
		}
		graph, _ := plan.Graph()
		for i, slot := range graph {
			if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_slots(run_id,revision,slot,ordinal,state,condition) VALUES(?,?,?,?,?,?)`,
				key.Run, key.Revision, slot.Key, i, "pending", slot.Condition); err != nil {
				return err
			}
		}
		x, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_plans SET status=?,reason='',approval_id=?,approver=?,approved_at=? WHERE run_id=? AND revision=? AND status=?`,
			StrongRunning, receipt, actor, formatTime(at), key.Run, key.Revision, StrongAwaiting)
		return rowsAffected(x, err)
	})
}

func (s *Store) setStrongRunState(ctx context.Context, key StrongRunKey, state, reason string) error {
	x, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_plans SET status=?,reason=? WHERE run_id=? AND revision=? AND plan_sha=?`, state, reason, key.Run, key.Revision, key.SHA)
	return rowsAffected(x, err)
}

func (s *Store) setStrongSlotState(ctx context.Context, key StrongRunKey, slot, state, reason string) error {
	x, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_slots SET state=?,reason=? WHERE run_id=? AND revision=? AND slot=?`, state, reason, key.Run, key.Revision, slot)
	return rowsAffected(x, err)
}

func (s *Store) strongDependencies(ctx context.Context, p model.StrongPlan, key StrongRunKey, slot model.StrongSlot) (bool, error) {
	for _, name := range slot.Deps {
		_, state, _, err := s.strongSlotArtifact(ctx, p, key, name)
		if err != nil {
			return false, err
		}
		if state != strongDone && state != strongSkipped {
			return false, nil
		}
	}
	return true, nil
}

func strongIntentIDs(key StrongRunKey, slot string) (string, string, string) {
	d := model.StrongArtifactDigest([]byte(fmt.Sprintf("%s:%d:%s:%s", key.Run, key.Revision, key.SHA, slot)))
	return "intent-" + d, "attempt-" + d, "card-" + d
}

func (s *Store) strongIntent(ctx context.Context, key StrongRunKey, slot string) (StrongIntent, error) {
	x := StrongIntent{Key: key, Slot: slot}
	var raw []byte
	err := s.q(ctx).QueryRowContext(ctx, `SELECT intent_id,attempt_id,card_id,state,route_json,reserved FROM strong_run_intents
		WHERE run_id=? AND revision=? AND slot=?`, key.Run, key.Revision, slot).
		Scan(&x.ID, &x.Attempt, &x.Card, &x.State, &raw, &x.Reserved)
	if err == nil && json.Unmarshal(raw, &x.Route) != nil {
		err = model.ErrStrongPlan
	}
	return x, err
}

func strongEntropy() (string, error) {
	var v [32]byte
	_, err := rand.Read(v[:])
	return hex.EncodeToString(v[:]), err
}

func strongReasonText(s string) bool {
	return len(s) > 0 && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00")
}
