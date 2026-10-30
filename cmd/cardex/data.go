package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"cardex/internal/model"
)

func cmdProject(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "project requires add, list, show, or set")
		return 2
	}
	switch args[0] {
	case "add":
		return projectAdd(home, asJSON, args[1:], stdout, stderr)
	case "list":
		return projectList(home, asJSON, stdout, stderr)
	case "show":
		return projectShow(home, asJSON, args[1:], stdout, stderr)
	case "set":
		return unimplemented(stdout, "R4-B")
	default:
		fmt.Fprintf(stderr, "unknown project verb %q\n", args[0])
		return 2
	}
}

func projectAdd(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("project add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "project id")
	repo := fs.String("repo", "", "main checkout")
	root := fs.String("worktree-root", "", "worktree root")
	testCmd := fs.String("test-cmd", "", "test command")
	design := fs.String("design-dir", "", "design directory")
	var forbidden stringsFlag
	fs.Var(&forbidden, "forbidden", "forbidden path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *repo == "" {
		fmt.Fprintln(stderr, "--repo is required")
		return 2
	}
	p := model.Project{
		ID:             *id,
		RepoPath:       *repo,
		WorktreeRoot:   *root,
		TestCmd:        *testCmd,
		DesignDir:      *design,
		ForbiddenPaths: forbidden,
	}
	if p.ID == "" {
		gen, err := newID("p")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		p.ID = gen
	}
	if p.WorktreeRoot == "" {
		p.WorktreeRoot = filepath.Join(filepath.Dir(p.RepoPath), "worktrees", "cardex")
	}
	if p.TestCmd == "" {
		p.TestCmd = "go test ./..."
	}
	if p.DesignDir == "" {
		p.DesignDir = "docs/design/"
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	if err := db.CreateProject(context.Background(), &p); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, p, p.ID+"\t"+p.RepoPath)
}

func projectList(home string, asJSON bool, stdout, stderr io.Writer) int {
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	items, err := db.ListProjects(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, items)
	}
	for _, p := range items {
		fmt.Fprintf(stdout, "%s\t%s\n", p.ID, p.RepoPath)
	}
	return 0
}

func projectShow(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "project show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	p, err := db.GetProject(context.Background(), args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, p, p.ID+"\t"+p.RepoPath)
}

func cmdObjective(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "objective requires a verb")
		return 2
	}
	switch args[0] {
	case "add":
		return objectiveAdd(home, asJSON, args[1:], stdout, stderr)
	case "list":
		return objectiveList(home, asJSON, args[1:], stdout, stderr)
	case "show":
		return objectiveShow(home, asJSON, args[1:], stdout, stderr)
	case "set", "pause", "resume", "cancel":
		return unimplemented(stdout, "R4-B")
	default:
		fmt.Fprintf(stderr, "unknown objective verb %q\n", args[0])
		return 2
	}
}

func objectiveAdd(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("objective add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "objective id")
	project := fs.String("project", "", "project id")
	title := fs.String("title", "", "title")
	var acceptance, checks stringsFlag
	fs.Var(&acceptance, "acceptance", "acceptance command")
	fs.Var(&checks, "check", "acceptance checklist item")
	soft := fs.Int64("soft-tokens", 0, "soft token budget")
	hard := fs.Int64("hard-tokens", 0, "hard token budget")
	priority := fs.Int("priority", 0, "priority")
	mode := fs.String("mode", string(model.ModeAuto), "auto or manual")
	freeze := fs.String("freeze-mode", string(model.FreezeSafe), "safe or fast")
	origin := fs.String("origin", "", "origin session")
	status := fs.String("status", string(model.ObjectiveActive), "status")
	branch := fs.String("integration-branch", "", "integration branch")
	channel := fs.String("channel", "", "explicit channel")
	modelName := fs.String("model", "", "explicit model")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *project == "" || *title == "" {
		fmt.Fprintln(stderr, "--project and --title are required")
		return 2
	}
	o := model.Objective{
		ID: *id, Project: *project, Title: *title,
		Acceptance: model.Acceptance{Commands: acceptance, Checklist: checks},
		Budget:     model.Budget{SoftTokens: *soft, HardTokens: *hard},
		Priority:   *priority, Mode: model.ObjectiveMode(*mode), FreezeMode: model.FreezeMode(*freeze),
		Origin: *origin, Status: model.ObjectiveStatus(*status), IntegrationBranch: *branch,
		Channel: *channel, Model: *modelName,
	}
	if o.ID == "" {
		gen, err := newID("o")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		o.ID = gen
	}
	if o.IntegrationBranch == "" {
		o.IntegrationBranch = "cardex/" + o.ID + "/main"
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	if err := db.CreateObjective(context.Background(), &o); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, o, o.ID+"\t"+o.Title)
}

func objectiveList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("objective list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project id")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	items, err := db.ListObjectives(context.Background(), *project)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, items)
	}
	for _, o := range items {
		fmt.Fprintf(stdout, "%s\t%s\n", o.ID, o.Title)
	}
	return 0
}

func objectiveShow(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "objective show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	o, err := db.GetObjective(context.Background(), args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, o, o.ID+"\t"+o.Title)
}

func cmdCard(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "card requires a verb")
		return 2
	}
	switch args[0] {
	case "add":
		return cardAdd(home, asJSON, args[1:], stdout, stderr)
	case "list":
		return cardList(home, asJSON, args[1:], stdout, stderr)
	case "show":
		return cardShow(home, asJSON, args[1:], stdout, stderr)
	case "cancel", "retry", "pause", "resume", "revive":
		return unimplemented(stdout, "R4-B")
	default:
		fmt.Fprintf(stderr, "unknown card verb %q\n", args[0])
		return 2
	}
}

func cardAdd(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("card add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "card id")
	objective := fs.String("objective", "", "objective id")
	node := fs.String("node", "", "design node id")
	kind := fs.String("kind", string(model.KindDev), "card kind")
	title := fs.String("title", "", "title")
	body := fs.String("body", "", "body")
	tier := fs.String("tier", "", "tier")
	var requires, deps, acceptance, claims stringsFlag
	fs.Var(&requires, "require", "capability")
	fs.Var(&deps, "dep", "dependency card id")
	fs.Var(&acceptance, "acceptance", "acceptance command")
	fs.Var(&claims, "claim", "path or path=symbol")
	base := fs.String("base", "", "base commit")
	status := fs.String("status", string(model.CardReady), "status")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *objective == "" {
		fmt.Fprintln(stderr, "--objective is required")
		return 2
	}
	kindVal := model.CardKind(*kind)
	if kindVal == "" {
		kindVal = model.KindDev
	}
	tierVal := normalizeTier(*tier)
	statusVal := model.CardStatus(*status)
	titleVal := *title
	if titleVal == "" {
		titleVal = "(needs input)"
		statusVal = model.CardNeedsInput
	}
	c := model.Card{
		ID: *id, Objective: *objective, Node: *node, Kind: kindVal,
		Title: titleVal, Body: *body, Tier: tierVal, Requires: requires,
		Deps: deps, Acceptance: acceptance, Base: *base, Status: statusVal,
	}
	if c.ID == "" {
		gen, err := newID("c")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		c.ID = gen
	}
	for _, raw := range claims {
		path, symbol, _ := stringsCut(raw, "=")
		if path == "" {
			continue
		}
		c.Claims = append(c.Claims, model.Claim{Card: c.ID, Path: path, Symbol: symbol})
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	if err := db.CreateCard(context.Background(), &c); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, c, c.ID+"\t"+c.Title)
}

func cardList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("card list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	objective := fs.String("objective", "", "objective id")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	items, err := db.ListCards(context.Background(), *objective)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, items)
	}
	for _, c := range items {
		fmt.Fprintf(stdout, "%s\t%s\n", c.ID, c.Title)
	}
	return 0
}

func cardShow(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "card show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	c, err := db.GetCard(context.Background(), args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, c, c.ID+"\t"+c.Title)
}

func emit(stdout io.Writer, asJSON bool, v any, line string) int {
	if asJSON {
		return writeJSON(stdout, v)
	}
	fmt.Fprintln(stdout, line)
	return 0
}

func normalizeTier(raw string) model.Tier {
	switch strings.ToLower(raw) {
	case "t1":
		return model.TierT1
	case "t2":
		return model.TierT2
	case "t3":
		return model.TierT3
	default:
		return model.Tier(raw)
	}
}

func stringsCut(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
