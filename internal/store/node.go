package store

import (
	"context"
	"fmt"

	"cardex/internal/model"
)

func prepareNode(n *model.DesignNode) error {
	if n == nil {
		return fmt.Errorf("nil node")
	}
	id, err := ensureID(n.ID, "n")
	if err != nil {
		return err
	}
	n.ID = id
	if !n.Layer.Valid() {
		return invalid("layer", n.Layer)
	}
	if !n.Status.Valid() {
		return invalid("node status", n.Status)
	}
	qs, err := model.NormalizeOpenQuestions(n.OpenQuestions)
	if err != nil {
		return err
	}
	n.OpenQuestions = qs
	return nil
}

func (s *Store) CreateNode(ctx context.Context, n *model.DesignNode) error {
	if err := prepareNode(n); err != nil {
		return err
	}
	raw, err := mustJSON(n.OpenQuestions)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO design_nodes(id, objective_id, parent_id, layer, doc_path, open_questions, status) VALUES (?,?,?,?,?,?,?)`,
		n.ID, n.Objective, n.Parent, string(n.Layer), n.DocPath, raw, string(n.Status))
	return err
}

func (s *Store) GetNode(ctx context.Context, id string) (model.DesignNode, error) {
	row := s.q(ctx).QueryRowContext(ctx, `SELECT id, objective_id, parent_id, layer, doc_path, open_questions, status FROM design_nodes WHERE id=?`, id)
	return scanNode(row)
}

func (s *Store) UpdateNode(ctx context.Context, n *model.DesignNode) error {
	if n == nil || n.ID == "" {
		return fmt.Errorf("node id required")
	}
	if !n.Layer.Valid() || !n.Status.Valid() {
		return fmt.Errorf("invalid node enum")
	}
	qs, err := model.NormalizeOpenQuestions(n.OpenQuestions)
	if err != nil {
		return err
	}
	n.OpenQuestions = qs
	raw, err := mustJSON(n.OpenQuestions)
	if err != nil {
		return err
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE design_nodes SET objective_id=?, parent_id=?, layer=?, doc_path=?, open_questions=?, status=? WHERE id=?`,
		n.Objective, n.Parent, string(n.Layer), n.DocPath, raw, string(n.Status), n.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM design_nodes WHERE id=?`, id)
	return rowsAffected(res, err)
}

func (s *Store) ListNodes(ctx context.Context, objectiveID string) ([]model.DesignNode, error) {
	q := `SELECT id, objective_id, parent_id, layer, doc_path, open_questions, status FROM design_nodes`
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
	out := []model.DesignNode{}
	for rows.Next() {
		item, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanNode(sc scanner) (model.DesignNode, error) {
	var n model.DesignNode
	var questions, layer, status string
	if err := sc.Scan(&n.ID, &n.Objective, &n.Parent, &layer, &n.DocPath, &questions, &status); err != nil {
		return n, err
	}
	var err error
	if err := parseJSON(questions, &n.OpenQuestions); err != nil {
		return n, err
	}
	n.OpenQuestions, err = model.NormalizeOpenQuestions(n.OpenQuestions)
	if err != nil {
		return n, err
	}
	n.Layer = model.Layer(layer)
	n.Status = model.NodeStatus(status)
	return n, nil
}
