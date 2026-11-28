package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/OttoPrua/Cardex/internal/model"
)

// StrongRunReport is private operator material. Never forward it to an HTTP
// handler or serialize any part of it directly for a redacted export.
type StrongRunReport struct {
	BudgetCheck                    StrongBudgetCheck
	Run                            StrongRunView
	Version                        int
	Template, TaskSHA, EvidenceSHA string
	Budget                         int64
	PriorRevisionSpend             int64
	PriorRevisionSpendKnown        bool
	Stages                         []StrongStageReport
}

type StrongStageReport struct {
	UnverifiedIntentIdentity                             bool `json:",omitempty"`
	ExitCode                                             *int
	ErrorClass                                           string
	RouteMatchesApproved                                 bool
	VerifiedConditionalSkip                              bool
	ReviewMajorDissent                                   *bool
	Slot                                                 string
	Phase                                                model.StrongPhase
	State, Reason                                        string
	Attempt, Outcome                                     string
	Channel, Model, Family                               string
	Attempts                                             int
	ModelDispatches, ModelCalls, ToolCalls               *int64
	Reserved, Charged                                    int64
	ValidatedCharge                                      bool
	SyntheticMeasured, AttemptEstimate, ProviderMeasured *model.Usage
	InputSHA, OutputSHA                                  string
	ArtifactStatus                                       string
	Review                                               map[string]string
	Events                                               []StrongReportEvent
	MoreEvents                                           bool
	RetryNotices                                         int
}
type StrongReportEvent struct{ Type, At string }

type StrongBudgetCheck struct {
	ValidatedStageSpend, ReconstructedReserved int64
	ChargeReconciles, ReservationReconciles    bool
}

// ReadPinnedStrongRunReport accepts only O1's strict, stable checkpointed
// database. Live sidecars and platforms without that non-mutating reader are
// deliberately unsupported; no fallback can open the native WAL.
func ReadPinnedStrongRunReport(ctx context.Context, path string, key StrongRunKey) (StrongRunReport, error) {
	var report StrongRunReport
	err := withSnapshotView(ctx, path, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		report, err = (&Store{}).ReadStrongRunReport(context.WithValue(ctx, txKey{}, tx), key)
		return err
	})
	if err != nil {
		return StrongRunReport{}, err
	}
	return report, nil
}

// ReadStrongRunReport provides one SQLite snapshot with no write, migrations,
// account access, or replay. An absent receipt never implies zero tokens.
func (s *Store) ReadStrongRunReport(ctx context.Context, key StrongRunKey) (StrongRunReport, error) {
	var out StrongRunReport
	err := s.WithTx(ctx, func(ctx context.Context) error {
		p, run, err := s.strongPlan(ctx, key)
		if err != nil {
			return err
		}
		graph, err := p.Graph()
		if err != nil {
			return err
		}
		_, slots, err := s.StrongRun(ctx, key)
		if err != nil {
			return err
		}
		byKey := make(map[string]StrongSlotView, len(slots))
		for _, slot := range slots {
			byKey[slot.Key] = slot
		}
		out = StrongRunReport{Run: run, Version: p.Version, Template: p.Template, TaskSHA: p.TaskSHA, EvidenceSHA: p.EvidenceSHA, Budget: p.GlobalTokens, PriorRevisionSpendKnown: true, Stages: make([]StrongStageReport, 0, len(graph))}
		if key.Revision > 1 {
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT spent FROM strong_run_plans WHERE run_id=? AND revision=?`, key.Run, key.Revision-1).Scan(&out.PriorRevisionSpend); err != nil {
				out.PriorRevisionSpendKnown = false
			}
		}
		verified := make(map[string]bool, len(graph))
		for _, g := range graph {
			status := byKey[g.Key]
			x := StrongStageReport{Slot: g.Key, Phase: g.Phase, State: status.State, Reason: status.Reason, ArtifactStatus: "not_accepted"}
			if x.State == "" {
				x.State = "not_materialized"
			}
			if err := s.readStrongStageUsage(ctx, key, p.Limits[g.Key].Routes, &x); err != nil {
				return err
			}
			if status.State == strongSkipped {
				x.VerifiedConditionalSkip = s.strongReportConditionalSkip(ctx, p, key, g, status)
			}
			if status.AcceptedAttempt != "" || status.Ref.Attempt != "" || status.State == strongDone {
				x.ArtifactStatus = "unverified"
				if run.ExecutionBlockedReason == "" && status.State == strongDone && x.Attempt != "" && status.AcceptedAttempt == x.Attempt &&
					status.Ref.Attempt == x.Attempt && x.InputSHA != "" && x.ValidatedCharge && x.RouteMatchesApproved {
					ref, state, fields, err := s.strongSlotArtifact(ctx, p, key, g.Key)
					if err == nil && state == strongDone && ref == status.Ref &&
						(g.Condition == "" || s.strongReportEnabledCondition(ctx, p, key, g, x.InputSHA, verified)) {
						verified[g.Key] = true
						x.ArtifactStatus = "verified_complete"
						x.OutputSHA = ref.SHA
						if g.Phase == model.StrongPhaseReview || g.Phase == model.StrongPhaseRewrite {
							x.Review = fields
						}
						if g.Phase == model.StrongPhaseRewrite {
							hasDissent := fields["__unresolved"] == "yes"
							x.ReviewMajorDissent = &hasDissent
						}
					}
				}
			}
			out.BudgetCheck.ValidatedStageSpend += x.Charged
			out.BudgetCheck.ReconstructedReserved += x.Reserved
			out.Stages = append(out.Stages, x)
		}
		out.BudgetCheck.ChargeReconciles = out.PriorRevisionSpendKnown && out.PriorRevisionSpend+out.BudgetCheck.ValidatedStageSpend == out.Run.Spent
		out.BudgetCheck.ReservationReconciles = out.BudgetCheck.ReconstructedReserved == out.Run.Reserved
		return nil
	})
	if err != nil {
		return StrongRunReport{}, err
	}
	return out, nil
}

// A disabled conditional stage persists a controller decision with no enabled
// anchor/focus or intent. All slots covered by that decision must agree.
func (s *Store) strongReportConditionalSkip(ctx context.Context, p model.StrongPlan, key StrongRunKey, g model.StrongSlot, status StrongSlotView) bool {
	if g.Condition == "" || status.AcceptedAttempt != "" || status.Ref != (model.StrongArtifactRef{}) {
		return false
	}
	graph, err := p.Graph()
	if err != nil {
		return false
	}
	var boundActor, boundReason string
	for _, slot := range graph {
		if slot.Condition != g.Condition {
			continue
		}
		var state, actor, reason, sha, focus, accepted string
		var raw []byte
		err := s.q(ctx).QueryRowContext(ctx, `SELECT state,condition_actor,condition_reason,condition_sha,condition_focus,accepted_attempt,accepted_ref
    FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`, key.Run, key.Revision, slot.Key).Scan(&state, &actor, &reason, &sha, &focus, &accepted, &raw)
		if err != nil || state != strongSkipped || !model.StrongKey(actor) || !strongReasonText(reason) || sha != "" || focus != "" || accepted != "" || len(raw) != 0 {
			return false
		}
		if boundActor != "" && (actor != boundActor || reason != boundReason) {
			return false
		}
		boundActor, boundReason = actor, reason
		if _, err := s.strongIntent(ctx, key, slot.Key); !errors.Is(err, sql.ErrNoRows) {
			return false
		}
		if ready, err := s.strongDependencies(ctx, p, key, slot); err != nil || !ready {
			return false
		}
	}
	return boundActor != ""
}

// Enabled conditional work must match the complete shared decision, the
// eligible already-verified anchor, and the input bytes the child actually
// received. Reassembling here cannot change the saved workflow.
func (s *Store) strongReportEnabledCondition(ctx context.Context, p model.StrongPlan, key StrongRunKey, g model.StrongSlot, bundleSHA string, verified map[string]bool) bool {
	graph, err := p.Graph()
	if err != nil {
		return false
	}
	type decision struct{ actor, reason, sha, focus string }
	var bound decision
	for _, slot := range graph {
		if slot.Condition != g.Condition {
			continue
		}
		var d decision
		var state string
		err := s.q(ctx).QueryRowContext(ctx, `SELECT state,condition_actor,condition_reason,condition_sha,condition_focus
    FROM strong_run_slots WHERE run_id=? AND revision=? AND slot=?`, key.Run, key.Revision, slot.Key).Scan(&state, &d.actor, &d.reason, &d.sha, &d.focus)
		if err != nil || state == strongSkipped || !model.StrongKey(d.actor) || !strongReasonText(d.reason) || !model.StrongDigestKey(d.sha) || !strongReasonText(d.focus) || (bound.actor != "" && bound != d) {
			return false
		}
		bound = d
	}
	anchor, err := s.strongConditionAnchor(ctx, p, key, g)
	if err != nil || bound.actor == "" || !verified[anchor] {
		return false
	}
	source, ok := strongSource(key, g)
	if !ok {
		return false
	}
	refs, err := s.strongInputRefs(ctx, p, key, g)
	if err != nil {
		return false
	}
	manifest, err := strongInputsSHA(refs)
	if err != nil {
		return false
	}
	current, err := s.currentStrongManifestSHA(ctx, source)
	if err != nil || manifest != current {
		return false
	}
	body, err := assembleStrongBundle(ctx, s, source, p.TaskSHA)
	return err == nil && model.StrongArtifactDigest(body) == bundleSHA
}

func (s *Store) readStrongStageUsage(ctx context.Context, key StrongRunKey, approved []model.StrongApprovedRoute, x *StrongStageReport) error {
	var intent, aid, cid, pin, state string
	var declared model.Usage
	var quote int64
	err := s.q(ctx).QueryRowContext(ctx, `SELECT intent_id,attempt_id,card_id,custody_pin,state,reserved,measured,usage_source,usage_confidence
	  FROM strong_run_intents WHERE run_id=? AND revision=? AND slot=?`, key.Run, key.Revision, x.Slot).
		Scan(&intent, &aid, &cid, &pin, &state, &quote, &declared.Tokens, &declared.Source, &declared.Confidence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// These deterministic identities include the exact approved plan SHA.
	// A syntactically valid same-revision replacement does not own the old
	// receipt, even when all adjacent association rows still agree.
	wantIntent, wantAttempt, wantCard := strongIntentIDs(key, x.Slot)
	if intent != wantIntent || aid != wantAttempt || cid != wantCard {
		x.UnverifiedIntentIdentity = true
		return nil
	}
	in, err := s.strongIntent(ctx, key, x.Slot)
	if err != nil || in.ID != intent || in.Attempt != aid || in.Card != cid {
		return model.ErrStrongPlan
	}
	if state == strongReserved || state == strongClaimed || state == strongStarting || state == strongStarted {
		x.Reserved = quote
	}
	a, err := s.GetAttempt(ctx, aid)
	if err != nil || a.Card != cid {
		return nil
	}
	x.Attempt = aid
	x.Attempts = 1 // Ledger attempt row, not an inference about whether a model answered.
	x.Outcome = string(a.Outcome)
	x.ExitCode = a.Exit
	x.ErrorClass = string(a.ErrorClass)
	if !a.StartedAt.IsZero() || a.PID != 0 || a.Exit != nil {
		x.Channel = a.Route.Channel
		x.Model = a.Route.Model
	}
	if reg, err := s.strongProducer(ctx, aid); err == nil && reg.Card == cid && reg.Worktree == a.Worktree {
		var claimed int
		if c, err := s.captureClaim(ctx, reg, &claimed); err == nil && claimed == 1 && c.Valid() && strongWorkflowCapturePin(c) == pin {
			x.InputSHA = c.BundleSHA
		}
	}
	var e int
	var u model.Usage
	var sealedInput string
	receiptErr := s.q(ctx).QueryRowContext(ctx, `SELECT exit_code,tokens,usage_source,usage_confidence,bundle_sha FROM strong_fake_execution
    WHERE intent_id=? AND attempt_id=? AND custody_pin=?`, in.ID, in.Attempt, pin).Scan(&e, &u.Tokens, &u.Source, &u.Confidence, &sealedInput)
	if receiptErr == nil && (strongExactUsage(u) || u == (model.Usage{})) && x.InputSHA != "" && sealedInput == x.InputSHA && (a.Exit == nil || *a.Exit == e) {
		one := int64(1)
		x.ModelDispatches = &one // Authenticated synthetic child, independently of usage.
		if actualSyntheticStrongRoute(a, in.Route) && knownStrongFamily(in.Route.Family) && slices.Contains(approved, in.Route) {
			x.Family = in.Route.Family
			x.RouteMatchesApproved = true
		}
		if strongExactUsage(u) && a.Exit != nil && a.Usage == u {
			x.SyntheticMeasured = &u
			if (state == strongDone || state == strongStopped) && declared == u {
				x.ValidatedCharge = true
				x.Charged = u.Tokens
			}
		}
	}
	if x.SyntheticMeasured == nil && a.Usage.Tokens > 0 && a.Usage.Source == "estimate" && a.Usage.Confidence > 0 && a.Usage.Confidence <= 1 {
		u := a.Usage
		x.AttemptEstimate = &u
	}
	// ST2/ST3 have one owned execution per slot, no model-internal counts,
	// tool telemetry or provider measurement. Leave those columns unknown.
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT event_type,ts FROM events WHERE entity=? OR entity=? ORDER BY ts,id LIMIT 65`, "attempt:"+aid, "card:"+cid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if len(x.Events) == 64 {
			x.MoreEvents = true
			break
		}
		var item StrongReportEvent
		if err := rows.Scan(&item.Type, &item.At); err != nil {
			return err
		}
		x.Events = append(x.Events, item)
		if item.Type == model.EventCardRetryOffered || item.Type == "retry_backoff" {
			x.RetryNotices++
		}
	}
	return rows.Err()
}
