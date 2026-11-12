package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Browser cards.
//
// A card with `--require browser` gets a Chrome that the host starts before
// the executor and outside the Cardex seatbelt. macOS seatbelt profiles do
// not nest, so a Chrome started inside the executor's sandbox cannot set up
// its own sandbox and crashes (target_crashed). Started here, Chrome keeps
// its own sandbox on; it is never given --no-sandbox.
//
// The executor reaches it over the Chrome DevTools Protocol:
//
//	CARDEX_BROWSER_CDP     http://127.0.0.1:<port>   (Playwright connectOverCDP, Puppeteer browserURL)
//	CARDEX_BROWSER_CDP_WS  ws://127.0.0.1:<port>/devtools/browser/<id>
//
// Each attempt gets its own Chrome: a random port bound to 127.0.0.1 only
// (--remote-debugging-port=0) and a throwaway user data dir under
// attempts/<id>/browser/profile. Chrome runs in its own process group, which
// is written to attempts/<id>/browser.pgid. When the attempt ends the host
// kills that group and removes the profile dir; reconcile does the same for a
// host that died.
//
// Upgrade point (launchServer): v1 exposes a raw CDP port, so any local
// process that finds the port can drive this Chrome while the card runs. To
// move to a Playwright browser server (launchServer, ws endpoint with a
// random path), replace launchBrowser's command and endpoint discovery and
// set CARDEX_BROWSER_WS instead; the env injection, browser.pgid and cleanup
// below stay the same.

const (
	browserCapability = "browser"
	browserGroupName  = "browser.pgid"
	browserDirName    = "browser"
)

// browserStartTimeout bounds how long the host waits for DevToolsActivePort.
var browserStartTimeout = 30 * time.Second

func requiresBrowser(card model.Card) bool {
	for _, item := range card.Requires {
		if item == browserCapability {
			return true
		}
	}
	return false
}

type browserSession struct {
	cmd     *exec.Cmd
	pgid    int
	dir     string // attempts/<id>/browser
	profile string // attempts/<id>/browser/profile
	httpURL string
	wsURL   string
	done    chan struct{}
}

// Env is what the executor gets. base is the env built so far; loopback is
// added to NO_PROXY so a channel proxy does not swallow the CDP connection.
func (b *browserSession) Env(base []string) []string {
	out := []string{
		"CARDEX_BROWSER_CDP=" + b.httpURL,
		"CARDEX_BROWSER_CDP_WS=" + b.wsURL,
	}
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		out = append(out, key+"="+withLoopback(lookupEnv(base, key)))
	}
	return out
}

// lookupEnv returns key's last value in env, else the host's own.
func lookupEnv(env []string, key string) string {
	val, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val, found = v, true
		}
	}
	if !found {
		val = os.Getenv(key)
	}
	return val
}

func withLoopback(list string) string {
	have := map[string]bool{}
	var parts []string
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			have[p] = true
			parts = append(parts, p)
		}
	}
	for _, p := range []string{"127.0.0.1", "localhost"} {
		if !have[p] {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ",")
}

// chromePath finds the Chrome binary: CARDEX_CHROME, then config
// browser.chrome, then the usual install locations.
func chromePath(cfg fileConfig) (string, error) {
	if p := strings.TrimSpace(os.Getenv("CARDEX_CHROME")); p != "" {
		return p, nil
	}
	if p := strings.TrimSpace(cfg.Browser.Chrome); p != "" {
		return p, nil
	}
	var candidates []string
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("browser: no Chrome found (set CARDEX_CHROME or browser.chrome in config.json)")
}

// browserArgs is the fixed v1 command line. No --no-sandbox, ever.
func browserArgs(profile string) []string {
	return []string{
		"--headless=new",
		"--remote-debugging-port=0",
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--password-store=basic",
		"--use-mock-keychain",
		"about:blank",
	}
}

// launchBrowser starts Chrome for one attempt and waits for its CDP endpoint.
// On any error nothing is left running.
func launchBrowser(ctx context.Context, cfg fileConfig, attemptDir string) (*browserSession, error) {
	bin, err := chromePath(cfg)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(attemptDir, browserDirName)
	profile := filepath.Join(dir, "profile")
	// A retry of the same attempt id starts from an empty profile.
	_ = os.RemoveAll(profile)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "chrome.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	args := browserArgs(profile)
	cmd := exec.Command(bin, args...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("browser: start %s: %w", bin, err)
	}
	_ = logf.Close()
	b := &browserSession{cmd: cmd, pgid: cmd.Process.Pid, dir: dir, profile: profile, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(b.done)
	}()
	if err := persistGroupFile(filepath.Join(attemptDir, browserGroupName), b.pgid); err != nil {
		b.Stop()
		return nil, err
	}
	port, wsPath, err := waitDevToolsPort(ctx, profile, b.done)
	if err != nil {
		b.Stop()
		return nil, fmt.Errorf("browser: %w (see %s)", err, filepath.Join(dir, "chrome.log"))
	}
	b.httpURL = "http://127.0.0.1:" + strconv.Itoa(port)
	b.wsURL = "ws://127.0.0.1:" + strconv.Itoa(port) + wsPath
	return b, nil
}

// waitDevToolsPort reads profile/DevToolsActivePort, which Chrome writes once
// the debugging server is listening: line 1 is the port, line 2 the browser
// target path.
func waitDevToolsPort(ctx context.Context, profile string, exited <-chan struct{}) (int, string, error) {
	path := filepath.Join(profile, "DevToolsActivePort")
	deadline := time.NewTimer(browserStartTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if port, ws, ok := readDevToolsPort(path); ok {
			return port, ws, nil
		}
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-exited:
			return 0, "", errors.New("chrome exited before its DevTools port was ready")
		case <-deadline.C:
			return 0, "", fmt.Errorf("DevToolsActivePort not written within %s", browserStartTimeout)
		case <-tick.C:
		}
	}
}

func readDevToolsPort(path string) (int, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		lines = append(lines, strings.TrimSpace(sc.Text()))
	}
	if len(lines) < 2 || lines[1] == "" {
		return 0, "", false
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port <= 0 || port > 65535 {
		return 0, "", false
	}
	return port, lines[1], true
}

// Stop kills Chrome's process group, waits briefly for it to go, and removes
// the throwaway profile. chrome.log stays with the attempt. Safe to call twice.
func (b *browserSession) Stop() {
	if b == nil {
		return
	}
	reapBrowser(b.pgid, b.profile)
	select {
	case <-b.done:
	case <-time.After(3 * time.Second):
	}
	_ = os.RemoveAll(b.profile)
	_ = os.Remove(filepath.Join(filepath.Dir(b.dir), browserGroupName))
}

func reapBrowser(pgid int, profile string) {
	if processGroupAlive(pgid) {
		_ = SignalGroup(pgid, syscall.SIGTERM)
		if !waitGroupGone(pgid, 2*time.Second) {
			killGroup(pgid)
			_ = waitGroupGone(pgid, 3*time.Second)
		}
	}
	if profile != "" {
		_ = os.RemoveAll(profile)
	}
}

// reapLostBrowser cleans up after a host that died: it kills the attempt's
// recorded Chrome group and removes its profile.
func reapLostBrowser(home, attemptID string) {
	attemptDir := filepath.Join(home, "attempts", attemptID)
	pgid := readGroupFile(filepath.Join(attemptDir, browserGroupName))
	if pgid <= 1 {
		return
	}
	reapBrowser(pgid, filepath.Join(attemptDir, browserDirName, "profile"))
	_ = os.Remove(filepath.Join(attemptDir, browserGroupName))
}
