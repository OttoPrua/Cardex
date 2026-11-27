package host_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestExecutorDropsHostBootstrap(t *testing.T) {
	t.Setenv("CARDEX_SCHED_HOST", "1")
	t.Setenv("CARDEX_SCHED_ATTEMPT", "from-parent")

	t.Run("fake", func(t *testing.T) {
		home, id, _ := seedPreparedCard(t, t.TempDir(), model.RunnerFake, "", 1, func(c *model.Card) {
			c.Sandbox = "off"
		}, func(string) any {
			return map[string]any{
				"attempts": map[string]any{
					"0": map[string]any{
						"echo_env": []string{
							"CARDEX_SCHED_HOST",
							"CARDEX_SCHED_ATTEMPT",
							"CARDEX_MANAGED_EXECUTOR",
							"CARDEX_FAKE_SCRIPT",
						},
					},
				},
			}
		})
		if err := host.Run(context.Background(), home, id); err != nil {
			t.Fatal(err)
		}
		log := readLog(t, home, id)
		assertExecutorEnv(t, log, true)
	})

	t.Run("engine-profile", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("engine profile fixture uses a POSIX shell executable")
		}
		bin := t.TempDir()
		stub := "#!/bin/sh\nprintf 'CARDEX_SCHED_HOST=%s\\nCARDEX_SCHED_ATTEMPT=%s\\nCARDEX_MANAGED_EXECUTOR=%s\\nBASE=%s\\n' \"$CARDEX_SCHED_HOST\" \"$CARDEX_SCHED_ATTEMPT\" \"$CARDEX_MANAGED_EXECUTOR\" \"$ANTHROPIC_BASE_URL\"\n"
		if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		home, id, _ := seedPreparedCard(t, t.TempDir(), model.RunnerClaude, "custom", 1, func(c *model.Card) {
			c.Sandbox = "off"
		}, nil)
		cfg := `{"engines":{"custom":{"base_url":"https://example.test/v1","auth_value":"token-value"}}}`
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := host.Run(context.Background(), home, id); err != nil {
			t.Fatal(err)
		}
		log := readLog(t, home, id)
		assertExecutorEnv(t, log, false)
		if !strings.Contains(log, "BASE=https://example.test/v1") {
			t.Fatalf("profile base URL missing\n%s", log)
		}
	})
}

func readLog(t *testing.T, home, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertExecutorEnv(t *testing.T, log string, wantScript bool) {
	t.Helper()
	if got := envValue(log, "CARDEX_SCHED_HOST"); got != "" {
		t.Fatalf("CARDEX_SCHED_HOST=%q leaked into the executor\n%s", got, log)
	}
	if got := envValue(log, "CARDEX_SCHED_ATTEMPT"); got != "" {
		t.Fatalf("CARDEX_SCHED_ATTEMPT=%q leaked into the executor\n%s", got, log)
	}
	if got := envValue(log, "CARDEX_MANAGED_EXECUTOR"); got != "1" {
		t.Fatalf("CARDEX_MANAGED_EXECUTOR=%q, want 1\n%s", got, log)
	}
	if wantScript && envValue(log, "CARDEX_FAKE_SCRIPT") == "" {
		t.Fatalf("runner script env was dropped\n%s", log)
	}
}

func envValue(log, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}
