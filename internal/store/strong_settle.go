package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
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
		model.StrongPhaseReview:    model.StrongWorkflowReview,
		model.StrongPhaseRewrite:   model.StrongWorkflowRewrite,
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
		case "Scope", "Criteria", "Commit", "Evidence", "Facts", "Uncertainty", "Answer", "Assumptions", "Counterexample", "Test", "Critique", "Objection", "Dispute", "Result", "Rationale", "Minority", "Source", "Recompute", "Denominator", "Threshold", "Issues", "Review":
			values[key] = val
		default:
			if !indexedStrongReviewField(key, "Issue-") && !indexedStrongReviewField(key, "Response-") {
				return nil, false
			}
			values[key] = val
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
	case model.StrongPhaseReview:
		_, ok := strongReviewIssues(values)
		return values, ok && values["Source"] == "Material 01" && values["Evidence"] == p.EvidenceSHA && values["Recompute"] != "" &&
			values["Denominator"] != "" && values["Threshold"] != "" && values["Facts"] != "" && values["Uncertainty"] != "" && values["Counterexample"] != ""
	case model.StrongPhaseRewrite:
		return values, len(body) <= 8<<10 && values["Source"] == "Material 01" && values["Review"] == "Material 02" &&
			values["Evidence"] == p.EvidenceSHA && values["Answer"] != "" && values["Rationale"] != ""
	default:
		return nil, false
	}
}

func indexedStrongReviewField(key, prefix string) bool {
	n, ok := strings.CutPrefix(key, prefix)
	i, err := strconv.Atoi(n)
	return ok && err == nil && i >= 1 && i <= 8 && n == fmt.Sprintf("%02d", i)
}

func strongReviewIssues(fields map[string]string) ([]string, bool) {
	raw := fields["Issues"]
	count, err := strconv.Atoi(raw)
	if err != nil || count < 0 || count > 8 || strconv.Itoa(count) != raw {
		return nil, false
	}
	severity := make([]string, 0, count)
	for i := 1; i <= 8; i++ {
		entry := fields[fmt.Sprintf("Issue-%02d", i)]
		if fields[fmt.Sprintf("Response-%02d", i)] != "" {
			return nil, false
		}
		if i > count {
			if entry != "" {
				return nil, false
			}
			continue
		}
		level, why, ok := strings.Cut(entry, "|")
		if !ok || (level != "major" && level != "minor") || !strongReasonText(why) {
			return nil, false
		}
		severity = append(severity, level)
	}
	return severity, true
}

func strongRewriteResponses(severity []string, fields map[string]string) (unresolved, valid bool) {
	if fields["Issues"] != "" {
		return false, false
	}
	for i := 1; i <= 8; i++ {
		entry := fields[fmt.Sprintf("Response-%02d", i)]
		if fields[fmt.Sprintf("Issue-%02d", i)] != "" {
			return false, false
		}
		if i > len(severity) {
			if entry != "" {
				return false, false
			}
			continue
		}
		decision, why, ok := strings.Cut(entry, "|")
		if !ok || (decision != "fixed" && decision != "dissent") || !strongReasonText(why) {
			return false, false
		}
		if decision == "dissent" && severity[i-1] == "major" {
			unresolved = true
		}
	}
	return unresolved, true
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
	if slot.Phase == model.StrongPhaseReview || slot.Phase == model.StrongPhaseRewrite {
		if err := checkKnownReviewIdentities(p, body); err != nil {
			return nil, err
		}

		pin, err := s.readStrongReviewPin(ctx, p, key)
		if err != nil {
			return nil, model.ErrStrongArtifact
		}
		intent, err := s.strongIntent(ctx, key, slot.Key)
		if err != nil || intent.Attempt != ref.Attempt || !slices.Contains(p.Limits[slot.Key].Routes, intent.Route) {
			return nil, model.ErrStrongArtifact
		}
		att, err := s.GetAttempt(ctx, intent.Attempt)
		if err != nil || !actualSyntheticStrongRoute(att, intent.Route) || !knownStrongFamily(intent.Route.Family) {
			return nil, model.ErrStrongArtifact
		}
		if slot.Phase == model.StrongPhaseReview && pin.independent(intent.Route) != "" {
			return nil, model.ErrStrongArtifact
		}
		if slot.Phase == model.StrongPhaseRewrite {
			if intent.Route != pin.Route || slot.Seat != pin.Seat {
				return nil, model.ErrStrongArtifact
			}
			_, state, review, err := s.strongSlotArtifact(ctx, p, key, "review")
			if err != nil || state != strongDone {
				return nil, model.ErrStrongArtifact
			}
			issues, ok := strongReviewIssues(review)
			unresolved, valid := strongRewriteResponses(issues, report)
			if !ok || !valid {
				return nil, model.ErrStrongArtifact
			}
			if unresolved {
				report["__unresolved"] = "yes"
			}
		}
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
		routeMatches := actualSyntheticStrongRoute(att, in.Route)
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
		if reason == "" && !routeMatches {
			reason = StrongReasonRoute
		}
		if reason == "" && (tokens > in.Reserved || v.Spent+tokens > p.GlobalTokens) {
			reason = StrongReasonOverrun
		}

		if reason == "" && (!passedStrongAttempt(att) || ref.Attempt != att.ID || s.checkStrongWorkflowCapture(ctx, in, custodyPin) != nil) {
			reason = StrongReasonArtifact
		}
		var report map[string]string
		if reason == "" {
			report, err = s.strongBoundResult(ctx, p, in.Key, slot, ref)
			if err != nil {
				reason = StrongReasonArtifact
			}
		}
		next := strongDone
		if reason != "" {
			next = strongStopped
		}
		settled, err := s.settleStrongQuota(ctx, p, in, tokens)
		if err != nil {
			return err
		}
		if !settled {
			return block(StrongReasonUnknownUsage)
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
		if err := s.writeStrongReviewPin(ctx, p, in, slot, ref, att); err != nil {
			return err
		}

		if slot.Phase == model.StrongPhaseRewrite {
			if report["__unresolved"] == "yes" {
				return s.setStrongRunState(ctx, in.Key, StrongUnresolved, "material_review_issues_unresolved")
			}
			return s.setStrongRunState(ctx, in.Key, StrongCompleted, "synthetic_review_and_rewrite_done")
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
