package main

import (
	"context"
	"testing"
)

func TestTickLateCrossDirectoryWriterAtCandidateGate(t *testing.T) {
	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 1
	cfg.ClaudeBin = "/usr/bin/true"
	a := newTask(root, cfg, typeSequence, "candidate", t.TempDir(), []string{"p"}, 5)
	a.WriteDomain = &WriteDomain{ID: "audit-a", Lineage: "audit-a", Component: "ops", Paths: []string{"internal/auth"}, Resources: []ResourceClaim{{Kind: resourceDatabase, ID: "audit.shared"}}}
	if err := saveTask(root, a); err != nil {
		t.Fatal(err)
	}
	planted := false
	tickCandidateFilterHook = func(task *Task) {
		if task.ID != a.ID || planted {
			return
		}
		planted = true
		b := plantLiveWorkspaceWriter(t, root, "late-writer", t.TempDir())
		b.WriteDomain = &WriteDomain{ID: "audit-b", Lineage: "audit-b", Component: "ops", Paths: []string{"internal/auth"}, Resources: []ResourceClaim{{Kind: resourceDatabase, ID: "audit.shared"}}}
		if err := saveTask(root, b); err != nil {
			t.Fatal(err)
		}
		if !writerConflictsWithActive(a, reconstructLiveWriterClaims(root)) {
			t.Fatal("fixture missing fresh conflict")
		}
	}
	orig := tickRunTask
	defer func() { tickRunTask = orig; tickCandidateFilterHook = nil }()
	ran := false
	tickRunTask = func(ctx context.Context, r string, c *Config, tk *Task, v string) error {
		ran = true
		finishTickTask(r, tk)
		return nil
	}
	_ = tickFilterOpts(root, cfg, true, true, a.ID, true)
	if !planted {
		t.Fatal("hook did not run")
	}
	if ran {
		t.Fatal("late cross-directory shared-resource writer did not block dispatch")
	}
}
