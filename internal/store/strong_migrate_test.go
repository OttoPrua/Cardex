package store

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStrongV14MigrationPreservesStrictRuntimeAndRules(t *testing.T) {
	strongTables := []string{"strong_producers", "strong_tasks", "strong_artifacts", "strong_input_bindings"}
	for _, fixture := range []struct {
		name             string
		downgrade        int
		standaloneStrict bool
	}{
		{"fresh", 0, false},
		{"D1 durable v13", 13, false},
		{"public rules v12 then D1", 12, false},
		{"standalone strict v12 then D1", 12, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "cardex.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			rules := []string{"retain synthetic project rule"}
			if fixture.standaloneStrict {
				rules = nil
			}
			p := model.Project{ID: "project-existing", RepoPath: t.TempDir(), HardRules: rules}
			o := model.Objective{ID: "objective-existing", Project: p.ID, Title: "Existing objective", Mode: model.ModeManual, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive, HardRules: rules}
			strict := fixture.downgrade != 12 || fixture.standaloneStrict
			c := model.Card{ID: "card-existing", Objective: o.ID, Kind: model.KindDev, Status: model.CardReady, Deps: []string{"gate-existing"}, StrictDeps: strict}
			for _, err := range []error{db.CreateProject(ctx, &p), db.CreateObjective(ctx, &o), db.CreateCard(ctx, &c)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			if fixture.downgrade != 0 {
				for _, table := range strongTables {
					if _, err := db.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
						t.Fatal(err)
					}
				}
				if fixture.downgrade == 12 {
					dropStrictRuntimeFixture(t, db)
					stmts := []string{`ALTER TABLE cards DROP COLUMN strict_deps`}
					if fixture.standaloneStrict {
						stmts = []string{
							`UPDATE cards SET deps='["gate-existing"]' WHERE id='card-existing'`,
							`ALTER TABLE projects DROP COLUMN hard_rules`,
							`ALTER TABLE objectives DROP COLUMN hard_rules`,
						}
					}
					for _, stmt := range stmts {
						if _, err := db.db.ExecContext(ctx, stmt); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := db.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>?`, fixture.downgrade); err != nil {
					t.Fatal(err)
				}
				if got, err := db.SchemaVersion(ctx); err != nil || got != fixture.downgrade {
					t.Fatalf("old schema = %d: %v", got, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				db, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if got, err := db.SchemaVersion(ctx); err != nil || got != 14 {
					t.Fatalf("schema = %d: %v", got, err)
				}
				gotP, err := db.GetProject(ctx, p.ID)
				if err != nil || !slices.Equal(gotP.HardRules, rules) {
					t.Fatalf("project: %+v %v", gotP, err)
				}
				gotO, err := db.GetObjective(ctx, o.ID)
				if err != nil || !slices.Equal(gotO.HardRules, rules) {
					t.Fatalf("objective: %+v %v", gotO, err)
				}
				gotC, err := db.GetCard(ctx, c.ID)
				if err != nil || gotC.StrictDeps != strict || !slices.Equal(gotC.Deps, c.Deps) {
					t.Fatalf("card: %+v %v", gotC, err)
				}
				for _, table := range strongTables {
					var n int
					if err := db.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
						t.Fatalf("%s has unsolicited rows %d: %v", table, n, err)
					}
				}
				var stamps int
				if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version IN (12,13,14)`).Scan(&stamps); err != nil || stamps != 3 {
					t.Fatalf("migration stamps=%d: %v", stamps, err)
				}
				var fence int
				if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name IN ('strict_card_insert','strict_card_update','strict_attempt_insert','strict_attempt_update')`).Scan(&fence); err != nil || fence != 4 {
					t.Fatalf("D1 guards=%d: %v", fence, err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
