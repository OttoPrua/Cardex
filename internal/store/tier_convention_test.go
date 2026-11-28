package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func legacyTierFixture(t *testing.T, reason string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	card := model.Card{ID: "legacy-card", Kind: model.KindDev, Tier: model.TierT1, Title: "Literal T3 stays", Status: model.CardReady}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{ID: "legacy-channel", Runner: model.RunnerCodex, Health: model.HealthOK, Models: []string{"fixture-strong"}, Tier: model.TierT1}
	if err := db.CreateChannel(ctx, &channel); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{ID: "legacy-positioning", Model: "fixture-strong", Tiers: []model.Tier{model.TierT1, model.TierT2}}
	if err := db.CreatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{ID: "legacy-attempt", Card: card.ID, Route: model.Route{Channel: channel.ID, Model: "fixture-strong"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE attempts SET route_reason=? WHERE id='legacy-attempt'", reason); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version=?", TierConventionSchema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTierConventionLegacyReviewHoldKeepsStrongFloor(t *testing.T) {
	path := legacyTierFixture(t, legacyTierRoute)
	raw, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	original := "prose about T3\n" + model.ReviewHoldMarker + "\ntier=T3\nchannel=fixture\nmodel=strong\n"
	if _, err := raw.Exec("UPDATE cards SET kind='review', body=?", original); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	db, err := OpenTierUpgrade(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := db.GetCard(t.Context(), "legacy-card")
	if err != nil || c.Body != original || c.Tier != model.TierT1 {
		t.Fatalf("card %+v, %v", c, err)
	}
	tier, channel, name, found, err := model.ParseReviewHold(c.Body)
	if err != nil || !found || tier != model.TierT1 || channel != "fixture" || name != "strong" {
		t.Fatalf("held tier=%s channel=%s model=%s found=%v, err=%v", tier, channel, name, found, err)
	}
}

const legacyTierRoute = `{"candidates":[{"channel":"legacy-channel","model":"fixture-strong","tier":"T3","unknown":"keep T3"}],"dispatch":{"expected_tier":"T3","actual_tier":"T3"},"unknown":"keep root"}`

func TestTierConventionPreservesRoutingEvidenceAndIsIdempotent(t *testing.T) {
	path := legacyTierFixture(t, legacyTierRoute)
	if db, err := Open(path); !errors.Is(err, ErrTierUpgradeRequired) {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("legacy was not fenced: %v", err)
	}
	for range 2 {
		db, err := OpenTierUpgrade(path)
		if err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		c, err := db.GetCard(ctx, "legacy-card")
		if err != nil || c.Tier != model.TierT1 || c.Title != "Literal T3 stays" {
			t.Fatalf("card %+v, %v", c, err)
		}
		ch, err := db.GetChannel(ctx, "legacy-channel")
		if err != nil || ch.Tier != model.TierT1 || len(ch.Models) != 1 || ch.Models[0] != "fixture-strong" {
			t.Fatalf("channel %+v, %v", ch, err)
		}
		p, err := db.GetPositioning(ctx, "legacy-positioning")
		if err != nil || len(p.Tiers) != 2 || p.Tiers[0] != model.TierT1 || p.Tiers[1] != model.TierT2 {
			t.Fatalf("positioning %+v, %v", p, err)
		}
		a, err := db.GetAttempt(ctx, "legacy-attempt")
		if err != nil || a.RouteReason.Dispatch == nil || a.RouteReason.Dispatch.Tier != model.TierT1 ||
			a.RouteReason.SelectedTier(a.Route) != model.TierT1 || a.RouteReason.Candidates[0].Tier != model.TierT1 {
			t.Fatalf("attempt %+v, %v", a, err)
		}
		var live string
		if err := db.db.QueryRowContext(ctx, "SELECT route_reason FROM attempts").Scan(&live); err != nil || live != legacyTierRoute {
			t.Fatalf("wire evidence changed: %q, %v", live, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTierConventionUnknownRollsBackEarlierChanges(t *testing.T) {
	path := legacyTierFixture(t, strings.Replace(legacyTierRoute, "\"T3\"", "\"T9\"", 1))
	if db, err := OpenTierUpgrade(path); err == nil || !strings.Contains(err.Error(), "unknown tier") {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("expected unknown-tier migration error; got %v", err)
	}
	db, err := sql.Open("sqlite", sqliteURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tier string
	if err := db.QueryRow("SELECT tier FROM cards").Scan(&tier); err != nil || tier != "T3" {
		t.Fatalf("earlier card update leaked: %s, %v", tier, err)
	}
	var recorded int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&recorded); err != nil || (recorded < 12 || recorded >= TierConventionSchema) {
		t.Fatalf("version marker leaked: %d, %v", recorded, err)
	}
}

func TestTierConventionRejectsActiveOldAttempt(t *testing.T) {
	path := legacyTierFixture(t, legacyTierRoute)
	db, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE attempts SET started_at='2026-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if db, err := OpenTierUpgrade(path); err == nil || !strings.Contains(err.Error(), "drain running attempts") {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("expected active attempt error; got %v", err)
	}
}

func TestTierConventionPendingOldPlanningResultRequiresDrain(t *testing.T) {
	path := legacyTierFixture(t, legacyTierRoute)
	raw, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("UPDATE cards SET kind='replan', status='done'; UPDATE attempts SET outcome='passed'; INSERT INTO events(id,ts,entity,event_type,data) VALUES ('absorbed','','attempt:legacy-attempt','loop.absorbed','{}')"); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if db, err := OpenTierUpgrade(path); err == nil || !strings.Contains(err.Error(), "consume finished planning") {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("unconsumed legacy result was not rejected: %v", err)
	}
	raw, err = sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("INSERT INTO events(id,ts,entity,event_type,data) VALUES ('done','','attempt:legacy-attempt','dev_disposed','{}')"); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	db, err := OpenTierUpgrade(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

func TestTierConventionNewWritesKeepLegacyWireSpelling(t *testing.T) {
	ctx := t.Context()
	db, err := Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := model.Card{ID: "c", Tier: model.TierT1, Kind: model.KindReview, Status: model.CardReady}
	if err := db.CreateCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	ch := model.Channel{ID: "ch", Tier: model.TierT1, Runner: model.RunnerCodex, Health: model.HealthOK}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	p := model.Positioning{ID: "m", Tiers: []model.Tier{model.TierT1, model.TierT3}}
	if err := db.CreatePositioning(ctx, &p); err != nil {
		t.Fatal(err)
	}
	r := model.RouteReason{
		Candidates: []model.RouteCandidate{{Tier: model.TierT1}},
		Dispatch:   &model.RouteDispatch{Tier: model.TierT3, ActualTier: model.TierT1},
	}
	a := model.Attempt{ID: "a", RouteReason: r}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		"SELECT tier FROM cards":        "T3",
		"SELECT tier FROM channels":     "T3",
		"SELECT tiers FROM positioning": `["T3","T1"]`,
		"SELECT json_extract(route_reason,'$.candidates[0].tier') FROM attempts":     "T3",
		"SELECT json_extract(route_reason,'$.dispatch.expected_tier') FROM attempts": "T1",
		"SELECT json_extract(route_reason,'$.dispatch.actual_tier') FROM attempts":   "T3",
	} {
		var v string
		if err := db.db.QueryRowContext(ctx, q).Scan(&v); err != nil || v != want {
			t.Fatalf("%s gives %s, want %s: %v", q, v, want, err)
		}
	}
	if c.Tier != model.TierT1 || ch.Tier != model.TierT1 || p.Tiers[0] != model.TierT1 ||
		r.Dispatch.Tier != model.TierT3 || r.Candidates[0].Tier != model.TierT1 {
		t.Fatal("encoding modified caller's model")
	}
}

func TestTierConventionRejectsExitedUnharvestedLegacyResultRegardlessOfCardStatus(t *testing.T) {
	for _, fixture := range []struct{ kind, status, fields string }{
		{"plan", "paused", "ended_at='2026-01-01T00:00:00Z'"},
		{"replan", "canceled", "exited=1"},
		{"normalize", "canceled", "exited=1"},
		{"ranking", "done", "ended_at='2026-01-01T00:00:00Z'"},
	} {
		t.Run(fixture.kind, func(t *testing.T) {
			path := legacyTierFixture(t, legacyTierRoute)
			raw, err := sql.Open("sqlite", sqliteURI(path))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec("UPDATE cards SET kind=?, status=?", fixture.kind, fixture.status); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec("UPDATE attempts SET outcome='', " + fixture.fields); err != nil {
				t.Fatal(err)
			}
			if upgraded, err := OpenTierUpgrade(path); err == nil || !strings.Contains(err.Error(), "consume finished planning") {
				if upgraded != nil {
					_ = upgraded.Close()
				}
				t.Fatalf("unharvested legacy artifact was not blocked: %v", err)
			}
			var marker int
			if err := raw.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=?", TierConventionSchema).Scan(&marker); err != nil || marker != 0 {
				t.Fatalf("activation marker changed: %d, %v", marker, err)
			}
		})
	}
}

func TestTierActivationDoesNotTrustBareSchemaVersion(t *testing.T) {
	for _, tamper := range []string{
		"DROP TABLE tier_semantics_activation",
		"UPDATE tier_semantics_activation SET convention='unknown'",
		"UPDATE tier_semantics_activation SET schema_version=14",
	} {
		db := openDB(t)
		path := db.path
		if _, err := db.db.Exec(tamper); err != nil {
			t.Fatal(err)
		}
		if active, err := db.TierConventionActive(t.Context()); err != nil || active {
			t.Fatalf("bad receipt was accepted: %v %v", active, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		for _, open := range []func(string) (*Store, error){Open, OpenTierUpgrade} {
			got, err := open(path)
			if err == nil || !strings.Contains(err.Error(), "no valid tier activation receipt") {
				if got != nil {
					_ = got.Close()
				}
				t.Fatalf("bad receipt was accepted on open: %v", err)
			}
		}
	}
}

func TestTierActivationFromST1V14PreservesD1AndLineage(t *testing.T) {
	ctx := t.Context()
	db := openDB(t)
	path := db.path
	// The v14 schema is installed by actual migration14; removing only T1's
	// table and receipt reproduces a physically complete D1+ST1 home.
	for _, stmt := range []string{
		"DROP TABLE tier_semantics_activation",
		"DELETE FROM schema_migrations WHERE version=15",
		"INSERT INTO strong_tasks(run_id,sha,size,body) VALUES ('fixture','fixture-hash',2,X'7B7D')",
		"INSERT INTO strong_input_bindings(run_id,target_stage,role,target_seat,allowlist_json,allowlist_sha) VALUES ('fixture','stage','review','seat','[]','fixture-allowlist')",
	} {
		if _, err := db.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if version, err := db.SchemaVersion(ctx); err != nil || version != 14 {
		t.Fatalf("expected actual ST1 physical schema 14: %d %v", version, err)
	}
	var d1Receipt, st1Receipt string
	for i, dst := range []*string{&d1Receipt, &st1Receipt} {
		if err := db.db.QueryRowContext(ctx, "SELECT applied_at FROM schema_migrations WHERE version=?", 13+i).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if ordinary, err := Open(path); !errors.Is(err, ErrTierUpgradeRequired) {
		if ordinary != nil {
			_ = ordinary.Close()
		}
		t.Fatalf("v14 cannot impersonate tier activation: %v", err)
	}
	backupDir := filepath.Join(filepath.Dir(path), "backups")
	prepareTestBackupOnWindows(t, backupDir)
	db, err := OpenTierUpgrade(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if active, err := db.TierConventionActive(ctx); err != nil || !active {
		t.Fatalf("v15 activation inactive: %v %v", active, err)
	}
	for i, want := range []string{d1Receipt, st1Receipt} {
		var got string
		if err := db.db.QueryRowContext(ctx, "SELECT applied_at FROM schema_migrations WHERE version=?", 13+i).Scan(&got); err != nil || got != want {
			t.Fatalf("v%d receipt changed: %q != %q: %v", 13+i, got, want, err)
		}
	}
	var task, binding string
	if err := db.db.QueryRowContext(ctx, "SELECT CAST(body AS TEXT) FROM strong_tasks WHERE run_id='fixture'").Scan(&task); err != nil || task != "{}" {
		t.Fatalf("ST1 task changed: %q %v", task, err)
	}
	if err := db.db.QueryRowContext(ctx, "SELECT allowlist_sha FROM strong_input_bindings WHERE run_id='fixture' AND target_stage='stage'").Scan(&binding); err != nil || binding != "fixture-allowlist" {
		t.Fatalf("ST1 input binding changed: %q %v", binding, err)
	}
	if copies := dbCopies(t, backupDir); len(copies) != 1 {
		t.Fatalf("expected one durable preactivation backup, got %d", len(copies))
	}
}
