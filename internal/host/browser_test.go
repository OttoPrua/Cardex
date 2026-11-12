package host_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// fakeChrome writes a stand-in for Chrome: it records its argv and pids next
// to the profile, writes DevToolsActivePort, leaves a child running in its
// group, and then sleeps.
func fakeChrome(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-chrome")
	script := `#!/bin/sh
for a in "$@"; do case "$a" in --user-data-dir=*) dir="${a#--user-data-dir=}";; esac; done
printf '%s\n' "$@" > "$dir/../args"
sleep 301 &
echo $! > "$dir/../child.pid"
echo $$ > "$dir/../chrome.pid"
printf '45678\n/devtools/browser/fake-id\n' > "$dir/DevToolsActivePort"
exec sleep 300
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func gone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := host.SignalProcess(pid, 0); err == syscall.ESRCH {
			return true
		}
		// A zombie child of this test process still answers kill 0.
		var ws syscall.WaitStatus
		if p, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); p == pid {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func browserCard(c *model.Card) { c.Requires = []string{"browser"} }

// A browser card's executor gets the CDP endpoint of a Chrome the host
// started for it, and the host removes that Chrome and its profile afterwards.
func TestBrowserCardInjectsCDPAndCleansUp(t *testing.T) {
	t.Setenv("CARDEX_CHROME", fakeChrome(t))
	t.Setenv("NO_PROXY", "example.internal")
	home := t.TempDir()
	_, id, _ := seedPreparedCard(t, home, model.RunnerFake, "", 1, browserCard, func(string) any {
		return map[string]any{"attempts": map[string]any{"0": map[string]any{
			"echo_env": []string{"CARDEX_BROWSER_CDP", "CARDEX_BROWSER_CDP_WS", "NO_PROXY"},
		}}}
	})
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "attempts", id)
	logb, err := os.ReadFile(filepath.Join(dir, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(logb)
	for _, want := range []string{
		"CARDEX_BROWSER_CDP=http://127.0.0.1:45678\n",
		"CARDEX_BROWSER_CDP_WS=ws://127.0.0.1:45678/devtools/browser/fake-id\n",
		"NO_PROXY=example.internal,127.0.0.1,localhost\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("executor output missing %q:\n%s", want, out)
		}
	}
	args, err := os.ReadFile(filepath.Join(dir, "browser", "args"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1", "--headless=new", "--user-data-dir=" + filepath.Join(dir, "browser", "profile")} {
		if !strings.Contains(string(args), want+"\n") {
			t.Fatalf("chrome args missing %s:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "--no-sandbox") {
		t.Fatalf("chrome was given --no-sandbox:\n%s", args)
	}
	if !gone(readPID(t, filepath.Join(dir, "browser", "chrome.pid"))) || !gone(readPID(t, filepath.Join(dir, "browser", "child.pid"))) {
		t.Fatal("chrome process group still running after the attempt")
	}
	if _, err := os.Stat(filepath.Join(dir, "browser", "profile")); !os.IsNotExist(err) {
		t.Fatalf("profile dir not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "browser.pgid")); !os.IsNotExist(err) {
		t.Fatalf("browser.pgid not removed: %v", err)
	}
	_, events := loadAttempt(t, home, id)
	for _, kind := range []string{"browser_start", "browser_stop", "host_finish"} {
		if _, ok := eventType(events, kind); !ok {
			t.Fatalf("missing %s event:\n%s", kind, eventData(t, events))
		}
	}
}

// A Chrome that never comes up fails the attempt before the executor starts.
func TestBrowserCardChromeFailsToStart(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad-chrome")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_CHROME", bad)
	home := t.TempDir()
	marker := filepath.Join(home, "executor-ran")
	_, id, _ := seedPreparedCard(t, home, model.RunnerFake, "", 1, browserCard, func(string) any {
		return map[string]any{"attempts": map[string]any{"0": map[string]any{
			"write_files": []map[string]string{{"path": marker, "content": "x"}},
		}}}
	})
	if err := host.Run(context.Background(), home, id); err == nil {
		t.Fatal("want an error when chrome cannot start")
	}
	a, events := loadAttempt(t, home, id)
	if a.Exit == nil || *a.Exit != 127 || !strings.Contains(a.ErrorExcerpt, "chrome exited") {
		t.Fatalf("attempt %+v", a)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("executor ran although chrome did not start")
	}
	if _, ok := eventType(events, "browser_start"); !ok {
		t.Fatalf("missing browser_start error event:\n%s", eventData(t, events))
	}
}

// Cards without the browser requirement never start Chrome.
func TestNonBrowserCardStartsNoChrome(t *testing.T) {
	t.Setenv("CARDEX_CHROME", "/nonexistent/chrome")
	home := t.TempDir()
	_, id, _ := seedPreparedCard(t, home, model.RunnerFake, "", 1, nil, func(string) any {
		return map[string]any{"attempts": map[string]any{"0": map[string]any{
			"echo_env": []string{"CARDEX_BROWSER_CDP"},
		}}}
	})
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	logb, _ := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if !strings.Contains(string(logb), "CARDEX_BROWSER_CDP=\n") {
		t.Fatalf("non-browser card got a CDP endpoint:\n%s", logb)
	}
	_, events := loadAttempt(t, home, id)
	if _, ok := eventType(events, "browser_start"); ok {
		t.Fatal("non-browser card started a browser")
	}
}

// Reconcile kills the Chrome group of an attempt whose host died and removes
// its profile.
func TestReconcileReapsLostBrowser(t *testing.T) {
	home := t.TempDir()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	deadPID := 0
	for pid := 1_000_000; pid < 1_000_400; pid++ {
		if host.SignalProcess(pid, 0) == syscall.ESRCH {
			deadPID = pid
			break
		}
	}
	a := model.Attempt{ID: "lost", Card: "c1", Route: model.Route{Channel: "ch1", Model: "fake"}, PID: deadPID, Worktree: filepath.Join(home, "work")}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "attempts", "lost")
	profile := filepath.Join(dir, "browser", "profile")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	defer func() { _ = host.SignalGroup(cmd.Process.Pid, syscall.SIGKILL) }()
	if err := os.WriteFile(filepath.Join(dir, "browser.pgid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := host.Reconcile(ctx, home); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("lost attempt's chrome group still running after reconcile")
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("profile not removed: %v", err)
	}
}

// With a real Chrome (CARDEX_BROWSER_E2E=1): the host-started Chrome answers
// /json/version on its loopback port, has no --no-sandbox, and is gone after
// the attempt.
func TestBrowserCardRealChrome(t *testing.T) {
	if os.Getenv("CARDEX_BROWSER_E2E") != "1" {
		t.Skip("set CARDEX_BROWSER_E2E=1 to start a real Chrome")
	}
	home := t.TempDir()
	_, id, _ := seedPreparedCard(t, home, model.RunnerFake, "", 1, browserCard, func(string) any {
		return map[string]any{"attempts": map[string]any{"0": map[string]any{
			"echo_env": []string{"CARDEX_BROWSER_CDP"},
			"sleep":    "3s",
		}}}
	})
	errc := make(chan error, 1)
	go func() { errc <- host.Run(context.Background(), home, id) }()
	var endpoint string
	deadline := time.Now().Add(30 * time.Second)
	for endpoint == "" && time.Now().Before(deadline) {
		db, err := store.Open(filepath.Join(home, "cardex.db"))
		if err == nil {
			events, _ := db.ListEvents(context.Background(), "attempt:"+id)
			_ = db.Close()
			for _, ev := range events {
				if ev.Type != "browser_start" {
					continue
				}
				var d struct {
					CDP   string `json:"cdp"`
					Error string `json:"error"`
				}
				_ = json.Unmarshal(ev.Data, &d)
				if d.Error != "" {
					t.Fatalf("browser_start: %s", d.Error)
				}
				endpoint = d.CDP
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if endpoint == "" {
		t.Fatal("no browser_start event")
	}
	resp, err := http.Get(endpoint + "/json/version")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "webSocketDebuggerUrl") {
		t.Fatalf("/json/version: %s", body)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(endpoint + "/json/version"); err == nil {
		t.Fatal("chrome still answering after the attempt")
	}
}
