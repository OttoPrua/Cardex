// Outcome decisions and loose card-list normalization. Design §5 and §6.4.
package loop

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Action is how one harvested attempt is disposed.
type Action string

const (
	ActionNone       Action = ""
	ActionReview     Action = "review"
	ActionRework     Action = "rework"
	ActionQuestion   Action = "question"
	ActionReschedule Action = "reschedule"
	ActionRetrySame  Action = "retry_same"
	ActionNormalize  Action = "normalize"
	ActionQuotaMove  Action = "quota_move"
	ActionAuthMove   Action = "auth_move"
	ActionContinue   Action = "continue"
	ActionVoid       Action = "void"
	ActionAbsorb     Action = "absorb"
)

// History counts earlier attempts on the same card.
type History struct {
	NoOutput           int
	SameChannelRetries int
	BudgetLimited      int
}

// Decision is the pure disposition for one attempt. Applying it is DevStep's job.
type Decision struct {
	Action       Action
	SetRework    bool
	ReworkCount  int
	QuestionKind model.QuestionKind
	QuestionText string
	Notify       bool
	BlockQuota   bool
	SameChannel  bool
	OtherChannel bool
	UseBranch    bool
	NeedsInput   bool
}

// Decide applies the design §5 row for outcome.
// reworkLimit is the maximum rework_count that still reworks; a failure that would
// push the count past it opens a question. retryLimit is how many transport or
// timeout failures stay on the same channel before a switch. budgetLeft is false
// when objective spend has reached a configured hard_tokens line.
func Decide(card model.Card, outcome model.Outcome, excerpt string, reworkLimit, retryLimit int, budgetLeft bool, hist History) Decision {
	if reworkLimit <= 0 {
		reworkLimit = 2
	}
	if retryLimit <= 0 {
		retryLimit = 2
	}
	switch outcome {
	case model.OutcomePassed:
		switch card.Kind {
		case model.KindDev, model.KindSkeleton, model.KindRepair, model.KindRebase, model.KindBacklog:
			return Decision{Action: ActionReview}
		case model.KindPlan, model.KindReview, model.KindNormalize, model.KindReplan:
			return Decision{Action: ActionAbsorb}
		default:
			return Decision{}
		}
	case model.OutcomeTestsFailed:
		return reworkOrQuestion(card, reworkLimit, "tests failed")
	case model.OutcomeNoOutput:
		if hist.NoOutput >= 1 {
			return Decision{
				Action:       ActionQuestion,
				QuestionKind: model.QuestionCustom,
				QuestionText: "no output after switching channel",
				NeedsInput:   true,
			}
		}
		return Decision{Action: ActionReschedule, OtherChannel: true}
	case model.OutcomeResultUnparsable:
		if card.Kind == model.KindNormalize {
			return Decision{NeedsInput: true}
		}
		return Decision{Action: ActionNormalize}
	case model.OutcomeQuotaHit:
		return Decision{Action: ActionQuotaMove, BlockQuota: true, OtherChannel: true, UseBranch: true}
	case model.OutcomeAuthFailed:
		return Decision{Action: ActionAuthMove, Notify: true, OtherChannel: true, UseBranch: true}
	case model.OutcomeTransport, model.OutcomeTimeout:
		if hist.SameChannelRetries < retryLimit {
			return Decision{Action: ActionRetrySame, SameChannel: true, UseBranch: true}
		}
		return Decision{Action: ActionReschedule, OtherChannel: true, UseBranch: true}
	case model.OutcomeRefusal:
		return Decision{
			Action:       ActionQuestion,
			QuestionKind: model.QuestionRefusal,
			QuestionText: "model refused",
			NeedsInput:   true,
		}
	case model.OutcomePermissionDenied:
		return Decision{
			Action:       ActionQuestion,
			QuestionKind: model.QuestionCustom,
			QuestionText: "permission denied: " + strings.TrimSpace(excerpt),
			NeedsInput:   true,
		}
	case model.OutcomeBudgetLimited:
		if hist.BudgetLimited == 0 && budgetLeft {
			return Decision{Action: ActionContinue, SameChannel: true, UseBranch: true}
		}
		text := "card budget exhausted and the objective budget has no remaining capacity"
		if budgetLeft {
			text = "card budget exhausted after one continuation"
		}
		return Decision{
			Action:       ActionQuestion,
			QuestionKind: model.QuestionBudget,
			QuestionText: text,
			NeedsInput:   true,
		}
	case model.OutcomeViolation:
		return Decision{
			Action:       ActionVoid,
			QuestionKind: model.QuestionCustom,
			QuestionText: "attempt voided after a hard-line violation",
			NeedsInput:   true,
		}
	default:
		return Decision{}
	}
}

func reworkOrQuestion(card model.Card, limit int, why string) Decision {
	if card.ReworkCount+1 > limit {
		return Decision{
			Action:       ActionQuestion,
			QuestionKind: model.QuestionCustom,
			QuestionText: fmt.Sprintf("rework_count would exceed %d (%s)", limit, why),
			NeedsInput:   true,
		}
	}
	return Decision{
		Action:      ActionRework,
		SetRework:   true,
		ReworkCount: card.ReworkCount + 1,
		SameChannel: true,
		UseBranch:   true,
	}
}

// ReviewOf splits a review result into pass or rework plus the remaining opinion.
func ReviewOf(raw string) (verdict, opinion string, ok bool) {
	body, found := sectionBody(raw, "复核结论")
	if !found {
		body = raw
	}
	fields := strings.Fields(body)
	for i, field := range fields {
		field = strings.Trim(strings.ToLower(field), "`*_.,;:()[]\"'")
		if field != "pass" && field != "rework" {
			continue
		}
		return field, strings.TrimSpace(strings.Join(fields[i+1:], " ")), true
	}
	return "", "", false
}

// ListedCard is one loosely normalized card from a plan or replan result.
type ListedCard struct {
	Key        string
	Kind       model.CardKind
	Title      string
	Body       string
	Tier       model.Tier
	Claims     []model.Claim
	Deps       []string
	Acceptance []string
	Node       string
	Op         string
	Outside    bool
	ID         string
}

// ListEvent is a fact recorded while normalizing a card list.
type ListEvent struct {
	Type string
	Card string
	Dep  string
	From string
	To   string
}

// ListQuestion is an open design question hung on a node.
type ListQuestion struct {
	Node string
	Text string
}

// CardList is the normalized form of a plan or replan result.
type CardList struct {
	Cards     []ListedCard
	Events    []ListEvent
	Questions []ListQuestion
	Broken    [][2]string
	Bad       bool
}

type rawCard struct {
	Key        string          `json:"key"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Tier       string          `json:"tier"`
	Claims     json.RawMessage `json:"claims"`
	Deps       []string        `json:"deps"`
	Acceptance []string        `json:"acceptance"`
	Node       string          `json:"node"`
	Op         string          `json:"op"`
}

// ParseCardList loosely reads the JSON card list in a result file.
// known is the set of card ids that already exist and may be named as deps.
// Each remaining cycle loses one edge. Cards whose node is set and different
// from scope stay out of Cards as work and become a design question on that node.
func ParseCardList(raw, scope string, known map[string]bool) CardList {
	body, found := sectionBody(raw, "新卡")
	if !found {
		body = raw
	}
	raws, err := decodeCards(body)
	if err != nil {
		return CardList{Bad: true, Events: []ListEvent{{Type: "plan_unparsable"}}}
	}
	return normalizeList(raws, scope, known)
}

func normalizeList(raws []rawCard, scope string, known map[string]bool) CardList {
	var out CardList
	drafts := make([]ListedCard, 0, len(raws))
	for i, raw := range raws {
		key := strings.TrimSpace(raw.Key)
		if key == "" {
			key = fmt.Sprintf("card-%d", i+1)
		}
		kind := canonKind(raw.Kind)
		title := strings.TrimSpace(raw.Title)
		if title == "" {
			title = key
		}
		tier := model.Tier(strings.TrimSpace(raw.Tier))
		if tier == "" || tier == model.TierNone || !tierListed(tier) {
			tier = model.DefaultTier(kind)
		}
		node := strings.TrimSpace(raw.Node)
		drafts = append(drafts, ListedCard{
			Key:        key,
			Kind:       kind,
			Title:      title,
			Body:       raw.Body,
			Tier:       tier,
			Claims:     decodeClaims(raw.Claims),
			Deps:       append([]string{}, raw.Deps...),
			Acceptance: append([]string{}, raw.Acceptance...),
			Node:       node,
			Op:         strings.ToLower(strings.TrimSpace(raw.Op)),
			Outside:    node != "" && node != scope,
		})
	}
	valid := map[string]bool{}
	for id := range known {
		valid[id] = true
	}
	for _, d := range drafts {
		if !d.Outside && d.Op != "delete" {
			valid[d.Key] = true
		}
	}
	for i := range drafts {
		if drafts[i].Outside {
			node := drafts[i].Node
			if node == "" {
				node = scope
			}
			out.Questions = append(out.Questions, ListQuestion{
				Node: node,
				Text: "设计问题: " + drafts[i].Title + " (" + drafts[i].Key + ")",
			})
			continue
		}
		keep := make([]string, 0, len(drafts[i].Deps))
		seen := map[string]bool{}
		for _, dep := range drafts[i].Deps {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if !valid[dep] || dep == drafts[i].Key || seen[dep] {
				out.Events = append(out.Events, ListEvent{Type: "invalid_dep", Card: drafts[i].Key, Dep: dep})
				continue
			}
			seen[dep] = true
			keep = append(keep, dep)
		}
		drafts[i].Deps = keep
	}
	ids := make([]string, 0, len(drafts))
	deps := map[string][]string{}
	for _, d := range drafts {
		if d.Outside || d.Op == "delete" {
			continue
		}
		ids = append(ids, d.Key)
		deps[d.Key] = append([]string{}, d.Deps...)
	}
	sort.Strings(ids)
	for k := range deps {
		sort.Strings(deps[k])
	}
	broken := map[[2]string]bool{}
	for {
		from, to, ok := breakOne(ids, deps)
		if !ok {
			break
		}
		edge := [2]string{from, to}
		if broken[edge] {
			break
		}
		broken[edge] = true
		nextDeps := make([]string, 0, len(deps[from]))
		for _, dep := range deps[from] {
			if dep == to {
				continue
			}
			nextDeps = append(nextDeps, dep)
		}
		deps[from] = nextDeps
		for i := range drafts {
			if drafts[i].Key != from {
				continue
			}
			next := make([]string, 0, len(drafts[i].Deps))
			for _, dep := range drafts[i].Deps {
				if dep == to {
					continue
				}
				next = append(next, dep)
			}
			drafts[i].Deps = next
		}
		out.Broken = append(out.Broken, edge)
		out.Questions = append(out.Questions, ListQuestion{
			Node: scope,
			Text: "依赖成环，已断开 " + from + " -> " + to,
		})
		out.Events = append(out.Events, ListEvent{Type: "cycle_break", From: from, To: to})
	}
	for _, d := range drafts {
		if d.Outside {
			continue
		}
		out.Cards = append(out.Cards, d)
	}
	return out
}

func breakOne(ids []string, deps map[string][]string) (from, to string, ok bool) {
	color := map[string]int{}
	var walk func(string) bool
	walk = func(n string) bool {
		color[n] = 1
		for _, m := range deps[n] {
			if _, in := deps[m]; !in && !containsStr(ids, m) {
				continue
			}
			if color[m] == 1 {
				from, to = n, m
				return true
			}
			if color[m] == 0 && walk(m) {
				return true
			}
		}
		color[n] = 2
		return false
	}
	for _, id := range ids {
		if color[id] == 0 && walk(id) {
			return from, to, true
		}
	}
	return "", "", false
}

func containsStr(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// ReplanChange is the subset of a replan list that may touch not-yet-started cards.
type ReplanChange struct {
	Create    []ListedCard
	Edit      []ListedCard
	Delete    []string
	Questions []ListQuestion
}

// PlanReplan keeps adds, edits, and deletes inside scope and not-yet-started cards.
// started is the set of card ids that already have an attempt.
func PlanReplan(existing []model.Card, started map[string]bool, scope string, list CardList) ReplanChange {
	byID := map[string]model.Card{}
	for _, card := range existing {
		byID[card.ID] = card
	}
	var ch ReplanChange
	ch.Questions = append(ch.Questions, list.Questions...)
	for _, d := range list.Cards {
		if d.Outside {
			continue
		}
		cur, ok := byID[d.Key]
		if d.Op == "delete" {
			if ok && cur.Node == scope && notYetStarted(cur, started) {
				ch.Delete = append(ch.Delete, d.Key)
			}
			continue
		}
		if !ok {
			d.Node = scope
			ch.Create = append(ch.Create, d)
			continue
		}
		if cur.Node != scope || !notYetStarted(cur, started) {
			continue
		}
		ch.Edit = append(ch.Edit, d)
	}
	return ch
}

// bindCreatedCards gives every new card an id that is not in taken and rewrites
// dependencies that name a key in this create list to that card's id.
// The first create with a repeated key keeps the name; later copies still get their own ids.
func bindCreatedCards(creates, edits []ListedCard, taken map[string]bool) ([]ListedCard, []ListedCard, error) {
	if taken == nil {
		taken = map[string]bool{}
	}
	keyToID := map[string]string{}
	for i := range creates {
		id, err := mintCardID(taken)
		if err != nil {
			return nil, nil, err
		}
		taken[id] = true
		creates[i].ID = id
		if _, ok := keyToID[creates[i].Key]; !ok {
			keyToID[creates[i].Key] = id
		}
	}
	for i := range creates {
		creates[i].Deps = remapListDeps(creates[i].Deps, keyToID)
	}
	for i := range edits {
		edits[i].Deps = remapListDeps(edits[i].Deps, keyToID)
	}
	return creates, edits, nil
}

func remapListDeps(deps []string, keyToID map[string]string) []string {
	if len(deps) == 0 {
		return deps
	}
	out := make([]string, len(deps))
	for i, dep := range deps {
		if id, ok := keyToID[dep]; ok {
			out[i] = id
			continue
		}
		out[i] = dep
	}
	return out
}

func mintCardID(taken map[string]bool) (string, error) {
	for n := 0; n < 8; n++ {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		id := "c" + hex.EncodeToString(buf[:])
		if !taken[id] {
			return id, nil
		}
	}
	return "", fmt.Errorf("card id collision")
}

func notYetStarted(card model.Card, started map[string]bool) bool {
	if started[card.ID] {
		return false
	}
	switch card.Status {
	case model.CardReady, model.CardWaiting, model.CardPaused:
		return true
	default:
		return false
	}
}

func canonKind(s string) model.CardKind {
	k := model.CardKind(strings.TrimSpace(s))
	for _, known := range model.AllCardKinds() {
		if k == known {
			return k
		}
	}
	return model.KindDev
}

func tierListed(t model.Tier) bool {
	for _, known := range model.AllTiers() {
		if t == known && t != model.TierNone {
			return true
		}
	}
	return false
}

func decodeCards(s string) ([]rawCard, error) {
	s = strings.TrimSpace(stripFence(s))
	s = strings.TrimSpace(stripComments(stripCommas(s)))
	if s == "" {
		return nil, fmt.Errorf("empty json")
	}
	var arr []rawCard
	if err := json.Unmarshal([]byte(s), &arr); err == nil {
		return arr, nil
	}
	var wrap struct {
		Cards []rawCard `json:"cards"`
	}
	if err := json.Unmarshal([]byte(s), &wrap); err == nil && wrap.Cards != nil {
		return wrap.Cards, nil
	}
	return nil, fmt.Errorf("unparsable json")
}

func decodeClaims(raw json.RawMessage) []model.Claim {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]model.Claim, 0, len(items))
	for _, item := range items {
		item = bytes.TrimSpace(item)
		if len(item) == 0 {
			continue
		}
		if item[0] == '"' {
			var path string
			if json.Unmarshal(item, &path) == nil && path != "" {
				out = append(out, model.Claim{Path: path})
			}
			continue
		}
		var claim struct {
			Path   string `json:"path"`
			Symbol string `json:"symbol"`
		}
		if json.Unmarshal(item, &claim) == nil && claim.Path != "" {
			out = append(out, model.Claim{Path: claim.Path, Symbol: claim.Symbol})
		}
	}
	return out
}

func sectionBody(raw, name string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "#") {
			continue
		}
		title := strings.TrimLeft(trim, "#")
		if normHeading(title) == normHeading(name) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		trim := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trim, "#") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
}

func normHeading(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	fence := ""
	switch {
	case strings.HasPrefix(s, "```"):
		fence = "```"
	case strings.HasPrefix(s, "~~~"):
		fence = "~~~"
	default:
		return s
	}
	rest := s[len(fence):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	rest = strings.TrimRight(rest, " \t\r\n")
	if i := strings.LastIndex(rest, fence); i >= 0 && strings.TrimSpace(rest[i:]) == fence {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

func stripComments(s string) string {
	var b strings.Builder
	in := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in {
			b.WriteByte(c)
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				in = false
			}
			continue
		}
		if c == '"' {
			in = true
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			i += 2
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func stripCommas(s string) string {
	var b strings.Builder
	in := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in {
			b.WriteByte(c)
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				in = false
			}
			continue
		}
		if c == '"' {
			in = true
			b.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\t' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}
