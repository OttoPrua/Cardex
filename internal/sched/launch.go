package sched

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

func init() {
	id, ok := schedHostAttempt(os.Getenv, os.Args)
	if !ok {
		return
	}
	ctx := host.WithWorkspace(context.Background(), workspace.Git{})
	if err := host.Run(ctx, os.Getenv("CARDEX_HOME"), id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// schedHostAttempt reports the attempt id when this process is the detached
// host child. The host exports CARDEX_SCHED_HOST into every descendant,
// including go test and the fake executor. Those must keep their own main.
func schedHostAttempt(getenv func(string) string, args []string) (string, bool) {
	if getenv("CARDEX_SCHED_HOST") != "1" || getenv("CARDEX_FAKE_CHILD") == "1" || goTestArgs(args) {
		return "", false
	}
	id := attemptFromArgs(args)
	if id == "" {
		id = getenv("CARDEX_SCHED_ATTEMPT")
	}
	if id == "" {
		return "", false
	}
	return id, true
}

func goTestArgs(args []string) bool {
	if len(args) < 2 {
		return false
	}
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "-test.") || strings.HasPrefix(arg, "--test.") {
			return true
		}
	}
	return false
}

func attemptFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "host" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// launchDetached starts the current executable as `host <attempt-id>` in a
// new process group. CARDEX_SCHED_HOST tells this package's init to call
// host.Run, because the cardex host subcommand is still the R2-A stub.
func launchDetached(home, attemptID string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, "host", attemptID)
	cmd.Env = append(os.Environ(),
		"CARDEX_HOME="+home,
		"CARDEX_SCHED_HOST=1",
		"CARDEX_SCHED_ATTEMPT="+attemptID,
	)
	proc.Detach(cmd)
	dir := filepath.Join(home, "attempts", attemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "launcher.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return 0, err
	}
	go func() {
		_ = cmd.Wait()
		_ = logf.Close()
	}()
	return cmd.Process.Pid, nil
}

func waitPID(ctx context.Context, dbAttempt func() (int, bool)) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		if _, ok := dbAttempt(); ok {
			return
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
