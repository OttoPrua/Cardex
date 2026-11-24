package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const goWrapArg = "-cardex-wrap-go"
const goRunArg = "-cardex-run-go-program"

func init() {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[1] {
	case goWrapArg:
		os.Exit(runGoWrap(os.Args[2:]))
	case goRunArg:
		if len(os.Args) < 3 {
			os.Exit(1)
		}
		env := prependPath(os.Environ(), os.Getenv("CARDEX_GO_WRAP_DIR"))
		os.Exit(execProcess(os.Args[2], os.Args[3:], env))
	}
}

// PrependGoTestWrap installs a go shim on PATH. The executor process keeps
// its own environment; the shim applies AcceptanceEnv only to `go test`.
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
	bin, err := findRealGo(os.Getenv("PATH"), os.Getenv("CARDEX_GO_WRAP_DIR"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "cardex:", err)
		return 1
	}
	env := os.Environ()
	subcommand, index := goCommand(args)
	switch subcommand {
	case "test":
		env = AcceptanceEnv(env)
	case "run":
		if useGoRunLauncher() {
			launcher, err := goRunLauncher()
			if err != nil {
				fmt.Fprintln(os.Stderr, "cardex:", err)
				return 1
			}
			// go run puts GOROOT/bin first on PATH for its program. Restore the
			// shim there, while letting an explicit -exec argument take precedence.
			args = slices.Insert(args, index+1, "-exec="+launcher)
		}
	}
	return execProcess(bin, args, env)
}

func useGoRunLauncher() bool {
	goos, goarch := os.Getenv("GOOS"), os.Getenv("GOARCH")
	if (goos != "" && goos != runtime.GOOS) || (goarch != "" && goarch != runtime.GOARCH) {
		return false
	}
	for _, flag := range strings.Fields(os.Getenv("GOFLAGS")) {
		name, _, _ := strings.Cut(flag, "=")
		if name == "-exec" || name == "--exec" {
			return false
		}
	}
	return true
}

func goRunLauncher() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Go's -exec parser accepts quotes around fields and has no quote escapes.
	if strings.ContainsAny(exe, " \t\r\n") {
		quote := "'"
		if strings.Contains(exe, quote) {
			quote = `"`
		}
		if strings.Contains(exe, quote) {
			return "", fmt.Errorf("cannot quote launcher path for go run -exec")
		}
		exe = quote + exe + quote
	}
	return exe + " " + goRunArg, nil
}

// goCommand returns the go subcommand and its position, skipping a leading -C dir.
func goCommand(args []string) (string, int) {
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
			return "", -1
		default:
			return a, i
		}
	}
	return "", -1
}

func findRealGo(path, wrapDir string) (string, error) {
	shim, _ := os.Stat(wrapDir)
	names := []string{"go"}
	if runtime.GOOS == "windows" {
		names = []string{"go.exe", "go.cmd", "go"}
	}
	for _, dir := range filepath.SplitList(path) {
		entry, err := os.Stat(dir)
		if err != nil || (shim != nil && os.SameFile(entry, shim)) {
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
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("go not found on PATH")
}

func execProcess(bin string, args, env []string) int {
	cmd := exec.Command(bin, args...)
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
