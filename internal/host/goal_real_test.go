package host_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// TestKimiGoalRealSmoke runs the real kimi-code CLI in goal mode through
// host.Run in a throwaway Cardex home. It is opt-in: CARDEX_REAL_KIMI=1 and
// KIMI_CODE_HOME must name the logged-in kimi home. It spends a few thousand
// subscription tokens.
func TestKimiGoalRealSmoke(t *testing.T) {
	if os.Getenv("CARDEX_REAL_KIMI") != "1" || os.Getenv("KIMI_CODE_HOME") == "" {
		t.Skip("set CARDEX_REAL_KIMI=1 and KIMI_CODE_HOME to run the real kimi goal smoke")
	}
	if _, err := exec.LookPath("kimi"); err != nil {
		t.Skip("kimi not on PATH")
	}
	realGoalSmoke(t, model.RunnerKimi, "kimi-code/kimi-for-coding")
}

// TestCursorGoalRealSmoke is the same check against cursor-agent's /goal.
// Opt-in with CARDEX_REAL_CURSOR=1.
func TestCursorGoalRealSmoke(t *testing.T) {
	if os.Getenv("CARDEX_REAL_CURSOR") != "1" {
		t.Skip("set CARDEX_REAL_CURSOR=1 to run the real cursor goal smoke")
	}
	if _, err := exec.LookPath("cursor-agent"); err != nil {
		t.Skip("cursor-agent not on PATH")
	}
	realGoalSmoke(t, model.RunnerCursor, "grok-4.7-low")
}

func realGoalSmoke(t *testing.T, rn model.RunnerName, defaultModel string) {
	t.Helper()
	home, id, work := seedPrepared(t, t.TempDir(), rn, "", 1, nil)
	if err := os.WriteFile(filepath.Join(home, "attempts", id, "hard-duration"), []byte("150s"), 0o644); err != nil {
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
	att, err := db.GetAttempt(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	att.Route.Model = os.Getenv("CARDEX_SMOKE_MODEL")
	if att.Route.Model == "" {
		att.Route.Model = defaultModel
	}
	if err := db.UpdateAttempt(ctx, &att); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	prompt := "Create a file named done.txt in the current directory containing exactly the word OK.\nWhen the file exists, mark the goal complete.\n"
	if err := os.WriteFile(filepath.Join(home, "prompt.md"), []byte(prompt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := host.Run(ctx, home, id); err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("CARDEX_SMOKE_OUT"); out != "" {
		if raw, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log")); err == nil {
			_ = os.WriteFile(out, raw, 0o644)
		}
	}
	attempt, events := loadAttempt(t, home, id)
	finishEv, _ := eventType(events, "host_finish")
	exitEv, _ := eventType(events, "goal_process_exit")
	t.Logf("finish %s", finishEv.Data)
	t.Logf("exit %s", exitEv.Data)
	var finished struct {
		GoalStatus string `json:"goal_status"`
	}
	_ = json.Unmarshal(finishEv.Data, &finished)
	if finished.GoalStatus != "complete" || attempt.ErrorClass != "" {
		t.Fatalf("goal %q class %q", finished.GoalStatus, attempt.ErrorClass)
	}
	raw, err := os.ReadFile(filepath.Join(work, "done.txt"))
	if err != nil || !strings.Contains(string(raw), "OK") {
		t.Fatalf("done.txt %q %v", raw, err)
	}
}
