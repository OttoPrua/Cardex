package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestHarness(t *testing.T) {
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	if !strings.HasPrefix(h.Home, h.Root) {
		t.Fatalf("home %s", h.Home)
	}
	info, err := os.Stat(h.Home)
	h.NoError(err)
	if !info.IsDir() {
		t.Fatal("home is not a directory")
	}
	mod, err := os.ReadFile(filepath.Join(h.Repo, "go.mod"))
	h.NoError(err)
	if !strings.Contains(string(mod), "module example.com/fixture") {
		t.Fatalf("go.mod %s", mod)
	}
	script, err := os.ReadFile(filepath.Join(h.Repo, "test.sh"))
	h.NoError(err)
	if !strings.Contains(string(script), "go test") {
		t.Fatalf("test command %s", script)
	}
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = h.Repo
	out, err := cmd.CombinedOutput()
	h.NoError(err)
	if strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("git %s", out)
	}
	testCmd := exec.Command(filepath.Join(h.Repo, "test.sh"))
	testCmd.Dir = h.Repo
	if out, err = testCmd.CombinedOutput(); err != nil {
		t.Fatalf("test.sh: %v\n%s", err, out)
	}

	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	ch, err := h.RegisterFakeChannel("anthropic", model.TierT2, RemainingWindow{Ratio: 0.7, Reset: reset, Confidence: 0.4})
	h.NoError(err)
	got, err := h.DB.GetChannel(t.Context(), ch.ID)
	h.NoError(err)
	h.Equal(got.Family, "anthropic")
	h.Equal(got.Tier, model.TierT2)
	if len(got.QuotaWindows) != 1 || got.QuotaWindows[0].Remaining != 0.7 || !got.QuotaWindows[0].ResetAt.Equal(reset) || got.QuotaWindows[0].Confidence != 0.4 {
		t.Fatalf("window %+v", got.QuotaWindows)
	}

	start := time.Now()
	h.NoError(h.TickUntil(func() bool { return true }, time.Second))
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("already-true tick took %s", time.Since(start))
	}
	start = time.Now()
	err = h.TickUntil(func() bool { return false }, 40*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Fatalf("timeout returned in %s", time.Since(start))
	}
}
