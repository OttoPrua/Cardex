package host_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// Canceling the card stops a running executor instead of letting it run on.
func TestRunStopsWhenCardCanceled(t *testing.T) {
	defer host.SetCancelPollForTest(50 * time.Millisecond)()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\necho started\nsleep 30\necho finished\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_API_KEY", "k")
	home, id, _ := seedPrepared(t, t.TempDir(), model.RunnerClaude, "kimi", 1, nil)

	go func() {
		// Cancel only once the executor has started; a fixed delay raced the
		// seatbelt startup on a loaded Mac.
		logPath := filepath.Join(home, "attempts", id, "output.log")
		for i := 0; i < 200; i++ {
			if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "started") {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		db, err := store.Open(filepath.Join(home, "cardex.db"))
		if err != nil {
			return
		}
		defer db.Close()
		ctx := context.Background()
		c, err := db.GetCard(ctx, "c1")
		if err != nil {
			return
		}
		c.Status = model.CardCanceled
		_ = db.UpdateCard(ctx, &c)
	}()

	start := time.Now()
	_ = host.Run(context.Background(), home, id)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("executor kept running after cancel: %s", took)
	}
	logb, _ := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if !strings.Contains(string(logb), "started") {
		t.Fatalf("executor never ran:\n%s", logb)
	}
	if strings.Contains(string(logb), "finished") {
		t.Fatalf("executor finished despite cancel:\n%s", logb)
	}
}
