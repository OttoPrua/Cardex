package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyShowAndCheck(t *testing.T) {
	home := t.TempDir()
	var out, errb bytes.Buffer
	if code := Main([]string{"--home", home, "proxy", "show"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "no proxy section") {
		t.Fatalf("unconfigured: %d %q %q", code, out.String(), errb.String())
	}
	cfg := `{"proxy":{"url":"http://127.0.0.1:1","channels":{"kimi":"direct"}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(k, "")
	}
	out.Reset()
	if code := Main([]string{"--home", home, "proxy", "show"}, &out, &errb); code != 0 {
		t.Fatalf("show: %d %q", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "(default)") || !strings.Contains(s, "http http://127.0.0.1:1") || !strings.Contains(s, "kimi") || !strings.Contains(s, "direct") {
		t.Fatalf("show output:\n%s", s)
	}
	if os.Getenv("HTTPS_PROXY") != "http://127.0.0.1:1" {
		t.Fatalf("process default not applied: %q", os.Getenv("HTTPS_PROXY"))
	}
	out.Reset()
	if code := Main([]string{"--home", home, "proxy", "check"}, &out, &errb); code != 1 {
		t.Fatalf("closed port must fail check: %d %s", code, out.String())
	}
}
