//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestWindowsLifecycleProbe(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".grok"), 0700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(home, "mode-probe")
	if err := os.WriteFile(probe, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Windows Go mode for requested 0600: %04o", info.Mode().Perm())
	if err := probeGrokBuildLifecycleState(&Task{ID: "windows", ActiveAttemptID: "attempt1"}, home); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".grok"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe residue: %v %v", entries, err)
	}
}

func TestWindowsProcessIdentity(t *testing.T) {
	id, ok := processStartIdentity(os.Getpid())
	if !ok || id == "" {
		t.Fatal("live process has no stable start identity")
	}
	workspace := t.TempDir()
	lease, err := prepareTaskProcessLease(exec.Command(os.Args[0]), "identity", workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.abort()
	rec := &AttemptRecord{PID: os.Getpid(), PGID: os.Getpid(), StartIdentity: id, WorkspaceLeaseID: workspace}
	if !verifyAttemptProcess(rec) {
		t.Fatal("actual identity rejected")
	}
	rec.StartIdentity = "wrong-creation-time"
	if verifyAttemptProcess(rec) {
		t.Fatal("PID with wrong start identity accepted")
	}
}

func windowsReservedTask(t *testing.T) (string, *Task) {
	t.Helper()
	root := testRoot(t)
	withSchedulerLock(t, root)
	tk := queuedSequence(t, root, testCfg(), "windows compatibility", t.TempDir())
	if err := reserveDispatchAttempt(root, tk); err != nil {
		t.Fatal(err)
	}
	taskExecRoot.Store(tk.ID, root)
	t.Cleanup(func() { taskExecRoot.Delete(tk.ID) })
	return root, tk
}

func TestWindowsBoundProcess(t *testing.T) {
	root, tk := windowsReservedTask(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsChild$")
	cmd.Env = append(os.Environ(), "CARDEX_WINDOWS_CHILD=sleep")
	cmd.Dir = tk.Dir
	setupProcGroup(cmd)
	err := runCmdRegisteredForTask(cmd, tk.ID)
	if err != nil {
		t.Fatalf("registered Windows child: %v", err)
	}
	rec, err := loadAttempt(root, tk.ID, tk.ActiveAttemptID)
	if err != nil || rec.PID <= 0 || rec.StartIdentity == "" || rec.State != attemptBound {
		t.Fatalf("no durable binding: %+v %v", rec, err)
	}
	if processAlive(rec.PID) {
		t.Fatal("reaped child reports alive")
	}
	if !producerGone(tk, rec) {
		t.Fatal("normal Windows completion cannot prove producer gone")
	}
}

func TestWindowsAdmissionDeniedDoesNotRequeue(t *testing.T) {
	root, tk := windowsReservedTask(t)
	err := abandonReservedAttemptForAdmission(root, tk)
	if !errors.Is(err, errAdmissionDenied) {
		t.Fatalf("error: %v", err)
	}
	got, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusHeld || eligible(got, time.Now()) {
		t.Fatalf("admission failure remains schedulable: %s", got.Status)
	}
	if strings.Contains(got.LastError, "before provider invoke") {
		t.Fatal("denial claims unproved zero-start")
	}
	if got.ActiveAttemptID != "" || got.effectiveControlState() != controlTerminal {
		t.Fatalf("denial custody not terminal: %+v", got)
	}
	if err := reserveDispatchAttempt(root, got); !errors.Is(err, errNotSchedulable) {
		t.Fatalf("second reservation after denial: %v", err)
	}
}

// This binary doubles as a native synthetic provider/descendant. It never calls a
// real provider and exits before testing flags or the global TestMain are processed.
func init() {
	if len(os.Args) > 2 && os.Args[1] == "-p" && os.Args[2] == "--output-format" && os.Getenv("CARDEX_TEST_COUNTING_CLAUDE") != "" {
		f, err := os.OpenFile(os.Getenv("CARDEX_TEST_COUNTING_CLAUDE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(8)
		}
		_, err = f.WriteString("x")
		f.Close()
		if err != nil {
			os.Exit(8)
		}
		fmt.Println(os.Getenv("CARDEX_TEST_COUNTING_OUTPUT"))
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "--model" {
		mode := "ok"
		for i, arg := range os.Args {
			if arg == "--prompt" && i+1 < len(os.Args) {
				mode = os.Args[i+1]
			}
		}
		if os.Getenv("KIMI_CODE_HOME") == "" || os.Getenv("KIMI_CODE_LEGACY_FLAG") != "1" || os.Getenv("KIMI_MODEL_THINKING_EFFORT") != "max" {
			os.Exit(9)
		}
		fmt.Println(`{"role":"meta","type":"system.version","version":"0.35.0"}`)
		fmt.Println(`{"role":"assistant","content":"WINDOWS_KIMI_OK"}`)
		fmt.Println(`{"role":"meta","type":"session.resume_hint","session_id":"synthetic-kimi"}`)
		if mode == "nonzero" {
			os.Exit(7)
		}
		os.Exit(0)
	}
	if len(os.Args) < 3 || os.Args[1] != "--cardex-windows-fixture" {
		return
	}
	args := os.Args[3:]
	switch os.Args[2] {
	case "stdin-provider":
		mode, record := args[0], args[1]
		prompt, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile(record, prompt, 0600)
		if mode == "claude" {
			fmt.Println(`{"type":"result","subtype":"success","is_error":false,"result":"WINDOWS_CLAUDE_OK"}`)
		} else {
			for i, arg := range args[2:] {
				if arg == "-o" && i+3 < len(args) {
					_ = os.WriteFile(args[i+3], []byte("WINDOWS_CODEX_OK"), 0600)
				}
			}
		}
	case "workspace-probe":
		if !workspaceProcessResidue(args[0]) {
			os.Exit(7)
		}
	case "crash-before-job-bind", "crash-before-anchor-bind":
		hook := func(child *exec.Cmd) {
			_ = os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0600)
			time.Sleep(30 * time.Second)
		}
		if os.Args[2] == "crash-before-anchor-bind" {
			windowsAfterAnchorStart = hook
		} else {
			afterCmdStart = hook
		}
		cmd := exec.CommandContext(context.Background(), os.Args[0], "--cardex-windows-fixture", "marker", args[0]+".marker")
		if len(args) > 1 {
			cmd.Dir = args[1]
		}
		setupProcGroup(cmd)
		if runCmdRegistered(cmd) != nil {
			os.Exit(5)
		}
	case "job-owner":
		cmd := exec.CommandContext(context.Background(), os.Args[0], "--cardex-windows-fixture", "linger", args[0])
		setupProcGroup(cmd)
		if err := runCmdRegistered(cmd); err != nil {
			os.Exit(5)
		}
	case "linger":
		if len(args) > 0 {
			_ = os.WriteFile(args[0], []byte(strconv.Itoa(os.Getpid())), 0600)
		}
		time.Sleep(30 * time.Second)
	case "marker":
		_ = os.WriteFile(args[0], []byte("executed"), 0600)
	case "args":
		_ = json.NewEncoder(os.Stdout).Encode(args)
	case "lock":
		_ = os.WriteFile(args[1], []byte("waiting"), 0600)
		err := withControlFileLock(args[0], "shared", func() error { return os.WriteFile(args[2], []byte("entered"), 0600) })
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case "grok":
		mode, calls := args[0], args[1]
		for _, arg := range args[2:] {
			if arg == "models" {
				fmt.Println("grok-4.6")
				os.Exit(0)
			}
		}
		f, err := os.OpenFile(calls, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(3)
		}
		f.WriteString("call\n")
		f.Close()
		if mode == "descendant" || mode == "timeout" {
			child := exec.Command(os.Args[0], "--cardex-windows-fixture", "linger", calls+".pid")
			if mode == "timeout" {
				child.Stdout = os.Stdout
				child.Stderr = os.Stderr
			}
			if err := child.Start(); err != nil {
				os.Exit(4)
			}
			for i := 0; i < 200; i++ {
				if _, err := os.Stat(calls + ".pid"); err == nil {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if mode == "timeout" {
				time.Sleep(30 * time.Second)
			}
		}
		fmt.Println(`{"type":"text","data":"WINDOWS_OK"}`)
		if mode != "missing" {
			fmt.Println(`{"type":"end","stopReason":"end_turn","sessionId":"synthetic"}`)
		}
		if mode == "duplicate" {
			fmt.Println(`{"type":"end","stopReason":"end_turn"}`)
		}
		if mode == "unknown" {
			fmt.Println(`{"type":"future_event","data":"unknown"}`)
		}
		if mode == "nonzero" {
			os.Exit(7)
		}
	}
	os.Exit(0)
}

func windowsFakeGrok(t *testing.T, mode string) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wrapper with spaces & punctuation")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path, calls := filepath.Join(dir, "grok.cmd"), filepath.Join(dir, "calls")
	script := fmt.Sprintf("@echo off\r\n\"%s\" --cardex-windows-fixture grok %s \"%s\" %%*\r\n", os.Args[0], mode, calls)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return path, calls
}

func TestWindowsGrokRunner(t *testing.T) {
	for _, mode := range []string{"ok", "missing", "duplicate", "unknown", "nonzero", "descendant", "bind_failure"} {
		t.Run(mode, func(t *testing.T) {
			bin, calls := windowsFakeGrok(t, mode)
			cfg := grokBuildTestConfig(t, bin)
			cfg.GrokBuild.OpusAdversarialReview = false
			root := testRoot(t)
			tk := queuedSequence(t, root, cfg, "explicit native Grok", t.TempDir())
			tk.RunnerExplicit = true
			tk.PreferRunner = grokBuildRunnerName
			tk.GrokModel = "grok-4.6"
			tk.GrokEffort = "xhigh"
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			if mode == "bind_failure" {
				attemptWriteHook = func(rec *AttemptRecord) error {
					if rec.State == attemptBound {
						return errors.New("synthetic durable bind failure")
					}
					return nil
				}
				defer func() { attemptWriteHook = nil }()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			err := runTaskVia(ctx, root, cfg, tk, grokBuildRunnerName)
			if err != nil {
				t.Fatalf("runner: %v", err)
			}
			got, err := loadTask(root, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := statusHeld
			if mode == "ok" {
				want = statusDone
			}
			if got.Status != want {
				t.Fatalf("status=%s want=%s error=%s", got.Status, want, got.LastError)
			}
			if got.ActiveAttemptID != "" || got.Attempts != 0 || got.PreferRunner != grokBuildRunnerName || got.CodexModel != "" {
				t.Fatalf("unexpected retry/fallback/custody: %+v", got)
			}
			if mode != "ok" {
				for _, ev := range readAllEventsRaw(t, root, tk.ID) {
					if ev.Type == evStepOK || ev.Type == evDone {
						t.Fatalf("failed result committed semantic completion: %s", ev.Type)
					}
				}
			}
			data, _ := os.ReadFile(calls)
			wantCalls := 1
			if mode == "bind_failure" {
				wantCalls = 0
			}
			if strings.Count(string(data), "call\n") != wantCalls {
				t.Fatalf("product starts=%q want=%d", data, wantCalls)
			}
			if mode == "descendant" {
				assertWindowsChildGone(t, calls+".pid")
			}
		})
	}
}

func assertWindowsChildGone(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Fatalf("descendant %d remains alive", pid)
	}
}

func TestWindowsJobTimeoutKillsDescendants(t *testing.T) {
	unrelated := exec.Command(os.Args[0], "--cardex-windows-fixture", "linger")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { unrelated.Process.Kill(); unrelated.Wait() }()
	root, tk := windowsReservedTask(t)
	_ = root
	_, calls := windowsFakeGrok(t, "timeout")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "--cardex-windows-fixture", "grok", "timeout", calls)
	cmd.Dir = tk.Dir
	setupProcGroup(cmd)
	if err := runCmdRegisteredForTask(cmd, tk.ID); err == nil {
		t.Fatal("timeout accepted")
	}
	assertWindowsChildGone(t, calls+".pid")
	if !processAlive(unrelated.Process.Pid) {
		t.Fatal("cancellation killed an unrelated process")
	}
	if processAlive(cmd.Process.Pid) || taskProcessResidue(tk.ID) {
		t.Fatal("timeout custody not recovered")
	}
}

func TestWindowsRunnerExitKillsJob(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner := exec.CommandContext(ctx, os.Args[0], "--cardex-windows-fixture", "job-owner", pidFile)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { owner.Process.Kill(); owner.Wait() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatal("owned descendant did not start")
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	owner.Wait()
	deadline = time.Now().Add(time.Second)
	data, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(string(data))
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assertWindowsChildGone(t, pidFile)
}

func TestWindowsKimiNativeExecutable(t *testing.T) {
	for _, mode := range []string{"ok", "nonzero"} {
		t.Run(mode, func(t *testing.T) {
			cfg := kimiCLITestConfig(t, os.Args[0])
			root := testRoot(t)
			tk := queuedSequence(t, root, cfg, "native Kimi", t.TempDir())
			tk.Prompts = []string{mode}
			tk.RunnerExplicit = true
			tk.PreferRunner = kimiCLIRunnerName
			tk.KimiModel = "kimi-code/k3"
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := runTaskVia(ctx, root, cfg, tk, kimiCLIRunnerName); err != nil {
				t.Fatal(err)
			}
			got, err := loadTask(root, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := statusDone
			if mode == "nonzero" {
				want = statusHeld
			}
			if got.Status != want || got.ActiveAttemptID != "" || got.Attempts != 0 || got.PreferRunner != kimiCLIRunnerName || got.CodexModel != "" {
				t.Fatalf("unexpected Kimi result: %+v", got)
			}
		})
	}
}

func TestWindowsStartFailureClosesJob(t *testing.T) {
	windowsJobsMu.Lock()
	before := len(windowsJobs)
	windowsJobsMu.Unlock()
	cmd := exec.CommandContext(context.Background(), filepath.Join(t.TempDir(), "missing.exe"))
	setupProcGroup(cmd)
	if err := runCmdRegistered(cmd); err == nil {
		t.Fatal("missing executable accepted")
	}
	windowsJobsMu.Lock()
	after := len(windowsJobs)
	windowsJobsMu.Unlock()
	if before != after {
		t.Fatal("start failure left a job registration")
	}
}

func TestWindowsResumeFailureReapsSuspendedChild(t *testing.T) {
	_, tk := windowsReservedTask(t)
	marker := filepath.Join(t.TempDir(), "must-not-run")
	cmd := exec.CommandContext(context.Background(), os.Args[0], "--cardex-windows-fixture", "marker", marker)
	cmd.Dir = tk.Dir
	setupProcGroup(cmd)
	attemptWriteHook = func(rec *AttemptRecord) error {
		if rec.State == attemptBound {
			return cmd.Process.Kill()
		}
		return nil
	}
	defer func() { attemptWriteHook = nil }()
	err := runCmdRegisteredForTask(cmd, tk.ID)
	if !errors.Is(err, errProcessExecution) || cmd.ProcessState == nil {
		t.Fatalf("resume failure not held/reaped: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("failed resume ran child code")
	}
}

func TestWindowsBindFailureNeverExecutesChild(t *testing.T) {
	_, tk := windowsReservedTask(t)
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	attemptWriteHook = func(rec *AttemptRecord) error {
		if rec.State == attemptBound {
			return errors.New("bind denied")
		}
		return nil
	}
	defer func() { attemptWriteHook = nil }()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "--cardex-windows-fixture", "marker", marker)
	cmd.Dir = tk.Dir
	setupProcGroup(cmd)
	err := runCmdRegisteredForTask(cmd, tk.ID)
	if !errors.Is(err, errProcessExecution) || cmd.ProcessState == nil {
		t.Fatalf("post-start error/reap: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("child ran before durable bind/job assignment")
	}
	if processAlive(cmd.Process.Pid) || taskProcessResidue(tk.ID) {
		t.Fatal("failed bind leaked custody")
	}
}

func TestWindowsControlLockAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	ready, entered := filepath.Join(root, "ready"), filepath.Join(root, "entered")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "--cardex-windows-fixture", "lock", root, ready, entered)
	err := withControlFileLock(root, "shared", func() error {
		if err := cmd.Start(); err != nil {
			return err
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if _, err := os.Stat(ready); err != nil {
			return fmt.Errorf("contender did not start: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(entered); !os.IsNotExist(err) {
			return errors.New("second process entered held critical section")
		}
		return nil
	})
	if err != nil {
		cancel()
		if cmd.Process != nil {
			cmd.Wait()
		}
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entered); err != nil {
		t.Fatal("contender never acquired released lock")
	}
}

func TestWindowsBatchArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "args.cmd")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("@echo off\r\n\"%s\" --cardex-windows-fixture args %%*\r\n", os.Args[0])), 0600); err != nil {
		t.Fatal(err)
	}
	want := []string{"space value", "a&b", "(value)", "a|b", "a<b", "a>b", "caret^value", "", "C:\\trailing\\", "C:\\trailing space\\", "中文路径"}
	cmd := providerCommandContext(context.Background(), path, want...)
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("args readback: %s %v", data, err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("batch argv: %#v want %#v", got, want)
	}
	for _, bad := range []string{"%PATH%", "x\ny", "a\"b", "!VARIABLE!"} {
		cmd := providerCommandContext(context.Background(), path, bad)
		if cmd.Err == nil {
			t.Fatalf("batch expansion input accepted: %q", bad)
		}
	}
}

func TestWindowsLifecycleRejectsOccupiedPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	original := []byte("preserve existing bytes")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if f, err := createPrivateProviderFile(path); err == nil {
		f.Close()
		t.Fatal("probe replaced existing file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(original) {
		t.Fatal("existing path changed")
	}
	if f, err := createPrivateProviderFile(t.TempDir()); err == nil {
		f.Close()
		t.Fatal("directory accepted as probe")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".grok"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := probeGrokBuildLifecycleState(&Task{ID: "negative", ActiveAttemptID: "attempt"}, home); err == nil {
		t.Fatal("non-directory state accepted")
	}
	linkedHome := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(linkedHome, ".grok")); err != nil {
		t.Fatal(err)
	}
	if err := probeGrokBuildLifecycleState(&Task{ID: "negative", ActiveAttemptID: "attempt"}, linkedHome); err == nil {
		t.Fatal("linked state directory accepted")
	}
}

func TestWindowsPrivateProbeRejectsUnsafeMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-probe")
	f, err := createPrivateProviderFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		t.Fatal(err)
	}
	sddl, _ := syscall.UTF16PtrFromString("D:P(A;;FA;;;" + sid + ")(A;;FA;;;SY)(A;;FA;;;BA)")
	var expected unsafe.Pointer
	ok, _, err := convertSecurityDescriptor.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&expected)), 0)
	if ok == 0 {
		t.Fatal(err)
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(expected)))
	name, _ := syscall.UTF16PtrFromString(path)
	h, err := syscall.CreateFile(name, 0x60000, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, 0, 0) // READ_CONTROL|WRITE_DAC
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(h)
	if err := verifyPrivateProbe(h, expected, sid); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateProbe(h, expected, "S-1-1-0"); err == nil {
		t.Fatal("wrong owner accepted")
	}
	if err := os.Link(path, path+".hardlink"); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateProbe(h, expected, sid); err == nil {
		t.Fatal("multiply-linked probe accepted")
	}
	if err := os.Remove(path + ".hardlink"); err != nil {
		t.Fatal(err)
	}
	// A null DACL grants Everyone access. Change only this disposable synthetic file.
	setSecurity := windowsSecurity.NewProc("SetSecurityInfo")
	code, _, _ := setSecurity.Call(uintptr(h), 1, 0x80000004, 0, 0, 0, 0)
	if code != 0 {
		t.Fatal(syscall.Errno(code))
	}
	if err := verifyPrivateProbe(h, expected, sid); err == nil {
		t.Fatal("null DACL accepted")
	}
}

func TestWindowsChild(t *testing.T) {
	if os.Getenv("CARDEX_WINDOWS_CHILD") == "sleep" {
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	}
}

// Capture an exact handle before killing the owner, including before any durable
// PID bind. Cleanup uses that handle even when the assertion reproduces a leak.
func TestWindowsCrashDuringCreationHasNoOrphan(t *testing.T) {
	for _, mode := range []string{"crash-before-job-bind", "crash-before-anchor-bind"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			workspace := t.TempDir()
			owner := exec.Command(os.Args[0], "--cardex-windows-fixture", mode, pidFile, workspace)
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { owner.Process.Kill(); owner.Wait() }()
			deadline := time.Now().Add(5 * time.Second)
			var pid int
			for time.Now().Before(deadline) {
				data, _ := os.ReadFile(pidFile)
				pid, _ = strconv.Atoi(string(data))
				if pid > 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("crash seam not reached")
			}
			h, err := syscall.OpenProcess(0x100001, false, uint32(pid)) // SYNCHRONIZE|TERMINATE
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.CloseHandle(h)
			defer syscall.TerminateProcess(h, 1)
			if !workspaceProcessResidue(workspace) {
				t.Fatal("active launch has no cross-process workspace lease")
			}
			if err := owner.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			owner.Wait()
			if !workspaceProcessResidue(workspace) && mode == "crash-before-job-bind" {
				state, err := syscall.WaitForSingleObject(h, 0)
				if err != nil || state != syscall.WAIT_OBJECT_0 {
					t.Fatal("workspace became free while owned child still alive")
				}
			}
			state, err := syscall.WaitForSingleObject(h, 3000)
			if err != nil || state != syscall.WAIT_OBJECT_0 {
				t.Fatalf("owner death leaked exact child: state=%d err=%v", state, err)
			}
			if _, err := os.Stat(pidFile + ".marker"); !os.IsNotExist(err) {
				t.Fatal("unbound provider ran code")
			}
		})
	}
}

func TestWindowsProtectedProcessIsNotDead(t *testing.T) {
	// Windows System is protected: inability to open it must not authorize lock theft.
	if !processAlive(4) {
		t.Fatal("protected/live System process reported dead")
	}
}

func TestWindowsJobRejectsBreakaway(t *testing.T) {
	cmd := exec.Command(os.Args[0], "--cardex-windows-fixture", "marker", filepath.Join(t.TempDir(), "marker"))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x01000000}
	if _, err := prepareTaskProcessLease(cmd, "", ""); err == nil || cmd.Process != nil {
		t.Fatal("breakaway launch admitted")
	}
}

func TestWindowsWorkspaceJobLeaseAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	if workspaceProcessResidue(dir) {
		t.Fatal("fresh workspace blocked")
	}
	cmd := exec.Command(os.Args[0])
	lease, err := prepareTaskProcessLease(cmd, "first", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.abort()
	observer := exec.Command(os.Args[0], "--cardex-windows-fixture", "workspace-probe", dir)
	if out, err := observer.CombinedOutput(); err != nil {
		t.Fatalf("other process missed lease: %s %v", out, err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	second, err := prepareTaskProcessLease(exec.Command(os.Args[0]), "second", alias)
	if second != nil {
		second.abort()
	}
	if !errors.Is(err, errWorkspaceExecutionLeaseBusy) {
		t.Fatalf("aliased workspace admitted second writer: %v", err)
	}
	other, err := prepareTaskProcessLease(exec.Command(os.Args[0]), "other", t.TempDir())
	if err != nil {
		t.Fatalf("unrelated workspace blocked: %v", err)
	}
	other.abort()
	lease.abort()
	if workspaceProcessResidue(dir) {
		t.Fatal("closed workspace job did not release")
	}
	if !workspaceProcessResidue(filepath.Join(dir, "missing")) {
		t.Fatal("unobservable workspace considered clear")
	}
}

func TestWindowsTickExecutesFreshTask(t *testing.T) {
	bin, calls := windowsFakeGrok(t, "ok")
	cfg := grokBuildTestConfig(t, bin)
	cfg.GrokBuild.OpusAdversarialReview = false
	cfg.DrainRescanSec = 1
	root := testRoot(t)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	task := queuedSequence(t, root, cfg, "Windows scheduler consumer", t.TempDir())
	task.RunnerExplicit = true
	task.PreferRunner = grokBuildRunnerName
	task.GrokModel = "grok-4.6"
	task.GrokEffort = "xhigh"
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := tick(root, cfg, false, true); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil || got.Status != statusDone || got.ActiveAttemptID != "" {
		t.Fatalf("scheduler did not execute fresh card: %+v %v", got, err)
	}
	data, err := os.ReadFile(calls)
	if err != nil || string(data) != "call\n" {
		t.Fatalf("unexpected provider calls: %q %v", data, err)
	}
	if workspaceProcessResidue(task.Dir) {
		t.Fatal("completed task retained its workspace job")
	}
}

func TestWindowsClaudeCodexBatchAdapters(t *testing.T) {
	for _, runner := range []string{"claude", "codex"} {
		t.Run(runner, func(t *testing.T) {
			root, task := windowsReservedTask(t)
			dir := filepath.Join(t.TempDir(), "provider 中文 & space")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			bin, record := filepath.Join(dir, runner+".cmd"), filepath.Join(dir, "stdin.txt")
			script := fmt.Sprintf("@echo off\r\n\"%s\" --cardex-windows-fixture stdin-provider %s \"%s\" %%*\r\n", os.Args[0], runner, record)
			if err := os.WriteFile(bin, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := testCfg()
			cfg.ClaudeBin, cfg.CodexBin = bin, bin
			cfg.StepTimeoutMin = 1
			prompt := "中文 prompt with %PATH% !literal! \"quoted\" & | > <\nsecond line"
			var result *claudeResult
			var err error
			if runner == "claude" {
				result, _, err = invokeClaudeCLI(context.Background(), cfg, task, prompt, "", nil)
			} else {
				result, _, err = invokeCodex(context.Background(), root, cfg, task, prompt)
			}
			if err != nil || result == nil || result.IsError || result.Result != "WINDOWS_"+strings.ToUpper(runner)+"_OK" {
				t.Fatalf("adapter result=%+v err=%v", result, err)
			}
			got, err := os.ReadFile(record)
			if err != nil || !strings.HasSuffix(string(got), prompt) {
				t.Fatalf("stdin changed: %q %v", got, err)
			}
			if workspaceProcessResidue(task.Dir) {
				t.Fatal("adapter left workspace job")
			}
		})
	}
}

func TestWindowsWaitDelayPreservesExecutionFailure(t *testing.T) {
	cmd := exec.Command(os.Args[0], "--cardex-windows-fixture", "args")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	failure := fmt.Errorf("%w: %w", errProcessExecution, exec.ErrWaitDelay)
	if got := rescueWaitDelay(failure, cmd); !errors.Is(got, errProcessExecution) {
		t.Fatalf("Windows process failure was erased: %v", got)
	}
}

// An observer may retain a process handle after cmd.Wait. Windows job accounting
// can still count that exited process; the signaled handle proves it cannot write.
func TestWindowsExitedJobProcessWithRetainedHandle(t *testing.T) {
	var retained syscall.Handle
	var observeErr error
	previous := afterCmdStart
	afterCmdStart = func(cmd *exec.Cmd) {
		retained, observeErr = syscall.OpenProcess(0x100000, false, uint32(cmd.Process.Pid))
	}
	defer func() {
		afterCmdStart = previous
		if retained != 0 {
			syscall.CloseHandle(retained)
		}
	}()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "--cardex-windows-fixture", "args")
	cmd.Dir = t.TempDir()
	setupProcGroup(cmd)
	if err := runCmdRegistered(cmd); err != nil {
		t.Fatalf("exited process was treated as a live descendant: %v", err)
	}
	if observeErr != nil || retained == 0 {
		t.Fatalf("failed to retain the process handle: %v", observeErr)
	}
	if status, err := syscall.WaitForSingleObject(retained, 0); err != nil || status != syscall.WAIT_OBJECT_0 {
		t.Fatalf("retained process is not signaled: status=%d err=%v", status, err)
	}
	if workspaceProcessResidue(cmd.Dir) {
		t.Fatal("completed command left its workspace job open")
	}
}
