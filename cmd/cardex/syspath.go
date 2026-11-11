package main

import (
	"os"
	"path"
	"runtime"
	"strings"
)

// systemPathDirs are the directories a login shell always has on PATH. A
// launchd or systemd unit often ships a hand-written PATH that leaves out the
// sbin ones, and then acceptance commands such as md5 or sha256sum (in /sbin
// on macOS) fail only under serve.
var systemPathDirs = []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// withSystemPath appends any missing system directory to path, keeping the
// existing order so user entries still win.
func withSystemPath(list, goos string) string {
	if goos == "windows" {
		return list
	}
	have := map[string]bool{}
	var parts []string
	for _, p := range strings.Split(list, ":") {
		if p == "" {
			continue
		}
		have[path.Clean(p)] = true
		parts = append(parts, p)
	}
	for _, d := range systemPathDirs {
		if !have[d] {
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, ":")
}

func ensureSystemPath() {
	_ = os.Setenv("PATH", withSystemPath(os.Getenv("PATH"), runtime.GOOS))
}
