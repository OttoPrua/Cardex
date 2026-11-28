package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
	"github.com/OttoPrua/Cardex/internal/strongevidence"
)

type strongFakeFixture struct {
	db                                 *store.Store
	dbPath, repo, bare, base, evidence string
	ctx                                context.Context
	route                              model.StrongApprovedRoute
	snapshot                           *strongevidence.Snapshot
}

func syntheticStrongGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("private synthetic git step %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newStrongFake(t *testing.T) *strongFakeFixture {
	t.Helper()
	if !strongcustody.Supported() {
		t.Skip("no local strong custody implementation")
	}
	ctx := t.Context()
	repo := t.TempDir()
	syntheticStrongGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "fact.txt"), []byte("Synthetic comparison: the threshold is four.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	syntheticStrongGit(t, repo, "add", "fact.txt")
	syntheticStrongGit(t, repo, "-c", "user.name=CardeX", "-c", "user.email=example@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "synthetic evidence")
	base := syntheticStrongGit(t, repo, "rev-parse", "HEAD")
	bare := filepath.Join(t.TempDir(), "fixture.git")
	syntheticStrongGit(t, repo, "clone", "--bare", "--no-hardlinks", repo, bare)
	snapshot, err := strongevidence.Open(ctx, bare, base, []string{"fact.txt"})
	if err != nil {
		t.Fatalf("pinned evidence: %v", err)
	}
	digest, err := snapshot.Record().Digest()
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "workflow.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InstallStrongWorkflowPrototype(ctx); err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: "synthetic-project", RepoPath: repo}
	if err := db.CreateProject(ctx, &project); err != nil {
		t.Fatal(err)
	}
	f := &strongFakeFixture{db: db, dbPath: dbPath, repo: repo, bare: bare, base: base, evidence: digest, ctx: ctx, route: model.StrongApprovedRoute{Runner: "fake", Channel: "fake-channel", Model: "fake-model", Family: "fake-family"}, snapshot: snapshot}
	t.Cleanup(func() { _ = f.db.Close() })
	return f
}

func (f *strongFakeFixture) plan(t *testing.T, run, template string) model.StrongPlan {
	t.Helper()
	task := []byte("Evaluate the fixed synthetic alternatives.")
	p := model.StrongPlan{Run: run, Revision: 1, Version: model.StrongPlanVersion, Project: "synthetic-project", Scope: "Compare only the two synthetic choices.", TaskSHA: model.StrongArtifactDigest(task),
		EvidenceCommit: f.base, EvidencePaths: []string{"fact.txt"}, EvidenceSHA: f.evidence, GlobalTokens: 200, Template: template, Seats: 1}
	if template == model.StrongTemplateStrong {
		p.Seats = 2
		p.Framing = true
		p.SecondPeerRound = true
		p.TargetedVerify = true
	}
	graph, err := p.Graph()
	if err != nil {
		t.Fatal(err)
	}
	p.Limits = map[string]model.StrongSlotLimit{}
	for _, s := range graph {
		if s.Phase != model.StrongPhaseCheckpoint {
			p.Limits[s.Key] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{f.route}}
		}
	}
	if err := f.db.SaveStrongTask(f.ctx, p.Run, p.TaskSHA, task); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *strongFakeFixture) approve(t *testing.T, p model.StrongPlan) store.StrongRunKey {
	t.Helper()
	key, err := f.db.SaveStrongPlan(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.db.ReserveStrongStage(f.ctx, key, "evidence", f.route)
	if err != nil || d.Reason != store.StrongReasonApproval || d.Intent.ID != "" {
		t.Fatalf("unapproved reservation: %+v %v", d, err)
	}
	bad := key
	bad.SHA = strings.Repeat("1", 64)
	if err := f.db.ApproveStrongPlan(f.ctx, bad, "synthetic-owner", "wrong", time.Now()); !errors.Is(err, model.ErrStrongPlan) {
		t.Fatalf("wrong revision/digest approved: %v", err)
	}
	if err := f.db.ApproveStrongPlan(f.ctx, key, "synthetic-owner", "no-evidence", time.Now()); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("unmaterialized evidence approved: %v", err)
	}
	if err := f.db.SaveStrongEvidence(f.ctx, key, f.snapshot); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.db.ApproveStrongPlan(f.ctx, key, "synthetic-owner", "approval-"+key.Run, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return key
}

func (f *strongFakeFixture) reserve(t *testing.T, key store.StrongRunKey, slot string) store.StrongIntent {
	t.Helper()
	d, err := f.db.ReserveStrongStage(f.ctx, key, slot, f.route)
	if err != nil || d.Launch || d.Reason != "" || d.Intent.ID == "" {
		t.Fatalf("reserve %s: %+v %v", slot, d, err)
	}
	return d.Intent
}

func (f *strongFakeFixture) openAgain(t *testing.T) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := store.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f.db = d
}

func (f *strongFakeFixture) prepared(t *testing.T, p model.StrongPlan, in store.StrongIntent) (model.Attempt, *strongcustody.Capture) {
	t.Helper()
	scope, pin, err := f.db.BindStrongStageInputs(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "attempt")
	syntheticStrongGit(t, f.repo, "clone", "--no-hardlinks", f.bare, working)
	working, err = filepath.EvalSymlinks(working)
	if err != nil {
		t.Fatal(err)
	}
	a := model.Attempt{ID: in.Attempt, Card: in.Card, Worktree: working, BaseCommit: f.base, Route: model.Route{Channel: in.Route.Channel, Model: in.Route.Model},
		RouteReason: model.RouteReason{
			Dispatch:   &model.RouteDispatch{Runner: model.RunnerFake, Channel: in.Route.Channel, Model: in.Route.Model},
			Candidates: []model.RouteCandidate{{Channel: in.Route.Channel, Model: in.Route.Model, Family: in.Route.Family}},
		}}
	if err := f.db.CreateAttempt(f.ctx, &a); err != nil {
		t.Fatal(err)
	}
	reg, err := f.db.RegisterStrongProducer(f.ctx, scope, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := prompt.AssembleStrongBundle(f.ctx, f.db, scope, p.TaskSHA)
	if err != nil {
		t.Fatal(err)
	}
	c, err := strongcustody.Prepare(f.ctx, f.db, a, reg, p.TaskSHA, pin, rendered)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return a, c
}

func (f *strongFakeFixture) fakeSlot(t *testing.T, p model.StrongPlan, in store.StrongIntent, body, usage string, recoverAfterStart bool, beforeSettle ...func()) (model.StrongArtifactRef, string, []byte) {
	t.Helper()
	a, c := f.prepared(t, p, in)
	bundle := c.LaunchInput()
	stageDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stageDir, "input.json")
	script := filepath.Join(stageDir, "script.json")
	step := map[string]any{"result": body, "output": usage}
	if in.Slot == "evidence" {
		step["prompt_evidence_facts"] = true
	}
	if strings.HasPrefix(in.Slot, "peer-") {
		step["write_files"] = []map[string]string{{"path": ".cardex-strong-references.json", "content": `["Material 01"]`}}
	}
	raw, _ := json.Marshal(map[string]any{"attempts": map[string]any{"0": step}})
	for file, content := range map[string][]byte{path: bundle, script: raw} {
		if err := os.WriteFile(file, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	lease, why, err := f.db.ClaimStrongIntent(f.ctx, in)
	if err != nil || why != "" || lease == "" {
		t.Fatalf("claim %s: %s %v", in.Slot, why, err)
	}
	if again, _, err := f.db.ClaimStrongIntent(f.ctx, in); err != nil || again != "" {
		t.Fatalf("duplicate claim %s: %v", again, err)
	}
	observed, out, err := runner.RunStrongFakeChild(f.ctx, runner.LaunchSpec{AttemptID: a.ID, WorkDir: a.Worktree, PromptPath: path, ScriptPath: script}, model.StrongArtifactDigest(bundle), lease,
		func() error { return f.db.VerifyStrongLaunch(f.ctx, in, lease, c, path) },
		func() error {
			if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, c, path); !errors.Is(err, model.ErrStrongPlan) {
				t.Fatalf("second process start authorized: %v", err)
			}
			return f.db.MarkStrongStarted(f.ctx, in, lease)
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecordStrongFakeReceipt(f.ctx, in, lease, observed); err != nil {
		t.Fatal(err)
	}
	if observed.Record().BundleSHA != model.StrongArtifactDigest(bundle) || !bytes.Contains(out, []byte("strong_fake_input_sha=")) {
		t.Fatal("missing actual transport authentication")
	}

	res, err := harvest.Harvest(f.ctx, harvest.Input{Attempt: a, Card: model.Card{ID: a.Card, Kind: model.KindDesign}, Exit: observed.Record().Exit, Output: out, Repo: a.Worktree, CaptureStrongResult: c})
	if err != nil {
		t.Fatal(err)
	}
	exit := observed.Record().Exit
	a.Exit = &exit
	a.Outcome = res.Outcome
	a.EndedAt = time.Now()
	a.Usage = observed.Record().Usage
	if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
		t.Fatal(err)
	}
	var ref model.StrongArtifactRef
	if res.StrongResult != nil {
		ref, err = f.db.SaveStrongArtifact(f.ctx, res.StrongResult)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, hook := range beforeSettle {
		hook()
	}
	if recoverAfterStart {
		f.openAgain(t)
		if err := f.db.RecoverStrongRun(f.ctx, in.Key); err != nil {
			t.Fatal(err)
		}
		if v, _, _ := f.db.StrongRun(f.ctx, in.Key); v.Status != store.StrongBlocked || v.Reason != store.StrongReasonUnknownLaunch {
			t.Fatalf("unsafe recovery: %+v", v)
		}
	}
	var reason string
	if recoverAfterStart {
		reason, err = f.db.ReconcileStrongStage(f.ctx, in.Key, in.Slot)
	} else {
		reason, err = f.db.SettleStrong(f.ctx, in, lease, ref)
	}
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.db.SettleStrong(f.ctx, in, lease, ref)
	if err != nil || replay != reason {
		t.Fatalf("settle replay: %s %v", replay, err)
	}
	return ref, reason, bundle
}

func strongReceipt(p model.StrongPlan, phase model.StrongPhase, appendFields string) string {
	b := "## 结论\nPhase: " + string(phase) + "\n"
	switch phase {
	case model.StrongPhaseFrame:
		b += "Scope: " + p.Scope + "\nCriteria: compare alternatives with one threshold\n"
	case model.StrongPhaseEvidence:
		b += "Commit: " + p.EvidenceCommit + "\nEvidence: " + p.EvidenceSHA + "\nUncertainty: no wider dataset\n"
	case model.StrongPhaseBlind:
		b += "Evidence: " + p.EvidenceSHA + "\nAnswer: synthetic recommendation\nAssumptions: one threshold\nCounterexample: changed denominator\nTest: recalculate separately\n"
	case model.StrongPhasePeer:
		b += "Critique: check the denominator\n"
	case model.StrongPhaseVerify:
		b += "Evidence: " + p.EvidenceSHA + "\nResult: insufficient\nRationale: no wider data\n"
	case model.StrongPhaseSynthesis:
		b += "Evidence: " + p.EvidenceSHA + "\nAnswer: keep threshold provisional\nRationale: verified facts and remaining unknowns separated\nMinority: record denominator dissent\n"
	}
	return b + appendFields
}

func TestStrongFakeWorkflowStopsBeforeST3(t *testing.T) {
	f := newStrongFake(t)
	for _, template := range []string{model.StrongTemplateStrong, model.StrongTemplateLight} {
		t.Run(template, func(t *testing.T) {
			p := f.plan(t, strings.ReplaceAll(template, ".", "-"), template)
			key := f.approve(t, p)
			if d, err := f.db.ReserveStrongStage(f.ctx, key, "st3", f.route); err != nil || d.Reason != store.StrongReasonDependency {
				t.Fatalf("checkpoint bypass: %+v %v", d, err)
			}
			run := func(in store.StrongIntent, fields string, recover bool) (model.StrongArtifactRef, []byte) {
				slot, _ := p.Slot(in.Slot)
				ref, why, body := f.fakeSlot(t, p, in, strongReceipt(p, slot.Phase, fields), "tokens=7\n", recover)
				if why != "" || ref.EnvelopeSHA == "" {
					t.Fatalf("finish %s: %s", in.Slot, why)
				}
				return ref, body
			}
			if p.Framing {
				run(f.reserve(t, key, "frame"), "", false)
			}
			ev := f.reserve(t, key, "evidence")
			f.openAgain(t)
			if err := f.db.RecoverStrongRun(f.ctx, key); err != nil {
				t.Fatal(err)
			}
			if again := f.reserve(t, key, "evidence"); again != ev {
				t.Fatal("pre-claim restart changed stable intent")
			}
			run(ev, "", true)
			if template == model.StrongTemplateLight {
				run(f.reserve(t, key, "answer"), "", false)
			} else {
				a, b := f.reserve(t, key, "blind-1"), f.reserve(t, key, "blind-2")
				v, _, err := f.db.StrongRun(f.ctx, key)
				if err != nil || v.Reserved != 40 {
					t.Fatalf("parallel reserve: %+v %v", v, err)
				}
				_, left := run(a, "", false)
				_, right := run(b, "", false)
				if !bytes.Equal(left, right) || !bytes.Contains(left, []byte("Synthetic comparison: the threshold is four.")) {
					t.Fatal("blind inputs did not use the same frozen facts")
				}
				objection, _ := run(f.reserve(t, key, "peer-1-1"), "Objection: denominator is unclear\nDispute: test denominator\n", false)
				run(f.reserve(t, key, "peer-1-2"), "", false)
				if d, err := f.db.ReserveStrongStage(f.ctx, key, "peer-2-1", f.route); err != nil || d.Reason != store.StrongReasonCondition {
					t.Fatalf("conditional round bypass: %+v %v", d, err)
				}
				if err := f.db.ResolveStrongCondition(f.ctx, key, model.StrongConditionNewObjection, "synthetic-owner", "new material objection", true, objection); err != nil {
					t.Fatal(err)
				}
				_, follow1 := run(f.reserve(t, key, "peer-2-1"), "", false)
				_, follow2 := run(f.reserve(t, key, "peer-2-2"), "", false)
				for _, payload := range [][]byte{follow1, follow2} {
					if !bytes.Contains(payload, []byte(`"focus":"denominator is unclear"`)) || bytes.Contains(payload, []byte("synthetic-owner")) {
						t.Fatal("new objection not isolated and passed anonymously")
					}
				}
				if err := f.db.ResolveStrongCondition(f.ctx, key, model.StrongConditionVerifiableDispute, "synthetic-owner", "verify captured issue only", true, objection); err != nil {
					t.Fatal(err)
				}
				_, verification := run(f.reserve(t, key, "verify"), "", false)
				if !bytes.Contains(verification, []byte(`"focus":"test denominator"`)) || bytes.Count(verification, []byte(`"reference":`)) != 2 {
					t.Fatal("verification was not limited to evidence and the dispute receipt")
				}
				run(f.reserve(t, key, "synthesis"), "", false)
			}
			d, err := f.db.ReserveStrongStage(f.ctx, key, "st3", f.route)
			v, slots, viewErr := f.db.StrongRun(f.ctx, key)
			if err != nil || viewErr != nil || d.Reason != store.StrongReasonST3 || d.Intent.ID != "" || v.Status != store.StrongCheckpoint || v.Reserved != 0 || v.Spent != int64(7*(len(slots)-1)) {
				t.Fatalf("ST3 escaped or incorrect accounting %s: %+v %+v %v %v", fmt.Sprint(len(slots)), d, v, err, viewErr)
			}
		})
	}
}

func TestStrongFakeBudgetApprovalAndRecoveryStops(t *testing.T) {
	for _, tc := range []struct {
		name, usage, want string
		wrongContract     bool
		spent, reserved   int64
	}{
		{"unknown usage", "", store.StrongReasonUnknownUsage, false, 0, 20},
		{"overspend", "tokens=27\n", store.StrongReasonOverrun, false, 27, 0},
		{"ordinary passed is not readiness", "tokens=7\n", store.StrongReasonArtifact, true, 7, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStrongFake(t)
			p := f.plan(t, "negative-run", model.StrongTemplateLight)
			key := f.approve(t, p)
			body := strongReceipt(p, model.StrongPhaseEvidence, "")
			if tc.wrongContract {
				body = "## 结论\nOrdinary result passed; no evidence contract.\n"
			}
			in := f.reserve(t, key, "evidence")
			_, why, _ := f.fakeSlot(t, p, in, body, tc.usage, false)
			v, _, err := f.db.StrongRun(f.ctx, key)
			if err != nil || why != tc.want || v.Status != store.StrongBlocked || v.Reason != tc.want || v.Spent != tc.spent || v.Reserved != tc.reserved {
				t.Fatalf("%+v settled reason %q %v", v, why, err)
			}
			d, err := f.db.ReserveStrongStage(f.ctx, key, "answer", f.route)
			if err != nil || d.Reason != tc.want || d.Intent.ID != "" {
				t.Fatalf("blocked but downstream reserved: %+v %v", d, err)
			}
		})
	}
	t.Run("crash after claim before process", func(t *testing.T) {
		f := newStrongFake(t)
		p := f.plan(t, "restart-run", model.StrongTemplateLight)
		key := f.approve(t, p)
		in := f.reserve(t, key, "evidence")
		_, capture := f.prepared(t, p, in)
		lease, why, err := f.db.ClaimStrongIntent(f.ctx, in)
		if err != nil || why != "" || lease == "" {
			t.Fatalf("claim: %s %v", why, err)
		}
		if err := capture.Close(); err != nil {
			t.Fatal(err)
		}
		f.openAgain(t)
		for range 2 {
			if err := f.db.RecoverStrongRun(f.ctx, key); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, capture, ""); !errors.Is(err, model.ErrStrongPlan) {
			t.Fatalf("late launch after restart: %v", err)
		}
		if later, why, err := f.db.ClaimStrongIntent(f.ctx, in); err != nil || later != "" || why != store.StrongReasonUnknownLaunch {
			t.Fatalf("undetermined launch was replayed: %s %v", why, err)
		}
		reason, err := f.db.ReconcileStrongStage(f.ctx, key, in.Slot)
		if err != nil || reason != store.StrongReasonUnknownLaunch {
			t.Fatalf("missing result was treated as success: %s %v", reason, err)
		}
		if err := f.db.CancelStrongRun(f.ctx, key, "synthetic-owner"); err != nil {
			t.Fatal(err)
		}
		v, _, err := f.db.StrongRun(f.ctx, key)
		if err != nil || v.Status != store.StrongCanceled || v.Reserved != 20 || v.Spent != 0 {
			t.Fatalf("cancel lost unresolved reservation: %+v %v", v, err)
		}
		p.Revision++
		if _, err := f.db.SaveStrongPlan(f.ctx, p); !errors.Is(err, model.ErrStrongPlan) {
			t.Fatalf("new version evaded unresolved prior exposure: %v", err)
		}
	})
	t.Run("exact per-attempt T0 and production disabled", func(t *testing.T) {
		f := newStrongFake(t)
		t0 := f.route
		t0.T0 = true
		receipt := "explicit-t0-receipt"
		for i := 1; i <= 2; i++ {
			p := f.plan(t, fmt.Sprintf("t0-run-%d", i), model.StrongTemplateLight)
			p.Limits["evidence"] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{t0}}
			key := f.approve(t, p)
			d, err := f.db.ReserveStrongStage(f.ctx, key, "evidence", t0)
			if err != nil || d.Intent.ID == "" {
				t.Fatalf("exact T0 intent: %+v %v", d, err)
			}
			if got, why, err := f.db.ClaimStrongIntent(f.ctx, d.Intent); err != nil || got != "" || why != store.StrongReasonT0 {
				t.Fatalf("T0 without confirmation: %s %v", why, err)
			}
			wrong := d.Intent
			wrong.Attempt = "wrong-attempt"
			if err := f.db.ConfirmStrongT0(f.ctx, wrong, "synthetic-owner", receipt); !errors.Is(err, model.ErrStrongPlan) {
				t.Fatalf("confirmed wrong attempt: %v", err)
			}
			err = f.db.ConfirmStrongT0(f.ctx, d.Intent, "synthetic-owner", receipt)
			if i == 1 {
				if err != nil {
					t.Fatal(err)
				}
				if got, why, err := f.db.ClaimStrongIntent(f.ctx, d.Intent); err != nil || got != "" || why != "t0_dispatch_disabled_in_st2" {
					t.Fatalf("real T0 enabled: %s %v", why, err)
				}
			} else if err == nil {
				t.Fatal("one T0 receipt approved two attempts")
			}
		}
		p := f.plan(t, "real-route", model.StrongTemplateLight)
		r := f.route
		r.Runner = "codex"
		p.Limits["evidence"] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{r}}
		key := f.approve(t, p)
		got, err := f.db.ReserveStrongStage(f.ctx, key, "evidence", r)
		if err != nil || got.Intent.ID != "" || got.Reason != store.StrongReasonIsolation {
			t.Fatalf("real runner admitted: %+v %v", got, err)
		}
	})
	t.Run("parallel reservations and cumulative revisions", func(t *testing.T) {
		f := newStrongFake(t)
		p := f.plan(t, "parallel-run", model.StrongTemplateStrong)
		p.Framing = false
		p.SecondPeerRound = false
		p.TargetedVerify = false
		delete(p.Limits, "frame")
		delete(p.Limits, "peer-2-1")
		delete(p.Limits, "peer-2-2")
		delete(p.Limits, "verify")
		p.GlobalTokens = 35
		key, err := f.db.SaveStrongPlan(f.ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.db.SaveStrongEvidence(f.ctx, key, f.snapshot); err != nil {
			t.Fatal(err)
		}
		errs := make(chan error, 2)
		for i := 1; i <= 2; i++ {
			go func(i int) {
				errs <- f.db.ApproveStrongPlan(f.ctx, key, "synthetic-owner", fmt.Sprintf("approval-concurrent-%d", i), time.Now())
			}(i)
		}
		x, y := <-errs, <-errs
		if (x == nil) == (y == nil) {
			t.Fatalf("concurrent approval must have one winner: %v %v", x, y)
		}
		f.fakeSlot(t, p, f.reserve(t, key, "evidence"), strongReceipt(p, model.StrongPhaseEvidence, ""), "tokens=7\n", false)
		results := make(chan store.StrongDecision, 2)
		for _, name := range []string{"blind-1", "blind-2"} {
			go func(name string) {
				got, err := f.db.ReserveStrongStage(f.ctx, key, name, f.route)
				if err != nil {
					got.Reason = err.Error()
				}
				results <- got
			}(name)
		}
		first, second := <-results, <-results
		if first.Intent.ID == "" {
			first, second = second, first
		}
		if first.Intent.ID == "" || second.Intent.ID != "" || second.Reason != "parallel_budget_already_reserved" {
			t.Fatalf("oversubscribed: %+v %+v", first, second)
		}
		v, _, err := f.db.StrongRun(f.ctx, key)
		if err != nil || v.Reserved != 20 || v.Spent != 7 {
			t.Fatalf("reserve accounting: %+v %v", v, err)
		}
		if again := f.reserve(t, key, first.Intent.Slot); again != first.Intent {
			t.Fatal("second tick changed intent")
		}
		f.fakeSlot(t, p, first.Intent, strongReceipt(p, model.StrongPhaseBlind, ""), "tokens=7\n", false)
		v, _, _ = f.db.StrongRun(f.ctx, key)
		if v.Reserved != 0 || v.Spent != 14 {
			t.Fatalf("settle accounting: %+v", v)
		}
		if err := f.db.CancelStrongRun(f.ctx, key, "synthetic-owner"); err != nil {
			t.Fatal(err)
		}
		p.Revision++
		p.Scope = "Same task, new deliberately approved scope."
		newKey, err := f.db.SaveStrongPlan(f.ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if newKey.SHA == key.SHA {
			t.Fatal("changed plan did not change fingerprint")
		}
		d, err := f.db.ReserveStrongStage(f.ctx, newKey, "evidence", f.route)
		if err != nil || d.Reason != store.StrongReasonApproval || d.Intent.ID != "" {
			t.Fatalf("old approval carried over: %+v %v", d, err)
		}
		updated, _, err := f.db.StrongRun(f.ctx, newKey)
		if err != nil || updated.Spent != 14 || updated.Reserved != 0 {
			t.Fatalf("revision reset cumulative budget: %+v %v", updated, err)
		}
	})
}

func TestStrongEvidenceCannotAdvanceFromEchoOrMalformedSnapshot(t *testing.T) {
	f := newStrongFake(t)
	p := f.plan(t, "evidence-negative", model.StrongTemplateLight)
	for _, bad := range []struct{ commit, path string }{
		{strings.Repeat("1", len(f.base)), "fact.txt"},
		{f.base[:12], "fact.txt"},
		{f.base, "absent-synthetic.txt"},
	} {
		if snap, err := strongevidence.Open(f.ctx, f.bare, bad.commit, []string{bad.path}); snap != nil || !errors.Is(err, model.ErrStrongArtifact) {
			t.Fatalf("nonexact evidence materialized: %+v %v", bad, err)
		}
	}
	badPlan := p
	badPlan.Run = "wrong-evidence"
	badPlan.EvidenceSHA = strings.Repeat("e", 64)
	if err := f.db.SaveStrongTask(f.ctx, badPlan.Run, badPlan.TaskSHA, []byte("Evaluate the fixed synthetic alternatives.")); err != nil {
		t.Fatal(err)
	}
	badKey, err := f.db.SaveStrongPlan(f.ctx, badPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.SaveStrongEvidence(f.ctx, badKey, f.snapshot); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("mismatched manifest installed: %v", err)
	}
	key, err := f.db.SaveStrongPlan(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.SaveStrongEvidence(f.ctx, key, &strongevidence.Snapshot{}); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("caller-made empty receipt accepted: %v", err)
	}
	if err := f.db.SaveStrongEvidence(f.ctx, key, f.snapshot); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.ExecContext(f.ctx, `UPDATE strong_run_evidence SET record=substr(record,1,length(record)-4) WHERE run_id=?`, key.Run); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ApproveStrongPlan(f.ctx, key, "synthetic-owner", "truncated-evidence", time.Now()); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("truncated record advanced: %v", err)
	}
	d, err := f.db.ReserveStrongStage(f.ctx, key, "evidence", f.route)
	if err != nil || d.Intent.ID != "" || d.Reason != store.StrongReasonApproval {
		t.Fatalf("unverified evidence bypassed approval: %+v %v", d, err)
	}
}

func TestStrongStartRejectsClosedCaptureChangedAttemptAndPayload(t *testing.T) {
	f := newStrongFake(t)
	p := f.plan(t, "start-proof", model.StrongTemplateLight)
	key := f.approve(t, p)
	in := f.reserve(t, key, "evidence")
	a, c := f.prepared(t, p, in)
	lease, why, err := f.db.ClaimStrongIntent(f.ctx, in)
	if err != nil || lease == "" || why != "" {
		t.Fatalf("claim: %v %s", err, why)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "input.json")
	body := c.LaunchInput()
	write := func(b []byte) {
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write([]byte("changed prepared payload"))
	if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, c, path); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("changed bytes authorized: %v", err)
	}
	write(body)
	changed := a
	changed.Route.Model = "changed-model"
	if err := f.db.UpdateAttempt(f.ctx, &changed); err != nil {
		t.Fatal(err)
	}
	if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, c, path); !errors.Is(err, model.ErrStrongPlan) {
		t.Fatalf("changed actual route authorized: %v", err)
	}
	if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(f.ctx, `UPDATE strong_run_slots SET state='stopped' WHERE run_id=? AND slot=?`, key.Run, in.Slot); err != nil {
		t.Fatal(err)
	}
	if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, c, path); !errors.Is(err, model.ErrStrongPlan) {
		t.Fatalf("changed phase authorized: %v", err)
	}
	if _, err := raw.ExecContext(f.ctx, `UPDATE strong_run_slots SET state='claimed' WHERE run_id=? AND slot=?`, key.Run, in.Slot); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	f.openAgain(t)
	// Explicitly do not call RecoverStrongRun: live custody is an invariant
	// checked at the start edge, not a voluntary recovery convention.
	if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, c, path); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("old closed handle authorized: %v", err)
	}
	if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, nil, path); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("missing handle authorized: %v", err)
	}
}

func TestStrongFakeChildRehashesAfterControllerBoundary(t *testing.T) {
	f := newStrongFake(t)
	p := f.plan(t, "boundary-proof", model.StrongTemplateLight)
	key := f.approve(t, p)
	in := f.reserve(t, key, "evidence")
	a, c := f.prepared(t, p, in)
	lease, why, err := f.db.ClaimStrongIntent(f.ctx, in)
	if err != nil || lease == "" || why != "" {
		t.Fatalf("claim: %v %s", err, why)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "input.json")
	script := filepath.Join(dir, "script.json")
	body := c.LaunchInput()
	good := `{"attempts":{"0":{"result":"## 结论\nFake body","output":"tokens=7\n"}}}`
	for target, value := range map[string][]byte{path: body, script: []byte(good)} {
		if err := os.WriteFile(target, value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	receipt, _, err := runner.RunStrongFakeChild(f.ctx, runner.LaunchSpec{AttemptID: a.ID, WorkDir: a.Worktree, PromptPath: path, ScriptPath: script}, model.StrongArtifactDigest(body), lease,
		func() error {
			if err := f.db.VerifyStrongLaunch(f.ctx, in, lease, c, path); err != nil {
				return err
			}
			return os.WriteFile(path, []byte("modified after controller authorization"), 0600)
		},
		func() error { return f.db.MarkStrongStarted(f.ctx, in, lease) })
	if receipt != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("child admitted different actual bytes: %v %v", receipt, err)
	}
	if err := f.db.RecordStrongFakeReceipt(f.ctx, in, lease, &runner.StrongFakeReceipt{}); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("self-made receipt accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Worktree, ".cardex-result.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered child wrote output: %v", err)
	}
}

func TestStrongInFlightAccountingSurvivesParallelStop(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			f := newStrongFake(t)
			p := f.plan(t, "parallel-account", model.StrongTemplateStrong)
			p.Framing = false
			p.SecondPeerRound = false
			p.TargetedVerify = false
			delete(p.Limits, "frame")
			delete(p.Limits, "peer-2-1")
			delete(p.Limits, "peer-2-2")
			delete(p.Limits, "verify")
			key := f.approve(t, p)
			evidence, _, _ := f.fakeSlot(t, p, f.reserve(t, key, "evidence"), strongReceipt(p, model.StrongPhaseEvidence, ""), "tokens=7\n", false)
			if _, err := f.db.BindStrongInputs(f.ctx, model.StrongStage{Run: key.Run, Stage: "plain-blind", Seat: "generic-seat", Role: model.StrongBlind}, []model.StrongArtifactRef{evidence}); !errors.Is(err, model.ErrStrongArtifact) {
				t.Fatalf("generic ST1 blind allowed workflow input: %v", err)
			}
			one, two := f.reserve(t, key, "blind-1"), f.reserve(t, key, "blind-2")
			_, reason, _ := f.fakeSlot(t, p, one, strongReceipt(p, model.StrongPhaseBlind, ""), "tokens=7\n", false, func() {
				if canceled {
					_, why, _ := f.fakeSlot(t, p, two, strongReceipt(p, model.StrongPhaseBlind, ""), "", false, func() {
						if err := f.db.CancelStrongRun(f.ctx, key, "synthetic-owner"); err != nil {
							t.Fatal(err)
						}
					})
					if why != store.StrongReasonUnknownUsage {
						t.Fatalf("unknown usage after cancel: %s", why)
					}
				} else {
					_, why, _ := f.fakeSlot(t, p, two, "## 结论\nMalformed stage receipt.", "tokens=7\n", false)
					if why != store.StrongReasonArtifact {
						t.Fatalf("parallel stopper: %s", why)
					}
				}
			})
			v, slots, err := f.db.StrongRun(f.ctx, key)
			expected, reasonExpected, spent, held := store.StrongBlocked, store.StrongReasonArtifact, int64(21), int64(0)
			if canceled {
				expected, reasonExpected, spent, held = store.StrongCanceled, "owner_canceled", 14, 20
			}
			if err != nil || reason != reasonExpected || v.Status != expected || v.Reason != reasonExpected || v.Spent != spent || v.Reserved != held {
				t.Fatalf("in-flight receipt failed to settle without advancing: %+v outcome %q %v", v, reason, err)
			}
			for _, s := range slots {
				if strings.HasPrefix(s.Key, "blind-") && (s.AcceptedAttempt != "" || s.Ref.Attempt != "") {
					t.Fatalf("stopped slot falsely accepted result: %+v", s)
				}
			}
		})
	}
}

func TestStrongST2OnlyPrototypeStillCancelsWithoutST3Tables(t *testing.T) {
	f := newStrongFake(t)
	p := f.plan(t, "st2-only-cancel", model.StrongTemplateLight)
	key := f.approve(t, p)
	in := strongReviewReserve(t, f, key, "evidence", f.route, "")
	external, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = external.Close() })
	for _, name := range []string{"strong_run_review_pin", "strong_run_quota_holds", "strong_run_quota_manifests"} {
		if _, err := external.ExecContext(f.ctx, "DROP TABLE "+name); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.CancelStrongRun(f.ctx, key, "synthetic-owner"); err != nil {
		t.Fatal(err)
	}
	v, _, err := f.db.StrongRun(f.ctx, key)
	if err != nil || v.Status != store.StrongCanceled || v.Reserved != 0 || in.Reserved == 0 {
		t.Fatalf("old prototype cancellation: %+v %v", v, err)
	}
	var count int
	if err := external.QueryRowContext(f.ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'strong_run_quota_%'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("old prototype silently upgraded: %d %v", count, err)
	}
}
