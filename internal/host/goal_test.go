package host_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestGoalModeSetsFieldsAndConsumesExit(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		runGoal(t, "#!/bin/sh\nsleep 1\nexit 0\n", false, 0)
	})
	t.Run("crash", func(t *testing.T) {
		runGoal(t, "#!/bin/sh\nsleep 1\nkill -KILL $$\n", true, 9)
	})
}

func runGoal(t *testing.T, script string, wantCrash bool, wantSignal int) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "grok")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GROK_HOME", t.TempDir())
	home, id, work := seedPrepared(t, t.TempDir(), model.RunnerGrok, "", 1, nil)
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	card, err := db.GetCard(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	card.Requires = []string{"goal_mode"}
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	prompt := "GOAL-PROMPT-R7\n"
	if err := os.WriteFile(filepath.Join(home, "prompt.md"), []byte(prompt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	_, events := loadAttempt(t, home, id)
	start, ok := eventType(events, "host_start")
	if !ok || strings.Contains(string(start.Data), "--prompt-file") {
		t.Fatalf("goal argv %s", start.Data)
	}
	if !strings.Contains(string(start.Data), "--cwd") {
		t.Fatalf("goal argv missing cwd %s", start.Data)
	}
	ev, ok := eventType(events, "grok_process_exit")
	if !ok {
		t.Fatal("missing grok_process_exit")
	}
	var got struct {
		Signaled   bool   `json:"signaled"`
		Signal     int    `json:"signal"`
		Crash      bool   `json:"crash"`
		GoalMode   bool   `json:"goal_mode"`
		Prompt     string `json:"prompt"`
		SessionDir string `json:"session_dir"`
	}
	if err := json.Unmarshal(ev.Data, &got); err != nil {
		t.Fatal(err)
	}
	wantDir := runner.GrokGoalSessionDir(work, id)
	if !got.GoalMode || got.Prompt != prompt || got.SessionDir != wantDir {
		t.Fatalf("fields %+v want dir %s", got, wantDir)
	}
	if got.Crash != wantCrash || got.Signaled != wantCrash || got.Signal != wantSignal {
		t.Fatalf("exit fact %+v", got)
	}
}

func TestGoalTerminalStaysOpenUntilSignaled(t *testing.T) {
	cases := []struct {
		status string
		class  model.ErrorClass
	}{
		{status: "complete", class: ""},
		{status: "budget_limited", class: model.ErrorBudgetLimited},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			verdict := ""
			if tc.status == "complete" {
				verdict = "achieved"
			}
			bin := t.TempDir()
			path := filepath.Join(bin, "grok")
			if err := os.WriteFile(path, []byte(goalStayScript(tc.status, verdict)), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			t.Setenv("GROK_HOME", t.TempDir())
			home, id, work := seedPrepared(t, t.TempDir(), model.RunnerGrok, "", 1, nil)
			if err := os.WriteFile(filepath.Join(home, "attempts", id, "hard-duration"), []byte("15s"), 0o644); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(home, "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			card, err := db.GetCard(ctx, "c1")
			if err != nil {
				t.Fatal(err)
			}
			card.Requires = []string{"goal_mode"}
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if err := host.Run(context.Background(), home, id); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Fatalf("goal %s took %s; the open TUI was not finished", tc.status, elapsed)
			}
			attempt, events := loadAttempt(t, home, id)
			if attempt.ErrorClass != tc.class {
				t.Fatalf("error class %q, want %q", attempt.ErrorClass, tc.class)
			}
			startEv, ok := eventType(events, "host_start")
			if !ok || !strings.Contains(string(startEv.Data), `"-s"`) || !strings.Contains(string(startEv.Data), `"`+id+`"`) {
				t.Fatalf("argv %s", startEv.Data)
			}
			exitEv, ok := eventType(events, "grok_process_exit")
			if !ok {
				t.Fatal("missing grok_process_exit")
			}
			var exited struct {
				Signaled   bool   `json:"signaled"`
				Signal     int    `json:"signal"`
				Crash      bool   `json:"crash"`
				SessionDir string `json:"session_dir"`
			}
			if err := json.Unmarshal(exitEv.Data, &exited); err != nil {
				t.Fatal(err)
			}
			wantDir := runner.GrokGoalSessionDir(work, id)
			if exited.SessionDir != wantDir || !exited.Signaled || exited.Signal != 15 || exited.Crash {
				t.Fatalf("exit %+v want dir %s signal 15", exited, wantDir)
			}
			finishEv, ok := eventType(events, "host_finish")
			if !ok {
				t.Fatal("missing host_finish")
			}
			var finished struct {
				ErrorClass string `json:"error_class"`
				Stop       string `json:"stop"`
				GoalStatus string `json:"goal_status"`
			}
			if err := json.Unmarshal(finishEv.Data, &finished); err != nil {
				t.Fatal(err)
			}
			if finished.GoalStatus != tc.status || finished.Stop == "hard_limit_timeout" || finished.ErrorClass == string(model.ErrorTimeout) {
				t.Fatalf("finish %+v", finished)
			}
			raw, err := os.ReadFile(filepath.Join(wantDir, "goal", "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), tc.status) {
				t.Fatalf("session state %s", raw)
			}
		})
	}
}

func TestGoalTerminalIgnoredSignalStillFinishes(t *testing.T) {
	cases := []struct {
		status string
		class  model.ErrorClass
	}{
		{status: "complete", class: ""},
		{status: "budget_limited", class: model.ErrorBudgetLimited},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			verdict := ""
			if tc.status == "complete" {
				verdict = "achieved"
			}
			bin := t.TempDir()
			path := filepath.Join(bin, "grok")
			if err := os.WriteFile(path, []byte(goalIgnoreScript(tc.status, verdict)), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			t.Setenv("GROK_HOME", t.TempDir())
			// The shell is the session leader, so its SIGTERM trap is the group.
			t.Setenv("CARDEX_OUTER_SANDBOX", "1")
			home, id, work := seedPrepared(t, t.TempDir(), model.RunnerGrok, "", 1, nil)
			if err := os.WriteFile(filepath.Join(home, "attempts", id, "hard-duration"), []byte("6s"), 0o644); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(home, "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			card, err := db.GetCard(ctx, "c1")
			if err != nil {
				t.Fatal(err)
			}
			card.Requires = []string{"goal_mode"}
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			errCh := make(chan error, 1)
			start := time.Now()
			go func() {
				errCh <- host.Run(context.Background(), home, id)
			}()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("goal reaper still waiting after SIGTERM was ignored")
			}
			if elapsed := time.Since(start); elapsed > 12*time.Second {
				t.Fatalf("goal %s took %s after an ignored SIGTERM", tc.status, elapsed)
			}
			attempt, events := loadAttempt(t, home, id)
			if attempt.Exit == nil || attempt.EndedAt.IsZero() {
				t.Fatalf("attempt was not persisted: exit %v ended %v", attempt.Exit, attempt.EndedAt)
			}
			if attempt.ErrorClass != tc.class {
				t.Fatalf("error class %q, want %q", attempt.ErrorClass, tc.class)
			}
			exitEv, ok := eventType(events, "grok_process_exit")
			if !ok {
				t.Fatal("missing grok_process_exit")
			}
			var exited struct {
				Signaled bool `json:"signaled"`
				Signal   int  `json:"signal"`
				Crash    bool `json:"crash"`
			}
			if err := json.Unmarshal(exitEv.Data, &exited); err != nil {
				t.Fatal(err)
			}
			if !exited.Signaled || exited.Signal == 15 || exited.Crash {
				t.Fatalf("exit %+v: ignored SIGTERM must be reaped without an executor crash", exited)
			}
			finishEv, ok := eventType(events, "host_finish")
			if !ok {
				t.Fatal("missing host_finish")
			}
			var finished struct {
				ErrorClass string `json:"error_class"`
				Stop       string `json:"stop"`
				GoalStatus string `json:"goal_status"`
			}
			if err := json.Unmarshal(finishEv.Data, &finished); err != nil {
				t.Fatal(err)
			}
			if finished.GoalStatus != tc.status || finished.Stop == "hard_limit_timeout" || finished.ErrorClass == string(model.ErrorTimeout) {
				t.Fatalf("finish %+v", finished)
			}
			if tc.status == "budget_limited" && finished.ErrorClass != string(model.ErrorBudgetLimited) {
				t.Fatalf("finish %+v", finished)
			}
			raw, err := os.ReadFile(filepath.Join(runner.GrokGoalSessionDir(work, id), "goal", "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), tc.status) {
				t.Fatalf("session state %s", raw)
			}
		})
	}
}

func goalIgnoreScript(status, verdict string) string {
	script := goalStayScript(status, verdict)
	script = strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\ntrap '' TERM\n", 1)
	return strings.Replace(script, "/bin/sleep 60\n", "while true; do\n  /bin/sleep 30\ndone\n", 1)
}

func goalStayScript(status, verdict string) string {
	return `#!/bin/sh
status=` + shellQuote(status) + `
verdict=` + shellQuote(verdict) + `
sid=""
cwd=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-s" ]; then sid=$arg; fi
  if [ "$prev" = "--cwd" ]; then cwd=$arg; fi
  prev=$arg
done
if [ -z "$sid" ] || [ -z "$cwd" ] || [ -z "$GROK_HOME" ]; then
  echo "missing session binding sid=$sid cwd=$cwd" >&2
  exit 2
fi
/usr/bin/python3 - "$GROK_HOME" "$cwd" "$sid" "$status" "$verdict" <<'PY'
import hashlib, json, os, sys
from urllib.parse import quote
home, cwd, sid, status, verdict = sys.argv[1:6]
enc = quote(cwd, safe="~")
if len(enc) > 255:
    base = os.path.basename(cwd.replace(os.sep, "-"))
    if base in ("", ".", "/"):
        base = "cwd"
    if len(base) > 40:
        base = base[:40]
    enc = base + "-" + hashlib.sha256(cwd.encode()).hexdigest()[:16]
root = os.path.join(home, "sessions", enc, sid)
os.makedirs(os.path.join(root, "goal"), exist_ok=True)
with open(os.path.join(root, "goal", "state.json"), "w") as f:
    json.dump({"goal_id": "g1", "status": status, "last_classifier_verdict": verdict}, f)
with open(os.path.join(root, "updates.jsonl"), "w") as f:
    json.dump({"params": {"sessionId": "s1", "update": {"sessionUpdate": "goal_updated", "goal_id": "g1", "status": status, "last_classifier_verdict": verdict}}}, f)
    f.write("\n")
with open(os.path.join(root, "summary.json"), "w") as f:
    json.dump({"info": {"id": "s1"}}, f)
PY
if [ "$?" -ne 0 ]; then
  exit 2
fi
/bin/sleep 60
`
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
