package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

const (
	launchStepWorktree = "worktree"
	launchStepPrompt   = "prompt"
	launchStepHost     = "host"
	summaryLimit       = 2048
	tickErrorWindow    = 10 * time.Minute
)

// launchScratch is the worktree and card branch this launch created.
// Drop flags are false when this call did not create that object.
type launchScratch struct {
	repo, worktree, branch, base string
	dropWorktree, dropBranch     bool
	strict                       *workspace.CreateRequest
	checkout                     *workspace.LaunchCheckout
}

// tickNoted marks an error already stored as tick_error by the step that saw it.
type tickNoted struct{ err error }

func (e tickNoted) Error() string { return e.err.Error() }
func (e tickNoted) Unwrap() error { return e.err }

func clipSummary(s string) string {
	if len(s) <= summaryLimit {
		return s
	}
	s = s[:summaryLimit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// failLaunch cleans this launch, ends the attempt when one exists, and puts
// the card on the existing backoff wait. It does not switch channel.
// The attempt outcome, the disposition marker, and the card backoff commit
// together. A handled launch failure returns ("", nil) so the tick can continue.
func (r *Runner) failLaunch(ctx context.Context, db *store.Store, card *model.Card, attempt *model.Attempt, step string, cause error, rt model.Route, scratch launchScratch) (string, error) {
	if card == nil {
		return "", fmt.Errorf("sched: launch card required")
	}
	if cause == nil {
		cause = fmt.Errorf("sched: launch failed")
	}
	// A generic launch error cannot attest that no comparison executor
	// started. Only the dedicated pre-exec comparison path can write the
	// exact no-wire receipt. In particular, dual-marked cards must not
	// enter D1's retry/backoff or Git cleanup path.
	if card.StrictComparison() || (attempt != nil && attempt.StrictComparison()) {
		return "", ctx.Err()
	}
	if card.StrictDeps {
		return r.failStrictLaunch(ctx, db, card, attempt, step, cause, rt, scratch)
	}
	summary := clipSummary(cause.Error())
	var fresh model.Card
	var failure model.Attempt
	var changed, strict bool
	prior := 0
	err := db.WithWriteTx(ctx, func(tx context.Context) error {
		var err error
		fresh, err = db.GetCard(tx, card.ID)
		if err != nil || !reflect.DeepEqual(fresh, *card) || !canDispatch(fresh) {
			return err
		}
		rows, err := db.ListAttempts(tx, card.ID)
		if err != nil {
			return err
		}
		if attempt != nil {
			failure, err = strictPreparedAttempt(rows, *attempt)
			if err != nil {
				// A refusal, newer attempt, or live owner beat this failure.
				return nil
			}
			exit := 1
			failure.Outcome, failure.ErrorClass, failure.ErrorExcerpt = model.OutcomeLaunchFailed, model.ErrorLaunchFailed, summary
			failure.EndedAt, failure.Exit = r.now().UTC(), &exit
		} else {
			for _, a := range rows {
				if attemptOpen(a) {
					return nil
				}
			}
		}
		prior, err = r.launchFailureStreak(tx, db, card.ID)
		if err != nil {
			return err
		}
		if attempt != nil {
			if err := db.UpdateAttempt(tx, &failure); err != nil {
				return err
			}
			if err := markLaunchDisposed(tx, db, r.now(), &failure); err != nil {
				return err
			}
			if rt.Channel == "" {
				rt = failure.Route
			}
		}
		strict = fresh.StrictComparison() || (attempt != nil && failure.StrictComparison())
		if strict {
			fresh.Status = model.CardNeedsInput
			fresh.WaitReason = "strict comparison could not start; no automatic retry"
		} else {
			r.applyLaunchBackoff(&fresh, prior)
		}
		if err := db.UpdateCard(tx, &fresh); err != nil {
			return err
		}
		if err := r.writeLaunchFailed(tx, db, card.ID, failure.ID, step, summary, rt); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil || !changed {
		return "", err
	}
	*card = fresh
	if attempt != nil {
		*attempt = failure
	}
	reportServeError(ctx, "launch", cause)
	reportServeEvent(ctx, "transition state=launch_backoff")
	// A successful failure transition is not a lease: zero backoff immediately
	// permits another launch. Reacquire exact failed ownership before cleanup.
	if attempt != nil && scratch.checkout != nil {
		r.dropFailedCheckout(ctx, db, fresh, failure, scratch)
	}
	if attempt != nil {
		if err := harvest.WriteUnavailableSummary(ctx, r.harvestInput(ctx, db, *card, *attempt, 1, nil)); err != nil {
			_ = noteEvidenceWriteFailed(ctx, db, r.Home, *attempt, err)
		}
	}
	if !strict && prior+1 >= 3 {
		if qerr := db.WithWriteTx(ctx, func(tx context.Context) error {
			current, err := db.GetCard(tx, fresh.ID)
			if err != nil || !reflect.DeepEqual(current, fresh) {
				return err
			}
			return r.openLaunchQuestion(tx, db, card, step, summary)
		}); qerr != nil {
			fmt.Fprintf(os.Stderr, "launch_failed question card=%s error=%v\n", card.ID, qerr)
		}
	}
	return "", nil
}

func (r *Runner) dropFailedCheckout(ctx context.Context, db *store.Store, card model.Card, failed model.Attempt, scratch launchScratch) {
	if !scratch.dropWorktree || scratch.checkout == nil || failed.Worktree != scratch.worktree || failed.Outcome != model.OutcomeLaunchFailed {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = db.WithWriteTx(bounded, func(tx context.Context) error {
		current, err := db.GetCard(tx, card.ID)
		if err != nil || !reflect.DeepEqual(current, card) {
			return err
		}
		latest, err := db.GetAttempt(tx, failed.ID)
		if err != nil || !reflect.DeepEqual(latest, failed) {
			return err
		}
		rows, err := db.ListAttempts(tx, "")
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID != failed.ID && ((row.Card == failed.Card && (row.Index >= failed.Index || attemptOpen(row))) ||
				(row.Worktree != "" && filepath.Clean(row.Worktree) == filepath.Clean(failed.Worktree))) {
				return nil
			}
		}
		(workspace.Git{}).DropOwnedLaunchCheckout(tx, scratch.repo, scratch.worktree, scratch.checkout)
		return nil
	})
}

func (r *Runner) applyLaunchBackoff(card *model.Card, prior int) {
	cfg := loop.LoadConfig(r.Home)
	wait := cfg.RetryBackoff
	if prior >= 1 {
		wait *= 2
	}
	if wait <= 0 {
		card.Status = model.CardReady
		card.WaitReason = ""
		return
	}
	until := r.now().UTC().Add(wait)
	card.Status = model.CardWaiting
	card.WaitReason = "backoff:" + until.Format(time.RFC3339Nano)
}

// markLaunchDisposed records the marker generic disposition already skips.
// The attempt entity is attempt:<id> and the type is dev_disposed.
func markLaunchDisposed(ctx context.Context, db *store.Store, now time.Time, attempt *model.Attempt) error {
	data, err := json.Marshal(map[string]string{
		"outcome": string(attempt.Outcome),
		"card":    attempt.Card,
	})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		TS:     now.UTC(),
		Entity: "attempt:" + attempt.ID,
		Type:   "dev_disposed",
		Data:   data,
	})
}

func (r *Runner) writeLaunchFailed(ctx context.Context, db *store.Store, cardID, attemptID, step, summary string, rt model.Route) error {
	payload := map[string]string{
		"card":  cardID,
		"step":  step,
		"error": summary,
	}
	if attemptID != "" {
		payload["attempt"] = attemptID
	}
	if rt.Channel != "" {
		payload["channel"] = rt.Channel
	}
	if rt.Model != "" {
		payload["model"] = rt.Model
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		TS:     r.now().UTC(),
		Entity: "card:" + cardID,
		Type:   model.EventLaunchFailed,
		Data:   data,
	})
}

func (r *Runner) launchFailureStreak(ctx context.Context, db *store.Store, cardID string) (int, error) {
	attempts, err := db.ListAttempts(ctx, cardID)
	if err != nil {
		return 0, err
	}
	var success time.Time
	failed := map[string]model.Attempt{}
	for _, attempt := range attempts {
		if attempt.Outcome == model.OutcomeLaunchFailed {
			failed[attempt.ID] = attempt
			continue
		}
		if attempt.PID > 0 || attempt.Outcome != "" {
			if success.IsZero() || attempt.StartedAt.After(success) {
				success = attempt.StartedAt
			}
		}
	}
	events, err := db.ListEvents(ctx, "card:"+cardID)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	n := 0
	for _, ev := range events {
		if ev.Type != model.EventLaunchFailed {
			continue
		}
		if !success.IsZero() && !ev.TS.After(success) {
			continue
		}
		n++
		var payload struct {
			Attempt string `json:"attempt"`
		}
		_ = json.Unmarshal(ev.Data, &payload)
		if payload.Attempt != "" {
			seen[payload.Attempt] = true
		}
	}
	for id, attempt := range failed {
		if seen[id] {
			continue
		}
		if !success.IsZero() && !attempt.StartedAt.After(success) && !attempt.EndedAt.After(success) {
			continue
		}
		n++
	}
	return n, nil
}

func (r *Runner) openLaunchQuestion(ctx context.Context, db *store.Store, card *model.Card, step, summary string) error {
	all, err := db.ListQuestions(ctx, card.Objective)
	if err != nil {
		return err
	}
	for _, q := range all {
		if q.Scope == card.ID && strings.TrimSpace(q.Answer) == "" && strings.HasPrefix(q.Text, "launch_failed\n") {
			return nil
		}
	}
	_, err = r.Questions(db).Create(ctx, model.Question{
		Objective: card.Objective,
		Scope:     card.ID,
		ScopeKind: "card",
		Kind:      model.QuestionCustom,
		Text:      "launch_failed\nstep: " + step + "\nerror: " + summary,
		Options:   []string{"approve", "reject"},
	})
	return err
}

// keepStep records err as a tick_error and remembers the first one.
// A canceled context is returned so the caller stops the round.
func (r *Runner) keepStep(ctx context.Context, db *store.Store, step, kind, id string, err error, first *error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.noteTickError(ctx, db, step, kind, id, err)
	if first != nil && *first == nil {
		*first = tickNoted{err}
	}
	return nil
}

func (r *Runner) noteTickError(ctx context.Context, db *store.Store, step, kind, id string, cause error) {
	if r == nil || cause == nil || (ctx != nil && ctx.Err() != nil) {
		return
	}
	reportServeError(ctx, step, cause)
	summary := clipSummary(cause.Error())
	key := step + "\x00" + kind + "\x00" + id + "\x00" + summary
	now := r.now()
	r.tickMu.Lock()
	if r.tickAt == nil {
		r.tickAt = map[string]time.Time{}
	}
	if prev, ok := r.tickAt[key]; ok && now.Sub(prev) < tickErrorWindow {
		r.tickMu.Unlock()
		return
	}
	object := tickObject(kind, id)
	data, merr := json.Marshal(tickPayload(step, kind, id, summary))
	if merr != nil || db == nil {
		r.tickMu.Unlock()
		fmt.Fprintf(os.Stderr, "tick_error step=%s object=%s error=%s\n", step, object, summary)
		return
	}
	err := db.AppendEvent(ctx, &model.Event{
		TS:     now.UTC(),
		Entity: tickEntity(kind, id),
		Type:   model.EventTickError,
		Data:   data,
	})
	if err != nil {
		r.tickMu.Unlock()
		fmt.Fprintf(os.Stderr, "tick_error step=%s object=%s error=%s\n", step, object, summary)
		return
	}
	r.tickAt[key] = now
	r.tickMu.Unlock()
}

func tickPayload(step, kind, id, summary string) map[string]string {
	payload := map[string]string{"step": step, "error": summary}
	switch kind {
	case "card":
		payload["card"] = id
	case "objective":
		payload["objective"] = id
	}
	return payload
}

func tickEntity(kind, id string) string {
	switch kind {
	case "card":
		if id != "" {
			return "card:" + id
		}
	case "objective":
		if id != "" {
			return "objective:" + id
		}
	}
	return "tick"
}

func tickObject(kind, id string) string {
	if kind == "" || id == "" {
		return "tick"
	}
	return kind + ":" + id
}

// A failed launch can race an operator edit too. Do not convert a newer
// card or attempt into backoff. Only remove an owned new worktree, never a
// shared branch, and never remove a preparation another host may own.
func (r *Runner) failStrictLaunch(ctx context.Context, db *store.Store, card *model.Card, attempt *model.Attempt, step string, cause error, rt model.Route, scratch launchScratch) (string, error) {
	summary := clipSummary(cause.Error())
	attemptID := ""
	prior := 0
	changed, err := withObservedCard(ctx, db, *card, func(ctx context.Context) error {
		var err error
		prior, err = r.launchFailureStreak(ctx, db, card.ID)
		if err != nil {
			return err
		}
		rows, err := db.ListAttempts(ctx, card.ID)
		if err != nil {
			return err
		}
		if attempt != nil {
			current, err := strictPreparedAttempt(rows, *attempt)
			if err != nil {
				return err
			}
			attemptID = current.ID
			exit := 1
			current.Outcome, current.ErrorClass, current.ErrorExcerpt = model.OutcomeLaunchFailed, model.ErrorLaunchFailed, summary
			current.EndedAt, current.Exit = r.now().UTC(), &exit
			if err := db.UpdateAttempt(ctx, &current); err != nil {
				return err
			}
			if err := markLaunchDisposed(ctx, db, r.now(), &current); err != nil {
				return err
			}
			*attempt = current
		} else {
			for _, current := range rows {
				if attemptOpen(current) {
					return errStrictAdmissionChanged
				}
			}
		}
		r.applyLaunchBackoff(card, prior)
		if err := db.UpdateCard(ctx, card); err != nil {
			return err
		}
		return r.writeLaunchFailed(ctx, db, card.ID, attemptID, step, summary, rt)
	})
	if err != nil {
		return "", err
	}
	if (changed || attempt == nil) && scratch.dropWorktree && scratch.strict != nil && ctx.Err() == nil {
		workspace.Git{}.DropStrictLaunch(ctx, scratch.strict.StrictOwner, *scratch.strict, scratch.worktree, scratch.branch, scratch.base)
	}
	if !changed {
		return "", nil
	}
	reportServeError(ctx, "launch", cause)
	reportServeEvent(ctx, "transition state=launch_backoff")
	if attempt != nil {
		if err := harvest.WriteUnavailableSummary(ctx, r.harvestInput(ctx, db, *card, *attempt, 1, nil)); err != nil {
			_ = noteEvidenceWriteFailed(ctx, db, r.Home, *attempt, err)
		}
	}
	if prior+1 >= 3 {
		if _, err := withObservedCard(ctx, db, *card, func(ctx context.Context) error {
			return r.openLaunchQuestion(ctx, db, card, step, summary)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "launch_failed question card=%s error=%v\n", card.ID, err)
		}
	}
	return "", nil
}
