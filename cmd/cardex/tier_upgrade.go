package main

import (
	"context"
	"fmt"
	"io"

	"github.com/OttoPrua/Cardex/internal/store"
)

func cmdMigrateTiers(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	const usage = "cardex [--home <dir>] migrate tiers --yes (requires a stopped old scheduler and drained attempts)"
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	if len(args) != 2 || args[0] != "tiers" || args[1] != "--yes" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if refuseManagedCardWrite(stderr) {
		return 1
	}
	dir, err := homePath(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	db, err := store.OpenTierUpgrade(absDB(dir))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	schema, err := db.SchemaVersion(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, map[string]any{"schema_version": schema, "tier_convention": "T1 strongest, T2 routine, T3 lightest"}, "Tier upgrade ready. T1 strongest, T2 routine, T3 lightest.")
}
