package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"time"

	"github.com/OttoPrua/Cardex/internal/homeguard"
	"github.com/OttoPrua/Cardex/internal/store"
)

const snapshotUsage = "snapshot --json [--card <id>] [--checkpointed-copy]"

func cmdSnapshot(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("snapshot", "print a small read-only status snapshot", "--json [--card <id>] [--checkpointed-copy]", stdout, stderr)
	card := fs.String("card", "", "card id")
	copyOnly := fs.Bool("checkpointed-copy", false, "--home points to a separate, stable checkpointed backup")
	// Flag parsing errors can echo arguments. Only print a fixed usage error.
	help := fs.Usage
	fs.Usage = func() {}
	fs.SetOutput(io.Discard)
	if code, ok := parseSet(fs, args); !ok {
		if code == 0 {
			help()
		} else {
			fmt.Fprintln(stderr, snapshotUsage)
		}
		return code
	}
	emptyCard := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "card" && *card == "" {
			emptyCard = true
		}
	})
	if !asJSON || fs.NArg() > 0 || emptyCard {
		fmt.Fprintln(stderr, snapshotUsage)
		return 2
	}
	if *copyOnly && home == "" {
		fmt.Fprintln(stderr, "snapshot: --checkpointed-copy requires --home <backup-directory>")
		return 2
	}
	if runtime.GOOS != "linux" && !*copyOnly {
		fmt.Fprintln(stderr, "snapshot: original-home reads are unsupported on this platform; use --checkpointed-copy with a stable backup")
		return 1
	}

	dir, err := homePath(home)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var data store.Snapshot
		path := filepath.Join(dir, "cardex.db")
		if *copyOnly {
			data, err = store.ReadCheckpointedCopy(ctx, path, *card)
		} else {
			data, err = store.ReadSnapshot(ctx, path, *card)
			if errors.Is(err, store.ErrSnapshotUnavailable) {
				data, err = tryLiveSnapshot(ctx, path, *card)
			}
		}
		if err == nil {
			return writeJSON(stdout, struct {
				Version  string `json:"version"`
				Revision string `json:"revision"`
				Serve    string `json:"serve"`
				store.Snapshot
			}{version, buildRevision(), "unknown", data})
		}
	}
	msg := "snapshot: database state is unavailable; try again with a stable --checkpointed-copy"
	switch {
	case errors.Is(err, store.ErrSnapshotNotFound):
		msg = "snapshot: database does not exist"
	case errors.Is(err, store.ErrSnapshotSchema):
		msg = "snapshot: database schema does not match this binary"
	case errors.Is(err, sql.ErrNoRows):
		msg = "snapshot: card not found"
	case homeguard.IsV1(err):
		msg = "snapshot: 这是 v1 的数据目录。用法：" + homeguard.Usage
	}
	fmt.Fprintln(stderr, msg)
	return 1
}
