package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/quota"
)

func TestChannelImportUsage(t *testing.T) {
	home := t.TempDir()
	mustHome(t, home, "channel", "set", "--id", "sub", "--runner", "codex")
	before := mustHome(t, home, "--json", "channel", "show", "sub")
	input := filepath.Join(t.TempDir(), "usage.json")
	now := time.Now().UTC().Truncate(time.Second)
	raw := fmt.Sprintf(`[{"provider":"codex","source":"codex-cli","account":"secret-sentinel","diagnostic":"secret-sentinel","usage":{"updatedAt":%q,"primary":{"usedPercent":25,"windowMinutes":300}}}]`, now.Format(time.RFC3339))
	if err := os.WriteFile(input, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	out := mustHome(t, home, "--json", "channel", "import-usage", "--id", "sub", "--provider", "codex", "--input", input)
	if !strings.Contains(out, `"state": "known"`) || strings.Contains(out, "secret-sentinel") {
		t.Fatal(out)
	}
	s, err := quota.ReadSubscription(home, "", "sub", now)
	if err != nil || s == nil || *s.Windows[0].Remaining != .75 {
		t.Fatalf("%+v %v", s, err)
	}
	after := mustHome(t, home, "--json", "channel", "show", "sub")
	if before != after {
		t.Fatal("import changed channel configuration/local quota")
	}
	for _, args := range [][]string{
		{"--id", "sub", "--provider", "codex"},
		{"--id", "missing", "--provider", "codex", "--input", input},
		{"--id", "sub", "--provider", "claude", "--input", input},
	} {
		code, _, _ := callCLI(t, append([]string{"--home", home, "channel", "import-usage"}, args...))
		if code == 0 {
			t.Fatal("accepted incomplete or mismatched import")
		}
	}
	if err := os.WriteFile(input, []byte(`[{"provider":"codex","source":"codex-cli","error":{"message":"secret-sentinel"}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	mustHome(t, home, "channel", "import-usage", "--id", "sub", "--provider", "codex", "--input", input)
	s, err = quota.ReadSubscription(home, "", "sub", now)
	if err != nil || s == nil || s.State != "unavailable" || len(s.Windows) != 0 {
		t.Fatal("provider failure retained previously known balance")
	}
}
