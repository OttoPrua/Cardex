package store

import (
	"context"
	"fmt"

	"github.com/OttoPrua/Cardex/internal/model"
)

func (s *Store) CreateProject(ctx context.Context, p *model.Project) error {
	if p == nil {
		return fmt.Errorf("nil project")
	}
	id, err := ensureID(p.ID, "p")
	if err != nil {
		return err
	}
	p.ID = id
	p.ForbiddenPaths = normStrings(p.ForbiddenPaths)
	raw, err := mustJSON(p.ForbiddenPaths)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO projects(id, repo_path, worktree_root, test_cmd, design_dir, forbidden_paths) VALUES (?,?,?,?,?,?)`,
		p.ID, p.RepoPath, p.WorktreeRoot, p.TestCmd, p.DesignDir, raw)
	return err
}

func (s *Store) GetProject(ctx context.Context, id string) (model.Project, error) {
	row := s.q(ctx).QueryRowContext(ctx, `SELECT id, repo_path, worktree_root, test_cmd, design_dir, forbidden_paths FROM projects WHERE id=?`, id)
	return scanProject(row)
}

func (s *Store) UpdateProject(ctx context.Context, p *model.Project) error {
	if p == nil || p.ID == "" {
		return fmt.Errorf("project id required")
	}
	p.ForbiddenPaths = normStrings(p.ForbiddenPaths)
	raw, err := mustJSON(p.ForbiddenPaths)
	if err != nil {
		return err
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE projects SET repo_path=?, worktree_root=?, test_cmd=?, design_dir=?, forbidden_paths=? WHERE id=?`,
		p.RepoPath, p.WorktreeRoot, p.TestCmd, p.DesignDir, raw, p.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id)
	return rowsAffected(res, err)
}

func (s *Store) ListProjects(ctx context.Context) ([]model.Project, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, repo_path, worktree_root, test_cmd, design_dir, forbidden_paths FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanProject(sc scanner) (model.Project, error) {
	var p model.Project
	var forbidden string
	if err := sc.Scan(&p.ID, &p.RepoPath, &p.WorktreeRoot, &p.TestCmd, &p.DesignDir, &forbidden); err != nil {
		return p, err
	}
	if err := parseJSON(forbidden, &p.ForbiddenPaths); err != nil {
		return p, err
	}
	p.ForbiddenPaths = normStrings(p.ForbiddenPaths)
	return p, nil
}
