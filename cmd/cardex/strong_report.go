package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/OttoPrua/Cardex/internal/board"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func cmdResearch(home string, args []string, stdout, stderr io.Writer) int {
	const usage = "research report [--redacted] --run <id> --revision <n> --sha <plan-sha>\nPrivate JSON by default; --redacted generates a separate allowlisted offline JSON report.\n"
	if groupHelp(stdout, args, usage) {
		return 0
	}
	if args[0] != "report" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := newFlagSet("research report", "inspect an existing synthetic research workflow", "--run --revision --sha [--redacted]", stdout, stderr)
	run := fs.String("run", "", "exact run key")
	rev := fs.Int("revision", 0, "exact plan revision")
	sha := fs.String("sha", "", "exact plan SHA")
	redacted := fs.Bool("redacted", false, "only allowlisted report text and aggregate usage")
	if code, ok := parseSet(fs, args[1:]); !ok {
		return code
	}
	if fs.NArg() != 0 || !model.StrongKey(*run) || *rev < 1 || !model.StrongDigestKey(*sha) {
		fmt.Fprint(stderr, usage)
		return 2
	}
	dir, err := homePath(home)
	if err != nil {
		fmt.Fprintln(stderr, "cannot resolve the report database")
		return 1
	}
	report, err := board.ReadStrongReport(context.Background(), filepath.Join(dir, "cardex.db"), store.StrongRunKey{Run: *run, Revision: *rev, SHA: *sha})
	if err != nil {
		fmt.Fprintln(stderr, "synthetic research report unavailable: requires an exact run revision and a stable checkpointed database without sidecars")
		return 1
	}
	if !*redacted {
		return writeJSON(stdout, report)
	}
	export, err := board.RedactedStrongReport(report)
	if err != nil {
		fmt.Fprintln(stderr, "synthetic research redacted report unavailable")
		return 1
	}
	return writeJSON(stdout, export)
}
