package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWithSystemPath(t *testing.T) {
	cases := []struct{ in, goos, want string }{
		{"/opt/homebrew/bin:/usr/bin:/bin", "darwin", "/opt/homebrew/bin:/usr/bin:/bin:/usr/local/bin:/usr/sbin:/sbin"},
		{"/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "linux", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"},
		{"", "darwin", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"},
		{"/sbin/:/x", "darwin", "/sbin/:/x:/usr/local/bin:/usr/bin:/bin:/usr/sbin"},
		{`C:\x`, "windows", `C:\x`},
	}
	for _, c := range cases {
		if got := withSystemPath(c.in, c.goos); got != c.want {
			t.Errorf("withSystemPath(%q,%s)=%q want %q", c.in, c.goos, got, c.want)
		}
	}
}

func TestRuntimePathUsesOnlyConfiguredToolDirectories(t *testing.T) {
	home := t.TempDir()
	first := filepath.Join(home, "first")
	second := filepath.Join(home, "second")
	raw, err := json.Marshal(map[string][]string{"runtime_path": {first, second, first}})
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(home, "config.json")
	if err := os.WriteFile(f, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	inherited := filepath.Join(home, "unused")
	t.Setenv("PATH", inherited)
	if err := ensureRuntimePath([]string{"--home", home, "serve"}); err != nil {
		t.Fatal(err)
	}
	want := withSystemPath(strings.Join([]string{first, second}, string(os.PathListSeparator)), runtime.GOOS)
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("PATH=%q, expected %q", got, want)
	}
	if err := os.WriteFile(f, []byte(`{"runtime_path":["relative"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimePath([]string{"--home", home, "serve"}); err == nil {
		t.Fatal("accepted a working-directory-dependent PATH")
	}
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("invalid config changed PATH: %s", got)
	}
	if err := os.WriteFile(f, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", inherited)
	if err := ensureRuntimePath([]string{"--home", home, "serve"}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got != withSystemPath(inherited, runtime.GOOS) {
		t.Fatalf("unconfigured PATH changed: %s", got)
	}
}
