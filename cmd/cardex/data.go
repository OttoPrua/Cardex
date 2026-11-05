package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"cardex/internal/model"
	"cardex/internal/store"
)

// objectiveQuestionWrite is nil on the success path. A test may set it so the
// Question step returns an error after CreateObjective, inside the same WithTx.
var objectiveQuestionWrite func(ctx context.Context, db *store.Store, q *model.Question) error

func cmdProject(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, projectGroupUsage) {
		return 0
	}
	switch args[0] {
	case "add":
		return projectAdd(home, asJSON, args[1:], stdout, stderr)
	case "list":
		return projectList(home, asJSON, args[1:], stdout, stderr)
	case "show":
		return projectShow(home, asJSON, args[1:], stdout, stderr)
	case "set":
		return projectSet(home, asJSON, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown project verb %q\n", args[0])
		return 2
	}
}

func projectAdd(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("project add", "create a project record in the home database", "--json", stdout, stderr)
	id := fs.String("id", "", "project id")
	repo := fs.String("repo", "", "main checkout")
	root := fs.String("worktree-root", "", "worktree root")
	testCmd := fs.String("test-cmd", "", "test command")
	design := fs.String("design-dir", "", "design directory")
	var forbidden stringsFlag
	fs.Var(&forbidden, "forbidden", "forbidden path")
	if code, ok := parseSet(fs, args); !ok {
		return code
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

func projectList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("project list", "list stored projects", "--json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
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
	fs := newFlagSet("project show", "show one stored project", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "project show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	p, err := db.GetProject(context.Background(), fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, p, p.ID+"\t"+p.RepoPath)
}

func cmdObjective(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, objectiveGroupUsage) {
		return 0
	}
	switch args[0] {
	case "add":
		return objectiveAdd(home, asJSON, args[1:], stdout, stderr)
	case "list":
		return objectiveList(home, asJSON, args[1:], stdout, stderr)
	case "show":
		return objectiveShow(home, asJSON, args[1:], stdout, stderr)
	case "set":
		return objectiveSet(home, asJSON, args[1:], stdout, stderr)
	case "pause", "resume", "cancel":
		return objectiveTransition(home, asJSON, args[0], args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown objective verb %q\n", args[0])
		return 2
	}
}

func objectiveAdd(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("objective add", "create an objective. origin comes from --origin, or from CARDEX_ORIGIN when the flag is absent", "--json", stdout, stderr)
	id := fs.String("id", "", "objective id")
	project := fs.String("project", "", "project id")
	title := fs.String("title", "", "title")
	var acceptance, checks stringsFlag
	fs.Var(&acceptance, "acceptance", "acceptance command")
	fs.Var(&checks, "check", "acceptance checklist item")
	soft := &softNumber{name: "soft-tokens", kind: numberInt64}
	hard := &softNumber{name: "hard-tokens", kind: numberInt64}
	priority := &softNumber{name: "priority", kind: numberInt}
	fs.Var(soft, "soft-tokens", "soft token budget")
	fs.Var(hard, "hard-tokens", "hard token budget")
	fs.Var(priority, "priority", "priority")
	mode := fs.String("mode", string(model.ModeAuto), "auto or manual")
	freeze := fs.String("freeze-mode", string(model.FreezeSafe), "safe or fast")
	originFlag := fs.String("origin", "", "origin session kind:id; a present flag wins over CARDEX_ORIGIN")
	status := fs.String("status", string(model.ObjectiveActive), "status")
	branch := fs.String("integration-branch", "", "integration branch")
	channel := fs.String("channel", "", "explicit channel")
	modelName := fs.String("model", "", "explicit model")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	_ = originFlag
	origin := resolvedOrigin(args)
	if *project == "" {
		fmt.Fprintln(stderr, "--project is required")
		return 2
	}
	modeVal, modeOK := normalizeObjectiveMode(*mode)
	freezeVal, freezeOK := normalizeFreezeMode(*freeze)
	statusVal, statusOK := normalizeObjectiveStatus(*status)
	titleVal := *title
	var reasons []string
	if titleVal == "" {
		titleVal = "(needs input)"
		statusVal = model.ObjectivePaused
		reasons = append(reasons, "missing title")
	}
	if !modeOK {
		reasons = append(reasons, "unrecognized mode "+*mode)
		modeVal = model.ModeAuto
		statusVal = model.ObjectivePaused
	}
	if !freezeOK {
		reasons = append(reasons, "unrecognized freeze-mode "+*freeze)
		freezeVal = model.FreezeSafe
		statusVal = model.ObjectivePaused
	}
	if !statusOK {
		reasons = append(reasons, "unrecognized status "+*status)
		statusVal = model.ObjectivePaused
	}
	softTokens := int64(0)
	if soft.ok {
		softTokens = soft.i64
	}
	hardTokens := int64(0)
	if hard.ok {
		hardTokens = hard.i64
	}
	priorityVal := 0
	if priority.ok {
		priorityVal = priority.n
	}
	var numNotes []string
	numNotes = append(numNotes, soft.bad...)
	numNotes = append(numNotes, hard.bad...)
	numNotes = append(numNotes, priority.bad...)
	o := model.Objective{
		ID: *id, Project: *project, Title: titleVal,
		Acceptance: model.Acceptance{Commands: acceptance, Checklist: checks},
		Budget:     model.Budget{SoftTokens: softTokens, HardTokens: hardTokens},
		Priority:   priorityVal, Mode: modeVal, FreezeMode: freezeVal,
		Origin: origin, Status: statusVal, IntegrationBranch: *branch,
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
	err = db.WithTx(context.Background(), func(ctx context.Context) error {
		if err := db.CreateObjective(ctx, &o); err != nil {
			return err
		}
		if len(reasons) == 0 {
			return nil
		}
		q := model.Question{
			Objective: o.ID,
			Kind:      model.QuestionCustom,
			Text:      strings.Join(reasons, "; "),
		}
		if objectiveQuestionWrite != nil {
			if err := objectiveQuestionWrite(ctx, db, &q); err != nil {
				return err
			}
		}
		return db.CreateQuestion(ctx, &q)
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := o.ID + "\t" + o.Title
	if o.Origin != "" {
		line += "\t" + o.Origin
	}
	return finishSoft(stdout, stderr, asJSON, numNotes, o, line)
}

func objectiveList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("objective list", "list stored objectives, optionally for one project", "--json", stdout, stderr)
	project := fs.String("project", "", "project id")
	if code, ok := parseSet(fs, args); !ok {
		return code
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
	fs := newFlagSet("objective show", "show one stored objective, including its origin", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "objective show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	o, err := db.GetObjective(context.Background(), fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := o.ID + "\t" + o.Title
	if o.Origin != "" {
		line += "\t" + o.Origin
	}
	return emit(stdout, asJSON, o, line)
}

func cmdCard(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, cardGroupUsage) {
		return 0
	}
	switch args[0] {
	case "add":
		return cardAdd(home, asJSON, args[1:], stdout, stderr)
	case "list":
		return cardList(home, asJSON, args[1:], stdout, stderr)
	case "show":
		return cardShow(home, asJSON, args[1:], stdout, stderr)
	case "cancel", "retry", "pause", "resume", "revive":
		return cardTransition(home, asJSON, args[0], args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown card verb %q\n", args[0])
		return 2
	}
}

func cardAdd(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("card add", "create a card. origin comes from --origin, or from CARDEX_ORIGIN when the flag is absent", "--json", stdout, stderr)
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
	originFlag := fs.String("origin", "", "origin session kind:id; a present flag wins over CARDEX_ORIGIN")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	_ = originFlag
	origin := resolvedOrigin(args)
	if *objective == "" {
		fmt.Fprintln(stderr, "--objective is required")
		return 2
	}
	kindVal, kindOK := normalizeKind(*kind)
	tierVal, tierOK := normalizeTier(*tier)
	statusVal, statusOK := normalizeStatus(*status)
	titleVal := *title
	var reasons []string
	if titleVal == "" {
		titleVal = "(needs input)"
		statusVal = model.CardNeedsInput
		reasons = append(reasons, "missing title")
	}
	if !kindOK {
		reasons = append(reasons, "kind "+*kind)
		statusVal = model.CardNeedsInput
	}
	if !tierOK {
		reasons = append(reasons, "tier "+*tier)
		statusVal = model.CardNeedsInput
	}
	if !statusOK {
		reasons = append(reasons, "status "+*status)
		statusVal = model.CardNeedsInput
	}
	c := model.Card{
		ID: *id, Objective: *objective, Node: *node, Kind: kindVal,
		Title: titleVal, Body: *body, Tier: tierVal, Requires: requires,
		Deps: deps, Acceptance: acceptance, Base: *base, Status: statusVal,
		WaitReason: strings.Join(reasons, "; "),
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
	if err := saveCardOrigin(context.Background(), db, c.ID, origin); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := c.ID + "\t" + c.Title
	if origin != "" {
		line += "\t" + origin
	}
	return emit(stdout, asJSON, cardView{Card: c, Origin: origin}, line)
}

func cardList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("card list", "list stored cards, optionally for one objective", "--json", stdout, stderr)
	objective := fs.String("objective", "", "objective id")
	if code, ok := parseSet(fs, args); !ok {
		return code
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
	fs := newFlagSet("card show", "show one stored card, including the origin recorded for it", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "card show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	c, err := db.GetCard(context.Background(), fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	origin, err := loadCardOrigin(context.Background(), db, c.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := c.ID + "\t" + c.Title
	if origin != "" {
		line += "\t" + origin
	}
	return emit(stdout, asJSON, cardView{Card: c, Origin: origin}, line)
}

func emit(stdout io.Writer, asJSON bool, v any, line string) int {
	if asJSON {
		return writeJSON(stdout, v)
	}
	fmt.Fprintln(stdout, line)
	return 0
}

func normalizeKind(raw string) (model.CardKind, bool) {
	if raw == "" {
		return model.KindDev, true
	}
	folded := strings.ToLower(raw)
	for _, k := range model.AllCardKinds() {
		if string(k) == folded {
			return k, true
		}
	}
	return model.KindDev, false
}

func normalizeTier(raw string) (model.Tier, bool) {
	switch strings.ToLower(raw) {
	case "":
		return model.TierNone, true
	case "t1":
		return model.TierT1, true
	case "t2":
		return model.TierT2, true
	case "t3":
		return model.TierT3, true
	default:
		return model.TierNone, false
	}
}

func normalizeObjectiveMode(raw string) (model.ObjectiveMode, bool) {
	if raw == "" {
		return model.ModeAuto, true
	}
	folded := strings.ToLower(raw)
	for _, m := range model.AllObjectiveModes() {
		if string(m) == folded {
			return m, true
		}
	}
	return model.ModeAuto, false
}

func normalizeFreezeMode(raw string) (model.FreezeMode, bool) {
	if raw == "" {
		return model.FreezeSafe, true
	}
	folded := strings.ToLower(raw)
	for _, m := range model.AllFreezeModes() {
		if string(m) == folded {
			return m, true
		}
	}
	return model.FreezeSafe, false
}

func normalizeObjectiveStatus(raw string) (model.ObjectiveStatus, bool) {
	if raw == "" {
		return model.ObjectiveActive, true
	}
	folded := strings.ToLower(raw)
	for _, s := range model.AllObjectiveStatuses() {
		if string(s) == folded {
			return s, true
		}
	}
	return model.ObjectivePaused, false
}

func normalizeStatus(raw string) (model.CardStatus, bool) {
	if raw == "" {
		return model.CardReady, true
	}
	folded := strings.ToLower(raw)
	for _, s := range model.AllCardStatuses() {
		if strings.ToLower(string(s)) == folded {
			return s, true
		}
	}
	return model.CardNeedsInput, false
}

func stringsCut(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
