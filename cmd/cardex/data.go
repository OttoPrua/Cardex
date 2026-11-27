package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/store"
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
	sandboxMode := fs.String("sandbox", "on", "project default for its cards: on, or off to run them outside the Cardex sandbox")
	reviewMode := fs.String("review", "", "review mode: auto, off, check, review, or inherit (see docs/review-policy.md)")
	scratchDir := fs.String("scratch-dir", "", "scratch directory for this project's card runs and acceptance commands; overrides the home scratch_dir")
	eventsMode := fs.String("events", "", "card lifecycle events: on, or off (default off)")
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
		Sandbox:        *sandboxMode,
		Review:         *reviewMode,
		ScratchDir:     *scratchDir,
		Events:         *eventsMode,
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
	case "brief":
		return objectiveResumeBrief(home, asJSON, args[1:], stdout, stderr)
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
	objReview := fs.String("review", "", "review mode: auto, off, check, review, or inherit (see docs/review-policy.md)")
	eventsMode := fs.String("events", "", "card lifecycle events: on, off, or inherit (empty inherits the project; default off)")
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
		Channel: *channel, Model: *modelName, Review: *objReview, Events: *eventsMode,
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
	fs := newFlagSet("objective show", "show one stored objective, including its origin", "<id> --origin --json --brief", stdout, stderr)
	brief := fs.Bool("brief", false, "card counts by status, plus one line each for needs_input and failed cards")
	acceptOrigin(fs)
	if code, ok := parseSet(fs, hoistFlags(fs, args)); !ok {
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
	ctx := context.Background()
	o, err := db.GetObjective(ctx, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if origin := readOrigin(fs); origin != "" {
		if cards, err := db.ListCardWaitStates(ctx, o.ID); err == nil {
			ids := make([]string, 0, len(cards))
			for _, card := range cards {
				ids = append(ids, card.ID)
			}
			noteReads(db, origin, "objective-show", ids)
		}
	}
	if *brief {
		cards, err := db.ListCards(ctx, o.ID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		view := buildObjectiveBrief(o.ID, cards)
		failedNormalizers := make(map[string][]string)
		for _, card := range cards {
			if card.Kind == model.KindNormalize && card.Status == model.CardFailed && len(card.Deps) > 0 {
				failedNormalizers[card.Deps[0]] = append(failedNormalizers[card.Deps[0]], card.ID)
			}
		}
		for i := range view.Blocked {
			card := &view.Blocked[i]
			if card.Reason != "" {
				continue
			}
			reason, err := objectiveFallbackReason(ctx, db, *card, failedNormalizers[card.ID])
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			card.Reason = reason
		}
		return emitObjectiveBrief(stdout, asJSON, view)
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
	case "absorb":
		return cardAbsorb(home, asJSON, args[1:], stdout, stderr)
	case "recover":
		return cardRecover(home, asJSON, args[1:], stdout, stderr)
	case "merged":
		return cardMerged(home, asJSON, args[1:], stdout, stderr)
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
	sandboxMode := fs.String("sandbox", "", "on or off; empty inherits the project default (on unless the project says off); fixed once the card exists")
	cardReview := fs.String("review", "", "review mode after a passed dev card: auto, off, check, review; empty inherits objective then project")
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
		WaitReason: strings.Join(reasons, "; "), Sandbox: *sandboxMode, Review: *cardReview,
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
	ctx := context.Background()
	if err := reviewAddCheck(ctx, db, c, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		if err := db.CreateCard(ctx, &c); err != nil {
			return err
		}
		return saveCardOrigin(ctx, db, c.ID, origin)
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := c.ID + "\t" + c.Title
	if origin != "" {
		line += "\t" + origin
	}
	return emit(stdout, asJSON, cardView{Card: c, Origin: origin}, line)
}

// reviewAddCheck warns when an rv- review has no stored subject, and rejects
// every non-empty --dep that names no stored card. A blank --dep does not hide
// a later one. It does not change the card record.
func reviewAddCheck(ctx context.Context, db *store.Store, card model.Card, stderr io.Writer) error {
	if card.Kind != model.KindReview {
		return nil
	}
	for _, dep := range card.Deps {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		if _, err := db.GetCard(ctx, dep); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("review dependency %s does not exist", dep)
			}
			return err
		}
	}
	id, explicit := model.ReviewSubject(card)
	if explicit || id == card.ID {
		return nil
	}
	_, err := db.GetCard(ctx, id)
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintf(stderr, "review %s: card %s does not exist; this review will be treated as a standalone review. Pass --dep to name the card under review.\n", card.ID, id)
		return nil
	}
	return err
}

func cardList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("card list", "list stored cards, optionally for one objective", "--objective --changed-since <RFC3339> --fields <name> --origin --json", stdout, stderr)
	objective := fs.String("objective", "", "objective id")
	changedSince := fs.String("changed-since", "", "RFC3339 time; list cards created or with a status or attempt change after it")
	var fieldFlags stringsFlag
	fs.Var(&fieldFlags, "fields", "card fields to print, comma-separated: "+strings.Join(cardListFields(), ","))
	acceptOrigin(fs)
	if code, ok := parseSet(fs, hoistFlags(fs, args)); !ok {
		return code
	}
	var since *time.Time
	if flagSeen(fs, "changed-since") {
		parsed, err := parseChangedSince(*changedSince)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		since = &parsed
	}
	var fields []string
	if len(fieldFlags) > 0 {
		parsed, err := parseCardFields(fieldFlags)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		fields = parsed
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	var items []model.Card
	if since == nil {
		items, err = db.ListCards(ctx, *objective)
	} else {
		items, err = db.ListCardsChangedSince(ctx, *objective, *since)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if origin := readOrigin(fs); origin != "" {
		ids := make([]string, 0, len(items))
		for _, card := range items {
			ids = append(ids, card.ID)
		}
		noteReads(db, origin, "card-list", ids)
	}
	if len(fields) > 0 {
		if asJSON {
			return writeJSON(stdout, projectCards(items, fields))
		}
		fmt.Fprint(stdout, formatCardFields(items, fields))
		return 0
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
	fs := newFlagSet("card show", "show one stored card, including the origin recorded for it", "<id> --origin --json --brief --claims --attempts all --summary", stdout, stderr)
	brief := fs.Bool("brief", false, "short status: id, objective, status, wait reason, tier, rework count, current attempt, latest event, pending question, commit")
	showClaims := fs.Bool("claims", false, "with --brief, include the claim list; the default brief prints claims_count only")
	attemptsMode := fs.String("attempts", "", "with --brief, pass all to list every attempt; the default brief keeps the latest 3")
	acceptOrigin(fs)
	summaryOnly := fs.Bool("summary", false, "print only the structured result summary")
	if code, ok := parseSet(fs, hoistFlags(fs, args)); !ok {
		return code
	}
	if *brief && *summaryOnly {
		fmt.Fprintln(stderr, "--brief and --summary select different output formats")
		return 2
	}
	allAttempts, err := parseAttemptsMode(*attemptsMode)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "card show <id>")
		return 2
	}
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	db, err := store.Open(filepath.Join(dir, "cardex.db"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	c, err := db.GetCard(ctx, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	noteReads(db, readOrigin(fs), "card-show", []string{c.ID})
	if *brief || *summaryOnly {
		attempts, err := db.ListAttempts(ctx, c.ID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *summaryOnly {
			sum, ok, err := latestSummary(dir, attempts)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if !ok {
				fmt.Fprintf(stderr, "no summary for %s\n", c.ID)
				return 1
			}
			if asJSON {
				return writeJSON(stdout, sum)
			}
			fmt.Fprint(stdout, harvest.FormatSummary(sum))
			return 0
		}
		events, err := db.ListEvents(ctx, "card:"+c.ID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		question, err := pendingBriefQuestion(ctx, db, c.ID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return emitCardBrief(stdout, asJSON, buildCardBrief(c, attempts, events, question, *showClaims, allAttempts))
	}
	origin, err := loadCardOrigin(ctx, db, c.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	history, err := cardShowHistory(ctx, db, c.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	attempts, err := cardShowAttempts(ctx, db, dir, c.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := c.ID + "\t" + c.Title
	if origin != "" {
		line += "\t" + origin
	}
	if useColor(stdout, asJSON) {
		line += "\t" + colorStatus(string(c.Status))
	}
	return emit(stdout, asJSON, cardView{Card: c, Origin: origin, History: history, Attempts: attempts}, line)
}

type cardHistoryItem struct {
	TS   string          `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type cardAttemptView struct {
	ID       string   `json:"id"`
	Evidence []string `json:"evidence"`
}

func latestSummary(home string, attempts []model.Attempt) (harvest.Summary, bool, error) {
	best := -1
	var bestPath string
	for i, attempt := range attempts {
		if !cardSafeID(attempt.ID) {
			continue
		}
		path := harvest.SummaryPath(home, attempt)
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			// Earlier versions always wrote summaries under home.
			path = filepath.Join(home, "attempts", attempt.ID, "summary.json")
			_, err = os.Stat(path)
		}
		if err != nil {
			continue
		}
		if best < 0 || attempt.Index > attempts[best].Index || (attempt.Index == attempts[best].Index && attempt.EndedAt.After(attempts[best].EndedAt)) {
			best = i
			bestPath = path
		}
	}
	if best < 0 {
		return harvest.Summary{}, false, nil
	}
	sum, err := harvest.LoadSummary(bestPath)
	if err != nil {
		return harvest.Summary{}, false, err
	}
	return sum, true, nil
}

func cardShowHistory(ctx context.Context, db *store.Store, id string) ([]cardHistoryItem, error) {
	events, err := db.ListEvents(ctx, "card:"+id)
	if err != nil {
		return nil, err
	}
	out := []cardHistoryItem{}
	for _, event := range events {
		if event.Type != "review_verdict" && event.Type != "rework" {
			continue
		}
		ts := ""
		if !event.TS.IsZero() {
			ts = event.TS.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, cardHistoryItem{TS: ts, Type: event.Type, Data: cardEventData(event.Data)})
	}
	return out, nil
}

func cardShowAttempts(ctx context.Context, db *store.Store, home, cardID string) ([]cardAttemptView, error) {
	attempts, err := db.ListAttempts(ctx, cardID)
	if err != nil {
		return nil, err
	}
	out := []cardAttemptView{}
	for _, attempt := range attempts {
		out = append(out, cardAttemptView{
			ID:       attempt.ID,
			Evidence: roots.EvidenceRefs(home, attempt.ID, attempt.PromptPath),
		})
	}
	return out, nil
}

func cardSafeID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\\`)
}

func cardEventData(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return json.RawMessage(`{}`)
	}
	if json.Valid(data) {
		return data
	}
	encoded, err := json.Marshal(string(data))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
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
