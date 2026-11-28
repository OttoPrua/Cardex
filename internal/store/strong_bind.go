package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

func (s *Store) strongSlotArtifact(ctx context.Context, p model.StrongPlan, key StrongRunKey, name string) (model.StrongArtifactRef, string, map[string]string, error) {
	var state, attempt string
	var raw []byte
	err := s.q(ctx).QueryRowContext(ctx, `SELECT state,accepted_attempt,accepted_ref FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`,
		key.Run, key.Revision, name).Scan(&state, &attempt, &raw)
	if err != nil {
		return model.StrongArtifactRef{}, "", nil, err
	}
	if state != "done" {
		return model.StrongArtifactRef{}, state, nil, nil
	}
	var ref model.StrongArtifactRef
	slot, ok := p.Slot(name)
	if !ok || len(raw) > 2048 || json.Unmarshal(raw, &ref) != nil || ref.Attempt != attempt {
		return ref, state, nil, model.ErrStrongArtifact
	}
	expectedIntent, err := s.strongIntent(ctx, key, name)
	if err != nil || expectedIntent.State != strongDone || expectedIntent.Attempt != attempt {
		return ref, state, nil, model.ErrStrongArtifact
	}
	var pin string
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT custody_pin FROM strong_run_intents WHERE intent_id=?`, expectedIntent.ID).Scan(&pin); err != nil || s.checkStrongWorkflowCapture(ctx, expectedIntent, pin) != nil {
		return ref, state, nil, model.ErrStrongArtifact
	}
	fields, err := s.strongBoundResult(ctx, p, key, slot, ref)
	return ref, state, fields, err
}

func (s *Store) strongInputRefs(ctx context.Context, p model.StrongPlan, key StrongRunKey, slot model.StrongSlot) ([]model.StrongArtifactRef, error) {
	ready, err := s.strongDependencies(ctx, p, key, slot)
	if err != nil || !ready {
		return nil, model.ErrStrongArtifact
	}
	if slot.Condition != "" {
		if _, err := s.strongConditionAnchor(ctx, p, key, slot); err != nil {
			return nil, err
		}
	}
	var from []model.StrongSlot
	graph, _ := p.Graph()
	include := func(goKey string) {
		if v, ok := p.Slot(goKey); ok {
			from = append(from, v)
		}
	}
	switch slot.Phase {
	case model.StrongPhaseFrame:
	case model.StrongPhaseEvidence:
		if p.Framing {
			include("frame")
		}
	case model.StrongPhaseBlind:
		include("evidence")
	case model.StrongPhasePeer:
		for _, v := range graph {
			if v.Phase == model.StrongPhaseBlind && v.Seat != slot.Seat {
				from = append(from, v)
			}
		}
	case model.StrongPhaseVerify:
		include("evidence")
		anchor, err := s.strongConditionAnchor(ctx, p, key, slot)
		if err != nil {
			return nil, err
		}
		include(anchor)
	case model.StrongPhaseSynthesis, model.StrongPhaseReview, model.StrongPhaseRewrite:
		for _, name := range slot.Deps {
			include(name)
		}
	default:
		return nil, model.ErrStrongPlan
	}
	out := make([]model.StrongArtifactRef, 0, len(from))
	for _, dep := range from {
		ref, state, _, err := s.strongSlotArtifact(ctx, p, key, dep.Key)
		if err != nil || (state != strongDone && state != strongSkipped) {
			return nil, model.ErrStrongArtifact
		}
		if state == strongDone {
			out = append(out, ref)
		}
	}
	if slot.Phase == model.StrongPhaseReview || slot.Phase == model.StrongPhaseRewrite {
		pin, err := s.readStrongReviewPin(ctx, p, key)
		if err != nil || len(out) < 2 || out[0] != pin.Ref {
			return nil, model.ErrStrongArtifact
		}
	}
	return out, nil
}

func (s *Store) strongConditionAnchor(ctx context.Context, p model.StrongPlan, key StrongRunKey, slot model.StrongSlot) (string, error) {
	if slot.Condition == "" {
		return "", nil
	}
	var actor, reason, sha, focus string
	err := s.q(ctx).QueryRowContext(ctx, `SELECT condition_actor,condition_reason,condition_sha,condition_focus FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`, key.Run, key.Revision, slot.Key).Scan(&actor, &reason, &sha, &focus)
	if err != nil || !model.StrongKey(actor) || !strongReasonText(reason) || !model.StrongDigestKey(sha) || !strongReasonText(focus) {
		return "", model.ErrStrongArtifact
	}
	field := "Objection"
	if slot.Condition == model.StrongConditionVerifiableDispute {
		field = "Dispute"
	}
	graph, _ := p.Graph()
	for _, prior := range graph {
		if prior.Phase != model.StrongPhasePeer || (slot.Condition == model.StrongConditionNewObjection && prior.Round != 1) {
			continue
		}
		ref, state, fields, err := s.strongSlotArtifact(ctx, p, key, prior.Key)
		if err != nil {
			return "", err
		}
		if state == strongDone && ref.EnvelopeSHA == sha && fields[field] == focus {
			return prior.Key, nil
		}
	}
	return "", model.ErrStrongArtifact
}

func strongInputsSHA(refs []model.StrongArtifactRef) (string, error) {
	if len(refs) == 0 {
		return model.StrongEmptyManifestSHA(), nil
	}
	body, err := json.Marshal(refs)
	if err != nil {
		return "", err
	}
	return model.StrongArtifactDigest(body), nil
}

// BindStrongStageInputs derives the manifest exclusively from the approved
// graph and exact accepted output rows. The caller cannot provide aliases,
// references or extra materials for a launch.
func (s *Store) BindStrongStageInputs(ctx context.Context, want StrongIntent) (model.StrongStage, string, error) {
	var scope model.StrongStage
	var pin string
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, v, err := s.strongPlan(ctx, want.Key)
		if err != nil || v.Status != StrongRunning || !p.HasExactRewriteCoverage() {
			return model.ErrStrongPlan
		}
		in, err := s.strongIntent(ctx, want.Key, want.Slot)
		if err != nil || in != want || in.State != strongReserved {
			return model.ErrStrongPlan
		}
		slot, ok := p.Slot(in.Slot)
		if !ok {
			return model.ErrStrongPlan
		}
		scope, ok = strongSource(in.Key, slot)
		if !ok {
			return model.ErrStrongPlan
		}
		refs, err := s.strongInputRefs(ctx, p, in.Key, slot)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			pin = model.StrongEmptyManifestSHA()
			return nil
		}
		pin, err = s.BindStrongInputs(ctx, scope, refs)
		return err
	})
	if err != nil {
		return model.StrongStage{}, "", err
	}
	return scope, pin, nil
}

// ResolveStrongCondition lets the controller apply one documented decision to
// preapproved slots. Enabling a step requires a genuine prior accepted receipt
// containing the relevant objection/dispute, not a caller's boolean alone.
func (s *Store) ResolveStrongCondition(ctx context.Context, key StrongRunKey, condition, actor, reason string, enable bool, anchor model.StrongArtifactRef) error {
	if !model.StrongKey(actor) || !strongReasonText(reason) ||
		(condition != model.StrongConditionNewObjection && condition != model.StrongConditionVerifiableDispute) {
		return model.ErrStrongPlan
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, v, err := s.strongPlan(ctx, key)
		if err != nil || v.Status != StrongRunning || !p.HasExactRewriteCoverage() {
			return model.ErrStrongPlan
		}
		graph, _ := p.Graph()
		var affected []model.StrongSlot
		for _, slot := range graph {
			if slot.Condition == condition {
				affected = append(affected, slot)
			}
		}
		if len(affected) == 0 {
			return model.ErrStrongPlan
		}
		sha, focus := "", ""
		if enable {
			needle := "Objection"
			if condition == model.StrongConditionVerifiableDispute {
				needle = "Dispute"
			}
			for _, slot := range graph {
				if slot.Phase != model.StrongPhasePeer || (condition == model.StrongConditionNewObjection && slot.Round != 1) {
					continue
				}
				ref, state, fields, err := s.strongSlotArtifact(ctx, p, key, slot.Key)
				if errors.Is(err, model.ErrStrongArtifact) {
					return err
				}
				if err != nil {
					return err
				}
				if state == strongDone && ref == anchor && fields[needle] != "" {
					sha = ref.EnvelopeSHA
					focus = fields[needle]
					break
				}
			}
			if sha == "" || !strongReasonText(focus) {
				return model.ErrStrongPlan
			}
		} else if anchor != (model.StrongArtifactRef{}) {
			return model.ErrStrongPlan
		}
		for _, slot := range affected {
			ready, err := s.strongDependencies(ctx, p, key, slot)
			if err != nil || !ready {
				return model.ErrStrongPlan
			}
			var oldReason, oldSHA, oldActor, oldFocus, state string
			err = s.q(ctx).QueryRowContext(ctx, `SELECT condition_reason,condition_sha,condition_actor,condition_focus,state FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`,
				key.Run, key.Revision, slot.Key).Scan(&oldReason, &oldSHA, &oldActor, &oldFocus, &state)
			if err != nil {
				return err
			}
			if oldReason != "" {
				if oldReason != reason || oldSHA != sha || oldActor != actor || oldFocus != focus {
					return model.ErrStrongPlan
				}
				continue
			}
			if state != strongSlotPending {
				return model.ErrStrongPlan
			}
			if !enable {
				state = strongSkipped
			}
			_, err = s.q(ctx).ExecContext(ctx, `UPDATE strong_run_slots SET state=?,condition_reason=?,condition_sha=?,condition_actor=?,condition_focus=? WHERE run_id=? AND revision=? AND slot=?`,
				state, reason, sha, actor, focus, key.Run, key.Revision, slot.Key)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func strongWorkflowCapturePin(c model.StrongCaptureClaim) string {
	raw, _ := json.Marshal(c)
	return model.StrongArtifactDigest(raw)
}

func (s *Store) checkStrongWorkflowCapture(ctx context.Context, in StrongIntent, pin string) error {
	if !model.StrongDigestKey(pin) {
		return model.ErrStrongArtifact
	}
	reg, err := s.strongProducer(ctx, in.Attempt)
	if err != nil || reg.Card != in.Card {
		return model.ErrStrongArtifact
	}
	var claimed int
	claim, err := s.captureClaim(ctx, reg, &claimed)
	if err != nil || claimed != 1 || !claim.Valid() || strongWorkflowCapturePin(claim) != pin || s.checkStrongProducer(ctx, reg, &claim) != nil {
		return model.ErrStrongArtifact
	}
	return nil
}

// strongBundleFocus attaches only the documented issue text, never the
// controller's actor, evidence hash, source ID or original review identity.
func (s *Store) strongBundleFocus(ctx context.Context, target model.StrongStage) (string, error) {
	prefix, key, ok := strings.Cut(target.Stage, "-")
	if !ok || !strings.HasPrefix(prefix, "v") ||
		(target.Role != model.StrongVerification && (target.Role != model.StrongWorkflowPeer || !strings.HasPrefix(key, "peer-2-"))) {
		return "", nil
	}
	revision, err := strconv.Atoi(strings.TrimPrefix(prefix, "v"))
	if err != nil || revision < 1 {
		return "", model.ErrStrongPlan
	}
	var sha string
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT plan_sha FROM strong_run_plans WHERE run_id=? AND revision=?`, target.Run, revision).Scan(&sha); err != nil {
		return "", err
	}
	run := StrongRunKey{Run: target.Run, Revision: revision, SHA: sha}
	p, _, err := s.strongPlan(ctx, run)
	if err != nil {
		return "", err
	}
	slot, ok := p.Slot(key)
	if !ok || slot.Condition == "" {
		return "", model.ErrStrongPlan
	}
	expected, ok := strongSource(run, slot)
	if !ok || expected != target {
		return "", model.ErrStrongPlan
	}
	if _, err := s.strongConditionAnchor(ctx, p, run, slot); err != nil {
		return "", err
	}
	var focus string
	err = s.q(ctx).QueryRowContext(ctx, `SELECT condition_focus FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`, target.Run, revision, key).Scan(&focus)
	return focus, err
}
