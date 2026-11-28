package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStrongWorkflowPrototypeDoesNotActivateOrRewriteV15(t *testing.T) {
	ctx := t.Context()
	path := legacyTierFixture(t, legacyTierRoute)
	if db, err := Open(path); !errors.Is(err, ErrTierUpgradeRequired) || db != nil {
		t.Fatalf("ordinary open bypassed explicit T1: %v", err)
	}
	prepareTestBackupOnWindows(t, filepath.Join(filepath.Dir(path), "backups"))
	db, err := OpenTierUpgrade(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(db *Store, wantTables int) {
		t.Helper()
		v, err := db.SchemaVersion(ctx)
		if err != nil || v != TierConventionSchema {
			t.Fatalf("global schema=%d %v", v, err)
		}
		active, err := db.TierConventionActive(ctx)
		if err != nil || !active {
			t.Fatalf("typed v15 receipt lost: %v", err)
		}
		var wire, route string
		if err := db.db.QueryRowContext(ctx, `SELECT tier FROM cards WHERE id='legacy-card'`).Scan(&wire); err != nil || wire != "T3" {
			t.Fatalf("legacy wire was rewritten %q %v", wire, err)
		}
		if err := db.db.QueryRowContext(ctx, `SELECT route_reason FROM attempts WHERE id='legacy-attempt'`).Scan(&route); err != nil || route != legacyTierRoute {
			t.Fatalf("stored old-wire routing receipt changed %q %v", route, err)
		}
		card, err := db.GetCard(ctx, "legacy-card")
		if err != nil || card.Tier != model.TierT1 {
			t.Fatalf("typed conventional read lost: %+v %v", card, err)
		}
		var tables int
		if err := db.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND (name LIKE 'strong_run_%' OR name='strong_fake_execution')`).Scan(&tables); err != nil || tables != wantTables {
			t.Fatalf("prototype tables %d want %d: %v", tables, wantTables, err)
		}
	}
	check(db, 0)
	for range 2 {
		if err := db.InstallStrongWorkflowPrototype(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check(db, 5)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	check(reopened, 5)
}

func TestStrongWorkflowPrototypeRejectsNumericV15WithoutTypedReceipt(t *testing.T) {
	ctx := t.Context()
	path := legacyTierFixture(t, legacyTierRoute)
	prepareTestBackupOnWindows(t, filepath.Join(filepath.Dir(path), "backups"))
	db, err := OpenTierUpgrade(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.ExecContext(ctx, `UPDATE tier_semantics_activation SET convention='incorrect-foreign-convention'`); err != nil {
		t.Fatal(err)
	}
	if err := db.InstallStrongWorkflowPrototype(ctx); !errors.Is(err, model.ErrStrongPlan) {
		t.Fatalf("numeric v15 bypassed typed activation: %v", err)
	}
	var added int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='strong_run_plans'`).Scan(&added); err != nil || added != 0 {
		t.Fatalf("failed install mutated schema: %d %v", added, err)
	}
}
