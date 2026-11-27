package store

import (
	"context"
	"fmt"
	"strings"

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
	if err := normProjectSandbox(p); err != nil {
		return err
	}
	if err := normReview("project review", &p.Review); err != nil {
		return err
	}
	if err := normEvents(&p.Events); err != nil {
		return err
	}
	p.ForbiddenPaths = normStrings(p.ForbiddenPaths)
	p.ScratchDir = stringsTrim(p.ScratchDir)
	hardRules, err := normalizeHardRules(p.HardRules)
	if err != nil {
		return err
	}
	p.HardRules = hardRules
	rules, err := mustJSON(hardRules)
	if err != nil {
		return err
	}
	raw, err := mustJSON(p.ForbiddenPaths)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO projects(id, repo_path, worktree_root, test_cmd, design_dir, forbidden_paths, sandbox, review, scratch_dir, events, hard_rules) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.RepoPath, p.WorktreeRoot, p.TestCmd, p.DesignDir, raw, p.Sandbox, p.Review, p.ScratchDir, p.Events, rules)
	return err
}

func (s *Store) GetProject(ctx context.Context, id string) (model.Project, error) {
	row := s.q(ctx).QueryRowContext(ctx, `SELECT id, repo_path, worktree_root, test_cmd, design_dir, forbidden_paths, sandbox, review, scratch_dir, events, hard_rules FROM projects WHERE id=?`, id)
	return scanProject(row)
}

func (s *Store) UpdateProject(ctx context.Context, p *model.Project) error {
	if p == nil || p.ID == "" {
		return fmt.Errorf("project id required")
	}
	if err := normProjectSandbox(p); err != nil {
		return err
	}
	if err := normReview("project review", &p.Review); err != nil {
		return err
	}
	if err := normEvents(&p.Events); err != nil {
		return err
	}
	p.ForbiddenPaths = normStrings(p.ForbiddenPaths)
	p.ScratchDir = stringsTrim(p.ScratchDir)
	hardRules, err := normalizeHardRules(p.HardRules)
	if err != nil {
		return err
	}
	p.HardRules = hardRules
	rules, err := mustJSON(hardRules)
	if err != nil {
		return err
	}
	raw, err := mustJSON(p.ForbiddenPaths)
	if err != nil {
		return err
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE projects SET repo_path=?, worktree_root=?, test_cmd=?, design_dir=?, forbidden_paths=?, sandbox=?, review=?, scratch_dir=?, events=?, hard_rules=? WHERE id=?`,
		p.RepoPath, p.WorktreeRoot, p.TestCmd, p.DesignDir, raw, p.Sandbox, p.Review, p.ScratchDir, p.Events, rules, p.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id)
	return rowsAffected(res, err)
}

func (s *Store) ListProjects(ctx context.Context) ([]model.Project, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, repo_path, worktree_root, test_cmd, design_dir, forbidden_paths, sandbox, review, scratch_dir, events, hard_rules FROM projects ORDER BY id`)
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
	var forbidden, rules string
	if err := sc.Scan(&p.ID, &p.RepoPath, &p.WorktreeRoot, &p.TestCmd, &p.DesignDir, &forbidden, &p.Sandbox, &p.Review, &p.ScratchDir, &p.Events, &rules); err != nil {
		return p, err
	}
	if err := parseJSON(forbidden, &p.ForbiddenPaths); err != nil {
		return p, err
	}
	p.ForbiddenPaths = normStrings(p.ForbiddenPaths)
	p.ScratchDir = stringsTrim(p.ScratchDir)
	if err := parseJSON(rules, &p.HardRules); err != nil {
		return p, err
	}
	return p, nil
}

func stringsTrim(s string) string {
	return strings.TrimSpace(s)
}

// normProjectSandbox stores the project default as "" (sandbox on) or "off".
func normProjectSandbox(p *model.Project) error {
	switch p.Sandbox {
	case "", "on":
		p.Sandbox = ""
	case "off":
	default:
		return invalid("project sandbox", p.Sandbox)
	}
	return nil
}

// normEvents stores "", "on", or "off". "inherit" is empty.
func normEvents(v *string) error {
	switch strings.ToLower(strings.TrimSpace(*v)) {
	case "", "inherit":
		*v = ""
	case "on", "true":
		*v = "on"
	case "off", "false":
		*v = "off"
	default:
		return invalid("events", *v)
	}
	return nil
}

// normReview stores a review mode; "inherit" becomes "".
func normReview(field string, v *string) error {
	m, ok := model.ParseReviewMode(*v)
	if !ok {
		return invalid(field, *v)
	}
	*v = string(m)
	return nil
}
