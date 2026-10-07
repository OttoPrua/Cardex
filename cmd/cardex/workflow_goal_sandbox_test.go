package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testWriteSandboxProfile = "cardex-linked-git"

func dumpedArgValue(raw, flag string) string {
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	for i, line := range lines {
		if line == flag && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}

func dumpedHasFlag(raw, flag string) bool {
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		if line == flag {
			return true
		}
	}
	return false
}

func linkGitCommonDirOutside(t *testing.T, worktree string) string {
	t.Helper()
	common := filepath.Join(t.TempDir(), "common.git")
	src := filepath.Join(worktree, ".git")
	if err := os.Rename(src, common); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("gitdir: "+common+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(common, worktree+string(filepath.Separator)) {
		t.Fatalf("common-dir fixture must sit outside worktree: worktree=%s common=%s", worktree, common)
	}
	return common
}

func TestNativeGoalWriteSandboxDefaultWorkspace(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	argvPath := filepath.Join(t.TempDir(), "argv")
	enableManualGrok(t, root, cfg, argvPath)
	if strings.TrimSpace(cfg.GrokBuild.WriteSandboxProfile) != "" {
		t.Fatal("default opt-in must stay empty")
	}
	wf := initTestWorkflow(t, root, dir)
	admitManualWriter(t, root, cfg, wf)
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); err != nil {
		t.Fatalf("goal-run: %v", err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	got := dumpedArgValue(string(raw), "--sandbox")
	if got != grokBuildWriteSandboxDefault {
		t.Fatalf("empty opt-in must launch --sandbox %s, got %q\n%s", grokBuildWriteSandboxDefault, got, raw)
	}
	if dumpedArgValue(string(raw), "--permission-mode") != "auto" {
		t.Fatalf("write-capable permission must stay auto:\n%s", raw)
	}
}

func TestNativeGoalWriteSandboxUnsafeDoesNotLaunchOff(t *testing.T) {
	t.Parallel()
	cfg := grokBuildTestConfig(t, "/usr/bin/true")
	cfg.GrokBuild.WriteSandboxProfile = "off"
	task := &Task{Type: typeSequence, Dir: t.TempDir(), SessionID: "sess-unsafe", GrokModel: "grok-4.6",
		Goal: &TaskGoalBinding{GrokHome: t.TempDir()}}
	sandbox, perm, err := resolveManualGrokTuple(cfg, task)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox != grokBuildWriteSandboxDefault || perm != "auto" {
		t.Fatalf("unsafe in-memory profile must fail closed to workspace/auto, got %s/%s", sandbox, perm)
	}
	args, _, err := manualGoalCommandArgs(cfg, task)
	if err != nil {
		t.Fatal(err)
	}
	if argValue(args, "--sandbox") != grokBuildWriteSandboxDefault {
		t.Fatalf("shared Goal consumer must not launch as off: %q", args)
	}
	if argValue(args, "--permission-mode") != "auto" {
		t.Fatalf("permission must stay auto: %q", args)
	}
}

func TestNativeGoalWriteSandboxReadOnlyUnchanged(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := t.TempDir()
	cfg := grokBuildTestConfig(t, "/usr/bin/true")
	cfg.GrokBuild.WriteSandboxProfile = testWriteSandboxProfile
	cfg.GrokBuild.ReadOnlySandboxProfile = grokBuildReadOnlySandboxMacOSNoopNetwork
	if err := validateGrokBuild(cfg); err != nil {
		t.Fatal(err)
	}
	task := &Task{Type: typeReview, Dir: dir, SessionID: "sess-ro", GrokModel: "grok-4.6",
		Goal: &TaskGoalBinding{GrokHome: home}}
	sandbox, perm, err := resolveManualGrokTuple(cfg, task)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox != grokBuildReadOnlySandboxMacOSNoopNetwork || perm != "plan" {
		t.Fatalf("read-only Goal must keep configured read-only/plan, got %s/%s", sandbox, perm)
	}
	args, _, err := manualGoalCommandArgs(cfg, task)
	if err != nil {
		t.Fatal(err)
	}
	if argValue(args, "--sandbox") != grokBuildReadOnlySandboxMacOSNoopNetwork {
		t.Fatalf("write-profile opt-in must not change read-only argv: %q", args)
	}
	if argValue(args, "--permission-mode") != "plan" {
		t.Fatalf("read-only permission must stay plan: %q", args)
	}

	cross := &Task{Type: typeCrossCheck, Dir: dir, SkipPermissions: true, SessionID: "sess-cross",
		GrokModel: "grok-4.6", Goal: &TaskGoalBinding{GrokHome: home}}
	sandbox, perm, err = resolveManualGrokTuple(cfg, cross)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox != grokBuildReadOnlySandboxMacOSNoopNetwork || perm != "plan" {
		t.Fatalf("crosscheck must stay read-only/plan, got %s/%s", sandbox, perm)
	}
}

func TestNativeGoalWriteSandboxExplicitManualAndHostedArgv(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	argvPath := filepath.Join(t.TempDir(), "argv")
	enableManualGrok(t, root, cfg, argvPath)
	cfg.GrokBuild.WriteSandboxProfile = testWriteSandboxProfile
	saveGoalCfg(t, root, cfg)
	if err := validateGrokBuild(cfg); err != nil {
		t.Fatal(err)
	}
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); err != nil {
		t.Fatalf("manual goal-run: %v", err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if dumpedArgValue(string(raw), "--sandbox") != testWriteSandboxProfile {
		t.Fatalf("manual launch must forward --sandbox %s:\n%s", testWriteSandboxProfile, raw)
	}
	if dumpedArgValue(string(raw), "--permission-mode") != "auto" {
		t.Fatalf("manual launch permission must stay auto:\n%s", raw)
	}
	tk, err = loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dumpedArgValue(string(raw), "-s") != tk.SessionID && dumpedArgValue(string(raw), "--resume") != tk.SessionID {
		t.Fatalf("manual launch missing session binding %s:\n%s", tk.SessionID, raw)
	}

	hostedHome := t.TempDir()
	hosted := *tk
	goalCopy := *tk.Goal
	goalCopy.GrokHome = hostedHome
	hosted.Goal = &goalCopy
	hosted.SessionID = "hosted-sess-1"
	args, grokHome, err := manualGoalCommandArgs(cfg, &hosted)
	if err != nil {
		t.Fatal(err)
	}
	if argValue(args, "--sandbox") != testWriteSandboxProfile {
		t.Fatalf("hosted argv consumer must forward --sandbox %s: %q", testWriteSandboxProfile, args)
	}
	if argValue(args, "--permission-mode") != "auto" {
		t.Fatalf("hosted argv consumer permission must stay auto: %q", args)
	}
	if grokHome != hostedHome {
		t.Fatalf("hosted consumer GROK_HOME=%q want %q", grokHome, hostedHome)
	}
	if argValue(args, "-s") != hosted.SessionID && argValue(args, "--resume") != hosted.SessionID {
		t.Fatalf("hosted argv consumer missing session %s: %q", hosted.SessionID, args)
	}
	if argValue(args, "--cwd") != dir {
		t.Fatalf("hosted argv --cwd=%q want worktree %q", argValue(args, "--cwd"), dir)
	}
}

func TestNativeGoalWriteSandboxOutsideGitMetadataCwd(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	common := linkGitCommonDirOutside(t, dir)
	cfg := workflowTestCfg(t, root)
	argvPath := filepath.Join(t.TempDir(), "argv")
	enableManualGrok(t, root, cfg, argvPath)
	cfg.GrokBuild.WriteSandboxProfile = testWriteSandboxProfile
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	admitManualWriter(t, root, cfg, wf)
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); err != nil {
		t.Fatalf("goal-run: %v", err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	cwd := dumpedArgValue(string(raw), "--cwd")
	if cwd != dir {
		t.Fatalf("--cwd must remain the worktree %q, got %q (common-dir %q)\n%s", dir, cwd, common, raw)
	}
	if dumpedArgValue(string(raw), "--sandbox") != testWriteSandboxProfile {
		t.Fatalf("explicit profile missing with outside git metadata:\n%s", raw)
	}
	if cwd == common || strings.HasPrefix(cwd, common+string(filepath.Separator)) {
		t.Fatalf("--cwd must not switch to git common-dir %q", common)
	}
}

func TestNativeGoalWriteSandboxSessionPersistence(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	argvPath := filepath.Join(t.TempDir(), "argv")
	home := t.TempDir()
	enableManualGrok(t, root, cfg, argvPath)
	cfg.GrokBuild.WriteSandboxProfile = testWriteSandboxProfile
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)
	tk.Goal.GrokHome = home
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := runManualGoalLaunch(t, root, cfg, wf, 0); err != nil {
		t.Fatalf("goal-run: %v", err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	tk, err = loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.SessionID == "" {
		t.Fatal("launch must bind a session id")
	}
	if dumpedArgValue(string(raw), "-s") != tk.SessionID && dumpedArgValue(string(raw), "--resume") != tk.SessionID {
		t.Fatalf("manual argv missing session %s:\n%s", tk.SessionID, raw)
	}
	if dumpedHasFlag(string(raw), "--sandbox") && dumpedArgValue(string(raw), "--sandbox") != testWriteSandboxProfile {
		t.Fatalf("session launch must keep explicit sandbox:\n%s", raw)
	}
	if strings.TrimSpace(tk.Goal.GrokHome) != home {
		t.Fatalf("manual Goal.GrokHome=%q want %q", tk.Goal.GrokHome, home)
	}

	sessionDir := grokGoalSessionDir(home, dir, tk.SessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	args, grokHome, err := manualGoalCommandArgs(cfg, tk)
	if err != nil {
		t.Fatal(err)
	}
	if grokHome != home {
		t.Fatalf("hosted/manual consumer GROK_HOME=%q want %q", grokHome, home)
	}
	if argValue(args, "--resume") != tk.SessionID {
		t.Fatalf("existing session dir must bind --resume %s: %q", tk.SessionID, args)
	}
	if argValue(args, "--sandbox") != testWriteSandboxProfile {
		t.Fatalf("resume argv must keep explicit sandbox: %q", args)
	}
}

const testInvocationSandboxProfile = "linked-common-dir"

// preFixGoalRunFlagSet is an immutable copy of HEAD goal-run flags
// (root/manual/hosted/budget only). It is not the shipped parser.
func preFixGoalRunFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("workflow goal-run", flag.ContinueOnError)
	_ = fs.String("root", "", "数据目录")
	_ = fs.Bool("manual", false, "")
	_ = fs.Bool("hosted", false, "")
	_ = fs.Int64("budget", 0, "")
	return fs
}

func TestPrefixtureGoalRunHadNoOneLaunchSandboxSelector(t *testing.T) {
	t.Parallel()
	err := parseWorkflowFlags(preFixGoalRunFlagSet(), []string{"wf-id", "-manual", "-sandbox", testInvocationSandboxProfile})
	if err == nil {
		t.Fatal("pre-fix goal-run had no -sandbox selector")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("pre-fix gap must be unknown -sandbox, got %v", err)
	}
}

func writeGrokSandboxTOML(t *testing.T, path, name, extends string, readWrite []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[profiles.%s]\n", name)
	if extends != "" {
		fmt.Fprintf(&b, "extends = %q\n", extends)
	}
	if len(readWrite) > 0 {
		b.WriteString("read_write = [")
		for i, p := range readWrite {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", p)
		}
		b.WriteString("]\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupInvocationSandboxLaunch(t *testing.T) (root, dir, argvPath, home string, cfg *Config, wf *WorkflowRecord, tk *Task, cfgBefore []byte) {
	t.Helper()
	root, dir = workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	argvPath = filepath.Join(t.TempDir(), "argv")
	home = t.TempDir()
	enableManualGrok(t, root, cfg, argvPath)
	if strings.TrimSpace(cfg.GrokBuild.WriteSandboxProfile) != "" {
		t.Fatal("shared write_sandbox_profile must stay empty for one-launch selector")
	}
	common := linkGitCommonDirOutside(t, dir)
	writeGrokSandboxTOML(t, filepath.Join(home, "sandbox.toml"), testInvocationSandboxProfile, "workspace", []string{common})
	wf = initTestWorkflow(t, root, dir)
	tk = admitManualWriter(t, root, cfg, wf)
	tk.Goal.GrokHome = home
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	var err error
	cfgBefore, err = os.ReadFile(configPath(root))
	if err != nil {
		t.Fatal(err)
	}
	return root, dir, argvPath, home, cfg, wf, tk, cfgBefore
}

// TestGoalRunOneLaunchExistingSandboxProfile captures the pre-fix gap: goal-run
// only had root/manual/hosted/budget plus shared-root sandbox, so there was no
// safe one-launch choice of an already-valid applicable profile. After the fix,
// one cmdWorkflowGoalRun invocation selects that existing profile and the name
// reaches the dumped launch argv; shared config bytes stay unchanged.
func TestGoalRunOneLaunchExistingSandboxProfile(t *testing.T) {
	root, dir, argvPath, _, _, wf, tk, cfgBefore := setupInvocationSandboxLaunch(t)
	headlessGoalStdin(t)
	if err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual", "-sandbox", testInvocationSandboxProfile}); err != nil {
		t.Fatalf("one-launch existing profile selector: %v", err)
	}
	cfgAfter, err := os.ReadFile(configPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(cfgAfter) != string(cfgBefore) {
		t.Fatalf("persisted shared config bytes must stay unchanged\nbefore:\n%s\nafter:\n%s", cfgBefore, cfgAfter)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("launcher must exec (argv dump missing): %v", err)
	}
	if dumpedArgValue(string(raw), "--sandbox") != testInvocationSandboxProfile {
		t.Fatalf("existing applicable profile must reach launch argv --sandbox %s:\n%s", testInvocationSandboxProfile, raw)
	}
	if dumpedArgValue(string(raw), "--cwd") != dir {
		t.Fatalf("--cwd must stay worktree %q:\n%s", dir, raw)
	}
	loaded, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal == nil {
		t.Fatal("goal binding missing")
	}
	if loaded.Goal.SandboxProfile != testInvocationSandboxProfile || loaded.Goal.LauncherSandbox != testInvocationSandboxProfile {
		t.Fatalf("selected profile identity must bind to existing task records: %+v", loaded.Goal)
	}
	if loaded.Goal.SandboxProfileDigest == "" || loaded.Goal.GitCommonDir == "" || loaded.Goal.CardexRoot != root {
		t.Fatalf("profile digest/git-common/root evidence missing: %+v", loaded.Goal)
	}
	if loaded.Goal.SandboxWorktree != dir {
		t.Fatalf("worktree evidence %q want %q", loaded.Goal.SandboxWorktree, dir)
	}
}

func TestGoalRunSandboxSelectorRejectsBeforeEffect(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"unknown-profile", "off", "bypass", "unrestricted", "devbox", "workspace",
		"read-only", grokBuildReadOnlySandboxMacOSNoopNetwork,
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			root, dir, argvPath, home, _, wf, tk, cfgBefore := setupInvocationSandboxLaunch(t)
			_ = dir
			_ = home
			_ = tk
			headlessGoalStdin(t)
			err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual", "-sandbox", name})
			if err == nil {
				t.Fatalf("selector %q must reject before launch", name)
			}
			if !errors.Is(err, errGoalUnsupportedTuple) {
				t.Fatalf("selector %q want errGoalUnsupportedTuple, got %v", name, err)
			}
			cfgAfter, rerr := os.ReadFile(configPath(root))
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(cfgAfter) != string(cfgBefore) {
				t.Fatalf("reject must leave shared config unchanged for %q", name)
			}
			if _, statErr := os.Stat(argvPath); statErr == nil {
				raw, _ := os.ReadFile(argvPath)
				t.Fatalf("reject %q must not exec launcher, argv dump:\n%s", name, raw)
			}
		})
	}
}

func TestParseGrokSandboxTOMLRejectsUnsupportedSyntax(t *testing.T) {
	t.Parallel()
	if _, err := parseGrokSandboxTOML([]byte("[profiles.ok]\nunknown = true\n"), "f"); err == nil {
		t.Fatal("unknown key must reject")
	}
	if _, err := parseGrokSandboxTOML([]byte("[profiles.ok]\nextends = \"workspace\"\nextends = \"workspace\"\n"), "f"); err == nil {
		t.Fatal("duplicate key must reject")
	}
	if _, err := parseGrokSandboxTOML([]byte("[profiles.ok.filesystem]\nread_write = [\"/tmp\"]\n"), "f"); err == nil {
		t.Fatal("nested table must reject")
	}
	if _, err := parseGrokSandboxTOML([]byte("read_write = [\"/tmp\"]\n"), "f"); err == nil {
		t.Fatal("key outside table must reject")
	}
	ok, err := parseGrokSandboxTOML([]byte("[profiles.ok]\nextends = \"workspace\"\nread_write = [\"/tmp/a\"]\ndeny = [\"/tmp/secret\"]\n"), "f")
	if err != nil {
		t.Fatal(err)
	}
	if len(ok["ok"].Deny) != 1 || ok["ok"].Deny[0] != "/tmp/secret" {
		t.Fatalf("deny not parsed: %+v", ok["ok"])
	}
}

func TestGrokSandboxUserProfileWinsOverProject(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	common := linkGitCommonDirOutside(t, dir)
	writeGrokSandboxTOML(t, filepath.Join(dir, ".grok", "sandbox.toml"), testInvocationSandboxProfile, "workspace", []string{common})
	writeGrokSandboxTOML(t, filepath.Join(home, "sandbox.toml"), testInvocationSandboxProfile, "workspace", []string{filepath.Join(t.TempDir(), "other.git")})
	task := &Task{Type: typeSequence, Dir: dir, GrokModel: "grok-4.6",
		Goal: &TaskGoalBinding{GrokHome: home, LaunchSandboxSelector: testInvocationSandboxProfile}}
	if _, _, err := resolveManualGrokTuple(cfg, task); err == nil || !errors.Is(err, errGoalUnsupportedTuple) {
		t.Fatalf("user profile without covering grant must win and reject, got %v", err)
	}
}

func TestGoalRunSandboxSelectorDenyOrReadOnlyOverlapRejected(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"deny", "read_only"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			root, dir, argvPath, home, _, wf, _, cfgBefore := setupInvocationSandboxLaunch(t)
			common := canonicalGitCommonDir(dir)
			body := "[profiles." + testInvocationSandboxProfile + "]\nextends = \"workspace\"\nread_write = [\"" + common + "\"]\n"
			if kind == "deny" {
				body += "deny = [\"" + common + "\"]\n"
			} else {
				body += "read_only = [\"" + common + "\"]\n"
			}
			if err := os.WriteFile(filepath.Join(home, "sandbox.toml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			headlessGoalStdin(t)
			err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual", "-sandbox", testInvocationSandboxProfile})
			if err == nil || !errors.Is(err, errGoalUnsupportedTuple) {
				t.Fatalf("%s overlap must reject before effect: %v", kind, err)
			}
			cfgAfter, rerr := os.ReadFile(configPath(root))
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(cfgAfter) != string(cfgBefore) {
				t.Fatal("overlap reject must leave shared config unchanged")
			}
			if _, statErr := os.Stat(argvPath); statErr == nil {
				raw, _ := os.ReadFile(argvPath)
				t.Fatalf("%s overlap must not exec launcher:\n%s", kind, raw)
			}
		})
	}
}

func TestGoalRunSandboxSelectorMismatchedGitGrantRejected(t *testing.T) {
	root, dir, argvPath, home, _, wf, _, cfgBefore := setupInvocationSandboxLaunch(t)
	writeGrokSandboxTOML(t, filepath.Join(home, "sandbox.toml"), testInvocationSandboxProfile, "workspace", []string{filepath.Join(t.TempDir(), "other.git")})
	headlessGoalStdin(t)
	err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual", "-sandbox", testInvocationSandboxProfile})
	if err == nil {
		t.Fatal("mismatched Git grant must reject before launch")
	}
	if !errors.Is(err, errGoalUnsupportedTuple) {
		t.Fatalf("want errGoalUnsupportedTuple, got %v", err)
	}
	cfgAfter, rerr := os.ReadFile(configPath(root))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(cfgAfter) != string(cfgBefore) {
		t.Fatal("mismatched grant reject must leave shared config unchanged")
	}
	if _, statErr := os.Stat(argvPath); statErr == nil {
		raw, _ := os.ReadFile(argvPath)
		t.Fatalf("mismatched grant must not exec launcher:\n%s", raw)
	}
	_ = dir
}

func TestGoalRunSandboxSelectorNoEnvOverrideOrCarry(t *testing.T) {
	root, dir, argvPath, home, cfg, wf, tk, cfgBefore := setupInvocationSandboxLaunch(t)
	t.Setenv("CARDEX_SANDBOX", "off")
	t.Setenv("GROK_SANDBOX", "bypass")
	t.Setenv("GROK_BUILD_SANDBOX", "unrestricted")
	headlessGoalStdin(t)
	if err := cmdWorkflowGoalRun([]string{"-root", root, wf.ID, "-manual", "-sandbox", testInvocationSandboxProfile}); err != nil {
		t.Fatalf("explicit selector: %v", err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if dumpedArgValue(string(raw), "--sandbox") != testInvocationSandboxProfile {
		t.Fatalf("env must not override explicit selector:\n%s", raw)
	}
	cfgAfter, err := os.ReadFile(configPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(cfgAfter) != string(cfgBefore) {
		t.Fatal("selector must not persist into shared config")
	}

	root2, dir2 := workflowTestRoot(t)
	cfg2 := workflowTestCfg(t, root2)
	argv2 := filepath.Join(t.TempDir(), "argv2")
	enableManualGrok(t, root2, cfg2, argv2)
	wf2 := initTestWorkflow(t, root2, dir2)
	admitManualWriter(t, root2, cfg2, wf2)
	if err := cmdWorkflowGoalRun([]string{"-root", root2, wf2.ID, "-manual"}); err != nil {
		t.Fatalf("default launch: %v", err)
	}
	raw2, err := os.ReadFile(argv2)
	if err != nil {
		t.Fatal(err)
	}
	if dumpedArgValue(string(raw2), "--sandbox") != grokBuildWriteSandboxDefault {
		t.Fatalf("unrelated launch must keep default argv --sandbox %s:\n%s", grokBuildWriteSandboxDefault, raw2)
	}
	_ = dir
	_ = home
	_ = cfg
	_ = tk
}

func TestGoalRunSandboxSelectorRefusesSilentSubstitution(t *testing.T) {
	t.Parallel()
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	common := linkGitCommonDirOutside(t, dir)
	writeGrokSandboxTOML(t, filepath.Join(home, "sandbox.toml"), testInvocationSandboxProfile, "workspace", []string{common})
	task := &Task{Type: typeSequence, Dir: dir, SessionID: "sess-sub", GrokModel: "grok-4.6",
		Goal: &TaskGoalBinding{GrokHome: home, LaunchSandboxSelector: testInvocationSandboxProfile}}
	sandbox, perm, err := resolveManualGrokTuple(cfg, task)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox != testInvocationSandboxProfile || perm != "auto" {
		t.Fatalf("got %s/%s", sandbox, perm)
	}
	if err := bindGoalSandboxEvidence(root, cfg, task); err != nil {
		t.Fatal(err)
	}
	args := []string{"--sandbox", sandbox, "--permission-mode", perm, "--cwd", dir}
	task.Goal.SandboxProfile = grokBuildWriteSandboxDefault
	if err := verifySandboxLauncherEvidence(root, task, args); err == nil {
		t.Fatal("silently substituting profile identity must fail")
	}
	task.Goal.SandboxProfile = testInvocationSandboxProfile
	task.Goal.CardexRoot = root + "-other"
	if err := verifySandboxLauncherEvidence(root, task, args); err == nil {
		t.Fatal("silently substituting cardex root must fail")
	}
	task.Goal.CardexRoot = root
	task.Dir = t.TempDir()
	if err := verifySandboxLauncherEvidence(root, task, args); err == nil {
		t.Fatal("silently substituting worktree must fail")
	}
}
