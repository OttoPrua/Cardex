package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/importer"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/positioning"
	"github.com/OttoPrua/Cardex/internal/question"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/sched"
	"github.com/OttoPrua/Cardex/internal/scorecard"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

const nodeGroupUsage = `node — read and change design nodes
purpose: list, show, freeze, or reopen a design node

parameters:
  list    --objective --json
  show    <id> --json
  freeze  <id>
  reopen  <id>
`

func cmdNode(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, nodeGroupUsage) {
		return 0
	}
	switch args[0] {
	case "list":
		return nodeList(home, asJSON, args[1:], stdout, stderr)
	case "show":
		return nodeShow(home, asJSON, args[1:], stdout, stderr)
	case "freeze":
		return nodeStatus(home, asJSON, model.NodeFrozen, args[1:], stdout, stderr)
	case "reopen":
		return nodeStatus(home, asJSON, model.NodeReopened, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown node verb %q\n", args[0])
		return 2
	}
}

func nodeList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("node list", "list design nodes, optionally for one objective", "--json", stdout, stderr)
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
	items, err := db.ListNodes(context.Background(), *objective)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, items)
	}
	for _, n := range items {
		fmt.Fprintf(stdout, "%s\t%s\n", n.ID, paintStatus(stdout, false, string(n.Status)))
	}
	return 0
}

func nodeShow(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("node show", "show one stored design node", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "node show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	n, err := db.GetNode(context.Background(), fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, n, n.ID+"\t"+paintStatus(stdout, asJSON, string(n.Status)))
}

func nodeStatus(home string, asJSON bool, status model.NodeStatus, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("node "+statusVerb(status), "set a design node status to "+string(status), "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "node %s <id>\n", statusVerb(status))
		return 2
	}
	db, err := openStoreForWrite(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	n, err := db.GetNode(context.Background(), fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch status {
	case model.NodeReopened:
		err = applyNodeReopen(db, &n)
	case model.NodeFrozen:
		err = applyNodeFreeze(db, &n)
	default:
		n.Status = status
		err = db.UpdateNode(context.Background(), &n)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, n, n.ID+"\t"+paintStatus(stdout, asJSON, string(n.Status)))
}

func statusVerb(status model.NodeStatus) string {
	if status == model.NodeFrozen {
		return "freeze"
	}
	return "reopen"
}

const loopReopenUsed = "loop.reopen_used"

type loopReopenNote struct {
	Question string   `json:"question"`
	Freeze   []string `json:"freeze"`
	Replan   string   `json:"replan"`
}

func applyNodeReopen(db *store.Store, n *model.DesignNode) error {
	ctx := context.Background()
	n.Status = model.NodeReopened
	if err := db.UpdateNode(ctx, n); err != nil {
		return err
	}
	cards, err := db.ListCards(ctx, n.Objective)
	if err != nil {
		return err
	}
	for i := range cards {
		if cards[i].Node != n.ID || !nodeNotStarted(cards[i].Status) {
			continue
		}
		cards[i].Status = model.CardPaused
		if err := db.UpdateCard(ctx, &cards[i]); err != nil {
			return err
		}
	}
	replanID, inFlight := nodeReplanInFlight(cards, n.ID)
	if !inFlight {
		card := model.Card{
			Objective: n.Objective,
			Node:      n.ID,
			Kind:      model.KindReplan,
			Title:     "重规划",
			Body:      "重规划\n",
			Tier:      model.DefaultTier(model.KindReplan),
			Status:    model.CardReady,
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			return err
		}
		replanID = card.ID
	}
	questions, err := db.ListQuestions(ctx, n.Objective)
	if err != nil {
		return err
	}
	used, err := usedReopenQuestions(ctx, db, n.ID)
	if err != nil {
		return err
	}
	var freezeIDs []string
	var reopenIDs []string
	qs := question.New(db, nil)
	for _, q := range questions {
		if q.Kind == model.QuestionFreeze && q.Scope == n.ID {
			freezeIDs = append(freezeIDs, q.ID)
			if err := acceptOpenFreeze(ctx, qs, q); err != nil {
				return err
			}
		}
		if q.Kind != model.QuestionReopen || q.Scope != n.ID || used[q.ID] {
			continue
		}
		if !questionConsented(q) && len(q.Options) > 0 && strings.TrimSpace(q.Answer) == "" {
			answered, err := qs.Answer(ctx, q.ID, q.Options[0])
			if err != nil {
				return err
			}
			q = answered
		}
		if questionConsented(q) {
			reopenIDs = append(reopenIDs, q.ID)
		}
	}
	if len(reopenIDs) == 0 {
		return appendReopenNote(ctx, db, n.ID, "", freezeIDs, replanID)
	}
	for _, id := range reopenIDs {
		if err := appendReopenNote(ctx, db, n.ID, id, freezeIDs, replanID); err != nil {
			return err
		}
	}
	return nil
}

func applyNodeFreeze(db *store.Store, n *model.DesignNode) error {
	ctx := context.Background()
	n.Status = model.NodeFrozen
	if err := db.UpdateNode(ctx, n); err != nil {
		return err
	}
	questions, err := db.ListQuestions(ctx, n.Objective)
	if err != nil {
		return err
	}
	qs := question.New(db, nil)
	for _, q := range questions {
		if q.Kind != model.QuestionFreeze || q.Scope != n.ID {
			continue
		}
		if err := acceptOpenFreeze(ctx, qs, q); err != nil {
			return err
		}
	}
	return nil
}

// acceptOpenFreeze stores an explicit accepting decision on an unanswered
// freeze question. Pending, Blocked, and the reminder loop read Answer.
// A refusal already stored, including 拒绝, is left as it is. 同意 is the
// accepting choice when it is one of the options; otherwise the first listed
// choice is stored. With no options the service accepts any non-empty answer,
// so the same accepting word is stored.
func acceptOpenFreeze(ctx context.Context, qs question.Service, q model.Question) error {
	if strings.TrimSpace(q.Answer) != "" {
		return nil
	}
	_, err := qs.Answer(ctx, q.ID, acceptingFreezeChoice(q.Options))
	return err
}

func acceptingFreezeChoice(options []string) string {
	for _, opt := range options {
		if opt == "同意" {
			return opt
		}
	}
	for _, opt := range options {
		if strings.TrimSpace(opt) != "" {
			return opt
		}
	}
	return "同意"
}

func appendReopenNote(ctx context.Context, db *store.Store, nodeID, questionID string, freezeIDs []string, replanID string) error {
	raw, err := json.Marshal(loopReopenNote{Question: questionID, Freeze: freezeIDs, Replan: replanID})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{Entity: "node:" + nodeID, Type: loopReopenUsed, Data: raw})
}

func usedReopenQuestions(ctx context.Context, db *store.Store, nodeID string) (map[string]bool, error) {
	ev, err := db.ListEvents(ctx, "node:"+nodeID)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, e := range ev {
		if e.Type != loopReopenUsed || len(e.Data) == 0 {
			continue
		}
		var note loopReopenNote
		if err := json.Unmarshal(e.Data, &note); err != nil {
			return nil, err
		}
		if note.Question != "" {
			used[note.Question] = true
		}
	}
	return used, nil
}

func questionConsented(q model.Question) bool {
	if strings.TrimSpace(q.Answer) == "" || len(q.Options) == 0 {
		return false
	}
	return q.Answer == q.Options[0]
}

func nodeNotStarted(st model.CardStatus) bool {
	switch st {
	case model.CardReady, model.CardWaiting, model.CardNeedsInput:
		return true
	default:
		return false
	}
}

func nodeTerminal(st model.CardStatus) bool {
	switch st {
	case model.CardDone, model.CardMerged, model.CardCanceled, model.CardFailed:
		return true
	default:
		return false
	}
}

func nodeReplanInFlight(cards []model.Card, nodeID string) (string, bool) {
	for _, c := range cards {
		if c.Node != nodeID || c.Kind != model.KindReplan {
			continue
		}
		if nodeTerminal(c.Status) || c.Status == model.CardPaused || nodeNotStarted(c.Status) {
			continue
		}
		return c.ID, true
	}
	return "", false
}

const questionGroupUsage = `question — list, show, and answer stored questions
purpose: answer goes through question.Service and stores only a listed option

parameters:
  list    --objective --json
  show    <id> --json
  answer  <id> <option> [--retry] --json
`

func cmdQuestion(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, questionGroupUsage) {
		return 0
	}
	switch args[0] {
	case "list":
		return questionList(home, asJSON, args[1:], stdout, stderr)
	case "show":
		return questionShow(home, asJSON, args[1:], stdout, stderr)
	case "answer":
		return questionAnswer(home, asJSON, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown question verb %q\n", args[0])
		return 2
	}
}

func questionList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("question list", "list stored questions", "--json", stdout, stderr)
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
	items, err := db.ListQuestions(context.Background(), *objective)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, items)
	}
	for _, q := range items {
		fmt.Fprintf(stdout, "%s\t%s\n", q.ID, q.Text)
	}
	return 0
}

func questionShow(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("question show", "show one stored question", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "question show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	q, err := db.GetQuestion(context.Background(), fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, q, q.ID+"\t"+q.Answer)
}

func questionAnswer(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("question answer", "save one stored option; optionally queue an approved transient retry", "<id> <option> [--retry] --json", stdout, stderr)
	retry := fs.Bool("retry", false, "atomically save approve and return this card to the ready queue")
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if flagToken(arg) == "retry" {
			args = hoistFlags(fs, args)
			break
		}
	}
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 || (*retry && fs.NArg() != 2) {
		fmt.Fprintln(stderr, "question answer <id> <option> [--retry]")
		return 2
	}
	option := ""
	if fs.NArg() >= 2 {
		option = fs.Arg(1)
	}
	db, err := openStoreForWrite(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	if *retry {
		result, err := question.AnswerAndRetry(ctx, db, fs.Arg(0), option)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return emit(stdout, asJSON, result, result.Question+"\t"+result.Answer+"\t"+result.Card+"\t"+string(result.Status))
	}
	svc := question.New(db, nil)
	q, err := svc.Answer(ctx, fs.Arg(0), option)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, q, q.ID+"\t"+q.Answer)
}

const channelGroupUsage = `channel — read and update channel records
purpose: list, show, set, or check channels. set changes redline, windows, and concurrency

parameters:
  list   --json
  show   <id> --json
  set    --id --runner --redline --concurrency --window name:rule[:tokens] --model
  set    --id <id> --dry-run --model <name> [--model <name> ...]
  set    --id <id> --dry-run [--add-model <name>] [--remove-model <name>]
  set    --id <id> --if-models <token> [--add-model <name>] [--remove-model <name>]
  check  [--probe --channel ID --model ID --confirm-possible-charge]
  import-usage --id <channel> --provider <provider> --input <CodexBar JSON file or ->
`

func cmdChannel(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, channelGroupUsage) {
		return 0
	}
	switch args[0] {
	case "list":
		return channelList(home, asJSON, args[1:], stdout, stderr)
	case "show":
		return channelShow(home, asJSON, args[1:], stdout, stderr)
	case "set":
		return channelSet(home, asJSON, args[1:], stdout, stderr)
	case "check":
		return channelCheck(home, asJSON, args[1:], stdout, stderr)
	case "import-usage":
		return channelImportUsage(home, asJSON, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown channel verb %q\n", args[0])
		return 2
	}
}

func channelList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("channel list", "list stored channels", "--json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	items, err := db.ListChannels(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, items)
	}
	for _, ch := range items {
		fmt.Fprintf(stdout, "%s\t%s\n", ch.ID, ch.Runner)
	}
	return 0
}

func channelShow(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("channel show", "show one stored channel", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "channel show <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ch, err := db.GetChannel(context.Background(), fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, ch, ch.ID+"\t"+string(ch.Runner))
}

func channelSet(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("channel set", "set channel options or preview/edit the model list", "--json", stdout, stderr)
	id := fs.String("id", "", "channel id")
	runnerName := fs.String("runner", "", "runner name")
	engineProfile := fs.String("engine-profile", "", "engines profile name")
	family := fs.String("family", "", "model family")
	redline := &softNumber{name: "redline", kind: numberFloat}
	concurrency := &softNumber{name: "concurrency", kind: numberInt}
	fs.Var(redline, "redline", "redline from 0 to 1 inclusive")
	fs.Var(concurrency, "concurrency", "concurrency limit")
	health := fs.String("health", "", "ok, auth_failed, or degraded")
	var windows, models, addModels, removeModels stringsFlag
	dryRun := fs.Bool("dry-run", false, "preview only from an existing checkpointed database; never write")
	expected := fs.String("if-models", "", "apply only the add/remove edit approved by the preview token")
	fs.Var(&windows, "window", "name:rule or name:rule:token-limit")
	fs.Var(&models, "model", "replace the entire model list (repeatable)")
	fs.Var(&addModels, "add-model", "append a model if absent (repeatable)")
	fs.Var(&removeModels, "remove-model", "remove an existing model (repeatable)")
	if modelChangeArgs(fs, args) {
		args = hoistFlags(fs, args)
	}
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if *id == "" && fs.NArg() == 1 {
		*id = fs.Arg(0)
	}
	if *id == "" {
		fmt.Fprintln(stderr, "channel set requires --id")
		return 2
	}
	if flagSeen(fs, "dry-run") || flagSeen(fs, "if-models") || len(addModels) > 0 || len(removeModels) > 0 {
		if (flagSeen(fs, "dry-run") && !*dryRun) || (*dryRun && flagSeen(fs, "if-models")) {
			fmt.Fprintln(stderr, "use --dry-run to preview or --if-models to apply, not both")
			return 2
		}
		if fs.NArg() > 1 || (flagSeen(fs, "id") && fs.NArg() != 0) {
			fmt.Fprintln(stderr, "supply exactly one channel id")
			return 2
		}
		for _, unrelated := range []string{"runner", "engine-profile", "family", "redline", "concurrency", "health", "window"} {
			if flagSeen(fs, unrelated) {
				fmt.Fprintf(stderr, "--%s cannot be mixed with a model preview or add/remove edit\n", unrelated)
				return 2
			}
		}
		return channelModelChange(home, asJSON, *dryRun, *id, *expected,
			modelEdit{replace: models, add: addModels, remove: removeModels}, stdout, stderr)
	}
	applyCommandProxy(home, stderr)
	db, err := openStoreForWrite(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	requestedHealth := model.Health(strings.TrimSpace(*health))
	deactivationOnly := requestedHealth == model.HealthDegraded || requestedHealth == model.HealthAuthFailed
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "id" && f.Name != "health" && f.Name != "json" {
			deactivationOnly = false
		}
	})
	var ch model.Channel
	var notes []string
	exit := 1
	invalidSet := func(reason string) error {
		exit = 2
		return errors.New(reason)
	}
	err = db.WithTx(context.Background(), func(ctx context.Context) error {
		ch, err = db.GetChannel(ctx, *id)
		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			return err
		}
		previouslyCompatible := runner.IsCompatibleBinding(ch.EngineProfile)
		previousBindingID := runner.CompatibleBindingID(ch.EngineProfile)
		// Disabling is always safe: a broken config or credential must not
		// prevent the operator from taking this identity out of routing.
		if previouslyCompatible && deactivationOnly {
			ch.Health = requestedHealth
			return db.UpdateChannel(ctx, &ch)
		}
		if missing {
			raw := strings.TrimSpace(*runnerName)
			val := model.RunnerName(raw)
			if raw == "" || val == model.RunnerFake {
				return invalidSet("--runner is required when creating a channel")
			}
			if !val.Valid() {
				notes = append(notes, "runner "+*runnerName)
				val = model.RunnerGrok
			}
			ch = model.Channel{ID: *id, Health: model.HealthOK, Runner: val}
		} else if flagSeen(fs, "runner") {
			val := model.RunnerName(strings.TrimSpace(*runnerName))
			if val.Valid() && val != "" {
				ch.Runner = val
			} else {
				notes = append(notes, "runner "+*runnerName)
			}
		}
		if flagSeen(fs, "engine-profile") {
			name := strings.TrimSpace(*engineProfile)
			if name == "" {
				return invalidSet("engine profile name is empty")
			}
			ch.EngineProfile = name
		}
		if flagSeen(fs, "family") {
			name := strings.TrimSpace(*family)
			if err := runner.ValidateChannelFamily(name); err != nil {
				return invalidSet(err.Error())
			}
			ch.Family = name
		}
		if redline.ok {
			ch.Redline = redline.f64
		}
		notes = append(notes, redline.bad...)
		if concurrency.ok {
			ch.Concurrency = concurrency.n
		}
		notes = append(notes, concurrency.bad...)
		if flagSeen(fs, "health") {
			val := model.Health(strings.TrimSpace(*health))
			if val.Valid() {
				ch.Health = val
			} else {
				notes = append(notes, "health "+*health)
				if !ch.Health.Valid() {
					ch.Health = model.HealthOK
				}
			}
		}
		if flagSeen(fs, "model") {
			ch.Models = models
		}
		if flagSeen(fs, "window") {
			parsed, bad := splitWindows(windows)
			notes = append(notes, bad...)
			if len(parsed) > 0 {
				ch.QuotaWindows = parsed
			}
		}
		if ch.Health == "" {
			ch.Health = model.HealthOK
		}
		if missing || previouslyCompatible || flagSeen(fs, "engine-profile") || flagSeen(fs, "family") ||
			(flagSeen(fs, "runner") && ch.EngineProfile != "") {
			others, err := db.ListChannels(ctx)
			if err != nil {
				return err
			}
			// Store-only rows omit the active config seed on unapproved homes.
			// First identify compatible bindings, then check the exact current
			// positioning snapshot within this channel update.
			projects, err := db.ListProjects(ctx)
			if err != nil {
				return err
			}
			var managedRoots []string
			for _, project := range projects {
				managedRoots = append(managedRoots, project.RepoPath)
				if project.WorktreeRoot != "" {
					managedRoots = append(managedRoots, project.WorktreeRoot)
				}
			}
			next, err := runner.ValidateChannelBinding(dir, ch, others, nil, managedRoots...)
			if err != nil {
				return invalidSet(err.Error())
			}
			ch = next
			if runner.IsCompatibleBinding(ch.EngineProfile) {
				rows, err := positioning.Inspect(dir, db).Current(ctx)
				if err != nil {
					return err
				}
				if err := model.ValidateCompatiblePositioning(ch, rows); err != nil {
					return invalidSet(err.Error())
				}
			}
		}
		if previouslyCompatible && !runner.IsCompatibleBinding(ch.EngineProfile) {
			return invalidSet("an api channel cannot be converted to a legacy channel; use a different channel id")
		}
		if runner.IsCompatibleBinding(ch.EngineProfile) {
			if len(notes) != 0 {
				return invalidSet("api channel has invalid options; no changes applied")
			}
			if flagSeen(fs, "model") {
				for _, name := range models {
					if strings.TrimSpace(name) == "" {
						return invalidSet("api channel model id is empty")
					}
				}
			}
			if flagSeen(fs, "health") && ch.Health == model.HealthOK {
				return invalidSet(model.CompatibleRuntimeHold)
			}
			if previousBindingID != runner.CompatibleBindingID(ch.EngineProfile) {
				if previouslyCompatible && !flagSeen(fs, "engine-profile") && !flagSeen(fs, "model") && !flagSeen(fs, "family") {
					return invalidSet("api channel profile changed; explicitly stage it with --engine-profile")
				}
			}
			if ch.Health == model.HealthOK {
				ch.Health = model.HealthDegraded
			}
		}
		if missing {
			return db.CreateChannel(ctx, &ch)
		}
		return db.UpdateChannel(ctx, &ch)
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exit
	}
	return finishSoft(stdout, stderr, asJSON, notes, ch, ch.ID+"\t"+string(ch.Runner))
}

func splitWindows(raw []string) ([]model.QuotaWindow, []string) {
	var good []model.QuotaWindow
	var bad []string
	for _, item := range raw {
		parsed, err := parseWindows([]string{item})
		if err != nil {
			bad = append(bad, "window "+item)
			continue
		}
		good = append(good, parsed...)
	}
	return good, bad
}

func parseWindows(raw []string) ([]model.QuotaWindow, error) {
	out := make([]model.QuotaWindow, 0, len(raw))
	for _, item := range raw {
		parts := strings.Split(item, ":")
		if len(parts) < 1 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("window %q needs a name", item)
		}
		w := model.QuotaWindow{Name: parts[0]}
		if len(parts) > 1 {
			w.ResetRule = parts[1]
		}
		if len(parts) > 2 && parts[2] != "" {
			n, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("window %q token limit: %w", item, err)
			}
			w.LimitTokens = n
		}
		out = append(out, w)
	}
	return out, nil
}

var runCompatibleProbe = runner.ProbeCompatible

func channelCheck(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("channel check", "report channel sign-in, tier, redline, and window problems with suggestions", "no command arguments for local checks; [--probe --channel ID --model ID --confirm-possible-charge] --json", stdout, stderr)
	probe := fs.Bool("probe", false, "send one minimal Anthropic-compatible Messages request")
	channelID := fs.String("channel", "", "one staged compatible channel")
	modelID := fs.String("model", "", "one exact model offered by that channel")
	acceptCharge := fs.Bool("confirm-possible-charge", false, "explicitly authorize the single probe, which may incur a charge")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	hasProbeFlags := flagSeen(fs, "probe") || flagSeen(fs, "channel") || flagSeen(fs, "model") || flagSeen(fs, "confirm-possible-charge")
	if hasProbeFlags && (!*probe || !*acceptCharge || strings.TrimSpace(*channelID) == "" ||
		strings.TrimSpace(*modelID) == "" || fs.NArg() != 0) {
		fmt.Fprintln(stderr, "a probe requires --probe --channel ID --model ID --confirm-possible-charge")
		return 2
	}
	resolved, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	db, err := openStore(resolved)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	if *probe {
		if refuseManagedCardWrite(stderr) {
			return 2
		}
		ctx := context.Background()
		ch, err := db.GetChannel(ctx, *channelID)
		if err != nil {
			fmt.Fprintln(stderr, "compatible channel not found")
			return 1
		}
		projects, err := db.ListProjects(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "compatible channel workspace boundary unavailable")
			return 1
		}
		var roots []string
		for _, project := range projects {
			roots = append(roots, project.RepoPath)
			if project.WorktreeRoot != "" {
				roots = append(roots, project.WorktreeRoot)
			}
		}
		receipt := runCompatibleProbe(ctx, resolved, ch, *modelID, roots)
		line := fmt.Sprintf("channel=%q endpoint=%q requested_model=%q returned_model=%q model_source=%s status=%s attempted=%t",
			receipt.Channel, receipt.Endpoint, receipt.RequestedModel, receipt.ReturnedModel, receipt.ModelSource, receipt.Status, receipt.Attempted)
		if code := emit(stdout, asJSON, receipt, line); code != 0 {
			return code
		}
		if receipt.Status != "ok" {
			return 1
		}
		return 0
	}
	for _, item := range channelFindings(db, resolved, true) {
		fmt.Fprintf(stdout, "problem: %s\nsuggestion: %s\n", item.problem, item.suggestion)
	}
	return 0
}

const routeGroupUsage = `route — explain a card's recorded route
purpose: print the stored structured route reason (candidates, filtered reasons, and scores)

parameters:
  explain <card> --json
`

func cmdRoute(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, routeGroupUsage) {
		return 0
	}
	if args[0] != "explain" {
		fmt.Fprintf(stderr, "unknown route verb %q\n", args[0])
		return 2
	}
	fs := newFlagSet("route explain", "print the attempt's stored route reason: candidates, filtered reasons, and scores", "<card> --json", stdout, stderr)
	if code, ok := parseSet(fs, args[1:]); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "route explain <card>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	cardID := fs.Arg(0)
	if _, err := db.GetCard(context.Background(), cardID); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	attempts, err := db.ListAttempts(context.Background(), cardID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	reason := model.RouteReason{
		Candidates: []model.RouteCandidate{},
		Filtered:   []model.FilteredCandidate{},
		Scores:     []model.RouteScore{},
	}
	var comparison *model.ComparisonResult
	if len(attempts) > 0 {
		best := attempts[0]
		for _, attempt := range attempts[1:] {
			if attempt.Index >= best.Index {
				best = attempt
			}
		}
		reason = best.RouteReason
		if best.StrictComparison() {
			comparison, err = db.GetStrictComparisonResult(context.Background(), best.ID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
	}
	if !asJSON {
		fmt.Fprintf(stdout, "card %s\n", cardID)
	}
	code := writeJSON(stdout, struct {
		model.RouteReason
		Comparison *model.ComparisonResult `json:"comparison_result,omitempty"`
	}{reason, comparison})
	if !asJSON && code == 0 {
		if line := lastNoCandidate(db, cardID); line != "" {
			fmt.Fprintln(stdout, line)
		}
	}
	return code
}

// lastNoCandidate is the newest scheduler note on why the card was not dispatched.
func lastNoCandidate(db *store.Store, cardID string) string {
	events, err := db.ListEvents(context.Background(), "card:"+cardID)
	if err != nil {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != sched.EventRouteNoCandidate {
			continue
		}
		var ex struct {
			Reason string `json:"reason"`
			Hint   string `json:"hint"`
		}
		if json.Unmarshal(events[i].Data, &ex) != nil {
			return ""
		}
		out := "not dispatched (" + events[i].TS.Local().Format("2006-01-02 15:04:05") + "): " + ex.Reason
		if ex.Hint != "" {
			out += "\nnext step: " + ex.Hint
		}
		return out
	}
	return ""
}

const positioningGroupUsage = `positioning — show and approve the model positioning table
purpose: show, propose, list proposals, approve, or reject. approve and reject call positioning.Store
the scheduler routes only from an approved table; a fresh install gets a starter proposal to approve

parameters:
  show        --json
  propose     [--file <json>] --json
  proposals   --json
  approve     <id>
  reject      <id>
`

func cmdPositioning(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, positioningGroupUsage) {
		return 0
	}
	switch args[0] {
	case "show":
		return positioningShow(home, asJSON, args[1:], stdout, stderr)
	case "propose":
		return positioningPropose(home, asJSON, args[1:], stdout, stderr)
	case "proposals":
		return positioningProposals(home, asJSON, args[1:], stdout, stderr)
	case "approve":
		return positioningDecide(home, true, args[1:], stdout, stderr)
	case "reject":
		return positioningDecide(home, false, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown positioning verb %q\n", args[0])
		return 2
	}
}

func openPositioning(home string, forWrite bool) (*positioning.Home, error) {
	dir, err := resolveHome(home)
	if err != nil {
		return nil, err
	}
	if forWrite {
		return positioning.OpenForWrite(dir)
	}
	return positioning.Open(dir)
}

func positioningShow(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("positioning show", "show the current positioning table from positioning.Store", "--json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	h, err := openPositioning(home, false)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer h.Close()
	rows, err := h.Current(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, rows)
	}
	for _, row := range rows {
		fmt.Fprintf(stdout, "%s\t%s\n", row.ID, row.Model)
	}
	if approved, err := h.Approved(context.Background()); err == nil && !approved {
		fmt.Fprintln(stderr, unapprovedHint(h))
	}
	return 0
}

// unapprovedHint says the table is not routed yet and how to approve one.
func unapprovedHint(h *positioning.Home) string {
	pending, err := h.Pending()
	if err == nil && len(pending) > 0 {
		id := pending[len(pending)-1].ID
		return "note: no positioning is approved yet, so no card will be dispatched. approve the pending proposal: cardex positioning approve " + id
	}
	return "note: no positioning is approved yet, so no card will be dispatched. run: cardex positioning propose, then cardex positioning approve <id>"
}

func positioningPropose(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("positioning propose", "write a proposal: the starter table (config.json seed, else one row per channel model) or a JSON file", "--file --json", stdout, stderr)
	file := fs.String("file", "", "JSON file: an array of positioning rows or {\"positioning\": [...]}")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	h, err := openPositioning(home, false)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer h.Close()
	ctx := context.Background()
	var entries []model.Positioning
	if *file != "" {
		entries, err = readPositioningFile(*file)
	} else {
		var chans []model.Channel
		chans, err = h.Channels(ctx)
		if err == nil {
			entries, err = h.StarterEntries(chans)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintln(stderr, "nothing to propose: add a channel with a model first (cardex channel set -id <id> -runner <runner> -model <model>)")
		return 1
	}
	p, err := h.Propose(ctx, entries)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, map[string]any{"id": p.ID, "entries": p.Entries, "diff": p.Diff, "approve": "cardex positioning approve " + p.ID})
	}
	fmt.Fprintf(stdout, "proposal %s\n", p.ID)
	for _, row := range p.Entries {
		kinds := "all"
		if len(row.Kinds) > 0 {
			parts := make([]string, len(row.Kinds))
			for i, k := range row.Kinds {
				parts[i] = string(k)
			}
			kinds = strings.Join(parts, ",")
		}
		fmt.Fprintf(stdout, "  %s\tfamily=%s\ttiers=%s\tkinds=%s\tpref=%d\n", row.Model, row.Family, joinTierList(row.Tiers), kinds, row.Preference)
	}
	fmt.Fprintf(stdout, "approve with: cardex positioning approve %s\n", p.ID)
	return 0
}

func joinTierList(tiers []model.Tier) string {
	parts := make([]string, 0, len(tiers))
	for _, t := range tiers {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ",")
}

func readPositioningFile(path string) ([]model.Positioning, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []model.Positioning
	if err := json.Unmarshal(body, &rows); err == nil {
		return rows, nil
	}
	var wrapped struct {
		Positioning []model.Positioning `json:"positioning"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("%s: want a JSON array of positioning rows or {\"positioning\": [...]}: %v", path, err)
	}
	return wrapped.Positioning, nil
}

func positioningProposals(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("positioning proposals", "list proposal files written by positioning.Store", "--json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	items, err := readProposals(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, items)
	}
	for _, item := range items {
		fmt.Fprintf(stdout, "%s\t%s\n", item.ID, paintStatus(stdout, false, item.Status))
	}
	return 0
}

type storedProposal struct {
	TierConvention string              `json:"tier_convention,omitempty"`
	ID             string              `json:"id"`
	Entries        []model.Positioning `json:"entries"`
	Diff           string              `json:"diff"`
	Status         string              `json:"status"`
}

func readProposals(home string) ([]storedProposal, error) {
	dir := filepath.Join(home, "positioning", "proposals")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []storedProposal{}, nil
		}
		return nil, err
	}
	out := []storedProposal{}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			return nil, err
		}
		var item storedProposal
		if err := json.Unmarshal(body, &item); err != nil {
			return nil, err
		}
		if item.TierConvention == positioning.ProposalTierWire {
			item.Entries = positioning.TranslateProposalWireTiers(item.Entries)
		} else if item.Status == "" || item.Status == "proposed" {
			item.Status = "legacy_reproposal_required"
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func positioningDecide(home string, approve bool, args []string, stdout, stderr io.Writer) int {
	name := "reject"
	purpose := "reject a proposal through positioning.Store"
	if approve {
		name = "approve"
		purpose = "approve a proposal through positioning.Store and make it current"
	}
	fs := newFlagSet("positioning "+name, purpose, "<id>", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "positioning %s <id>\n", name)
		return 2
	}
	h, err := openPositioning(home, approve)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer h.Close()
	id := fs.Arg(0)
	if approve {
		err = h.Approve(context.Background(), id)
	} else {
		err = h.Reject(context.Background(), id)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, name+" "+id)
	return 0
}

const scheduleGroupUsage = `schedule — list configured schedules and enqueue one template
purpose: list prints names from config.json. run enqueues that name's card template once and does not start an executor

parameters:
  list --json
  run <name> --json
`

func cmdSchedule(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, scheduleGroupUsage) {
		return 0
	}
	switch args[0] {
	case "list":
		return scheduleList(home, asJSON, args[1:], stdout, stderr)
	case "run":
		return scheduleRun(home, asJSON, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown schedule verb %q\n", args[0])
		return 2
	}
}

type scheduleFile struct {
	TierConvention string          `json:"tier_convention"`
	Schedules      []scheduleEntry `json:"schedules"`
}

type scheduleEntry struct {
	tierConvention string
	Name           string       `json:"name"`
	Every          string       `json:"every"`
	Cron           string       `json:"cron"`
	CronLite       string       `json:"cron-lite"`
	Template       cardTemplate `json:"card_template"`
}

type cardTemplate struct {
	Objective  string   `json:"objective"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Tier       string   `json:"tier"`
	Acceptance []string `json:"acceptance"`
	Requires   []string `json:"requires"`
}

func loadSchedules(home string) ([]scheduleEntry, string, error) {
	dir, err := resolveHome(home)
	if err != nil {
		return nil, "", err
	}
	body, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, dir, nil
		}
		return nil, dir, err
	}
	var file scheduleFile
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, dir, nil
	}
	for i := range file.Schedules {
		file.Schedules[i].tierConvention = file.TierConvention
	}
	return file.Schedules, dir, nil
}

func scheduleList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("schedule list", "print configured schedule names", "--json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	items, _, err := loadSchedules(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	names := []string{}
	for _, item := range items {
		if strings.TrimSpace(item.Name) != "" {
			names = append(names, item.Name)
		}
	}
	if asJSON {
		return writeJSON(stdout, names)
	}
	for _, name := range names {
		fmt.Fprintln(stdout, name)
	}
	return 0
}

func scheduleRun(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("schedule run", "enqueue one card from the named template and do not start an executor", "<name> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "schedule run <name>")
		return 2
	}
	name := fs.Arg(0)
	items, _, err := loadSchedules(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var entry *scheduleEntry
	for i := range items {
		if items[i].Name == name {
			entry = &items[i]
			break
		}
	}
	if entry == nil {
		fmt.Fprintf(stderr, "unknown schedule %q\n", name)
		return 1
	}
	db, err := openStoreForWrite(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	card, err := enqueueTemplate(context.Background(), db, *entry)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(stdout, asJSON, card, card.ID+"\t"+card.Title)
}

func enqueueTemplate(ctx context.Context, db *store.Store, entry scheduleEntry) (model.Card, error) {
	tmpl := entry.Template
	tierLabel, err := model.TierFromConvention(model.Tier(strings.TrimSpace(tmpl.Tier)), entry.tierConvention)
	if err != nil {
		return model.Card{}, err
	}
	tmpl.Tier = string(tierLabel)
	objID := strings.TrimSpace(tmpl.Objective)
	if objID == "" {
		objs, err := db.ListObjectives(ctx, "")
		if err != nil {
			return model.Card{}, err
		}
		for _, obj := range objs {
			if obj.Status == model.ObjectiveActive {
				objID = obj.ID
				break
			}
		}
	}
	if objID == "" {
		return model.Card{}, fmt.Errorf("schedule %s has no objective", entry.Name)
	}
	kind := model.CardKind(tmpl.Kind)
	if !kind.Valid() || kind == "" {
		kind = model.KindRanking
	}
	tier := model.Tier(tmpl.Tier)
	if tier == "" {
		tier = model.DefaultTier(kind)
	}
	title := tmpl.Title
	if title == "" {
		title = entry.Name
	}
	id, err := newID("sch")
	if err != nil {
		return model.Card{}, err
	}
	card := model.Card{
		ID: id, Objective: objID, Kind: kind, Title: title, Body: tmpl.Body,
		Tier: tier, Status: model.CardReady, Acceptance: tmpl.Acceptance,
		Requires: append([]string(nil), tmpl.Requires...),
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		return model.Card{}, err
	}
	return card, nil
}

func cmdScorecard(home string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("scorecard", "print scorecard.Compute rows, or the package error when Compute cannot aggregate", "no command arguments", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	dir := home
	if resolved, err := resolveHome(home); err == nil {
		dir = resolved
	}
	rows, err := scorecard.Compute(context.Background(), dir)
	if err != nil {
		fmt.Fprintln(stdout, err.Error())
		return 1
	}
	return writeJSON(stdout, rows)
}

func cmdHost(home string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("host", "run host.Run for one attempt id. an unknown attempt fails before any provider CLI starts", "<attempt-id>", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "host <attempt-id>")
		return 2
	}
	if refuseManagedRuntimeStart(stderr) {
		return 2
	}
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := host.Run(host.WithWorkspace(context.Background(), workspace.Git{}), dir, fs.Arg(0)); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "host finished "+fs.Arg(0))
	return 0
}

func cmdImport(home string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("import-legacy", "read a v1 home and map its tasks into this v2 home. the v1 tree is not written", "<v1-home>", stdout, stderr)
	dry := fs.Bool("dry-run", false, "print v1 status counts, v2 placement counts, and unparsed entries without writing v2 rows")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "import-legacy [--dry-run] <v1-home>")
		return 2
	}
	v2 := ""
	if !*dry {
		dir, err := resolveHome(home)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		v2 = dir
	}
	report, err := importer.Import(context.Background(), v2, fs.Arg(0), *dry)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	printImportReport(stdout, report)
	return 0
}

func printImportReport(stdout io.Writer, report importer.Report) {
	var v1Keys, v2Keys []string
	for key := range report.Counts {
		switch {
		case strings.HasPrefix(key, "v1:"):
			v1Keys = append(v1Keys, key)
		case strings.HasPrefix(key, "v2:"):
			v2Keys = append(v2Keys, key)
		}
	}
	sort.Strings(v1Keys)
	sort.Strings(v2Keys)
	for _, key := range v1Keys {
		fmt.Fprintf(stdout, "v1 %s %d\n", strings.TrimPrefix(key, "v1:"), report.Counts[key])
	}
	for _, key := range v2Keys {
		fmt.Fprintf(stdout, "v2 %s %d\n", strings.TrimPrefix(key, "v2:"), report.Counts[key])
	}
	for _, line := range report.Unparsed {
		fmt.Fprintf(stdout, "unparsed: %s\n", line)
	}
	for _, line := range report.Notes {
		fmt.Fprintf(stdout, "note: %s\n", line)
	}
}
