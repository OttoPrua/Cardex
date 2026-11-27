package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/question"
	"github.com/OttoPrua/Cardex/internal/route"
	"github.com/OttoPrua/Cardex/internal/store"
)

const projectGroupUsage = `project — read and update project records
purpose: add, list, show, or set a project stored in the home database

parameters:
  add   --id --repo --worktree-root --test-cmd --design-dir --forbidden --hard-rule --sandbox on|off --review auto|off|check|review --scratch-dir --events on|off
  list  --json
  show  <id> --json
  set   --id <id> --repo --worktree-root --test-cmd --design-dir --forbidden --hard-rule --sandbox on|off --review auto|off|check|review --scratch-dir --events on|off
`

func projectSet(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("project set", "update fields on a stored project", "--json", stdout, stderr)
	id := fs.String("id", "", "project id")
	repo := fs.String("repo", "", "main checkout")
	root := fs.String("worktree-root", "", "worktree root")
	testCmd := fs.String("test-cmd", "", "test command")
	design := fs.String("design-dir", "", "design directory")
	sandboxMode := fs.String("sandbox", "", "project default for its cards: on, or off to run them outside the Cardex sandbox")
	reviewMode := fs.String("review", "", "review mode: auto, off, check, review, or inherit (see docs/review-policy.md)")
	scratchDir := fs.String("scratch-dir", "", "scratch directory for this project's card runs and acceptance commands; overrides the home scratch_dir")
	eventsMode := fs.String("events", "", "card lifecycle events: on, or off (default off)")
	var forbidden, rules stringsFlag
	fs.Var(&forbidden, "forbidden", "forbidden path")
	fs.Var(&rules, "hard-rule", "replace project hard rules; repeat for multiple rules or pass an empty value to clear")
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
	db, err := openStoreForWrite(home)
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
	if flagSeen(fs, "hard-rule") {
		p.HardRules = rules
	}
	if flagSeen(fs, "sandbox") {
		p.Sandbox = *sandboxMode
	}
	if flagSeen(fs, "review") {
		p.Review = *reviewMode
	}
	if flagSeen(fs, "scratch-dir") {
		p.ScratchDir = *scratchDir
	}
	if flagSeen(fs, "events") {
		p.Events = *eventsMode
	}
	if err := db.UpdateProject(context.Background(), &p); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, p, p.ID+"\t"+p.RepoPath)
}

const objectiveGroupUsage = `objective — read and update objective records
purpose: add, list, show, brief, set, pause, resume, or cancel an objective

parameters:
  add     --id --project --title --acceptance --check --hard-rule --soft-tokens --hard-tokens --priority --mode --freeze-mode --origin --status --integration-branch --channel --model --review --events on|off|inherit
  list    --project --json
  show    <id> --brief [--origin <kind:id>] --json
  brief   <id> --json
  set     --id <id> --mode --freeze-mode --soft-tokens --hard-tokens --priority --hard-rule --review --events on|off|inherit
  pause   <id>
  resume  <id>
  cancel  <id>
`

func objectiveSet(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("objective set", "update mode, freeze-mode, budget, or priority on a stored objective", "--json", stdout, stderr)
	id := fs.String("id", "", "objective id")
	mode := fs.String("mode", "", "auto or manual")
	freeze := fs.String("freeze-mode", "", "safe or fast")
	soft := &softNumber{name: "soft-tokens", kind: numberInt64}
	hard := &softNumber{name: "hard-tokens", kind: numberInt64}
	priority := &softNumber{name: "priority", kind: numberInt}
	fs.Var(soft, "soft-tokens", "soft token budget")
	fs.Var(hard, "hard-tokens", "hard token budget")
	fs.Var(priority, "priority", "priority")
	reviewMode := fs.String("review", "", "review mode: auto, off, check, review, or inherit (see docs/review-policy.md)")
	eventsMode := fs.String("events", "", "card lifecycle events: on, off, or inherit (empty inherits the project; default off)")
	var rules stringsFlag
	fs.Var(&rules, "hard-rule", "replace objective hard rules; repeat for multiple rules or pass an empty value to clear")
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
	db, err := openStoreForWrite(home)
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
	var notes []string
	if flagSeen(fs, "hard-rule") {
		o.HardRules = rules
	}
	if flagSeen(fs, "mode") {
		val, ok := normalizeObjectiveMode(*mode)
		if !ok {
			notes = append(notes, "mode "+*mode)
		} else {
			o.Mode = val
		}
	}
	if flagSeen(fs, "freeze-mode") {
		val, ok := normalizeFreezeMode(*freeze)
		if !ok {
			notes = append(notes, "freeze-mode "+*freeze)
		} else {
			o.FreezeMode = val
		}
	}
	if soft.ok {
		o.Budget.SoftTokens = soft.i64
	}
	notes = append(notes, soft.bad...)
	if hard.ok {
		o.Budget.HardTokens = hard.i64
	}
	notes = append(notes, hard.bad...)
	if priority.ok {
		o.Priority = priority.n
	}
	notes = append(notes, priority.bad...)
	if flagSeen(fs, "review") {
		o.Review = *reviewMode
	}
	if flagSeen(fs, "events") {
		o.Events = *eventsMode
	}
	if err := db.UpdateObjective(context.Background(), &o); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return finishSoft(stdout, stderr, asJSON, notes, o, o.ID+"\t"+o.Title)
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
	db, err := openStoreForWrite(home)
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
	return emit(stdout, asJSON, o, o.ID+"\t"+paintStatus(stdout, asJSON, string(o.Status)))
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
purpose: add, list, show, cancel, retry, pause, resume, revive, absorb, recover, or mark merged a card

parameters:
  add     --id --objective --node --kind --title --body --tier --require --dep --acceptance --claim --base --status --origin
  list    --objective --changed-since <RFC3339> --fields <name> [--origin <kind:id>] --json
  show    <id> --brief --claims --attempts all [--origin <kind:id>] --json --summary
  cancel  <id>
  retry   <id>
  pause   <id>
  resume  <id>
  revive  <id>
  absorb  <id> --json   running card whose passed attempt was already disposed -> reviewing
  recover <id> --json                  queue the current, already approved auto-route recovery
  recover --note <code> <id> --json    save a predefined repair hint without changing routing or status
  merged  [--note <text>] <id> --json   merge_queued, reviewing or done card -> merged (after a manual merge)
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
	db, err := openStoreForWrite(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	var c model.Card
	err = db.WithWriteTx(context.Background(), func(ctx context.Context) error {
		var err error
		c, err = db.GetCard(ctx, fs.Arg(0))
		if err != nil || legacyReadOnly(c.Legacy.Status) {
			return err
		}
		target := cardVerbStatus(verb)
		leavingBlocked := c.Status == model.CardNeedsInput && (target == model.CardPaused || target == model.CardCanceled)
		if target == model.CardReady || leavingBlocked {
			events, err := db.ListEvents(ctx, "card:"+c.ID)
			if err != nil {
				return err
			}
			auto := route.LatestAutoSelection(events, c.ID)
			onAuto := auto.EventID != "" && auto.Channel != "" && c.Channel == auto.Channel
			if onAuto && leavingBlocked && !route.InterruptedRecoveryHeld(c, events) {
				ev, err := route.HoldInterruptedRecovery(c.ID, auto.EventID)
				if err != nil {
					return err
				}
				if err := db.AppendEvent(ctx, &ev); err != nil {
					return err
				}
			}
			if onAuto && target == model.CardReady {
				_, offered := route.FallbackRetryBlockFor(c, events)
				blocked := c.Status == model.CardNeedsInput || offered || route.InterruptedRecoveryHeld(c, events)
				if !blocked && (c.Status == model.CardPaused || c.Status == model.CardCanceled) {
					// Older clients already cleared WaitQuestionID on pause.
					// A same-channel retry or rework can also have a failed
					// attempt; require evidence of a recovery question.
					blocked, err = legacyAutoQuestionBlock(ctx, db, c, auto)
					if err != nil {
						return err
					}
				}
				if blocked {
					if verb != "retry" {
						return fmt.Errorf("automatic recovery needs the current approved retry decision; use card recover")
					}
					if _, err := question.RetryApprovedAutoRoute(ctx, db, c.ID); err != nil {
						return err
					}
					c, err = db.GetCard(ctx, c.ID)
					return err
				}
			}
		}
		c.Status = target
		return db.UpdateCard(ctx, &c)
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	origin, err := loadCardOrigin(context.Background(), db, c.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := c.ID + "\t" + paintStatus(stdout, asJSON, string(c.Status))
	return emit(stdout, asJSON, cardView{Card: c, Origin: origin}, line)
}

// legacyAutoQuestionBlock detects historical scheduler questions that lost
// their card link on pause. They can block a transition, never approve one.
func legacyAutoQuestionBlock(ctx context.Context, db *store.Store, card model.Card, auto route.AutoSelection) (bool, error) {
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil {
		return false, err
	}
	var newest model.Attempt
	for _, a := range attempts {
		if newest.ID == "" || a.Index > newest.Index {
			newest = a
		}
	}
	if newest.ID == "" || newest.Route.Channel != auto.Channel ||
		newest.Outcome == "" || newest.Outcome == model.OutcomePassed ||
		(!newest.StartedAt.IsZero() && newest.StartedAt.Before(auto.At)) ||
		(!newest.EndedAt.IsZero() && newest.EndedAt.Before(auto.At)) {
		return false, nil
	}
	questions, err := db.ListQuestions(ctx, card.Objective)
	if err != nil {
		return false, err
	}
	for _, q := range questions {
		if q.ScopeKind != "card" || q.Scope != card.ID || q.Kind != model.QuestionCustom {
			continue
		}
		head, details, _ := strings.Cut(q.Text, "\n")
		switch {
		case head == "no output after switching channel":
			if newest.Outcome != model.OutcomeNoOutput {
				continue
			}
			if details != "" && !strings.Contains("\n"+details, "\n- attempt "+newest.ID+" on ") {
				continue
			}
		case head == "no other channel is available", strings.HasPrefix(head, "no other channel is available ("):
		default:
			continue
		}
		// The first delivery is the oldest issuance evidence; a later
		// redelivery does not make a previous attempt's decision current.
		// With incomplete legacy timing, only the scoped recovery question
		// itself (not the mere fact of a failed attempt) can keep the hold.
		var issued time.Time
		for _, delivery := range q.Deliveries {
			if !delivery.At.IsZero() && (issued.IsZero() || delivery.At.Before(issued)) {
				issued = delivery.At
			}
		}
		if issued.IsZero() || (!issued.Before(auto.At) && !issued.Before(newest.StartedAt)) {
			return true, nil
		}
	}
	return false, nil
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

func cardAbsorb(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("card absorb", "move a running card whose latest attempt passed and was already disposed into reviewing; reuses an open review card or creates one, never starts an executor", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "card absorb <id>")
		return 2
	}
	db, err := openStoreForWrite(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	res, err := loop.AbsorbPassed(context.Background(), db, home, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	line := res.Card + "\t" + paintStatus(stdout, asJSON, string(res.Status))
	if res.Review != "" {
		line += "\treview " + res.Review + " " + paintStatus(stdout, asJSON, string(res.ReviewStatus))
	}
	return emit(stdout, asJSON, res, line)
}

func cardMerged(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("card merged", "mark a card merged after its branch was merged outside Cardex; allowed from merge_queued, reviewing or done, or waiting on a canceled repair/rebase card", "[--note <text>] <id> --json", stdout, stderr)
	note := fs.String("note", "", "optional note, e.g. the merge commit")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "card merged [--note <text>] <id>")
		return 2
	}
	db, err := openStoreForWrite(home)
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
	from := c.Status
	waitWas := c.WaitReason
	switch from {
	case model.CardMergeQueued, model.CardReviewing, model.CardDone:
	case model.CardMerged:
		return emit(stdout, asJSON, cardView{Card: c}, c.ID+"\t"+paintStatus(stdout, asJSON, string(c.Status)))
	case model.CardWaiting:
		if msg := canceledBlocker(ctx, db, c); msg != "" {
			fmt.Fprintln(stderr, msg)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "card %s is %s; only merge_queued, reviewing, done, or waiting on a canceled repair/rebase card can be marked merged\n", c.ID, from)
		return 1
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		c.Status = model.CardMerged
		c.WaitReason = ""
		if err := db.UpdateCard(ctx, &c); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]string{"card": c.ID, "from": string(from), "wait_reason": waitWas, "note": *note})
		return db.AppendEvent(ctx, &model.Event{Entity: "card:" + c.ID, Type: "card_marked_merged", Data: raw, TS: time.Now().UTC()})
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, cardView{Card: c}, c.ID+"\t"+paintStatus(stdout, asJSON, string(c.Status)))
}

// cardRecover queues the exact active, already approved automatic reroute, or
// stores a fixed prompt hint. Hint storage never mutates any card fields.
func cardRecover(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("card recover", "queue an approved recovery, or save a predefined repair hint only", "[--note <code>] <id> --json", stdout, stderr)
	noteFlag := fs.String("note", "", "store only this hint: boundary-check, rerun-checks, resolve-conflicts")
	if code, ok := parseSet(fs, hoistFlags(fs, args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "card recover [--note <code>] <id>")
		return 2
	}
	noteOnly := flagSeen(fs, "note")
	hint := model.RepairHint(*noteFlag)
	if noteOnly && hint.PromptText() == "" {
		fmt.Fprintln(stderr, "card recover: use a predefined hint code: boundary-check, rerun-checks, resolve-conflicts")
		return 2
	}
	db, err := openStoreForWrite(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	if !noteOnly {
		result, err := question.RetryApprovedAutoRoute(ctx, db, fs.Arg(0))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return emit(stdout, asJSON, result, result.Card+"\t"+string(result.Status))
	}
	var c model.Card
	err = db.WithWriteTx(ctx, func(ctx context.Context) error {
		var err error
		c, err = db.GetCard(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		if (c.Kind != model.KindRepair && c.Kind != model.KindRebase) || legacyReadOnly(c.Legacy.Status) {
			return fmt.Errorf("card %s does not accept repair hints", c.ID)
		}
		switch c.Status {
		case model.CardReady, model.CardWaiting, model.CardNeedsInput, model.CardPaused, model.CardFailed:
		default:
			return fmt.Errorf("card %s does not accept a hint from %s", c.ID, c.Status)
		}
		raw, err := json.Marshal(struct {
			Code model.RepairHint `json:"code"`
		}{Code: hint})
		if err != nil {
			return err
		}
		return db.AppendEvent(ctx, &model.Event{Entity: "card:" + c.ID, Type: model.EventRepairHint, Data: raw})
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Acknowledge without echoing a stored card body or diagnostic archive.
	return emit(stdout, asJSON, map[string]string{"card": c.ID, "status": string(c.Status), "hint": string(hint)}, c.ID+"\t"+string(c.Status))
}

// canceledBlocker returns "" when a waiting card is blocked only by a repair or
// rebase card that has been canceled, so marking it merged cannot strand live work.
func canceledBlocker(ctx context.Context, db *store.Store, c model.Card) string {
	f := strings.Fields(c.WaitReason)
	if len(f) != 2 || (f[0] != "repair" && f[0] != "rebase") {
		return fmt.Sprintf("card %s is waiting (%q); only a wait on a repair or rebase card can be overridden", c.ID, c.WaitReason)
	}
	b, err := db.GetCard(ctx, f[1])
	if err != nil {
		return fmt.Sprintf("card %s waits on %s: %v", c.ID, f[1], err)
	}
	if b.Status != model.CardCanceled {
		return fmt.Sprintf("card %s waits on %s %s, which is %s; cancel it first", c.ID, f[0], b.ID, b.Status)
	}
	return ""
}
