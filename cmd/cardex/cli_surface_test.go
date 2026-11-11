package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"reflect"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/importer"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/positioning"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/question"
	"github.com/OttoPrua/Cardex/internal/sched"
	"github.com/OttoPrua/Cardex/internal/scorecard"
	"github.com/OttoPrua/Cardex/internal/store"
)

func callCLI(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, errb lockBuf
	code := Main(args, &out, &errb)
	return code, out.String(), errb.String()
}

func mustHome(t *testing.T, home string, args ...string) string {
	t.Helper()
	full := append([]string{"--home", home}, args...)
	code, out, errb := callCLI(t, full)
	if code != 0 {
		t.Fatalf("cardex %s\nexit %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), code, out, errb)
	}
	return out
}

func withDB(t *testing.T, home string, fn func(*store.Store)) {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fn(db)
}

func TestCommandHelpNamesPurposeAndParameters(t *testing.T) {
	commands := []struct {
		args []string
		want []string
	}{
		{[]string{"serve", "--help"}, []string{"--addr"}},
		{[]string{"project", "--help"}, []string{"add", "--json"}},
		{[]string{"project", "add", "--help"}, []string{"-repo", "-id"}},
		{[]string{"project", "list", "--help"}, []string{"--json"}},
		{[]string{"project", "show", "--help"}, []string{"<id>", "--json"}},
		{[]string{"project", "set", "--help"}, []string{"-repo", "--json"}},
		{[]string{"objective", "--help"}, []string{"add", "<id>", "--origin", "--json"}},
		{[]string{"objective", "add", "--help"}, []string{"-origin", "-project", "--json"}},
		{[]string{"objective", "list", "--help"}, []string{"--json"}},
		{[]string{"objective", "show", "--help"}, []string{"<id>", "--json"}},
		{[]string{"objective", "set", "--help"}, []string{"-mode", "--json"}},
		{[]string{"objective", "pause", "--help"}, []string{"<id>", "--json"}},
		{[]string{"objective", "resume", "--help"}, []string{"<id>", "--json"}},
		{[]string{"objective", "cancel", "--help"}, []string{"<id>", "--json"}},
		{[]string{"node", "--help"}, []string{"list", "show", "freeze", "reopen", "<id>", "--json"}},
		{[]string{"node", "list", "--help"}, []string{"--json"}},
		{[]string{"node", "show", "--help"}, []string{"<id>", "--json"}},
		{[]string{"node", "freeze", "--help"}, []string{"node freeze\n", "<id>", "--json"}},
		{[]string{"node", "reopen", "--help"}, []string{"node reopen\n", "<id>", "--json"}},
		{[]string{"card", "--help"}, []string{"add", "<id>", "--origin", "--json"}},
		{[]string{"card", "add", "--help"}, []string{"-origin", "-objective", "--json"}},
		{[]string{"card", "list", "--help"}, []string{"--json"}},
		{[]string{"card", "show", "--help"}, []string{"<id>", "--json"}},
		{[]string{"card", "cancel", "--help"}, []string{"<id>", "--json"}},
		{[]string{"card", "retry", "--help"}, []string{"<id>", "--json"}},
		{[]string{"card", "pause", "--help"}, []string{"<id>", "--json"}},
		{[]string{"card", "resume", "--help"}, []string{"<id>", "--json"}},
		{[]string{"card", "revive", "--help"}, []string{"<id>", "--json"}},
		{[]string{"question", "--help"}, []string{"answer", "<id>", "<option>", "--json"}},
		{[]string{"question", "list", "--help"}, []string{"--json"}},
		{[]string{"question", "show", "--help"}, []string{"<id>", "--json"}},
		{[]string{"question", "answer", "--help"}, []string{"<id>", "<option>"}},
		{[]string{"channel", "--help"}, []string{"list", "show", "<id>", "--json"}},
		{[]string{"channel", "list", "--help"}, []string{"--json"}},
		{[]string{"channel", "show", "--help"}, []string{"<id>", "--json"}},
		{[]string{"channel", "set", "--help"}, []string{"-runner", "-redline", "--json"}},
		{[]string{"channel", "check", "--help"}, []string{"no command arguments"}},
		{[]string{"route", "--help"}, []string{"explain", "<card>"}},
		{[]string{"route", "explain", "--help"}, []string{"<card>", "--json"}},
		{[]string{"positioning", "--help"}, []string{"show", "proposals", "approve", "reject", "<id>", "--json"}},
		{[]string{"positioning", "show", "--help"}, []string{"--json"}},
		{[]string{"positioning", "proposals", "--help"}, []string{"--json"}},
		{[]string{"positioning", "approve", "--help"}, []string{"<id>"}},
		{[]string{"positioning", "reject", "--help"}, []string{"<id>"}},
		{[]string{"schedule", "--help"}, []string{"list", "run", "<name>", "--json"}},
		{[]string{"schedule", "list", "--help"}, []string{"--json"}},
		{[]string{"schedule", "run", "--help"}, []string{"<name>"}},
		{[]string{"scorecard", "--help"}, []string{"no command arguments"}},
		{[]string{"import-legacy", "--help"}, []string{"<v1-home>", "-dry-run"}},
		{[]string{"doctor", "--help"}, []string{"no command arguments"}},
		{[]string{"host", "--help"}, []string{"<attempt-id>"}},
	}
	for _, tc := range commands {
		code, out, errb := callCLI(t, tc.args)
		if code != 0 {
			t.Fatalf("%v exit %d\n%s\n%s", tc.args, code, out, errb)
		}
		if !strings.Contains(out, "purpose") || !strings.Contains(out, "parameters") {
			t.Fatalf("%v help missing purpose or parameters\n%s", tc.args, out)
		}
		if strings.Contains(out, "未实现") {
			t.Fatalf("%v still stub\n%s", tc.args, out)
		}
		for _, needle := range tc.want {
			if !strings.Contains(out, needle) {
				t.Fatalf("%v help missing %q\n%s", tc.args, needle, out)
			}
		}
	}
	_, out, _ := callCLI(t, []string{"project", "add", "--help"})
	if strings.Contains(out, "import-legacy [--dry-run]") {
		t.Fatalf("project add help was swallowed\n%s", out)
	}
	_, freeze, _ := callCLI(t, []string{"node", "freeze", "--help"})
	if strings.Contains(freeze, "node frozen") {
		t.Fatalf("freeze help uses the status name\n%s", freeze)
	}
	_, reopen, _ := callCLI(t, []string{"node", "reopen", "--help"})
	if strings.Contains(reopen, "node reopened") {
		t.Fatalf("reopen help uses the status name\n%s", reopen)
	}
}

func TestRecordCommandsRoundTrip(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	other := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "--json", "project", "set", "--id", "p1", "--repo", other, "--test-cmd", "go test ./internal/importer")
	var project model.Project
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "project", "show", "p1")), &project); err != nil {
		t.Fatal(err)
	}
	if project.RepoPath != other || project.TestCmd != "go test ./internal/importer" {
		t.Fatalf("project %+v", project)
	}
	var projects []model.Project
	if err := json.Unmarshal([]byte(mustHome(t, home, "project", "list", "--json")), &projects); err != nil || len(projects) != 1 {
		t.Fatalf("projects %s %v", mustHome(t, home, "project", "list", "--json"), err)
	}

	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Light the lamp", "--mode", "auto")
	mustHome(t, home, "objective", "set", "--id", "o1", "--mode", "manual", "--priority", "3")
	mustHome(t, home, "objective", "pause", "o1")
	var paused model.Objective
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "objective", "show", "o1")), &paused); err != nil {
		t.Fatal(err)
	}
	if paused.Status != model.ObjectivePaused || paused.Mode != model.ModeManual || paused.Priority != 3 {
		t.Fatalf("paused %+v", paused)
	}
	mustHome(t, home, "objective", "resume", "o1")
	var resumed model.Objective
	if err := json.Unmarshal([]byte(mustHome(t, home, "objective", "show", "--json", "o1")), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Status != model.ObjectiveActive {
		t.Fatalf("resumed %+v", resumed)
	}
	var objectives []model.Objective
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "objective", "list")), &objectives); err != nil || len(objectives) != 1 {
		t.Fatal(objectives)
	}

	mustHome(t, home, "card", "add", "--id", "c1", "--objective", "o1", "--title", "Write the chart", "--kind", "dev")
	for _, verb := range []struct {
		name   string
		status model.CardStatus
	}{
		{"pause", model.CardPaused},
		{"resume", model.CardReady},
		{"cancel", model.CardCanceled},
		{"revive", model.CardReady},
		{"retry", model.CardReady},
	} {
		mustHome(t, home, "card", verb.name, "c1")
		var card model.Card
		if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "card", "show", "c1")), &card); err != nil {
			t.Fatal(err)
		}
		if card.Status != verb.status {
			t.Fatalf("%s status %s", verb.name, card.Status)
		}
	}
	var cards []model.Card
	if err := json.Unmarshal([]byte(mustHome(t, home, "card", "list", "--json", "--objective", "o1")), &cards); err != nil || len(cards) != 1 {
		t.Fatalf("cards %+v", cards)
	}
	mustHome(t, home, "objective", "cancel", "o1")
	var canceled model.Objective
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "objective", "show", "o1")), &canceled); err != nil {
		t.Fatal(err)
	}
	if canceled.Status != model.ObjectiveCanceled {
		t.Fatalf("canceled %+v", canceled)
	}
}

func TestOriginFlagOverridesEnv(t *testing.T) {
	t.Setenv("CARDEX_ORIGIN", "envkind:env-session")
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o-flag", "--project", "p1", "--title", "Flagged", "--origin", "flagkind:flag-session")
	mustHome(t, home, "objective", "add", "--id", "o-env", "--project", "p1", "--title", "From env")
	var flagged, fromEnv model.Objective
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "objective", "show", "o-flag")), &flagged); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "objective", "show", "o-env")), &fromEnv); err != nil {
		t.Fatal(err)
	}
	if flagged.Origin != "flagkind:flag-session" || fromEnv.Origin != "envkind:env-session" {
		t.Fatalf("flag %q env %q", flagged.Origin, fromEnv.Origin)
	}
	mustHome(t, home, "card", "add", "--id", "c-flag", "--objective", "o-flag", "--title", "Flagged card", "--origin", "flagkind:card-session")
	mustHome(t, home, "card", "add", "--id", "c-env", "--objective", "o-env", "--title", "Env card")
	type shown struct {
		ID     string `json:"id"`
		Origin string `json:"origin"`
	}
	var cardFlag, cardEnv shown
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "card", "show", "c-flag")), &cardFlag); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "card", "show", "c-env")), &cardEnv); err != nil {
		t.Fatal(err)
	}
	if cardFlag.Origin != "flagkind:card-session" || cardEnv.Origin != "envkind:env-session" {
		t.Fatalf("card flag %q env %q", cardFlag.Origin, cardEnv.Origin)
	}
	text := mustHome(t, home, "card", "show", "c-flag")
	if !strings.Contains(text, "flagkind:card-session") {
		t.Fatalf("text show %q", text)
	}
}

func TestNodeQuestionAndChannel(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Chart the coast")
	withDB(t, home, func(db *store.Store) {
		node := model.DesignNode{ID: "n1", Objective: "o1", Layer: model.LayerModule, Status: model.NodeFreezable}
		if err := db.CreateNode(context.Background(), &node); err != nil {
			t.Fatal(err)
		}
		q := model.Question{
			ID: "q1", Objective: "o1", Kind: model.QuestionCustom, Text: "Which lamp?",
			Options: []string{"keep the lamp", "drop the lamp"},
		}
		if err := db.CreateQuestion(context.Background(), &q); err != nil {
			t.Fatal(err)
		}
	})
	var nodes []model.DesignNode
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "node", "list")), &nodes); err != nil || len(nodes) != 1 || nodes[0].ID != "n1" {
		t.Fatalf("nodes %s", mustHome(t, home, "--json", "node", "list"))
	}
	mustHome(t, home, "node", "freeze", "n1")
	var frozen model.DesignNode
	if err := json.Unmarshal([]byte(mustHome(t, home, "node", "show", "--json", "n1")), &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Status != model.NodeFrozen {
		t.Fatalf("frozen %+v", frozen)
	}
	mustHome(t, home, "node", "reopen", "n1")
	var reopened model.DesignNode
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "node", "show", "n1")), &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.Status != model.NodeReopened {
		t.Fatalf("reopened %+v", reopened)
	}

	var questions []model.Question
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "question", "list")), &questions); err != nil || len(questions) != 1 {
		t.Fatalf("questions %+v", questions)
	}
	mustHome(t, home, "question", "answer", "q1", "keep the lamp")
	var answered model.Question
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "question", "show", "q1")), &answered); err != nil {
		t.Fatal(err)
	}
	if answered.Answer != "keep the lamp" {
		t.Fatalf("answer %+v", answered)
	}
	code, _, errb := callCLI(t, []string{"--home", home, "question", "answer", "q1", "nope"})
	if code == 0 || !strings.Contains(errb, "not one of the stored choices") {
		t.Fatalf("bad option exit %d %s", code, errb)
	}
	code, _, errb = callCLI(t, []string{"--home", home, "question", "answer", "q1"})
	if code == 0 || !strings.Contains(errb, "empty answer") {
		t.Fatalf("empty exit %d %s", code, errb)
	}
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "question", "show", "q1")), &answered); err != nil {
		t.Fatal(err)
	}
	if answered.Answer != "keep the lamp" {
		t.Fatalf("answer changed %+v", answered)
	}

	mustHome(t, home, "channel", "set", "--id", "ch1", "--runner", "grok", "--redline", "0.25", "--model", "lumen-model", "--window", "daily:fixed:100")
	var channels []model.Channel
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "channel", "list")), &channels); err != nil || len(channels) != 1 || channels[0].ID != "ch1" {
		t.Fatalf("channels %+v", channels)
	}
	var channel model.Channel
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "channel", "show", "ch1")), &channel); err != nil {
		t.Fatal(err)
	}
	if channel.Runner != model.RunnerGrok || channel.Redline != 0.25 || len(channel.Models) != 1 || channel.Models[0] != "lumen-model" {
		t.Fatalf("channel %+v", channel)
	}
	if code, out, errb := callCLI(t, []string{"--home", home, "channel", "check"}); code != 0 {
		t.Fatalf("check %d\n%s\n%s", code, out, errb)
	}
}

func TestRouteExplainUsesStoredReason(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Route the ferry")
	mustHome(t, home, "card", "add", "--id", "c1", "--objective", "o1", "--title", "Cross the bar")
	reason := model.RouteReason{
		Candidates: []model.RouteCandidate{{
			Channel: "lumen-channel", Model: "lumen-model", Family: "lumen", Tier: model.TierT2, Preference: 2, Remaining: 0.8,
		}},
		Filtered: []model.FilteredCandidate{{
			Channel: "other-channel", Model: "other-model", Reason: "redline kept it out",
		}},
		Scores: []model.RouteScore{{
			Channel: "lumen-channel", Model: "lumen-model", Score: 0.42,
		}},
	}
	withDB(t, home, func(db *store.Store) {
		attempt := model.Attempt{ID: "a1", Card: "c1", Index: 3, RouteReason: reason}
		if err := db.CreateAttempt(context.Background(), &attempt); err != nil {
			t.Fatal(err)
		}
	})
	out := mustHome(t, home, "--json", "route", "explain", "c1")
	var got model.RouteReason
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reason)
	gotRaw, _ := json.Marshal(got)
	if string(raw) != string(gotRaw) {
		t.Fatalf("reason\n%s\ngot\n%s", raw, gotRaw)
	}
	if !strings.Contains(out, "lumen-channel") || !strings.Contains(out, "redline kept it out") || !strings.Contains(out, "0.42") {
		t.Fatalf("explain %s", out)
	}
}

func TestPositioningApproveUsesStore(t *testing.T) {
	home := t.TempDir()
	h, err := positioning.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	prop, err := h.Propose(context.Background(), []model.Positioning{{
		ID: "row-lumen", Model: "lumen-model", Family: "lumen", Tiers: []model.Tier{model.TierT2}, Preference: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	var proposals []storedProposal
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "positioning", "proposals")), &proposals); err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 1 || proposals[0].ID != prop.ID {
		t.Fatalf("proposals %+v want %s", proposals, prop.ID)
	}
	before := mustHome(t, home, "--json", "positioning", "show")
	if strings.Contains(before, "lumen-model") {
		t.Fatalf("current before approve %s", before)
	}
	mustHome(t, home, "positioning", "approve", prop.ID)
	after := mustHome(t, home, "--json", "positioning", "show")
	if !strings.Contains(after, "lumen-model") {
		t.Fatalf("current after approve %s", after)
	}
	h, err = positioning.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	rows, err := h.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.Model == "lumen-model" {
			found = true
		}
	}
	if !found {
		t.Fatalf("store current %+v", rows)
	}
	second, err := h.Propose(context.Background(), []model.Positioning{{
		ID: "row-other", Model: "other-model", Family: "other", Tiers: []model.Tier{model.TierT1}, Preference: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	code, _, errb := callCLI(t, []string{"--home", home, "positioning", "reject", second.ID})
	if code == 0 || !strings.Contains(errb, "unspecified") {
		t.Fatalf("reject %d %s", code, errb)
	}
	rows, err = h.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Model == "other-model" {
			t.Fatalf("reject changed current %+v", rows)
		}
	}
}

func TestScheduleRunEnqueuesOnce(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	bin, marker := sentinelBin(t, "grok", "cursor-agent", "kimi", "codex", "claude", "agy", "opencode")
	t.Setenv("PATH", bin)
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Sweep", "--mode", "manual")
	body := []byte(`{"schedules":[{"name":"lamp-sweep","every":"24h","card_template":{"objective":"o1","kind":"dev","title":"Sweep the lamp","body":"fictional sweep","requires":["web","goal_mode"]}}]}`)
	if err := os.WriteFile(filepath.Join(home, "config.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustHome(t, home, "schedule", "list"), "lamp-sweep") {
		t.Fatal(mustHome(t, home, "schedule", "list"))
	}
	var names []string
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "schedule", "list")), &names); err != nil || len(names) != 1 || names[0] != "lamp-sweep" {
		t.Fatalf("names %v", names)
	}
	out := mustHome(t, home, "schedule", "run", "lamp-sweep")
	if !strings.Contains(out, "Sweep the lamp") {
		t.Fatalf("run %s", out)
	}
	var cards []model.Card
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "card", "list")), &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].Title != "Sweep the lamp" || cards[0].Objective != "o1" || cards[0].Status != model.CardReady {
		t.Fatalf("cards %+v", cards)
	}
	if strings.Join(cards[0].Requires, ",") != "web,goal_mode" {
		t.Fatalf("requires %+v", cards[0].Requires)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("schedule started an executor: %v", err)
	}
}

func TestHostSubcommandUsesGit(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitCmd("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	gitCmd("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	wt := filepath.Join(home, "wt")
	cmd := exec.Command("git", "worktree", "add", "-b", "cardex/o/c/c", wt, "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree: %v\n%s", err, out)
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	proj := model.Project{ID: "p", RepoPath: repo, TestCmd: "true"}
	obj := model.Objective{ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive, IntegrationBranch: "main"}
	ch := model.Channel{ID: "ch", Runner: model.RunnerFake, Health: model.HealthOK, Models: []string{"fake"}}
	card := model.Card{ID: "c", Objective: "o", Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardRunning}
	if err := db.CreateProject(ctx, &proj); err != nil || db.CreateObjective(ctx, &obj) != nil || db.CreateChannel(ctx, &ch) != nil || db.CreateCard(ctx, &card) != nil {
		t.Fatal("seed")
	}
	id := "a-host"
	dir := filepath.Join(home, "attempts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := []byte(`{"attempts":{"0":{"write_files":[{"path":"from-host.txt","content":"ok\n"}],"exit_code":0}}}`)
	if err := os.WriteFile(filepath.Join(dir, "script.json"), script, 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(prompt, []byte("prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hard-duration"), []byte("30s"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt := model.Attempt{
		ID: id, Card: "c", Index: 0, Route: model.Route{Channel: "ch", Model: "fake"},
		Worktree: wt, PromptPath: prompt, StartedAt: time.Now().UTC(),
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr lockBuf
	if code := cmdHost(home, []string{id}, &stdout, &stderr); code != 0 {
		t.Fatalf("host %d %s", code, stderr.String())
	}
	db, err = store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.ListEvents(ctx, "attempt:"+id)
	if err != nil {
		t.Fatal(err)
	}
	data := ""
	for _, event := range events {
		if event.Type == "workspace_commit" {
			data = string(event.Data)
		}
	}
	if !strings.Contains(data, "head") || strings.Contains(data, "not implemented") {
		t.Fatalf("host subcommand workspace_commit %s", data)
	}
}

func TestScorecardSurfacesCompute(t *testing.T) {
	home := t.TempDir()
	dir, err := resolveHome(home)
	if err != nil {
		t.Fatal(err)
	}
	rows, compErr := scorecard.Compute(context.Background(), dir)
	code, out, errb := callCLI(t, []string{"--home", home, "scorecard"})
	if compErr != nil {
		if code == 0 {
			t.Fatalf("compute error %v exited 0\n%s", compErr, out)
		}
		msg := compErr.Error()
		if !strings.Contains(out, msg) && !strings.Contains(errb, msg) {
			t.Fatalf("stdout %q stderr %q want %q", out, errb, msg)
		}
		return
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	var got []scorecard.Row
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %s: %v", out, err)
	}
	want, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRaw) != string(want) {
		t.Fatalf("cli %s compute %s", gotRaw, want)
	}
}

func TestImportedHistoryStaysReadOnly(t *testing.T) {
	v1 := t.TempDir()
	home := t.TempDir()
	rows := []struct {
		rel, id, status string
	}{
		{"archive/done.json", "d1", "done"},
		{"archive/failed.json", "f1", "failed"},
		{"archive/canceled.json", "c1", "canceled"},
		{"tasks/bogus.json", "u1", "bogus"},
	}
	for _, row := range rows {
		writeLegacyFile(t, v1, row.rel, map[string]any{
			"id": row.id, "title": row.id, "status": row.status, "project": "lumen",
			"prompts": []string{"history " + row.id}, "step": 1,
		})
	}
	writeLegacyFile(t, v1, "tasks/held.json", map[string]any{
		"id": "h1", "title": "Held lamp", "status": "held", "project": "lumen",
		"hold_reason": "waiting on the lamp", "prompts": []string{"held prompt"}, "step": 2,
	})
	if _, err := importer.Import(context.Background(), home, v1, false); err != nil {
		t.Fatal(err)
	}
	verbs := []string{"retry", "resume", "revive", "pause", "cancel"}
	for _, row := range rows {
		id := "v1-" + row.id
		for _, verb := range verbs {
			var before model.Card
			withDB(t, home, func(db *store.Store) {
				var err error
				before, err = db.GetCard(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
			})
			mustHome(t, home, "card", verb, id)
			var after model.Card
			withDB(t, home, func(db *store.Store) {
				var err error
				after, err = db.GetCard(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
			})
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("%s %s changed\nbefore %+v\nafter %+v", row.status, verb, before, after)
			}
		}
	}
	var held model.Card
	withDB(t, home, func(db *store.Store) {
		var err error
		held, err = db.GetCard(context.Background(), "v1-h1")
		if err != nil {
			t.Fatal(err)
		}
	})
	mustHome(t, home, "card", "revive", "v1-h1")
	var revived model.Card
	withDB(t, home, func(db *store.Store) {
		var err error
		revived, err = db.GetCard(context.Background(), "v1-h1")
		if err != nil {
			t.Fatal(err)
		}
	})
	if revived.Status != model.CardReady || revived.Legacy.Reason != "waiting on the lamp" || revived.Legacy.Status != "held" {
		t.Fatalf("revived %+v", revived)
	}
	revived.Status = held.Status
	if !reflect.DeepEqual(revived, held) {
		t.Fatalf("revive changed more than status\nbefore %+v\nafter %+v", held, revived)
	}
}

func TestNodeReopenAndFreezeLoopEffects(t *testing.T) {
	t.Run("reopen", func(t *testing.T) {
		home, obj := seedNodeHome(t)
		withDB(t, home, func(db *store.Store) {
			node := model.DesignNode{ID: "n1", Objective: obj, Layer: model.LayerModule, Status: model.NodeFrozen}
			other := model.DesignNode{ID: "n2", Objective: obj, Layer: model.LayerModule, Status: model.NodeFrozen}
			if err := db.CreateNode(context.Background(), &node); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateNode(context.Background(), &other); err != nil {
				t.Fatal(err)
			}
			seedNodeCard(t, db, "ready", obj, "n1", model.KindDev, model.CardReady)
			seedNodeCard(t, db, "waiting", obj, "n1", model.KindDev, model.CardWaiting)
			seedNodeCard(t, db, "needs", obj, "n1", model.KindDev, model.CardNeedsInput)
			seedNodeCard(t, db, "running", obj, "n1", model.KindDev, model.CardRunning)
			seedNodeCard(t, db, "done", obj, "n1", model.KindDev, model.CardDone)
			seedNodeCard(t, db, "other", obj, "n2", model.KindDev, model.CardReady)
			open := model.Question{ID: "reopen-open", Objective: obj, Scope: "n1", ScopeKind: "node", Kind: model.QuestionReopen, Text: "open the node", Options: []string{"同意", "拒绝"}}
			consented := model.Question{ID: "reopen-yes", Objective: obj, Scope: "n1", ScopeKind: "node", Kind: model.QuestionReopen, Text: "already agreed", Options: []string{"同意", "拒绝"}, Answer: "同意"}
			rejected := model.Question{ID: "reopen-no", Objective: obj, Scope: "n1", ScopeKind: "node", Kind: model.QuestionReopen, Text: "already refused", Options: []string{"同意", "拒绝"}, Answer: "拒绝"}
			freeze := model.Question{ID: "freeze-1", Objective: obj, Scope: "n1", ScopeKind: "node", Kind: model.QuestionFreeze, Text: "freeze it", Options: []string{"同意", "拒绝"}, Answer: "同意"}
			for _, q := range []*model.Question{&open, &consented, &rejected, &freeze} {
				if err := db.CreateQuestion(context.Background(), q); err != nil {
					t.Fatal(err)
				}
			}
		})
		mustHome(t, home, "node", "reopen", "n1")
		withDB(t, home, func(db *store.Store) {
			node, err := db.GetNode(context.Background(), "n1")
			if err != nil || node.Status != model.NodeReopened {
				t.Fatalf("node %+v %v", node, err)
			}
			want := map[string]model.CardStatus{
				"ready": model.CardPaused, "waiting": model.CardPaused, "needs": model.CardPaused,
				"running": model.CardRunning, "done": model.CardDone, "other": model.CardReady,
			}
			for id, status := range want {
				card, err := db.GetCard(context.Background(), id)
				if err != nil || card.Status != status {
					t.Fatalf("%s %+v %v", id, card, err)
				}
				if status == model.CardPaused && (card.Status == model.CardReady || card.Status == model.CardRunning) {
					t.Fatalf("%s still dispatch-eligible", id)
				}
			}
			cards, err := db.ListCards(context.Background(), obj)
			if err != nil {
				t.Fatal(err)
			}
			var replans []model.Card
			for _, card := range cards {
				if card.Node == "n1" && card.Kind == model.KindReplan {
					replans = append(replans, card)
				}
			}
			if len(replans) != 1 || replans[0].Status != model.CardReady {
				t.Fatalf("replans %+v", replans)
			}
			notes := reopenNotes(t, db, "n1")
			if !noteUses(notes, "reopen-open") || !noteUses(notes, "reopen-yes") {
				t.Fatalf("notes %+v", notes)
			}
			open, err := db.GetQuestion(context.Background(), "reopen-open")
			if err != nil || open.Answer != open.Options[0] {
				t.Fatalf("open %+v %v", open, err)
			}
			rejected, err := db.GetQuestion(context.Background(), "reopen-no")
			if err != nil || rejected.Answer != "拒绝" || noteUses(notes, "reopen-no") {
				t.Fatalf("rejected %+v notes %+v %v", rejected, notes, err)
			}
		})
	})
	t.Run("in-flight replan", func(t *testing.T) {
		home, obj := seedNodeHome(t)
		withDB(t, home, func(db *store.Store) {
			node := model.DesignNode{ID: "n1", Objective: obj, Layer: model.LayerModule, Status: model.NodeFrozen}
			if err := db.CreateNode(context.Background(), &node); err != nil {
				t.Fatal(err)
			}
			seedNodeCard(t, db, "running-replan", obj, "n1", model.KindReplan, model.CardRunning)
			seedNodeCard(t, db, "ready", obj, "n1", model.KindDev, model.CardReady)
			q := model.Question{ID: "reopen-open", Objective: obj, Scope: "n1", ScopeKind: "node", Kind: model.QuestionReopen, Text: "again", Options: []string{"同意", "拒绝"}, Answer: "同意"}
			if err := db.CreateQuestion(context.Background(), &q); err != nil {
				t.Fatal(err)
			}
		})
		mustHome(t, home, "node", "reopen", "n1")
		withDB(t, home, func(db *store.Store) {
			running, err := db.GetCard(context.Background(), "running-replan")
			if err != nil || running.Status != model.CardRunning {
				t.Fatalf("running %+v %v", running, err)
			}
			ready, err := db.GetCard(context.Background(), "ready")
			if err != nil || ready.Status != model.CardPaused {
				t.Fatalf("ready %+v %v", ready, err)
			}
			cards, err := db.ListCards(context.Background(), obj)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, card := range cards {
				if card.Kind == model.KindReplan {
					n++
				}
			}
			if n != 1 || !noteUses(reopenNotes(t, db, "n1"), "reopen-open") {
				t.Fatalf("replans %d", n)
			}
		})
	})
	t.Run("freeze", func(t *testing.T) {
		home, obj := seedNodeHome(t)
		withDB(t, home, func(db *store.Store) {
			node := model.DesignNode{ID: "n1", Objective: obj, Layer: model.LayerModule, Status: model.NodeFreezable}
			other := model.DesignNode{ID: "n2", Objective: obj, Layer: model.LayerModule, Status: model.NodeFreezable}
			if err := db.CreateNode(context.Background(), &node); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateNode(context.Background(), &other); err != nil {
				t.Fatal(err)
			}
			freeze := model.Question{ID: "freeze-1", Objective: obj, Scope: "n1", ScopeKind: "node", Kind: model.QuestionFreeze, Text: "freeze it", Options: []string{"同意", "拒绝"}, Answer: "同意"}
			elsewhere := model.Question{ID: "freeze-2", Objective: obj, Scope: "n2", ScopeKind: "node", Kind: model.QuestionFreeze, Text: "other", Options: []string{"同意", "拒绝"}, Answer: "同意"}
			if err := db.CreateQuestion(context.Background(), &freeze); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateQuestion(context.Background(), &elsewhere); err != nil {
				t.Fatal(err)
			}
		})
		mustHome(t, home, "node", "freeze", "n1")
		withDB(t, home, func(db *store.Store) {
			node, err := db.GetNode(context.Background(), "n1")
			if err != nil || node.Status != model.NodeFrozen {
				t.Fatalf("node %+v %v", node, err)
			}
			notes := reopenNotes(t, db, "n1")
			if len(notes) != 0 {
				t.Fatalf("freeze wrote reopen notes %+v", notes)
			}
			q, err := db.GetQuestion(context.Background(), "freeze-1")
			if err != nil || q.Answer != "同意" {
				t.Fatalf("question %+v %v", q, err)
			}
			other, err := db.GetQuestion(context.Background(), "freeze-2")
			if err != nil || other.Answer != "同意" {
				t.Fatalf("other %+v %v", other, err)
			}
		})
	})
}

func TestNodeFreezeAndReopenCloseUnansweredFreeze(t *testing.T) {
	type qspec struct {
		id, scope, answer string
		kind              model.QuestionKind
	}
	cases := []struct {
		name        string
		verb        string
		questions   []qspec
		cards       bool
		closed      []string
		pending     map[string]string
		blocked     map[string]bool
		answers     map[string]string
		silent      []string
		reminded    []string
		reopenNotes int
		reopenFor   string
	}{
		{
			name:      "freeze-unanswered",
			verb:      "freeze",
			questions: []qspec{{id: "freeze-1", scope: "n1", kind: model.QuestionFreeze}},
			closed:    []string{"n1"},
			blocked:   map[string]bool{"n1": false},
			answers:   map[string]string{"freeze-1": "同意"},
			silent:    []string{"freeze-1"},
		},
		{
			name: "reopen-closes-freeze",
			verb: "reopen",
			questions: []qspec{
				{id: "freeze-1", scope: "n1", kind: model.QuestionFreeze},
				{id: "reopen-1", scope: "n1", kind: model.QuestionReopen},
			},
			cards:       true,
			closed:      []string{"n1"},
			blocked:     map[string]bool{"n1": false},
			answers:     map[string]string{"freeze-1": "同意", "reopen-1": "同意"},
			silent:      []string{"freeze-1", "reopen-1"},
			reopenNotes: 1,
			reopenFor:   "reopen-1",
		},
		{
			name: "other-node",
			verb: "freeze",
			questions: []qspec{
				{id: "freeze-1", scope: "n1", kind: model.QuestionFreeze},
				{id: "freeze-2", scope: "n2", kind: model.QuestionFreeze},
			},
			closed:   []string{"n1"},
			pending:  map[string]string{"n2": "freeze-2"},
			blocked:  map[string]bool{"n1": false, "n2": true},
			answers:  map[string]string{"freeze-1": "同意", "freeze-2": ""},
			silent:   []string{"freeze-1"},
			reminded: []string{"freeze-2"},
		},
		{
			name:      "refusal",
			verb:      "freeze",
			questions: []qspec{{id: "freeze-1", scope: "n1", kind: model.QuestionFreeze, answer: "拒绝"}},
			closed:    []string{"n1"},
			blocked:   map[string]bool{"n1": false},
			answers:   map[string]string{"freeze-1": "拒绝"},
			silent:    []string{"freeze-1"},
		},
		{
			name: "reopen-keeps-refusal",
			verb: "reopen",
			questions: []qspec{
				{id: "freeze-no", scope: "n1", kind: model.QuestionFreeze, answer: "拒绝"},
				{id: "reopen-1", scope: "n1", kind: model.QuestionReopen},
			},
			cards:       true,
			closed:      []string{"n1"},
			blocked:     map[string]bool{"n1": false},
			answers:     map[string]string{"freeze-no": "拒绝", "reopen-1": "同意"},
			silent:      []string{"freeze-no", "reopen-1"},
			reopenNotes: 1,
			reopenFor:   "reopen-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, obj := seedManualNodeHome(t)
			withDB(t, home, func(db *store.Store) {
				ctx := context.Background()
				status := model.NodeFreezable
				if tc.verb == "reopen" {
					status = model.NodeFrozen
				}
				for _, id := range []string{"n1", "n2"} {
					node := model.DesignNode{ID: id, Objective: obj, Layer: model.LayerModule, Status: status}
					if err := db.CreateNode(ctx, &node); err != nil {
						t.Fatal(err)
					}
				}
				if tc.cards {
					seedNodeCard(t, db, "ready", obj, "n1", model.KindDev, model.CardReady)
					seedNodeCard(t, db, "waiting", obj, "n1", model.KindDev, model.CardWaiting)
					seedNodeCard(t, db, "needs", obj, "n1", model.KindDev, model.CardNeedsInput)
					seedNodeCard(t, db, "running", obj, "n1", model.KindDev, model.CardRunning)
					seedNodeCard(t, db, "done", obj, "n1", model.KindDev, model.CardDone)
					seedNodeCard(t, db, "other", obj, "n2", model.KindDev, model.CardReady)
				}
				for _, spec := range tc.questions {
					q := model.Question{
						ID: spec.id, Objective: obj, Scope: spec.scope, ScopeKind: "node",
						Kind: spec.kind, Text: spec.id, Options: []string{"同意", "拒绝"}, Answer: spec.answer,
					}
					if err := db.CreateQuestion(ctx, &q); err != nil {
						t.Fatal(err)
					}
				}
			})
			mustHome(t, home, "node", tc.verb, "n1")
			withDB(t, home, func(db *store.Store) {
				ctx := context.Background()
				node, err := db.GetNode(ctx, "n1")
				if err != nil {
					t.Fatal(err)
				}
				wantStatus := model.NodeFrozen
				if tc.verb == "reopen" {
					wantStatus = model.NodeReopened
				}
				if node.Status != wantStatus {
					t.Fatalf("status %s", node.Status)
				}
				qs := question.New(db, nil)
				for _, scope := range tc.closed {
					pending, err := qs.Pending(ctx, scope)
					if err != nil {
						t.Fatal(err)
					}
					if len(pending) != 0 {
						t.Fatalf("scope %s still pending %+v", scope, pending)
					}
				}
				for scope, id := range tc.pending {
					pending, err := qs.Pending(ctx, scope)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, q := range pending {
						if q.ID == id {
							found = true
						}
					}
					if !found {
						t.Fatalf("scope %s pending %+v missing %s", scope, pending, id)
					}
				}
				for scope, want := range tc.blocked {
					blocked, err := qs.Blocked(ctx, scope)
					if err != nil {
						t.Fatal(err)
					}
					if blocked != want {
						t.Fatalf("scope %s blocked %v", scope, blocked)
					}
				}
				for id, want := range tc.answers {
					q, err := db.GetQuestion(ctx, id)
					if err != nil || q.Answer != want {
						t.Fatalf("%s answer %q %v", id, q.Answer, err)
					}
				}
				notes := reopenNotes(t, db, "n1")
				if len(notes) != tc.reopenNotes {
					t.Fatalf("notes %+v", notes)
				}
				if tc.reopenFor != "" && !noteUses(notes, tc.reopenFor) {
					t.Fatalf("notes %+v", notes)
				}
				if tc.cards {
					want := map[string]model.CardStatus{
						"ready": model.CardPaused, "waiting": model.CardPaused, "needs": model.CardPaused,
						"running": model.CardRunning, "done": model.CardDone, "other": model.CardReady,
					}
					for id, status := range want {
						card, err := db.GetCard(ctx, id)
						if err != nil || card.Status != status {
							t.Fatalf("%s %+v %v", id, card, err)
						}
					}
					cards, err := db.ListCards(ctx, obj)
					if err != nil {
						t.Fatal(err)
					}
					n := 0
					for _, card := range cards {
						if card.Node == "n1" && card.Kind == model.KindReplan {
							n++
							if card.Status != model.CardReady {
								t.Fatalf("replan %+v", card)
							}
						}
					}
					if n != 1 {
						t.Fatalf("replans %d", n)
					}
				}
			})
			binless := t.TempDir()
			t.Setenv("PATH", binless)
			var notified []string
			runner := sched.New(home)
			runner.Notify = func(_ context.Context, msg notify.Message) error {
				notified = append(notified, msg.QuestionID)
				return nil
			}
			if err := runner.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, id := range tc.silent {
				for _, got := range notified {
					if got == id {
						t.Fatalf("reminded %s in %v", id, notified)
					}
				}
			}
			for _, id := range tc.reminded {
				found := false
				for _, got := range notified {
					if got == id {
						found = true
					}
				}
				if !found {
					t.Fatalf("notify %v missing %s", notified, id)
				}
			}
		})
	}
}

func seedManualNodeHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Chart", "--mode", "manual")
	return home, "o1"
}

func seedNodeHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Chart")
	return home, "o1"
}

func seedNodeCard(t *testing.T, db *store.Store, id, obj, node string, kind model.CardKind, status model.CardStatus) {
	t.Helper()
	card := model.Card{ID: id, Objective: obj, Node: node, Kind: kind, Title: id, Body: id, Tier: model.TierT3, Status: status}
	if err := db.CreateCard(context.Background(), &card); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyFile(t *testing.T, root, rel string, body map[string]any) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func reopenNotes(t *testing.T, db *store.Store, nodeID string) []loopReopenNote {
	t.Helper()
	ev, err := db.ListEvents(context.Background(), "node:"+nodeID)
	if err != nil {
		t.Fatal(err)
	}
	var notes []loopReopenNote
	for _, e := range ev {
		if e.Type != loopReopenUsed || len(e.Data) == 0 {
			continue
		}
		var note loopReopenNote
		if err := json.Unmarshal(e.Data, &note); err != nil {
			t.Fatal(err)
		}
		notes = append(notes, note)
	}
	return notes
}

func noteUses(notes []loopReopenNote, id string) bool {
	for _, note := range notes {
		if note.Question == id {
			return true
		}
	}
	return false
}

func TestHostUnknownAttempt(t *testing.T) {
	home := t.TempDir()
	bin, marker := sentinelBin(t, "grok", "cursor-agent", "kimi", "codex", "claude", "agy", "opencode")
	t.Setenv("PATH", bin)
	code, out, errb := callCLI(t, []string{"--home", home, "host", "missing-attempt"})
	if code == 0 {
		t.Fatalf("exit 0 %s", out)
	}
	if !strings.Contains(errb, "host: load attempt") {
		t.Fatalf("stderr %s", errb)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("host launched a provider: %v", err)
	}
	code, _, errb = callCLI(t, []string{"--home", home, "host"})
	if code == 0 {
		t.Fatalf("missing id stderr %s", errb)
	}
}

func TestDoctorFindingsContinue(t *testing.T) {
	home := t.TempDir()
	bin, marker := sentinelBin(t, "grok")
	t.Setenv("PATH", bin)
	mustHome(t, home, "channel", "set", "--id", "zero", "--runner", "grok", "--redline", "0", "--window", "daily:fixed:10", "--model", "m-zero")
	mustHome(t, home, "channel", "set", "--id", "one", "--runner", "grok", "--redline", "1", "--window", "weekly:rolling:10", "--model", "m-one")
	mustHome(t, home, "channel", "set", "--id", "high", "--runner", "grok", "--redline", "2", "--window", "fortnight:rolling:10", "--model", "m-high")
	mustHome(t, home, "channel", "set", "--id", "low", "--runner", "grok", "--redline", "-1", "--window", "5h:whenever:10", "--model", "m-low")
	code, out, errb := callCLI(t, []string{"--home", home, "doctor"})
	if code != 0 {
		t.Fatalf("doctor exit %d\n%s\n%s", code, out, errb)
	}
	if strings.TrimSpace(errb) != "" {
		t.Fatalf("stderr %s", errb)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "problem: ") && !strings.HasPrefix(line, "suggestion: ") {
			t.Fatalf("non-problem line %q\n%s", line, out)
		}
	}
	problems := problemLines(out)
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if !strings.HasPrefix(line, "problem: ") {
			continue
		}
		next := ""
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		if i+1 < len(lines) {
			next = lines[i+1]
		}
		if !strings.HasPrefix(next, "suggestion: ") || strings.TrimPrefix(next, "suggestion: ") == "" {
			t.Fatalf("problem without suggestion: %s", line)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("doctor executed a PATH binary: %v", err)
	}
	joined := strings.Join(problems, "\n")
	gitAt := strings.Index(out, "problem: git is not on PATH")
	laterAt := strings.Index(out, "problem: opencode is not on PATH")
	if gitAt < 0 || laterAt < gitAt {
		t.Fatalf("later binary was not reported after git\n%s", out)
	}
	if strings.Contains(joined, "grok is not on PATH") {
		t.Fatalf("present binary reported missing\n%s", joined)
	}
	if strings.Contains(joined, "redline 0 is outside") || strings.Contains(joined, "redline 1 is outside") {
		t.Fatalf("boundary redline reported\n%s", joined)
	}
	if !strings.Contains(joined, "channel high redline 2 is outside 0 through 1") || !strings.Contains(joined, "channel low redline -1 is outside 0 through 1") {
		t.Fatalf("missing redline problem\n%s", joined)
	}
	if !strings.Contains(out, "suggestion: run channel set --id high --redline with a value from 0 to 1 inclusive") {
		t.Fatalf("missing redline suggestion\n%s", out)
	}
	if !strings.Contains(joined, `channel high window "fortnight" is not accepted`) {
		t.Fatalf("missing window problem\n%s", joined)
	}
	if !strings.Contains(out, "suggestion: use a window name of 5h, daily, weekly, or monthly") {
		t.Fatalf("missing window suggestion\n%s", out)
	}
	if strings.Contains(joined, `window "daily"`) || strings.Contains(joined, `window "5h"`) || strings.Contains(joined, `window "weekly"`) {
		t.Fatalf("accepted window reported\n%s", joined)
	}
	if !strings.Contains(joined, "tier T1") || !strings.Contains(joined, "tier T2") || !strings.Contains(joined, "tier T3") {
		t.Fatalf("tiers\n%s", joined)
	}
}

func TestSoftBusinessValues(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		home := t.TempDir()
		repo := t.TempDir()
		mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
		mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Ferry", "--mode", "manual", "--freeze-mode", "safe", "--priority", "1")
		mustHome(t, home, "channel", "set", "--id", "ch1", "--runner", "grok", "--redline", "0.25", "--concurrency", "1", "--health", "ok", "--window", "daily:fixed:10", "--model", "m")
		return home
	}
	objectiveOf := func(t *testing.T, home string) model.Objective {
		t.Helper()
		code, out, errb := callCLI(t, []string{"--home", home, "--json", "objective", "show", "o1"})
		if code != 0 {
			t.Fatalf("show %d %s", code, errb)
		}
		var obj model.Objective
		if err := json.Unmarshal([]byte(out), &obj); err != nil {
			t.Fatal(err)
		}
		return obj
	}
	channelOf := func(t *testing.T, home, id string) model.Channel {
		t.Helper()
		code, out, errb := callCLI(t, []string{"--home", home, "--json", "channel", "show", id})
		if code != 0 {
			t.Fatalf("show %d %s", code, errb)
		}
		var ch model.Channel
		if err := json.Unmarshal([]byte(out), &ch); err != nil {
			t.Fatal(err)
		}
		return ch
	}
	t.Run("unknown mode", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "objective", "set", "--id", "o1", "--mode", "NOPE", "--priority", "7"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if !strings.Contains(out+errb, "needs correction: mode NOPE") {
			t.Fatalf("report\n%s\n%s", out, errb)
		}
		obj := objectiveOf(t, home)
		if obj.Mode != model.ModeManual || obj.Priority != 7 || obj.FreezeMode != model.FreezeSafe {
			t.Fatalf("mode %s freeze %s priority %d", obj.Mode, obj.FreezeMode, obj.Priority)
		}
	})
	t.Run("unknown freeze-mode", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "objective", "set", "--id", "o1", "--freeze-mode", "LOOSE", "--priority", "4"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if !strings.Contains(out+errb, "needs correction: freeze-mode LOOSE") {
			t.Fatalf("report\n%s\n%s", out, errb)
		}
		obj := objectiveOf(t, home)
		if obj.FreezeMode != model.FreezeSafe || obj.Mode != model.ModeManual || obj.Priority != 4 {
			t.Fatalf("mode %s freeze %s priority %d", obj.Mode, obj.FreezeMode, obj.Priority)
		}
	})
	t.Run("unknown mode keeps valid freeze", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "objective", "set", "--id", "o1", "--mode", "NOPE", "--freeze-mode", "fast", "--priority", "9"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if !strings.Contains(out+errb, "needs correction: mode NOPE") || strings.Contains(out+errb, "freeze-mode") {
			t.Fatalf("report\n%s\n%s", out, errb)
		}
		obj := objectiveOf(t, home)
		if obj.Mode != model.ModeManual || obj.FreezeMode != model.FreezeFast || obj.Priority != 9 {
			t.Fatalf("mode %s freeze %s priority %d", obj.Mode, obj.FreezeMode, obj.Priority)
		}
	})
	t.Run("json reports mode without breaking the record", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "--json", "objective", "set", "--id", "o1", "--mode", "NOPE", "--priority", "6"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if strings.Contains(out, "needs correction") || !strings.Contains(errb, "needs correction: mode NOPE") {
			t.Fatalf("stdout %s\nstderr %s", out, errb)
		}
		var obj model.Objective
		if err := json.Unmarshal([]byte(out), &obj); err != nil {
			t.Fatal(err)
		}
		if obj.Mode != model.ModeManual || obj.Priority != 6 {
			t.Fatalf("%+v", obj)
		}
	})
	t.Run("unparseable window", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "channel", "set", "--id", "ch1", "--redline", "0.5", "--window", "daily:fixed:nope"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if !strings.Contains(out+errb, "needs correction: window daily:fixed:nope") {
			t.Fatalf("report\n%s\n%s", out, errb)
		}
		ch := channelOf(t, home, "ch1")
		if ch.Redline != 0.5 || ch.Runner != model.RunnerGrok || len(ch.QuotaWindows) != 1 || ch.QuotaWindows[0].Name != "daily" {
			t.Fatalf("%+v", ch)
		}
	})
	t.Run("mixed windows", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "channel", "set", "--id", "ch1", "--concurrency", "2", "--window", "weekly:rolling:3", "--window", ":fixed"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if !strings.Contains(out+errb, "needs correction: window :fixed") {
			t.Fatalf("report\n%s\n%s", out, errb)
		}
		ch := channelOf(t, home, "ch1")
		if ch.Concurrency != 2 || len(ch.QuotaWindows) != 1 || ch.QuotaWindows[0].Name != "weekly" || ch.QuotaWindows[0].LimitTokens != 3 {
			t.Fatalf("%+v", ch)
		}
	})
	t.Run("unrecognized runner", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "channel", "set", "--id", "ch1", "--runner", "nope", "--redline", "0.5"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if !strings.Contains(out+errb, "needs correction: runner nope") {
			t.Fatalf("report\n%s\n%s", out, errb)
		}
		ch := channelOf(t, home, "ch1")
		if ch.Runner != model.RunnerGrok || ch.Redline != 0.5 {
			t.Fatalf("%+v", ch)
		}
	})
	t.Run("unrecognized health", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "channel", "set", "--id", "ch1", "--health", "banana", "--concurrency", "4"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if !strings.Contains(out+errb, "needs correction: health banana") {
			t.Fatalf("report\n%s\n%s", out, errb)
		}
		ch := channelOf(t, home, "ch1")
		if ch.Health != model.HealthOK || ch.Concurrency != 4 {
			t.Fatalf("%+v", ch)
		}
	})
	t.Run("create with unrecognized runner", func(t *testing.T) {
		home := setup(t)
		code, out, errb := callCLI(t, []string{"--home", home, "channel", "set", "--id", "ch-new", "--runner", "nope", "--redline", "0.5"})
		if code != 0 {
			t.Fatalf("exit %d %s", code, errb)
		}
		if !strings.Contains(out+errb, "needs correction: runner nope") {
			t.Fatalf("report\n%s\n%s", out, errb)
		}
		ch := channelOf(t, home, "ch-new")
		if ch.Runner != model.RunnerGrok || ch.Redline != 0.5 {
			t.Fatalf("%+v", ch)
		}
	})
	t.Run("missing runner still exits 2", func(t *testing.T) {
		home := setup(t)
		code, _, _ := callCLI(t, []string{"--home", home, "channel", "set", "--id", "ch-empty", "--redline", "0.5"})
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
		code, out, errb := callCLI(t, []string{"--home", home, "channel", "list"})
		if code != 0 {
			t.Fatalf("list %d %s", code, errb)
		}
		if strings.Contains(out, "ch-empty") {
			t.Fatalf("created %s", out)
		}
	})
	t.Run("missing objective id still exits 2", func(t *testing.T) {
		home := setup(t)
		code, _, _ := callCLI(t, []string{"--home", home, "objective", "set", "--mode", "manual", "--priority", "3"})
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
		obj := objectiveOf(t, home)
		if obj.Priority != 1 {
			t.Fatalf("priority %d", obj.Priority)
		}
	})
	t.Run("unknown verb still exits 2", func(t *testing.T) {
		home := setup(t)
		code, _, _ := callCLI(t, []string{"--home", home, "objective", "explode"})
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("unknown flag still exits 2", func(t *testing.T) {
		home := setup(t)
		code, _, _ := callCLI(t, []string{"--home", home, "objective", "set", "--id", "o1", "--nope", "1"})
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
		obj := objectiveOf(t, home)
		if obj.Priority != 1 || obj.Mode != model.ModeManual {
			t.Fatalf("mode %s priority %d", obj.Mode, obj.Priority)
		}
	})
	showObjective := func(t *testing.T, home, id string) model.Objective {
		t.Helper()
		code, out, errb := callCLI(t, []string{"--home", home, "--json", "objective", "show", id})
		if code != 0 {
			t.Fatalf("show %d %s", code, errb)
		}
		var obj model.Objective
		if err := json.Unmarshal([]byte(out), &obj); err != nil {
			t.Fatal(err)
		}
		return obj
	}
	cases := []struct {
		name  string
		prep  [][]string
		args  []string
		note  string
		check func(t *testing.T, home string)
	}{
		{
			name: "objective set priority keeps mode",
			args: []string{"objective", "set", "--id", "o1", "--mode", "auto", "--priority", "nope"},
			note: "needs correction: priority nope",
			check: func(t *testing.T, home string) {
				obj := objectiveOf(t, home)
				if obj.Mode != model.ModeAuto || obj.Priority != 1 || obj.FreezeMode != model.FreezeSafe {
					t.Fatalf("mode %s freeze %s priority %d", obj.Mode, obj.FreezeMode, obj.Priority)
				}
			},
		},
		{
			name: "channel set concurrency keeps redline",
			args: []string{"channel", "set", "--id", "ch1", "--redline", "0.5", "--concurrency", "nope"},
			note: "needs correction: concurrency nope",
			check: func(t *testing.T, home string) {
				ch := channelOf(t, home, "ch1")
				if ch.Redline != 0.5 || ch.Concurrency != 1 || ch.Runner != model.RunnerGrok {
					t.Fatalf("%+v", ch)
				}
			},
		},
		{
			name: "objective add priority keeps the other fields",
			args: []string{"objective", "add", "--id", "o2", "--project", "p1", "--title", "Ferry", "--mode", "manual", "--soft-tokens", "8", "--priority", "nope"},
			note: "needs correction: priority nope",
			check: func(t *testing.T, home string) {
				obj := showObjective(t, home, "o2")
				if obj.Title != "Ferry" || obj.Mode != model.ModeManual || obj.Priority != 0 || obj.Budget.SoftTokens != 8 || obj.Budget.HardTokens != 0 || obj.Project != "p1" {
					t.Fatalf("%+v", obj)
				}
			},
		},
		{
			name: "objective set soft-tokens keeps the stored budget",
			prep: [][]string{{"objective", "set", "--id", "o1", "--soft-tokens", "11", "--hard-tokens", "22"}},
			args: []string{"objective", "set", "--id", "o1", "--soft-tokens", "nope", "--priority", "5"},
			note: "needs correction: soft-tokens nope",
			check: func(t *testing.T, home string) {
				obj := objectiveOf(t, home)
				if obj.Budget.SoftTokens != 11 || obj.Budget.HardTokens != 22 || obj.Priority != 5 {
					t.Fatalf("%+v", obj.Budget)
				}
			},
		},
		{
			name: "objective set hard-tokens keeps the stored budget",
			prep: [][]string{{"objective", "set", "--id", "o1", "--soft-tokens", "11", "--hard-tokens", "22"}},
			args: []string{"objective", "set", "--id", "o1", "--hard-tokens", "nope"},
			note: "needs correction: hard-tokens nope",
			check: func(t *testing.T, home string) {
				obj := objectiveOf(t, home)
				if obj.Budget.HardTokens != 22 || obj.Budget.SoftTokens != 11 || obj.Priority != 1 {
					t.Fatalf("soft %d hard %d priority %d", obj.Budget.SoftTokens, obj.Budget.HardTokens, obj.Priority)
				}
			},
		},
		{
			name: "objective add soft-tokens keeps the default",
			args: []string{"objective", "add", "--id", "o3", "--project", "p1", "--title", "Mast", "--priority", "2", "--soft-tokens", "nope"},
			note: "needs correction: soft-tokens nope",
			check: func(t *testing.T, home string) {
				obj := showObjective(t, home, "o3")
				if obj.Budget.SoftTokens != 0 || obj.Priority != 2 || obj.Title != "Mast" {
					t.Fatalf("%+v", obj)
				}
			},
		},
		{
			name: "objective add hard-tokens keeps the default",
			args: []string{"objective", "add", "--id", "o4", "--project", "p1", "--title", "Hull", "--mode", "manual", "--soft-tokens", "4", "--hard-tokens", "nope"},
			note: "needs correction: hard-tokens nope",
			check: func(t *testing.T, home string) {
				obj := showObjective(t, home, "o4")
				if obj.Budget.HardTokens != 0 || obj.Budget.SoftTokens != 4 || obj.Mode != model.ModeManual || obj.Title != "Hull" || obj.Priority != 0 {
					t.Fatalf("%+v", obj)
				}
			},
		},
		{
			name: "channel set redline keeps concurrency",
			args: []string{"channel", "set", "--id", "ch1", "--redline", "nope", "--concurrency", "3"},
			note: "needs correction: redline nope",
			check: func(t *testing.T, home string) {
				ch := channelOf(t, home, "ch1")
				if ch.Redline != 0.25 || ch.Concurrency != 3 || ch.Runner != model.RunnerGrok {
					t.Fatalf("%+v", ch)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := setup(t)
			for _, prep := range tc.prep {
				mustHome(t, home, prep...)
			}
			code, out, errb := callCLI(t, append([]string{"--home", home}, tc.args...))
			if code != 0 {
				t.Fatalf("exit %d\n%s\n%s", code, out, errb)
			}
			if !strings.Contains(out+errb, tc.note) {
				t.Fatalf("report\n%s\n%s", out, errb)
			}
			tc.check(t, home)
		})
	}
}

func TestDoctorExecutorNames(t *testing.T) {
	real := []string{"grok", "cursor-agent", "kimi"}
	display := []string{"grok-build", "cursor", "kimi-cli"}
	kept := []string{"codex", "claude", "agy", "opencode", "git", "sandbox-exec"}
	assertSuggestions := func(t *testing.T, out string) {
		t.Helper()
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		for i, line := range lines {
			if !strings.HasPrefix(line, "problem: ") {
				continue
			}
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "suggestion: ") || strings.TrimPrefix(lines[i+1], "suggestion: ") == "" {
				t.Fatalf("problem without suggestion: %s\n%s", line, out)
			}
		}
	}
	t.Run("real binaries", func(t *testing.T) {
		home := t.TempDir()
		bin, marker := sentinelBin(t, real...)
		t.Setenv("PATH", bin)
		code, out, errb := callCLI(t, []string{"--home", home, "doctor"})
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errb)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("doctor executed a PATH binary: %v", err)
		}
		assertSuggestions(t, out)
		joined := out + errb
		for _, name := range real {
			if strings.Contains(joined, name+" is not on PATH") {
				t.Fatalf("%s reported missing\n%s", name, out)
			}
		}
		for _, name := range kept {
			if !strings.Contains(out, name+" is not on PATH") {
				t.Fatalf("missing %s\n%s", name, out)
			}
		}
	})
	t.Run("display names", func(t *testing.T) {
		home := t.TempDir()
		bin, marker := sentinelBin(t, display...)
		t.Setenv("PATH", bin)
		code, out, errb := callCLI(t, []string{"--home", home, "doctor"})
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errb)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("doctor executed a PATH binary: %v", err)
		}
		assertSuggestions(t, out)
		for _, name := range real {
			if !strings.Contains(out, name+" is not on PATH") {
				t.Fatalf("real name %s not reported\n%s", name, out)
			}
		}
		for _, name := range display {
			if strings.Contains(out, name+" is not on PATH") {
				t.Fatalf("display name looked up %s\n%s", name, out)
			}
		}
	})
}

func TestImportLegacyCLI(t *testing.T) {
	v1 := t.TempDir()
	path := filepath.Join(v1, "tasks", "queued.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"id":"q1","title":"Queue the lumen ferry","status":"queued","project":"lumen","dir":"/tmp/lumen"}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(v1, "tasks", "broken.json")
	if err := os.WriteFile(broken, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	code, out, errb := callCLI(t, []string{"--home", home, "import-legacy", "--dry-run", v1})
	if code != 0 {
		t.Fatalf("dry-run %d\n%s\n%s", code, out, errb)
	}
	if !strings.Contains(out, "v1 queued 1") || !strings.Contains(out, "v2 ready 1") || !strings.Contains(out, "unparsed:") || !strings.Contains(out, "broken.json") {
		t.Fatalf("report\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "cardex.db")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a database: %v", err)
	}
	mustHome(t, home, "import-legacy", v1)
	var cards []model.Card
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "card", "list")), &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("cards %+v", cards)
	}
	mustHome(t, home, "import-legacy", v1)
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "card", "list")), &cards); err != nil {
		t.Fatal(err)
	}
	var objectives []model.Objective
	if err := json.Unmarshal([]byte(mustHome(t, home, "--json", "objective", "list")), &objectives); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 || len(objectives) != 2 {
		t.Fatalf("second import cards %d objectives %d", len(cards), len(objectives))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatal("v1 bytes changed")
	}
}

func TestServeMainStops(t *testing.T) {
	if os.Getenv("CARDEX_SERVE_CHILD") == "1" {
		os.Exit(Main([]string{"--home", os.Getenv("CARDEX_SERVE_HOME"), "serve", "--addr", "127.0.0.1:0"}, os.Stdout, os.Stderr))
	}
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeMainStops$")
	cmd.Env = append(os.Environ(), "CARDEX_SERVE_CHILD=1", "CARDEX_SERVE_HOME="+home)
	proc.NewGroup(cmd)
	var buf lockBuf
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(buf.String(), "serve listening") {
		if time.Now().After(deadline) {
			_ = host.SignalProcess(cmd.Process.Pid, syscall.SIGKILL)
			t.Fatalf("serve output:\n%s", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(buf.String(), ":8787") || strings.Contains(buf.String(), ":8788") {
		_ = host.SignalProcess(cmd.Process.Pid, syscall.SIGKILL)
		t.Fatalf("reserved port\n%s", buf.String())
	}
	if err := host.SignalProcess(cmd.Process.Pid, syscall.SIGTERM); err != nil {
		_ = host.SignalProcess(cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve exit %v\n%s", err, buf.String())
		}
	case <-time.After(5 * time.Second):
		_ = host.SignalProcess(cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("serve did not stop")
	}
}

func sentinelBin(t *testing.T, names ...string) (dir, marker string) {
	t.Helper()
	dir = t.TempDir()
	marker = filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\necho ran >> '" + marker + "'\n"
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, marker
}

func problemLines(out string) []string {
	var problems []string
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "problem: "); ok {
			problems = append(problems, rest)
		}
	}
	return problems
}
