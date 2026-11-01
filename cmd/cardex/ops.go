package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"cardex/internal/model"
)

const projectGroupUsage = `project — read and update project records
purpose: add, list, show, or set a project stored in the home database

parameters:
  add   --id --repo --worktree-root --test-cmd --design-dir --forbidden
  list  --json
  show  <id> --json
  set   --id <id> --repo --worktree-root --test-cmd --design-dir --forbidden
`

func projectSet(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("project set", "update fields on a stored project", "--json", stdout, stderr)
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
	if *id == "" && fs.NArg() == 1 {
		*id = fs.Arg(0)
	}
	if *id == "" {
		fmt.Fprintln(stderr, "project set requires --id")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	p, err := db.GetProject(context.Background(), *id)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if flagSeen(fs, "repo") {
		p.RepoPath = *repo
	}
	if flagSeen(fs, "worktree-root") {
		p.WorktreeRoot = *root
	}
	if flagSeen(fs, "test-cmd") {
		p.TestCmd = *testCmd
	}
	if flagSeen(fs, "design-dir") {
		p.DesignDir = *design
	}
	if flagSeen(fs, "forbidden") {
		p.ForbiddenPaths = forbidden
	}
	if err := db.UpdateProject(context.Background(), &p); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, p, p.ID+"\t"+p.RepoPath)
}

const objectiveGroupUsage = `objective — read and update objective records
purpose: add, list, show, set, pause, resume, or cancel an objective

parameters:
  add     --id --project --title --acceptance --check --soft-tokens --hard-tokens --priority --mode --freeze-mode --origin --status --integration-branch --channel --model
  list    --project --json
  show    <id> --json
  set     --id <id> --mode --freeze-mode --soft-tokens --hard-tokens --priority
  pause   <id>
  resume  <id>
  cancel  <id>
`

func objectiveSet(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("objective set", "update mode, freeze-mode, budget, or priority on a stored objective", "--json", stdout, stderr)
	id := fs.String("id", "", "objective id")
	mode := fs.String("mode", "", "auto or manual")
	freeze := fs.String("freeze-mode", "", "safe or fast")
	soft := fs.Int64("soft-tokens", 0, "soft token budget")
	hard := fs.Int64("hard-tokens", 0, "hard token budget")
	priority := fs.Int("priority", 0, "priority")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if *id == "" && fs.NArg() == 1 {
		*id = fs.Arg(0)
	}
	if *id == "" {
		fmt.Fprintln(stderr, "objective set requires --id")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	o, err := db.GetObjective(context.Background(), *id)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if flagSeen(fs, "mode") {
		val, ok := normalizeObjectiveMode(*mode)
		if !ok {
			fmt.Fprintln(stderr, "unrecognized mode")
			return 2
		}
		o.Mode = val
	}
	if flagSeen(fs, "freeze-mode") {
		val, ok := normalizeFreezeMode(*freeze)
		if !ok {
			fmt.Fprintln(stderr, "unrecognized freeze-mode")
			return 2
		}
		o.FreezeMode = val
	}
	if flagSeen(fs, "soft-tokens") {
		o.Budget.SoftTokens = *soft
	}
	if flagSeen(fs, "hard-tokens") {
		o.Budget.HardTokens = *hard
	}
	if flagSeen(fs, "priority") {
		o.Priority = *priority
	}
	if err := db.UpdateObjective(context.Background(), &o); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, o, o.ID+"\t"+o.Title)
}

func objectiveTransition(home string, asJSON bool, verb string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("objective "+verb, "set the objective status to "+string(objectiveVerbStatus(verb)), "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "objective %s <id>\n", verb)
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
	o.Status = objectiveVerbStatus(verb)
	if err := db.UpdateObjective(context.Background(), &o); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, o, o.ID+"\t"+string(o.Status))
}

func objectiveVerbStatus(verb string) model.ObjectiveStatus {
	switch verb {
	case "pause":
		return model.ObjectivePaused
	case "cancel":
		return model.ObjectiveCanceled
	default:
		return model.ObjectiveActive
	}
}

const cardGroupUsage = `card — read and update card records
purpose: add, list, show, cancel, retry, pause, resume, or revive a card

parameters:
  add     --id --objective --node --kind --title --body --tier --require --dep --acceptance --claim --base --status --origin
  list    --objective --json
  show    <id> --json
  cancel  <id>
  retry   <id>
  pause   <id>
  resume  <id>
  revive  <id>
`

func cardTransition(home string, asJSON bool, verb string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("card "+verb, "change a stored card to "+string(cardVerbStatus(verb))+" without starting an executor", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "card %s <id>\n", verb)
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
	if !legacyReadOnly(c.Legacy.Status) {
		c.Status = cardVerbStatus(verb)
		if err := db.UpdateCard(context.Background(), &c); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	origin, err := loadCardOrigin(context.Background(), db, c.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := c.ID + "\t" + string(c.Status)
	return emit(stdout, asJSON, cardView{Card: c, Origin: origin}, line)
}

func cardVerbStatus(verb string) model.CardStatus {
	switch verb {
	case "pause":
		return model.CardPaused
	case "cancel":
		return model.CardCanceled
	default:
		return model.CardReady
	}
}

func legacyReadOnly(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "failed", "canceled", "unparsed":
		return true
	default:
		return false
	}
}
