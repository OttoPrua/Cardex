package store

import (
	"context"
	"fmt"

	"github.com/OttoPrua/Cardex/internal/model"
)

func prepareObjective(o *model.Objective) error {
	if o == nil {
		return fmt.Errorf("nil objective")
	}
	id, err := ensureID(o.ID, "o")
	if err != nil {
		return err
	}
	o.ID = id
	if !o.Mode.Valid() {
		return invalid("mode", o.Mode)
	}
	if !o.FreezeMode.Valid() {
		return invalid("freeze_mode", o.FreezeMode)
	}
	if !o.Status.Valid() {
		return invalid("status", o.Status)
	}
	o.Acceptance.Commands = normStrings(o.Acceptance.Commands)
	o.Acceptance.Checklist = normStrings(o.Acceptance.Checklist)
	return nil
}

func (s *Store) CreateObjective(ctx context.Context, o *model.Objective) error {
	if err := prepareObjective(o); err != nil {
		return err
	}
	raw, err := mustJSON(o.Acceptance)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO objectives(id, project_id, title, acceptance, soft_tokens, hard_tokens, priority, mode, freeze_mode, origin, status, integration_branch, channel, model) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.ID, o.Project, o.Title, raw, o.Budget.SoftTokens, o.Budget.HardTokens, o.Priority, string(o.Mode), string(o.FreezeMode), o.Origin, string(o.Status), o.IntegrationBranch, o.Channel, o.Model)
	return err
}

func (s *Store) GetObjective(ctx context.Context, id string) (model.Objective, error) {
	row := s.q(ctx).QueryRowContext(ctx, `SELECT id, project_id, title, acceptance, soft_tokens, hard_tokens, priority, mode, freeze_mode, origin, status, integration_branch, channel, model FROM objectives WHERE id=?`, id)
	return scanObjective(row)
}

func (s *Store) UpdateObjective(ctx context.Context, o *model.Objective) error {
	if o == nil || o.ID == "" {
		return fmt.Errorf("objective id required")
	}
	if !o.Mode.Valid() || !o.FreezeMode.Valid() || !o.Status.Valid() {
		return fmt.Errorf("invalid objective enum")
	}
	o.Acceptance.Commands = normStrings(o.Acceptance.Commands)
	o.Acceptance.Checklist = normStrings(o.Acceptance.Checklist)
	raw, err := mustJSON(o.Acceptance)
	if err != nil {
		return err
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE objectives SET project_id=?, title=?, acceptance=?, soft_tokens=?, hard_tokens=?, priority=?, mode=?, freeze_mode=?, origin=?, status=?, integration_branch=?, channel=?, model=? WHERE id=?`,
		o.Project, o.Title, raw, o.Budget.SoftTokens, o.Budget.HardTokens, o.Priority, string(o.Mode), string(o.FreezeMode), o.Origin, string(o.Status), o.IntegrationBranch, o.Channel, o.Model, o.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeleteObjective(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM objectives WHERE id=?`, id)
	return rowsAffected(res, err)
}

func (s *Store) ListObjectives(ctx context.Context, projectID string) ([]model.Objective, error) {
	q := `SELECT id, project_id, title, acceptance, soft_tokens, hard_tokens, priority, mode, freeze_mode, origin, status, integration_branch, channel, model FROM objectives`
	var args []any
	if projectID != "" {
		q += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	q += ` ORDER BY id`
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Objective{}
	for rows.Next() {
		item, err := scanObjective(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanObjective(sc scanner) (model.Objective, error) {
	var o model.Objective
	var acceptance, mode, freeze, status string
	if err := sc.Scan(&o.ID, &o.Project, &o.Title, &acceptance, &o.Budget.SoftTokens, &o.Budget.HardTokens, &o.Priority, &mode, &freeze, &o.Origin, &status, &o.IntegrationBranch, &o.Channel, &o.Model); err != nil {
		return o, err
	}
	if err := parseJSON(acceptance, &o.Acceptance); err != nil {
		return o, err
	}
	o.Acceptance.Commands = normStrings(o.Acceptance.Commands)
	o.Acceptance.Checklist = normStrings(o.Acceptance.Checklist)
	o.Mode = model.ObjectiveMode(mode)
	o.FreezeMode = model.FreezeMode(freeze)
	o.Status = model.ObjectiveStatus(status)
	return o, nil
}
