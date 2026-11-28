package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/OttoPrua/Cardex/internal/homeguard"
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

// ensureRuntimePath runs at executable entry, before any credential adapter,
// Git subprocess, shell, or background worker resolves a command name.
func ensureRuntimePath(args []string) error {
	home, _, rest, err := splitGlobal(args)
	if err == nil && len(rest) > 0 && (rest[0] == "snapshot" || rest[0] == "verify" || rest[0] == "research" || isOfflineChannelQuote(rest)) {
		// Evidence-only commands need neither executables nor config reads.
		return nil
	}
	inherited := os.Getenv("PATH")
	if err != nil || isGlobalHelp(rest) || len(rest) == 0 || rest[0] == "version" || rest[0] == "--version" || rest[0] == "-v" {
		return os.Setenv("PATH", withSystemPath(inherited, runtime.GOOS))
	}
	dir, err := homePath(home)
	if err != nil || homeguard.Check(dir) != nil {
		return os.Setenv("PATH", withSystemPath(inherited, runtime.GOOS))
	}
	configured, err := configuredRuntimePath(dir)
	if err != nil {
		if channelHealthStopOnly(rest) {
			// This command uses the local store but runs no executor, Git or
			// credential adapter. Broken tool config must not block disabling
			// a live binding. Keep the inherited environment unchanged.
			if fi, statErr := os.Lstat(filepath.Join(dir, "cardex.db")); statErr == nil && fi.Mode().IsRegular() {
				return nil
			}
		}
		return err
	}
	if configured != "" {
		inherited = configured
	}
	return os.Setenv("PATH", withSystemPath(inherited, runtime.GOOS))
}

func configuredRuntimePath(home string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read runtime PATH configuration: %w", err)
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil || len(top["runtime_path"]) == 0 || bytes.Equal(bytes.TrimSpace(top["runtime_path"]), []byte("null")) {
		// Preserve existing behavior for homes without a runtime_path setting.
		return "", nil
	}
	var entries []string
	if err := json.Unmarshal(top["runtime_path"], &entries); err != nil || len(entries) == 0 {
		return "", fmt.Errorf("runtime_path must be a non-empty array of absolute directories")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(entries))
	for i, entry := range entries {
		if !filepath.IsAbs(entry) || strings.ContainsRune(entry, os.PathListSeparator) || strings.ContainsAny(entry, "\x00\n\r") {
			return "", fmt.Errorf("runtime_path entry %d must be one absolute directory", i+1)
		}
		entry = filepath.Clean(entry)
		key := entry
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, entry)
		}
	}
	return strings.Join(out, string(os.PathListSeparator)), nil
}

// channelHealthStopOnly is intentionally narrower than channel set. It does
// not move flags across operands, accept duplicate fields, or interpret a
// flag-like argument as a value for some other flag.
func channelHealthStopOnly(args []string) bool {
	if len(args) < 2 || args[0] != "channel" || args[1] != "set" {
		return false
	}
	id, health := "", ""
	for i := 2; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			if id != "" || i != len(args)-1 {
				return false
			}
			id = arg
			continue
		}
		name, value, equals := strings.Cut(arg, "=")
		if name != "--id" && name != "-id" && name != "--health" && name != "-health" {
			return false
		}
		if !equals {
			i++
			if i == len(args) {
				return false
			}
			value = args[i]
		}
		if value == "" || strings.HasPrefix(value, "-") {
			return false
		}
		switch name {
		case "--id", "-id":
			if id != "" {
				return false
			}
			id = value
		default:
			if health != "" {
				return false
			}
			health = strings.TrimSpace(value)
			if health != "degraded" && health != "auth_failed" {
				return false
			}
		}
	}
	return strings.TrimSpace(id) != "" && health != ""
}
