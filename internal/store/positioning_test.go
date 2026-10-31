package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"cardex/internal/model"
)

func TestPositioningTierCaseIsSoft(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	p := model.Positioning{
		Model: "opus", Family: "anthropic", Tiers: []model.Tier{"t2", " T3 "}, Preference: 1,
	}
	if err := db.CreatePositioning(ctx, &p); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetPositioning(ctx, p.ID)
	if err != nil || len(got.Tiers) != 2 || got.Tiers[0] != model.TierT2 || got.Tiers[1] != model.TierT3 {
		t.Fatalf("created %+v err %v", got.Tiers, err)
	}
	p.Tiers = []model.Tier{" t1 "}
	if err := db.UpdatePositioning(ctx, &p); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetPositioning(ctx, p.ID)
	if err != nil || len(got.Tiers) != 1 || got.Tiers[0] != model.TierT1 {
		t.Fatalf("updated %+v err %v", got.Tiers, err)
	}

	unknown := model.Positioning{Model: "other", Family: "f", Tiers: []model.Tier{"nope"}, Preference: 1}
	if err := db.CreatePositioning(ctx, &unknown); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetPositioning(ctx, unknown.ID)
	if err != nil || len(got.Tiers) != 0 {
		t.Fatalf("unknown tier stored %+v err %v", got.Tiers, err)
	}
}

func TestMigrationMovesPositioningVersionRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM schema_migrations WHERE version>=3`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO positioning(id, version, model, family, tiers, capabilities, preference) VALUES ('cardex.positioning.version', 7, '', '', '[]', '[]', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.ListPositioning(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("list %+v err %v", rows, err)
	}
	if _, err := db.GetPositioning(ctx, legacyPositioningVersionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get version row %v", err)
	}
	version, ok, err := db.PositioningPublishedVersion(ctx)
	if err != nil || !ok || version != 7 {
		t.Fatalf("version %d ok %v err %v", version, ok, err)
	}
}
