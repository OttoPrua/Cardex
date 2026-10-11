package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bootstrapMaterialFixture(t *testing.T) (string, *Config, *WorkflowRecord, *Task, *AttemptRecord, []byte, *bootstrapRecoveryAuthorization) {
	t.Helper()
	root, cfg, wf, tk, original, before := bootstrapBeforeNativeFixture(t)
	name := "same-materials"
	dir := filepath.Join(tk.Dir, "materials")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var materials []bootstrapMaterialCopy
	for _, name := range []string{"stage.md", "fixture.json"} {
		source, copyPath := filepath.Join(t.TempDir(), name), filepath.Join(dir, name)
		content := []byte("same frozen bytes for " + name)
		for _, p := range []string{source, copyPath} {
			if err := os.WriteFile(p, content, 0600); err != nil {
				t.Fatal(err)
			}
		}
		materials = append(materials, bootstrapMaterialCopy{Source: source, Copy: copyPath, SHA256: sha256Hex(string(content))})
	}
	base := fmt.Sprintf("[profiles.%s]\nextends = \"workspace\"\nread_write = [%q]\n", name, tk.Dir)
	oldText := base + fmt.Sprintf("read_only = [%q, %q]\n", materials[0].Source, materials[1].Source)
	snapshot := filepath.Join(t.TempDir(), "original-profile.toml")
	if err := os.WriteFile(snapshot, []byte(oldText), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := parseGrokSandboxTOML([]byte(oldText), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tk.Goal.SandboxProfile, tk.Goal.LauncherSandbox = name, name
	tk.Goal.SandboxProfileDigest = profileDigest(profiles[name])
	tk.Prompts = []string{fmt.Sprintf("Read %s and %s, preserving their bytes.", materials[0].Source, materials[1].Source)}
	contract := frozenGoalStageContract(wf, tk)
	tk.Goal.InputDigest = sha256Hex(contract)
	if err := writeGoalStageContract(root, wf.ID, contract); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	currentText := base + fmt.Sprintf("read_only = [%q]\n", dir)
	profilePath := filepath.Join(tk.Goal.GrokHome, "sandbox.toml")
	if err := os.WriteFile(profilePath, []byte(currentText), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err = parseGrokSandboxTOML([]byte(currentText), profilePath)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := loadBootstrapRecoveryAuthorization(mintBootstrapBeforeNativeAuthorization(t, tk, wf, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	auth.SandboxRepair = &bootstrapSandboxRepair{OriginalProfilePath: snapshot, OriginalProfileSHA256: sha256Hex(oldText), LaunchProfileDigest: profileDigest(profiles[name]), Materials: materials}
	pair := directoryBootstrapWarningPrefix + materials[0].Source + "\n" + realBootstrapErrorPrefix + name + realBootstrapErrorSuffix
	if err := writeBootstrapLauncherEvidence(root, bootstrapLauncherEvidence{Kind: bootstrapBeforeProviderKind, AttemptID: original.AttemptID, TaskID: tk.ID, ExitStatus: "1", RefusalOutput: pair}); err != nil {
		t.Fatal(err)
	}
	return root, cfg, wf, tk, original, before, auth
}

func TestBootstrapMaterialRepairBoundaries(t *testing.T) {
	for _, name := range []string{"same-bytes", "copy-drift", "source-drift", "extra-file", "symlink", "widened-ro", "changed-rw", "old-profile-drift", "new-profile-drift", "contract-drift"} {
		t.Run(name, func(t *testing.T) {
			root, _, wf, tk, original, before, auth := bootstrapMaterialFixture(t)
			fix := auth.SandboxRepair
			profilePath := filepath.Join(tk.Goal.GrokHome, "sandbox.toml")
			switch name {
			case "copy-drift":
				os.WriteFile(fix.Materials[0].Copy, []byte("changed"), 0600)
			case "source-drift":
				os.WriteFile(fix.Materials[0].Source, []byte("changed"), 0600)
			case "extra-file":
				os.WriteFile(filepath.Join(filepath.Dir(fix.Materials[0].Copy), "private"), []byte("extra"), 0600)
			case "symlink":
				os.Remove(fix.Materials[0].Copy)
				if err := os.Symlink(fix.Materials[0].Source, fix.Materials[0].Copy); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
			case "old-profile-drift":
				fix.OriginalProfileSHA256 = sha256Hex("wrong")
			case "new-profile-drift":
				fix.LaunchProfileDigest = sha256Hex("wrong")
			case "contract-drift":
				tk.Goal.InputDigest = sha256Hex("wrong")
			case "widened-ro", "changed-rw":
				raw, _ := os.ReadFile(profilePath)
				field := "read_only"
				if name == "changed-rw" {
					field = "read_write"
				}
				raw = []byte(strings.Replace(string(raw), field+" = [", fmt.Sprintf("%s = [%q, ", field, t.TempDir()), 1))
				os.WriteFile(profilePath, raw, 0600)
				p, err := parseGrokSandboxTOML(raw, profilePath)
				if err != nil {
					t.Fatal(err)
				}
				fix.LaunchProfileDigest = profileDigest(p[tk.Goal.SandboxProfile])
			}
			err := prepareBootstrapSandbox(root, wf, tk, auth)
			if name == "same-bytes" {
				if err != nil {
					t.Fatal(err)
				}
				envelope, err := os.ReadFile(tk.Goal.RecoveryLaunchContractPath)
				if err != nil {
					t.Fatal(err)
				}
				originalContract, _ := os.ReadFile(filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt"))
				if !strings.Contains(string(envelope), string(originalContract)) || sha256Hex(string(originalContract)) != tk.Goal.InputDigest {
					t.Fatal("original frozen contract changed")
				}
				for _, m := range fix.Materials {
					if !strings.Contains(string(envelope), m.Source) || !strings.Contains(string(envelope), m.Copy) || !strings.Contains(string(envelope), m.SHA256) {
						t.Fatal("missing usable mapping")
					}
				}
				if tk.Goal.LaunchSandboxSelector != tk.Goal.SandboxProfile || tk.Goal.RecoveryLaunchProfileDigest != fix.LaunchProfileDigest {
					t.Fatal("lost actual named profile")
				}
			} else if err == nil {
				t.Fatal("unsafe repair accepted")
			}
			if countBootstrapConsumeEvents(t, root, tk.ID) != 0 {
				t.Fatal("validation consumed recovery")
			}
			assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, before)
		})
	}
}

func TestBootstrapMaterialRecoveryKeepsNamedProfileAndSingleUse(t *testing.T) {
	root, cfg, wf, tk, original, before, auth := bootstrapMaterialFixture(t)
	argv := filepath.Join(t.TempDir(), "argv")
	cfg.GrokBuildBin = fakeGrokGoalBin(t, argv)
	saveGoalCfg(t, root, cfg)
	path := filepath.Join(t.TempDir(), "authorization.json")
	raw, _ := json.Marshal(auth)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	headlessGoalStdin(t)
	if err := runBootstrapRecoveryCLI(root, wf.ID, path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if dumpedArgValue(string(got), "--sandbox") != tk.Goal.SandboxProfile {
		t.Fatalf("wrong actual sandbox: %s", got)
	}
	after, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Goal.InputDigest != tk.Goal.InputDigest || after.Goal.SandboxProfileDigest != tk.Goal.SandboxProfileDigest || after.Goal.BudgetTokens != tk.Goal.BudgetTokens || after.Goal.HardTimeoutSec != tk.Goal.HardTimeoutSec {
		t.Fatal("frozen binding/allowance changed")
	}
	if after.Goal.RecoveryLaunchProfileDigest != auth.SandboxRepair.LaunchProfileDigest || after.Goal.RecoveryLaunchContractPath == "" {
		t.Fatal("actual launch mapping not recorded")
	}
	if countBootstrapConsumeEvents(t, root, tk.ID) != 1 {
		t.Fatal("recovery not consumed once")
	}
	if err := runBootstrapRecoveryCLI(root, wf.ID, path); err == nil {
		t.Fatal("authorization replay accepted")
	}
	assertOriginalAttemptUnchanged(t, root, tk.ID, original.AttemptID, before)
}
