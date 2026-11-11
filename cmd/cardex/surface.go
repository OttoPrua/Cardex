package main

import (
	"context"
	"encoding/json"
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
		fmt.Fprintf(stdout, "%s\t%s\n", n.ID, n.Status)
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
	return emit(stdout, asJSON, n, n.ID+"\t"+string(n.Status))
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
	return emit(stdout, asJSON, n, n.ID+"\t"+string(n.Status))
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
  answer  <id> <option>
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
	fs := newFlagSet("question answer", "save one stored option through question.Service", "<id> <option> --json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "question answer <id> <option>")
		return 2
	}
	option := ""
	if fs.NArg() >= 2 {
		option = fs.Arg(1)
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	svc := question.New(db, nil)
	q, err := svc.Answer(context.Background(), fs.Arg(0), option)
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
  check
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
		return channelCheck(home, args[1:], stdout, stderr)
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
	fs := newFlagSet("channel set", "set redline, quota windows, or concurrency on a channel", "--json", stdout, stderr)
	id := fs.String("id", "", "channel id")
	runner := fs.String("runner", "", "runner name")
	redline := &softNumber{name: "redline", kind: numberFloat}
	concurrency := &softNumber{name: "concurrency", kind: numberInt}
	fs.Var(redline, "redline", "redline from 0 to 1 inclusive")
	fs.Var(concurrency, "concurrency", "concurrency limit")
	health := fs.String("health", "", "ok, auth_failed, or degraded")
	var windows, models stringsFlag
	fs.Var(&windows, "window", "name:rule or name:rule:token-limit")
	fs.Var(&models, "model", "model name")
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
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	ch, err := db.GetChannel(ctx, *id)
	missing := err != nil
	var notes []string
	if missing {
		raw := strings.TrimSpace(*runner)
		val := model.RunnerName(raw)
		if raw == "" || val == model.RunnerFake {
			fmt.Fprintln(stderr, "--runner is required when creating a channel")
			return 2
		}
		if !val.Valid() {
			notes = append(notes, "runner "+*runner)
			val = model.RunnerGrok
		}
		ch = model.Channel{ID: *id, Health: model.HealthOK, Runner: val}
	} else if flagSeen(fs, "runner") {
		val := model.RunnerName(strings.TrimSpace(*runner))
		if val.Valid() && val != "" {
			ch.Runner = val
		} else {
			notes = append(notes, "runner "+*runner)
		}
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
	if missing {
		if err := db.CreateChannel(ctx, &ch); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else if err := db.UpdateChannel(ctx, &ch); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
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

func channelCheck(home string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("channel check", "report channel tier, redline, and window problems with suggestions", "no command arguments", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
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
	for _, item := range channelFindings(db, resolved) {
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
	if len(attempts) > 0 {
		best := attempts[0]
		for _, attempt := range attempts[1:] {
			if attempt.Index >= best.Index {
				best = attempt
			}
		}
		reason = best.RouteReason
	}
	if !asJSON {
		fmt.Fprintf(stdout, "card %s\n", cardID)
	}
	return writeJSON(stdout, reason)
}

const positioningGroupUsage = `positioning — show and approve the model positioning table
purpose: show, list proposals, approve, or reject. approve and reject call positioning.Store

parameters:
  show        --json
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

func openPositioning(home string) (*positioning.Home, error) {
	dir, err := resolveHome(home)
	if err != nil {
		return nil, err
	}
	return positioning.Open(dir)
}

func positioningShow(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("positioning show", "show the current positioning table from positioning.Store", "--json", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	h, err := openPositioning(home)
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
	return 0
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
		fmt.Fprintf(stdout, "%s\t%s\n", item.ID, item.Status)
	}
	return 0
}

type storedProposal struct {
	ID      string              `json:"id"`
	Entries []model.Positioning `json:"entries"`
	Diff    string              `json:"diff"`
	Status  string              `json:"status"`
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
	h, err := openPositioning(home)
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
	Schedules []scheduleEntry `json:"schedules"`
}

type scheduleEntry struct {
	Name     string       `json:"name"`
	Every    string       `json:"every"`
	Cron     string       `json:"cron"`
	CronLite string       `json:"cron-lite"`
	Template cardTemplate `json:"card_template"`
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
	db, err := openStore(home)
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
