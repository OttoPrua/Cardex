package host

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/sandbox"
)

// TestBrowserCardSandboxSmoke is the real-machine check for browser cards:
// a Node/Playwright script runs inside the same Cardex seatbelt an executor
// gets, connects over CDP to the Chrome the host launched outside it, opens a
// local page and submits a form. For contrast the same script then tries to
// launch Chrome itself inside the seatbelt with Chrome's own sandbox on
// (Playwright adds --no-sandbox unless chromiumSandbox is true), which is what
// crashed before.
//
// Run on macOS with CARDEX_BROWSER_SMOKE_DIR pointing at a directory that has
// node_modules/playwright-core installed.
func TestBrowserCardSandboxSmoke(t *testing.T) {
	dir := os.Getenv("CARDEX_BROWSER_SMOKE_DIR")
	if dir == "" {
		t.Skip("set CARDEX_BROWSER_SMOKE_DIR to run the real Chrome + seatbelt smoke")
	}
	if runtime.GOOS != "darwin" {
		t.Log("not macOS: the seatbelt wrap is a no-op here")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	chrome, err := chromePath(fileConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Local page with a form, served by the test (outside the sandbox).
	var mu sync.Mutex
	var submitted []string
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<!doctype html><title>cardex smoke</title><form method="post" action="/submit"><input name="who" id="who"><button id="go" type="submit">Send</button></form>`)
		case "/submit":
			_ = r.ParseForm()
			mu.Lock()
			submitted = append(submitted, r.PostFormValue("who"))
			mu.Unlock()
			fmt.Fprintf(w, `<!doctype html><title>done</title><p id="result">got %s</p>`, r.PostFormValue("who"))
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	pageURL := "http://" + ln.Addr().String() + "/"

	script := `import { chromium } from 'playwright-core';
const mode = process.argv[2];
let browser;
try {
  if (mode === 'cdp') {
    browser = await chromium.connectOverCDP(process.env.CARDEX_BROWSER_CDP);
  } else {
    browser = await chromium.launch({ executablePath: process.env.SMOKE_CHROME, headless: true, chromiumSandbox: true, timeout: 20000 });
  }
  const ctx = browser.contexts()[0] ?? await browser.newContext();
  const page = await ctx.newPage();
  page.on('crash', () => { console.log('TARGET_CRASHED'); });
  await page.goto(process.env.SMOKE_URL, { timeout: 20000 });
  await page.fill('#who', 'cardex-' + mode);
  await Promise.all([page.waitForNavigation({ timeout: 20000 }), page.click('#go')]);
  const text = await page.textContent('#result');
  console.log('RESULT ' + text);
  console.log('SMOKE_OK ' + mode);
  await page.close();
} catch (e) {
  console.log('SMOKE_FAIL ' + mode + ' ' + String(e && e.message || e).split('\n')[0]);
  process.exitCode = 2;
} finally {
  if (browser && mode === 'cdp') { await browser.close().catch(() => {}); }
  if (browser && mode !== 'cdp') { await browser.close().catch(() => {}); }
}
`
	if err := os.WriteFile(filepath.Join(dir, "smoke.mjs"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	attemptDir := t.TempDir()
	b, err := launchBrowser(context.Background(), fileConfig{}, attemptDir)
	if err != nil {
		t.Fatal(err)
	}
	pgid := b.pgid
	defer b.Stop()
	if out, err := exec.Command("ps", "-o", "args=", "-g", strconv.Itoa(pgid)).CombinedOutput(); err == nil {
		if strings.Contains(string(out), "--no-sandbox") {
			t.Fatalf("chrome group has --no-sandbox:\n%s", out)
		}
		t.Logf("chrome group processes: %d", strings.Count(strings.TrimSpace(string(out)), "\n")+1)
	}

	runSandboxed := func(mode string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, "smoke.mjs", mode)
		cmd.Dir = dir
		cmd.Env = mergeEnv(os.Environ(), append(b.Env(os.Environ()), "SMOKE_URL="+pageURL, "SMOKE_CHROME="+chrome))
		wrapped, err := sandbox.Wrap(cmd, []string{dir})
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range sandbox.Consume(wrapped) {
			t.Logf("sandbox event %s: %s", ev.Type, ev.Data)
		}
		if runtime.GOOS == "darwin" && !strings.Contains(wrapped.Path, "sandbox-exec") {
			t.Fatalf("%s: not wrapped in sandbox-exec: %s", mode, wrapped.Path)
		}
		out, _ := wrapped.CombinedOutput()
		t.Logf("%s output:\n%s", mode, out)
		return string(out)
	}

	out := runSandboxed("cdp")
	if !strings.Contains(out, "SMOKE_OK cdp") || !strings.Contains(out, "RESULT got cardex-cdp") || strings.Contains(out, "TARGET_CRASHED") {
		t.Fatalf("sandboxed CDP smoke failed:\n%s", out)
	}
	mu.Lock()
	got := append([]string(nil), submitted...)
	mu.Unlock()
	if len(got) == 0 || got[0] != "cardex-cdp" {
		t.Fatalf("form submission not received: %v", got)
	}

	// Contrast: launching Chrome inside the seatbelt (the old way).
	old := runSandboxed("launch")
	t.Logf("in-sandbox launch with Chrome's sandbox on succeeded=%v", strings.Contains(old, "SMOKE_OK launch"))

	b.Stop()
	if processGroupAlive(pgid) {
		t.Fatal("chrome group alive after Stop")
	}
	if _, err := os.Stat(b.profile); !os.IsNotExist(err) {
		t.Fatalf("profile not removed: %v", err)
	}
}
