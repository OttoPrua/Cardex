package harvest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/roots"
)

const StageEvidenceName = "stage-evidence.v1.json"
const stageEvidenceLimit = 256 << 10

// StageEvidence is a handoff-safe projection of one completed harvest.
// It has no transcripts, prompt content, output excerpts, or worktree paths.
type StageEvidence struct {
	Version   int            `json:"version"`
	Attempt   string         `json:"attempt"`
	Card      string         `json:"card"`
	Kind      model.CardKind `json:"kind"`
	Outcome   model.Outcome  `json:"outcome"`
	Base      string         `json:"base"`
	Candidate string         `json:"candidate"`
	Paths     []string       `json:"paths"`
	Checks    []StageCheck   `json:"checks"`
	Review    *StageReview   `json:"review,omitempty"`
}

type StageCheck struct {
	Command string `json:"command"`
	Exit    int    `json:"exit"`
}

type StageReview struct {
	Verdict  string         `json:"verdict"`
	Findings []StageFinding `json:"findings,omitempty"`
	Tests    []string       `json:"tests,omitempty"`
}

type StageFinding struct {
	Priority string `json:"priority"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Text     string `json:"text"`
}

func stageEvidenceKind(kind model.CardKind) bool {
	switch kind {
	case model.KindDesign, model.KindPlan, model.KindReplan, model.KindDev, model.KindRepair,
		model.KindRebase, model.KindSkeleton, model.KindReview:
		return true
	default:
		return false
	}
}

// IsCommitID allows complete lower-case SHA-1/SHA-256 IDs only.
func IsCommitID(id string) bool {
	return (len(id) == 40 || len(id) == 64) && strings.Trim(id, "0123456789abcdef") == ""
}

// stageCandidate freezes a clean checkout before and after acceptance. Host
// intentionally leaves HeadCommit empty when it had no changes to commit;
// reviewers usually write only the ignored result file. In that case the
// harvester may witness the clean HEAD itself.
func stageCandidate(ctx context.Context, in Input, dir string) string {
	if in.Home == "" || !safeAttemptID(in.Attempt.ID) || in.Card.ID != in.Attempt.Card ||
		!stageEvidenceKind(in.Card.Kind) || !IsCommitID(in.Attempt.BaseCommit) {
		return ""
	}
	head, err := gitOut(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err != nil || !IsCommitID(head) || (in.Attempt.HeadCommit != "" && head != in.Attempt.HeadCommit) ||
		(in.Card.Kind == model.KindReview && head != in.Attempt.BaseCommit) {
		return ""
	}
	status, err := gitExec(ctx, dir, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return ""
	}
	// Historical attempts may not exclude the untracked result document yet.
	// A tracked modification (including to that file) is not an exception.
	for _, row := range strings.Split(string(status), "\x00") {
		if row != "" && row != "?? .cardex-result.md" {
			return ""
		}
	}
	return head
}

// FrozenStagePaths computes the changed paths of a commit range, excluding
// working-copy state, external diff tools, and text conversion.
func FrozenStagePaths(ctx context.Context, dir, base, candidate string) ([]string, error) {
	if !IsCommitID(base) || !IsCommitID(candidate) {
		return nil, errors.New("invalid frozen diff range")
	}
	files, err := gitExec(ctx, dir, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", base, candidate, "--")
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(strings.Split(string(files), "\x00"), func(s string) bool { return s == "" }), nil
}

func saveStageEvidence(ctx context.Context, in Input, dir, frozenCandidate string, outcome model.Outcome, runs []commandRun, sum Summary) error {
	if in.Card.UsesStrictReviewObjects(in.Attempt) {
		ctx = proc.WithExactGitObjects(ctx)
	}
	switch outcome {
	case model.OutcomePassed, model.OutcomeTestsFailed:
	default:
		return nil
	}
	if frozenCandidate == "" || stageCandidate(ctx, in, dir) != frozenCandidate {
		return nil
	}
	paths, err := FrozenStagePaths(ctx, dir, in.Attempt.BaseCommit, frozenCandidate)
	if err != nil {
		// A missing source commit is unavailable evidence, not a harvest failure.
		return nil
	}
	record := StageEvidence{
		Version: 1, Attempt: in.Attempt.ID, Card: in.Card.ID, Kind: in.Card.Kind, Outcome: outcome,
		Base: in.Attempt.BaseCommit, Candidate: frozenCandidate, Paths: paths, Checks: []StageCheck{},
	}
	for _, run := range runs {
		record.Checks = append(record.Checks, StageCheck{Command: run.Command, Exit: run.Exit})
	}
	if sum.Review != nil && sum.Review.Verdict != "" {
		record.Review = &StageReview{Verdict: sum.Review.Verdict, Tests: sum.Review.Tests}
		for i, group := range [][]ReviewIssue{sum.Review.P0, sum.Review.P1, sum.Review.P2} {
			for _, issue := range group {
				record.Review.Findings = append(record.Review.Findings, StageFinding{fmt.Sprintf("P%d", i), issue.File, issue.Line, issue.Text})
			}
		}
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(body) > stageEvidenceLimit {
		return nil
	}
	outDir := roots.AttemptDir(in.Home, in.Attempt.ID, in.Attempt.PromptPath)
	return WriteStageJSON(outDir, StageEvidenceName, append(body, '\n'))
}

// WriteStageJSON publishes a complete artifact, or leaves no partial artifact.
func WriteStageJSON(dir, name string, body []byte) (err error) {
	file, err := os.CreateTemp(dir, ".stage-handoff-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, name))
}

// invalidateStageEvidence removes a previous projection before this same
// attempt rewrites acceptance or result files. If removal is denied, do not
// overwrite its evidence generation with unrelated output.
func invalidateStageEvidence(in Input) error {
	if in.Home == "" || !safeAttemptID(in.Attempt.ID) {
		return nil
	}
	outDir := roots.AttemptDir(in.Home, in.Attempt.ID, in.Attempt.PromptPath)
	err := os.Remove(filepath.Join(outDir, StageEvidenceName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("harvest: invalidate stage evidence: %w", err)
	}
	return nil
}

func LoadStageEvidence(home string, attempt model.Attempt) (StageEvidence, error) {
	if !safeAttemptID(attempt.ID) {
		return StageEvidence{}, fmt.Errorf("invalid stage attempt id")
	}
	filePath := filepath.Join(roots.AttemptDir(home, attempt.ID, attempt.PromptPath), StageEvidenceName)
	st, err := os.Lstat(filePath)
	if err != nil {
		return StageEvidence{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > stageEvidenceLimit+1 {
		return StageEvidence{}, fmt.Errorf("invalid stage evidence file")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return StageEvidence{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, stageEvidenceLimit+2))
	if err != nil {
		return StageEvidence{}, err
	}
	if len(data) > stageEvidenceLimit+1 {
		return StageEvidence{}, fmt.Errorf("stage evidence is too large")
	}
	var record StageEvidence
	if err := json.Unmarshal(data, &record); err != nil {
		return StageEvidence{}, errors.New("cannot decode stage evidence")
	}
	if record.Version != 1 || record.Attempt != attempt.ID || record.Card != attempt.Card ||
		record.Outcome != attempt.Outcome || record.Base != attempt.BaseCommit || (attempt.HeadCommit != "" && record.Candidate != attempt.HeadCommit) ||
		!IsCommitID(record.Base) || !IsCommitID(record.Candidate) || !stageEvidenceKind(record.Kind) {
		return StageEvidence{}, fmt.Errorf("stage evidence is not bound to this attempt")
	}
	return record, nil
}
