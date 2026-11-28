package sched

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

func init() {
	// Internal host startup is only `host <attempt-id>` plus the marker
	// launchDetached sets. An inherited CARDEX_SCHED_ATTEMPT must not pull
	// version, card add, or any other ordinary CLI back into host.Run.
	if os.Getenv("CARDEX_SCHED_HOST") != "1" || os.Getenv(proc.ManagedExecutorEnv) == "1" {
		return
	}
	id := attemptFromArgs(os.Args)
	if id == "" || os.Getenv("CARDEX_SCHED_ATTEMPT") != id {
		return
	}
	ctx := host.WithWorkspace(context.Background(), workspace.Git{})
	if err := host.Run(ctx, os.Getenv("CARDEX_HOME"), id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// attemptFromArgs accepts only the argv launchDetached builds: the executable,
// then the command host, then one attempt id. A later host token, an extra
// argument, or a different command stays on the ordinary CLI path.
func attemptFromArgs(args []string) string {
	if len(args) != 3 || args[1] != "host" || args[2] == "" {
		return ""
	}
	return args[2]
}

// launchDetached starts the current executable as `host <attempt-id>` in a
// new process group. CARDEX_SCHED_HOST tells this package's init to call
// host.Run. The attempt argument must match the launcher marker.
func launchDetached(ctx context.Context, home, attemptID, dir string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, "host", attemptID)
	ctx = runner.WithCompatibleCredentialEnv(ctx, home)
	cmd.Env = append(proc.CompatibleChildEnv(ctx, os.Environ()),
		"CARDEX_HOME="+home,
		"CARDEX_SCHED_HOST=1",
		"CARDEX_SCHED_ATTEMPT="+attemptID,
	)
	proc.Detach(cmd)
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
