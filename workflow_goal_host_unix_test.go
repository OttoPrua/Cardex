//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeHostedGrokBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	py := filepath.Join(dir, "grok.py")
	body := `#!/usr/bin/env python3
import json, os, sys, urllib.parse
args = sys.argv[1:]
cwd = ""
sid = ""
prev = ""
for a in args:
    if a in ("-p", "--single", "--prompt-file"):
        sys.stderr.write("p-not-goal\n")
        sys.exit(2)
    if prev == "--cwd":
        cwd = a
    if prev in ("-s", "--resume", "--session-id"):
        sid = a
    prev = a
home = os.environ.get("GROK_HOME", "")
if not home or not cwd or not sid:
    sys.stderr.write("missing grok home/cwd/session\n")
    sys.exit(2)
enc = urllib.parse.quote(cwd, safe="")
sess = os.path.join(home, "sessions", enc, sid)
os.makedirs(os.path.join(sess, "goal"), exist_ok=True)
goal = "hosted-goal-1"

def write_state(status, classifier="", event="goal_updated"):
    st = {
        "goal_id": goal,
        "status": status,
        "phase": "Idle" if status != "active" else "Executing",
        "token_budget": 0,
        "last_classifier_verdict": classifier,
        "total_verify_rounds": 0,
        "total_worker_rounds": 1,
        "history": [{"event": event}],
    }
    with open(os.path.join(sess, "goal", "state.json"), "w") as f:
        json.dump(st, f)
        f.write("\n")
    with open(os.path.join(sess, "summary.json"), "w") as f:
        json.dump({"info": {"id": sid, "cwd": cwd}}, f)
        f.write("\n")
    line = {
        "method": "_x.ai/session/update",
        "params": {
            "sessionId": sid,
            "update": {
                "sessionUpdate": "goal_updated",
                "goal_id": goal,
                "status": status,
                "phase": "idle" if status != "active" else "executing",
                "last_classifier_verdict": classifier,
                "last_event": event,
            },
        },
    }
    with open(os.path.join(sess, "updates.jsonl"), "a") as f:
        f.write(json.dumps(line) + "\n")

write_state("active", event="goal_started")
buf = ""
while True:
    ch = sys.stdin.read(1)
    if ch == "":
        break
    if ch == "\x1b":
        continue
    if ch in ("\n", "\r"):
        line = buf.strip()
        buf = ""
        if not line:
            continue
        if line.startswith("/goal pause"):
            write_state("paused", event="goal_paused")
        elif line.startswith("/goal resume"):
            write_state("active", event="goal_resumed")
        elif line.startswith("/goal clear") or line.startswith("/quit"):
            write_state("complete", "achieved", "goal_completed")
            break
        elif line.startswith("/goal ") and not line.startswith("/goal status"):
            write_state("active", event="goal_started")
    else:
        buf += ch
`
	if err := os.WriteFile(py, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "grok")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec python3 -u "+shSingleQuote(py)+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func TestHostedGoalInjectPauseStopAndSync(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg.GrokBuildBin = fakeHostedGrokBin(t)
	cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
	saveGoalCfg(t, root, cfg)
	wf := initTestWorkflow(t, root, dir)
	tk := admitManualWriter(t, root, cfg, wf)

	errCh := make(chan error, 1)
	go func() {
		errCh <- launchHostedWorkflowGoal(root, cfg, wf, 0)
	}()

	deadline := time.Now().Add(8 * time.Second)
	var sess string
	for time.Now().Before(deadline) {
		loaded, err := loadTask(root, tk.ID)
		if err == nil && loaded.SessionID != "" {
			sess = loaded.SessionID
			state := filepath.Join(grokGoalSessionDir(home, dir, sess), "goal", "state.json")
			if _, err := os.Stat(state); err == nil {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if sess == "" {
		t.Fatal("hosted launch did not bind session")
	}
	obs, err := observeNativeGrokGoal(home, dir, sess, "")
	if err != nil || obs.NativeStatus != "active" {
		t.Fatalf("expected active after inject: obs=%+v err=%v", obs, err)
	}

	if err := requestGoalControl(root, cfg, wf, goalControlPause, home); err != nil {
		t.Fatalf("hosted pause: %v", err)
	}
	obs, err = observeNativeGrokGoal(home, dir, sess, "")
	if err != nil || obs.NativeStatus != "paused" {
		t.Fatalf("native pause post-state: %+v err=%v", obs, err)
	}
	loaded, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if _, err := loadGoalHostStatus(root, loaded.ID, firstNonBlank(loaded.ActiveAttemptID, loaded.Goal.BoundAttemptID)); err != nil {
		t.Logf("host-status missing after pause (native post-state is source of truth): %v", err)
	}

	if err := requestGoalControl(root, cfg, wf, goalControlStop, home); err != nil {
		t.Fatalf("hosted stop: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("hosted run: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("hosted supervisor did not return after stop")
	}

	synced, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{GrokHome: home, GrokCWD: dir})
	if err != nil {
		t.Fatalf("goal-sync: %v", err)
	}
	if synced.Goal.Observation != goalObsDone {
		t.Fatalf("expected done, got %s note=%s", synced.Goal.Observation, synced.Goal.ObservationNote)
	}
	if strings.Contains(synced.Goal.ObservationNote, "stream_incomplete") {
		t.Fatal("must not collapse to stream_incomplete")
	}
}

func TestInjectHostedControlPrefixesEscBeforePause(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pty-capture")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := injectHostedControl(f, goalControlPause, "/goal pause"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 3 || raw[0] != 0x1b || raw[1] != 0x1b {
		t.Fatalf("pause must send Esc Esc before slash, got %q", raw)
	}
	if !strings.Contains(string(raw), "/goal pause") {
		t.Fatalf("missing slash command: %q", raw)
	}
}

func TestOpenGoalPTYMatchesShippedHelper(t *testing.T) {
	master, slave, err := openGoalPTY()
	if err != nil {
		t.Fatalf("openGoalPTY: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if !fileIsTerminal(slave) {
		t.Fatal("PTY slave must be a terminal")
	}
}

// Exercise the production hosted launcher with real PTY backpressure, no provider.
func TestHostedGoalDrainsOutputAndDeadline(t *testing.T) {
	for _, mode := range []string{"exit", "deadline", "broken-output"} {
		waitForDeadline := mode != "exit"
		t.Run(mode, func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			cfg := workflowTestCfg(t, root)
			t.Setenv("GROK_HOME", t.TempDir())
			marker := filepath.Join(t.TempDir(), "stdin-received")
			fake := filepath.Join(t.TempDir(), "synthetic-grok")
			body := fmt.Sprintf(`#!/usr/bin/env python3
import json, os, pathlib, sys, time
# Exceeds the kernel PTY buffer before the child reads /goal.
sys.stdout.write("PTY-OUT-" * 65536)
sys.stdout.flush()
sys.stderr.write("PTY-ERR-visible\n")
sys.stderr.flush()
line = sys.stdin.readline()
assert line.startswith("/goal "), repr(line)
pathlib.Path(%q).write_text(line)
# Let the production launcher bind the actual PID before returning.
deadline = time.monotonic() + 4
while time.monotonic() < deadline:
    if any(json.loads(p.read_text()).get("pid") == os.getpid()
           for p in pathlib.Path(%q).rglob("*.json")):
        break
    time.sleep(0.01)
else:
    sys.exit(2)
if %s:
    time.sleep(30)
else:
    sys.stdout.write("PTY-TAIL-" * 131072 + "PTY-END\n")
    sys.stdout.flush()
`, marker, filepath.Join(root, "control", "attempts"), map[bool]string{true: "True", false: "False"}[waitForDeadline])
			if err := os.WriteFile(fake, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			cfg.GrokBuildBin = fake
			cfg.GrokBuild = &GrokBuildRoute{Enabled: true, Model: "grok-4.6", Effort: "xhigh"}
			saveGoalCfg(t, root, cfg)
			wf := initTestWorkflow(t, root, dir)
			tk := admitManualWriter(t, root, cfg, wf)
			tk.Goal.HardTimeoutSec = 6
			if err := saveTask(root, tk); err != nil {
				t.Fatal(err)
			}
			// An ordinary file is the caller's output sink, not a product log.
			out, err := os.CreateTemp(t.TempDir(), "synthetic-output")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "broken-output" {
				name := out.Name()
				out.Close()
				out, err = os.Open(name) // The caller sink cannot be written.
				if err != nil {
					t.Fatal(err)
				}
			}
			defer out.Close()
			oldStdout := os.Stdout
			os.Stdout = out
			start := time.Now()
			warnings := captureStderr(t, func() { err = launchHostedWorkflowGoal(root, cfg, wf, 0) })
			os.Stdout = oldStdout
			if !waitForDeadline && strings.Contains(warnings, "output may be incomplete") {
				t.Fatal(warnings)
			}
			if err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed > 9*time.Second {
				t.Fatalf("cleanup took %v", elapsed)
			}
			if mode == "broken-output" {
				if !strings.Contains(warnings, "hosted PTY output may be incomplete") {
					t.Fatal("missing visible output failure warning")
				}
			} else if _, err := os.Stat(marker); err != nil {
				t.Fatalf("child blocked before stdin: %v", err)
			}
			raw, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			if mode != "broken-output" && (strings.Count(string(raw), "PTY-OUT-") != 65536 || !strings.Contains(string(raw), "PTY-ERR-visible")) {
				t.Fatal("hosted PTY output was lost instead of relayed to the caller")
			}
			if !waitForDeadline && (strings.Count(string(raw), "PTY-TAIL-") != 131072 || !strings.Contains(string(raw), "PTY-END")) {
				t.Fatal("final PTY output truncated on exit")
			}
			fresh, err := loadTask(root, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Goal.Observation != goalObsUnknown || !fresh.Goal.CustodyReleased || fresh.ActiveAttemptID != "" {
				t.Fatalf("exit must release custody without claiming native completion: %+v", fresh.Goal)
			}
			if waitForDeadline && !strings.Contains(fresh.Goal.ObservationNote, "hard timeout") {
				t.Fatal(fresh.Goal.ObservationNote)
			}
		})
	}
}

func TestUnknownSuccessorRequiresRecoveredCustodyAndRetainsHistory(t *testing.T) {
	for _, blocked := range []string{"missing-attempt", "held-lease", "held-lease-failed", "round-limit", "stale-receipt", ""} {
		t.Run(blocked, func(t *testing.T) {
			root, cfg, wf, writer, receipt := d4SuccessorFixture(t)
			writer.Goal.LastNativeStatus = ""
			writer.Goal.BudgetTokens = 1234
			writer.Goal.HardTimeoutSec = 60
			if err := saveTask(root, writer); err != nil {
				t.Fatal(err)
			}
			rewriteDesignReceipt(t, receipt, map[string]any{"consumed_revision": writer.Revision, "consumed_observation": goalObsUnknown})
			switch blocked {
			case "missing-attempt":
				writer.Goal.BoundAttemptID = "missing"
				writer.ActiveAttemptID = "missing"
				if err := saveTask(root, writer); err != nil {
					t.Fatal(err)
				}
				rewriteDesignReceipt(t, receipt, map[string]any{"consumed_attempt_id": "missing", "consumed_revision": writer.Revision})
			case "held-lease", "held-lease-failed":
				if blocked == "held-lease-failed" {
					writer.Status = statusFailed
					if err := saveTask(root, writer); err != nil {
						t.Fatal(err)
					}
					rewriteDesignReceipt(t, receipt, map[string]any{"consumed_revision": writer.Revision})
				}
				lease, err := acquireWorkspaceExecutionLease(writer.Dir)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
			case "round-limit":
				wf.CurrentRound = wf.MaxRounds
				if err := persistWorkflow(root, cfg, wf); err != nil {
					t.Fatal(err)
				}
			case "stale-receipt":
				rewriteDesignReceipt(t, receipt, map[string]any{"consumed_revision": writer.Revision - 1})
			}
			next, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, goalObsUnknown, receipt)
			if blocked != "" {
				if err == nil {
					t.Fatalf("%s admitted a successor", blocked)
				}
				actual, _ := loadWorkflow(root, cfg, wf.ID)
				if actual.WriterTaskID != writer.ID {
					t.Fatal("rejected successor changed writer")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if next.ID == writer.ID || next.Goal.Started || next.SessionID != "" || next.ActiveAttemptID != "" || next.Goal.BoundAttemptID != "" {
				t.Fatalf("successor must be a distinct unstarted writer: %+v", next)
			}
			if next.Goal.BudgetTokens != 1234 || next.Goal.HardTimeoutSec != 60 || next.Goal.Provider != writer.Goal.Provider || next.PreferRunner != writer.PreferRunner {
				t.Fatalf("successor widened the contract: %+v", next)
			}
			old, err := loadTask(root, writer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if old.Status != statusFailed || old.Goal.Observation != goalObsUnknown || old.Goal.EvidenceComplete || old.Goal.LastNativeStatus != "" {
				t.Fatalf("retirement must preserve unknown native history: %+v", old)
			}
			if !old.Goal.CustodyReleased || old.ActiveAttemptID != "" || wf.CurrentRound != 1 {
				t.Fatalf("custody/round: %+v", old)
			}
			if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, goalObsUnknown, receipt); err == nil {
				t.Fatal("consumed decision replayed")
			}
		})
	}
}
