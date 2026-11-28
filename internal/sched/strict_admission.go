package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

var errStrictAdmissionChanged = errors.New("strict launch snapshot changed")

// observedStrictCard verifies the complete definition (including claims,
// prerequisites and stored status) while the caller holds the SQLite writer.
func observedStrictCard(ctx context.Context, db *store.Store, before model.Card) (model.Card, error) {
	current, err := db.GetCard(ctx, before.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return current, errStrictAdmissionChanged
	}
	if err != nil {
		return current, err
	}
	if !current.StrictDeps || !reflect.DeepEqual(current, before) {
		return current, errStrictAdmissionChanged
	}
	return current, nil
}

// writeObservedCard preserves the normal legacy write path. A strict
// scheduler transition instead compares and writes under one writer lock.
func writeObservedCard(ctx context.Context, db *store.Store, before model.Card, after *model.Card) (bool, error) {
	return withObservedCard(ctx, db, before, func(ctx context.Context) error {
		return db.UpdateCard(ctx, after)
	})
}

func withObservedCard(ctx context.Context, db *store.Store, before model.Card, write func(context.Context) error) (bool, error) {
	if !before.StrictDeps {
		return true, write(ctx)
	}
	err := db.WithWriteTx(ctx, func(ctx context.Context) error {
		if _, err := observedStrictCard(ctx, db, before); err != nil {
			return err
		}
		return write(ctx)
	})
	if errors.Is(err, errStrictAdmissionChanged) {
		return false, nil
	}
	return err == nil, err
}

type strictLaunchView struct {
	card    model.Card
	obj     model.Objective
	project model.Project
}

func (v strictLaunchView) verify(ctx context.Context, db *store.Store) (model.Card, []model.Attempt, error) {
	current, err := observedStrictCard(ctx, db, v.card)
	if err != nil {
		return current, nil, err
	}
	if !canDispatch(current) {
		return current, nil, errStrictAdmissionChanged
	}
	obj, err := db.GetObjective(ctx, current.Objective)
	if errors.Is(err, sql.ErrNoRows) {
		return current, nil, errStrictAdmissionChanged
	}
	if err != nil {
		return current, nil, err
	}
	if (obj.Status != model.ObjectiveActive && obj.Status != model.ObjectiveDoneCandidate) || !reflect.DeepEqual(obj, v.obj) {
		return current, nil, errStrictAdmissionChanged
	}
	project, err := db.GetProject(ctx, obj.Project)
	if errors.Is(err, sql.ErrNoRows) {
		return current, nil, errStrictAdmissionChanged
	}
	if err != nil {
		return current, nil, err
	}
	if !reflect.DeepEqual(project, v.project) {
		return current, nil, errStrictAdmissionChanged
	}
	missing, err := missingReviewSubject(ctx, db, current)
	if err != nil {
		return current, nil, err
	}
	if missing != "" {
		return current, nil, errStrictAdmissionChanged
	}
	reason, err := db.ExecutionDependencyBlocker(ctx, current)
	if err != nil {
		return current, nil, err
	}
	if reason != "" {
		return current, nil, errStrictAdmissionChanged
	}
	attempts, err := db.ListAttempts(ctx, current.ID)
	return current, attempts, err
}

// prepareStrictAttempt installs the attempt, review pin, route release and
// Running state atomically after checking the exact inputs to Git preparation.
func (r *Runner) prepareStrictAttempt(ctx context.Context, db *store.Store, v strictLaunchView, history []model.Attempt, card *model.Card, attempt *model.Attempt, pin model.PinnedReviewSubject, rel pinRelease, rt model.Route) error {
	return db.WithWriteTx(ctx, func(ctx context.Context) error {
		current, prior, err := v.verify(ctx, db)
		if err != nil {
			return err
		}
		if current.Status == model.CardRunning || !reflect.DeepEqual(prior, history) {
			return errStrictAdmissionChanged
		}
		for _, old := range prior {
			if attemptOpen(old) {
				return errStrictAdmissionChanged
			}
		}
		if err := db.CreateAttempt(ctx, attempt); err != nil {
			return err
		}
		if err := r.pinReviewSubject(ctx, db, current, attempt.ID, pin); err != nil {
			return err
		}
		current.Status, current.WaitReason = model.CardRunning, ""
		if rel.released {
			current.Channel, current.Model = rt.Channel, rt.Model
		}
		if err := db.UpdateCard(ctx, &current); err != nil {
			return err
		}
		if rel.released || rel.failoverOrigin != "" {
			if err := appendPinRelease(ctx, db, r.now(), current.ID, rel, rt); err != nil {
				return err
			}
		}
		*card = current
		return nil
	})
}

// strictPreparedAttempt requires the exact most recent, still-unclaimed
// preparation. In particular, an older unstarted row is not eligible to
// resume after a second scheduler has added or claimed a newer attempt.
func strictPreparedAttempt(prior []model.Attempt, expected model.Attempt) (model.Attempt, error) {
	var current model.Attempt
	for _, item := range prior {
		if item.ID == expected.ID {
			current = item
		} else if item.Index >= expected.Index || attemptOpen(item) {
			return current, errStrictAdmissionChanged
		}
	}
	if current.ID == "" || current.PID != 0 || !attemptOpen(current) || !reflect.DeepEqual(current, expected) {
		return current, errStrictAdmissionChanged
	}
	return current, nil
}

// resumeStrictAttempt atomically replaces only the dispatch status of the
// still-authoritative prepared attempt. The ordinary resume policy is unchanged.
func resumeStrictAttempt(ctx context.Context, db *store.Store, v strictLaunchView, card *model.Card, expected model.Attempt) error {
	return db.WithWriteTx(ctx, func(ctx context.Context) error {
		current, prior, err := v.verify(ctx, db)
		if err != nil {
			return err
		}
		if _, err := strictPreparedAttempt(prior, expected); err != nil {
			return err
		}
		if current.Status != model.CardRunning {
			current.Status, current.WaitReason = model.CardRunning, ""
			if err := db.UpdateCard(ctx, &current); err != nil {
				return err
			}
		}
		*card = current
		return nil
	})
}

// confirmStrictHostStart rechecks current state after prompt generation, before
// invoking the host. No DB lock is held across prompt generation or OS start;
// the host independently rechecks at its own admission boundary.
func confirmStrictHostStart(ctx context.Context, db *store.Store, v strictLaunchView, expected model.Attempt, attempted model.Attempt) error {
	return db.WithWriteTx(ctx, func(ctx context.Context) error {
		currentCard, prior, err := v.verify(ctx, db)
		if err != nil {
			return err
		}
		if currentCard.Status != model.CardRunning {
			return errStrictAdmissionChanged
		}
		current, err := strictPreparedAttempt(prior, expected)
		if err != nil {
			return err
		}
		if current.PromptPath != attempted.PromptPath {
			current.PromptPath = attempted.PromptPath
			return db.UpdateAttempt(ctx, &current)
		}
		return nil
	})
}

// strictGitPreparation runs after taking the durable repo slot, but before
// any Git mutation. Even a different card may own the shared subject branch.
func strictGitPreparation(ctx context.Context, db *store.Store, v strictLaunchView, history []model.Attempt, branch string) ([]string, error) {
	var closed []string
	err := db.WithWriteTx(ctx, func(ctx context.Context) error {
		current, prior, err := v.verify(ctx, db)
		if err != nil {
			return err
		}
		if current.Status == model.CardRunning || !reflect.DeepEqual(prior, history) {
			return errStrictAdmissionChanged
		}
		for _, old := range prior {
			if attemptOpen(old) {
				return errStrictAdmissionChanged
			}
		}
		if branch == "" {
			return nil
		}
		all, err := db.ListAttempts(ctx, "")
		if err != nil {
			return err
		}
		for _, other := range all {
			if strings.TrimPrefix(other.Branch, "refs/heads/") != branch {
				continue
			}
			if attemptOpen(other) {
				return errStrictAdmissionChanged
			}
			if other.Outcome == "" || other.EndedAt.IsZero() || other.Exit == nil || other.Worktree == "" {
				continue
			}
			// A terminal row may also be created by reaping a crashed host.
			// Require the exact receipt that finish writes after Git returned
			// successfully; a missing receipt is not proof Git was drained.
			events, err := db.ListEvents(ctx, "attempt:"+other.ID)
			if err != nil {
				return err
			}
			if strictWorkspaceCompletion(other, events) {
				closed = append(closed, other.Worktree)
			}
		}
		return nil
	})
	return closed, err
}

// strictWorkspaceCompletion distinguishes Git's explicit clean completion
// (head:"") from absent, null, malformed, conflicting and error receipts.
func strictWorkspaceCompletion(attempt model.Attempt, events []model.Event) bool {
	found := false
	for _, e := range events {
		if e.Type != "workspace_commit" || e.TS.Before(attempt.EndedAt) {
			continue
		}
		if found { // Ambiguous completion is not evidence of drained Git.
			return false
		}
		var receipt struct {
			Head  *string         `json:"head"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(e.Data, &receipt) != nil || receipt.Head == nil ||
			len(receipt.Error) != 0 || *receipt.Head != attempt.HeadCommit {
			return false
		}
		found = true
	}
	return found
}
