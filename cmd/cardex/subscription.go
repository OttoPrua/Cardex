package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/OttoPrua/Cardex/internal/quota"
)

func channelImportUsage(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("channel import-usage", "import one explicitly authorized CodexBar usage snapshot; never reads credentials or starts a collector", "--id --provider --input", stdout, stderr)
	id := fs.String("id", "", "target channel (shares usage with its source_group)")
	provider := fs.String("provider", "", "codex, claude, cursor, grok, gemini, or kimi")
	input := fs.String("input", "", "CodexBar JSON file, or - for stdin; required")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if *id == "" || *provider == "" || *input == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "--id, --provider and --input are required")
		return 2
	}
	r := os.Stdin
	if *input != "-" {
		f, err := os.Open(*input)
		if err != nil {
			fmt.Fprintln(stderr, "cannot open CodexBar usage input")
			return 1
		}
		defer f.Close()
		r = f
	}
	s, err := quota.ParseCodexBar(r, *provider, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ch, err := db.GetChannel(context.Background(), *id)
	if err != nil {
		fmt.Fprintln(stderr, "target channel not found")
		return 1
	}
	dir, err := resolveHome(home)
	if err == nil {
		err = quota.SaveSubscription(dir, ch.SourceGroup, ch.ID, s)
	}
	if err != nil {
		fmt.Fprintln(stderr, "cannot save subscription usage")
		return 1
	}
	return emit(stdout, asJSON, s, fmt.Sprintf("%s\t%s\t%s", ch.ID, s.Provider, s.State))
}
