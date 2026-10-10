package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func holdClosedBudgetLimitedWriter(t *testing.T, root string, wf *WorkflowRecord) *Task {
	t.Helper()
	cfg := workflowTestCfg(t, root)
	writer, err := admitWorkflowWriter(root, cfg, wf, "")
	if err != nil {
		t.Fatalf("admit writer: %v", err)
	}
	tk, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := newAttemptID()
	tk.Status = statusHeld
	tk.SessionID = "writer-session-synthetic"
	tk.ActiveAttemptID = attemptID
	tk.Goal = &TaskGoalBinding{
		WriterMode:        goalWriterManual,
		Observation:       goalObsUnknown,
		ClassifierVerdict: "budget_limited",
		Started:           true,
		BoundAttemptID:    attemptID,
		CustodyReleased:   false,
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := writeAttempt(root, &AttemptRecord{
		TaskID: tk.ID, AttemptID: attemptID, ControlEpoch: tk.ControlEpoch, State: attemptExited,
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

func commitOwnedSourceChange(t *testing.T, dir string) (commit, tree string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(dir, "internal", "auth", "token.go"), "package auth\n// owned source change\n")
	workflowGit(t, dir, "add", "-A")
	workflowGit(t, dir, "commit", "-q", "-m", "owned-source")
	return workflowHeadCandidate(t, dir)
}

func snapshotWorkflowAndTasks(t *testing.T, root, wfID string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	add := func(p string) {
		b, err := os.ReadFile(p)
		if err == nil {
			out[p] = append([]byte(nil), b...)
		}
	}
	add(workflowPath(root, wfID))
	add(workflowProgressJSONPath(root, wfID))
	add(workflowProgressMDPath(root, wfID))
	entries, err := os.ReadDir(tasksDir(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			add(filepath.Join(tasksDir(root), e.Name()))
		}
	}
	return out
}

func assertWorkflowStateUnchanged(t *testing.T, before, after map[string][]byte) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("workflow/task file count changed: %d -> %d", len(before), len(after))
	}
	for p, b := range before {
		got, ok := after[p]
		if !ok {
			t.Fatalf("missing file after refusal: %s", p)
		}
		if !bytes.Equal(b, got) {
			t.Fatalf("refused command mutated %s", p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Fatalf("refused command created %s", p)
		}
	}
}

func cmdFreeze(root, id, commit, tree string, retained bool, extra ...string) error {
	args := []string{"freeze-candidate", id, "-root", root, "-commit", commit, "-tree", tree, "-changed-paths", "internal/auth"}
	args = append(args, extra...)
	if retained {
		args = append(args, "-retained-held-source")
	}
	return cmdWorkflow(args)
}

func writeExternalReviewReceipt(t *testing.T, wf *WorkflowRecord, writer *Task, artifactBody string, mutate func(*externalIndependentReviewReceipt)) string {
	t.Helper()
	dir := t.TempDir()
	art := filepath.Join(dir, "review-artifact.md")
	if err := os.WriteFile(art, []byte(artifactBody), 0o644); err != nil {
		t.Fatal(err)
	}
	artSHA := sha256Hex(artifactBody)
	paths := append([]string(nil), wf.Candidate.ChangedPaths...)
	digests := copyStringMap(wf.Candidate.PathDigests)
	rec := externalIndependentReviewReceipt{
		Schema:                  externalIndependentReviewSchemaV1,
		Kind:                    externalIndependentReviewMethod,
		Method:                  externalIndependentReviewMethod,
		Actor:                   "manager-local-synthetic",
		SessionID:               "mgr-session-synthetic-1",
		WorkflowID:              wf.ID,
		WriterTaskID:            writer.ID,
		WriterSessionID:         writer.SessionID,
		WriterAttemptID:         goalAttemptID(writer),
		WriterRevision:          writer.Revision,
		CandidateCommit:         wf.Candidate.Commit,
		CandidateTree:           wf.Candidate.Tree,
		ChangedPaths:            paths,
		PathDigests:             digests,
		ArtifactPath:            art,
		ArtifactSHA256:          artSHA,
		EvidenceSHA256:          externalReviewEvidenceSHA256(artSHA, wf.Candidate.Commit, wf.Candidate.Tree, digests),
		OwnerAuthorizationScope: ownerScopeLocalIntegration,
		CustodyClosed:           true,
		Attestation:             "local manager-attested evidence; does not cryptographically verify Human identity",
	}
	if mutate != nil {
		mutate(&rec)
	}
	receipt := filepath.Join(dir, "external-review-receipt.json")
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func reloadWF(t *testing.T, root, id string) *WorkflowRecord {
	t.Helper()
	wf, err := loadWorkflow(root, workflowTestCfg(t, root), id)
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

func TestExternalReviewExplicitFreezeChecksLeaseAndLiveProof(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("workflow_external_review.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"workspaceLeaseHeld", "taskHasLiveWriterProof", "recordedAttemptCloseout", "assertGitDiffCoveredByDeclared"} {
		if !bytes.Contains(src, []byte(needle)) {
			t.Fatalf("explicit freeze must consult %s", needle)
		}
	}
	if bytes.Contains(src, []byte(`HasPrefix(c, rel+"/")`)) {
		t.Fatal("pathCoveredByWriteDomain must not treat an ancestor directory as owned")
	}
}

func TestExternalReviewDefaultFreezeRefusesHeldWriter(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	wf := initTestWorkflow(t, root, dir)
	writer := holdClosedBudgetLimitedWriter(t, root, wf)
	before := snapshotWorkflowAndTasks(t, root, wf.ID)
	commit, tree := workflowHeadCandidate(t, dir)
	err := cmdFreeze(root, wf.ID, commit, tree, false)
	if !errors.Is(err, errWorkflowDuplicateRole) {
		t.Fatalf("default freeze must refuse a held writer: %v", err)
	}
	assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, wf.ID))
	fresh, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != statusHeld {
		t.Fatalf("held writer mutated to %s", fresh.Status)
	}
	if fresh.Goal == nil || fresh.Goal.Observation != goalObsUnknown || fresh.Goal.ClassifierVerdict != "budget_limited" {
		t.Fatalf("historical unknown/budget_limited was rewritten: %+v", fresh.Goal)
	}
}

func TestExternalReviewExplicitFreezeSucceedsOnlyWithCloseoutAndReleasedCustody(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	wf := initTestWorkflow(t, root, dir)
	writer := holdClosedBudgetLimitedWriter(t, root, wf)
	writerBefore, err := os.ReadFile(taskPath(root, writer.ID))
	if err != nil {
		t.Fatal(err)
	}
	commit, tree := commitOwnedSourceChange(t, dir)
	if err := cmdFreeze(root, wf.ID, commit, tree, true); err != nil {
		t.Fatalf("explicit freeze: %v", err)
	}
	wf = reloadWF(t, root, wf.ID)
	if wf.Candidate == nil || wf.Candidate.Commit != commit || wf.Candidate.Tree != tree {
		t.Fatalf("frozen candidate: %+v", wf.Candidate)
	}
	if wf.Candidate.FreezeKind != freezeKindRetainedHeldSource {
		t.Fatalf("freeze kind=%q", wf.Candidate.FreezeKind)
	}
	if wf.Candidate.PathDigests["internal/auth"] == "" {
		t.Fatal("per-path digest missing")
	}
	fresh, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != statusHeld || fresh.Goal == nil || fresh.Goal.Observation != goalObsUnknown {
		t.Fatalf("explicit freeze manufactured done/achieved: status=%s goal=%+v", fresh.Status, fresh.Goal)
	}
	writerAfter, err := os.ReadFile(taskPath(root, writer.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(writerBefore, writerAfter) {
		t.Fatal("explicit freeze must not rewrite historical writer bytes")
	}
	if _, err := admitWorkflowReviewer(root, workflowTestCfg(t, root), wf); err == nil {
		t.Fatal("model review must still refuse a held writer")
	}
}

func TestExternalReviewIngestBindsIndependentActorAndReleasesLocalIntegration(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	writer := holdClosedBudgetLimitedWriter(t, root, wf)
	commit, tree := commitOwnedSourceChange(t, dir)
	if err := cmdFreeze(root, wf.ID, commit, tree, true); err != nil {
		t.Fatal(err)
	}
	wf = reloadWF(t, root, wf.ID)
	writer, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := writeExternalReviewReceipt(t, wf, writer, verdictJSON("pass", nil, nil), nil)
	if err := cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", receipt}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	wf = reloadWF(t, root, wf.ID)
	if wf.Review == nil || !wf.Review.Admissible || wf.Review.Method != reviewMethodExternalIndependentLocal {
		t.Fatalf("review snapshot: %+v", wf.Review)
	}
	if wf.Review.Actor == writer.ID || wf.Review.SessionID == writer.SessionID {
		t.Fatal("review actor/session must be distinct from the original author")
	}
	if wf.ReviewerTaskID != "" {
		t.Fatalf("must not mint a fake reviewer card: %s", wf.ReviewerTaskID)
	}
	if wf.EffectGates.Integration != effectGateHeld || wf.EffectGates.Live != effectGateHeld || wf.EffectGates.Cutover != effectGateHeld {
		t.Fatalf("ingest must not release gates: %+v", wf.EffectGates)
	}
	if err := cmdWorkflow([]string{"try-release-integration", wf.ID, "-root", root}); err != nil {
		t.Fatalf("try-release: %v", err)
	}
	wf = reloadWF(t, root, wf.ID)
	integ, err := loadTask(root, wf.IntegrationTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if integ.Status != statusQueued {
		t.Fatalf("integration status=%s", integ.Status)
	}
	if wf.EffectGates.Integration != effectGateReleased || wf.EffectGates.Live != effectGateHeld || wf.EffectGates.Cutover != effectGateHeld {
		t.Fatalf("gates after release: %+v", wf.EffectGates)
	}
	if !integrationGateAllows(root, cfg, integ) {
		t.Fatal("tick latch must admit the revalidated external receipt")
	}
	if writerDispatchBlocksReleasedIntegration(root, wf, integ) {
		t.Fatal("held original writer left a dispatch/resource deadlock against the integration card")
	}
	live := mergeLiveWriterTasks(nil, reconstructLiveWriterClaims(root))
	if writerConflictsWithActive(integ, live) {
		t.Fatal("tick write-domain consumer still serializes the released integration behind the held writer")
	}
	freshWriter, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshWriter.Status != statusHeld {
		t.Fatalf("release manufactured writer status %s", freshWriter.Status)
	}
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	reviews := 0
	for _, tk := range tasks {
		if tk != nil && tk.Type == typeReview {
			reviews++
		}
	}
	if reviews != 0 {
		t.Fatalf("external ingress minted %d typeReview cards", reviews)
	}
}

func TestExternalReviewFailClosedCases(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		prepare     func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task)
		freezeFirst bool
		run         func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error
		wantIs      error
	}
	ingest := func(mutate func(*externalIndependentReviewReceipt), body string) func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
		return func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
			wf2 := reloadWF(t, root, wf.ID)
			w, err := loadTask(root, writer.ID)
			if err != nil {
				return err
			}
			receipt := writeExternalReviewReceipt(t, wf2, w, body, mutate)
			return cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", receipt})
		}
	}
	cases := []tc{
		{
			name:        "same-author-session",
			freezeFirst: true,
			run:         ingest(func(r *externalIndependentReviewReceipt) { r.SessionID = r.WriterSessionID }, verdictJSON("pass", nil, nil)),
			wantIs:      errWorkflowExternalReview,
		},
		{
			name:        "drifted-path-digest",
			freezeFirst: true,
			run: ingest(func(r *externalIndependentReviewReceipt) {
				r.PathDigests = copyStringMap(r.PathDigests)
				r.PathDigests["internal/auth"] = strings.Repeat("ab", 32)
				r.EvidenceSHA256 = externalReviewEvidenceSHA256(r.ArtifactSHA256, r.CandidateCommit, r.CandidateTree, r.PathDigests)
			}, verdictJSON("pass", nil, nil)),
			wantIs: errWorkflowExternalReview,
		},
		{
			name:        "drifted-artifact-hash",
			freezeFirst: true,
			run:         ingest(func(r *externalIndependentReviewReceipt) { r.ArtifactSHA256 = strings.Repeat("cd", 32) }, verdictJSON("pass", nil, nil)),
			wantIs:      errWorkflowExternalReview,
		},
		{
			name:        "stale-revision",
			freezeFirst: true,
			run: ingest(func(r *externalIndependentReviewReceipt) {
				r.WriterRevision = r.WriterRevision + 9
			}, verdictJSON("pass", nil, nil)),
			wantIs: errWorkflowExternalReview,
		},
		{
			name:        "open-findings",
			freezeFirst: true,
			run:         ingest(nil, verdictJSON("pass", []string{"still open"}, nil)),
			wantIs:      errWorkflowExternalReview,
		},
		{
			name:        "failed-review",
			freezeFirst: true,
			run:         ingest(nil, verdictJSON("block", []string{"p0"}, nil)),
			wantIs:      errWorkflowExternalReview,
		},
		{
			name:        "missing-owner-scope",
			freezeFirst: true,
			run:         ingest(func(r *externalIndependentReviewReceipt) { r.OwnerAuthorizationScope = "" }, verdictJSON("pass", nil, nil)),
			wantIs:      errWorkflowExternalReview,
		},
		{
			name:        "wrong-owner-scope-live",
			freezeFirst: true,
			run:         ingest(func(r *externalIndependentReviewReceipt) { r.OwnerAuthorizationScope = "live" }, verdictJSON("pass", nil, nil)),
			wantIs:      errWorkflowExternalReview,
		},
		{
			name:        "author-adversarial-method",
			freezeFirst: true,
			run: ingest(func(r *externalIndependentReviewReceipt) {
				r.Method = "AUTHOR_ADVERSARIAL"
				r.Kind = "AUTHOR_ADVERSARIAL"
			}, verdictJSON("pass", nil, nil)),
			wantIs: errWorkflowExternalReview,
		},
		{
			name: "unresolved-attempt",
			run: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
				if err := writeAttempt(root, &AttemptRecord{
					TaskID: writer.ID, AttemptID: writer.ActiveAttemptID, ControlEpoch: writer.ControlEpoch, State: attemptBound,
				}); err != nil {
					return err
				}
				commit, tree := workflowHeadCandidate(t, dir)
				return cmdFreeze(root, wf.ID, commit, tree, true)
			},
			wantIs: errWorkflowCustody,
		},
		{
			name: "ambiguous-path",
			run: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
				commit, tree := workflowHeadCandidate(t, dir)
				return cmdWorkflow([]string{"freeze-candidate", wf.ID, "-root", root, "-commit", commit, "-tree", tree, "-changed-paths", "internal/billing", "-retained-held-source"})
			},
			wantIs: errWriteDomainAmbiguousClaim,
		},
		{
			name: "live-process",
			run: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
				registerTaskInvoke(writer.ID, os.Getpid())
				t.Cleanup(func() { unregisterTaskInvoke(writer.ID, os.Getpid()) })
				commit, tree := workflowHeadCandidate(t, dir)
				return cmdFreeze(root, wf.ID, commit, tree, true)
			},
			wantIs: errWorkflowCustody,
		},
		{
			name: "ancestor-directory-claim",
			run: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
				commit, tree := commitOwnedSourceChange(t, dir)
				return cmdWorkflow([]string{"freeze-candidate", wf.ID, "-root", root, "-commit", commit, "-tree", tree, "-changed-paths", "internal", "-retained-held-source"})
			},
			wantIs: errWriteDomainAmbiguousClaim,
		},
		{
			name: "absolute-changed-path",
			run: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
				commit, tree := commitOwnedSourceChange(t, dir)
				return cmdWorkflow([]string{"freeze-candidate", wf.ID, "-root", root, "-commit", commit, "-tree", tree, "-changed-paths", "/internal/auth", "-retained-held-source"})
			},
			wantIs: errWriteDomainAmbiguousClaim,
		},
		{
			name: "undeclared-extra-changed-file",
			run: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
				mustWriteFile(t, filepath.Join(dir, "internal", "auth", "token.go"), "package auth\n// owned\n")
				mustWriteFile(t, filepath.Join(dir, "internal", "billing", "bill.go"), "package billing\n// extra undeclared\n")
				workflowGit(t, dir, "add", "-A")
				workflowGit(t, dir, "commit", "-q", "-m", "owned-plus-extra")
				commit, tree := workflowHeadCandidate(t, dir)
				return cmdFreeze(root, wf.ID, commit, tree, true)
			},
			wantIs: errWriteDomainAmbiguousClaim,
		},
		{
			name:        "ingest-ancestor-directory-claim",
			freezeFirst: true,
			run: ingest(func(r *externalIndependentReviewReceipt) {
				r.ChangedPaths = []string{"internal"}
			}, verdictJSON("pass", nil, nil)),
			wantIs: errWriteDomainAmbiguousClaim,
		},
		{
			name:        "ingest-absolute-changed-path",
			freezeFirst: true,
			run: ingest(func(r *externalIndependentReviewReceipt) {
				r.ChangedPaths = []string{"/internal/auth"}
			}, verdictJSON("pass", nil, nil)),
			wantIs: errWriteDomainAmbiguousClaim,
		},
		{
			name: "ingest-undeclared-extra-changed-file",
			prepare: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) {
				mustWriteFile(t, filepath.Join(dir, "internal", "auth", "other.go"), "package auth\n")
				workflowGit(t, dir, "add", "-A")
				workflowGit(t, dir, "commit", "-q", "-m", "auth-other")
			},
			freezeFirst: true,
			run: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
				wf2 := reloadWF(t, root, wf.ID)
				w, err := loadTask(root, writer.ID)
				if err != nil {
					return err
				}
				receipt := writeExternalReviewReceipt(t, wf2, w, verdictJSON("pass", nil, nil), func(r *externalIndependentReviewReceipt) {
					r.ChangedPaths = []string{"internal/auth/other.go"}
					digests, err := digestPathsAtTree(dir, r.CandidateTree, r.ChangedPaths)
					if err != nil {
						t.Fatal(err)
					}
					r.PathDigests = digests
					r.EvidenceSHA256 = externalReviewEvidenceSHA256(r.ArtifactSHA256, r.CandidateCommit, r.CandidateTree, digests)
				})
				return cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", receipt})
			},
			wantIs: errWriteDomainAmbiguousClaim,
		},
		{
			name:        "try-release-without-review",
			freezeFirst: true,
			run: func(t *testing.T, root, dir string, wf *WorkflowRecord, writer *Task) error {
				return cmdWorkflow([]string{"try-release-integration", wf.ID, "-root", root})
			},
			wantIs: errWorkflowHeld,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, dir := workflowTestRoot(t)
			wf := initTestWorkflow(t, root, dir)
			writer := holdClosedBudgetLimitedWriter(t, root, wf)
			if tc.prepare != nil {
				tc.prepare(t, root, dir, wf, writer)
			}
			if tc.freezeFirst {
				commit, tree := commitOwnedSourceChange(t, dir)
				if err := cmdFreeze(root, wf.ID, commit, tree, true); err != nil {
					t.Fatalf("fixture freeze: %v", err)
				}
			}
			before := snapshotWorkflowAndTasks(t, root, wf.ID)
			err := tc.run(t, root, dir, wf, writer)
			if err == nil {
				t.Fatal("command must fail closed")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err=%v want %v", err, tc.wantIs)
			}
			assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, wf.ID))
			tasks, err := loadTasks(root)
			if err != nil {
				t.Fatal(err)
			}
			reviews, writers, integs := 0, 0, 0
			for _, tk := range tasks {
				if tk == nil || tk.WorkflowID != wf.ID {
					continue
				}
				switch {
				case tk.Type == typeReview:
					reviews++
				case tk.IntegrationGate != nil:
					integs++
				default:
					writers++
				}
			}
			if reviews != 0 {
				t.Fatalf("duplicate review task minted: %d", reviews)
			}
			if writers != 1 || integs != 1 {
				t.Fatalf("writer/integration count %d/%d", writers, integs)
			}
			fresh, err := loadTask(root, writer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Status != statusHeld {
				t.Fatalf("writer status %s", fresh.Status)
			}
		})
	}
}

func TestExternalReviewReplayAndConcurrentDuplicate(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	wf := initTestWorkflow(t, root, dir)
	writer := holdClosedBudgetLimitedWriter(t, root, wf)
	commit, tree := commitOwnedSourceChange(t, dir)
	if err := cmdFreeze(root, wf.ID, commit, tree, true); err != nil {
		t.Fatal(err)
	}
	wf = reloadWF(t, root, wf.ID)
	writer, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := writeExternalReviewReceipt(t, wf, writer, verdictJSON("pass", nil, nil), nil)
	if err := cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", receipt}); err != nil {
		t.Fatal(err)
	}
	before := snapshotWorkflowAndTasks(t, root, wf.ID)
	other := writeExternalReviewReceipt(t, wf, writer, verdictJSON("pass", nil, nil), func(r *externalIndependentReviewReceipt) {
		r.Actor = "manager-local-synthetic-2"
		r.SessionID = "mgr-session-synthetic-2"
	})
	err = cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", other})
	if !errors.Is(err, errWorkflowExternalReview) {
		t.Fatalf("replay of a different receipt must fail closed: %v", err)
	}
	assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, wf.ID))

	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", receipt})
	}()
	go func() {
		defer wg.Done()
		err2 = cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", receipt})
	}()
	wg.Wait()
	if err1 != nil && err2 != nil {
		t.Fatalf("concurrent same-receipt ingest: %v / %v", err1, err2)
	}
	after := snapshotWorkflowAndTasks(t, root, wf.ID)
	assertWorkflowStateUnchanged(t, before, after)
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	reviews := 0
	for _, tk := range tasks {
		if tk != nil && tk.Type == typeReview {
			reviews++
		}
	}
	if reviews != 0 {
		t.Fatalf("concurrent ingest minted review cards: %d", reviews)
	}
}

func TestExternalReviewDriftedArtifactFailsReleaseWithoutMutation(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	wf := initTestWorkflow(t, root, dir)
	writer := holdClosedBudgetLimitedWriter(t, root, wf)
	commit, tree := commitOwnedSourceChange(t, dir)
	if err := cmdFreeze(root, wf.ID, commit, tree, true); err != nil {
		t.Fatal(err)
	}
	wf = reloadWF(t, root, wf.ID)
	writer, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := writeExternalReviewReceipt(t, wf, writer, verdictJSON("pass", nil, nil), nil)
	if err := cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", receipt}); err != nil {
		t.Fatal(err)
	}
	wf = reloadWF(t, root, wf.ID)
	if err := os.WriteFile(wf.Review.ArtifactPath, []byte(verdictJSON("pass", []string{"drifted"}, nil)), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotWorkflowAndTasks(t, root, wf.ID)
	err = cmdWorkflow([]string{"try-release-integration", wf.ID, "-root", root})
	if !errors.Is(err, errWorkflowHeld) {
		t.Fatalf("drifted artifact must hold: %v", err)
	}
	assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, wf.ID))
	integ, err := loadTask(root, wf.IntegrationTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if integ.Status != statusHeld {
		t.Fatalf("integration status=%s", integ.Status)
	}
}

func TestExternalReviewCandidatePathDriftFailsIngest(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	wf := initTestWorkflow(t, root, dir)
	writer := holdClosedBudgetLimitedWriter(t, root, wf)
	mustWriteFile(t, filepath.Join(dir, "internal", "auth", "token.go"), "package auth\n// v2\n")
	workflowGit(t, dir, "add", "-A")
	workflowGit(t, dir, "commit", "-q", "-m", "second")
	commit2, tree2 := workflowHeadCandidate(t, dir)
	if err := cmdFreeze(root, wf.ID, commit2, tree2, true); err != nil {
		t.Fatal(err)
	}
	wf = reloadWF(t, root, wf.ID)
	writer, err := loadTask(root, writer.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit1 := workflowGit(t, dir, "rev-parse", "HEAD~1")
	tree1 := workflowGit(t, dir, "rev-parse", "HEAD~1^{tree}")
	before := snapshotWorkflowAndTasks(t, root, wf.ID)
	receipt := writeExternalReviewReceipt(t, wf, writer, verdictJSON("pass", nil, nil), func(r *externalIndependentReviewReceipt) {
		r.CandidateCommit = commit1
		r.CandidateTree = tree1
		digests, err := digestPathsAtTree(dir, tree1, r.ChangedPaths)
		if err != nil {
			t.Fatal(err)
		}
		r.PathDigests = digests
		r.EvidenceSHA256 = externalReviewEvidenceSHA256(r.ArtifactSHA256, commit1, tree1, digests)
	})
	err = cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", receipt})
	if !errors.Is(err, errWorkflowExternalReview) {
		t.Fatalf("candidate drift must fail: %v", err)
	}
	assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, wf.ID))
}

func TestExternalReviewTempCLI(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "cardex-tempcli")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	runFlow := func(t *testing.T) {
		t.Helper()
		root, dir := workflowTestRoot(t)
		wf := initTestWorkflow(t, root, dir)
		writer := holdClosedBudgetLimitedWriter(t, root, wf)
		commit, tree := commitOwnedSourceChange(t, dir)
		run := func(args ...string) string {
			t.Helper()
			cmd := exec.Command(bin, args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
			return string(out)
		}
		freezeOut := run("workflow", "freeze-candidate", wf.ID, "-root", root,
			"-commit", commit, "-tree", tree, "-changed-paths", "internal/auth", "-retained-held-source")
		t.Logf("freeze: %s", strings.TrimSpace(freezeOut))
		if !strings.Contains(freezeOut, "commit="+commit) || !strings.Contains(freezeOut, "tree="+tree) {
			t.Fatalf("freeze output: %s", freezeOut)
		}
		wf = reloadWF(t, root, wf.ID)
		writer, err := loadTask(root, writer.ID)
		if err != nil {
			t.Fatal(err)
		}
		receipt := writeExternalReviewReceipt(t, wf, writer, verdictJSON("pass", nil, nil), nil)
		ing := run("workflow", "ingest-external-review", wf.ID, "-root", root, "-receipt", receipt)
		t.Logf("ingest: %s", strings.TrimSpace(ing))
		if !strings.Contains(ing, "actor=manager-local-synthetic") || !strings.Contains(ing, "method=external_independent_local") {
			t.Fatalf("ingest output: %s", ing)
		}
		if strings.Contains(ing, "session="+writer.SessionID) {
			t.Fatalf("bound writer session: %s", ing)
		}
		rel := run("workflow", "try-release-integration", wf.ID, "-root", root)
		t.Logf("release: %s", strings.TrimSpace(rel))
		if !strings.Contains(rel, "integration="+effectGateReleased) {
			t.Fatalf("release output: %s", rel)
		}
		if !strings.Contains(rel, "live="+effectGateHeld) || !strings.Contains(rel, "cutover="+effectGateHeld) {
			t.Fatalf("live/cutover must stay held: %s", rel)
		}
	}
	runFlow(t)
	runFlow(t)
}

func setupManualLocalIntegration(t *testing.T) (string, *WorkflowRecord, string, *manualLocalIntegrationReceipt) {
	t.Helper()
	root, dir := workflowTestRoot(t)
	wf := initTestWorkflow(t, root, dir)
	writer := holdClosedBudgetLimitedWriter(t, root, wf)
	commit, tree := commitOwnedSourceChange(t, dir)
	if err := cmdFreeze(root, wf.ID, commit, tree, true); err != nil {
		t.Fatal(err)
	}
	wf = reloadWF(t, root, wf.ID)
	writer, _ = loadTask(root, writer.ID)
	review := writeExternalReviewReceipt(t, wf, writer, verdictJSON("pass", nil, nil), nil)
	if err := cmdWorkflow([]string{"ingest-external-review", wf.ID, "-root", root, "-receipt", review}); err != nil {
		t.Fatal(err)
	}
	wf = reloadWF(t, root, wf.ID)
	_, target := workflowTestRoot(t)
	target, targetErr := filepath.EvalSymlinks(target)
	if targetErr != nil {
		t.Fatal(targetErr)
	}
	// Real local adoption in a different synthetic target repository.
	rel := "internal/auth/token.go"
	body, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, rel), body, 0644); err != nil {
		t.Fatal(err)
	}
	evidenceDir := t.TempDir()
	artifact := filepath.Join(evidenceDir, "adoption.txt")
	checks := filepath.Join(evidenceDir, "checks.txt")
	mustWriteFile(t, artifact, "Actual manager copied exact owned bytes to synthetic target; no provider.\n")
	mustWriteFile(t, checks, "Synthetic local byte/mode and no-network checks passed.\n")
	aSHA, _, _ := fileSHA256Hex(artifact)
	cSHA, _, _ := fileSHA256Hex(checks)
	integ, _ := loadTask(root, wf.IntegrationTaskID)
	rec := &manualLocalIntegrationReceipt{Schema: manualLocalIntegrationSchema, Method: manualLocalIntegrationMethod,
		WorkflowID: wf.ID, IntegrationTaskID: integ.ID, IntegrationRevision: integ.Revision,
		Actor: "manager-synthetic", SessionID: "manager-adoption-session", TargetRepo: target,
		OwnerAuthorizationScope: ownerScopeLocalIntegration, OwnerAuthorizationTargetRepo: target,
		ExternalReviewSHA256: wf.Review.ReceiptSHA256, CandidateCommit: commit, CandidateTree: tree,
		FileSHA256: map[string]string{rel: sha256Hex(string(body))}, FileGitModes: map[string]string{rel: "100644"},
		ArtifactPath: artifact, ArtifactSHA256: aSHA, ChecksPath: checks, ChecksSHA256: cSHA, ChecksPassed: true}
	return root, wf, target, rec
}

func saveManualAdoptionReceipt(t *testing.T, rec *manualLocalIntegrationReceipt) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "manual-adoption.json")
	bytes, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManualLocalIntegrationTempCLI(t *testing.T) {
	root, wf, target, rec := setupManualLocalIntegration(t)
	beforeWriter, _ := os.ReadFile(taskPath(root, wf.WriterTaskID))
	bin := filepath.Join(t.TempDir(), "cardex")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	p := saveManualAdoptionReceipt(t, rec)
	args := []string{"workflow", "record-local-integration", wf.ID, "-root", root, "-receipt", p, "-target-repo", target}
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("manual CLI: %v %s", err, out)
	}
	t.Logf("actual CLI: %s", strings.TrimSpace(string(out)))
	if !strings.Contains(string(out), "provider_dispatches=0") {
		t.Fatal("missing honest zero-provider result")
	}
	integ, _ := loadTask(root, wf.IntegrationTaskID)
	if integ.Status != statusDone || integ.schedulingAllowed() || integ.SessionID != "" || integ.ActiveAttemptID != "" || integ.Attempts != 0 {
		t.Fatalf("placeholder queued/provider fabricated: %+v", integ)
	}
	wf = reloadWF(t, root, wf.ID)
	if wf.Status != workflowStatusLocallyIntegrated || workflowClaimsActive(wf) || wf.LocalIntegration == nil ||
		wf.EffectGates.Live != effectGateHeld || wf.EffectGates.Cutover != effectGateHeld {
		t.Fatalf("manual outcome: %+v", wf)
	}
	afterWriter, _ := os.ReadFile(taskPath(root, wf.WriterTaskID))
	if !bytes.Equal(beforeWriter, afterWriter) {
		t.Fatal("original writer changed")
	}
	before := snapshotWorkflowAndTasks(t, root, wf.ID)
	// Identical retry is safe, but never promotes into the existing model lane.
	if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Fatalf("exact replay: %v %s", err, out)
	}
	assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, wf.ID))
	if err := cmdWorkflow([]string{"try-release-integration", wf.ID, "-root", root}); err == nil {
		t.Fatal("manual adoption must not requeue")
	}
	assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, wf.ID))
	// A replay still verifies actual target bytes.
	mustWriteFile(t, filepath.Join(target, "internal/auth/token.go"), "drift\n")
	if _, err := exec.Command(bin, args...).CombinedOutput(); err == nil {
		t.Fatal("drifted adopted bytes replay accepted")
	}
	assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, wf.ID))
}

func TestManualLocalIntegrationRefusesAffectedCases(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, string, *WorkflowRecord, string, *manualLocalIntegrationReceipt)
	}{
		{"wrong-target", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			r.TargetRepo += "/elsewhere"
		}},
		{"wrong-authority-target", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			r.OwnerAuthorizationTargetRepo += "/elsewhere"
		}},
		{"wrong-authority-scope", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			r.OwnerAuthorizationScope = "live"
		}},
		{"stale-revision", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			r.IntegrationRevision++
		}},
		{"wrong-integration-id", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			r.IntegrationTaskID = "other"
		}},
		{"candidate-drift", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			r.CandidateTree = "other"
		}},
		{"same-author", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			r.SessionID = "writer-session-synthetic"
		}},
		{"not-actually-adopted", func(t *testing.T, _ string, _ *WorkflowRecord, target string, _ *manualLocalIntegrationReceipt) {
			mustWriteFile(t, filepath.Join(target, "internal/auth/token.go"), "old\n")
		}},
		{"mode-drift", func(t *testing.T, _ string, _ *WorkflowRecord, target string, _ *manualLocalIntegrationReceipt) {
			if err := os.Chmod(filepath.Join(target, "internal/auth/token.go"), 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{"checks-not-passed", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			r.ChecksPassed = false
		}},
		{"checks-tampered", func(t *testing.T, _ string, _ *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			mustWriteFile(t, r.ChecksPath, "tamper")
		}},
		{"review-tampered", func(t *testing.T, _ string, w *WorkflowRecord, _ string, _ *manualLocalIntegrationReceipt) {
			mustWriteFile(t, w.Review.ArtifactPath, "tamper")
		}},
		{"integration-gate-drift", func(t *testing.T, root string, w *WorkflowRecord, _ string, r *manualLocalIntegrationReceipt) {
			x, _ := loadTask(root, w.IntegrationTaskID)
			x.IntegrationGate.CandidateTree = "wrong"
			if err := saveTask(root, x); err != nil {
				t.Fatal(err)
			}
			x, _ = loadTask(root, w.IntegrationTaskID)
			r.IntegrationRevision = x.Revision
		}},
		{"active-placeholder", func(t *testing.T, root string, w *WorkflowRecord, _ string, _ *manualLocalIntegrationReceipt) {
			x, _ := loadTask(root, w.IntegrationTaskID)
			restoreScheduling(x)
			x.Status = statusQueued
			if err := saveTask(root, x); err != nil {
				t.Fatal(err)
			}
		}},
		{"dispatched-history", func(t *testing.T, root string, w *WorkflowRecord, _ string, _ *manualLocalIntegrationReceipt) {
			if err := writeAttempt(root, &AttemptRecord{TaskID: w.IntegrationTaskID, AttemptID: "old", State: attemptExited}); err != nil {
				t.Fatal(err)
			}
		}},
		{"writer-custody-unresolved", func(t *testing.T, root string, w *WorkflowRecord, _ string, _ *manualLocalIntegrationReceipt) {
			x, _ := loadTask(root, w.WriterTaskID)
			a, _ := loadAttempt(root, x.ID, goalAttemptID(x))
			a.State = attemptBound
			if err := writeAttempt(root, a); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, w, target, r := setupManualLocalIntegration(t)
			tc.mutate(t, root, w, target, r)
			p := saveManualAdoptionReceipt(t, r)
			before := snapshotWorkflowAndTasks(t, root, w.ID)
			if err := cmdWorkflow([]string{"record-local-integration", w.ID, "-root", root, "-receipt", p, "-target-repo", target}); err == nil {
				t.Fatal("must refuse")
			}
			assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, w.ID))
		})
	}
}

func TestManualLocalIntegrationConcurrentReplay(t *testing.T) {
	root, w, target, r := setupManualLocalIntegration(t)
	p := saveManualAdoptionReceipt(t, r)
	cfg := workflowTestCfg(t, root)
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			wf := reloadWF(t, root, w.ID)
			errs <- recordManualLocalIntegration(root, cfg, wf, p, target)
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	integ, _ := loadTask(root, w.IntegrationTaskID)
	if integ.Revision != r.IntegrationRevision+1 {
		t.Fatalf("duplicate terminal transition revision %d", integ.Revision)
	}
	before := snapshotWorkflowAndTasks(t, root, w.ID)
	r.Actor = "other-real-manager"
	other := saveManualAdoptionReceipt(t, r)
	if err := recordManualLocalIntegration(root, cfg, reloadWF(t, root, w.ID), other, target); err == nil {
		t.Fatal("different receipt replay accepted")
	}
	assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, w.ID))
}

func TestManualLocalIntegrationRejectsFIFO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX FIFO case; production regular-file reader is cross-platform")
	}
	bin := filepath.Join(t.TempDir(), "cardex")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	for _, kind := range []string{"receipt", "artifact", "checks", "adopted-file"} {
		t.Run(kind, func(t *testing.T) {
			root, w, target, r := setupManualLocalIntegration(t)
			p := saveManualAdoptionReceipt(t, r)
			dest := p
			switch kind {
			case "artifact":
				dest = r.ArtifactPath
			case "checks":
				dest = r.ChecksPath
			case "adopted-file":
				dest = filepath.Join(target, "internal/auth/token.go")
			}
			if err := os.Remove(dest); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("mkfifo", dest).CombinedOutput(); err != nil {
				t.Fatalf("mkfifo: %v %s", err, out)
			}
			before := snapshotWorkflowAndTasks(t, root, w.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, bin, "workflow", "record-local-integration", w.ID, "-root", root, "-receipt", p, "-target-repo", target).CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("FIFO blocked normal CLI: %s", out)
			}
			if err == nil {
				t.Fatal("FIFO accepted")
			}
			assertWorkflowStateUnchanged(t, before, snapshotWorkflowAndTasks(t, root, w.ID))
		})
	}
}

func TestManualLocalIntegrationRecoversCommittedTerminal(t *testing.T) {
	root, w, target, r := setupManualLocalIntegration(t)
	p := saveManualAdoptionReceipt(t, r)
	cfg := workflowTestCfg(t, root)
	transitionCrashAt = transitionCrashAfterCommit
	t.Cleanup(func() { transitionCrashAt = "" })
	err := recordManualLocalIntegration(root, cfg, w, p, target)
	transitionCrashAt = ""
	if err == nil {
		t.Fatal("expected synthetic crash")
	}
	if err := recordManualLocalIntegration(root, cfg, reloadWF(t, root, w.ID), p, target); err != nil {
		t.Fatalf("normal exact recovery: %v", err)
	}
	integ, _ := loadTask(root, w.IntegrationTaskID)
	wf := reloadWF(t, root, w.ID)
	if integ.Revision != r.IntegrationRevision+1 || integ.Status != statusDone || integ.schedulingAllowed() || wf.LocalIntegration == nil || wf.Status != workflowStatusLocallyIntegrated {
		t.Fatalf("recovery minted extra terminal or lost actual outcome: %+v", integ)
	}
}
