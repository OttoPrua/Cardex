package sched

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// TestNestedCardCommand is the process regression for task re-entry.
// A cardex binary started as the fake runner must be able to run version,
// and card add inside a managed executor must not create a card or a second host.
func TestNestedCardCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("nested process fixture uses a POSIX shell executable")
	}
	bin := buildCardex(t)
	home := t.TempDir()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ch := model.Channel{ID: "ch", Runner: model.RunnerFake, Health: model.HealthOK, Models: []string{"fake"}}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	claudeCh := model.Channel{ID: "ch-claude", Runner: model.RunnerClaude, Health: model.HealthOK, Models: []string{"fake"}}
	if err := db.CreateChannel(ctx, &claudeCh); err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "c", Objective: "o", Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardRunning, Sandbox: "off"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	seedHostAttempt(t, db, home, "a-ver", "ch", echoScript)
	seedHostAttempt(t, db, home, "a-host", "ch-claude", nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var problems []string
	note := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	// External management must not keep a marker inherited from a CardeX executor.
	code, stdout, stderr := runCardex(t, bin, managedClear("CARDEX_HOME="+home), "card", "add", "--id", "c-ext", "--objective", "o", "--title", "external", "--tier", "T2")
	if code != 0 || !strings.Contains(stdout, "c-ext") {
		note("external card add code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runCardex(t, bin, envWith("CARDEX_HOME="+home, "CARDEX_MANAGED_EXECUTOR=1"), "version")
	if code != 0 || !strings.Contains(stdout, "cardex") {
		note("managed version code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCardex(t, bin, envWith("CARDEX_HOME="+home, "CARDEX_MANAGED_EXECUTOR=1"), "card")
	if code != 0 || !strings.Contains(stdout, "card —") {
		note("managed card help code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCardex(t, bin, envWith("CARDEX_HOME="+home, "CARDEX_MANAGED_EXECUTOR=1"), "card", "list")
	if code != 0 || !strings.Contains(stdout, "c-ext") {
		note("managed card list code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	// The launch marker plus a later "host" token is still an ordinary CLI.
	before := countAttemptEvents(t, home, "a-ver", "host_start")
	code, stdout, stderr = runCardex(t, bin, managedClear(
		"CARDEX_HOME="+home,
		"CARDEX_SCHED_HOST=1",
		"CARDEX_SCHED_ATTEMPT=a-ver",
	), "version", "host", "a-ver")
	after := countAttemptEvents(t, home, "a-ver", "host_start")
	if code != 0 || !strings.Contains(stdout, "cardex") || after != before {
		note("version with a host token restarted host: code=%d host_start %d->%d stdout=%q stderr=%q", code, before, after, stdout, stderr)
	}
	code, stdout, stderr = runCardex(t, bin, managedClear(
		"CARDEX_HOME="+home,
		"CARDEX_SCHED_HOST=1",
		"CARDEX_SCHED_ATTEMPT=a-ver",
	), "card", "add", "--id", "c-pos", "--objective", "o", "--title", "host", "--tier", "T2")
	titled := countAttemptEvents(t, home, "a-ver", "host_start")
	if code != 0 || !strings.Contains(stdout, "c-pos") || titled != after || !cardExists(t, home, "c-pos") {
		note("title host restarted host or skipped card add: code=%d host_start %d->%d card=%v stdout=%q stderr=%q", code, after, titled, cardExists(t, home, "c-pos"), stdout, stderr)
	}
	code, stdout, stderr = runCardex(t, bin, managedClear(
		"CARDEX_HOME="+home,
		"CARDEX_SCHED_HOST=1",
		"CARDEX_SCHED_ATTEMPT=a-ver",
	), "host", "a-ver", "extra")
	extraHost := countAttemptEvents(t, home, "a-ver", "host_start")
	if code == 0 || extraHost != titled || !strings.Contains(stderr, "host <attempt-id>") {
		note("host with an extra arg used the launch shortcut: code=%d host_start %d->%d stdout=%q stderr=%q", code, titled, extraHost, stdout, stderr)
	}

	stub := writeNestedRunner(t, bin, filepath.Join(home, "attempts", "a-host", "nested-guard"))
	hostOut, hostErr, hostErrRun := runHost(t, bin, home, "a-host", "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := readAttemptLog(t, home, "a-host")
	starts := countAttemptEvents(t, home, "a-host", "host_start")
	finishes := countAttemptEvents(t, home, "a-host", "host_finish")
	if hostErrRun != nil || starts != 1 || finishes != 1 || strings.Contains(log, "runner-reentered") {
		note("nested runner host err=%v starts=%d finishes=%d reentered=%v stdout=%q stderr=%q log=%s", hostErrRun, starts, finishes, strings.Contains(log, "runner-reentered"), hostOut, hostErr, log)
	}
	if envLine(log, "CARDEX_SCHED_HOST") != "" || envLine(log, "CARDEX_SCHED_ATTEMPT") != "" || envLine(log, "CARDEX_MANAGED_EXECUTOR") != "1" {
		note("nested runner env log=%s", log)
	}
	if !strings.Contains(log, "cardex ") || !strings.Contains(log, "version_exit=0") {
		note("nested version missing from runner output log=%s", log)
	}
	for _, exitLine := range []string{"nest_exit=2", "help_title_exit=2", "flag_title_exit=2"} {
		if !strings.Contains(log, exitLine) || !strings.Contains(log, "CardeX-managed executor") {
			note("nested card add %s missing log=%s", exitLine, log)
			break
		}
	}
	for _, id := range []string{"c-nest", "c-help", "c-flag"} {
		if cardExists(t, home, id) {
			note("nested card add created %s", id)
		}
	}

	if len(problems) > 0 {
		t.Fatalf("nested card command:\n%s", strings.Join(problems, "\n"))
	}
}

func buildCardex(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cardex")
	// Windows os/exec resolves an extensionless path through PATHEXT, so the
	// built CLI and the runner's calls to it must use the .exe name.
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/cardex")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build cardex: %v\n%s", err, out)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

var echoScript = []byte(`{"attempts":{"0":{"echo_env":["CARDEX_SCHED_HOST","CARDEX_SCHED_ATTEMPT","CARDEX_MANAGED_EXECUTOR","CARDEX_FAKE_SCRIPT"],"exit_code":0}}}`)

func seedHostAttempt(t *testing.T, db *store.Store, home, id, channel string, script []byte) {
	t.Helper()
	dir := filepath.Join(home, "attempts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if script != nil {
		if err := os.WriteFile(filepath.Join(dir, "script.json"), script, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hard-duration"), []byte("30s"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, "wt-"+id)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	attempt := model.Attempt{
		ID: id, Card: "c", Index: 0, Route: model.Route{Channel: channel, Model: "fake"},
		Worktree: work, PromptPath: filepath.Join(dir, "prompt.txt"),
	}
	if err := db.CreateAttempt(context.Background(), &attempt); err != nil {
		t.Fatal(err)
	}
}

func envWith(extra ...string) []string {
	base := envDrop(os.Environ(), envKeys(extra...)...)
	return append(base, extra...)
}

func managedClear(extra ...string) []string {
	return envDrop(envWith(extra...), "CARDEX_MANAGED_EXECUTOR")
}

func envKeys(extra ...string) []string {
	keys := make([]string, 0, len(extra))
	for _, kv := range extra {
		key, _, _ := strings.Cut(kv, "=")
		keys = append(keys, key)
	}
	return keys
}

func envDrop(base []string, keys ...string) []string {
	drop := map[string]bool{}
	for _, key := range keys {
		if key != "" {
			drop[key] = true
		}
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if ok && drop[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// writeNestedRunner is the executor the host starts. It calls the candidate
// binary under test, so version and card add are child processes of that run.
func writeNestedRunner(t *testing.T, bin, guard string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	body := fmt.Sprintf(`#!/bin/sh
guard=%s
if [ -f "$guard" ]; then
  echo runner-reentered
  exit 0
fi
echo 1 > "$guard"
printf 'CARDEX_SCHED_HOST=%%s\n' "${CARDEX_SCHED_HOST-}"
printf 'CARDEX_SCHED_ATTEMPT=%%s\n' "${CARDEX_SCHED_ATTEMPT-}"
printf 'CARDEX_MANAGED_EXECUTOR=%%s\n' "${CARDEX_MANAGED_EXECUTOR-}"
%s version
echo "version_exit=$?"
%s card add --id c-nest --objective o --title nested --tier T2
echo "nest_exit=$?"
%s card add --id c-help --objective o --title help --tier T2
echo "help_title_exit=$?"
%s card add --id c-flag --objective o --title --help --tier T2
echo "flag_title_exit=$?"
`, strconvQuote(guard), strconvQuote(bin), strconvQuote(bin), strconvQuote(bin), strconvQuote(bin))
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func strconvQuote(s string) string {
	return fmt.Sprintf("%q", s)
}

func runCardex(t *testing.T, bin string, env []string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%v timed out stdout=%q stderr=%q", args, stdout.String(), stderr.String())
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%v: %v stderr=%q", args, err, stderr.String())
		}
		code = exit.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

func runHost(t *testing.T, bin, home, id string, extra ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "host", id)
	cmd.Env = managedClear(append([]string{
		"CARDEX_HOME=" + home,
		"CARDEX_SCHED_HOST=1",
		"CARDEX_SCHED_ATTEMPT=" + id,
	}, extra...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.String(), stderr.String(), ctx.Err()
	}
	return stdout.String(), stderr.String(), err
}

func countAttemptEvents(t *testing.T, home, id, kind string) int {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.ListEvents(context.Background(), "attempt:"+id)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range events {
		if event.Type == kind {
			n++
		}
	}
	return n
}

func cardExists(t *testing.T, home, id string) bool {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.GetCard(context.Background(), id)
	return err == nil
}

func readAttemptLog(t *testing.T, home, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func envLine(log, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}
