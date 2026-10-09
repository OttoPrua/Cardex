package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
)

func completionVerifySuccessPayload() string {
	return `{"type":"text","data":"GROK_OK"}` + "\n" + grok105PublicEnd
}

func setupCompletionVerifyGrok(t *testing.T, work, verify string, optIn bool) (root string, cfg *Config, task *Task) {
	t.Helper()
	bin, _ := fakeGrokBuildCounted(t, completionVerifySuccessPayload(), "", 0)
	cfg = grokBuildTestConfig(t, bin)
	cfg.GrokBuild.OpusAdversarialReview = false
	cfg.HarvestMode = harvestModeOff
	isolateGrokLifecycleHome(t, cfg)
	root = testRoot(t)
	if work == "" {
		work = t.TempDir()
	}
	task = newTask(root, cfg, typeSequence, "completion verify", work, []string{"review only the current synthetic candidate"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	task.GrokModel = "grok-4.6"
	task.GrokEffort = "xhigh"
	task.ReviewAfter = false
	task.Verify = verify
	task.VerifyOnSuccess = optIn
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	return root, cfg, task
}

func runCompletionVerifyGrok(t *testing.T, ctx context.Context, root string, cfg *Config, task *Task) *Task {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runTaskVia(ctx, root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatalf("runTaskVia: %v", err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertVerifyRecorded(t *testing.T, root string, got *Task, wantExit int, wantTail string) {
	t.Helper()
	if got.Harvest == nil || got.Harvest.VerifyExit == nil {
		t.Fatalf("expected harvest verify evidence, got %+v", got.Harvest)
	}
	if *got.Harvest.VerifyExit != wantExit {
		t.Fatalf("verify_exit=%d want %d harvest=%+v", *got.Harvest.VerifyExit, wantExit, got.Harvest)
	}
	if wantTail != "" && !strings.Contains(got.Harvest.VerifyTail, wantTail) {
		t.Fatalf("verify_tail %q missing %q", got.Harvest.VerifyTail, wantTail)
	}
	events, _, err := loadTaskEvents(root, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Detail == nil {
			continue
		}
		exit, ok := ev.Detail["verify_exit"]
		if !ok {
			continue
		}
		found = true
		switch v := exit.(type) {
		case float64:
			if int(v) != wantExit {
				t.Fatalf("event verify_exit=%v want %d type=%s", exit, wantExit, ev.Type)
			}
		case int:
			if v != wantExit {
				t.Fatalf("event verify_exit=%v want %d type=%s", exit, wantExit, ev.Type)
			}
		default:
			t.Fatalf("event verify_exit type %T", exit)
		}
		if cmd, _ := ev.Detail["verify_command"].(string); strings.TrimSpace(cmd) == "" {
			t.Fatalf("event missing verify_command: %+v", ev)
		}
	}
	if !found {
		t.Fatalf("no event recorded verify_exit; events=%d", len(events))
	}
}

func TestCompletionVerifyPassUnmodifiedWorktree(t *testing.T) {
	work := t.TempDir()
	root, cfg, task := setupCompletionVerifyGrok(t, work, "printf 'PASS_UNMODIFIED\\n'; printf '%s\\n' \"$0\"", true)
	got := runCompletionVerifyGrok(t, nil, root, cfg, task)
	if got.Status != statusDone {
		t.Fatalf("status %s last %q", got.Status, got.LastError)
	}
	if got.Verdict != "" {
		t.Fatalf("success-path verify must not invoke harvest verdict, got %q", got.Verdict)
	}
	assertVerifyRecorded(t, root, got, 0, "PASS_UNMODIFIED")
	if runtime.GOOS != "windows" && !strings.Contains(got.Harvest.VerifyTail, "/bin/sh") {
		t.Fatalf("POSIX shell argv not recorded in tail: %q", got.Harvest.VerifyTail)
	}
	if !strings.Contains(got.Harvest.Diagnostics, "/bin/sh") && runtime.GOOS != "windows" {
		t.Fatalf("diagnostics should name POSIX shell: %q", got.Harvest.Diagnostics)
	}
}

func TestCompletionVerifyPassModifiedWorktree(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, cfg, task := setupCompletionVerifyGrok(t, work, "printf 'PASS_MODIFIED\\n'", true)
	got := runCompletionVerifyGrok(t, nil, root, cfg, task)
	if got.Status != statusDone {
		t.Fatalf("status %s last %q", got.Status, got.LastError)
	}
	body, err := os.ReadFile(filepath.Join(work, "hello.txt"))
	if err != nil || string(body) != "changed\n" {
		t.Fatalf("modified worktree not preserved: %s %v", body, err)
	}
	assertVerifyRecorded(t, root, got, 0, "PASS_MODIFIED")
}

func TestCompletionVerifyFailPreservesWork(t *testing.T) {
	work := t.TempDir()
	keep := filepath.Join(work, "keep.txt")
	if err := os.WriteFile(keep, []byte("retain-me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, cfg, task := setupCompletionVerifyGrok(t, work, "printf 'FAIL_TAIL\\n'; exit 7", true)
	got := runCompletionVerifyGrok(t, nil, root, cfg, task)
	if got.Status == statusDone {
		t.Fatalf("failed verify marked done: %+v", got)
	}
	if got.Status != statusHeld {
		t.Fatalf("status %s last %q", got.Status, got.LastError)
	}
	if !strings.Contains(got.LastError, completionVerifyReasonFailed) {
		t.Fatalf("precise reason missing: %q", got.LastError)
	}
	body, err := os.ReadFile(keep)
	if err != nil || string(body) != "retain-me\n" {
		t.Fatalf("worktree bytes lost: %s %v", body, err)
	}
	assertVerifyRecorded(t, root, got, 7, "FAIL_TAIL")
}

func delayedVerifyWriterScript(started, delayed string) string {
	return "(sleep 2; touch " + shSingleQuote(delayed) + ") & touch " + shSingleQuote(started) + "; sleep 30"
}

func assertNoDelayedVerifyMarker(t *testing.T, delayed string) {
	t.Helper()
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(delayed); !os.IsNotExist(err) {
		t.Fatalf("delayed marker %s appeared; writer-capable child survived reclaim: %v", delayed, err)
	}
}

func TestCompletionVerifyTimeoutPreservesWork(t *testing.T) {
	prev := completionVerifyTimeout
	completionVerifyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { completionVerifyTimeout = prev })
	work := t.TempDir()
	keep := filepath.Join(work, "keep-timeout.txt")
	started := filepath.Join(work, "started")
	delayed := filepath.Join(work, "delayed-marker")
	if err := os.WriteFile(keep, []byte("timeout-keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, cfg, task := setupCompletionVerifyGrok(t, work, delayedVerifyWriterScript(started, delayed), true)
	began := time.Now()
	got := runCompletionVerifyGrok(t, nil, root, cfg, task)
	if elapsed := time.Since(began); elapsed > 3*time.Second {
		t.Fatalf("timeout reclaim took %v; spawned tree still held the pipe", elapsed)
	}
	if got.Status == statusDone {
		t.Fatal("timeout verify marked done")
	}
	if got.Status != statusHeld || !strings.Contains(got.LastError, completionVerifyReasonTimeout) {
		t.Fatalf("status %s last %q", got.Status, got.LastError)
	}
	body, err := os.ReadFile(keep)
	if err != nil || string(body) != "timeout-keep\n" {
		t.Fatalf("worktree bytes lost: %s %v", body, err)
	}
	if got.Harvest == nil || got.Harvest.VerifyExit == nil {
		t.Fatalf("timeout must record verify result: %+v", got.Harvest)
	}
	assertNoDelayedVerifyMarker(t, delayed)
}

func TestCompletionVerifyCancelPreservesWork(t *testing.T) {
	work := t.TempDir()
	keep := filepath.Join(work, "keep-cancel.txt")
	started := filepath.Join(work, "started")
	delayed := filepath.Join(work, "delayed-marker")
	if err := os.WriteFile(keep, []byte("cancel-keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, cfg, task := setupCompletionVerifyGrok(t, work, delayedVerifyWriterScript(started, delayed), true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runTaskVia(ctx, root, cfg, task, grokBuildRunnerName)
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			cancel()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("verify command never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	canceledAt := time.Now()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runTaskVia after cancel: %v", err)
		}
		if elapsed := time.Since(canceledAt); elapsed > 3*time.Second {
			t.Fatalf("cancel reclaim took %v; spawned tree still held the pipe", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not reclaim the spawned command tree promptly")
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == statusDone {
		t.Fatal("canceled verify marked done")
	}
	if got.Status != statusHeld && got.Status != statusCanceled {
		t.Fatalf("status %s last %q", got.Status, got.LastError)
	}
	if got.Status == statusHeld && !strings.Contains(got.LastError, completionVerifyReasonCanceled) &&
		!strings.Contains(got.LastError, "cancel") {
		t.Fatalf("precise cancel reason missing: %q", got.LastError)
	}
	body, err := os.ReadFile(keep)
	if err != nil || string(body) != "cancel-keep\n" {
		t.Fatalf("worktree bytes lost: %s %v", body, err)
	}
	assertNoDelayedVerifyMarker(t, delayed)
}

func TestCompletionVerifyCardexCancelReclaimsProcessTree(t *testing.T) {
	bin := buildCardexCLI(t)
	root := t.TempDir()
	work := t.TempDir()
	keep := filepath.Join(work, "keep-cli-cancel.txt")
	started := filepath.Join(work, "started")
	delayed := filepath.Join(work, "delayed-marker")
	if err := os.WriteFile(keep, []byte("cli-cancel-keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCompletionVerifyCLIConfig(t, root, bin)
	add := exec.Command(bin, "add",
		"-root", root,
		"-runner", grokBuildRunnerName,
		"-grok-model", "grok-4.6",
		"-grok-effort", "xhigh",
		"-dir", work,
		"-title", "cli-cancel-verify",
		"-verify", delayedVerifyWriterScript(started, delayed),
		"-verify-on-success",
		"-review-after=false",
		"review only the current synthetic candidate",
	)
	addOut, err := add.CombinedOutput()
	if err != nil {
		t.Fatalf("cardex add: %v\n%s", err, addOut)
	}
	id := onlyTaskID(t, root)
	runCmd := exec.Command(bin, "run", "-root", root, id)
	var runBuf bytes.Buffer
	runCmd.Stdout = &runBuf
	runCmd.Stderr = &runBuf
	if err := runCmd.Start(); err != nil {
		t.Fatalf("cardex run start: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = runCmd.Process.Kill()
			_, _ = runCmd.Process.Wait()
			t.Fatalf("verify command never started\nrun:\n%s", runBuf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	canceledAt := time.Now()
	cancelCmd := exec.Command(bin, "cancel", "-root", root, id)
	cancelOut, cancelErr := cancelCmd.CombinedOutput()
	waitCh := make(chan error, 1)
	go func() { waitCh <- runCmd.Wait() }()
	select {
	case <-waitCh:
		if elapsed := time.Since(canceledAt); elapsed > 3*time.Second {
			t.Fatalf("cardex cancel reclaim took %v\nrun:\n%s\ncancel:\n%s", elapsed, runBuf.String(), cancelOut)
		}
	case <-time.After(3 * time.Second):
		_ = runCmd.Process.Kill()
		<-waitCh
		t.Fatalf("cardex run did not exit after cancel\nrun:\n%s\ncancel:\n%s", runBuf.String(), cancelOut)
	}
	t.Logf("cardex run:\n%s\ncardex cancel err=%v:\n%s", runBuf.String(), cancelErr, cancelOut)
	if cancelErr != nil {
		t.Fatalf("cardex cancel: %v\n%s\nrun:\n%s", cancelErr, cancelOut, runBuf.String())
	}
	got, err := findTaskAnywhere(root, id)
	if err != nil {
		t.Fatalf("load canceled card: %v\nrun:\n%s\ncancel:\n%s", err, runBuf.String(), cancelOut)
	}
	if got.Status == statusDone {
		t.Fatalf("cardex cancel left verify marked done: %+v\nrun:\n%s\ncancel:\n%s", got, runBuf.String(), cancelOut)
	}
	if got.Status != statusHeld && got.Status != statusCanceled {
		t.Fatalf("status %s last %q control=%s\nrun:\n%s\ncancel:\n%s",
			got.Status, got.LastError, got.effectiveControlState(), runBuf.String(), cancelOut)
	}
	body, err := os.ReadFile(keep)
	if err != nil || string(body) != "cli-cancel-keep\n" {
		t.Fatalf("worktree bytes lost: %s %v", body, err)
	}
	assertNoDelayedVerifyMarker(t, delayed)
}

func TestCompletionVerifyOldCardSkipsSuccessPath(t *testing.T) {
	work := t.TempDir()
	marker := filepath.Join(work, "should-not-run")
	root, cfg, task := setupCompletionVerifyGrok(t, work, "touch "+marker, false)
	got := runCompletionVerifyGrok(t, nil, root, cfg, task)
	if got.Status != statusDone {
		t.Fatalf("old card success path: status %s last %q", got.Status, got.LastError)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("old card ran completion-verify on provider success")
	}
	if got.Harvest != nil && got.Harvest.VerifyExit != nil {
		t.Fatalf("old card stored completion verify: %+v", got.Harvest)
	}
}

func TestLocalCompletionShellIsPOSIXOnThisHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		name, prefix := localCompletionShell()
		if !strings.Contains(strings.ToLower(name), "cmd.exe") || len(prefix) == 0 || prefix[0] != "/C" {
			t.Fatalf("windows shell %q %v", name, prefix)
		}
		return
	}
	name, prefix := localCompletionShell()
	if name != "/bin/sh" || len(prefix) != 1 || prefix[0] != "-c" {
		t.Fatalf("POSIX shell want /bin/sh -c, got %q %v", name, prefix)
	}
}

func TestCompletionVerifyWindowsShellSource(t *testing.T) {
	data, err := os.ReadFile("completion_verify_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "cmd.exe") || !strings.Contains(text, `"/C"`) {
		t.Fatalf("windows helper must pin cmd.exe /C, got:\n%s", text)
	}
	if strings.Contains(text, "sh -c") {
		t.Fatal("windows helper must not select POSIX sh")
	}
}

func fakeSSHRecorder(t *testing.T, recordPath, stdout string, exit int) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shSingleQuote(recordPath) + "\n"
	script += "printf '%s' " + shSingleQuote(stdout) + "\n"
	script += "exit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestCompletionVerifyRemoteUsesAdapterCwd(t *testing.T) {
	record := filepath.Join(t.TempDir(), "ssh.args")
	localMarker := filepath.Join(t.TempDir(), "local-ran")
	remoteCwd := "/remote/actual/cwd"
	_, cfg, task := setupCompletionVerifyGrok(t, t.TempDir(), "touch "+localMarker, true)
	cfg.SSHBin = fakeSSHRecorder(t, record, "REMOTE_OK\n", 0)
	cfg.RemoteHosts = map[string]RemoteHostConfig{
		"box": {Shell: "posix", CodexBin: "codex"},
	}
	task.RemoteHost = "box"
	task.Dir = remoteCwd
	res := runCompletionVerify(context.Background(), cfg, task)
	if !res.Passed || res.Exit != 0 {
		t.Fatalf("remote verify %+v", res)
	}
	if !strings.Contains(res.Tail, "REMOTE_OK") {
		t.Fatalf("remote tail %q", res.Tail)
	}
	if _, err := os.Stat(localMarker); !os.IsNotExist(err) {
		t.Fatal("remote verify ran locally")
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	text := string(args)
	if !strings.Contains(text, remoteCwd) {
		t.Fatalf("fake adapter missing remote cwd %q in %q", remoteCwd, text)
	}
	if !strings.Contains(text, "box") {
		t.Fatalf("fake adapter missing host in %q", text)
	}
	if !strings.Contains(text, "/bin/sh") {
		t.Fatalf("posix remote command missing /bin/sh: %q", text)
	}
}

func TestCompletionVerifyRemoteUnsupportedDoesNotRunLocally(t *testing.T) {
	localMarker := filepath.Join(t.TempDir(), "local-ran")
	root, cfg, task := setupCompletionVerifyGrok(t, t.TempDir(), "touch "+localMarker, true)
	task.RemoteHost = "missing-host"
	task.Dir = "/remote/cwd"
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	withSchedulerLock(t, root)
	if err := applyCompletionVerify(context.Background(), root, cfg, task, nil); err != nil {
		t.Fatalf("applyCompletionVerify: %v", err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == statusDone {
		t.Fatal("unsupported remote marked done")
	}
	if got.Status != statusHeld || !strings.Contains(got.LastError, completionVerifyReasonUnsupportedRemote) {
		t.Fatalf("status %s last %q", got.Status, got.LastError)
	}
	if _, err := os.Stat(localMarker); !os.IsNotExist(err) {
		t.Fatal("unsupported remote ran locally")
	}
	if got.Harvest == nil || got.Harvest.VerifyExit == nil {
		t.Fatalf("must record unsupported result: %+v", got.Harvest)
	}
}

func fakeSSHWindowsCwdProjector(t *testing.T, recordPath, ranMarker string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\n"
	script += "printf '%s\\n' \"$@\" > " + shSingleQuote(recordPath) + "\n"
	script += "remote=\"\"\n"
	script += "for a; do remote=$a; done\n"
	script += "after=${remote#cd /d }\n"
	script += "case \"$after\" in\n"
	script += "\\\"*) after=${after#\\\"}; after=${after#*\\\"} ;;\n"
	script += "esac\n"
	script += "after=${after# }\n"
	script += "case \"$after\" in\n"
	script += "'&&'*) printf 'cd_failed\\n' >&2; exit 1 ;;\n"
	script += "'&'*) printf 'FALSE_SUCCESS\\n'; : > " + shSingleQuote(ranMarker) + "; exit 0 ;;\n"
	script += "esac\n"
	script += "printf 'unrecognized remote command\\n' >&2\n"
	script += "exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestCompletionVerifyRemoteWindowsCwdFailDoesNotRunVerify(t *testing.T) {
	verify := `printf '%s\n' "$HOME" & echo still-inner`
	posixCwd := `/remote/dir with space`
	posixCmd := remoteCompletionVerifyCommand("posix", posixCwd, verify)
	if !strings.Contains(posixCmd, "&&") {
		t.Fatalf("posix remote must require successful cd: %q", posixCmd)
	}
	if !strings.Contains(posixCmd, shellQuote(verify)) {
		t.Fatalf("posix Verify not protected from outer expansion: %q", posixCmd)
	}
	if !strings.Contains(posixCmd, shellQuote(posixCwd)) {
		t.Fatalf("posix cwd not protected from outer expansion: %q", posixCmd)
	}

	missing := `C:\cardex-no-such-cwd\verify-fail`
	winCmd := remoteCompletionVerifyCommand("cmd", missing, verify)
	if !strings.Contains(winCmd, "&&") {
		t.Fatalf("windows remote must require successful cwd change, got %q", winCmd)
	}
	if strings.Contains(winCmd, " & ") && !strings.Contains(winCmd, " && ") {
		t.Fatalf("unconditional & after cd still runs verify: %q", winCmd)
	}
	if !strings.Contains(winCmd, cmdDoubleQuote(verify)) {
		t.Fatalf("windows Verify not quoted as a single cmd.exe /C argument: %q", winCmd)
	}

	record := filepath.Join(t.TempDir(), "ssh.args")
	ranMarker := filepath.Join(t.TempDir(), "verify-ran")
	localMarker := filepath.Join(t.TempDir(), "local-ran")
	_, cfg, task := setupCompletionVerifyGrok(t, t.TempDir(), "touch "+localMarker, true)
	cfg.SSHBin = fakeSSHWindowsCwdProjector(t, record, ranMarker)
	cfg.RemoteHosts = map[string]RemoteHostConfig{
		"winbox": {Shell: "cmd", CodexBin: "codex"},
	}
	task.RemoteHost = "winbox"
	task.Dir = missing
	task.Verify = verify
	res := runCompletionVerify(context.Background(), cfg, task)
	if res.Passed {
		t.Fatalf("failed windows cd reported success: %+v", res)
	}
	if res.Reason == completionVerifyReasonPassed {
		t.Fatalf("failed windows cd classified passed: %+v", res)
	}
	if _, err := os.Stat(ranMarker); !os.IsNotExist(err) {
		t.Fatal("windows verify payload ran after failed cd")
	}
	if _, err := os.Stat(localMarker); !os.IsNotExist(err) {
		t.Fatal("windows remote verify ran locally")
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	text := string(args)
	if !strings.Contains(text, "winbox") {
		t.Fatalf("fake adapter missing host in %q", text)
	}
	if !strings.Contains(text, "cd /d") || !strings.Contains(text, "&&") {
		t.Fatalf("projected windows command missing required cd &&: %q", text)
	}
	if !strings.Contains(text, cmdDoubleQuote(verify)) {
		t.Fatalf("projected command lost quoted Verify expression: %q", text)
	}
	if !strings.Contains(text, "$HOME") {
		t.Fatalf("Verify $HOME was outer-expanded: %q", text)
	}
}

func TestCompletionVerifyCLIConsumer(t *testing.T) {
	bin := buildCardexCLI(t)
	run := func(name, verify string, wantDone bool, wantExit int, wantTail string) {
		t.Helper()
		root := t.TempDir()
		work := t.TempDir()
		if !wantDone {
			if err := os.WriteFile(filepath.Join(work, "keep-cli.txt"), []byte("cli-keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		writeCompletionVerifyCLIConfig(t, root, bin)
		add := exec.Command(bin, "add",
			"-root", root,
			"-runner", grokBuildRunnerName,
			"-grok-model", "grok-4.6",
			"-grok-effort", "xhigh",
			"-dir", work,
			"-title", name,
			"-verify", verify,
			"-verify-on-success",
			"-review-after=false",
			"review only the current synthetic candidate",
		)
		addOut, err := add.CombinedOutput()
		if err != nil {
			t.Fatalf("cardex add: %v\n%s", err, addOut)
		}
		id := onlyTaskID(t, root)
		runCmd := exec.Command(bin, "run", "-root", root, id)
		runOut, runErr := runCmd.CombinedOutput()
		if runErr != nil && wantDone {
			t.Fatalf("cardex run: %v\n%s", runErr, runOut)
		}
		got, err := loadTask(root, id)
		if err != nil {
			t.Fatal(err)
		}
		if wantDone {
			if got.Status != statusDone {
				t.Fatalf("%s status %s last %q\nrun:\n%s", name, got.Status, got.LastError, runOut)
			}
		} else {
			if got.Status == statusDone {
				t.Fatalf("%s marked done\nrun:\n%s", name, runOut)
			}
			if got.Status != statusHeld {
				t.Fatalf("%s status %s last %q", name, got.Status, got.LastError)
			}
			body, err := os.ReadFile(filepath.Join(work, "keep-cli.txt"))
			if err != nil || string(body) != "cli-keep\n" {
				t.Fatalf("cli fail lost worktree: %s %v", body, err)
			}
		}
		assertVerifyRecorded(t, root, got, wantExit, wantTail)
	}
	run("cli-pass", "printf 'CLI_PASS\\n'", true, 0, "CLI_PASS")
	run("cli-fail", "printf 'CLI_FAIL\\n'; exit 4", false, 4, "CLI_FAIL")
}

func writeCompletionVerifyCLIConfig(t *testing.T, root, unusedBin string) {
	t.Helper()
	_ = unusedBin
	payload := completionVerifySuccessPayload()
	grok, _ := fakeGrokBuildCounted(t, payload, "", 0)
	cfg := grokBuildTestConfig(t, grok)
	cfg.GrokBuild.OpusAdversarialReview = false
	cfg.HarvestMode = harvestModeOff
	for _, d := range []string{"tasks", "archive", "logs", "events", "templates"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
}

func onlyTaskID(t *testing.T, root string) string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	if len(ids) != 1 {
		t.Fatalf("want 1 task, got %v", ids)
	}
	return ids[0]
}

func buildCardexCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cardex")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build cardex: %v\n%s", err, out)
	}
	return bin
}

func caseVariantExisting(dir string) (string, bool) {
	parent := filepath.Dir(dir)
	base := filepath.Base(dir)
	var b strings.Builder
	flipped := false
	for _, r := range base {
		switch {
		case unicode.IsUpper(r):
			b.WriteRune(unicode.ToLower(r))
			flipped = true
		case unicode.IsLower(r):
			b.WriteRune(unicode.ToUpper(r))
			flipped = true
		default:
			b.WriteRune(r)
		}
	}
	if !flipped {
		return "", false
	}
	variant := filepath.Join(parent, b.String())
	sa, errA := os.Stat(dir)
	sb, errB := os.Stat(variant)
	if errA != nil || errB != nil {
		return "", false
	}
	return variant, os.SameFile(sa, sb)
}

// A completed model call still counts when mechanical verification holds the
// card. A legal retry must keep that usage and replace the stale verify result.
func TestCompletionVerifyRetryPreservesUsageAndRefreshesEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the POSIX fake provider; native verification is covered separately")
	}
	root := testRoot(t)
	work := t.TempDir()
	payload := `{"type":"result","result":"done","session_id":"sess-verify","num_turns":1,"total_cost_usd":0.02,"duration_ms":10,"usage":{"input_tokens":5,"output_tokens":2},"is_error":false}`
	cfg := runTaskCfg(t, fakeClaudeBin(t, payload, "", 0))
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	task := newTask(root, cfg, typeSequence, "verify retry usage", work, []string{"bounded fixture"}, 5)
	task.Verify = "test -f retry-ready"
	task.VerifyOnSuccess = true
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, "claude"); err != nil {
		t.Fatal(err)
	}
	first := loadMust(t, root, task.ID)
	if first.Status != statusHeld || first.RawUsage == nil || first.RawUsage.Accumulated.InputTokens == nil || *first.RawUsage.Accumulated.InputTokens != 5 {
		t.Fatalf("verification hold lost provider usage: %+v", first)
	}
	if first.Harvest == nil || !strings.Contains(first.Harvest.Diagnostics, "verify_failed") {
		t.Fatalf("missing first failure evidence: %+v", first.Harvest)
	}
	if err := os.WriteFile(filepath.Join(work, "retry-ready"), []byte("kept progress"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmdSetStatus([]string{"-root", root, "-fresh", task.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	retried := loadMust(t, root, task.ID)
	if err := runTaskVia(context.Background(), root, cfg, retried, "claude"); err != nil {
		t.Fatal(err)
	}
	got := loadMust(t, root, task.ID)
	if got.Status != statusDone || got.RawUsage == nil || got.RawUsage.Accumulated.InputTokens == nil || *got.RawUsage.Accumulated.InputTokens != 10 {
		t.Fatalf("retry must retain both actual calls: %+v", got)
	}
	if got.Harvest == nil || got.Harvest.VerifyExit == nil || *got.Harvest.VerifyExit != 0 || !strings.Contains(got.Harvest.Diagnostics, "verify_passed") || strings.Contains(got.Harvest.Diagnostics, "verify_failed") {
		t.Fatalf("current success still reports stale verification: %+v", got.Harvest)
	}
}
