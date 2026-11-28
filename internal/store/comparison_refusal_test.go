package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAPI2DisabledComparisonRecognizesOnlyInstalledTypedCustody(t *testing.T) {
	ctx := t.Context()
	path := legacyTierFixture(t, legacyTierRoute)
	prepareTestBackupOnWindows(t, filepath.Join(filepath.Dir(path), "backups"))
	db, err := OpenTierUpgrade(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(attempt string, want bool) {
		t.Helper()
		if err := db.WithWriteTx(ctx, func(txCtx context.Context) error {
			owned, err := db.strictComparisonTypedOwner(txCtx, attempt)
			if err == nil && owned != want {
				t.Fatalf("typed fixture owner for %q: %t, want %t", attempt, owned, want)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	check("synthetic-attempt", false) // v16 is absent; do not install it.
	if err := db.InstallStrongWorkflowPrototype(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO strong_run_intents
		(run_id,revision,slot,intent_id,attempt_id,card_id,state,route_json,reserved)
		VALUES ('fixture',1,'slot','fixture-intent','synthetic-attempt','legacy-card','reserved','{}',1)`); err != nil {
		t.Fatal(err)
	}
	check("synthetic-attempt", true)
	check("unrelated-attempt", false)
}
