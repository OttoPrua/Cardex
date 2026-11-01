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

	"cardex/internal/model"
	"cardex/internal/positioning"
	"cardex/internal/store"
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
	bin, marker := sentinelBin(t, "grok-build", "codex", "claude", "cursor", "kimi-cli", "agy", "opencode")
	t.Setenv("PATH", bin)
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Sweep", "--mode", "manual")
	body := []byte(`{"schedules":[{"name":"lamp-sweep","every":"24h","card_template":{"objective":"o1","kind":"dev","title":"Sweep the lamp","body":"fictional sweep"}}]}`)
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
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("schedule started an executor: %v", err)
	}
}

func TestScorecardSurfacesCompute(t *testing.T) {
	home := t.TempDir()
	code, out, errb := callCLI(t, []string{"--home", home, "scorecard"})
	if code == 0 {
		t.Fatalf("exit 0\n%s", out)
	}
	if !strings.Contains(out, "not implemented") || strings.Contains(out, "未实现") || strings.Contains(errb, "未实现") {
		t.Fatalf("stdout %q stderr %q", out, errb)
	}
}

func TestHostUnknownAttempt(t *testing.T) {
	home := t.TempDir()
	bin, marker := sentinelBin(t, "grok-build", "codex", "claude", "cursor", "kimi-cli", "agy", "opencode")
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
	bin, marker := sentinelBin(t, "grok-build")
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
	if strings.Contains(joined, "grok-build is not on PATH") {
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
	var buf lockBuf
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(buf.String(), "serve listening") {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("serve output:\n%s", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(buf.String(), ":8787") || strings.Contains(buf.String(), ":8788") {
		_ = cmd.Process.Kill()
		t.Fatalf("reserved port\n%s", buf.String())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
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
		_ = cmd.Process.Kill()
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
