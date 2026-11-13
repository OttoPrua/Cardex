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
	"github.com/OttoPrua/Cardex/internal/store"
)

// kimiGoalScript plays kimi-code's TUI: the trust dialog, the input box, then
// it records the goal in the session wire and stays open until signaled.
func kimiGoalScript(update string) string {
	return `#!/bin/sh
[ -n "$KIMI_CODE_HOME" ] || { echo "no KIMI_CODE_HOME" >&2; exit 2; }
[ "$KIMI_CODE_NO_AUTO_UPDATE" = 1 ] || { echo "auto update on" >&2; exit 2; }
for a in "$@"; do case "$a" in --prompt|--yolo|--auto|-y) echo "bad flag $a" >&2; exit 2;; esac; done
printf 'Trust this folder?\r\n  %s\r\n  > Trust and continue\r\n    Exit\r\n' "$PWD"
IFS= read -r ans
printf '\033[2J > \r\n Never Ask  context: 0%% (0/1M)\r\n'
IFS= read -r goal
case "$goal" in */goal*GOAL-KIMI-R1*) ;; *) echo "no goal line" >&2; exit 3;; esac
dir="$KIMI_CODE_HOME/sessions/wd_w_1/session_1"
mkdir -p "$dir/agents/main"
printf '%s' "$goal" > "$KIMI_CODE_HOME/goal-line"
cwd=$(pwd -W 2>/dev/null || pwd)
printf '{"id":"session_1","cwd":"%s","createdAt":1}\n' "$cwd" > "$dir/state.json"
printf '{"type":"goal.create","goalId":"g1","objective":"o"}\n` + update + `\n' > "$dir/agents/main/wire.jsonl"
while :; do sleep 1; done
`
}

func TestKimiGoalModeHost(t *testing.T) {
	cases := []struct {
		name   string
		update string
		status string
		class  model.ErrorClass
	}{
		{name: "complete", update: `{"type":"goal.update","status":"complete"}`, status: "complete"},
		{name: "budget", update: `{"type":"goal.update","status":"blocked","reason":"Blocked after goal budget reached"}`, status: "budget_limited", class: model.ErrorBudgetLimited},
		{name: "paused", update: `{"type":"goal.update","status":"paused","reason":"Paused after runtime error"}`, status: "paused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "kimi"), []byte(kimiGoalScript(tc.update)), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			kh := t.TempDir()
			t.Setenv("KIMI_CODE_HOME", kh)
			home, id, _ := seedPrepared(t, t.TempDir(), model.RunnerKimi, "", 1, nil)
			if err := os.WriteFile(filepath.Join(home, "attempts", id, "hard-duration"), []byte("20s"), 0o644); err != nil {
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
			if err := os.WriteFile(filepath.Join(home, "prompt.md"), []byte("GOAL-KIMI-R1 first\nsecond\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if err := host.Run(ctx, home, id); err != nil {
				t.Fatal(err)
			}
			if el := time.Since(start); el > 15*time.Second {
				t.Fatalf("goal took %s; the open TUI was not finished", el)
			}
			attempt, events := loadAttempt(t, home, id)
			if attempt.ErrorClass != tc.class {
				t.Fatalf("error class %q want %q", attempt.ErrorClass, tc.class)
			}
			startEv, ok := eventType(events, "host_start")
			if !ok || !strings.Contains(string(startEv.Data), `"pty":true`) && !strings.Contains(string(startEv.Data), `"drive":true`) {
				t.Fatalf("host_start %s", startEv.Data)
			}
			line, err := os.ReadFile(filepath.Join(kh, "goal-line"))
			if err != nil {
				t.Fatalf("goal never submitted: %v", err)
			}
			if !strings.Contains(string(line), "/goal \x1b[200~GOAL-KIMI-R1 first") {
				t.Fatalf("goal line %q", line)
			}
			exitEv, ok := eventType(events, "goal_process_exit")
			if !ok {
				t.Fatal("missing goal_process_exit")
			}
			var exited struct {
				Runner     string `json:"runner"`
				Crash      bool   `json:"crash"`
				SessionDir string `json:"session_dir"`
			}
			if err := json.Unmarshal(exitEv.Data, &exited); err != nil {
				t.Fatal(err)
			}
			if exited.Runner != "kimi" || exited.Crash || !strings.HasSuffix(filepath.ToSlash(exited.SessionDir), "session_1") {
				t.Fatalf("exit %+v", exited)
			}
			finishEv, _ := eventType(events, "host_finish")
			var finished struct {
				GoalStatus string `json:"goal_status"`
				DriveError string `json:"drive_error"`
				Stop       string `json:"stop"`
			}
			_ = json.Unmarshal(finishEv.Data, &finished)
			if finished.GoalStatus != tc.status {
				t.Fatalf("finish %+v want goal_status %q", finished, tc.status)
			}
			if tc.status == "paused" && !strings.Contains(finished.DriveError, "paused") {
				t.Fatalf("paused goal not reported: %+v", finished)
			}
		})
	}
}
