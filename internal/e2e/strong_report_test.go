package e2e

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/board"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func pinnedStrongFixtureReport(t *testing.T, f *strongFakeFixture, key store.StrongRunKey) (store.StrongRunReport, error) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("strict O1 read unsupported on this platform")
	}
	path := filepath.Join(t.TempDir(), "cardex.db")
	s, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ExecContext(f.ctx, "VACUUM INTO ?", path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	return board.ReadStrongReport(f.ctx, path, key)
}

func strongTokensEqual(tokens *int64, want int64) bool {
	if want == 0 {
		return tokens == nil
	}
	return tokens != nil && *tokens == want
}

func TestStrongRunReportRecomputesUsageAndSealedReview(t *testing.T) {
	f, p, key, r := setupST3(t, model.StrongTemplateLight, "18 passes out of 25. /private-path-canary/file.go")
	advanceST3Author(t, f, p, key, "Incorrect result; task-note-CANARY.")
	strongReviewReserve(t, f, key, "review", r.same, store.StrongReasonReviewSameFamily)
	read := func() store.StrongRunReport {
		t.Helper()
		report, err := pinnedStrongFixtureReport(t, f, key)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	blocked := read()
	if blocked.Stages[2].Reason != store.StrongReasonReviewSameFamily || blocked.Stages[2].Attempts != 0 || blocked.Stages[2].SyntheticMeasured != nil {
		t.Fatalf("review refusal missing in read-only snapshot: %+v", blocked.Stages)
	}
	review := strongReviewReserve(t, f, key, "review", r.cross, "")
	kind := model.EventCardRetryOffered
	if err := f.db.AppendEvent(f.ctx, &model.Event{Entity: "attempt:" + review.Attempt, Type: kind, Data: json.RawMessage(`{"canary":"/private-prompt/PROTECTED-PROMPT-CANARY"}`)}); err != nil {
		t.Fatal(err)
	}
	f.fakeSlot(t, p, review, crossReviewResult(p, "18/25 is 72 percent. replay-metadata-CANARY."), "tokens=11\n", false)
	rewrite := strongReviewReserve(t, f, key, "rewrite", f.route, "")
	f.fakeSlot(t, p, rewrite, rewriteReviewResult(p, "Answer revised; protected identity-CANARY.", "dissent"), "tokens=13\n", false)
	before, err := os.ReadFile(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	report := read()
	out, err := board.RedactedStrongReport(report)
	if err != nil {
		t.Fatal(err)
	}
	private, _ := json.Marshal(report)
	public, _ := json.Marshal(out)
	if len(report.Stages) != 4 || report.Stages[2].OutputSHA == "" || report.Stages[2].InputSHA == "" || report.Stages[2].Review["Recompute"] == "" || report.Stages[2].Family != r.cross.Family || report.Stages[2].RetryNotices != 1 || !bytes.Contains(private, []byte(kind)) {
		t.Fatalf("review receipt missing: %s", private)
	}
	if report.Run.Spent != 38 || report.BudgetCheck.ValidatedStageSpend != 38 || !report.BudgetCheck.ChargeReconciles || !report.BudgetCheck.ReservationReconciles || !strongTokensEqual(out.Report.KnownSyntheticTokens, 38) || out.Report.Result != "review_and_rewrite_with_unresolved_issues" {
		t.Fatalf("unreconciled measured spend or terminal result: %+v %+v", report, out)
	}
	for _, secret := range []string{"CANARY", key.Run, key.SHA, review.Attempt, review.Route.Model, p.EvidenceSHA, p.EvidenceCommit, "private-path-canary", kind} {
		if strings.Contains(string(public), secret) {
			t.Fatalf("leaked %q: %s", secret, public)
		}
	}
	after, err := os.ReadFile(f.dbPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read-only report changed database bytes")
	}

	// The copied fixture's stored terminal flag is not sufficient. Damage
	// the actual sealed review body and authenticated subprocess bundle join.
	tamper, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tamper.Close()

	mutate := func(query string, args ...any) {
		t.Helper()
		res, err := tamper.ExecContext(f.ctx, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("tamper targeted %d rows: %v", n, err)
		}
	}
	redacted := func() board.StrongExport {
		t.Helper()
		o, err := board.RedactedStrongReport(read())
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	// Simulate an exact pre-tightening approval with a missing rewrite
	// counterpart on the same run/revision. Private raw budget history stays
	// visible, but the replacement hash does not own the old child receipts.
	validWire, _, err := p.CanonicalWire()
	if err != nil {
		t.Fatal(err)
	}
	legacy := p
	legacy.Limits = maps.Clone(p.Limits)
	legacy.Limits["rewrite"] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{f.route}}
	legacyWire, legacySHA, err := legacy.CanonicalWire()
	if err != nil || legacy.HasExactRewriteCoverage() {
		t.Fatalf("invalid historical fixture: %v", err)
	}
	mutate("UPDATE strong_run_plans SET plan_json=?,plan_sha=? WHERE run_id=?", legacyWire, legacySHA, key.Run)
	legacyKey := key
	legacyKey.SHA = legacySHA
	history, err := pinnedStrongFixtureReport(t, f, legacyKey)
	if err != nil || history.Run.Key != legacyKey || history.Run.ExecutionBlockedReason != store.StrongReasonRewriteCoverage || history.Run.Spent != 38 || history.BudgetCheck.ChargeReconciles {
		t.Fatalf("legacy disclosure or historical charge lost: %+v %v", history, err)
	}
	for _, stage := range history.Stages {
		if stage.ArtifactStatus == "verified_complete" || stage.OutputSHA != "" || stage.Review != nil || !stage.UnverifiedIntentIdentity || stage.SyntheticMeasured != nil {
			t.Fatalf("incomplete approval vouched for historical artifact: %+v", stage)
		}
	}
	legacyExport, err := board.RedactedStrongReport(history)
	if err != nil || legacyExport.Report.Result != "blocked_by_plan_contract" || legacyExport.Report.KnownSyntheticTokens != nil || legacyExport.Report.KnownSyntheticDispatches != 0 {
		t.Fatalf("legacy approval promoted or usage hidden: %+v %v", legacyExport, err)
	}
	mutate("UPDATE strong_run_plans SET plan_json=?,plan_sha=? WHERE run_id=?", validWire, key.SHA, key.Run)
	mutate("UPDATE strong_run_plans SET status=? WHERE run_id=?", store.StrongCompleted, key.Run)
	if got := redacted(); got.Report.Result != "terminal_evidence_unverified" {
		t.Fatalf("sealed dissent was discarded: %+v", got)
	}
	mutate("UPDATE strong_run_plans SET status=? WHERE run_id=?", store.StrongUnresolved, key.Run)
	mutate("UPDATE strong_run_intents SET state='stopped' WHERE attempt_id=?", review.Attempt)
	stopped := read()
	if got := redacted(); got.Report.Result != "terminal_evidence_unverified" || stopped.Stages[2].ArtifactStatus != "unverified" || stopped.Stages[2].Charged != 11 {
		t.Fatalf("stopped intent accepted or erased legitimate charge: %+v %+v", stopped.Stages[2], got)
	}
	mutate("UPDATE strong_run_intents SET state='done' WHERE attempt_id=?", review.Attempt)
	mutate("UPDATE strong_run_slots SET state='skipped' WHERE run_id=? AND slot='evidence'", key.Run)
	if got := redacted(); got.Report.Result != "terminal_evidence_unverified" {
		t.Fatalf("required stage skipped without proof: %+v", got)
	}
	mutate("UPDATE strong_run_slots SET state='done' WHERE run_id=? AND slot='evidence'", key.Run)
	if _, err := tamper.ExecContext(f.ctx, "UPDATE strong_artifacts SET body=? WHERE attempt_id=?", []byte("## 结论\nFAULT-CANARY"), review.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := tamper.ExecContext(f.ctx, "UPDATE strong_fake_execution SET bundle_sha=? WHERE attempt_id=?", strings.Repeat("9", 64), rewrite.Attempt); err != nil {
		t.Fatal(err)
	}
	damaged := read()
	publicDamaged, err := board.RedactedStrongReport(damaged)
	if err != nil || publicDamaged.Report.Result != "terminal_evidence_unverified" || !strongTokensEqual(publicDamaged.Report.KnownSyntheticTokens, 25) || publicDamaged.Report.UnknownUsageAttempts != 1 ||
		damaged.Stages[2].ArtifactStatus != "unverified" || len(damaged.Stages[2].Review) != 0 || damaged.BudgetCheck.ChargeReconciles {
		t.Fatalf("damaged evidence promoted: %+v %+v %v", damaged, publicDamaged, err)
	}
	rawDamaged, _ := json.Marshal(publicDamaged)
	if bytes.Contains(rawDamaged, []byte("FAULT-CANARY")) {
		t.Fatalf("fault canary leaked: %s", rawDamaged)
	}
}

func TestStrongRunReportKeepsUnknownAndFailedSeparate(t *testing.T) {
	for _, tc := range []struct {
		label, usage string
		expected     int64
	}{
		{"unknown", "no usage telemetry\n", 0}, {"failed structured result", "tokens=5\n", 5},
	} {
		t.Run(tc.label, func(t *testing.T) {
			f, p, key, _ := setupST3(t, model.StrongTemplateLight, "Fixture.")
			in := strongReviewReserve(t, f, key, "evidence", f.route, "")
			_, why, _ := f.fakeSlot(t, p, in, "## 结论\nMalformed phase receipt.", tc.usage, false)
			if why == "" {
				t.Fatal("malformed fixture advanced")
			}
			report, err := pinnedStrongFixtureReport(t, f, key)
			if err != nil {
				t.Fatal(err)
			}
			out, err := board.RedactedStrongReport(report)
			if err != nil || !strongTokensEqual(out.Report.KnownSyntheticTokens, tc.expected) || out.Report.KnownSyntheticDispatches != 1 || report.Stages[0].ArtifactStatus == "verified_complete" || report.Stages[0].ModelCalls != nil || report.Stages[0].ProviderMeasured != nil || (out.Report.UnknownUsageAttempts == 1) != (tc.expected == 0) {
				t.Fatalf("failed/unknown incorrectly measured: %+v %+v %v", report, out, err)
			}
		})
	}
}

func TestStrongRunReportVerifiesActualConditionalSkip(t *testing.T) {
	f := newStrongFake(t)
	p := f.plan(t, "synthetic-reviewed-skip", model.StrongTemplateStrong)
	key := f.approve(t, p)
	for _, slot := range []string{"frame", "evidence", "blind-1", "blind-2", "peer-1-1", "peer-1-2"} {
		s, _ := p.Slot(slot)
		in := f.reserve(t, key, slot)
		_, why, _ := f.fakeSlot(t, p, in, strongReceipt(p, s.Phase, ""), "tokens=7\n", false)
		if why != "" {
			t.Fatalf("settle %s: %s", slot, why)
		}
	}
	if err := f.db.ResolveStrongCondition(f.ctx, key, model.StrongConditionNewObjection, "synthetic-owner", "no new material objection", false, model.StrongArtifactRef{}); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]store.StrongStageReport {
		t.Helper()
		r, err := pinnedStrongFixtureReport(t, f, key)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]store.StrongStageReport{}
		for _, s := range r.Stages {
			out[s.Slot] = s
		}
		return out
	}
	good := read()
	for _, slot := range []string{"peer-2-1", "peer-2-2"} {
		if !good[slot].VerifiedConditionalSkip || good[slot].State != "skipped" {
			t.Fatalf("authentic skip not accepted: %+v", good[slot])
		}
	}
	external, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err := external.ExecContext(f.ctx, "UPDATE strong_run_slots SET condition_actor='' WHERE run_id=? AND slot='peer-2-2'", key.Run); err != nil {
		t.Fatal(err)
	}
	forged := read()
	for _, slot := range []string{"peer-2-1", "peer-2-2"} {
		if forged[slot].VerifiedConditionalSkip {
			t.Fatalf("mismatched decision accepted: %+v", forged[slot])
		}
	}
}

func TestStrongRunReportEnabledGroupRequiresOriginalDecisionAndBundle(t *testing.T) {
	f, p, _, routes := setupST3(t, model.StrongTemplateStrong, "Fixture permitting one targeted verification.")
	p.Run = "synthetic-enabled-condition"
	p.SecondPeerRound = true
	p.TargetedVerify = true
	if err := f.db.SaveStrongTask(f.ctx, p.Run, p.TaskSHA, []byte("Evaluate the fixed synthetic alternatives.")); err != nil {
		t.Fatal(err)
	}
	graph, err := p.Graph()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range graph {
		if _, ok := p.Limits[s.Key]; !ok {
			p.Limits[s.Key] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{f.route}}
		}
	}
	key := f.approve(t, p)
	run := func(slot string, route model.StrongApprovedRoute, body string) model.StrongArtifactRef {
		t.Helper()
		in := strongReviewReserve(t, f, key, slot, route, "")
		ref, why, _ := f.fakeSlot(t, p, in, body, "tokens=7\n", false)
		if why != "" {
			t.Fatalf("%s did not settle: %s", slot, why)
		}
		return ref
	}
	var anchor model.StrongArtifactRef
	for _, g := range graph {
		switch g.Key {
		case "peer-2-1":
			if err := f.db.ResolveStrongCondition(f.ctx, key, model.StrongConditionNewObjection, "synthetic-owner", "review material objection", true, anchor); err != nil {
				t.Fatal(err)
			}
		case "verify":
			if err := f.db.ResolveStrongCondition(f.ctx, key, model.StrongConditionVerifiableDispute, "synthetic-owner", "verify the captured dispute", true, anchor); err != nil {
				t.Fatal(err)
			}
		}
		switch g.Phase {
		case model.StrongPhaseReview:
			run(g.Key, routes.cross, crossReviewResult(p, "Recalculated the synthetic fixture."))
		case model.StrongPhaseRewrite:
			run(g.Key, f.route, rewriteReviewResult(p, "Revised fixture answer.", "fixed"))
		default:
			extra := ""
			if g.Key == "peer-1-1" {
				extra = "Objection: Check scope\nDispute: Check denominator\n"
			}
			ref := run(g.Key, f.route, strongReceipt(p, g.Phase, extra))
			if extra != "" {
				anchor = ref
			}
		}
	}
	read := func() (store.StrongRunReport, board.StrongExport) {
		t.Helper()
		r, err := pinnedStrongFixtureReport(t, f, key)
		if err != nil {
			t.Fatal(err)
		}
		o, err := board.RedactedStrongReport(r)
		if err != nil {
			t.Fatal(err)
		}
		return r, o
	}
	view, good := read()
	if good.Report.Result != "review_and_rewrite_validated" {
		t.Fatalf("legitimate enabled group did not complete: %s %+v", good.Report.Result, view.Stages)
	}
	external, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	update := func(query string, args ...any) {
		t.Helper()
		if _, err := external.ExecContext(f.ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	assertDowngrade := func(slot string) {
		t.Helper()
		view, bad := read()
		if bad.Report.Result != "terminal_evidence_unverified" || !view.BudgetCheck.ChargeReconciles || bad.Report.KnownSyntheticDispatches != good.Report.KnownSyntheticDispatches || !strongTokensEqual(bad.Report.KnownSyntheticTokens, *good.Report.KnownSyntheticTokens) {
			t.Fatalf("tampered group was promoted or genuine usage lost: %+v", bad)
		}
		for _, stage := range view.Stages {
			if stage.Slot == slot && stage.ArtifactStatus != "unverified" {
				t.Fatalf("tampered conditional artifact accepted: %+v", stage)
			}
		}
	}
	update("UPDATE strong_run_slots SET condition_actor='' WHERE run_id=? AND slot='peer-2-2'", key.Run)
	assertDowngrade("peer-2-1")
	update("UPDATE strong_run_slots SET condition_actor='synthetic-owner' WHERE run_id=? AND slot='peer-2-2'", key.Run)
	update("UPDATE strong_run_slots SET condition_focus='different focus' WHERE run_id=? AND slot='verify'", key.Run)
	assertDowngrade("verify")
}
