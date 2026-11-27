package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

type blockerRow struct {
	ID         string           `json:"id"`
	Objective  string           `json:"objective"`
	Status     model.CardStatus `json:"status"`
	Reason     string           `json:"reason"`
	QuestionID string           `json:"question_id,omitempty"`
}

func cmdBlockers(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("blockers", "list current blocked cards and why they stopped", "[--objective <id>] --json", stdout, stderr)
	objective := fs.String("objective", "", "only report cards in this objective")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "blockers [--objective <id>] [--json]")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	var blockers []blockerRow
	if err := db.WithTx(context.Background(), func(ctx context.Context) error {
		var err error
		blockers, err = collectBlockers(ctx, db, *objective)
		return err
	}); err != nil {
		fmt.Fprintf(stderr, "blockers: %v\n", err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, blockers)
	}
	if len(blockers) == 0 {
		fmt.Fprintln(stdout, "no blockers")
	}
	for _, blocker := range blockers {
		reason := blocker.Reason
		if blocker.QuestionID != "" {
			reason = fmt.Sprintf("%s (question %s)", reason, clipBriefText(blocker.QuestionID))
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", clipBriefText(blocker.Objective), clipBriefText(blocker.ID), blocker.Status, reason)
	}
	return 0
}

func collectBlockers(ctx context.Context, db *store.Store, objective string) ([]blockerRow, error) {
	if objective != "" {
		if _, err := db.GetObjective(ctx, objective); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("objective %q not found", objective)
			}
			return nil, err
		}
	}
	states, err := db.ListCardBlockerStates(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]model.CardStatus, len(states))
	failedNormalizers := make(map[string][]string)
	for _, state := range states {
		statuses[state.ID] = state.Status
		if state.Kind == model.KindNormalize && state.Status == model.CardFailed && len(state.Deps) > 0 {
			parent := state.Deps[0]
			failedNormalizers[parent] = append(failedNormalizers[parent], state.ID)
		}
	}
	result := []blockerRow{}
	for _, state := range states {
		if objective != "" && state.Objective != objective {
			continue
		}
		if state.ObjectiveStatus == model.ObjectiveDone || state.ObjectiveStatus == model.ObjectiveCanceled {
			continue
		}
		row := blockerRow{ID: state.ID, Objective: state.Objective, Status: state.Status}
		switch state.Status {
		case model.CardNeedsInput, model.CardFailed:
			if err := stoppedReason(ctx, db, state.Card, &row, failedNormalizers[state.ID]); err != nil {
				return nil, err
			}
		case model.CardPaused:
			row.Reason = "card is paused"
		case model.CardWaiting, model.CardReady:
			if state.Status == model.CardWaiting {
				row.Reason = blockedReason(state.Card)
			}
			if row.Reason == "" {
				switch state.ObjectiveStatus {
				case model.ObjectivePaused:
					row.Reason = "objective is paused"
				case "":
					row.Reason = "objective does not exist"
				default:
					row.Reason = dependencyBlocker(state.Card, statuses)
				}
			}
			if row.Reason == "" {
				if state.Status == model.CardReady {
					continue // Being ready or not yet routed is not itself a blocker.
				}
				row.Reason = "waiting; no reason recorded"
			}
		default:
			continue
		}
		row.Reason = clipBriefText(row.Reason)
		result = append(result, row)
	}
	return result, nil
}

func stoppedReason(ctx context.Context, db *store.Store, c model.Card, row *blockerRow, failedNormalizers []string) error {
	row.Reason = blockedReason(c)
	var question *cardBriefQuestion
	if c.WaitQuestionID == "" {
		if c.Status == model.CardNeedsInput || row.Reason == "" {
			q, err := pendingBriefQuestion(ctx, db, c.ID)
			if err != nil {
				return err
			}
			question = q
		}
	} else {
		q, err := db.GetQuestion(ctx, c.WaitQuestionID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Do not replace an answered or missing bound question with an
		// unrelated, older one about the same card.
		if err == nil && q.Scope == c.ID && (q.ScopeKind == "card" || q.ScopeKind == "") && q.Answer == "" {
			question = &cardBriefQuestion{ID: q.ID, Line: clipBriefText(q.Text)}
		}
	}
	if question != nil {
		row.QuestionID = question.ID
		if row.Reason == "" {
			row.Reason = question.Line
		}
	}
	if row.Reason == "" {
		reason, err := briefAttemptFallbackReason(ctx, db, objectiveBlocked{ID: c.ID, Status: string(c.Status)}, failedNormalizers)
		if err != nil {
			return err
		}
		row.Reason = reason
	}
	if row.Reason == "" {
		if c.Status == model.CardFailed {
			row.Reason = "failed; no reason recorded"
		} else {
			row.Reason = "needs input; no reason recorded"
		}
	}
	return nil
}

func dependencyBlocker(card model.Card, statuses map[string]model.CardStatus) string {
	ignore := ""
	// The scheduler treats Deps[0] as a subject, not a completion gate,
	// for these four kinds. Do not show an in-flight subject as a blocker.
	switch card.Kind {
	case model.KindReview:
		if subject, explicit := model.ReviewSubject(card); explicit && statuses[subject] == "" {
			return "review subject " + subject + " does not exist"
		}
		fallthrough
	case model.KindRepair, model.KindRebase, model.KindNormalize:
		if len(card.Deps) > 0 {
			ignore = strings.TrimSpace(card.Deps[0])
		}
	}
	for _, id := range card.Deps {
		id = strings.TrimSpace(id)
		if id == "" || id == ignore {
			continue
		}
		status := statuses[id]
		switch status {
		case model.CardMerged, model.CardDone, model.CardCanceled:
			continue
		case "":
			return "dependency " + id + " does not exist"
		default:
			return fmt.Sprintf("dependency %s is %s", id, status)
		}
	}
	return ""
}
