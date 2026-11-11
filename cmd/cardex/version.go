package main

import (
	"fmt"
	"io"
	"runtime/debug"
)

// revision can be set at build time:
//
//	go build -ldflags "-X main.revision=$(git rev-parse --short HEAD)" ./cmd/cardex
//
// Otherwise the vcs stamp go build embeds from a git checkout is used.
var revision string

func buildRevision() string {
	if revision != "" {
		return revision
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "unknown (built without vcs info; use -ldflags -X main.revision=...)"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}

func cmdVersion(stdout io.Writer) int {
	fmt.Fprintln(stdout, "cardex", buildRevision())
	return 0
}
