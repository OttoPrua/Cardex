package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// deviceLiterals are the /dev nodes a short-lived CLI needs to open for write.
var deviceLiterals = []string{
	"/dev/null",
	"/dev/zero",
	"/dev/dtracehelper",
	"/dev/random",
	"/dev/urandom",
	"/dev/tty",
	"/dev/ptmx",
	"/dev/stdin",
	"/dev/stdout",
	"/dev/stderr",
}

// Profile is the seatbelt profile for writable paths.
// Read is allowed by default, network is allowed, and writes are limited to
// writable, TMPDIR, and the /dev nodes above. Paths are expanded through
// symlinks so a /var alias and its /private/var target are both covered.
func Profile(writable []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	b.WriteString("(allow file-read*)\n")
	b.WriteString("(allow network*)\n")
	b.WriteString("(deny file-write*)\n")
	for _, p := range expandPaths(append(append([]string{}, writable...), os.TempDir())) {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbString(p))
	}
	for _, dev := range deviceLiterals {
		fmt.Fprintf(&b, "(allow file-write* (literal %s))\n", sbString(dev))
	}
	b.WriteString("(allow file-write* (regex #\"^/dev/ttys[0-9]+$\"))\n")
	b.WriteString("(allow file-write* (regex #\"^/dev/pts(/|$)\"))\n")
	return b.String()
}

func expandPaths(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || p == "/" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		add(abs)
		if real, ok := resolveExisting(abs); ok {
			add(real)
		}
	}
	return out
}

func resolveExisting(p string) (string, bool) {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, true
	}
	parent, base := filepath.Dir(p), filepath.Base(p)
	if parent == p {
		return "", false
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", false
	}
	return filepath.Join(realParent, base), true
}

func sbString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
