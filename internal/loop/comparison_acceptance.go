package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

const (
	comparisonApprovalInvalidated = "objective_optional_comparison_approval_invalidated"
	comparisonAcceptanceRejected  = "objective_optional_comparison_acceptance_failed"
	comparisonGitHeld             = "objective_optional_comparison_git_held"
	comparisonGitUnknown          = "objective_optional_comparison_update_unknown"
)

type comparisonAcceptancePin struct {
	Ref   string `json:"integration_ref"`
	OID   string `json:"integration_commit"`
	Scope string `json:"acceptance_scope_sha256"`
}

func exactComparisonOID(oid string) bool {
	if (len(oid) != 40 && len(oid) != 64) || oid != strings.ToLower(oid) {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

func (p comparisonAcceptancePin) valid() bool {
	return strings.HasPrefix(p.Ref, "refs/heads/") && len(p.Ref) > len("refs/heads/") &&
		exactComparisonOID(p.OID) && len(p.Scope) == 64 && exactComparisonOID(p.Scope)
}

func (p comparisonAcceptancePin) question() string {
	return "Objective acceptance passed at exact integration commit " + p.OID + "; approve this commit for a local primary fast-forward."
}

func (st *step) comparisonWorktreeRoot() string {
	// Match the scheduler's Git.Create root, without inferring ownership
	// from a Git pointer or broadening to siblings of the configured root.
	root := st.snap.project.WorktreeRoot
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(st.home, "worktrees")
	}
	return root
}

func (st *step) verifyComparisonAcceptanceSource() bool {
	return workspace.VerifyOptionalAcceptanceRepository(st.ctx, st.snap.project.RepoPath, st.comparisonWorktreeRoot())
}

func (st *step) resolveComparisonRef(ref string) (string, bool) {
	repo := st.snap.project.RepoPath
	if repo == "" || !strings.HasPrefix(ref, "refs/heads/") {
		return "", false
	}
	ctx := proc.WithExactGitObjects(st.ctx)
	if _, err := acceptGit(ctx, repo, "check-ref-format", ref); err != nil {
		return "", false
	}
	out, err := acceptGit(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	oid := strings.TrimSpace(out)
	return oid, err == nil && exactComparisonOID(oid)
}

func (st *step) currentComparisonAcceptance() (comparisonAcceptancePin, bool) {
	obj := st.snap.obj
	if obj.IntegrationBranch == "" {
		return comparisonAcceptancePin{}, false
	}
	ref := obj.IntegrationBranch
	if !strings.HasPrefix(ref, "refs/heads/") {
		ref = "refs/heads/" + ref
	}
	if ref == "refs/heads/main" || ref == "refs/heads/master" {
		return comparisonAcceptancePin{}, false
	}
	oid, ok := st.resolveComparisonRef(ref)
	if !ok {
		return comparisonAcceptancePin{}, false
	}
	// Bind the exact acceptance definition and project/repository/worktree
	// root identity; the receipt exposes only its digest, never the paths.
	raw, _ := json.Marshal(struct {
		Version, Objective, Project, Repo, Ref, WorktreeRoot string
		Commands, Checklist                                  []string
	}{"cardex-optional-comparison-v2", obj.ID, obj.Project, st.snap.project.RepoPath, ref, st.comparisonWorktreeRoot(),
		append([]string{}, obj.Acceptance.Commands...), append([]string{}, obj.Acceptance.Checklist...)})
	sum := sha256.Sum256(raw)
	return comparisonAcceptancePin{Ref: ref, OID: oid, Scope: hex.EncodeToString(sum[:])}, true
}

func (st *step) openComparisonCandidate(pin comparisonAcceptancePin, excluded []string, previous comparisonExemption) error {
	// If the externally mutable branch advanced while tests ran, do not
	// even ask for this result; qualify the current tip on a later step.
	if !st.verifyComparisonAcceptanceSource() {
		return nil
	}
	if current, ok := st.currentComparisonAcceptance(); !ok || current != pin {
		return nil
	}
	if st.snap.obj.Status != model.ObjectiveDoneCandidate {
		st.snap.obj.Status = model.ObjectiveDoneCandidate
		if err := st.saveObjective(); err != nil {
			return err
		}
	}
	// Do not reuse an unrelated pending objective question, even if it has
	// the same kind/scope. Its text wasn't an exact-commit approval request.
	id, err := st.openFreshQuestion(model.QuestionDone, st.snap.obj.ID, "objective", pin.question(), true)
	if err != nil {
		return err
	}
	cards := slices.Clone(previous.Cards)
	for _, card := range excluded {
		if !slices.Contains(cards, card) {
			cards = append(cards, card)
		}
	}
	return st.event("objective:"+st.snap.obj.ID, objectiveComparisonExemptionEvent,
		comparisonExemption{Question: id, Cards: cards, Acceptance: &pin, Supersedes: previous.Question})
}

func (s *snap) comparisonMergeUnknown() bool {
	return s.hasEvent("objective:"+s.obj.ID, comparisonGitUnknown)
}

func (st *step) acceptanceRejectedAlready(question string, pin comparisonAcceptancePin) bool {
	for _, ev := range st.snap.events {
		if ev.Entity != "question:"+question || ev.Type != comparisonAcceptanceRejected {
			continue
		}
		var attempted comparisonAcceptancePin
		if json.Unmarshal(ev.Data, &attempted) == nil && attempted == pin {
			return true
		}
	}
	return false
}

func (st *step) invalidateComparisonApproval(question, reason string) error {
	entity := "question:" + question
	if st.snap.hasEvent(entity, comparisonApprovalInvalidated) {
		return nil
	}
	return st.event(entity, comparisonApprovalInvalidated, map[string]string{"reason": reason})
}

func (st *step) prepareAcceptedComparison(approval model.Question) error {
	if ok, err := st.checkComparisonAcceptanceRepo(approval.ID); err != nil || !ok {
		return err
	}
	receipt, _ := st.snap.comparisonCompletionReceipt()
	questionEntity := "question:" + approval.ID
	invalidated := st.snap.hasEvent(questionEntity, comparisonApprovalInvalidated)
	current, exists := st.currentComparisonAcceptance()
	if !invalidated && (!exists || receipt.Acceptance == nil || *receipt.Acceptance != current) {
		// Commit revocation before retesting, even if a mutable ref later
		// returns to the original exact OID or acceptance is interrupted.
		return st.invalidateComparisonApproval(approval.ID, "tested_candidate_no_longer_matches")
	}
	if !exists || !st.allFrozen() {
		return nil
	}
	safe, err := st.priorComparisonCardsSafe(receipt)
	if err != nil || !safe {
		return err
	}
	unfinished, excluded, err := st.anyUnfinished()
	if err != nil || unfinished {
		return err
	}
	if invalidated {
		if st.acceptanceRejectedAlready(approval.ID, current) {
			return nil
		}
		ok, err := st.testComparisonAcceptance(current)
		if err != nil {
			return err
		}
		if !ok {
			return st.event(questionEntity, comparisonAcceptanceRejected, current)
		}
		return st.openComparisonCandidate(current, excluded, receipt)
	}
	return st.prepareComparisonGitEntry(approval.ID, current)
}

// An unsupported or temporarily busy repository is not evidence that the
// approved integration commit changed. Do not inspect its refs or mutate it.
func (st *step) checkComparisonAcceptanceRepo(question string) (bool, error) {
	if st.verifyComparisonAcceptanceSource() {
		return true, nil
	}
	return false, st.noteComparisonGitHeld(question, "acceptance_repository_unverified", workspace.ErrOptionalCandidateHeld)
}

func (st *step) prepareComparisonGitEntry(question string, pin comparisonAcceptancePin) error {
	for _, primary := range []string{"refs/heads/main", "refs/heads/master"} {
		base, ok := st.resolveComparisonRef(primary)
		if !ok {
			// Use master only when main is demonstrably absent.
			_, absent := acceptGit(proc.WithExactGitObjects(st.ctx), st.snap.project.RepoPath, "show-ref", "--verify", "--quiet", primary)
			var status *exec.ExitError
			if errors.As(absent, &status) && status.ExitCode() == 1 {
				continue
			}
			return st.noteComparisonGitHeld(question, "primary_ref_unavailable", workspace.ErrOptionalCandidateHeld)
		}
		err := (workspace.Git{}).CheckOptionalCandidate(st.ctx, st.snap.project.RepoPath, primary, pin.Ref, base, pin.OID, st.comparisonWorktreeRoot())
		if err != nil {
			_, storeErr := st.recordComparisonGitResult(question, pin, err)
			return storeErr
		}
		return st.saveComparisonEntry(question, pin, primary, base)
	}
	return st.noteComparisonGitHeld(question, "primary_ref_unavailable", workspace.ErrOptionalCandidateHeld)
}

// recordComparisonGitResult persists typed outcomes inside the dedicated
// writer. This returned bool says whether a pre-entry fence must stay open.
func (st *step) recordComparisonGitResult(question string, pin comparisonAcceptancePin, err error) (bool, error) {
	switch {
	case errors.Is(err, workspace.ErrOptionalIntegrationChanged):
		return false, st.invalidateComparisonApproval(question, "integration_changed_during_git_validation")
	case errors.Is(err, workspace.ErrOptionalCandidateUpdateUnknown), errors.Is(err, workspace.ErrOptionalRefProofAmbiguous):
		reason := "operator_reconciliation_required"
		if errors.Is(err, workspace.ErrOptionalRefProofAmbiguous) {
			reason = "candidate_ref_verification_ambiguous"
		}
		st.comparisonDiagnostic = err
		return true, st.event("objective:"+st.snap.obj.ID, comparisonGitUnknown,
			map[string]string{"question_id": question, "accepted_commit": pin.OID, "reason": reason})
	case !errors.Is(err, workspace.ErrOptionalCandidateHeld):
		return true, err
	}
	reason := "local_git_verification_failed"
	switch {
	case errors.Is(err, workspace.ErrOptionalPrecommitNoWrite):
		// Git proved that no commit packet was attempted and both direct
		// refs remain exact. Do not count this as a merge, consume the
		// approval, or keep the phase-1 fence open.
		reason = "git_prepare_no_write_retriable"
	case errors.Is(err, workspace.ErrOptionalPrimaryCheckedOut):
		reason = "primary_checked_out"
	case errors.Is(err, workspace.ErrOptionalCandidateDiverged):
		reason = "not_fast_forward"
	case errors.Is(err, workspace.ErrOptionalCandidateChanged):
		reason = "primary_ref_changed"
	case errors.Is(err, workspace.ErrOptionalRepositoryBusy):
		reason = "repository_busy"
	}
	return false, st.noteComparisonGitHeld(question, reason, err)
}

func (st *step) noteComparisonGitHeld(question, reason string, diagnostic error) error {
	entity := "question:" + question
	for _, ev := range st.snap.events {
		if ev.Entity != entity || ev.Type != comparisonGitHeld {
			continue
		}
		var data map[string]string
		if json.Unmarshal(ev.Data, &data) == nil && data["reason"] == reason {
			return nil
		}
	}
	st.comparisonDiagnostic = diagnostic
	return st.event(entity, comparisonGitHeld, map[string]string{"reason": reason})
}
