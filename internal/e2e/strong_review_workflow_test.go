package e2e

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/strongevidence"
)

type syntheticReviewRoutes struct{ cross, same, sameModel, unknown, exhausted model.StrongApprovedRoute }

func setupST3(t *testing.T, template, fact string) (*strongFakeFixture, model.StrongPlan, store.StrongRunKey, syntheticReviewRoutes) {
	t.Helper()
	f := newStrongFake(t)
	if err := os.WriteFile(filepath.Join(f.repo, "fact.txt"), []byte(fact+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	syntheticStrongGit(t, f.repo, "add", "fact.txt")
	syntheticStrongGit(t, f.repo, "-c", "user.name=CardeX", "-c", "user.email=example@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "approved formula evidence")
	f.base = syntheticStrongGit(t, f.repo, "rev-parse", "HEAD")
	f.bare = filepath.Join(t.TempDir(), "review.git")
	syntheticStrongGit(t, f.repo, "clone", "--no-hardlinks", "--bare", f.repo, f.bare)
	var err error
	f.snapshot, err = strongevidence.Open(f.ctx, f.bare, f.base, []string{"fact.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f.evidence, err = f.snapshot.Record().Digest()
	if err != nil {
		t.Fatal(err)
	}
	p := f.plan(t, "cross-synthetic", template)
	p.CrossFamilyReview = true
	p.Framing, p.TargetedVerify, p.SecondPeerRound = false, false, false
	p.Limits = map[string]model.StrongSlotLimit{}
	g, err := p.Graph()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range g {
		p.Limits[s.Key] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{f.route}}
	}
	r := syntheticReviewRoutes{
		cross:     model.StrongApprovedRoute{Runner: "fake", Channel: "review-channel", Model: "review-model", Family: "review-family"},
		same:      model.StrongApprovedRoute{Runner: "fake", Channel: "other-handle", Model: "other-model", Family: "FAKE-FAMILY"},
		sameModel: model.StrongApprovedRoute{Runner: "fake", Channel: "same-model-handle", Model: strings.ToUpper(f.route.Model), Family: f.route.Family},
		unknown:   model.StrongApprovedRoute{Runner: "fake", Channel: "unknown-handle", Model: "unknown-model", Family: "unknown"},
		exhausted: model.StrongApprovedRoute{Runner: "fake", Channel: "empty-handle", Model: "empty-model", Family: "third-family"},
	}
	for _, route := range []model.StrongApprovedRoute{f.route, r.cross, r.same, r.sameModel, r.unknown, r.exhausted} {
		left := 0.75
		if route == r.exhausted {
			left = 0
		}
		ch := model.Channel{ID: route.Channel, Runner: model.RunnerFake, Family: route.Family, Health: model.HealthOK,
			SourceGroup: route.Channel, Models: []string{route.Model}, Redline: 0.1, QuotaWindows: []model.QuotaWindow{{Name: "synthetic-window",
				LimitTokens: 1000, Remaining: left, Confidence: 1, ResetAt: time.Now().Add(time.Hour)}}}
		if err := f.db.CreateChannel(f.ctx, &ch); err != nil {
			t.Fatal(err)
		}
		m := model.Positioning{ID: "position-" + route.Channel, Model: route.Model, Family: route.Family, Tiers: []model.Tier{model.TierT1}}
		if err := f.db.CreatePositioning(f.ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	p.Limits[p.StrongAuthorSlot()] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{f.route, r.same}}
	p.Limits["rewrite"] = p.Limits[p.StrongAuthorSlot()]
	p.Limits["review"] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{r.same, r.sameModel, r.unknown, r.exhausted, r.cross}}
	return f, p, f.approve(t, p), r
}

func strongReviewReserve(t *testing.T, f *strongFakeFixture, key store.StrongRunKey, slot string, route model.StrongApprovedRoute, want string) store.StrongIntent {
	t.Helper()
	d, err := f.db.ReserveStrongStage(f.ctx, key, slot, route)
	if err != nil || d.Reason != want || (want != "" && d.Intent.ID != "") || (want == "" && d.Intent.ID == "") {
		t.Fatalf("%s reserve: %+v %v", slot, d, err)
	}
	return d.Intent
}

func advanceST3Author(t *testing.T, f *strongFakeFixture, p model.StrongPlan, key store.StrongRunKey, wrong string) {
	t.Helper()
	graph, _ := p.Graph()
	for _, s := range graph {
		if s.Phase == model.StrongPhaseReview {
			break
		}
		body := strongReceipt(p, s.Phase, "")
		if s.Key == p.StrongAuthorSlot() {
			if s.Phase == model.StrongPhaseBlind {
				body = strings.Replace(body, "synthetic recommendation", wrong, 1)
			}
			if s.Phase == model.StrongPhaseSynthesis {
				body = strings.Replace(body, "keep threshold provisional", wrong, 1)
			}
		}
		in := strongReviewReserve(t, f, key, s.Key, f.route, "")
		ref, why, _ := f.fakeSlot(t, p, in, body, "tokens=7\n", false)
		if why != "" || ref.Attempt == "" {
			t.Fatalf("author predecessor %s: %s", s.Key, why)
		}
	}
}

func crossReviewResult(p model.StrongPlan, math string) string {
	return "## 结论\nPhase: review\nSource: Material 01\nEvidence: " + p.EvidenceSHA +
		"\nRecompute: " + math + "\nDenominator: independently read approved inputs\nThreshold: use the declared rule direction\nFacts: separate committed inputs from inference\nUncertainty: fixture only\nCounterexample: alternate denominator reverses the verdict\nIssues: 2\nIssue-01: major|Incorrect acceptance rule\nIssue-02: minor|State scope in the conclusion\n"
}

func rewriteReviewResult(p model.StrongPlan, answer, decision string) string {
	return "## 结论\nPhase: rewrite\nSource: Material 01\nReview: Material 02\nEvidence: " + p.EvidenceSHA +
		"\nAnswer: " + answer + "\nRationale: changed the decision using the exact review data\nResponse-01: " + decision + "|Scope of the change is disclosed\nResponse-02: fixed|Added a fixture scope qualifier\n"
}

func TestStrongCrossFamilyReviewAndOneActualAuthorRewrite(t *testing.T) {
	for _, tc := range []struct{ template, fact, wrong, math, answer, decision, status string }{
		{model.StrongTemplateLight, "Eligible=25; Passing=18; minimum=75 percent.", "18 divided by 20 is 90 percent, approve.",
			"18 divided by 25 is 72 percent, below the 75 percent minimum.", "Do not approve at 72 percent.", "fixed", store.StrongCompleted},
		{model.StrongTemplateStrong, "Baseline=8; requested=10; reject above a 20 percent increase.", "Increasing from 8 to 10 is allowed under a 20 percent cap.",
			"Increase equals 2 divided by 8, or 25 percent, above the 20 percent cap.", "Dissent remains recorded concerning the proposed cap interpretation.", "dissent", store.StrongUnresolved},
	} {
		t.Run(tc.template, func(t *testing.T) {
			f, p, key, r := setupST3(t, tc.template, tc.fact)
			strongReviewReserve(t, f, key, "review", r.cross, store.StrongReasonDependency)
			advanceST3Author(t, f, p, key, tc.wrong)
			strongReviewReserve(t, f, key, "review", r.same, store.StrongReasonReviewSameFamily)
			strongReviewReserve(t, f, key, "review", r.sameModel, store.StrongReasonReviewSameModel)
			strongReviewReserve(t, f, key, "review", r.unknown, store.StrongReasonReviewFamilyUnknown)
			strongReviewReserve(t, f, key, "review", r.exhausted, store.StrongReasonReviewQuota)
			strongReviewReserve(t, f, key, "rewrite", f.route, store.StrongReasonDependency)
			review := strongReviewReserve(t, f, key, "review", r.cross, "")
			ref, why, payload := f.fakeSlot(t, p, review, crossReviewResult(p, tc.math), "tokens=7\n", false)
			var observed struct {
				Evidence  []strongevidence.File `json:"evidence"`
				Materials []struct{ Body string }
			}
			if why != "" || ref.Attempt != review.Attempt || json.Unmarshal(payload, &observed) != nil || len(observed.Evidence) != 1 ||
				string(observed.Evidence[0].Body) != tc.fact+"\n" || len(observed.Materials) != 2 || !strings.Contains(observed.Materials[0].Body, tc.wrong) ||
				bytes.Contains(payload, []byte(r.cross.Channel)) || bytes.Contains(payload, []byte(f.route.Family)) {
				t.Fatalf("unbound/incomplete review evidence: %s %+v", why, observed)
			}
			strongReviewReserve(t, f, key, "rewrite", r.same, store.StrongReasonReviewAuthor)
			rewrite := strongReviewReserve(t, f, key, "rewrite", f.route, "")
			if again := strongReviewReserve(t, f, key, "rewrite", f.route, ""); again != rewrite {
				t.Fatal("rewrite duplicated before claim")
			}
			rf, why, raw := f.fakeSlot(t, p, rewrite, rewriteReviewResult(p, tc.answer, tc.decision), "tokens=7\n", false)
			if why != "" || rf.Attempt != rewrite.Attempt || !bytes.Contains(raw, []byte(tc.math)) {
				t.Fatalf("rewrite was not bound to exact review: %s", why)
			}
			v, slots, err := f.db.StrongRun(f.ctx, key)
			if err != nil || v.Status != tc.status || v.Reserved != 0 || len(slots) < 2 || slots[len(slots)-1].AcceptedAttempt != rewrite.Attempt {
				t.Fatalf("workflow receipt: %+v %v", v, err)
			}
			if err := f.db.CancelStrongRun(f.ctx, key, "synthetic-owner"); err != nil {
				t.Fatal(err)
			}
			after, _, err := f.db.StrongRun(f.ctx, key)
			if err != nil || after != v {
				t.Fatalf("completed synthetic state changed on cancellation: %+v %+v %v", v, after, err)
			}

			for range 2 {
				d, err := f.db.ReserveStrongStage(f.ctx, key, "rewrite", f.route)
				if err != nil || d.Intent.ID != "" {
					t.Fatalf("rewrite replay reserved again: %+v %v", d, err)
				}
			}
		})
	}
}

func TestStrongCrossFamilyStopsWhenPinDisappearsOrActualReviewDiffers(t *testing.T) {
	for _, scenario := range []string{"pin removed", "actual route differs"} {
		t.Run(scenario, func(t *testing.T) {
			f, p, key, r := setupST3(t, model.StrongTemplateLight, "Eligible=25, passing=18.")
			advanceST3Author(t, f, p, key, "Wrong denominator 20.")
			review := strongReviewReserve(t, f, key, "review", r.cross, "")
			if scenario == "pin removed" {
				_, cap := f.prepared(t, p, review)
				defer cap.Close()
				c, err := f.db.GetChannel(f.ctx, r.cross.Channel)
				if err != nil {
					t.Fatal(err)
				}
				c.Models = nil
				if err := f.db.UpdateChannel(f.ctx, &c); err != nil {
					t.Fatal(err)
				}
				if lease, why, err := f.db.ClaimStrongIntent(f.ctx, review); err != nil || lease != "" || why != store.StrongReasonReviewFamilyUnknown {
					t.Fatalf("released candidate launched: %s %s %v", lease, why, err)
				}
				strongReviewReserve(t, f, key, "review", r.same, store.StrongReasonReviewSameFamily)
			} else {
				_, why, _ := f.fakeSlot(t, p, review, crossReviewResult(p, "Use 18 divided by 25."), "tokens=7\n", false, func() {
					a, err := f.db.GetAttempt(f.ctx, review.Attempt)
					if err != nil {
						t.Fatal(err)
					}
					a.Route.Model = "different-model"
					if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
						t.Fatal(err)
					}
				})
				if why != store.StrongReasonRoute {
					t.Fatalf("mismatched actual was accepted: %s", why)
				}
				strongReviewReserve(t, f, key, "rewrite", f.route, store.StrongReasonRoute)
			}
		})
	}
}

func TestStrongCrossFamilyReviewPlanRequiresExplicitAuthorRewriteRoute(t *testing.T) {
	_, p, _, r := setupST3(t, model.StrongTemplateLight, "Synthetic fixed constraints.")
	p.Limits["rewrite"] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{r.cross}}
	if _, _, err := p.Canonical(); !errors.Is(err, model.ErrStrongPlan) {
		t.Fatalf("unauthorized author replacement approved: %v", err)
	}
	if s, ok := p.Slot("rewrite"); !ok || s.Seat != "seat-1" {
		t.Fatalf("missing explicit author seat, got %s", fmt.Sprint(s))
	}
}

func TestStrongCrossFamilyFinalStartRechecksQuotaAndPersistsDenial(t *testing.T) {
	f, p, key, r := setupST3(t, model.StrongTemplateLight, "Eligible=25, passing=18.")
	advanceST3Author(t, f, p, key, "Wrong denominator 20.")
	review := strongReviewReserve(t, f, key, "review", r.cross, "")
	_, cap := f.prepared(t, p, review)
	lease, why, err := f.db.ClaimStrongIntent(f.ctx, review)
	if err != nil || why != "" || lease == "" {
		t.Fatalf("approved state could not claim: %q %v", why, err)
	}
	ch, err := f.db.GetChannel(f.ctx, r.cross.Channel)
	if err != nil {
		t.Fatal(err)
	}
	ch.QuotaWindows[0].ResetAt = time.Now().Add(-time.Minute)
	if err := f.db.UpdateChannel(f.ctx, &ch); err != nil {
		t.Fatal(err)
	}
	if err := f.db.VerifyStrongLaunch(f.ctx, review, lease, cap, ""); !errors.Is(err, model.ErrStrongPlan) || !strings.Contains(err.Error(), store.StrongReasonReviewQuota) {
		t.Fatalf("expired quota reached start: %v", err)
	}
	v, slots, err := f.db.StrongRun(f.ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, slot := range slots {
		if slot.Key == "review" && slot.State == "stopped" && slot.Reason == store.StrongReasonReviewQuota {
			found = true
		}
	}
	if !found || v.Status != store.StrongBlocked || v.Reserved != 0 {
		t.Fatalf("no durable no-start release: %+v %+v", v, slots)
	}
}

func TestStrongCrossFamilyRewriteNeedsEveryIssueAndDoesNotRepeatOnFailure(t *testing.T) {
	f, p, key, r := setupST3(t, model.StrongTemplateLight, "Eligible=25, passing=18.")
	advanceST3Author(t, f, p, key, "Wrong denominator 20.")
	review := strongReviewReserve(t, f, key, "review", r.cross, "")
	f.fakeSlot(t, p, review, crossReviewResult(p, "Use 18 divided by 25."), "tokens=7\n", false)
	rewrite := strongReviewReserve(t, f, key, "rewrite", f.route, "")
	incomplete := strings.Replace(rewriteReviewResult(p, "result", "fixed"), "Response-02: fixed|Added a fixture scope qualifier\n", "", 1)
	_, why, _ := f.fakeSlot(t, p, rewrite, incomplete, "tokens=7\n", false)
	if why != store.StrongReasonArtifact {
		t.Fatalf("missing numbered disposition accepted: %s", why)
	}
	strongReviewReserve(t, f, key, "rewrite", f.route, store.StrongReasonArtifact)
}

func TestStrongST3ApprovalRejectsConcreteIdentityRelabels(t *testing.T) {
	f, p, key, r := setupST3(t, model.StrongTemplateLight, "A bounded fixture.")
	for tag, changed := range map[string]model.StrongApprovedRoute{
		"exact route":            {Runner: f.route.Runner, Channel: f.route.Channel, Model: f.route.Model, Family: r.cross.Family},
		"case and account alias": {Runner: f.route.Runner, Channel: r.cross.Channel, Model: strings.ToUpper(f.route.Model), Family: r.cross.Family},
	} {
		t.Run(tag, func(t *testing.T) {
			next := p
			next.Limits = maps.Clone(p.Limits)
			next.Limits["review"] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{changed}}
			if _, _, err := next.Canonical(); !errors.Is(err, model.ErrStrongPlan) {
				t.Fatalf("conflicting concrete family approved: %v", err)
			}
		})
	}
	advanceST3Author(t, f, p, key, "One synthetic answer.")
	// Change the trusted channel registry after the author attempt. Family
	// labels no longer match, but the frozen actual account/model still does.
	c, err := f.db.GetChannel(f.ctx, f.route.Channel)
	if err != nil {
		t.Fatal(err)
	}
	c.Family = r.cross.Family
	if err := f.db.UpdateChannel(f.ctx, &c); err != nil {
		t.Fatal(err)
	}
	changed := f.route
	changed.Family = r.cross.Family
	strongReviewReserve(t, f, key, "review", changed, store.StrongReasonRoute)
}

func TestStrongST3QuotaCountsOtherRunHoldsAndMeasuredSpend(t *testing.T) {
	f, p, key, _ := setupST3(t, model.StrongTemplateLight, "Single controlled window.")
	c, err := f.db.GetChannel(f.ctx, f.route.Channel)
	if err != nil {
		t.Fatal(err)
	}
	c.QuotaWindows[0].LimitTokens = 35
	if err := f.db.UpdateChannel(f.ctx, &c); err != nil {
		t.Fatal(err)
	}
	another := p
	another.Run = "other-run"
	if err := f.db.SaveStrongTask(f.ctx, another.Run, another.TaskSHA, []byte("Evaluate the fixed synthetic alternatives.")); err != nil {
		t.Fatal(err)
	}
	second := f.approve(t, another)
	first := strongReviewReserve(t, f, key, "evidence", f.route, "")
	strongReviewReserve(t, f, second, "evidence", f.route, store.StrongReasonReviewQuota)
	f.fakeSlot(t, p, first, strongReceipt(p, model.StrongPhaseEvidence, ""), "tokens=16\n", false)
	strongReviewReserve(t, f, second, "evidence", f.route, store.StrongReasonReviewQuota)
	strongReviewReserve(t, f, key, "answer", f.route, store.StrongReasonReviewQuota)
}

func TestStrongST3ReviewInputFailsOnKnownExactIdentity(t *testing.T) {
	f, p, key, r := setupST3(t, model.StrongTemplateLight, "Evidence without identity.")
	advanceST3Author(t, f, p, key, "An author identity was embedded: "+strings.ToUpper(f.route.Model)+".")
	review := strongReviewReserve(t, f, key, "review", r.cross, "")
	scope, _, err := f.db.BindStrongStageInputs(f.ctx, review)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := prompt.AssembleStrongBundle(f.ctx, f.db, scope, p.TaskSHA); b != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("known route identity leaked to review: %s %v", b, err)
	}
}

func TestStrongST3FinalAttemptRouteRefusalReleasesOnlyOwnedClaim(t *testing.T) {
	f, p, key, _ := setupST3(t, model.StrongTemplateLight, "Exact synthetic current attempt.")
	in := strongReviewReserve(t, f, key, "evidence", f.route, "")
	_, cap := f.prepared(t, p, in)
	lease, why, err := f.db.ClaimStrongIntent(f.ctx, in)
	if err != nil || why != "" || lease == "" {
		t.Fatalf("claim: %q %v", why, err)
	}
	a, err := f.db.GetAttempt(f.ctx, in.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	a.Route.Model = "different-model"
	if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
		t.Fatal(err)
	}
	if err := f.db.VerifyStrongLaunch(f.ctx, in, strings.Repeat("0", 64), cap, ""); !errors.Is(err, model.ErrStrongPlan) {
		t.Fatal(err)
	}
	v, _, err := f.db.StrongRun(f.ctx, key)
	if err != nil || v.Reserved != in.Reserved {
		t.Fatalf("unowned lease released held claim: %+v %v", v, err)
	}
	if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, cap, ""); !errors.Is(err, model.ErrStrongPlan) || !strings.Contains(err.Error(), store.StrongReasonRoute) {
		t.Fatalf("mismatched route not denied: %v", err)
	}
	v, _, err = f.db.StrongRun(f.ctx, key)
	if err != nil || v.Reserved != 0 || v.Spent != 0 || v.Status != store.StrongBlocked || v.Reason != store.StrongReasonRoute {
		t.Fatalf("incomplete no-start state: %+v %v", v, err)
	}
}

func TestStrongST3RewriteKeepsMeasuredAccountAcrossWindowReset(t *testing.T) {
	for _, changeAccount := range []bool{false, true} {
		t.Run(fmt.Sprint(changeAccount), func(t *testing.T) {
			f, p, key, r := setupST3(t, model.StrongTemplateLight, "Approved synthetic account fixture.")
			ch, err := f.db.GetChannel(f.ctx, f.route.Channel)
			if err != nil {
				t.Fatal(err)
			}
			reset := time.Now().Add(3 * time.Second)
			ch.QuotaWindows[0].ResetAt = reset
			if err := f.db.UpdateChannel(f.ctx, &ch); err != nil {
				t.Fatal(err)
			}
			advanceST3Author(t, f, p, key, "A sealed answer.")
			review := strongReviewReserve(t, f, key, "review", r.cross, "")
			f.fakeSlot(t, p, review, crossReviewResult(p, "Two verified local claims."), "tokens=7\n", false)
			time.Sleep(time.Until(reset))
			ch.QuotaWindows[0].ResetAt = reset.Add(time.Hour)
			want := ""
			if changeAccount {
				ch.SourceGroup = "reassigned-account"
				want = store.StrongReasonReviewAuthor
			}
			if err := f.db.UpdateChannel(f.ctx, &ch); err != nil {
				t.Fatal(err)
			}
			strongReviewReserve(t, f, key, "rewrite", f.route, want)
		})
	}
}

func TestStrongST3ExpiredQuotaDoesNotReleaseUnprovedPendingOwnership(t *testing.T) {
	for _, scenario := range []string{"active", "settled", "foreign", "rebound worktree"} {
		t.Run(scenario, func(t *testing.T) {
			f, p, key, _ := setupST3(t, model.StrongTemplateLight, "Pending ownership fixture.")
			in := strongReviewReserve(t, f, key, "evidence", f.route, "")
			_, cap := f.prepared(t, p, in)
			lease, why, err := f.db.ClaimStrongIntent(f.ctx, in)
			if err != nil || why != "" || lease == "" {
				t.Fatalf("claim: %q %v", why, err)
			}
			ch, err := f.db.GetChannel(f.ctx, in.Route.Channel)
			if err != nil {
				t.Fatal(err)
			}
			ch.QuotaWindows[0].ResetAt = time.Now().Add(-time.Minute)
			if err := f.db.UpdateChannel(f.ctx, &ch); err != nil {
				t.Fatal(err)
			}
			a, err := f.db.GetAttempt(f.ctx, in.Attempt)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "active":
				a.StartedAt = time.Now()
			case "settled":
				a.EndedAt = time.Now()
			case "rebound worktree":
				a.Worktree = t.TempDir()
			case "foreign":
				a.Card = "foreign-card"
				db, err := sql.Open("sqlite", f.dbPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				if _, err := db.ExecContext(f.ctx, "UPDATE attempts SET card_id=? WHERE id=?", a.Card, a.ID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "foreign" {
				if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, cap, ""); !errors.Is(err, model.ErrStrongPlan) && !errors.Is(err, model.ErrStrongArtifact) {
				t.Fatalf("issued start without owned pending attempt: %v", err)
			}
			v, slots, err := f.db.StrongRun(f.ctx, key)
			if err != nil || v.Reserved != in.Reserved || v.Status != store.StrongRunning || slots[0].State != "claimed" {
				t.Fatalf("uncertain no-start ownership released or transitioned: %+v %+v %v", v, slots, err)
			}
		})
	}
}
