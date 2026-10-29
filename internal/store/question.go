package store

import (
	"context"
	"fmt"

	"cardex/internal/model"
)

func prepareQuestion(q *model.Question) error {
	if q == nil {
		return fmt.Errorf("nil question")
	}
	id, err := ensureID(q.ID, "q")
	if err != nil {
		return err
	}
	q.ID = id
	if !q.Kind.Valid() {
		return invalid("question kind", q.Kind)
	}
	q.Options = normStrings(q.Options)
	if q.Deliveries == nil {
		q.Deliveries = []model.Delivery{}
	}
	return nil
}

func (s *Store) CreateQuestion(ctx context.Context, q *model.Question) error {
	if err := prepareQuestion(q); err != nil {
		return err
	}
	options, err := mustJSON(q.Options)
	if err != nil {
		return err
	}
	deliveries, err := mustJSON(q.Deliveries)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO questions(id, objective_id, scope, scope_kind, kind, text, options, answer, deliveries) VALUES (?,?,?,?,?,?,?,?,?)`,
		q.ID, q.Objective, q.Scope, q.ScopeKind, string(q.Kind), q.Text, options, q.Answer, deliveries)
	return err
}

func (s *Store) GetQuestion(ctx context.Context, id string) (model.Question, error) {
	row := s.q(ctx).QueryRowContext(ctx, questionSelect+` WHERE id=?`, id)
	return scanQuestion(row)
}

func (s *Store) UpdateQuestion(ctx context.Context, q *model.Question) error {
	if q == nil || q.ID == "" {
		return fmt.Errorf("question id required")
	}
	if !q.Kind.Valid() {
		return invalid("question kind", q.Kind)
	}
	q.Options = normStrings(q.Options)
	if q.Deliveries == nil {
		q.Deliveries = []model.Delivery{}
	}
	options, err := mustJSON(q.Options)
	if err != nil {
		return err
	}
	deliveries, err := mustJSON(q.Deliveries)
	if err != nil {
		return err
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE questions SET objective_id=?, scope=?, scope_kind=?, kind=?, text=?, options=?, answer=?, deliveries=? WHERE id=?`,
		q.Objective, q.Scope, q.ScopeKind, string(q.Kind), q.Text, options, q.Answer, deliveries, q.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeleteQuestion(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM questions WHERE id=?`, id)
	return rowsAffected(res, err)
}

const questionSelect = `SELECT id, objective_id, scope, scope_kind, kind, text, options, answer, deliveries FROM questions`

func (s *Store) ListQuestions(ctx context.Context, objectiveID string) ([]model.Question, error) {
	q := questionSelect
	var args []any
	if objectiveID != "" {
		q += ` WHERE objective_id=?`
		args = append(args, objectiveID)
	}
	q += ` ORDER BY id`
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Question{}
	for rows.Next() {
		item, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanQuestion(sc scanner) (model.Question, error) {
	var q model.Question
	var kind, options, deliveries string
	if err := sc.Scan(&q.ID, &q.Objective, &q.Scope, &q.ScopeKind, &kind, &q.Text, &options, &q.Answer, &deliveries); err != nil {
		return q, err
	}
	q.Kind = model.QuestionKind(kind)
	if err := parseJSON(options, &q.Options); err != nil {
		return q, err
	}
	if err := parseJSON(deliveries, &q.Deliveries); err != nil {
		return q, err
	}
	q.Options = normStrings(q.Options)
	if q.Deliveries == nil {
		q.Deliveries = []model.Delivery{}
	}
	return q, nil
}
