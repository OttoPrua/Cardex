package e2e

import (
	"context"
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
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = h.Repo
	out, err := cmd.CombinedOutput()
	h.NoError(err)
	rev := strings.TrimSpace(string(out))
	if rev == "" {
		t.Fatal("HEAD is empty")
	}
	for _, name := range []string{"go.mod", "answer_test.go"} {
		show := exec.Command("git", "cat-file", "-e", rev+":"+name)
		show.Dir = h.Repo
		if out, err = show.CombinedOutput(); err != nil {
			t.Fatalf("%s missing from %s: %v %s", name, rev, err, out)
		}
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
	h.Equal(got.Runner, model.RunnerFake)
	for _, prod := range model.AllRunners() {
		if got.Runner == prod {
			t.Fatalf("production runner %s", got.Runner)
		}
	}
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

	h.Sched = blockOnCancel{}
	done := make(chan error, 1)
	start = time.Now()
	go func() {
		done <- h.TickUntil(func() bool { return false }, 50*time.Millisecond)
	}()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("expected timeout from blocking tick")
		}
		if time.Since(start) > time.Second {
			t.Fatalf("blocking tick returned after %s", time.Since(start))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TickUntil hung while Tick blocked on context")
	}
}

type blockOnCancel struct{}

func (blockOnCancel) Tick(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
