package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStrictDepsMigrationAndRoundTrip(t *testing.T) {
	ctx := t.Context()
	original := openDB(t)
	c := model.Card{ID: "legacy", Kind: model.KindDev, Status: model.CardReady, Deps: []string{"canceled", "done"}}
	if err := original.CreateCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	dropStrictRuntimeFixture(t, original)
	for _, stmt := range []string{
		`ALTER TABLE cards DROP COLUMN strict_deps`,
		`DELETE FROM schema_migrations WHERE version>11`,
	} {
		if _, err := original.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(original.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old, err := db.GetCard(ctx, c.ID)
	if err != nil || old.StrictDeps || !slices.Equal(old.Deps, c.Deps) {
		t.Fatalf("migrated old card %+v error %v", old, err)
	}
	var oldDeps string
	if err := db.db.QueryRowContext(ctx, `SELECT deps FROM cards WHERE id=?`, c.ID).Scan(&oldDeps); err != nil || oldDeps != `["canceled","done"]` {
		t.Fatalf("legacy dependency storage %q error %v", oldDeps, err)
	}

	var strict model.Card
	if err := json.Unmarshal([]byte(`{"id":"strict","kind":"dev","status":"ready","strict_deps":true,"deps":["canceled","done"]}`), &strict); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &strict); err != nil {
		t.Fatal(err)
	}
	strict, err = db.GetCard(ctx, strict.ID)
	if err != nil || !strict.StrictDeps || !slices.Equal(strict.Deps, c.Deps) {
		t.Fatalf("round-trip card %+v error %v", strict, err)
	}
	strict.Status = model.CardPaused
	if err := db.UpdateCard(ctx, &strict); err != nil {
		t.Fatal(err)
	}
	cards, err := db.ListCards(ctx, "")
	if err != nil || len(cards) != 2 || cards[0].StrictDeps || !cards[1].StrictDeps || cards[1].Status != model.CardPaused {
		t.Fatalf("list after status update %+v error %v", cards, err)
	}
}

func TestStrictDepsV13MigratesBothV12ShapesAndFresh(t *testing.T) {
	for _, tc := range []struct {
		name      string
		downgrade []string
		strict    bool
		rules     []string
	}{
		{"published rules v12", []string{`ALTER TABLE cards DROP COLUMN strict_deps`}, false, []string{"Preserve this rule."}},
		{"standalone strict v12", []string{`ALTER TABLE projects DROP COLUMN hard_rules`, `ALTER TABLE objectives DROP COLUMN hard_rules`}, true, nil},
		{"fresh", nil, false, []string{"Rules and strict deps coexist."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			initial := openDB(t)
			if len(tc.downgrade) == 0 {
				if version, err := initial.SchemaVersion(ctx); err != nil || version != latestSchema {
					t.Fatalf("fresh schema = %d; err %v", version, err)
				}
			}
			p := model.Project{ID: "p", RepoPath: t.TempDir(), HardRules: tc.rules}
			if err := initial.CreateProject(ctx, &p); err != nil {
				t.Fatal(err)
			}
			o := model.Objective{ID: "o", Project: p.ID, Title: "Synthetic", Mode: model.ModeManual, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive, HardRules: tc.rules}
			if err := initial.CreateObjective(ctx, &o); err != nil {
				t.Fatal(err)
			}
			c := model.Card{ID: "d", Objective: o.ID, Kind: model.KindDev, Status: model.CardReady, StrictDeps: tc.strict, Deps: []string{"canceled", "done"}}
			if err := initial.CreateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			if len(tc.downgrade) > 0 {
				dropStrictRuntimeFixture(t, initial)
				if tc.strict {
					if _, err := initial.db.ExecContext(ctx, `UPDATE cards SET deps=? WHERE id=?`, `["canceled","done"]`, c.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, stmt := range tc.downgrade {
				if _, err := initial.db.ExecContext(ctx, stmt); err != nil {
					t.Fatal(err)
				}
			}
			if len(tc.downgrade) > 0 {
				if _, err := initial.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>12`); err != nil {
					t.Fatal(err)
				}
			}
			if err := initial.Close(); err != nil {
				t.Fatal(err)
			}

			for range 2 {
				db, err := Open(initial.path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if version, err := db.SchemaVersion(ctx); err != nil || version != latestSchema {
					t.Fatalf("schema = %d; err %v", version, err)
				}
				gotP, err := db.GetProject(ctx, p.ID)
				if err != nil || !slices.Equal(gotP.HardRules, tc.rules) {
					t.Fatalf("project rules %v; err %v", gotP.HardRules, err)
				}
				gotO, err := db.GetObjective(ctx, o.ID)
				if err != nil || !slices.Equal(gotO.HardRules, tc.rules) {
					t.Fatalf("objective rules %v; err %v", gotO.HardRules, err)
				}
				gotC, err := db.GetCard(ctx, c.ID)
				if err != nil || gotC.StrictDeps != tc.strict || !slices.Equal(gotC.Deps, c.Deps) {
					t.Fatalf("card strict=%t deps=%v; err %v", gotC.StrictDeps, gotC.Deps, err)
				}
			}
		})
	}
}

func dropStrictRuntimeFixture(t *testing.T, db *Store) {
	t.Helper()
	for _, stmt := range []string{
		`DROP TRIGGER strict_card_insert`, `DROP TRIGGER strict_card_update`,
		`DROP TRIGGER strict_attempt_insert`, `DROP TRIGGER strict_attempt_update`,
		`ALTER TABLE attempts DROP COLUMN strict_runtime_rev`,
	} {
		if _, err := db.db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStrictRuntimeFenceRejectsLegacyAttemptAndIdentityWrites(t *testing.T) {
	ctx, db := t.Context(), openDB(t)
	for _, c := range []model.Card{
		{ID: "legacy", Kind: model.KindDev, Status: model.CardReady, Deps: []string{"gate"}},
		{ID: "strict", Kind: model.KindDev, Status: model.CardReady, StrictDeps: true, Deps: []string{"gate"}},
	} {
		if err := db.CreateCard(ctx, &c); err != nil {
			t.Fatal(err)
		}
		a := model.Attempt{ID: "attempt-" + c.ID, Card: c.ID}
		if err := db.CreateAttempt(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	legacyInsert := `INSERT INTO attempts(id, ` + attemptWriteColumns + `, changed_at) SELECT ?, ` +
		attemptWriteColumns + `, changed_at FROM attempts WHERE id=?`
	for _, tc := range []struct {
		query string
		args  []any
		pass  bool
	}{
		{legacyInsert, []any{"a-old-strict", "attempt-strict"}, false},
		{legacyInsert, []any{"a-old-legacy", "attempt-legacy"}, true},
		{`UPDATE attempts SET pid=123 WHERE id=?`, []any{"attempt-strict"}, false},
		{`UPDATE attempts SET pid=123 WHERE id=?`, []any{"attempt-legacy"}, true},
		{`UPDATE cards SET id=? WHERE id='legacy'`, []any{strictRuntimeGuard}, false},
		{`UPDATE cards SET deps='[]' WHERE id='strict'`, nil, false},
	} {
		if _, err := db.db.ExecContext(ctx, tc.query, tc.args...); (err == nil) != tc.pass {
			t.Fatalf("legacy SQL %s args=%v pass=%t err=%v", tc.query, tc.args, tc.pass, err)
		}
	}
	a, err := db.GetAttempt(ctx, "attempt-strict")
	if err != nil || a.PID != 0 {
		t.Fatalf("old runtime changed strict PID: %+v; err %v", a, err)
	}
	a.PID = 123
	if err := db.UpdateAttempt(ctx, &a); err != nil {
		t.Fatalf("new runtime did not pass its fence: %v", err)
	}
	var rawDeps string
	if err := db.db.QueryRowContext(ctx, `SELECT deps FROM cards WHERE id='strict'`).Scan(&rawDeps); err != nil {
		t.Fatal(err)
	}
	var deps []string
	if err := json.Unmarshal([]byte(rawDeps), &deps); err != nil || !slices.Equal(deps, []string{"gate", strictRuntimeGuard}) {
		t.Fatalf("old scheduler does not see the guard: %q; err %v", rawDeps, err)
	}
	c, err := db.GetCard(ctx, "strict")
	if err != nil || !slices.Equal(c.Deps, []string{"gate"}) {
		t.Fatalf("new scheduler sees the reserved guard: %v err %v", c.Deps, err)
	}
}

func TestStrictRuntimeFenceDoesNotStopPreUpgradeHostFinishing(t *testing.T) {
	ctx, db := t.Context(), openDB(t)
	c := model.Card{ID: "strict", Kind: model.KindDev, Status: model.CardRunning, StrictDeps: true}
	if err := db.CreateCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	a := model.Attempt{ID: "a", Card: c.ID, PID: 123}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE attempts SET strict_runtime_rev=0 WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE attempts SET pid=123 WHERE id='a'`); err == nil {
		t.Fatal("legacy PID-only claim succeeded for an already claimed attempt")
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE attempts SET pid=123, ended_at=? WHERE id='a'`, "2026-01-01T01:00:00Z"); err != nil {
		t.Fatalf("a prior host could not record its terminal result: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE attempts SET pid=123, ended_at=? WHERE id='a'`, "2026-01-01T02:00:00Z"); err == nil {
		t.Fatal("legacy rewrote the same attempt after its first terminal write")
	}
}

func TestStrictRuntimeFenceRepairsExistingV13UnderMigrationBackup(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "pre-fence v13"
		if partial {
			name = "v13 with missing trigger"
		}
		t.Run(name, func(t *testing.T) {
			ctx, original := t.Context(), openDB(t)
			c := model.Card{ID: "strict", Kind: model.KindDev, Status: model.CardReady, StrictDeps: true, Deps: []string{"upstream"}}
			if err := original.CreateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			existing := model.Attempt{ID: "prepared", Card: c.ID}
			if err := original.CreateAttempt(ctx, &existing); err != nil {
				t.Fatal(err)
			}
			if partial {
				if _, err := original.db.ExecContext(ctx, `DROP TRIGGER strict_attempt_update`); err != nil {
					t.Fatal(err)
				}
			} else {
				dropStrictRuntimeFixture(t, original)
				if _, err := original.db.ExecContext(ctx, `UPDATE cards SET deps='["upstream"]' WHERE id='strict'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := original.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>13`); err != nil {
				t.Fatal(err)
			}
			var marker string
			if err := original.db.QueryRowContext(ctx, `SELECT applied_at FROM schema_migrations WHERE version=13`).Scan(&marker); err != nil {
				t.Fatal(err)
			}
			path := original.path
			if err := original.Close(); err != nil {
				t.Fatal(err)
			}
			home := filepath.Dir(path)
			snapshots := filepath.Join(home, "snapshots")
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backups_dir":"snapshots"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			prepareTestBackupOnWindows(t, snapshots)

			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if v, err := db.SchemaVersion(ctx); err != nil || v != latestSchema {
				t.Fatalf("schema %d, expected %d: %v", v, latestSchema, err)
			}
			var gotMarker string
			if err := db.db.QueryRowContext(ctx, `SELECT applied_at FROM schema_migrations WHERE version=13`).Scan(&gotMarker); err != nil || gotMarker != marker {
				t.Fatalf("version 13 marker was replaced: %q vs %q: %v", gotMarker, marker, err)
			}
			var raw string
			var wire []string
			if err := db.db.QueryRowContext(ctx, `SELECT deps FROM cards WHERE id='strict'`).Scan(&raw); err != nil ||
				json.Unmarshal([]byte(raw), &wire) != nil || !slices.Equal(wire, []string{"upstream", strictRuntimeGuard}) {
				t.Fatalf("guarded wire deps: %s: %v", raw, err)
			}
			if _, err := db.db.ExecContext(ctx, `UPDATE attempts SET pid=999 WHERE id='prepared'`); err == nil {
				t.Fatal("repaired epoch trigger accepted a legacy claim")
			}
			got, err := db.GetAttempt(ctx, existing.ID)
			if err != nil || got.PID != 0 {
				t.Fatalf("existing attempt %+v: %v", got, err)
			}
			got.ErrorExcerpt = "synthetic amendment"
			if err := db.UpdateAttempt(ctx, &got); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateAttempt(ctx, &model.Attempt{ID: "current", Card: c.ID}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if copies := dbCopies(t, snapshots); len(copies) != 1 {
				t.Fatalf("shape repair should take one backup, got %v", copies)
			} else if err := Inspect(copies[0], func(frozen *Store) {
				var n int
				if err := frozen.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND name='strict_attempt_update'`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("fence exists in pre-repair backup: %d: %v", n, err)
				}
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
