package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Chrome that never writes DevToolsActivePort fails within the start
// timeout, and nothing is left running or on disk.
func TestLaunchBrowserStartTimeout(t *testing.T) {
	old := browserStartTimeout
	browserStartTimeout = 500 * time.Millisecond
	defer func() { browserStartTimeout = old }()
	bin := filepath.Join(t.TempDir(), "silent-chrome")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho $$ > \"$(dirname \"$0\")/pid\"\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_CHROME", bin)
	attemptDir := t.TempDir()
	_, err := launchBrowser(context.Background(), fileConfig{}, attemptDir)
	if err == nil || !strings.Contains(err.Error(), "DevToolsActivePort not written") {
		t.Fatalf("want start timeout, got %v", err)
	}
	pgid := readGroupFile(filepath.Join(filepath.Dir(bin), "pid"))
	if pgid > 1 && processGroupAlive(pgid) {
		t.Fatal("silent chrome still running after start timeout")
	}
	if _, err := os.Stat(filepath.Join(attemptDir, browserDirName, "profile")); !os.IsNotExist(err) {
		t.Fatalf("profile left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(attemptDir, browserGroupName)); !os.IsNotExist(err) {
		t.Fatalf("browser.pgid left behind: %v", err)
	}
}

// A retry of the same attempt starts from an empty profile.
func TestLaunchBrowserClearsOldProfile(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-chrome")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in --user-data-dir=*) d=\"${a#--user-data-dir=}\";; esac; done\nls \"$d\" > \"$d/../seen\"\nprintf '1234\\n/devtools/browser/x\\n' > \"$d/DevToolsActivePort\"\nexec sleep 300\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_CHROME", bin)
	attemptDir := t.TempDir()
	stale := filepath.Join(attemptDir, browserDirName, "profile", "Cookies")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := launchBrowser(context.Background(), fileConfig{}, attemptDir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	seen, _ := os.ReadFile(filepath.Join(attemptDir, browserDirName, "seen"))
	if strings.Contains(string(seen), "Cookies") {
		t.Fatalf("old profile reused: %s", seen)
	}
}

func TestBrowserEnvDropsSecrets(t *testing.T) {
	env := browserEnv([]string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "HTTPS_PROXY=http://p", "ANTHROPIC_API_KEY=sk", "GITHUB_TOKEN=x", "KIMI_API_KEY=k"})
	got := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "HTTPS_PROXY=http://p"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %v", want, env)
		}
	}
	for _, bad := range []string{"API_KEY", "TOKEN"} {
		if strings.Contains(got, bad) {
			t.Fatalf("%s leaked: %v", bad, env)
		}
	}
}
