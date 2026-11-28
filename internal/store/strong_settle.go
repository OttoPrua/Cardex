package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

func strongSource(key StrongRunKey, slot model.StrongSlot) (model.StrongStage, bool) {
	mapping := map[model.StrongPhase]model.StrongRole{
		model.StrongPhaseFrame:     model.StrongFraming,
		model.StrongPhaseEvidence:  model.StrongWorkflowEvidence,
		model.StrongPhaseBlind:     model.StrongWorkflowBlind,
		model.StrongPhasePeer:      model.StrongWorkflowPeer,
		model.StrongPhaseVerify:    model.StrongVerification,
		model.StrongPhaseSynthesis: model.StrongWorkflowSynthesis,
	}
	role, ok := mapping[slot.Phase]
	scope := model.StrongStage{Run: key.Run, Stage: fmt.Sprintf("v%d-%s", key.Revision, slot.Key), Seat: slot.Seat, Role: role}
	return scope, ok && scope.Valid()
}

func (s *Store) StrongStageSource(ctx context.Context, key StrongRunKey, name string) (model.StrongStage, error) {
	p, _, err := s.strongPlan(ctx, key)
	if err != nil {
		return model.StrongStage{}, err
	}
	slot, ok := p.Slot(name)
	if !ok {
		return model.StrongStage{}, model.ErrStrongPlan
	}
	x, ok := strongSource(key, slot)
	if !ok {
		return model.StrongStage{}, model.ErrStrongPlan
	}
	return x, nil
}

// strongReport validates a bounded V1 stage receipt, not the factual truth
// of free-form prose. ST3 may not treat this contract as a review.
func strongReport(p model.StrongPlan, slot model.StrongSlot, body []byte) (map[string]string, bool) {
	if !model.StrongResultMarkdown(body) || len(body) > 64<<10 {
		return nil, false
	}
	_, rest, ok := bytes.Cut(body, []byte{'\n'})
	if !ok {
		return nil, false
	}
	lines := strings.Split(string(rest), "\n")
	if len(lines) < 2 || strings.TrimSuffix(lines[0], "\r") != "Phase: "+string(slot.Phase) {
		return nil, false
	}
	values := map[string]string{}
	for _, line := range lines[1:] {
		line = strings.TrimSuffix(line, "\r")
		// The structured receipt ends at the first blank line. Literal prose,
		// code fences or quoted Markdown below it never become control fields.
		if line == "" {
			break
		}
		key, val, ok := strings.Cut(line, ": ")
		if !ok || val == "" || strings.TrimSpace(val) != val || len(val) > 4096 || values[key] != "" {
			return nil, false
		}
		switch key {
		case "Scope", "Criteria", "Commit", "Evidence", "Facts", "Uncertainty", "Answer", "Assumptions", "Counterexample", "Test", "Critique", "Objection", "Dispute", "Result", "Rationale", "Minority":
			values[key] = val
		default:
			return nil, false
		}
	}
	switch slot.Phase {
	case model.StrongPhaseFrame:
		return values, values["Scope"] == p.Scope && values["Criteria"] != ""
	case model.StrongPhaseEvidence:
		return values, values["Commit"] == p.EvidenceCommit && values["Evidence"] == p.EvidenceSHA && values["Facts"] != "" && values["Uncertainty"] != ""
	case model.StrongPhaseBlind:
		return values, values["Evidence"] == p.EvidenceSHA && values["Answer"] != "" && values["Assumptions"] != "" && values["Counterexample"] != "" && values["Test"] != ""
	case model.StrongPhasePeer:
		return values, values["Critique"] != ""
	case model.StrongPhaseVerify:
		return values, values["Evidence"] == p.EvidenceSHA && (values["Result"] == "support" || values["Result"] == "refute" || values["Result"] == "insufficient") && values["Rationale"] != ""
	case model.StrongPhaseSynthesis:
		return values, len(body) <= 8<<10 && values["Evidence"] == p.EvidenceSHA && values["Answer"] != "" && values["Rationale"] != "" && values["Minority"] != ""
	default:
		return nil, false
	}
}

func (s *Store) strongBoundResult(ctx context.Context, p model.StrongPlan, key StrongRunKey, slot model.StrongSlot, ref model.StrongArtifactRef) (map[string]string, error) {
	if _, err := s.strongEvidence(ctx, p, key); err != nil {
		return nil, model.ErrStrongArtifact
	}
	expected, ok := strongSource(key, slot)
	if !ok || ref.Run != key.Run || ref.Stage != expected.Stage {
		return nil, model.ErrStrongArtifact
	}
	got, body, _, err := s.readStrongArtifact(ctx, ref)
	if err != nil || got != expected {
		return nil, model.ErrStrongArtifact
	}
	if err := s.CheckStrongAnonymity(ctx, expected, p.TaskSHA, body); err != nil {
		return nil, model.ErrStrongArtifact
	}
	report, ok := strongReport(p, slot, body)
	if !ok {
		return nil, model.ErrStrongArtifact
	}
	return report, nil
}

func actualSyntheticStrongRoute(a model.Attempt, want model.StrongApprovedRoute) bool {
	if want.Runner != string(model.RunnerFake) || want.T0 || a.Route.Channel != want.Channel || a.Route.Model != want.Model ||
		a.RouteReason.Dispatch == nil || a.RouteReason.Dispatch.Runner != model.RunnerFake ||
		a.RouteReason.Dispatch.Channel != want.Channel || a.RouteReason.Dispatch.Model != want.Model {
		return false
	}
	var correct int
	for _, candidate := range a.RouteReason.Candidates {
		if candidate.Channel == want.Channel && candidate.Model == want.Model {
			if candidate.Family != want.Family {
				return false
			}
			correct++
		}
	}
	return correct == 1
}

func strongExactUsage(u model.Usage) bool {
	return u.Tokens > 0 && u.Tokens <= model.StrongTokenCeiling && u.Source == "strong_fake_child" && u.Confidence == 1
}

// SettleStrong accounts one lease without launching or replaying a process.
// A successful ordinary attempt is never sufficient: the approved exact ST1
// envelope and this phase's report contract are required to advance.
func (s *Store) SettleStrong(ctx context.Context, want StrongIntent, lease string, ref model.StrongArtifactRef) (string, error) {
	if !model.StrongDigestKey(lease) {
		return "", model.ErrStrongPlan
	}
	var reason string
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, v, err := s.strongPlan(ctx, want.Key)
		if err != nil {
			return err
		}
		in, err := s.strongIntent(ctx, want.Key, want.Slot)
		if err != nil || !sameStrongIntent(in, want) {
			return model.ErrStrongPlan
		}
		slot, ok := p.Slot(in.Slot)
		if !ok {
			return model.ErrStrongPlan
		}
		var durableLease, custodyPin string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT lease,custody_pin FROM strong_run_intents WHERE intent_id=?`, in.ID).Scan(&durableLease, &custodyPin); err != nil || lease != durableLease {
			return model.ErrStrongPlan
		}
		if in.State == strongDone || in.State == strongStopped {
			var accepted []byte
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT reason,accepted_ref FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`,
				in.Key.Run, in.Key.Revision, in.Slot).Scan(&reason, &accepted); err != nil {
				return err
			}
			if in.State == strongDone {
				var old model.StrongArtifactRef
				if json.Unmarshal(accepted, &old) != nil || old != ref {
					return model.ErrStrongPlan
				}
			}
			return nil
		}
		if in.State != strongStarted && in.State != strongStarting {
			return model.ErrStrongPlan
		}
		advance := v.Status == StrongRunning || (v.Status == StrongBlocked && v.Reason == StrongReasonUnknownLaunch)
		block := func(why string) error {
			reason = why
			if err := s.setStrongSlotState(ctx, in.Key, in.Slot, in.State, why); err != nil {
				return err
			}
			if advance {
				return s.setStrongRunState(ctx, in.Key, StrongBlocked, why)
			}
			return nil
		}
		att, err := s.GetAttempt(ctx, in.Attempt)
		if err != nil || att.Card != in.Card || att.EndedAt.IsZero() || att.Exit == nil {
			return block(StrongReasonUnknownLaunch)
		}
		if !actualSyntheticStrongRoute(att, in.Route) {
			return block(StrongReasonRoute)
		}
		exit, usage, err := s.strongMeasuredUsage(ctx, in, custodyPin)
		if err != nil || exit != *att.Exit || usage != att.Usage || !strongExactUsage(usage) {
			return block(StrongReasonUnknownUsage)
		}
		tokens := usage.Tokens
		if !advance {
			reason = v.Reason
			if reason == "" {
				reason = "run_not_advancing"
			}
		}
		if reason == "" && (tokens > in.Reserved || v.Spent+tokens > p.GlobalTokens) {
			reason = StrongReasonOverrun
		}

		if reason == "" && (!passedStrongAttempt(att) || ref.Attempt != att.ID || s.checkStrongWorkflowCapture(ctx, in, custodyPin) != nil) {
			reason = StrongReasonArtifact
		}
		if reason == "" {
			if _, err := s.strongBoundResult(ctx, p, in.Key, slot, ref); err != nil {
				reason = StrongReasonArtifact
			}
		}
		next := strongDone
		if reason != "" {
			next = strongStopped
		}
		r, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_plans SET reserved=reserved-?,spent=spent+? WHERE run_id=? AND revision=? AND reserved>=?`,
			in.Reserved, tokens, in.Key.Run, in.Key.Revision, in.Reserved)
		if err := rowsAffected(r, err); err != nil {
			return err
		}
		r, err = s.q(ctx).ExecContext(ctx, `UPDATE strong_run_intents SET state=?,measured=?,usage_source=?,usage_confidence=? WHERE intent_id=? AND state IN (?,?)`,
			next, tokens, att.Usage.Source, att.Usage.Confidence, in.ID, strongStarted, strongStarting)
		if err := rowsAffected(r, err); err != nil {
			return err
		}
		if reason != "" {
			if err := s.setStrongSlotState(ctx, in.Key, in.Slot, next, reason); err != nil {
				return err
			}
			if advance {
				return s.setStrongRunState(ctx, in.Key, StrongBlocked, reason)
			}
			return nil
		}
		raw, _ := json.Marshal(ref)
		r, err = s.q(ctx).ExecContext(ctx, `UPDATE strong_run_slots SET state=?,reason='',accepted_ref=?,accepted_attempt=? WHERE run_id=? AND revision=? AND slot=?`,
			next, raw, att.ID, in.Key.Run, in.Key.Revision, in.Slot)
		if err := rowsAffected(r, err); err != nil {
			return err
		}
		if v.Status == StrongBlocked && v.Reason == StrongReasonUnknownLaunch {
			var remaining int
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM strong_run_intents i LEFT JOIN strong_producers p ON p.attempt_id=i.attempt_id
			WHERE i.run_id=? AND i.revision=? AND (i.state IN (?,?,?) OR (i.state=? AND p.claimed=1))`,
				in.Key.Run, in.Key.Revision, strongClaimed, strongStarting, strongStarted, strongReserved).Scan(&remaining); err != nil {
				return err
			}
			if remaining == 0 {
				return s.setStrongRunState(ctx, in.Key, StrongRunning, "")
			}
		}
		return nil
	})
	return reason, err
}

// ReconcileStrongStage recovers by exact durable custody and ledger. It
// neither hands out the old lease nor asks a runner to perform another call.
func (s *Store) ReconcileStrongStage(ctx context.Context, key StrongRunKey, slot string) (string, error) {
	result := ""
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		_, v, err := s.strongPlan(ctx, key)
		if err != nil {
			return err
		}
		if v.Status != StrongBlocked && v.Status != StrongCanceled {
			return model.ErrStrongPlan
		}
		in, err := s.strongIntent(ctx, key, slot)
		if err != nil {
			return err
		}
		if in.State == strongClaimed {
			result = StrongReasonUnknownLaunch
			return nil
		}
		if in.State != strongStarting && in.State != strongStarted {
			return model.ErrStrongPlan
		}
		var lease string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT lease FROM strong_run_intents WHERE intent_id=?`, in.ID).Scan(&lease); err != nil {
			return err
		}
		var ref model.StrongArtifactRef
		_ = s.q(ctx).QueryRowContext(ctx, `SELECT run_id,stage_id,attempt_id,origin,format,sha,size,envelope_sha
			FROM strong_artifacts WHERE attempt_id=?`, in.Attempt).Scan(&ref.Run, &ref.Stage, &ref.Attempt, &ref.Origin, &ref.Format, &ref.SHA, &ref.Size, &ref.EnvelopeSHA)
		result, err = s.SettleStrong(ctx, in, lease, ref)
		return err
	})
	return result, err
}
