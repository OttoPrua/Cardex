package proxy

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func envMap(list []string) map[string]string {
	out := map[string]string{}
	for _, kv := range list {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

func TestNoSectionChangesNothing(t *testing.T) {
	home := writeConfig(t, `{"engines":{}}`)
	env, err := ChildEnv(home, "codex")
	if err != nil || env != nil {
		t.Fatalf("env=%v err=%v", env, err)
	}
	if env, err := ChildEnv(t.TempDir(), "codex"); err != nil || env != nil {
		t.Fatalf("missing file: env=%v err=%v", env, err)
	}
}

func TestDefaultAndOverrides(t *testing.T) {
	home := writeConfig(t, `{"proxy":{"url":"http://127.0.0.1:6152","socks":"socks5://127.0.0.1:6153",
		"channels":{"kimi":"direct","agy":"http://10.0.0.2:7890","grok":"socks5h://10.0.0.3:1080"}}}`)
	def, err := ChildEnv(home, "codex")
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(def)
	if m["HTTPS_PROXY"] != "http://127.0.0.1:6152" || m["https_proxy"] != "http://127.0.0.1:6152" {
		t.Fatalf("https: %v", m)
	}
	if m["ALL_PROXY"] != "socks5://127.0.0.1:6153" {
		t.Fatalf("all: %v", m)
	}
	if !strings.Contains(m["NO_PROXY"], "100.64.0.0/10") || !strings.Contains(m["no_proxy"], "localhost") {
		t.Fatalf("no_proxy default: %v", m)
	}

	kimi, _ := ChildEnv(home, "kimi")
	for k, v := range envMap(kimi) {
		if v != "" {
			t.Fatalf("direct must clear %s, got %q", k, v)
		}
	}
	if len(kimi) != 8 {
		t.Fatalf("direct must set all 8 keys, got %v", kimi)
	}

	agy := envMap(mustEnv(t, home, "agy"))
	if agy["HTTPS_PROXY"] != "http://10.0.0.2:7890" || agy["ALL_PROXY"] != "http://10.0.0.2:7890" {
		t.Fatalf("agy override: %v", agy)
	}
	grok := envMap(mustEnv(t, home, "grok"))
	if grok["ALL_PROXY"] != "socks5h://10.0.0.3:1080" || grok["HTTPS_PROXY"] != "socks5h://10.0.0.3:1080" {
		t.Fatalf("grok socks override: %v", grok)
	}
}

func mustEnv(t *testing.T, home, ch string) []string {
	t.Helper()
	env, err := ChildEnv(home, ch)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestInvalidConfigFailsLoudly(t *testing.T) {
	for _, body := range []string{
		`{"proxy":{}}`,
		`{"proxy":{"url":"127.0.0.1:6152"}}`,
		`{"proxy":{"url":"ftp://h:1"}}`,
		`{"proxy":{"url":"http://h:1","channels":{"kimi":"drect"}}}`,
	} {
		if _, err := ChildEnv(writeConfig(t, body), "x"); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestApplyProcess(t *testing.T) {
	home := writeConfig(t, `{"proxy":{"url":"http://127.0.0.1:6152","no_proxy":["a.local"]}}`)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(k, "before")
	}
	if err := ApplyProcess(home); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("https_proxy") != "http://127.0.0.1:6152" || os.Getenv("NO_PROXY") != "a.local" {
		t.Fatalf("got %q %q", os.Getenv("https_proxy"), os.Getenv("NO_PROXY"))
	}
}

func TestCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := Config{URL: "http://" + ln.Addr().String(), Channels: map[string]string{"kimi": Direct, "agy": "http://127.0.0.1:1"}}
	res := cfg.Check(time.Second)
	if res[cfg.URL] != nil {
		t.Fatalf("listening proxy reported %v", res[cfg.URL])
	}
	if res["http://127.0.0.1:1"] == nil {
		t.Fatal("closed port reported ok")
	}
	if _, ok := res[Direct]; ok {
		t.Fatal("direct must not be dialed")
	}
}
