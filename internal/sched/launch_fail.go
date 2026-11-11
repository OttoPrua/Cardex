package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

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
	workspace.Git{}.DropLaunch(ctx, scratch.repo, scratch.worktree, scratch.branch, scratch.base, scratch.dropWorktree, scratch.dropBranch)
	summary := clipSummary(cause.Error())
	// Count before this attempt is marked, so it is not paired with the event twice.
	prior, err := r.launchFailureStreak(ctx, db, card.ID)
	if err != nil {
		return "", err
	}
	fresh, err := db.GetCard(ctx, card.ID)
	if err != nil {
		return "", err
	}
	r.applyLaunchBackoff(&fresh, prior)
	if rt.Channel == "" && attempt != nil {
		rt = attempt.Route
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if attempt != nil && attempt.ID != "" {
			exit := 1
			attempt.Outcome = model.OutcomeLaunchFailed
			attempt.ErrorClass = model.ErrorLaunchFailed
			attempt.ErrorExcerpt = summary
			attempt.EndedAt = r.now().UTC()
			attempt.Exit = &exit
			if err := db.UpdateAttempt(ctx, attempt); err != nil {
				return err
			}
			if err := markLaunchDisposed(ctx, db, r.now(), attempt); err != nil {
				return err
			}
		}
		return db.UpdateCard(ctx, &fresh)
	})
	if err != nil {
		return "", err
	}
	*card = fresh
	attemptID := ""
	if attempt != nil {
		attemptID = attempt.ID
	}
	if werr := r.writeLaunchFailed(ctx, db, card.ID, attemptID, step, summary, rt); werr != nil {
		fmt.Fprintf(os.Stderr, "launch_failed card=%s step=%s error=%s write=%v\n", card.ID, step, summary, werr)
	} else {
		prior++
	}
	if prior >= 3 {
		if qerr := r.openLaunchQuestion(ctx, db, card, step, summary); qerr != nil {
			fmt.Fprintf(os.Stderr, "launch_failed question card=%s error=%v\n", card.ID, qerr)
		}
	}
	return "", nil
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
