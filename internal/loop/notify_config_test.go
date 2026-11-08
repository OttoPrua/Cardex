package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotifyReceiversKeyIsKnown(t *testing.T) {
	home := t.TempDir()
	body := `{
  "other": 1,
  "notify": {
    "fallback": "codex:thread-0001",
    "receivers": [{"id": "example-manager", "adapter": "hermes", "target": "profile-a/example-manager", "enabled": true}],
    "extra": 1
  }
}
`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(home)
	if cfg.NotifyFallback != "codex:thread-0001" {
		t.Fatalf("fallback %q", cfg.NotifyFallback)
	}
	text := strings.Join(cfg.Warnings, "\n")
	if strings.Contains(text, "receivers") {
		t.Fatalf("receivers warned: %s", text)
	}
	if !strings.Contains(text, "unknown notify key extra") || !strings.Contains(text, "unknown config key other") {
		t.Fatalf("warnings %s", text)
	}
}
