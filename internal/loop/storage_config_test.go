package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageDirKeysAreKnown(t *testing.T) {
	home := t.TempDir()
	body := `{
  "backups_dir": "/tmp/cardex-bak",
  "attempts_dir": "/tmp/cardex-attempts",
  "scratch_dir": "/tmp/cardex-scratch",
  "other": 1
}
`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(LoadConfig(home).Warnings, "\n")
	for _, key := range []string{"backups_dir", "attempts_dir", "scratch_dir"} {
		if strings.Contains(text, "unknown config key "+key) {
			t.Fatalf("known key warned: %s", text)
		}
	}
	if !strings.Contains(text, "unknown config key other") {
		t.Fatalf("warnings %s", text)
	}
}
