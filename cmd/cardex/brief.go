package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// briefAttemptLimit is the folded attempt history on card show --brief.
// The default JSON card keeps every attempt: truncating that field would
// break existing consumers, so the limit applies only to --brief.
const briefAttemptLimit = 3

// briefTextLimit bounds free text on --brief. A merge note can be several
// kilobytes and a review opinion is stored up to 64 KiB; the summary keeps
// the leading runes of each field.
const briefTextLimit = 240

type cardBriefAttempt struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Channel   string `json:"channel"`
	Model     string `json:"model"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

type cardBriefEvent struct {
	ID   string          `json:"id"`
	TS   string          `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type cardBriefQuestion struct {
	ID   string `json:"id"`
	Line string `json:"line"`
}

type cardBriefView struct {
	ID          string             `json:"id"`
	Objective   string             `json:"objective"`
	Status      string             `json:"status"`
	WaitReason  string             `json:"wait_reason"`
	Tier        string             `json:"tier"`
	ReworkCount int                `json:"rework_count"`
	ClaimsCount int                `json:"claims_count"`
	Claims      *[]model.Claim     `json:"claims,omitempty"`
	Attempt     *cardBriefAttempt  `json:"attempt"`
	Attempts    []cardBriefAttempt `json:"attempts"`
	Event       *cardBriefEvent    `json:"event"`
	Question    *cardBriefQuestion `json:"question"`
	Commit      string             `json:"commit"`
}

type objectiveStatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type objectiveBlocked struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type objectiveBriefView struct {
	ID      string                 `json:"id"`
	Counts  []objectiveStatusCount `json:"counts"`
	Blocked []objectiveBlocked     `json:"blocked"`
}

func parseAttemptsMode(raw string) (all bool, err error) {
	switch strings.TrimSpace(raw) {
	case "":
		return false, nil
	case "all":
		return true, nil
	default:
		return false, fmt.Errorf("--attempts must be all")
	}
}

func buildCardBrief(c model.Card, attempts []model.Attempt, events []model.Event, questions []model.Question, showClaims, allAttempts bool) cardBriefView {
	sorted := append([]model.Attempt(nil), attempts...)
	sortAttemptsOldestFirst(sorted)
	history := make([]cardBriefAttempt, 0, len(sorted))
	for _, attempt := range sorted {
		history = append(history, briefAttempt(attempt))
	}
	view := cardBriefView{
		ID:          c.ID,
		Objective:   c.Objective,
		Status:      string(c.Status),
		WaitReason:  clipBriefText(c.WaitReason),
		Tier:        string(c.Tier),
		ReworkCount: c.ReworkCount,
		ClaimsCount: len(c.Claims),
		Attempt:     selectBriefAttempt(sorted),
		Attempts:    foldBriefAttempts(history, allAttempts),
		Event:       latestBriefEvent(events),
		Question:    pendingBriefQuestion(questions, c.ID),
		Commit:      latestCommit(c, sorted),
	}
	if showClaims {
		claims := append([]model.Claim(nil), c.Claims...)
		view.Claims = &claims
	}
	return view
}

func emitCardBrief(stdout io.Writer, asJSON bool, view cardBriefView) int {
	if asJSON {
		return writeJSON(stdout, view)
	}
	fmt.Fprint(stdout, formatCardBrief(view))
	return 0
}

func formatCardBrief(view cardBriefView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "id\t%s\n", view.ID)
	fmt.Fprintf(&b, "objective\t%s\n", view.Objective)
	fmt.Fprintf(&b, "status\t%s\n", view.Status)
	fmt.Fprintf(&b, "wait_reason\t%s\n", firstLine(view.WaitReason))
	fmt.Fprintf(&b, "tier\t%s\n", view.Tier)
	fmt.Fprintf(&b, "rework_count\t%d\n", view.ReworkCount)
	fmt.Fprintf(&b, "claims_count\t%d\n", view.ClaimsCount)
	writeBriefAttempt(&b, "attempt", view.Attempt)
	writeBriefEvent(&b, view.Event)
	if view.Question == nil {
		fmt.Fprintf(&b, "question\t\t\n")
	} else {
		fmt.Fprintf(&b, "question\t%s\t%s\n", view.Question.ID, view.Question.Line)
	}
	fmt.Fprintf(&b, "commit\t%s\n", view.Commit)
	for i := range view.Attempts {
		writeBriefAttempt(&b, "attempts", &view.Attempts[i])
	}
	if view.Claims != nil {
		for _, claim := range *view.Claims {
			fmt.Fprintf(&b, "claim\t%s\t%s\n", claim.Path, claim.Symbol)
		}
	}
	return b.String()
}

func writeBriefAttempt(b *strings.Builder, label string, attempt *cardBriefAttempt) {
	if attempt == nil {
		fmt.Fprintf(b, "%s\t\t\t\t\t\t\n", label)
		return
	}
	fmt.Fprintf(b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", label, attempt.ID, attempt.Status, attempt.Channel, attempt.Model, attempt.StartedAt, attempt.EndedAt)
}

func writeBriefEvent(b *strings.Builder, event *cardBriefEvent) {
	if event == nil {
		fmt.Fprintf(b, "event\t\t\n")
		return
	}
	fmt.Fprintf(b, "event\t%s\t%s\n", event.TS, event.Type)
}

func briefAttempt(a model.Attempt) cardBriefAttempt {
	return cardBriefAttempt{
		ID:        a.ID,
		Status:    attemptBriefStatus(a),
		Channel:   a.Route.Channel,
		Model:     a.Route.Model,
		StartedAt: formatCLITime(a.StartedAt),
		EndedAt:   formatCLITime(a.EndedAt),
	}
}

func attemptBriefStatus(a model.Attempt) string {
	if a.Outcome != "" {
		return string(a.Outcome)
	}
	if attemptOpen(a) {
		return "running"
	}
	return "ended"
}

func attemptOpen(a model.Attempt) bool {
	return a.Outcome == "" && a.Exit == nil && a.EndedAt.IsZero()
}

func selectBriefAttempt(sorted []model.Attempt) *cardBriefAttempt {
	if len(sorted) == 0 {
		return nil
	}
	chosen := sorted[len(sorted)-1]
	for _, attempt := range sorted {
		if attemptOpen(attempt) {
			chosen = attempt
		}
	}
	view := briefAttempt(chosen)
	return &view
}

func foldBriefAttempts(history []cardBriefAttempt, all bool) []cardBriefAttempt {
	if history == nil {
		history = []cardBriefAttempt{}
	}
	if all || len(history) <= briefAttemptLimit {
		return history
	}
	return history[len(history)-briefAttemptLimit:]
}

func sortAttemptsOldestFirst(attempts []model.Attempt) {
	for i := 1; i < len(attempts); i++ {
		item := attempts[i]
		j := i
		for j > 0 && attemptNewer(attempts[j-1], item) {
			attempts[j] = attempts[j-1]
			j--
		}
		attempts[j] = item
	}
}

func attemptNewer(a, b model.Attempt) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.After(b.StartedAt)
	}
	if a.Index != b.Index {
		return a.Index > b.Index
	}
	return a.ID > b.ID
}

func latestBriefEvent(events []model.Event) *cardBriefEvent {
	if len(events) == 0 {
		return nil
	}
	best := events[0]
	for _, event := range events[1:] {
		if eventNewer(event, best) {
			best = event
		}
	}
	return &cardBriefEvent{
		ID:   best.ID,
		TS:   formatCLITime(best.TS),
		Type: best.Type,
		Data: clipBriefData(best.Data),
	}
}

// eventNewer compares parsed timestamps. SQLite orders event.ts as
// RFC3339Nano text, and in that order "12:00:00Z" sorts after
// "12:00:00.5Z", so the last row is not the latest instant.
func eventNewer(a, b model.Event) bool {
	if !a.TS.Equal(b.TS) {
		return a.TS.After(b.TS)
	}
	return a.ID > b.ID
}

func clipBriefText(raw string) string {
	return clipRunes(firstLine(raw), briefTextLimit)
}

func clipRunes(s string, n int) string {
	if s == "" || n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// clipBriefData keeps a short payload as JSON. A longer payload becomes a
// JSON string of its leading runes so the brief stays valid and bounded.
func clipBriefData(data json.RawMessage) json.RawMessage {
	data = cardEventData(data)
	if len([]rune(string(data))) <= briefTextLimit {
		return data
	}
	encoded, err := json.Marshal(clipRunes(string(data), briefTextLimit))
	if err != nil {
		return json.RawMessage(`""`)
	}
	return encoded
}

func pendingBriefQuestion(questions []model.Question, cardID string) *cardBriefQuestion {
	for _, question := range questions {
		if question.Answer != "" || question.Scope != cardID {
			continue
		}
		if question.ScopeKind != "" && question.ScopeKind != "card" {
			continue
		}
		return &cardBriefQuestion{ID: question.ID, Line: clipBriefText(question.Text)}
	}
	return nil
}

func latestCommit(c model.Card, sorted []model.Attempt) string {
	for i := len(sorted) - 1; i >= 0; i-- {
		if sorted[i].HeadCommit != "" {
			return sorted[i].HeadCommit
		}
	}
	for i := len(sorted) - 1; i >= 0; i-- {
		if sorted[i].BaseCommit != "" {
			return sorted[i].BaseCommit
		}
	}
	return c.Base
}

func buildObjectiveBrief(id string, cards []model.Card) objectiveBriefView {
	counts := make(map[model.CardStatus]int, len(model.AllCardStatuses()))
	for _, card := range cards {
		counts[card.Status]++
	}
	view := objectiveBriefView{
		ID:      id,
		Counts:  make([]objectiveStatusCount, 0, len(model.AllCardStatuses())),
		Blocked: []objectiveBlocked{},
	}
	for _, status := range model.AllCardStatuses() {
		view.Counts = append(view.Counts, objectiveStatusCount{Status: string(status), Count: counts[status]})
	}
	blocked := make([]model.Card, 0)
	for _, card := range cards {
		if card.Status == model.CardNeedsInput || card.Status == model.CardFailed {
			blocked = append(blocked, card)
		}
	}
	sortCardsByID(blocked)
	for _, card := range blocked {
		view.Blocked = append(view.Blocked, objectiveBlocked{
			ID:     card.ID,
			Status: string(card.Status),
			Reason: blockedReason(card),
		})
	}
	return view
}

func emitObjectiveBrief(stdout io.Writer, asJSON bool, view objectiveBriefView) int {
	if asJSON {
		return writeJSON(stdout, view)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "id\t%s\n", view.ID)
	for _, count := range view.Counts {
		fmt.Fprintf(&b, "%s\t%d\n", count.Status, count.Count)
	}
	for _, card := range view.Blocked {
		fmt.Fprintf(&b, "blocked\t%s\t%s\t%s\n", card.ID, card.Status, card.Reason)
	}
	fmt.Fprint(stdout, b.String())
	return 0
}

func blockedReason(card model.Card) string {
	if line := clipBriefText(card.WaitReason); line != "" {
		return line
	}
	return clipBriefText(card.Legacy.Reason)
}

func sortCardsByID(cards []model.Card) {
	for i := 1; i < len(cards); i++ {
		item := cards[i]
		j := i
		for j > 0 && cards[j-1].ID > item.ID {
			cards[j] = cards[j-1]
			j--
		}
		cards[j] = item
	}
}

func firstLine(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\t", " "))
		if line != "" {
			return line
		}
	}
	return ""
}

func formatCLITime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseChangedSince(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("--changed-since requires an RFC3339 time")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("--changed-since must be RFC3339, got %q", raw)
}

// cardListFields are the scalar card columns --fields may select.
// Arrays such as claims stay on card show, so a field list cannot silently
// turn into a full claim dump.
func cardListFields() []string {
	return []string{
		"id", "objective", "node", "kind", "title", "body", "tier", "status",
		"wait_reason", "rework_count", "base", "channel", "model",
	}
}

func parseCardFields(raw []string) ([]string, error) {
	allowed := map[string]bool{}
	for _, name := range cardListFields() {
		allowed[name] = true
	}
	var out []string
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !allowed[part] {
				return nil, fmt.Errorf("unknown card field %q", part)
			}
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--fields requires a value")
	}
	return out, nil
}

func cardFieldValue(card model.Card, name string) (any, bool) {
	switch name {
	case "id":
		return card.ID, true
	case "objective":
		return card.Objective, true
	case "node":
		return card.Node, true
	case "kind":
		return string(card.Kind), true
	case "title":
		return card.Title, true
	case "body":
		return card.Body, true
	case "tier":
		return string(card.Tier), true
	case "status":
		return string(card.Status), true
	case "wait_reason":
		return card.WaitReason, true
	case "rework_count":
		return card.ReworkCount, true
	case "base":
		return card.Base, true
	case "channel":
		return card.Channel, true
	case "model":
		return card.Model, true
	default:
		return nil, false
	}
}

func projectCards(cards []model.Card, fields []string) []map[string]any {
	out := make([]map[string]any, 0, len(cards))
	for _, card := range cards {
		row := make(map[string]any, len(fields))
		for _, field := range fields {
			value, ok := cardFieldValue(card, field)
			if ok {
				row[field] = value
			}
		}
		out = append(out, row)
	}
	return out
}

func formatCardFields(cards []model.Card, fields []string) string {
	var b strings.Builder
	for _, card := range cards {
		for i, field := range fields {
			if i > 0 {
				b.WriteByte('\t')
			}
			value, _ := cardFieldValue(card, field)
			fmt.Fprint(&b, value)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// changeIndex is the time evidence card list --changed-since can see.
// Cards have no status-updated column. A card matches when an attempt
// started or ended after the cutoff, or when an event on that card or one
// of its attempts did. card add writes card_created when the card has no
// origin event, and a status verb writes card_status when the status
// changes, so those writes are events this index can see.
type changeIndex struct {
	attempts      map[string][]model.Attempt
	cardEvents    map[string][]time.Time
	attemptEvents map[string][]time.Time
}

func newChangeIndex(attempts []model.Attempt, events []model.Event) changeIndex {
	index := changeIndex{
		attempts:      map[string][]model.Attempt{},
		cardEvents:    map[string][]time.Time{},
		attemptEvents: map[string][]time.Time{},
	}
	for _, attempt := range attempts {
		index.attempts[attempt.Card] = append(index.attempts[attempt.Card], attempt)
	}
	for _, event := range events {
		if cardID, ok := strings.CutPrefix(event.Entity, "card:"); ok {
			index.cardEvents[cardID] = append(index.cardEvents[cardID], event.TS)
			continue
		}
		if attemptID, ok := strings.CutPrefix(event.Entity, "attempt:"); ok {
			index.attemptEvents[attemptID] = append(index.attemptEvents[attemptID], event.TS)
		}
	}
	return index
}

func (index changeIndex) changed(cardID string, since time.Time) bool {
	for _, attempt := range index.attempts[cardID] {
		if attempt.StartedAt.After(since) || (!attempt.EndedAt.IsZero() && attempt.EndedAt.After(since)) {
			return true
		}
		for _, ts := range index.attemptEvents[attempt.ID] {
			if ts.After(since) {
				return true
			}
		}
	}
	for _, ts := range index.cardEvents[cardID] {
		if ts.After(since) {
			return true
		}
	}
	return false
}

func filterCardsChangedSince(cards []model.Card, index changeIndex, since time.Time) []model.Card {
	out := []model.Card{}
	for _, card := range cards {
		if index.changed(card.ID, since) {
			out = append(out, card)
		}
	}
	return out
}
