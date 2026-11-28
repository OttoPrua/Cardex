package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

const EventDependencyDeferred = model.EventHostDependencyDeferred

type strictLaunchStage uint8

const (
	strictProbe strictLaunchStage = iota
	strictClaim
	strictExec
)

// strictLaunchGate reads the actual card and prerequisites at each launch
// boundary. A prepared attempt can be reused, but a running or closed one
// must never be relaunched by invoking 'host' directly.
func strictLaunchGate(ctx context.Context, db *store.Store, attempt *model.Attempt, stage strictLaunchStage) (bool, error) {
	return strictLaunchGateWithReviewScope(ctx, db, attempt, stage, nil)
}

type strictReviewScope struct {
	card, objective, project, repo, attempt, worktree, base string
	verified                                                bool
}

// Read and verify Git outside SQLite's writer. The following admission
// compares exactly which checkout, project and object identity was proved.
func proveStrictHostReviewScope(ctx context.Context, db *store.Store, card model.Card, a model.Attempt) (*strictReviewScope, error) {
	if !card.UsesStrictReviewObjects(a) {
		return nil, nil
	}
	proof := &strictReviewScope{card: card.ID, objective: card.Objective, attempt: a.ID, worktree: a.Worktree, base: a.BaseCommit}
	obj, err := db.GetObjective(ctx, card.Objective)
	if errors.Is(err, sql.ErrNoRows) {
		return proof, nil
	}
	if err != nil {
		return nil, err
	}
	proof.project = obj.Project
	p, err := db.GetProject(ctx, obj.Project)
	if errors.Is(err, sql.ErrNoRows) {
		return proof, nil
	}
	if err != nil {
		return nil, err
	}
	proof.repo = p.RepoPath
	proof.verified = workspace.VerifyReviewObjectScope(ctx, proof.repo, proof.worktree, proof.base)
	return proof, ctx.Err()
}

func (p *strictReviewScope) matches(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, a model.Attempt) (bool, error) {
	if p == nil || !p.verified || p.card != card.ID || p.objective != obj.ID || p.project != obj.Project ||
		p.attempt != a.ID || p.worktree != a.Worktree || p.base != a.BaseCommit {
		return false, nil
	}
	project, err := db.GetProject(ctx, obj.Project)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && p.repo == project.RepoPath, err
}

func strictLaunchGateWithReviewScope(ctx context.Context, db *store.Store, attempt *model.Attempt, stage strictLaunchStage, reviewScope *strictReviewScope) (bool, error) {
	allowed := false
	err := db.WithWriteTx(ctx, func(ctx context.Context) error {
		a, err := db.GetAttempt(ctx, attempt.ID)
		if err != nil {
			return err
		}
		if a.Exit != nil || !a.EndedAt.IsZero() || a.Outcome != "" {
			return errors.New("host: strict dependency attempt is already closed")
		}
		if stage == strictExec {
			if a.PID != os.Getpid() {
				return errors.New("host: strict dependency attempt is not owned by this host")
			}
		} else if a.PID != 0 {
			return errors.New("host: strict dependency attempt already has a host")
		}
		card, err := db.GetCard(ctx, a.Card)
		if err != nil {
			return err
		}
		var reason string
		attempts, err := db.ListAttempts(ctx, card.ID)
		if err != nil {
			return err
		}
		for _, other := range attempts {
			if other.ID == a.ID {
				continue
			}
			active := other.PID != 0 && other.Outcome == "" && other.Exit == nil && other.EndedAt.IsZero()
			if other.Index >= a.Index || active {
				reason = "strict preparation is no longer the current attempt"
				break
			}
		}
		if reason == "" && card.Status != model.CardReady && card.Status != model.CardRunning {
			reason = fmt.Sprintf("card %s is %s", card.ID, card.Status)
		}
		if reason == "" && reviewScope != nil && !card.UsesStrictReviewObjects(a) {
			reason = "strict review Git object scope is unverified"
		}
		if reason == "" && card.StrictDeps {
			obj, err := db.GetObjective(ctx, card.Objective)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				reason = "objective " + card.Objective + " does not exist"
			case err != nil:
				return err
			case obj.Status != model.ObjectiveActive && obj.Status != model.ObjectiveDoneCandidate:
				reason = fmt.Sprintf("objective %s is %s", obj.ID, obj.Status)
			case stage == strictExec && (reviewScope != nil || card.UsesStrictReviewObjects(a)):
				matched, err := reviewScope.matches(ctx, db, card, obj, a)
				if err != nil {
					return err
				}
				if !matched || !card.UsesStrictReviewObjects(a) {
					reason = "strict review Git object scope is unverified"
				}
			}
		}
		if reason == "" && card.StrictDeps {
			if card.Kind == model.KindReview {
				if subject, explicit := model.ReviewSubject(card); explicit {
					if _, err := db.GetCard(ctx, subject); errors.Is(err, sql.ErrNoRows) {
						reason = "review subject " + subject + " does not exist"
					} else if err != nil {
						return err
					}
				}
			}
			if reason == "" {
				reason, err = db.ExecutionDependencyBlocker(ctx, card)
				if err != nil {
					return err
				}
			}
		}
		if reason != "" {
			if stage == strictExec {
				a.PID = 0
				a.StartedAt = time.Time{}
				if err := db.UpdateAttempt(ctx, &a); err != nil {
					return err
				}
			}
			if card.Status == model.CardRunning {
				// Dispatch set running while preparing this attempt. Only
				// undo that provisional state if no other host owns work.
				newerOrOwned := false
				for _, other := range attempts {
					if other.ID == a.ID {
						continue
					}
					active := other.PID != 0 && other.Outcome == "" && other.Exit == nil && other.EndedAt.IsZero()
					if other.Index >= a.Index || active {
						newerOrOwned = true
						break
					}
				}
				if !newerOrOwned {
					card.Status, card.WaitReason = model.CardReady, ""
					if err := db.UpdateCard(ctx, &card); err != nil {
						return err
					}
				}
			}
			deferral := model.HostDependencyDeferral{Card: card.ID, Reason: reason}
			if stage == strictProbe && card.StrictDeps {
				deferral.Attempt, deferral.Phase = a.ID, model.HostDependencyPreclaimProbe
			}
			raw, err := json.Marshal(deferral)
			if err != nil {
				return err
			}
			return db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: EventDependencyDeferred, Data: raw})
		}
		if stage == strictClaim {
			a.PID = os.Getpid()
			// The previous timestamp may be from a much earlier preparation.
			// Reconciliation measures the real host's deadline from this claim.
			a.StartedAt = time.Now().UTC()
			if err := db.UpdateAttempt(ctx, &a); err != nil {
				return err
			}
			*attempt = a
		}
		allowed = true
		return nil
	})
	return allowed, err
}

// failStrictNoExecutor writes the terminal row and exact proof together. If
// the owned claim changed, a stale host must not free a newer checkout.
func failStrictNoExecutor(ctx context.Context, db *store.Store, prior *model.Attempt, cause error) error {
	pctx, cancel := persistContext(ctx)
	defer cancel()
	if err := db.WithWriteTx(pctx, func(ctx context.Context) error {
		current, err := db.GetAttempt(ctx, prior.ID)
		if err != nil {
			return err
		}
		card, err := db.GetCard(ctx, current.Card)
		if err != nil {
			return err
		}
		if !card.StrictDeps || current.PID != os.Getpid() || current.StrictCheckoutID() != prior.StrictCheckoutID() ||
			current.Exit != nil || !current.EndedAt.IsZero() || current.Outcome != "" {
			return errors.New("host: strict no-executor receipt does not own this preparation")
		}
		code := 127
		current.Exit, current.EndedAt = &code, time.Now().UTC()
		current.ErrorExcerpt = trimExcerpt(cause.Error())
		if err := db.UpdateAttempt(ctx, &current); err != nil {
			return err
		}
		proof, err := json.Marshal(map[string]string{"checkout_id": current.StrictCheckoutID()})
		if err != nil {
			return err
		}
		if err := db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + current.ID, Type: model.StrictNoExecutorEvent, Data: proof}); err != nil {
			return err
		}
		appendEvent(ctx, db, current.ID, "host_finish", map[string]any{"exit": code, "error": current.ErrorExcerpt, "started": false})
		return nil
	}); err != nil {
		return err
	}
	return cause
}
