package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// briefAttemptLimit is the folded attempt history on card show --brief.
// The default JSON card keeps every attempt: truncating that field would
// break existing consumers, so the limit applies only to --brief.
const briefAttemptLimit = 3

// briefTextLimit bounds free text on --brief. A merge note can be several
// kilobytes and a review opinion is stored up to 64 KiB; the summary keeps
// the leading bytes of each field without splitting a UTF-8 rune.
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

	DependencyBlocker string `json:"dependency_blocker,omitempty"`
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

func buildCardBrief(c model.Card, attempts []model.Attempt, events []model.Event, question *cardBriefQuestion, showClaims, allAttempts bool) cardBriefView {
	sorted := append([]model.Attempt(nil), attempts...)
	sort.Slice(sorted, func(i, j int) bool { return attemptNewer(sorted[j], sorted[i]) })
	recent := sorted
	if !allAttempts && len(recent) > briefAttemptLimit {
		recent = recent[len(recent)-briefAttemptLimit:]
	}
	history := make([]cardBriefAttempt, 0, len(recent))
	for _, attempt := range recent {
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
		Attempts:    history,
		Event:       latestBriefEvent(events),
		Question:    question,
		Commit:      latestCommit(c, sorted),
	}
	if showClaims {
		view.Claims = &c.Claims
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
	fmt.Fprintf(&b, "wait_reason\t%s\n", view.WaitReason)
	if view.DependencyBlocker != "" {
		fmt.Fprintf(&b, "dependency_blocker\t%s\n", view.DependencyBlocker)
	}
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
	if end := strings.IndexAny(raw, "\r\n"); end >= 0 {
		raw = raw[:end]
	}
	raw = strings.TrimSpace(raw)
	if len(raw) > briefTextLimit {
		limit := briefTextLimit
		for limit > 0 && !utf8.RuneStart(raw[limit]) {
			limit--
		}
		raw = raw[:limit]
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(raw, " "))
}

// clipBriefData keeps a short payload as JSON. A longer payload becomes a
// JSON string preview. Count formatted bytes because writeJSON indents objects.
func clipBriefData(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return json.RawMessage(`{}`)
	}
	if len(data) <= briefTextLimit {
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, data, "    ", "  "); err == nil && formatted.Len() <= briefTextLimit {
			return data
		}
	}
	encoded, _ := json.Marshal(clipBriefText(string(data)))
	return encoded
}

func pendingBriefQuestion(ctx context.Context, db *store.Store, cardID string) (*cardBriefQuestion, error) {
	question, err := db.FindPendingCardQuestion(ctx, cardID)
	if err != nil || question == nil {
		return nil, err
	}
	return &cardBriefQuestion{ID: question.ID, Line: clipBriefText(question.Text)}, nil
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
	// A card may name a branch as its base before the first attempt.
	if len(c.Base) == 40 || len(c.Base) == 64 {
		if _, err := hex.DecodeString(c.Base); err == nil {
			return c.Base
		}
	}
	return ""
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

func objectiveFallbackReason(ctx context.Context, db *store.Store, card objectiveBlocked, failedNormalizers []string) (string, error) {
	question, err := pendingBriefQuestion(ctx, db, card.ID)
	if err != nil {
		return "", err
	}
	if question != nil && question.Line != "" {
		return question.Line, nil
	}
	return briefAttemptFallbackReason(ctx, db, card, failedNormalizers)
}

func briefAttemptFallbackReason(ctx context.Context, db *store.Store, card objectiveBlocked, failedNormalizers []string) (string, error) {
	latest, err := latestStoredAttempt(ctx, db, card.ID)
	if err != nil {
		return "", err
	}
	if card.Status == string(model.CardFailed) {
		if reason := clipBriefText(latest.ErrorExcerpt); reason != "" {
			return reason, nil
		}
		return string(latest.Outcome), nil
	}
	if latest.ID == "" {
		return "", nil
	}
	events, err := db.ListEvents(ctx, "card:"+card.ID)
	if err != nil {
		return "", err
	}
	since := attemptResultTime(latest)
	var parseFailure model.Event
	for _, event := range events {
		switch event.Type {
		case "plan_unparsable", "review_unparsable":
			var source struct {
				Attempt string `json:"attempt"`
			}
			if err := json.Unmarshal(event.Data, &source); err != nil {
				continue
			}
			// Older parse events lack an attempt ID.
			matches := source.Attempt == latest.ID ||
				(source.Attempt == "" && !since.IsZero() && !event.TS.Before(since))
			if matches && (parseFailure.ID == "" || eventNewer(event, parseFailure)) {
				parseFailure = event
			}
		}
	}
	if parseFailure.ID != "" {
		return parseFailure.Type, nil
	}
	if len(failedNormalizers) > 0 {
		reason, err := normalizeFallbackReason(ctx, db, latest.ID, failedNormalizers)
		if err != nil || reason != "" {
			return reason, err
		}
	}
	if latest.Outcome == model.OutcomeResultUnparsable {
		return string(latest.Outcome), nil
	}
	return "", nil
}

func normalizeFallbackReason(ctx context.Context, db *store.Store, attemptID string, failedNormalizers []string) (string, error) {
	// The plan records which result the normalizer belongs to. Retrying a
	// normalizer changes its attempt times without changing that association.
	events, err := db.ListEvents(ctx, "attempt:"+attemptID)
	if err != nil {
		return "", err
	}
	for _, event := range events {
		if event.Type != loop.NormalizePlannedEvent {
			continue
		}
		var plan struct {
			Normalize string `json:"normalize"`
		}
		if err := json.Unmarshal(event.Data, &plan); err != nil || !slices.Contains(failedNormalizers, plan.Normalize) {
			continue
		}
		failure, err := latestStoredAttempt(ctx, db, plan.Normalize)
		if err != nil {
			return "", err
		}
		if failure.Outcome == "" || failure.Outcome == model.OutcomePassed {
			continue
		}
		reason := "normalize " + failure.Card + ": " + string(failure.Outcome)
		if excerpt := clipBriefText(failure.ErrorExcerpt); excerpt != "" {
			reason += " (" + excerpt + ")"
		}
		return clipBriefText(reason), nil
	}
	return "", nil
}

func latestStoredAttempt(ctx context.Context, db *store.Store, cardID string) (model.Attempt, error) {
	attempts, err := db.ListAttempts(ctx, cardID)
	var latest model.Attempt
	for _, attempt := range attempts {
		if latest.ID == "" || attemptNewer(attempt, latest) {
			latest = attempt
		}
	}
	return latest, err
}

func attemptResultTime(attempt model.Attempt) time.Time {
	if !attempt.EndedAt.IsZero() {
		return attempt.EndedAt
	}
	return attempt.StartedAt
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
