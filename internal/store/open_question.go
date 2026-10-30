package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"cardex/internal/model"
)

// migrateOpenQuestions rewrites legacy string arrays in the same open_questions column.
func migrateOpenQuestions(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT id, open_questions FROM design_nodes`)
	if err != nil {
		return err
	}
	type row struct {
		id  string
		raw string
	}
	var pending []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.raw); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range pending {
		if item.raw == "" || !model.ContainsLegacyOpenQuestion([]byte(item.raw)) {
			continue
		}
		qs, err := model.ParseOpenQuestions([]byte(item.raw))
		if err != nil {
			return err
		}
		raw, err := json.Marshal(qs)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE design_nodes SET open_questions=? WHERE id=?`, string(raw), item.id); err != nil {
			return err
		}
	}
	return nil
}

// ListOpenQuestions returns questions limited to nodeID and status.
// An empty nodeID searches every node. An empty status does not filter by status.
func (s *Store) ListOpenQuestions(ctx context.Context, nodeID string, status model.OpenQuestionStatus) ([]model.OpenQuestion, error) {
	if status != "" && !status.Valid() {
		return nil, invalid("open question status", status)
	}
	var nodes []model.DesignNode
	if nodeID != "" {
		n, err := s.GetNode(ctx, nodeID)
		if err != nil {
			return nil, err
		}
		nodes = []model.DesignNode{n}
	} else {
		listed, err := s.ListNodes(ctx, "")
		if err != nil {
			return nil, err
		}
		nodes = listed
	}
	out := []model.OpenQuestion{}
	for _, n := range nodes {
		for _, q := range n.OpenQuestions {
			if status != "" && q.Status != status {
				continue
			}
			q.CitedPaths = append([]string(nil), q.CitedPaths...)
			out = append(out, q)
		}
	}
	return out, nil
}
