package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sidecarFixture() map[string]any {
	return map[string]any{"role": "design", "runner": "codex", "actual_runner": "codex", "identity": "fixture-reader", "session_id": "fixture-session", "read_only": true, "status": "completed", "result_path": "/fixture/result.md", "digest": strings.Repeat("a", 64), "input_identity": "fixture-original-input"}
}

func putSidecar(t *testing.T, root, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workflowsDir(root), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir(root), name), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowKnownSidecarsDoNotBlockAdmission(t *testing.T) {
	root, dir := workflowTestRoot(t)
	putSidecar(t, root, "fixture.design-receipt.json", sidecarFixture())
	putSidecar(t, root, "wf0101-0000-fixture.design-receipt.json", sidecarFixture())
	putSidecar(t, root, "fixture.retained-preimage-hashes.json", map[string]any{"meaning": "preimage only", "head": strings.Repeat("a", 40), "hashes": map[string]string{"src/f.go": strings.Repeat("b", 64)}})
	wf := initTestWorkflow(t, root, dir)
	cfg := workflowTestCfg(t, root)
	loaded, broken, err := scanWorkflows(root, cfg)
	if err != nil || len(loaded) != 1 || len(broken) != 0 {
		t.Fatalf("scan loaded=%d broken=%v err=%v", len(loaded), broken, err)
	}
	if err := auditWorkflowWriteDomains(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowSidecarCannotHideUnknownOrWorkflowClaim(t *testing.T) {
	for _, key := range []string{"id", "ID", "Repo", "Write_Domain", "sChEmA", "schema", "write_domain", "writer_engine", "mode", "module_id", "worktree", "repo", "goal", "reviewer_task_id", "design_lineage", "candidate", "status-ambiguous", "malformed-digest", "unknown-suffix"} {
		t.Run(key, func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			wf := initTestWorkflow(t, root, dir)
			cfg := workflowTestCfg(t, root)
			value := sidecarFixture()
			name := "fixture.design-receipt.json"
			switch key {
			case "status-ambiguous":
				value["status"] = "writing"
			case "malformed-digest":
				value["digest"] = "not-a-hash"
			case "unknown-suffix":
				name = "fixture.unknown.json"
			default:
				value[key] = nil
			}
			putSidecar(t, root, name, value)
			if err := auditWorkflowWriteDomains(root, cfg, wf); !errors.Is(err, errWorkflowWriteOverlap) {
				t.Fatalf("unknown/claim must block: %v", err)
			}
		})
	}
	t.Run("malformed-real-workflow", func(t *testing.T) {
		root, dir := workflowTestRoot(t)
		wf := initTestWorkflow(t, root, dir)
		cfg := workflowTestCfg(t, root)
		if err := os.WriteFile(workflowPath(root, "wf0101-0000-broken"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := auditWorkflowWriteDomains(root, cfg, wf); !errors.Is(err, errWorkflowWriteOverlap) {
			t.Fatalf("corrupt workflow must block: %v", err)
		}
	})
}

func TestWorkflowKnownSidecarsPreserveRealConflict(t *testing.T) {
	root, dir := workflowTestRoot(t)
	wf := initTestWorkflow(t, root, dir)
	cfg := workflowTestCfg(t, root)
	putSidecar(t, root, "fixture.design-receipt.json", sidecarFixture())
	other := *wf
	other.ID = "wf0101-0000-other"
	other.ModuleID = "other"
	other.WriteDomain.ID = "other"
	other.WriteDomain.Lineage = "other-lineage"
	if err := auditWorkflowWriteDomains(root, cfg, &other); !errors.Is(err, errWorkflowWriteOverlap) {
		t.Fatalf("real overlap must block: %v", err)
	}
}

func TestWorkflowSidecarCLI(t *testing.T) {
	bin := os.Getenv("CARDEX_SIDECAR_TEST_BINARY")
	if bin == "" {
		bin = buildCardexCLI(t)
	}
	root, dir := workflowTestRoot(t)
	putSidecar(t, root, "fixture.design-receipt.json", sidecarFixture())
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	args := []string{"workflow", "init", "-root", root, "-module", "sidecar-consumer", "-goal", "fixture source only", "-dir", dir, "-terminal-criteria", "fixture checks", "-write-paths", "internal/auth", "-engine", grokBuildRunnerName, "-max-rounds", "1"}
	if out, err := run(args...); err != nil {
		t.Fatalf("normal CLI init failed: %v %s", err, out)
	}
	if out, err := run("workflow", "list", "-root", root); err != nil || !strings.Contains(out, "sidecar-consumer") || strings.Contains(out, "损坏") {
		t.Fatalf("CLI list %v %s", err, out)
	}
	conflict := append([]string(nil), args...)
	for i := range conflict {
		if conflict[i] == "sidecar-consumer" {
			conflict[i] = "conflicting-consumer"
		}
	}
	if out, err := run(conflict...); err == nil || !strings.Contains(out, "overlap") {
		t.Fatalf("CLI true overlap allowed: %v %s", err, out)
	}
	putSidecar(t, root, "fixture.design-receipt.json", func() map[string]any {
		v := sidecarFixture()
		v["ID"] = nil
		v["Repo"] = "/fixture/repo"
		v["Write_Domain"] = map[string]any{"paths": []string{"internal/auth"}}
		return v
	}())
	unknown := append([]string(nil), args...)
	for i := range unknown {
		if unknown[i] == "sidecar-consumer" {
			unknown[i] = "unknown-consumer"
		}
		if unknown[i] == "internal/auth" {
			unknown[i] = "internal/billing"
		}
	}
	if out, err := run(unknown...); err == nil || !strings.Contains(out, "unloadable") {
		t.Fatalf("CLI disguised workflow allowed: %v %s", err, out)
	}
}

// Read-only guard verification against the original root; never persists cards.
func TestWorkflowOriginalRootAuditReadonly(t *testing.T) {
	root := os.Getenv("CARDEX_SIDECAR_ORIGINAL_ROOT")
	if root == "" {
		t.Skip("set CARDEX_SIDECAR_ORIGINAL_ROOT for an explicit read-only external-root audit")
	}
	cfg, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	records, broken, err := scanWorkflows(root, cfg)
	if err != nil || len(broken) != 0 {
		t.Fatalf("original scan broken=%v err=%v", broken, err)
	}
	for _, wf := range records {
		if workflowClaimsActive(wf) {
			if err := auditWorkflowWriteDomains(root, cfg, wf); err != nil {
				t.Fatalf("original active guard: %v", err)
			}
		}
	}
	t.Logf("original root %d records loaded, unknown and live conflict guards exercised read-only", len(records))
}

func TestWorkflowPreimageSidecarCannotHideClaim(t *testing.T) {
	for _, bad := range []string{"id", "extra", "bad-hash", "empty-head"} {
		t.Run(bad, func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			wf := initTestWorkflow(t, root, dir)
			cfg := workflowTestCfg(t, root)
			value := map[string]any{"meaning": "preimage", "head": strings.Repeat("a", 40), "hashes": map[string]string{"src/f.go": strings.Repeat("b", 64)}}
			switch bad {
			case "bad-hash":
				value["hashes"] = map[string]string{"src/f.go": "bad"}
			case "empty-head":
				value["head"] = ""
			default:
				value[bad] = nil
			}
			putSidecar(t, root, "fixture.retained-preimage-hashes.json", value)
			if err := auditWorkflowWriteDomains(root, cfg, wf); !errors.Is(err, errWorkflowWriteOverlap) {
				t.Fatalf("bad preimage must remain unknown: %v", err)
			}
		})
	}
}
