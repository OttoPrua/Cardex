package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestM3LaunchGitEvidenceBeforeAttemptRow(t *testing.T) {
	for _, tt := range []struct {
		name, command, kind string
		noRepo              bool
	}{
		{"missing base", "git rev-parse", "missing_base", false},
		{"missing repository", "git show-ref", "not_repository", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertLaunchGitEvidenceBeforeAttemptRow(t, tt.command, tt.kind, tt.noRepo)
		})
	}
}

func assertLaunchGitEvidenceBeforeAttemptRow(t *testing.T, command, kind string, noRepo bool) {
	t.Helper()
	ctx := context.Background()
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	if noRepo {
		if err := os.Rename(filepath.Join(repo, ".git"), filepath.Join(repo, ".git-disabled")); err != nil {
			t.Fatal(err)
		}
	}
	badBase := "private-basename-do-not-print"
	card := model.Card{ID: "c-git-failed", Objective: obj, Kind: model.KindDev, Title: "bad base", Base: badBase, Status: model.CardReady, Tier: model.TierT2, Channel: "cha", Model: "model-a"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) {
		t.Fatal("invalid base reached runner")
		return 0, nil
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("diagnostic should not create an attempt row: %+v %v", attempts, err)
	}
	events := eventsOfType(t, db, "card:"+card.ID, model.EventLaunchFailed)
	if len(events) != 1 {
		t.Fatalf("expected one launch_failed event, got %+v", events)
	}
	var payload map[string]string
	if err := json.Unmarshal(events[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	s := payload["error"]
	match := regexp.MustCompile(`attempt="(a[a-f0-9]{16})"`).FindStringSubmatch(s)
	if len(match) != 2 {
		t.Fatalf("missing pre-row attempt in %q", s)
	}
	worktree := filepath.Join(filepath.Dir(repo), "wt", card.ID+"-"+match[1])
	for _, field := range []string{"command=" + strconv.Quote(command), "kind=" + kind, `card="` + card.ID + `"`, `worktree=` + strconv.Quote(worktree)} {
		if !strings.Contains(s, field) {
			t.Errorf("missing %s in %q", field, s)
		}
	}
	if strings.Contains(s, "\n") || strings.Contains(strings.ToLower(s), "usage:") || strings.Contains(s, badBase) {
		t.Fatalf("usage or Git arguments leaked: %q", s)
	}
}
