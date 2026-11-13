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
	"github.com/OttoPrua/Cardex/internal/sandbox"
)

// A card created with sandbox off runs its executor without the Cardex seatbelt,
// and the attempt records that it did.
func TestRunSandboxOffCard(t *testing.T) {
	sandbox.SkipDenialTest(t)
	home := homeOutsideTMP(t)
	target := filepath.Join(home, "outside-off.txt")
	_, id, _ := seedPreparedCard(t, home, model.RunnerFake, "", 1, func(c *model.Card) { c.Sandbox = "off" }, func(string) any {
		return map[string]any{"attempts": map[string]any{"0": map[string]any{
			"outside_write": target,
			"output":        "OFF-MARKER\n",
		}}}
	})
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	_, events := loadAttempt(t, home, id)
	ev, ok := eventType(events, "sandbox_skipped")
	if !ok || !strings.Contains(string(ev.Data), "sandbox off") {
		t.Fatalf("want sandbox_skipped with sandbox off, events:\n%s", eventData(t, events))
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("sandbox-off card could not write outside its worktree: %v", err)
		}
	}
}
