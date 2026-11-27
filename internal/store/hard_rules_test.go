package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestMigration12KeepsExistingRowsWithoutHardRules(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: "seed-project", RepoPath: "repo"}
	objective := model.Objective{ID: "seed-objective", Project: project.ID, Mode: model.ModeAuto, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive}
	if err := db.CreateProject(ctx, &project); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &objective); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"ALTER TABLE projects DROP COLUMN hard_rules",
		"ALTER TABLE objectives DROP COLUMN hard_rules",
		"DELETE FROM schema_migrations WHERE version=12",
	} {
		if _, err := db.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := db.GetProject(ctx, project.ID)
	if err != nil || p.RepoPath != "repo" || len(p.HardRules) != 0 {
		t.Fatalf("project migration: %+v: %v", p, err)
	}
	o, err := db.GetObjective(ctx, objective.ID)
	if err != nil || o.Project != project.ID || len(o.HardRules) != 0 {
		t.Fatalf("objective migration: %+v: %v", o, err)
	}
}

func TestInvalidHardRuleLeavesProjectUnchanged(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := model.Project{ID: "seed-project", HardRules: []string{"Original"}}
	if err := db.CreateProject(ctx, &p); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"two\nlines", strings.Repeat("界", 171), "hidden\x00byte"} {
		p.HardRules = []string{invalid}
		if err := db.UpdateProject(ctx, &p); err == nil {
			t.Fatalf("accepted invalid rule")
		}
		stored, err := db.GetProject(ctx, p.ID)
		if err != nil || len(stored.HardRules) != 1 || stored.HardRules[0] != "Original" {
			t.Fatalf("rules changed: %+v: %v", stored, err)
		}
	}
}
