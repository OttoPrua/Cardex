package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestTierUpgradeCLIRequiresExplicitActivation(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "cardex.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec("DELETE FROM schema_migrations WHERE version=?", store.TierConventionSchema); err != nil {
		t.Fatal(err)
	}
	var out, messages bytes.Buffer
	run := func(args ...string) int {
		out.Reset()
		messages.Reset()
		return Main(append([]string{"--home", home}, args...), &out, &messages)
	}
	if code := run("migrate", "tiers"); code != 2 {
		t.Fatalf("without --yes: %d %s", code, messages.String())
	}
	if code := run("card", "list"); code == 0 || !strings.Contains(messages.String(), "tier upgrade required") {
		t.Fatalf("ordinary command opened an old database: %d %s", code, messages.String())
	}
	if code := run("doctor"); code != 0 || !strings.Contains(out.String()+messages.String(), "tier upgrade required") {
		t.Fatalf("doctor missed activation state: %d %s %s", code, out.String(), messages.String())
	}
	var version int
	if err := legacy.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || (version < 12 || version >= store.TierConventionSchema) {
		t.Fatalf("unexpected mutation: %d %v", version, err)
	}
	t.Setenv(proc.ManagedExecutorEnv, "1")
	if code := run("migrate", "tiers", "--yes"); code == 0 || !strings.Contains(messages.String(), "managed executor") {
		t.Fatalf("managed executor bypassed upgrade guard: %d %s", code, messages.String())
	}
	t.Setenv(proc.ManagedExecutorEnv, "")
	if code := run("migrate", "tiers", "--yes", "--json"); code != 0 || !strings.Contains(out.String(), "T1 strongest") {
		t.Fatalf("explicit activation failed: %d %s %s", code, out.String(), messages.String())
	}
	if code := run("card", "list"); code != 0 {
		t.Fatalf("ordinary command failed after activation: %d %s", code, messages.String())
	}
}
