package store

import (
	"context"
	"fmt"

	"cardex/internal/model"
)

func prepareAttempt(a *model.Attempt) error {
	if a == nil {
		return fmt.Errorf("nil attempt")
	}
	id, err := ensureID(a.ID, "a")
	if err != nil {
		return err
	}
	a.ID = id
	if !a.Outcome.Valid() {
		return invalid("outcome", a.Outcome)
	}
	if !a.ErrorClass.Valid() {
		return invalid("error_class", a.ErrorClass)
	}
	if a.RouteReason.Candidates == nil {
		a.RouteReason.Candidates = []model.RouteCandidate{}
	}
	if a.RouteReason.Filtered == nil {
		a.RouteReason.Filtered = []model.FilteredCandidate{}
	}
	if a.RouteReason.Scores == nil {
		a.RouteReason.Scores = []model.RouteScore{}
	}
	return nil
}

func (s *Store) CreateAttempt(ctx context.Context, a *model.Attempt) error {
	if err := prepareAttempt(a); err != nil {
		return err
	}
	return s.insertAttempt(ctx, a)
}

func (s *Store) insertAttempt(ctx context.Context, a *model.Attempt) error {
	reason, err := mustJSON(a.RouteReason)
	if err != nil {
		return err
	}
	usage, err := mustJSON(a.Usage)
	if err != nil {
		return err
	}
	exitCode, exited := 0, 0
	if a.Exit != nil {
		exitCode = *a.Exit
		exited = 1
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO attempts(id, card_id, attempt_index, channel, model, route_reason, worktree, branch, base_commit, head_commit, pid, started_at, ended_at, exit_code, exited, usage, outcome, error_class, error_excerpt, prompt_path) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Card, a.Index, a.Route.Channel, a.Route.Model, reason, a.Worktree, a.Branch, a.BaseCommit, a.HeadCommit, a.PID, formatTime(a.StartedAt), formatTime(a.EndedAt), exitCode, exited, usage, string(a.Outcome), string(a.ErrorClass), a.ErrorExcerpt, a.PromptPath)
	return err
}

func (s *Store) GetAttempt(ctx context.Context, id string) (model.Attempt, error) {
	row := s.q(ctx).QueryRowContext(ctx, attemptSelect+` WHERE id=?`, id)
	return scanAttempt(row)
}

func (s *Store) UpdateAttempt(ctx context.Context, a *model.Attempt) error {
	if a == nil || a.ID == "" {
		return fmt.Errorf("attempt id required")
	}
	if !a.Outcome.Valid() || !a.ErrorClass.Valid() {
		return fmt.Errorf("invalid attempt enum")
	}
	if err := prepareAttempt(a); err != nil {
		return err
	}
	reason, err := mustJSON(a.RouteReason)
	if err != nil {
		return err
	}
	usage, err := mustJSON(a.Usage)
	if err != nil {
		return err
	}
	exitCode, exited := 0, 0
	if a.Exit != nil {
		exitCode = *a.Exit
		exited = 1
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE attempts SET card_id=?, attempt_index=?, channel=?, model=?, route_reason=?, worktree=?, branch=?, base_commit=?, head_commit=?, pid=?, started_at=?, ended_at=?, exit_code=?, exited=?, usage=?, outcome=?, error_class=?, error_excerpt=?, prompt_path=? WHERE id=?`,
		a.Card, a.Index, a.Route.Channel, a.Route.Model, reason, a.Worktree, a.Branch, a.BaseCommit, a.HeadCommit, a.PID, formatTime(a.StartedAt), formatTime(a.EndedAt), exitCode, exited, usage, string(a.Outcome), string(a.ErrorClass), a.ErrorExcerpt, a.PromptPath, a.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeleteAttempt(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM attempts WHERE id=?`, id)
	return rowsAffected(res, err)
}

const attemptSelect = `SELECT id, card_id, attempt_index, channel, model, route_reason, worktree, branch, base_commit, head_commit, pid, started_at, ended_at, exit_code, exited, usage, outcome, error_class, error_excerpt, prompt_path FROM attempts`

func (s *Store) ListAttempts(ctx context.Context, cardID string) ([]model.Attempt, error) {
	q := attemptSelect
	var args []any
	if cardID != "" {
		q += ` WHERE card_id=?`
		args = append(args, cardID)
	}
	q += ` ORDER BY id`
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Attempt{}
	for rows.Next() {
		item, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanAttempt(sc scanner) (model.Attempt, error) {
	var a model.Attempt
	var reason, started, ended, usage, outcome, class string
	var exitCode, exited int
	if err := sc.Scan(&a.ID, &a.Card, &a.Index, &a.Route.Channel, &a.Route.Model, &reason, &a.Worktree, &a.Branch, &a.BaseCommit, &a.HeadCommit, &a.PID, &started, &ended, &exitCode, &exited, &usage, &outcome, &class, &a.ErrorExcerpt, &a.PromptPath); err != nil {
		return a, err
	}
	if err := parseJSON(reason, &a.RouteReason); err != nil {
		return a, err
	}
	if a.RouteReason.Candidates == nil {
		a.RouteReason.Candidates = []model.RouteCandidate{}
	}
	if a.RouteReason.Filtered == nil {
		a.RouteReason.Filtered = []model.FilteredCandidate{}
	}
	if a.RouteReason.Scores == nil {
		a.RouteReason.Scores = []model.RouteScore{}
	}
	if err := parseJSON(usage, &a.Usage); err != nil {
		return a, err
	}
	var err error
	if a.StartedAt, err = parseTime(started); err != nil {
		return a, err
	}
	if a.EndedAt, err = parseTime(ended); err != nil {
		return a, err
	}
	if exited != 0 {
		v := exitCode
		a.Exit = &v
	}
	a.Outcome = model.Outcome(outcome)
	a.ErrorClass = model.ErrorClass(class)
	return a, nil
}
