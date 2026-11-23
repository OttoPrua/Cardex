package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const goWrapArg = "-cardex-wrap-go"

func init() {
	if len(os.Args) >= 2 && os.Args[1] == goWrapArg {
		os.Exit(runGoWrap(os.Args[2:]))
	}
}

// PrependGoTestWrap installs a go shim on PATH. The executor process keeps
// its own environment; the shim applies AcceptanceEnv only to `go test`.
// dir is the directory that will hold the shim. CARDEX_GO_WRAP_PATH remembers
// PATH before the shim, so the shim can find the real go binary.
func PrependGoTestWrap(env []string, dir string) ([]string, error) {
	if env == nil {
		env = os.Environ()
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeGoWrap(dir, exe); err != nil {
		return nil, err
	}
	out := append([]string{}, env...)
	if envValue(out, "CARDEX_GO_WRAP_PATH") == "" {
		out = append(out, "CARDEX_GO_WRAP_PATH="+envValue(out, "PATH"))
	}
	out = setEnv(out, "CARDEX_GO_WRAP_DIR", dir)
	out = prependPath(out, dir)
	return out, nil
}

func writeGoWrap(dir, exe string) error {
	if runtime.GOOS == "windows" {
		body := "@echo off\r\n" + winQuote(exe) + " " + goWrapArg + " %*\r\n"
		return os.WriteFile(filepath.Join(dir, "go.cmd"), []byte(body), 0o644)
	}
	body := "#!/bin/sh\nexec " + shSingle(exe) + " " + goWrapArg + " \"$@\"\n"
	return os.WriteFile(filepath.Join(dir, "go"), []byte(body), 0o755)
}

func runGoWrap(args []string) int {
	wrapDir := os.Getenv("CARDEX_GO_WRAP_DIR")
	bin, err := findRealGo(os.Getenv("CARDEX_GO_WRAP_PATH"), wrapDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cardex:", err)
		return 1
	}
	env := os.Environ()
	if goCommand(args) == "test" {
		env = AcceptanceEnv(env)
	}
	env = dropExact(env, "CARDEX_GO_WRAP_PATH", "CARDEX_GO_WRAP_DIR")
	env = removePathDir(env, wrapDir)
	return execProcess(bin, append([]string{bin}, args...), env)
}

// goCommand returns the go subcommand, skipping a leading -C dir.
// `go test` is the only command whose environment is cleaned.
func goCommand(args []string) string {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-C" || a == "--C":
			i += 2
			continue
		case strings.HasPrefix(a, "-C=") || strings.HasPrefix(a, "--C="):
			i++
			continue
		case strings.HasPrefix(a, "-"):
			return ""
		default:
			return a
		}
	}
	return ""
}

func findRealGo(path, wrapDir string) (string, error) {
	if path == "" {
		path = os.Getenv("PATH")
	}
	wrapDir = filepath.Clean(wrapDir)
	names := []string{"go"}
	if runtime.GOOS == "windows" {
		names = []string{"go.exe", "go.cmd", "go"}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || filepath.Clean(dir) == wrapDir {
			continue
		}
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			st, err := os.Stat(candidate)
			if err != nil || st.IsDir() {
				continue
			}
			if runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
				continue
			}
			return candidate, nil
		}
	}
	return "", fmt.Errorf("go not found on PATH")
}

func execProcess(bin string, argv, env []string) int {
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "cardex:", err)
	return 1
}

func dropExact(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		drop := false
		for _, want := range keys {
			if key == want {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

func removePathDir(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	dir = filepath.Clean(dir)
	out := append([]string{}, env...)
	for i, kv := range out {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.EqualFold(key, "PATH") {
			continue
		}
		var parts []string
		for _, p := range filepath.SplitList(val) {
			if p == "" || filepath.Clean(p) == dir {
				continue
			}
			parts = append(parts, p)
		}
		out[i] = key + "=" + strings.Join(parts, string(filepath.ListSeparator))
	}
	return out
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		k, val, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, key) {
			return val
		}
	}
	return ""
}

func setEnv(env []string, key, val string) []string {
	prefixed := key + "="
	for i, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && k == key {
			env[i] = prefixed + val
			return env
		}
	}
	return append(env, prefixed+val)
}

func prependPath(env []string, dir string) []string {
	sep := string(filepath.ListSeparator)
	for i, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.EqualFold(key, "PATH") {
			continue
		}
		if val == "" {
			env[i] = key + "=" + dir
		} else {
			env[i] = key + "=" + dir + sep + val
		}
		return env
	}
	return append(env, "PATH="+dir)
}

func shSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func winQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
