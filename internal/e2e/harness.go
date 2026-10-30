package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/sched"
	"cardex/internal/store"
)

// Harness builds a temp git repo, a temp CARDEX_HOME, and drives sched.Loop.Tick.
type Harness struct {
	TB      testing.TB
	Root    string
	Home    string
	Repo    string
	TestCmd string
	DB      *store.Store
	Sched   sched.Loop
}

// New creates a temp CARDEX_HOME and a git repo with a small Go module.
func New(tb testing.TB) *Harness {
	tb.Helper()
	root := tb.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		tb.Fatal(err)
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	h := &Harness{
		TB:      tb,
		Root:    root,
		Home:    home,
		TestCmd: "./test.sh",
		DB:      db,
		Sched:   sched.Stub{},
	}
	if err := h.initRepo(); err != nil {
		tb.Fatal(err)
	}
	return h
}

func (h *Harness) initRepo() error {
	dir := filepath.Join(h.Root, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	if err := git("init"); err != nil {
		return err
	}
	files := map[string]string{
		"go.mod":         "module example.com/fixture\n\ngo 1.24\n",
		"answer.go":      "package fixture\n\nfunc Answer() int {\n\treturn 1\n}\n",
		"answer_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) {\n\tif Answer() != 1 {\n\t\tt.Fatal(Answer())\n\t}\n}\n",
		"test.sh":        "#!/bin/sh\ncd \"$(dirname \"$0\")\"\ngo test ./...\n",
	}
	for name, body := range files {
		mode := os.FileMode(0o644)
		if name == "test.sh" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			return err
		}
	}
	ident := []string{"-c", "user.name=cardex-fixture", "-c", "user.email=fixture@cardex.invalid", "-c", "commit.gpgsign=false"}
	if err := git(append(ident, "add", "--", "go.mod", "answer.go", "answer_test.go", "test.sh")...); err != nil {
		return err
	}
	if err := git(append(ident, "commit", "-m", "fixture")...); err != nil {
		return err
	}
	h.Repo = dir
	return nil
}

// RegisterFakeChannel records a channel with family, tier, and a remaining window.
func (h *Harness) RegisterFakeChannel(family string, tier model.Tier, window RemainingWindow) (model.Channel, error) {
	ch := model.Channel{
		Runner:      model.RunnerFake,
		Family:      family,
		Tier:        tier,
		Health:      model.HealthOK,
		Models:      []string{"fake"},
		SourceGroup: family,
		QuotaWindows: []model.QuotaWindow{{
			Name:       "5h",
			ResetRule:  "rolling",
			ResetAt:    window.Reset,
			Remaining:  window.Ratio,
			Confidence: window.Confidence,
		}},
	}
	if err := h.DB.CreateChannel(context.Background(), &ch); err != nil {
		return model.Channel{}, err
	}
	return ch, nil
}

// TickUntil calls Sched.Tick until cond is true or timeout elapses.
// A condition that is already true returns without waiting.
func (h *Harness) TickUntil(cond func() bool, timeout time.Duration) error {
	if cond == nil {
		return fmt.Errorf("nil condition")
	}
	if cond() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	timedOut := func() error {
		return fmt.Errorf("timeout after %s", timeout)
	}
	for {
		if cond() {
			return nil
		}
		if ctx.Err() != nil {
			return timedOut()
		}
		if h.Sched != nil {
			err := h.Sched.Tick(ctx)
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
				if cond() {
					return nil
				}
				return timedOut()
			}
		}
		if cond() {
			return nil
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			if cond() {
				return nil
			}
			return timedOut()
		case <-timer.C:
		}
	}
}

// Equal fails the test when got and want differ.
func (h *Harness) Equal(got, want any) {
	h.TB.Helper()
	if !reflect.DeepEqual(got, want) {
		h.TB.Fatalf("got %#v want %#v", got, want)
	}
}

// NoError fails the test when err is non-nil.
func (h *Harness) NoError(err error) {
	h.TB.Helper()
	if err != nil {
		h.TB.Fatal(err)
	}
}
