package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStrongAuthorCoverageRejectsHistoricalMalformedApprovalBeforeAnyWork(t *testing.T) {
	s := openDB(t)
	ctx := t.Context()
	if err := s.InstallStrongWorkflowPrototype(ctx); err != nil {
		t.Fatal(err)
	}
	a := model.StrongApprovedRoute{Runner: "fake", Channel: "author-a", Model: "model-a", Family: "family-a"}
	b := model.StrongApprovedRoute{Runner: "fake", Channel: "author-b", Model: "model-b", Family: "family-b"}
	p := model.StrongPlan{Run: "synthetic-contract", Revision: 1, Version: model.StrongPlanVersion, Template: model.StrongTemplateLight, Project: "private-synthetic",
		Scope: "Use approved evidence.", TaskSHA: strings.Repeat("a", 64), EvidenceSHA: strings.Repeat("b", 64), EvidenceCommit: strings.Repeat("c", 40), EvidencePaths: []string{"fact.txt"},
		GlobalTokens: 120, Seats: 1, CrossFamilyReview: true, Limits: map[string]model.StrongSlotLimit{
			"evidence": {Tokens: 20, Routes: []model.StrongApprovedRoute{a}},
			"answer":   {Tokens: 20, Routes: []model.StrongApprovedRoute{a, b}},
			"review":   {Tokens: 20, Routes: []model.StrongApprovedRoute{b}},
			"rewrite":  {Tokens: 20, Routes: []model.StrongApprovedRoute{a, b}},
		}}
	if _, _, err := p.Canonical(); err != nil {
		t.Fatalf("covered A+B rejected: %v", err)
	}
	p.Limits["rewrite"] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{a}}
	if _, _, err := p.Canonical(); !errors.Is(err, model.ErrStrongPlan) {
		t.Fatalf("uncovered author B approved: %v", err)
	}
	// Emulate the exact wire and approval the older validation accepted.
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	key := StrongRunKey{Run: p.Run, Revision: p.Revision, SHA: model.StrongArtifactDigest(body)}
	if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_plans(run_id,revision,plan_sha,plan_json,status,approval_id) VALUES(?,?,?,?,?,?)`, key.Run, key.Revision, key.SHA, body, StrongRunning, "old-approval"); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"evidence", "answer"} {
		if d, err := s.ReserveStrongStage(ctx, key, slot, a); err != nil || d.Reason != StrongReasonRewriteCoverage || d.Launch || d.Intent.ID != "" {
			t.Fatalf("uncovered legacy run reserved work: %+v %v", d, err)
		}
	}
	if view, _, err := s.StrongRun(ctx, key); err != nil || view.Status != StrongRunning || view.ExecutionBlockedReason != StrongReasonRewriteCoverage {
		t.Fatalf("unsafe legacy run cannot be safely disclosed: %+v %v", view, err)
	}
	// Exact old committed reservations for work that never obtained a launch
	// lease and work with a possibly consumed lease. Both use real local quota
	// holds; only the provably reserved state can be cleared on cancellation.
	c := model.Channel{ID: a.Channel, SourceGroup: "local-account", Runner: model.RunnerFake, Health: model.HealthOK,
		QuotaWindows: []model.QuotaWindow{{Name: "fixture-window", LimitTokens: 100, Remaining: 1, Confidence: 1, ResetAt: time.Now().Add(time.Hour)}}}
	if err := s.CreateChannel(ctx, &c); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct{ slot, state string }{{"evidence", strongReserved}, {"answer", strongStarted}} {
		id, aid, cid := strongIntentIDs(key, x.slot)
		in := StrongIntent{Key: key, Slot: x.slot, ID: id, Attempt: aid, Card: cid, State: x.state, Route: a, Reserved: 20}
		r, _ := json.Marshal(a)
		if err := s.WithWriteTx(ctx, func(ctx context.Context) error {
			if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_slots(run_id,revision,slot,ordinal,state) VALUES(?,?,?,?,?)`, key.Run, key.Revision, x.slot, 0, x.state); err != nil {
				return err
			}
			if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_intents(run_id,revision,slot,intent_id,attempt_id,card_id,state,route_json,reserved,lease) VALUES(?,?,?,?,?,?,?,?,?,?)`, key.Run, key.Revision, x.slot, id, aid, cid, x.state, r, 20, strings.Repeat("1", 64)); err != nil {
				return err
			}
			if _, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_plans SET reserved=reserved+20 WHERE run_id=?`, key.Run); err != nil {
				return err
			}
			return s.reserveStrongQuota(ctx, p, in)
		}); err != nil {
			t.Fatal(err)
		}
		if got, why, err := s.ClaimStrongIntent(ctx, in); err != nil || got != "" || why != StrongReasonRewriteCoverage {
			t.Fatalf("claim accepted %q %q %v", got, why, err)
		}
		if err := s.VerifyStrongLaunch(ctx, in, strings.Repeat("1", 64), nil, ""); !errors.Is(err, model.ErrStrongPlan) {
			t.Fatalf("final launch accepted: %v", err)
		}
	}
	if err := s.RecoverStrongRun(ctx, key); err != nil {
		t.Fatal(err)
	}
	if v, _, err := s.StrongRun(ctx, key); err != nil || v.Status != StrongBlocked || v.Reason != StrongReasonRewriteCoverage || v.Reserved != 40 {
		t.Fatalf("recovery dropped legacy holds: %+v %v", v, err)
	}
	if err := s.CancelStrongRun(ctx, key, "owner"); err != nil {
		t.Fatal(err)
	}
	if v, _, err := s.StrongRun(ctx, key); err != nil || v.Status != StrongCanceled || v.Reserved != 20 || v.ExecutionBlockedReason != StrongReasonRewriteCoverage {
		t.Fatalf("cancel did not retain active uncertain hold: %+v %v", v, err)
	}
	for slot, want := range map[string]string{"evidence": "released", "answer": "held"} {
		id, _, _ := strongIntentIDs(key, slot)
		var got string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT state FROM strong_run_quota_holds WHERE intent_id=?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("quota %s=%s wanted %s: %v", slot, got, want, err)
		}
	}
	var raw, sha, approval string
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT CAST(plan_json AS TEXT),plan_sha,approval_id FROM strong_run_plans WHERE run_id=?`, key.Run).Scan(&raw, &sha, &approval); err != nil || raw != string(body) || sha != key.SHA || approval != "old-approval" {
		t.Fatalf("legacy approval was reconstructed: %s %s %s %v", raw, sha, approval, err)
	}
	var intents int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM strong_run_intents`).Scan(&intents); err != nil || intents != 2 {
		t.Fatalf("unexpected intents %d: %v", intents, err)
	}
}

func TestStrongReportPhaseRejectsStructuredOtherPhaseFields(t *testing.T) {
	p := model.StrongPlan{Scope: "Exact fixture", EvidenceSHA: strings.Repeat("a", 64), EvidenceCommit: strings.Repeat("b", 40)}
	report := func(phase model.StrongPhase, fields string) []byte {
		return []byte("## 结论\nPhase: " + string(phase) + "\n" + fields)
	}
	answer := "Evidence: " + p.EvidenceSHA + "\nAnswer: first\nAssumptions: fixed\nCounterexample: other\nTest: vary\n"
	evidence := "Commit: " + p.EvidenceCommit + "\nEvidence: " + p.EvidenceSHA + "\nFacts: fixed\nUncertainty: limited\n"
	for phase, base := range map[model.StrongPhase]string{model.StrongPhaseBlind: answer, model.StrongPhaseEvidence: evidence} {
		for _, impostor := range []string{"Source: Material 01", "Review: Material 02", "Recompute: fake", "Denominator: 9", "Threshold: above", "Issues: 1", "Issue-01: major|wrong", "Issue-000: malformed", "Response-01: fixed|wrong", "Response-9: malformed"} {
			if _, ok := strongReport(p, model.StrongSlot{Phase: phase}, report(phase, base+impostor+"\n")); ok {
				t.Fatalf("%s controlled by %q", phase, impostor)
			}
			if _, ok := strongReport(p, model.StrongSlot{Phase: phase}, report(phase, base+"\n"+impostor+"\n")); !ok {
				t.Fatalf("%s rejected harmless lower Markdown %q", phase, impostor)
			}
		}
	}
	r := "Source: Material 01\nEvidence: " + p.EvidenceSHA + "\nRecompute: expected\nDenominator: fixed\nThreshold: correct\nFacts: anchored\nUncertainty: known\nCounterexample: none\nIssues: 1\nIssue-01: major|fix this\n"
	w := "Source: Material 01\nReview: Material 02\nEvidence: " + p.EvidenceSHA + "\nAnswer: replaced\nRationale: based on review\nResponse-01: fixed|corrected\n"
	if _, ok := strongReport(p, model.StrongSlot{Phase: model.StrongPhaseReview}, report(model.StrongPhaseReview, r)); !ok {
		t.Fatal("valid review rejected")
	}
	if fields, ok := strongReport(p, model.StrongSlot{Phase: model.StrongPhaseRewrite}, report(model.StrongPhaseRewrite, w)); !ok {
		t.Fatal("valid rewrite rejected")
	} else if _, ok := strongRewriteResponses([]string{"major"}, fields); !ok {
		t.Fatal("valid rewrite issue receipt rejected")
	}
}
