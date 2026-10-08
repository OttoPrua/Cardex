package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	completionVerifyReasonPassed            = "verify_passed"
	completionVerifyReasonFailed            = "verify_failed"
	completionVerifyReasonTimeout           = "verify_timeout"
	completionVerifyReasonCanceled          = "verify_canceled"
	completionVerifyReasonUnsupportedRemote = "verify_unsupported_remote"
	completionVerifyReasonError             = "verify_error"
	completionVerifyActor                   = "runner:completion-verify"
)

var (
	errCompletionVerifyUnsupportedRemote = errors.New("completion verify unsupported on remote host")
	completionVerifyTimeout              = harvestVerifyTimeout
)

type completionVerifyResult struct {
	Command string
	Shell   string
	Dir     string
	Remote  bool
	Exit    int
	Tail    string
	Err     error
	Reason  string
	Passed  bool
}

func completionVerifyOptedIn(t *Task) bool {
	return t != nil && t.VerifyOnSuccess && strings.TrimSpace(t.Verify) != ""
}

func completionVerifyShellLine() string {
	name, prefix := localCompletionShell()
	if len(prefix) == 0 {
		return name
	}
	return strings.TrimSpace(name + " " + strings.Join(prefix, " "))
}

func applyCompletionVerify(ctx context.Context, root string, cfg *Config, t *Task, lg *os.File) error {
	if !completionVerifyOptedIn(t) {
		return nil
	}
	res := runCompletionVerify(ctx, cfg, t)
	revoked := root != "" && t != nil && (diskCanceled(root, t.ID) || diskControlRevoked(root, t.ID))
	if revoked {
		res.Reason = completionVerifyReasonCanceled
		res.Passed = false
		if res.Err == nil {
			res.Err = context.Canceled
		}
	}
	recordCompletionVerifyOnTask(t, res)
	if lg != nil {
		logBlock(lg, "COMPLETION_VERIFY", fmt.Sprintf("reason=%s exit=%d shell=%s dir=%s tail=%s",
			res.Reason, res.Exit, res.Shell, res.Dir, res.Tail))
	}
	if res.Passed {
		return nil
	}
	if revoked {
		return context.Canceled
	}
	return holdCompletionVerify(root, t, res)
}

func recordCompletionVerifyOnTask(t *Task, res completionVerifyResult) {
	if t == nil {
		return
	}
	if t.Harvest == nil {
		t.Harvest = &HarvestEvidence{At: time.Now().Format(time.RFC3339), Attempt: t.Attempts}
	}
	t.Harvest.VerifyCommand = res.Command
	exit := res.Exit
	t.Harvest.VerifyExit = &exit
	t.Harvest.VerifyTail = res.Tail
	note := "completion_verify " + res.Reason + " shell=" + res.Shell
	if res.Remote {
		note += " remote=1 cwd=" + res.Dir
	}
	if res.Err != nil {
		note += " err=" + res.Err.Error()
	}
	// The current result replaces our prior trailing note after a legal retry;
	// the earlier failure remains in the event log, not in the success metadata.
	diagnostics := t.Harvest.Diagnostics
	if strings.HasPrefix(diagnostics, "completion_verify ") {
		diagnostics = ""
	} else if before, _, found := strings.Cut(diagnostics, "; completion_verify "); found {
		diagnostics = before
	}
	if diagnostics == "" {
		t.Harvest.Diagnostics = note
	} else {
		t.Harvest.Diagnostics = diagnostics + "; " + note
	}
}

func completionVerifyEventDetail(t *Task) map[string]any {
	detail := map[string]any{}
	if t == nil || t.Harvest == nil || strings.TrimSpace(t.Harvest.VerifyCommand) == "" {
		return detail
	}
	detail["verify_command"] = t.Harvest.VerifyCommand
	if t.Harvest.VerifyExit != nil {
		detail["verify_exit"] = *t.Harvest.VerifyExit
	}
	if t.Harvest.VerifyTail != "" {
		detail["verify_tail"] = t.Harvest.VerifyTail
	}
	if strings.Contains(t.Harvest.Diagnostics, "shell=") {
		detail["verify_shell"] = t.Harvest.Diagnostics
	}
	return detail
}

func mergeCompletionVerifyDetail(detail map[string]any, t *Task) map[string]any {
	extra := completionVerifyEventDetail(t)
	if len(extra) == 0 {
		return detail
	}
	if detail == nil {
		detail = map[string]any{}
	}
	for k, v := range extra {
		if _, exists := detail[k]; !exists {
			detail[k] = v
		}
	}
	return detail
}

func holdCompletionVerify(root string, t *Task, res completionVerifyResult) error {
	t.Status = statusHeld
	msg := "completion verify " + res.Reason
	if res.Err != nil {
		msg += ": " + res.Err.Error()
	} else if res.Tail != "" {
		msg += ": " + firstLine(res.Tail)
	}
	t.LastError = msg
	t.touch()
	detail := map[string]any{
		"reason":         "completion_verify_" + res.Reason,
		"reason_class":   "completion_verify",
		"verify_command": res.Command,
		"verify_exit":    res.Exit,
		"verify_tail":    res.Tail,
		"verify_shell":   res.Shell,
	}
	if res.Remote {
		detail["verify_remote"] = true
		detail["verify_cwd"] = res.Dir
	}
	if res.Err != nil {
		detail["err"] = res.Err.Error()
	}
	return finishIfStopped(persistTaskEvent(root, t, evHeld, completionVerifyActor, statusHeld, t.Step,
		withCostTelemetry(detail, t)))
}

func runCompletionVerify(ctx context.Context, cfg *Config, t *Task) completionVerifyResult {
	res := completionVerifyResult{
		Command: t.Verify,
		Shell:   completionVerifyShellLine(),
		Dir:     t.Dir,
		Exit:    -1,
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, completionVerifyTimeout)
	defer cancel()

	var out []byte
	var exit int
	var err error
	if strings.TrimSpace(t.RemoteHost) != "" {
		res.Remote = true
		if cfg != nil {
			if rh, ok := cfg.RemoteHosts[t.RemoteHost]; ok {
				if rh.Shell == "posix" {
					res.Shell = "ssh /bin/sh -c"
				} else {
					res.Shell = "ssh cmd.exe /C"
				}
			}
		}
		out, exit, err = runRemoteCompletionVerify(ctx, cfg, t)
	} else {
		out, exit, err = runLocalCompletionVerify(ctx, t)
	}
	res.Exit = exit
	res.Err = err
	res.Tail = tailUTF8Bytes(string(out), harvestVerifyTailCap)
	res.Reason = classifyCompletionVerify(ctx, err, exit)
	res.Passed = res.Reason == completionVerifyReasonPassed
	return res
}

func classifyCompletionVerify(ctx context.Context, err error, exit int) string {
	if errors.Is(err, errCompletionVerifyUnsupportedRemote) {
		return completionVerifyReasonUnsupportedRemote
	}
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return completionVerifyReasonTimeout
	}
	if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return completionVerifyReasonCanceled
	}
	if err != nil && exit < 0 {
		return completionVerifyReasonError
	}
	if exit != 0 {
		return completionVerifyReasonFailed
	}
	return completionVerifyReasonPassed
}

func runLocalCompletionVerify(ctx context.Context, t *Task) ([]byte, int, error) {
	name, prefix := localCompletionShell()
	args := append(append([]string{}, prefix...), t.Verify)
	cmd := exec.CommandContext(ctx, name, args...)
	if t != nil && t.Dir != "" {
		cmd.Dir = t.Dir
	}
	return runCompletionVerifyCmd(ctx, cmd, t)
}

func runRemoteCompletionVerify(ctx context.Context, cfg *Config, t *Task) ([]byte, int, error) {
	if t == nil || strings.TrimSpace(t.RemoteHost) == "" {
		return nil, -1, errCompletionVerifyUnsupportedRemote
	}
	if cfg == nil {
		return nil, -1, fmt.Errorf("%w: missing config", errCompletionVerifyUnsupportedRemote)
	}
	rh, ok := cfg.RemoteHosts[t.RemoteHost]
	if !ok {
		return nil, -1, fmt.Errorf("%w: host %q is not configured", errCompletionVerifyUnsupportedRemote, t.RemoteHost)
	}
	sshBin := strings.TrimSpace(cfg.SSHBin)
	if sshBin == "" {
		sshBin = "ssh"
	}
	remoteCwd := t.Dir
	remoteCmd := remoteCompletionVerifyCommand(rh.Shell, remoteCwd, t.Verify)
	cmd := exec.CommandContext(ctx, sshBin, "-o", "BatchMode=yes", t.RemoteHost, remoteCmd)
	return runCompletionVerifyCmd(ctx, cmd, t)
}

func remoteCompletionVerifyCommand(shell, cwd, verify string) string {
	if shell == "posix" {
		// shellQuote keeps $ ` and spaces inside Verify/cwd from the outer ssh shell.
		return "cd " + shellQuote(cwd) + " && /bin/sh -c " + shellQuote(verify)
	}
	dir := strings.ReplaceAll(cwd, "/", `\`)
	// cmd.exe `&` still runs the right-hand side after a failed cd; `&&` matches
	// invokeRemoteClaude and requires the remote cwd change to succeed.
	return "cd /d " + cmdDoubleQuote(dir) + " && cmd.exe /C " + cmdDoubleQuote(verify)
}

func cmdDoubleQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func runCompletionVerifyCmd(ctx context.Context, cmd *exec.Cmd, t *Task) ([]byte, int, error) {
	setupProcGroup(cmd)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	taskID := ""
	if t != nil {
		taskID = t.ID
	}
	var err error
	if taskID != "" {
		if _, ok := taskExecRoot.Load(taskID); ok {
			err = runCmdRegisteredForTaskWorkspace(cmd, taskID, cmd.Dir)
		} else {
			err = runCmdRegistered(cmd)
		}
	} else {
		err = runCmdRegistered(cmd)
	}
	if cmd.Process != nil {
		_ = killProcGroup(cmd.Process.Pid)
	}
	if ctx != nil && ctx.Err() != nil {
		return buf.Bytes(), -1, ctx.Err()
	}
	if err == nil {
		return buf.Bytes(), 0, nil
	}
	if errors.Is(err, errAdmissionDenied) || errors.Is(err, errProducerInvalidated) {
		return buf.Bytes(), -1, context.Canceled
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return buf.Bytes(), ee.ExitCode(), nil
	}
	return buf.Bytes(), -1, err
}
