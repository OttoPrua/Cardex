package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/store"
)

const stageHandoffName = "stage-handoff.v1.json"
const stageHandoffFallback = "stage_handoff_fallback"
const stagePromptLimit = 12 << 10

type stageLink struct {
	Card    string `json:"card"`
	Attempt string `json:"attempt"`
	Mode    string `json:"-"`
}

type stageHandoff struct {
	Version            int                   `json:"version"`
	Stage              string                `json:"stage"`
	TargetCard         string                `json:"target_card"`
	TargetBase         string                `json:"target_base"`
	Source             harvest.StageEvidence `json:"source"`
	FrozenDiff         *stageDiff            `json:"diff,omitempty"`
	ReworkNote         string                `json:"review_note,omitempty"`
	SourceTask         string                `json:"source_task"`
	Constraints        []string              `json:"constraints"`
	Decisions          []stageDecision       `json:"decisions"`
	Unresolved         []stageDecision       `json:"unresolved"`
	RepoReferences     []string              `json:"repo_references"`
	EvidenceReferences []stageReference      `json:"evidence_references"`
}

type stageDiff struct {
	Base      string   `json:"base"`
	Candidate string   `json:"candidate"`
	Paths     []string `json:"paths,omitempty"`
}

type stageDecision struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Text   string `json:"text"`
	Answer string `json:"answer,omitempty"`
}

type stageReference struct {
	Attempt string `json:"attempt"`
	File    string `json:"file"`
}

func stageLinkFor(ctx context.Context, db *store.Store, card model.Card, dest model.Attempt) (stageLink, error) {
	if card.Kind == model.KindReview {
		events, err := db.ListEvents(ctx, "attempt:"+dest.ID)
		if err != nil {
			return stageLink{}, err
		}
		for j := len(events) - 1; j >= 0; j-- {
			if events[j].Type != model.ReviewSubjectEvent {
				continue
			}
			var pin model.PinnedReviewSubject
			if json.Unmarshal(events[j].Data, &pin) != nil || pin.Subject == "" || pin.Standalone || pin.Subject == card.ID {
				return stageLink{Mode: "independent_review"}, nil
			}
			return stageLink{Card: pin.Subject, Mode: "independent_review"}, nil
		}
		return stageLink{Mode: "independent_review"}, nil
	}
	switch card.Kind {
	case model.KindDesign, model.KindPlan, model.KindReplan, model.KindDev, model.KindSkeleton, model.KindRepair, model.KindRebase:
	default:
		return stageLink{}, nil
	}
	events, err := db.ListEvents(ctx, "card:"+card.ID)
	if err != nil {
		return stageLink{}, err
	}
	if card.ReworkCount > 0 {
		for j := len(events) - 1; j >= 0; j-- {
			if events[j].Type != "rework" {
				continue
			}
			var e struct {
				Source         string `json:"source"`
				Attempt        string `json:"attempt_id"`
				Count          int    `json:"rework_count"`
				QuestionOpened bool   `json:"question_opened"`
			}
			if json.Unmarshal(events[j].Data, &e) != nil || e.Count != card.ReworkCount || e.QuestionOpened || e.Attempt == "" {
				continue
			}
			switch e.Source {
			case "review":
				return stageLink{Attempt: e.Attempt, Mode: "review_rework"}, nil
			case "tests_failed":
				return stageLink{Card: card.ID, Attempt: e.Attempt, Mode: "test_rework"}, nil
			}
		}
		return stageLink{Mode: "rework"}, nil
	}
	for j := len(events) - 1; j >= 0; j-- {
		if events[j].Type != loop.StageParentEvent {
			continue
		}
		var link stageLink
		if json.Unmarshal(events[j].Data, &link) != nil || link.Card == "" || link.Attempt == "" {
			return stageLink{Mode: "implementation"}, nil
		}
		link.Mode = "implementation"
		return link, nil
	}
	if card.Kind == model.KindReplan && len(card.Deps) > 0 {
		return stageLink{Card: card.Deps[0], Mode: "planning"}, nil
	}
	if (card.Kind == model.KindPlan || card.Kind == model.KindReplan) && card.Node != "" {
		node, err := db.GetNode(ctx, card.Node)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && node.Objective != card.Objective) {
			return stageLink{Mode: "planning"}, nil
		}
		if err != nil {
			return stageLink{}, err
		}
		source, err := loop.StageDesignSource(ctx, db, node)
		return stageLink{Card: source, Mode: "planning"}, err
	}
	return stageLink{}, nil
}

func stageSourceAttempt(ctx context.Context, db *store.Store, link stageLink, dest model.Attempt) (model.Attempt, error) {
	if link.Attempt != "" {
		a, err := db.GetAttempt(ctx, link.Attempt)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && link.Card != "" && a.Card != link.Card) {
			return model.Attempt{}, nil
		}
		return a, err
	}
	if link.Card == "" {
		return model.Attempt{}, nil
	}
	attempts, err := db.ListAttempts(ctx, link.Card)
	if err != nil {
		return model.Attempt{}, err
	}
	var best model.Attempt
	for _, a := range attempts {
		if a.Outcome == "" || a.ID == dest.ID {
			continue
		}
		if best.ID == "" || a.Index > best.Index || (a.Index == best.Index && a.EndedAt.After(best.EndedAt)) {
			best = a
		}
	}
	return best, nil
}

func validStageSource(ctx context.Context, db *store.Store, link stageLink, target, source model.Card, attempt, dest model.Attempt, repo string) (bool, error) {
	if source.Objective != target.Objective || attempt.ID == dest.ID || !harvest.IsCommitID(dest.BaseCommit) {
		return false, nil
	}
	// Rework and cold review have one exact frozen candidate, so a new
	// branch tip cannot borrow tests from a different revision.
	switch link.Mode {
	case "independent_review":
		return attempt.Outcome == model.OutcomePassed && attempt.HeadCommit == dest.BaseCommit, nil
	case "test_rework":
		return source.ID == target.ID && attempt.Outcome == model.OutcomeTestsFailed &&
			attempt.Index < dest.Index && attempt.HeadCommit == dest.BaseCommit, nil
	case "review_rework":
		if source.Kind != model.KindReview || attempt.Outcome != model.OutcomePassed || attempt.HeadCommit != dest.BaseCommit {
			return false, nil
		}
		events, err := db.ListEvents(ctx, "attempt:"+attempt.ID)
		if err != nil {
			return false, err
		}
		for _, e := range events {
			if e.Type == model.ReviewSubjectEvent {
				var pin model.PinnedReviewSubject
				if json.Unmarshal(e.Data, &pin) == nil && !pin.Standalone && pin.Subject == target.ID {
					return true, nil
				}
			}
		}
		return false, nil
	case "implementation":
		if source.Kind != model.KindPlan && source.Kind != model.KindReplan {
			return false, nil
		}
	case "planning":
		if source.Node != target.Node || (source.Kind != model.KindDesign && source.Kind != model.KindReplan &&
			!(source.Kind == model.KindPlan && target.Kind == model.KindReplan)) {
			return false, nil
		}
	default:
		return false, nil
	}
	if attempt.Outcome != model.OutcomePassed || !harvest.IsCommitID(attempt.HeadCommit) {
		return false, nil
	}
	return stageAncestor(ctx, repo, attempt.HeadCommit, dest.BaseCommit), ctx.Err()
}

func stageAncestor(ctx context.Context, repo, old, next string) bool {
	if !harvest.IsCommitID(old) || !harvest.IsCommitID(next) {
		return false
	}
	return stageGit(ctx, repo, "merge-base", "--is-ancestor", old, next) == nil
}

// A review after rework still reviews the full candidate, not just the patch
// from the last attempt. Tests retain their latest-attempt range in Source.
func stageReviewDiff(ctx context.Context, db *store.Store, source model.Card, attempt model.Attempt, repo string) (*stageDiff, error) {
	id, err := rootCardID(ctx, db, source)
	if err != nil {
		return nil, err
	}
	history, err := db.ListAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	best := attempt
	if id != source.ID {
		best.Index = math.MaxInt
	}
	slices.SortFunc(history, func(a, b model.Attempt) int {
		if a.Index != b.Index {
			if a.Index < b.Index {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, earlier := range history {
		if earlier.Index >= best.Index || earlier.ID == attempt.ID || earlier.Branch != attempt.Branch ||
			earlier.Outcome == "" || !stageAncestor(ctx, repo, earlier.BaseCommit, attempt.HeadCommit) {
			continue
		}
		if earlier.HeadCommit != "" && (!stageAncestor(ctx, repo, earlier.HeadCommit, attempt.HeadCommit) ||
			!stageAncestor(ctx, repo, earlier.BaseCommit, earlier.HeadCommit)) {
			continue
		}
		best = earlier
		break
	}
	if !stageAncestor(ctx, repo, best.BaseCommit, attempt.HeadCommit) {
		return nil, nil
	}
	paths, err := harvest.FrozenStagePaths(ctx, repo, best.BaseCommit, attempt.HeadCommit)
	if err != nil {
		return nil, ctx.Err()
	}
	return &stageDiff{best.BaseCommit, attempt.HeadCommit, paths}, nil
}

// pinnedStageReviewRange recovers Git coordinates only, not acceptance, for
// an already pinned candidate whose versioned evidence could not be used.
// Prove that the detached checkout, its recorded SHA, and the pinned
// subject's branch are the same commit of the same local repository.
func pinnedStageReviewRange(ctx context.Context, db *store.Store, subject string, obj model.Objective, repo string, dest model.Attempt) (*stageDiff, error) {
	if subject == "" || dest.Branch == "" || dest.Worktree == "" || !harvest.IsCommitID(dest.BaseCommit) {
		return nil, nil
	}
	card, err := db.GetCard(ctx, subject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rootID, err := reviewBranchCard(ctx, db, subject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	root := card
	if rootID != card.ID {
		root, err = db.GetCard(ctx, rootID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	wantBranch := cardBranchName(root.Objective, root.ID)
	if dest.Branch != wantBranch {
		return nil, nil
	}
	sourceObj, err := db.GetObjective(ctx, root.Objective)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Do not infer a foreign repository's relationship from equal hashes.
	// An explicitly pinned cross-objective target can use a distinct
	// checkout path when both paths share the same Git object directory.
	if sourceObj.Project != obj.Project {
		p, err := db.GetProject(ctx, sourceObj.Project)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !sameStageRepo(ctx, repo, p.RepoPath) {
			return nil, ctx.Err()
		}
	}
	if (card.Objective != root.Objective) || !sameStageRepo(ctx, repo, dest.Worktree) {
		return nil, ctx.Err()
	}
	target := dest.BaseCommit
	if stageCommit(ctx, dest.Worktree, "HEAD") != target || stageCommit(ctx, repo, "refs/heads/"+wantBranch) != target {
		return nil, ctx.Err()
	}
	// Validate local execution against the exact frozen commit. An
	// attached or dirty checkout is not a clean materialization of it.
	if !cleanDetachedStageCheckout(ctx, dest.Worktree) {
		return nil, ctx.Err()
	}
	latest, err := stageSourceAttempt(ctx, db, stageLink{Card: subject}, dest)
	if err != nil {
		return nil, err
	}
	if latest.ID == "" || latest.HeadCommit != target || latest.Branch != wantBranch ||
		latest.Outcome != model.OutcomePassed || !stageAncestor(ctx, repo, latest.BaseCommit, target) {
		return nil, ctx.Err()
	}
	base, err := recordedStageReviewBase(ctx, db, root, latest, repo)
	if err != nil || base == "" {
		return nil, err
	}
	return &stageDiff{Base: base, Candidate: target}, nil
}

// A live integration ref is not history: it can advance into the candidate
// after the producing attempt, quietly dropping changes from the review. For
// rework or a separate repair, require the root card's recorded initial base
// on this exact branch. Missing or inconsistent lineage gives no range.
func recordedStageReviewBase(ctx context.Context, db *store.Store, root model.Card, producing model.Attempt, repo string) (string, error) {
	if root.ID == producing.Card && producing.Index == 0 {
		return producing.BaseCommit, nil
	}
	history, err := db.ListAttempts(ctx, root.ID)
	if err != nil {
		return "", err
	}
	var base string
	for _, initial := range history {
		if initial.Index != 0 || initial.Outcome == "" || initial.Branch != producing.Branch ||
			!stageAncestor(ctx, repo, initial.BaseCommit, producing.BaseCommit) {
			continue
		}
		if initial.HeadCommit != "" && (!stageAncestor(ctx, repo, initial.BaseCommit, initial.HeadCommit) ||
			!stageAncestor(ctx, repo, initial.HeadCommit, producing.HeadCommit)) {
			continue
		}
		if base != "" && base != initial.BaseCommit {
			return "", nil
		}
		base = initial.BaseCommit
	}
	return base, ctx.Err()
}

func cleanDetachedStageCheckout(ctx context.Context, worktree string) bool {
	var exit *exec.ExitError
	if err := stageGit(ctx, worktree, "symbolic-ref", "--quiet", "HEAD"); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return false
	}
	status, err := stageGitOutput(ctx, worktree, "--no-optional-locks", "status", "--porcelain=v1", "-z",
		"--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	return err == nil && status == ""
}

func sameStageRepo(ctx context.Context, left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	l, err := stageGitOutput(ctx, left, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false
	}
	r, err := stageGitOutput(ctx, right, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false
	}
	li, le := os.Stat(l)
	ri, re := os.Stat(r)
	return le == nil && re == nil && os.SameFile(li, ri)
}

func stageCommit(ctx context.Context, repo, ref string) string {
	id, err := stageGitOutput(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil || !harvest.IsCommitID(id) {
		return ""
	}
	return id
}

func (r *Runner) buildStageContext(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, repo string, dest model.Attempt, dir string) (*prompt.StageContext, error) {
	link, err := stageLinkFor(ctx, db, card, dest)
	if err != nil || link.Mode == "" {
		return nil, err
	}
	fallback := func(reason string) (*prompt.StageContext, error) {
		// An unstarted attempt may resume after a previous prompt write.
		// Do not leave a versioned artifact claiming that prompt used evidence.
		if err := os.Remove(filepath.Join(dir, stageHandoffName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			// A stale artifact is never loaded as prompt input; the fallback
			// event still makes it explicit that it was not used.
			reason += ":artifact_cleanup_failed"
		}
		if err := noteStageFallback(ctx, db, dest.ID, link.Mode, reason); err != nil {
			return nil, err
		}
		if link.Mode != "independent_review" {
			return nil, nil
		}
		// A cold review is an information boundary even when evidence
		// cannot be loaded or recorded. Do not reopen the legacy context.
		diff, err := pinnedStageReviewRange(ctx, db, link.Card, obj, repo, dest)
		if err != nil {
			return nil, err
		}
		status := "unavailable"
		if diff != nil {
			status = "acceptance_unavailable"
		}
		body, err := json.Marshal(struct {
			Version int        `json:"version"`
			Stage   string     `json:"stage"`
			Status  string     `json:"evidence_status"`
			Reason  string     `json:"reason"`
			Diff    *stageDiff `json:"diff,omitempty"`
		}{1, link.Mode, status, reason, diff})
		if err != nil {
			return nil, err
		}
		return &prompt.StageContext{Data: body, Review: true, ReviewUnavailable: diff == nil, ReviewAcceptanceUnavailable: diff != nil}, nil
	}
	sourceAttempt, err := stageSourceAttempt(ctx, db, link, dest)
	if err != nil {
		return nil, err
	}
	if sourceAttempt.ID == "" {
		return fallback("source_attempt_missing")
	}
	source, err := db.GetCard(ctx, sourceAttempt.Card)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback("source_card_missing")
	}
	if err != nil {
		return nil, err
	}
	// Do not open another objective's artifacts. An explicit review pin
	// may still permit a Git-only range in a shared repository.
	if source.Objective != card.Objective {
		return fallback("cross_objective_artifact_out_of_scope")
	}
	evidence, err := harvest.LoadStageEvidence(r.Home, sourceAttempt)
	if err != nil || evidence.Kind != source.Kind {
		return fallback("stage_evidence_unavailable")
	}
	sourceAttempt.HeadCommit = evidence.Candidate
	compatible, err := validStageSource(ctx, db, link, card, source, sourceAttempt, dest, repo)
	if err != nil {
		return nil, err
	}
	if !compatible {
		return fallback("source_not_current")
	}
	if link.Mode == "review_rework" && (evidence.Review == nil ||
		(evidence.Review.Verdict != "rework" && evidence.Review.Verdict != "missing")) {
		return fallback("review_opinion_unavailable")
	}
	project, err := db.GetProject(ctx, obj.Project)
	if err != nil {
		return nil, err
	}
	record := stageHandoff{
		Version: 1, Stage: link.Mode, TargetCard: card.ID, TargetBase: dest.BaseCommit,
		Source: evidence, SourceTask: source.Title,
		Constraints: slices.Clone(project.ForbiddenPaths), Decisions: []stageDecision{}, Unresolved: []stageDecision{},
		RepoReferences: []string{}, EvidenceReferences: []stageReference{},
	}
	if link.Mode == "independent_review" {
		// The artifact is verification and frozen diff metadata, not input
		// from a previous reviewer, even for a manually linked review.
		record.Source.Review = nil
		record.FrozenDiff, err = stageReviewDiff(ctx, db, source, sourceAttempt, repo)
		if err != nil {
			return nil, err
		}
		if record.FrozenDiff == nil {
			return fallback("frozen_diff_unavailable")
		}
	} else {
		record.EvidenceReferences = append(record.EvidenceReferences, stageReference{sourceAttempt.ID, harvest.StageEvidenceName})
	}
	if link.Mode == "review_rework" {
		// Preserve the entire current, task-bound review note, not an
		// older review's opinion. Large or unparsed notes use the old flow.
		record.ReworkNote = strings.TrimSpace(card.ReworkNote)
		if len(record.ReworkNote) > 2<<10 {
			return fallback("review_opinion_exceeds_prompt_budget")
		}
	}
	evidenceDir := roots.AttemptDir(r.Home, sourceAttempt.ID, sourceAttempt.PromptPath)
	if len(evidence.Checks) > 0 && stageFilePresent(evidenceDir, "acceptance.log") {
		record.EvidenceReferences = append(record.EvidenceReferences, stageReference{sourceAttempt.ID, "acceptance.log"})
	}
	if record.Source.Review != nil && stageFilePresent(evidenceDir, "result.md") {
		record.EvidenceReferences = append(record.EvidenceReferences, stageReference{sourceAttempt.ID, "result.md"})
	}
	if link.Mode != "independent_review" {
		if err := record.collectScope(ctx, db, card, source, obj, project, repo); err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(data) > stagePromptLimit {
		return fallback("stage_evidence_exceeds_prompt_budget")
	}
	if err := harvest.WriteStageJSON(dir, stageHandoffName, append(slices.Clone(data), '\n')); err != nil {
		return fallback("stage_handoff_write_failed")
	}
	return &prompt.StageContext{Data: data, Review: link.Mode == "independent_review", Rework: link.Mode == "review_rework"}, nil
}

func stageFilePresent(dir, name string) bool {
	i, err := os.Lstat(filepath.Join(dir, name))
	return err == nil && i.Mode().IsRegular()
}

func (h *stageHandoff) collectScope(ctx context.Context, db *store.Store, card, source model.Card, obj model.Objective, project model.Project, repo string) error {
	// References are repo-relative and pinned to the target base, not paths
	// to private runtime directories or links embedded in author output.
	designDir := strings.TrimSpace(project.DesignDir)
	if designDir == "" {
		designDir = "docs/design"
	}
	h.addRepoRef(ctx, repo, path.Join(designDir, obj.ID, "context.md"))
	h.addRepoRef(ctx, repo, path.Join("docs/design", obj.ID, "context.md"))
	if card.Node != "" {
		node, err := db.GetNode(ctx, card.Node)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && node.Objective == obj.ID {
			h.addRepoRef(ctx, repo, node.DocPath)
			for _, q := range node.OpenQuestions {
				decision := stageDecision{ID: q.ID, Status: string(q.Status), Text: q.Text, Answer: q.Answer}
				if q.Status == model.OQAnswered {
					h.Decisions = append(h.Decisions, decision)
				} else {
					h.Unresolved = append(h.Unresolved, decision)
				}
			}
		}
	}
	questions, err := db.ListQuestions(ctx, obj.ID)
	if err != nil {
		return err
	}
	for _, q := range questions {
		if (q.ScopeKind != "card" || (q.Scope != card.ID && q.Scope != source.ID)) &&
			(q.ScopeKind != "node" || card.Node == "" || q.Scope != card.Node) {
			continue
		}
		decision := stageDecision{ID: q.ID, Status: string(q.Kind), Text: q.Text, Answer: q.Answer}
		if strings.TrimSpace(q.Answer) == "" {
			h.Unresolved = append(h.Unresolved, decision)
		} else {
			h.Decisions = append(h.Decisions, decision)
		}
	}
	return nil
}

func (h *stageHandoff) addRepoRef(ctx context.Context, repo, file string) {
	file = path.Clean(strings.TrimSpace(file))
	if file == "." || file == ".." || strings.HasPrefix(file, "../") || path.IsAbs(file) ||
		strings.ContainsAny(file, ":\\\x00\r\n") {
		return
	}
	ref := h.TargetBase + ":" + file
	if !slices.Contains(h.RepoReferences, ref) &&
		stageGit(ctx, repo, "cat-file", "-e", ref) == nil {
		h.RepoReferences = append(h.RepoReferences, ref)
	}
}

func stageGit(ctx context.Context, repo string, args ...string) error {
	return stageGitCommand(ctx, repo, args...).Run()
}

func stageGitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	out, err := stageGitCommand(ctx, repo, args...).Output()
	return strings.TrimSpace(string(out)), err
}

func stageGitCommand(ctx context.Context, repo string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(proc.AcceptanceEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

func noteStageFallback(ctx context.Context, db *store.Store, attempt, mode, reason string) error {
	entity := "attempt:" + attempt
	existing, err := db.ListEvents(ctx, entity)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.Type == stageHandoffFallback {
			return nil
		}
	}
	body, err := json.Marshal(map[string]string{"stage": mode, "reason": reason})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{Entity: entity, Type: stageHandoffFallback, Data: body})
}
