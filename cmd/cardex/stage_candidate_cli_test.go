package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func stageCandidateBin(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("CARDEX_CANDIDATE_BIN")); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("CARDEX_CANDIDATE_BIN=%s: %v", p, err)
		}
		return p
	}
	p := filepath.Join("..", "..", ".cardex-stage", "cardex")
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("stage candidate CLI not built")
	}
	return p
}

func TestStageCandidateCLIThreeConsumers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX candidate CLI fixtures")
	}
	bin := stageCandidateBin(t)
	for i := 0; i < 2; i++ {
		t.Run("ingest-done-closed", func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			wf := initTestWorkflow(t, root, dir)
			writer := doneClosedWriter(t, root, wf)
			commit, tree := commitOwnedSourceChange(t, dir)
			cmd := exec.Command(bin, "workflow", "freeze-candidate", wf.ID, "-root", root,
				"-commit", commit, "-tree", tree, "-changed-paths", "internal/auth")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("freeze: %v\n%s", err, out)
			}
			wf = reloadWF(t, root, wf.ID)
			writer, err = loadTask(root, writer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if writer.Status != statusDone {
				t.Fatalf("freeze rewrote done writer to %s", writer.Status)
			}
			receipt := writeDoneClosedReviewReceipt(t, dir, wf, writer)
			ing := exec.Command(bin, "workflow", "ingest-external-review", wf.ID, "-root", root, "-receipt", receipt)
			ingOut, err := ing.CombinedOutput()
			if err != nil {
				t.Fatalf("ingest: %v\n%s", err, ingOut)
			}
			if !strings.Contains(string(ingOut), "method=external_independent_local") {
				t.Fatalf("ingest output: %s", ingOut)
			}
			fresh, err := loadTask(root, writer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Status != statusDone {
				t.Fatalf("ingest rewrote done writer to %s", fresh.Status)
			}
		})
		t.Run("run-id-persist-reject", func(t *testing.T) {
			root := testRoot(t)
			t.Cleanup(func() { killLockPID(root) })
			countPath := filepath.Join(t.TempDir(), "calls")
			cfg := runTaskCfg(t, fakeClaudeStealLockBin(t, root, countPath))
			cfg.DrainRescanSec = 1
			if err := saveConfig(root, cfg); err != nil {
				t.Fatal(err)
			}
			tk := newTask(root, cfg, typeSequence, "candidate-persist-reject", t.TempDir(), []string{"p"}, 5)
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "run", "-root", root, tk.ID)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("candidate run-ID persist-reject must be nonzero, stdout=%q", out)
			}
			if strings.Contains(string(out), "完成") || strings.Contains(strings.ToLower(string(out)), "done") {
				t.Fatalf("candidate stdout reported done: %s", out)
			}
			got, lerr := loadTask(root, tk.ID)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if got.Status == statusDone {
				t.Fatalf("disk status=%s", got.Status)
			}
			raw, _ := os.ReadFile(countPath)
			if strings.TrimSpace(string(raw)) != "1" {
				t.Fatalf("provider count=%q", raw)
			}
		})
	}
}
