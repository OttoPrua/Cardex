package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// externalIndependentReviewReceipt is local manager-attested evidence of a
// completed independent review. It is not cryptographic proof of Human identity
// and is not a typeReview card, producer RESULT, or AUTHOR_ADVERSARIAL check.
type externalIndependentReviewReceipt struct {
	Schema                  string            `json:"schema"`
	Kind                    string            `json:"kind"`
	Method                  string            `json:"method"`
	Actor                   string            `json:"actor"`
	SessionID               string            `json:"session_id"`
	WorkflowID              string            `json:"workflow_id"`
	WriterTaskID            string            `json:"writer_task_id"`
	WriterSessionID         string            `json:"writer_session_id"`
	WriterAttemptID         string            `json:"writer_attempt_id"`
	WriterRevision          int64             `json:"writer_revision"`
	CandidateCommit         string            `json:"candidate_commit"`
	CandidateTree           string            `json:"candidate_tree"`
	ChangedPaths            []string          `json:"changed_paths"`
	PathDigests             map[string]string `json:"path_digests"`
	ArtifactPath            string            `json:"artifact_path"`
	ArtifactSHA256          string            `json:"artifact_sha256"`
	EvidenceSHA256          string            `json:"evidence_sha256"`
	OwnerAuthorizationScope string            `json:"owner_authorization_scope"`
	CustodyClosed           bool              `json:"custody_closed"`
	Attestation             string            `json:"attestation,omitempty"`
}

type validatedExternalReview struct {
	ReceiptPath string
	ReceiptSHA  string
	Receipt     *externalIndependentReviewReceipt
	Artifact    string
	ArtifactSHA string
	EvidenceSHA string
	Verdict     *reviewVerdict
	PathDigests map[string]string
	Writer      *Task
	Commit      string
	Tree        string
}

// WorkflowLocalIntegration identifies actual manager adoption, never a native
// provider result. Its receipt is local attestation, like the external review.
type WorkflowLocalIntegration struct {
	ReceiptPath         string `json:"receipt_path"`
	ReceiptSHA256       string `json:"receipt_sha256"`
	TargetRepo          string `json:"target_repo"`
	Actor               string `json:"actor"`
	SessionID           string `json:"session_id"`
	IntegrationTaskID   string `json:"integration_task_id"`
	IntegrationRevision int64  `json:"integration_revision"`
}

type manualLocalIntegrationReceipt struct {
	Schema                       string            `json:"schema"`
	Method                       string            `json:"method"`
	WorkflowID                   string            `json:"workflow_id"`
	IntegrationTaskID            string            `json:"integration_task_id"`
	IntegrationRevision          int64             `json:"integration_revision"`
	Actor                        string            `json:"actor"`
	SessionID                    string            `json:"session_id"`
	TargetRepo                   string            `json:"target_repo"`
	OwnerAuthorizationScope      string            `json:"owner_authorization_scope"`
	OwnerAuthorizationTargetRepo string            `json:"owner_authorization_target_repo"`
	ExternalReviewSHA256         string            `json:"external_review_sha256"`
	CandidateCommit              string            `json:"candidate_commit"`
	CandidateTree                string            `json:"candidate_tree"`
	FileSHA256                   map[string]string `json:"file_sha256"`
	FileGitModes                 map[string]string `json:"file_git_modes"`
	ArtifactPath                 string            `json:"artifact_path"`
	ArtifactSHA256               string            `json:"artifact_sha256"`
	ChecksPath                   string            `json:"checks_path"`
	ChecksSHA256                 string            `json:"checks_sha256"`
	ChecksPassed                 bool              `json:"checks_passed"`
}

const manualLocalIntegrationSchema = "cardex.workflow.manual_local_integration.v1"
const manualLocalIntegrationMethod = "manual_local_integration"

func freezeRetainedHeldSourceCandidate(root string, cfg *Config, wf *WorkflowRecord, cand WorkflowCandidate) error {
	if wf == nil || wf.ID == "" {
		return errWorkflowMalformed
	}
	return withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		return freezeRetainedHeldSourceCandidateLocked(root, cfg, wf, cand)
	})
}

func freezeRetainedHeldSourceCandidateLocked(root string, cfg *Config, wf *WorkflowRecord, cand WorkflowCandidate) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	if strings.TrimSpace(cand.Commit) == "" || strings.TrimSpace(cand.Tree) == "" {
		return fmt.Errorf("%w: candidate needs both commit and tree", errWorkflowMalformed)
	}
	if err := assertRetainedHeldSourceFreezeAllowed(root, wf); err != nil {
		return err
	}
	return bindFrozenWorkflowCandidate(root, cfg, wf, cand, freezeKindRetainedHeldSource)
}

func assertRetainedHeldSourceFreezeAllowed(root string, wf *WorkflowRecord) error {
	if wf == nil || strings.TrimSpace(wf.WriterTaskID) == "" {
		return fmt.Errorf("%w: retained held SOURCE freeze needs the original writer", errWorkflowMalformed)
	}
	writer, err := findTaskAnywhere(root, wf.WriterTaskID)
	if err != nil || writer == nil {
		return fmt.Errorf("%w: writer %s missing/unreadable", errWorkflowCustody, wf.WriterTaskID)
	}
	if writer.Status != statusHeld {
		return fmt.Errorf("%w: writer %s is %s; -retained-held-source only freezes a held SOURCE candidate",
			errWorkflowCustody, writer.ID, writer.Status)
	}
	if taskHasLiveWriterProof(root, writer) {
		return fmt.Errorf("%w: writer %s still has live process/lease/resource custody",
			errWorkflowCustody, writer.ID)
	}
	if workspaceLeaseHeld(writer.Dir) {
		return fmt.Errorf("%w: writer %s workspace lease is still held", errWorkflowCustody, writer.ID)
	}
	if !recordedAttemptCloseout(root, writer) {
		return fmt.Errorf("%w: writer %s has no conclusive original-attempt closeout",
			errWorkflowCustody, writer.ID)
	}
	if workflowCardHasUnresolvedCustody(root, writer) {
		return fmt.Errorf("%w: writer %s custody is unresolved", errWorkflowCustody, writer.ID)
	}
	if err := workflowWriteDomainClaimProvable(wf); err != nil {
		return err
	}
	live := mergeLiveWriterTasks(nil, reconstructLiveWriterClaims(root))
	for _, other := range live {
		if other == nil || other.ID == writer.ID {
			continue
		}
		if writerConflictsWithActive(writer, []*Task{other}) {
			return fmt.Errorf("%w: writer %s still conflicts with live claim %s",
				errWorkflowCustody, writer.ID, other.ID)
		}
	}
	return nil
}

func workflowWriteDomainClaimProvable(wf *WorkflowRecord) error {
	if wf == nil {
		return errWorkflowMalformed
	}
	if len(wf.WriteDomain.Paths) == 0 {
		return fmt.Errorf("%w: write domain paths are empty (ambiguous claim)", errWriteDomainAmbiguousClaim)
	}
	root, ok := provenWorkflowRepoRoot(wf)
	if !ok {
		return fmt.Errorf("%w: repository identity cannot be proven", errWriteDomainAmbiguousClaim)
	}
	if _, err := NormalizeWriteDomain(root, wf.WriteDomain); err != nil {
		return fmt.Errorf("%w: %v", errWriteDomainAmbiguousClaim, err)
	}
	return nil
}

func workflowPathsInsideWriteDomain(wf *WorkflowRecord, paths []string) error {
	if wf == nil {
		return errWorkflowMalformed
	}
	if len(paths) == 0 {
		return fmt.Errorf("%w: retained held SOURCE freeze needs owned changed paths", errWorkflowMalformed)
	}
	claimed := wf.WriteDomain.Paths
	if len(claimed) == 0 {
		return fmt.Errorf("%w: write domain paths are empty (ambiguous claim)", errWriteDomainAmbiguousClaim)
	}
	for _, raw := range paths {
		p, err := normalizeRepoRelPathClaim(raw)
		if err != nil {
			return err
		}
		if !pathCoveredByWriteDomain(p, claimed) {
			return fmt.Errorf("%w: changed path %q is outside the owned write domain",
				errWriteDomainAmbiguousClaim, raw)
		}
	}
	return nil
}

func normalizeRepoRelPathClaim(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("%w: empty changed path", errWriteDomainAmbiguousClaim)
	}
	slash := filepath.ToSlash(s)
	if filepath.IsAbs(s) || filepath.IsAbs(filepath.FromSlash(s)) || strings.HasPrefix(slash, "/") {
		return "", fmt.Errorf("%w: changed path %q is absolute", errWriteDomainAmbiguousClaim, raw)
	}
	if slash == "." || slash == ".." || strings.HasPrefix(slash, "../") ||
		strings.Contains(slash, "/../") || strings.HasSuffix(slash, "/..") || strings.Contains(slash, "..") {
		return "", fmt.Errorf("%w: changed path %q is ambiguous", errWriteDomainAmbiguousClaim, raw)
	}
	rel := filepath.ToSlash(filepath.Clean(s))
	if rel == "." || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: changed path %q is ambiguous", errWriteDomainAmbiguousClaim, raw)
	}
	return strings.TrimSuffix(rel, "/"), nil
}

func pathCoveredByWriteDomain(rel string, claimed []string) bool {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, c := range claimed {
		c = strings.TrimSpace(filepath.ToSlash(c))
		if c == "" || strings.HasPrefix(c, "/") {
			continue
		}
		if rel == c || strings.HasPrefix(rel, c+"/") {
			return true
		}
	}
	return false
}

func candidateGitChangedFiles(worktree, commit string) ([]string, error) {
	parentOut, err := gitOutput(worktree, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^")
	if err != nil {
		return nil, fmt.Errorf("%w: candidate commit has no parent; cannot derive a changed-file diff",
			errWorkflowMalformed)
	}
	parent := strings.TrimSpace(string(parentOut))
	out, err := gitOutput(worktree, "diff-tree", "-r", "--no-commit-id", "--name-only", parent, commit)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot derive candidate git diff: %v", errWorkflowMalformed, err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		p := strings.TrimSpace(filepath.ToSlash(line))
		if p == "" {
			continue
		}
		files = append(files, p)
	}
	return files, nil
}

func assertGitDiffCoveredByDeclared(worktree, commit string, declared, domainPaths []string) error {
	actual, err := candidateGitChangedFiles(worktree, commit)
	if err != nil {
		return err
	}
	if len(actual) == 0 {
		return fmt.Errorf("%w: candidate has an empty file diff against its parent", errWorkflowMalformed)
	}
	for _, f := range actual {
		if _, err := normalizeRepoRelPathClaim(f); err != nil {
			return fmt.Errorf("%w: git changed path %q", err, f)
		}
		if !pathCoveredByWriteDomain(f, declared) {
			return fmt.Errorf("%w: undeclared changed file %q", errWriteDomainAmbiguousClaim, f)
		}
		if !pathCoveredByWriteDomain(f, domainPaths) {
			return fmt.Errorf("%w: changed file %q is outside the owned write domain",
				errWriteDomainAmbiguousClaim, f)
		}
	}
	return nil
}

func digestPathsAtTree(worktree, tree string, paths []string) (map[string]string, error) {
	if strings.TrimSpace(worktree) == "" || strings.TrimSpace(tree) == "" {
		return nil, fmt.Errorf("%w: digest needs worktree and tree", errWorkflowMalformed)
	}
	out := make(map[string]string, len(paths))
	for _, rel := range paths {
		rel = strings.TrimSpace(filepath.ToSlash(rel))
		if rel == "" {
			return nil, fmt.Errorf("%w: empty changed path", errWorkflowMalformed)
		}
		d, err := digestOnePathAtTree(worktree, tree, rel)
		if err != nil {
			return nil, err
		}
		out[rel] = d
	}
	return out, nil
}

func digestOnePathAtTree(worktree, tree, rel string) (string, error) {
	out, err := gitOutput(worktree, "ls-tree", "-r", "--full-tree", tree, "--", rel)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("%w: path %q missing from tree %s", errWorkflowMalformed, rel, tree)
	}
	type fileHash struct{ path, sum string }
	var files []fileHash
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			return "", fmt.Errorf("%w: ls-tree line for %q", errWorkflowMalformed, rel)
		}
		meta, p := line[:tab], line[tab+1:]
		fields := strings.Fields(meta)
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		blob, err := gitOutput(worktree, "cat-file", "blob", fields[2])
		if err != nil {
			return "", fmt.Errorf("%w: cat-file blob %s: %v", errWorkflowMalformed, fields[2], err)
		}
		files = append(files, fileHash{path: p, sum: sha256Hex(string(blob))})
	}
	if len(files) == 0 {
		return "", fmt.Errorf("%w: path %q has no blobs in tree %s", errWorkflowMalformed, rel, tree)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\t%s\n", f.path, f.sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func externalReviewEvidenceSHA256(artifactSHA, commit, tree string, pathDigests map[string]string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(artifactSHA))
	b.WriteByte('\n')
	b.WriteString(strings.TrimSpace(commit))
	b.WriteByte('\n')
	b.WriteString(strings.TrimSpace(tree))
	b.WriteByte('\n')
	keys := make([]string, 0, len(pathDigests))
	for k := range pathDigests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\t')
		b.WriteString(pathDigests[k])
		b.WriteByte('\n')
	}
	return sha256Hex(b.String())
}

func fileSHA256Hex(path string) (string, []byte, error) {
	data, err := readRegularFileNoBlock(path)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, nil
}

func parseExternalIndependentReviewReceipt(data []byte) (*externalIndependentReviewReceipt, error) {
	var rec externalIndependentReviewReceipt
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("%w: receipt is not JSON: %v", errWorkflowExternalReview, err)
	}
	return &rec, nil
}

func ingestExternalIndependentReview(root string, cfg *Config, wf *WorkflowRecord, receiptPath string) error {
	if wf == nil || wf.ID == "" {
		return errWorkflowMalformed
	}
	return withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		return ingestExternalIndependentReviewLocked(root, cfg, wf, receiptPath)
	})
}

func ingestExternalIndependentReviewLocked(root string, cfg *Config, wf *WorkflowRecord, receiptPath string) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	validated, err := validateExternalIndependentReview(root, cfg, wf, receiptPath)
	if err != nil {
		return err
	}
	if wf.Review != nil && wf.Review.Method == reviewMethodExternalIndependentLocal {
		if wf.Review.ReceiptSHA256 == validated.ReceiptSHA &&
			wf.Review.ArtifactSHA256 == validated.ArtifactSHA &&
			wf.Review.EvidenceSHA256 == validated.EvidenceSHA &&
			wf.Review.Actor == validated.Receipt.Actor &&
			wf.Review.SessionID == validated.Receipt.SessionID {
			return nil
		}
		return fmt.Errorf("%w: a different external review is already bound; refusing duplicate without mutation",
			errWorkflowExternalReview)
	}
	if wf.ReviewerTaskID != "" {
		return fmt.Errorf("%w: model reviewer %s is already bound; refusing a second review identity",
			errWorkflowDuplicateRole, wf.ReviewerTaskID)
	}
	if active, found := workflowActiveRole(root, wf, "reviewer"); found {
		return duplicateRoleErr("reviewer", active)
	}
	v := validated.Verdict
	snap := &WorkflowReview{
		Round:           wf.CurrentRound,
		Verdict:         v.Verdict,
		P0:              append([]string(nil), v.P0...),
		P1:              append([]string(nil), v.P1...),
		P2:              append([]string(nil), v.P2...),
		Admissible:      true,
		CandidateCommit: validated.Commit,
		CandidateTree:   validated.Tree,
		Method:          reviewMethodExternalIndependentLocal,
		Actor:           validated.Receipt.Actor,
		SessionID:       validated.Receipt.SessionID,
		AttemptID:       validated.Receipt.WriterAttemptID,
		WriterTaskID:    validated.Writer.ID,
		WriterSessionID: validated.Writer.SessionID,
		WriterRevision:  validated.Writer.Revision,
		ArtifactPath:    validated.Receipt.ArtifactPath,
		ArtifactSHA256:  validated.ArtifactSHA,
		EvidenceSHA256:  validated.EvidenceSHA,
		ReceiptPath:     validated.ReceiptPath,
		ReceiptSHA256:   validated.ReceiptSHA,
		OwnerScope:      ownerScopeLocalIntegration,
		PathDigests:     copyStringMap(validated.PathDigests),
	}
	wf.Review = snap
	wf.ReviewerTaskID = ""
	wf.EffectGates.Integration = effectGateHeld
	wf.Status = workflowStatusReviewPassed
	if err := emitRootNotify(root, wf, rootNotifyReviewPassed,
		"admissible external independent local review; integration, live and cutover all remain held"); err != nil {
		return err
	}
	if err := syncIntegrationGate(root, wf); err != nil {
		return err
	}
	return persistWorkflow(root, cfg, wf)
}

func validateExternalIndependentReview(root string, cfg *Config, wf *WorkflowRecord, receiptPath string) (*validatedExternalReview, error) {
	_ = cfg
	if wf == nil {
		return nil, errWorkflowMalformed
	}
	receiptPath = strings.TrimSpace(receiptPath)
	if receiptPath == "" {
		return nil, fmt.Errorf("%w: receipt path required", errWorkflowExternalReview)
	}
	abs, err := filepath.Abs(receiptPath)
	if err != nil {
		return nil, fmt.Errorf("%w: receipt path: %v", errWorkflowExternalReview, err)
	}
	receiptSHA, raw, err := fileSHA256Hex(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: read receipt: %v", errWorkflowExternalReview, err)
	}
	rec, err := parseExternalIndependentReviewReceipt(raw)
	if err != nil {
		return nil, err
	}
	if reason := receiptIdentityHoldReason(rec); reason != "" {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, reason)
	}
	if rec.WorkflowID != wf.ID {
		return nil, fmt.Errorf("%w: receipt workflow_id does not match", errWorkflowExternalReview)
	}
	if wf.Candidate == nil || (wf.Candidate.Commit == "" && wf.Candidate.Tree == "") {
		return nil, fmt.Errorf("%w: freeze a candidate before external review ingest", errWorkflowMalformed)
	}
	if wf.WriterTaskID == "" {
		return nil, fmt.Errorf("%w: no writer to bind", errWorkflowMalformed)
	}
	writer, err := findTaskAnywhere(root, wf.WriterTaskID)
	if err != nil || writer == nil {
		return nil, fmt.Errorf("%w: writer missing/unreadable", errWorkflowCustody)
	}
	if rec.WriterTaskID != writer.ID || rec.WriterTaskID != wf.WriterTaskID {
		return nil, fmt.Errorf("%w: writer identity mismatch", errWorkflowExternalReview)
	}
	if rec.WriterSessionID != writer.SessionID {
		return nil, fmt.Errorf("%w: writer session mismatch", errWorkflowExternalReview)
	}
	wantAttempt := goalAttemptID(writer)
	if rec.WriterAttemptID == "" || rec.WriterAttemptID != wantAttempt {
		return nil, fmt.Errorf("%w: writer attempt mismatch", errWorkflowExternalReview)
	}
	if rec.WriterRevision != writer.Revision {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonStaleRevision)
	}
	if sameAuthorActorOrSession(writer, rec.Actor, rec.SessionID) {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonSameAuthor)
	}
	if strings.TrimSpace(rec.OwnerAuthorizationScope) != ownerScopeLocalIntegration {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonMissingAuthority)
	}
	if rec.OwnerAuthorizationScope == effectGateReleased ||
		strings.EqualFold(rec.OwnerAuthorizationScope, "live") ||
		strings.EqualFold(rec.OwnerAuthorizationScope, "cutover") {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonMissingAuthority)
	}
	if err := assertRetainedHeldSourceFreezeAllowed(root, wf); err != nil {
		return nil, err
	}
	if !rec.CustodyClosed {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonCustody)
	}
	commit, tree, err := verifyWorkflowCandidate(wf.Worktree, rec.CandidateCommit, rec.CandidateTree)
	if err != nil {
		return nil, err
	}
	if commit != wf.Candidate.Commit || tree != wf.Candidate.Tree {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonCandidateMismatch)
	}
	paths := append([]string(nil), rec.ChangedPaths...)
	if len(paths) == 0 {
		paths = append([]string(nil), wf.Candidate.ChangedPaths...)
	}
	if err := workflowPathsInsideWriteDomain(wf, paths); err != nil {
		return nil, err
	}
	if err := assertGitDiffCoveredByDeclared(wf.Worktree, commit, paths, wf.WriteDomain.Paths); err != nil {
		return nil, err
	}
	digests, err := digestPathsAtTree(wf.Worktree, tree, paths)
	if err != nil {
		return nil, err
	}
	if !stringMapsEqual(digests, rec.PathDigests) {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonEvidenceDrift)
	}
	if wf.Candidate.PathDigests != nil && !stringMapsEqual(digests, wf.Candidate.PathDigests) {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonEvidenceDrift)
	}
	artPath := strings.TrimSpace(rec.ArtifactPath)
	if artPath == "" || !filepath.IsAbs(artPath) {
		return nil, fmt.Errorf("%w: artifact_path must be an absolute file", errWorkflowExternalReview)
	}
	artSHA, artBytes, err := fileSHA256Hex(artPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read artifact: %v", errWorkflowExternalReview, err)
	}
	if artSHA != strings.ToLower(strings.TrimSpace(rec.ArtifactSHA256)) {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonEvidenceDrift)
	}
	evidence := externalReviewEvidenceSHA256(artSHA, commit, tree, digests)
	if evidence != strings.ToLower(strings.TrimSpace(rec.EvidenceSHA256)) {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonEvidenceDrift)
	}
	rawArt := string(artBytes)
	v := parseReviewVerdict(rawArt)
	if reason := reviewHoldReason(v, rawArt); reason != "" {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, reason)
	}
	if !reviewVerdictIsAdmissiblePass(v) {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonFindingsOpen)
	}
	gate := &IntegrationGate{CandidateCommit: commit, CandidateTree: tree}
	snap := &WorkflowReview{CandidateCommit: commit, CandidateTree: tree}
	if !candidateIdentitiesMatch(gate, snap, wf.Candidate) {
		return nil, fmt.Errorf("%w: %s", errWorkflowExternalReview, holdReasonCandidateMismatch)
	}
	return &validatedExternalReview{
		ReceiptPath: abs,
		ReceiptSHA:  receiptSHA,
		Receipt:     rec,
		Artifact:    rawArt,
		ArtifactSHA: artSHA,
		EvidenceSHA: evidence,
		Verdict:     v,
		PathDigests: digests,
		Writer:      writer,
		Commit:      commit,
		Tree:        tree,
	}, nil
}

func receiptIdentityHoldReason(rec *externalIndependentReviewReceipt) string {
	if rec == nil {
		return holdReasonIncompleteEvidence
	}
	if rec.Schema != externalIndependentReviewSchemaV1 {
		return holdReasonIncompleteEvidence
	}
	if rec.Kind != externalIndependentReviewMethod || rec.Method != externalIndependentReviewMethod {
		if isAuthorAdversarialMethod(rec.Kind) || isAuthorAdversarialMethod(rec.Method) {
			return holdReasonAuthorAdversarial
		}
		return holdReasonIncompleteEvidence
	}
	if isAuthorAdversarialMethod(rec.Kind) || isAuthorAdversarialMethod(rec.Method) {
		return holdReasonAuthorAdversarial
	}
	if strings.TrimSpace(rec.Actor) == "" || strings.TrimSpace(rec.SessionID) == "" {
		return holdReasonIncompleteEvidence
	}
	return ""
}

func isAuthorAdversarialMethod(s string) bool {
	n := strings.ToLower(strings.TrimSpace(s))
	return n == "author_adversarial" || n == "author-adversarial" || strings.Contains(n, "author_adversarial")
}

func sameAuthorActorOrSession(writer *Task, actor, session string) bool {
	if writer == nil {
		return true
	}
	actor = strings.TrimSpace(actor)
	session = strings.TrimSpace(session)
	if actor == "" || session == "" {
		return true
	}
	if actor == writer.ID {
		return true
	}
	if writer.SessionID != "" && session == writer.SessionID {
		return true
	}
	if writer.SessionID != "" && actor == writer.SessionID {
		return true
	}
	return false
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func evaluateExternalIndependentRelease(root string, cfg *Config, t *Task) IntegrationReleaseDecision {
	dec := IntegrationReleaseDecision{HoldReason: holdReasonIncompleteEvidence}
	if t == nil || t.IntegrationGate == nil {
		dec.HoldReason = holdReasonNoGate
		return dec
	}
	gate := t.IntegrationGate
	if strings.TrimSpace(gate.WorkflowID) == "" || strings.TrimSpace(gate.ExternalReceiptPath) == "" {
		dec.HoldReason = holdReasonIncompleteEvidence
		return dec
	}
	wf, err := loadWorkflow(root, cfg, gate.WorkflowID)
	if err != nil || wf == nil {
		dec.HoldReason = holdReasonIncompleteEvidence
		return dec
	}
	if wf.Review == nil || wf.Review.Method != reviewMethodExternalIndependentLocal {
		dec.HoldReason = holdReasonIncompleteEvidence
		return dec
	}
	validated, err := validateExternalIndependentReview(root, cfg, wf, gate.ExternalReceiptPath)
	if err != nil {
		dec.HoldReason = externalHoldReasonFromErr(err)
		return dec
	}
	if validated.ReceiptSHA != strings.TrimSpace(gate.ExternalReceiptSHA256) ||
		validated.ReceiptSHA != strings.TrimSpace(wf.Review.ReceiptSHA256) {
		dec.HoldReason = holdReasonEvidenceDrift
		return dec
	}
	if validated.ArtifactSHA != wf.Review.ArtifactSHA256 || validated.EvidenceSHA != wf.Review.EvidenceSHA256 {
		dec.HoldReason = holdReasonEvidenceDrift
		return dec
	}
	if validated.Receipt.Actor != wf.Review.Actor || validated.Receipt.SessionID != wf.Review.SessionID {
		dec.HoldReason = holdReasonEvidenceDrift
		return dec
	}
	if !candidateIdentitiesMatch(gate, &WorkflowReview{
		CandidateCommit: validated.Commit,
		CandidateTree:   validated.Tree,
	}, wf.Candidate) {
		dec.HoldReason = holdReasonCandidateMismatch
		return dec
	}
	if writerDispatchBlocksReleasedIntegration(root, wf, t) {
		dec.HoldReason = holdReasonCustody
		return dec
	}
	dec.Admit = true
	dec.HoldReason = ""
	dec.Verdict = validated.Verdict
	return dec
}

func externalHoldReasonFromErr(err error) string {
	if err == nil {
		return holdReasonIncompleteEvidence
	}
	msg := err.Error()
	for _, reason := range []string{
		holdReasonSameAuthor,
		holdReasonStaleRevision,
		holdReasonMissingAuthority,
		holdReasonEvidenceDrift,
		holdReasonAuthorAdversarial,
		holdReasonCandidateMismatch,
		holdReasonCustody,
		holdReasonFindingsOpen,
		holdReasonConcerns,
		holdReasonBlock,
		holdReasonMissingOutput,
		holdReasonUnknownVocabulary,
		holdReasonIncompleteEvidence,
	} {
		if strings.Contains(msg, reason) {
			return reason
		}
	}
	if strings.Contains(msg, "lease") || strings.Contains(msg, "live process") || strings.Contains(msg, "closeout") {
		return holdReasonCustody
	}
	return holdReasonIncompleteEvidence
}

// writerDispatchBlocksReleasedIntegration is the custody-aware consumer check
// tick uses (reconstructed live process/lease claims). A held original writer
// with recorded closeout and released process/lease is not a live dispatch
// occupant of the same write domain.
func writerDispatchBlocksReleasedIntegration(root string, wf *WorkflowRecord, integ *Task) bool {
	if integ == nil {
		return true
	}
	if taskHasLiveWriterProof(root, integ) {
		return true
	}
	live := mergeLiveWriterTasks(nil, reconstructLiveWriterClaims(root))
	filtered := make([]*Task, 0, len(live))
	for _, other := range live {
		if other == nil {
			continue
		}
		if wf != nil && other.ID == wf.WriterTaskID && other.Status == statusHeld &&
			recordedAttemptCloseout(root, other) && !taskHasLiveWriterProof(root, other) {
			continue
		}
		filtered = append(filtered, other)
	}
	return writerConflictsWithActive(integ, filtered)
}

// recordManualLocalIntegration verifies adoption already performed by the
// authorized manager. It writes only Cardex records; it never copies source,
// restores scheduling, invents an attempt, or invokes a provider.
func recordManualLocalIntegration(root string, cfg *Config, wf *WorkflowRecord, receiptPath, targetRepo string) error {
	if wf == nil || wf.ID == "" {
		return errWorkflowMalformed
	}
	return withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		if err := refreshWorkflow(root, cfg, wf); err != nil {
			return err
		}
		return withSortedTaskControlLocks(root, []string{wf.WriterTaskID, wf.IntegrationTaskID}, func() error {
			return recordManualLocalIntegrationLocked(root, cfg, wf, receiptPath, targetRepo)
		})
	})
}

func recordManualLocalIntegrationLocked(root string, cfg *Config, wf *WorkflowRecord, receiptPath, targetRepo string) error {
	refuse := func(reason string) error {
		return fmt.Errorf("%w: manual local integration: %s", errWorkflowHeld, reason)
	}
	abs, err := filepath.Abs(receiptPath)
	if err != nil || strings.TrimSpace(receiptPath) == "" {
		return refuse("receipt path required")
	}
	receiptSHA, raw, err := fileSHA256Hex(abs)
	if err != nil {
		return refuse("receipt unreadable")
	}
	var rec manualLocalIntegrationReceipt
	if err := json.Unmarshal(raw, &rec); err != nil {
		return refuse("invalid receipt JSON")
	}
	if rec.Schema != manualLocalIntegrationSchema || rec.Method != manualLocalIntegrationMethod ||
		rec.WorkflowID != wf.ID || rec.IntegrationTaskID != wf.IntegrationTaskID || rec.IntegrationRevision <= 0 {
		return refuse("receipt identity mismatch")
	}
	if wf.Review == nil || wf.Review.Method != reviewMethodExternalIndependentLocal || wf.Candidate == nil {
		return refuse("completed external independent review required")
	}
	validated, err := validateExternalIndependentReview(root, cfg, wf, wf.Review.ReceiptPath)
	if err != nil {
		return err
	}
	if validated.ReceiptSHA != wf.Review.ReceiptSHA256 || rec.ExternalReviewSHA256 != validated.ReceiptSHA ||
		validated.ArtifactSHA != wf.Review.ArtifactSHA256 || validated.EvidenceSHA != wf.Review.EvidenceSHA256 ||
		validated.Receipt.Actor != wf.Review.Actor || validated.Receipt.SessionID != wf.Review.SessionID ||
		rec.CandidateCommit != validated.Commit || rec.CandidateTree != validated.Tree {
		return refuse("external review/candidate drift")
	}
	if sameAuthorActorOrSession(validated.Writer, rec.Actor, rec.SessionID) {
		return refuse("real distinct manager actor/session required")
	}
	if rec.OwnerAuthorizationScope != ownerScopeLocalIntegration || !filepath.IsAbs(targetRepo) ||
		rec.TargetRepo != targetRepo || rec.OwnerAuthorizationTargetRepo != targetRepo {
		return refuse("exact target local authority required")
	}
	canonical, err := filepath.EvalSymlinks(targetRepo)
	if err != nil {
		return refuse("target repository missing")
	}
	top, err := gitOutput(canonical, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSpace(string(top))) != filepath.Clean(canonical) {
		return refuse("target must be the repository root")
	}
	if filepath.Clean(canonical) != filepath.Clean(targetRepo) {
		return refuse("target must be canonical")
	}
	if !rec.ChecksPassed {
		return refuse("successful adoption checks required")
	}
	for _, evidence := range []struct{ path, sum string }{{rec.ArtifactPath, rec.ArtifactSHA256}, {rec.ChecksPath, rec.ChecksSHA256}} {
		if !filepath.IsAbs(evidence.path) {
			return refuse("absolute artifact/check path required")
		}
		sum, bytes, err := fileSHA256Hex(evidence.path)
		if err != nil || len(bytes) == 0 || sum != evidence.sum {
			return refuse("artifact/check hash drift")
		}
	}
	if err := verifyAdoptedCandidateFiles(wf.Worktree, validated.Tree, wf.Candidate.ChangedPaths, canonical, rec.FileSHA256, rec.FileGitModes); err != nil {
		return err
	}
	// Resume a previously committed terminal projection through the existing
	// recovery primitive before comparing its revision for an exact replay.
	if err := recoverIncompleteTerminalLocked(root, wf.IntegrationTaskID); err != nil {
		return err
	}
	integ, err := loadTask(root, wf.IntegrationTaskID)
	if err != nil || integ.WorkflowID != wf.ID || integ.IntegrationGate == nil || integ.IntegrationGate.WorkflowID != wf.ID ||
		integ.Type != typeSequence {
		return refuse("integration placeholder identity mismatch")
	}
	gate := integ.IntegrationGate
	if gate.WriterTaskID != wf.WriterTaskID || gate.CandidateCommit != validated.Commit || gate.CandidateTree != validated.Tree ||
		gate.ReviewMethod != reviewMethodExternalIndependentLocal || gate.ExternalReceiptSHA256 != validated.ReceiptSHA || gate.ExternalReceiptPath != wf.Review.ReceiptPath {
		return refuse("integration gate identity drift")
	}
	if integ.ActiveAttemptID != "" || integ.SessionID != "" || integ.Attempts != 0 || integ.Step != 0 || integ.Goal != nil ||
		integ.schedulingAllowed() || workspaceLeaseHeld(integ.Dir) || !producerGone(integ, nil) {
		return refuse("integration placeholder active or dispatched")
	}
	attempts, err := os.ReadDir(attemptsDir(root, integ.ID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(attempts) != 0 {
		return refuse("integration placeholder has attempt history")
	}
	events, partial, err := loadTaskEvents(root, integ.ID)
	if err != nil || partial {
		return refuse("integration event history incomplete")
	}
	for _, ev := range events {
		if ev.Type == evDispatched || ev.Type == evRetry || ev.Type == evStepOK || (ev.Type == evQueued && ev.Status == statusQueued) {
			return refuse("integration placeholder was dispatched/retried")
		}
	}
	detail := map[string]any{"method": manualLocalIntegrationMethod, "receipt_sha256": receiptSHA, "target_repo": canonical, "candidate_commit": validated.Commit, "candidate_tree": validated.Tree, "manager_session": rec.SessionID, "provider_dispatches": 0}
	actor := "workflow:manual-local-integration:" + rec.Actor
	if integ.Status == statusDone {
		journal, err := loadTransition(root, integ.ID, integ.LastCommittedTransitionID)
		if err != nil || journal.State != transitionCommitted || journal.EventType != evDone || journal.Actor != actor ||
			journal.ExpectedRevision != rec.IntegrationRevision || journal.NewRevision != integ.Revision || journal.DetailDigest != digestDetail(detail) {
			return refuse("different adoption or stale integration revision")
		}
		if wf.LocalIntegration != nil {
			if wf.LocalIntegration.ReceiptSHA256 != receiptSHA {
				return refuse("different adoption already recorded")
			}
			return nil
		}
		// Recover only this exact committed manual terminal if workflow projection
		// failed after the existing terminal journal committed.
	} else {
		if integ.Status != statusHeld || integ.Revision != rec.IntegrationRevision || wf.LocalIntegration != nil {
			return refuse("unstarted held placeholder/current revision required")
		}
		integ.Status = statusDone
		if err := commitTaskTransitionLocked(root, integ, transitionRequest{EventType: evDone, Actor: actor, Status: statusDone, Step: integ.Step, Detail: detail}); err != nil {
			return err
		}
	}
	wf.LocalIntegration = &WorkflowLocalIntegration{ReceiptPath: abs, ReceiptSHA256: receiptSHA, TargetRepo: canonical, Actor: rec.Actor, SessionID: rec.SessionID, IntegrationTaskID: integ.ID, IntegrationRevision: integ.Revision}
	wf.Status = workflowStatusLocallyIntegrated
	wf.EffectGates.Integration = effectGateReleased
	// Live/cutover are untouched: manual adoption is not runtime authority.
	return persistWorkflow(root, cfg, wf)
}

func verifyAdoptedCandidateFiles(source, tree string, paths []string, target string, sums, modes map[string]string) error {
	out, err := gitOutput(source, append([]string{"ls-tree", "-r", "--full-tree", tree, "--"}, paths...)...)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		meta, rel, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return fmt.Errorf("%w: adoption requires regular candidate files", errWorkflowHeld)
		}
		if _, err := normalizeRepoRelPathClaim(rel); err != nil {
			return err
		}
		expected[rel] = true
		blob, err := gitOutput(source, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		want := sha256Hex(string(blob))
		if sums[rel] != want || modes[rel] != fields[0] {
			return fmt.Errorf("%w: candidate file receipt mismatch %s", errWorkflowHeld, rel)
		}
		dest := filepath.Join(target, filepath.FromSlash(rel))
		// Reject symlink ancestors as well as a symlink file; never verify bytes
		// outside the exact authorized repository through path redirection.
		current := target
		for _, part := range strings.Split(rel, "/") {
			current = filepath.Join(current, part)
			st, err := os.Lstat(current)
			if err != nil || st.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: adopted path missing/redirected %s", errWorkflowHeld, rel)
			}
		}
		st, err := os.Stat(dest)
		got, _, readErr := fileSHA256Hex(dest)
		if err != nil || !st.Mode().IsRegular() || readErr != nil || got != want ||
			(fields[0] == "100644" && st.Mode().Perm()&0111 != 0) || (fields[0] == "100755" && st.Mode().Perm()&0111 != 0111) {
			return fmt.Errorf("%w: adopted bytes/mode mismatch %s", errWorkflowHeld, rel)
		}
	}
	if len(expected) == 0 || len(expected) != len(sums) || len(expected) != len(modes) {
		return fmt.Errorf("%w: exact adopted file set required", errWorkflowHeld)
	}
	return nil
}
