package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cardex/internal/model"
	"cardex/internal/store"
)

const historyPageSize = 50

type pageData struct {
	Title    string
	Current  *currentView
	Channels *channelsView
	Card     *cardView
	History  *historyView
}

type currentView struct {
	Objectives       []objectiveView  `json:"objectives"`
	Cards            []cardBrief      `json:"cards"`
	StatusCounts     []statusCount    `json:"status_counts"`
	Waiting          []waitingCard    `json:"waiting"`
	RunningAttempts  []runningAttempt `json:"running_attempts"`
	MergeQueue       []cardBrief      `json:"merge_queue"`
	PendingQuestions []questionBrief  `json:"pending_questions"`
}

type objectiveView struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status string     `json:"status"`
	Nodes  []nodeView `json:"nodes"`
}

type nodeView struct {
	ID                string     `json:"id"`
	Parent            string     `json:"parent"`
	Layer             string     `json:"layer"`
	Status            string     `json:"status"`
	OpenQuestionCount int        `json:"open_question_count"`
	Children          []nodeView `json:"children"`
}

type statusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type waitingCard struct {
	ID         string `json:"id"`
	WaitReason string `json:"wait_reason"`
}

type runningAttempt struct {
	ID      string      `json:"id"`
	Card    string      `json:"card"`
	Channel string      `json:"channel"`
	Claims  []claimView `json:"claims"`
}

type claimView struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
}

type cardBrief struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
}

type questionBrief struct {
	ID        string `json:"id"`
	Objective string `json:"objective"`
	Text      string `json:"text"`
}

type channelsView struct {
	Channels []channelView `json:"channels"`
}

type channelView struct {
	ID          string       `json:"id"`
	Health      string       `json:"health"`
	Redline     float64      `json:"redline"`
	Concurrency int          `json:"concurrency"`
	Occupied    int          `json:"occupied"`
	Windows     []windowView `json:"windows"`
}

type windowView struct {
	Name       string   `json:"name"`
	Remaining  *float64 `json:"remaining"`
	Confidence float64  `json:"confidence"`
	ResetAt    string   `json:"reset_at"`
}

type cardView struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Status   string         `json:"status"`
	Attempts []attemptView  `json:"attempts"`
	History  []historyEvent `json:"history"`
}

type historyEvent struct {
	TS   string          `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type eventView struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type attemptView struct {
	ID           string            `json:"id"`
	RouteReason  model.RouteReason `json:"route_reason"`
	Outcome      string            `json:"outcome"`
	ErrorExcerpt string            `json:"error_excerpt"`
	Exit         *int              `json:"exit"`
	BaseCommit   string            `json:"base_commit"`
	HeadCommit   string            `json:"head_commit"`
	Events       []eventView       `json:"events"`
	LogTailURL   string            `json:"log_tail_url"`
	Ended        bool              `json:"ended"`
	Evidence     []string          `json:"evidence"`
}

// RouteReasonJSON is the route reason as HTML-escaped text in the card template.
func (a attemptView) RouteReasonJSON() string {
	b, err := json.Marshal(a.RouteReason)
	if err != nil {
		return ""
	}
	return string(b)
}

type historyView struct {
	Query    string        `json:"query"`
	Status   string        `json:"status"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int           `json:"total"`
	Items    []historyItem `json:"items"`
}

type historyItem struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	LegacyStatus string `json:"legacy_status,omitempty"`
	LegacySource string `json:"legacy_source,omitempty"`
	LegacyReason string `json:"legacy_reason,omitempty"`
}

// Href links a history card to its detail and an objective back to a history search.
func (h historyItem) Href() string {
	if h.Kind == "card" {
		return "/cards/" + url.PathEscape(h.ID)
	}
	return "/history?q=" + url.QueryEscape(h.ID)
}

// loadCurrent lists active objectives and their current cards. Cards of other
// objectives, including a large done/canceled history, are never loaded into
// this response. Unanswered questions are selected separately and stay when
// the objective is active, paused, or done_candidate.
func loadCurrent(ctx context.Context, db *store.Store) (*currentView, error) {
	objectives, err := db.ListObjectives(ctx, "")
	if err != nil {
		return nil, err
	}
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return nil, err
	}
	questions, err := db.ListQuestions(ctx, "")
	if err != nil {
		return nil, err
	}
	view := &currentView{
		Objectives:       []objectiveView{},
		Cards:            []cardBrief{},
		Waiting:          []waitingCard{},
		RunningAttempts:  []runningAttempt{},
		MergeQueue:       []cardBrief{},
		PendingQuestions: []questionBrief{},
	}
	counts := map[model.CardStatus]int{}
	objStatus := map[string]model.ObjectiveStatus{}
	cardsByID := map[string]model.Card{}
	for _, o := range objectives {
		objStatus[o.ID] = o.Status
		if o.Status != model.ObjectiveActive {
			continue
		}
		nodes, err := db.ListNodes(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		flat := make([]nodeView, 0, len(nodes))
		for _, n := range nodes {
			flat = append(flat, nodeView{
				ID:                n.ID,
				Parent:            n.Parent,
				Layer:             string(n.Layer),
				Status:            string(n.Status),
				OpenQuestionCount: openQuestionCount(n),
			})
		}
		ov := objectiveView{ID: o.ID, Title: o.Title, Status: string(o.Status), Nodes: nestNodes(flat)}
		view.Objectives = append(view.Objectives, ov)
		cards, err := db.ListCards(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range cards {
			if !currentCard(c) {
				continue
			}
			cardsByID[c.ID] = c
			counts[c.Status]++
			view.Cards = append(view.Cards, cardBrief{ID: c.ID, Title: c.Title, Status: string(c.Status)})
			if c.Status == model.CardWaiting {
				view.Waiting = append(view.Waiting, waitingCard{ID: c.ID, WaitReason: c.WaitReason})
			}
			if c.Status == model.CardMergeQueued {
				view.MergeQueue = append(view.MergeQueue, cardBrief{ID: c.ID, Title: c.Title, Status: string(c.Status)})
			}
		}
	}
	for _, status := range model.AllCardStatuses() {
		view.StatusCounts = append(view.StatusCounts, statusCount{Status: string(status), Count: counts[status]})
	}
	for _, a := range attempts {
		if !a.EndedAt.IsZero() {
			continue
		}
		card, ok := cardsByID[a.Card]
		if !ok {
			continue
		}
		run := runningAttempt{ID: a.ID, Card: a.Card, Channel: a.Route.Channel, Claims: []claimView{}}
		for _, cl := range card.Claims {
			run.Claims = append(run.Claims, claimView{Path: cl.Path, Symbol: cl.Symbol})
		}
		view.RunningAttempts = append(view.RunningAttempts, run)
	}
	for _, q := range questions {
		if q.Answer != "" || !pendingQuestionStatus(objStatus[q.Objective]) {
			continue
		}
		view.PendingQuestions = append(view.PendingQuestions, questionBrief{ID: q.ID, Objective: q.Objective, Text: q.Text})
	}
	return view, nil
}

// pendingQuestionStatus reports whether an unanswered question on an objective
// of this status stays on the default view. Kind is irrelevant. Paused and
// done_candidate are not active work; their unanswered questions still are.
func pendingQuestionStatus(status model.ObjectiveStatus) bool {
	switch status {
	case model.ObjectiveActive, model.ObjectivePaused, model.ObjectiveDoneCandidate:
		return true
	default:
		return false
	}
}

func openQuestionCount(n model.DesignNode) int {
	nOpen := 0
	for _, q := range n.OpenQuestions {
		if q.Status != model.OQAnswered {
			nOpen++
		}
	}
	return nOpen
}

// currentCard is work on the default view. Paused cards, legacy held cards,
// and any v1 import (non-empty legacy status, source, or reason) are history.
func currentCard(c model.Card) bool {
	if c.Status == model.CardPaused {
		return false
	}
	if c.Legacy.Status == "held" {
		return false
	}
	return !legacyImported(c)
}

func legacyImported(c model.Card) bool {
	return c.Legacy.Status != "" || c.Legacy.SourcePath != "" || c.Legacy.Reason != ""
}

func loadChannels(ctx context.Context, db *store.Store) (*channelsView, error) {
	channels, err := db.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return nil, err
	}
	occupied := map[string]int{}
	for _, a := range attempts {
		if a.EndedAt.IsZero() && a.Route.Channel != "" {
			occupied[a.Route.Channel]++
		}
	}
	view := &channelsView{Channels: []channelView{}}
	for _, ch := range channels {
		row := channelView{
			ID:          ch.ID,
			Health:      string(ch.Health),
			Redline:     ch.Redline,
			Concurrency: ch.Concurrency,
			Occupied:    occupied[ch.ID],
			Windows:     []windowView{},
		}
		for _, w := range ch.QuotaWindows {
			row.Windows = append(row.Windows, windowView{
				Name:       w.Name,
				Remaining:  windowRemaining(w),
				Confidence: w.Confidence,
				ResetAt:    formatReset(w.ResetAt),
			})
		}
		view.Channels = append(view.Channels, row)
	}
	return view, nil
}

func formatReset(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func loadCard(ctx context.Context, db *store.Store, id string) (*cardView, error) {
	card, err := db.GetCard(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errNotFound
		}
		return nil, err
	}
	attempts, err := db.ListAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	history, err := cardHistory(ctx, db, card.ID)
	if err != nil {
		return nil, err
	}
	home, err := resolveHome()
	if err != nil {
		return nil, err
	}
	view := &cardView{
		ID: card.ID, Title: card.Title, Status: string(card.Status),
		Attempts: []attemptView{}, History: history,
	}
	for _, a := range attempts {
		events, err := db.ListEvents(ctx, "attempt:"+a.ID)
		if err != nil {
			return nil, err
		}
		evs := make([]eventView, 0, len(events))
		for _, e := range events {
			evs = append(evs, eventView{Type: e.Type, Data: eventData(e.Data)})
		}
		view.Attempts = append(view.Attempts, attemptView{
			ID:           a.ID,
			RouteReason:  a.RouteReason,
			Outcome:      string(a.Outcome),
			ErrorExcerpt: a.ErrorExcerpt,
			Exit:         a.Exit,
			BaseCommit:   a.BaseCommit,
			HeadCommit:   a.HeadCommit,
			Events:       evs,
			LogTailURL:   "/attempts/" + a.ID + "/output.log",
			Ended:        !a.EndedAt.IsZero(),
			Evidence:     evidencePaths(home, a.ID),
		})
	}
	return view, nil
}

func cardHistory(ctx context.Context, db *store.Store, id string) ([]historyEvent, error) {
	events, err := db.ListEvents(ctx, "card:"+id)
	if err != nil {
		return nil, err
	}
	out := []historyEvent{}
	for _, event := range events {
		if event.Type != "review_verdict" && event.Type != "rework" {
			continue
		}
		ts := ""
		if !event.TS.IsZero() {
			ts = event.TS.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, historyEvent{TS: ts, Type: event.Type, Data: eventData(event.Data)})
	}
	return out, nil
}

func evidencePaths(home, id string) []string {
	out := []string{}
	if strings.TrimSpace(home) == "" || !safeID(id) {
		return out
	}
	for _, name := range []string{"output.log", "acceptance.log", "result.md"} {
		path := filepath.Join(home, "attempts", id, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, "attempts/"+id+"/"+name)
	}
	return out
}

func loadHistory(ctx context.Context, db *store.Store, query, status string, page int) (*historyView, error) {
	if page < 1 {
		page = 1
	}
	objectives, err := db.ListObjectives(ctx, "")
	if err != nil {
		return nil, err
	}
	cards, err := db.ListCards(ctx, "")
	if err != nil {
		return nil, err
	}
	items := []historyItem{}
	for _, o := range objectives {
		if o.Status != model.ObjectiveDone && o.Status != model.ObjectiveCanceled {
			continue
		}
		items = append(items, historyItem{Kind: "objective", ID: o.ID, Title: o.Title, Status: string(o.Status)})
	}
	for _, c := range cards {
		if !legacyImported(c) {
			continue
		}
		items = append(items, historyItem{
			Kind:         "card",
			ID:           c.ID,
			Title:        c.Title,
			Status:       string(c.Status),
			LegacyStatus: c.Legacy.Status,
			LegacySource: c.Legacy.SourcePath,
			LegacyReason: c.Legacy.Reason,
		})
	}
	items = filterHistory(items, query, status)
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID == items[j].ID {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ID < items[j].ID
	})
	total := len(items)
	page = clampHistoryPage(page, total)
	start64 := int64(page-1) * int64(historyPageSize)
	if start64 < 0 || start64 > int64(total) {
		start64 = int64(total)
	}
	start := int(start64)
	end := start + historyPageSize
	if end > total {
		end = total
	}
	pageItems := items[start:end]
	if pageItems == nil {
		pageItems = []historyItem{}
	}
	return &historyView{
		Query:    query,
		Status:   status,
		Page:     page,
		PageSize: historyPageSize,
		Total:    total,
		Items:    pageItems,
	}, nil
}

func filterHistory(items []historyItem, query, status string) []historyItem {
	query = strings.TrimSpace(query)
	status = strings.TrimSpace(status)
	if query == "" && status == "" {
		return items
	}
	q := strings.ToLower(query)
	out := make([]historyItem, 0, len(items))
	for _, item := range items {
		if status != "" && !strings.EqualFold(item.Status, status) && !strings.EqualFold(item.LegacyStatus, status) {
			continue
		}
		if q != "" {
			blob := strings.ToLower(item.ID + "\n" + item.Title + "\n" + item.Status + "\n" + item.LegacyStatus + "\n" + item.LegacySource + "\n" + item.LegacyReason)
			if !strings.Contains(blob, q) {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

// nestNodes builds the display forest for one objective. Each node's parent
// string stays as stored. A cycle, including a self-parent, is shown by
// omitting one back-edge so every stored node appears once and child lists
// stay acyclic. A node whose parent is missing from this objective stays a
// display root, as does a node with an empty parent.
func nestNodes(flat []nodeView) []nodeView {
	order := make([]string, 0, len(flat))
	byID := make(map[string]nodeView, len(flat))
	for _, n := range flat {
		if _, ok := byID[n.ID]; ok {
			continue
		}
		byID[n.ID] = n
		order = append(order, n.ID)
	}
	childrenOf := make(map[string][]string, len(order))
	for _, id := range order {
		n := byID[id]
		if n.Parent == "" || n.Parent == n.ID {
			continue
		}
		if _, ok := byID[n.Parent]; !ok {
			continue
		}
		childrenOf[n.Parent] = append(childrenOf[n.Parent], id)
	}

	visited := make(map[string]bool, len(order))
	inStack := make(map[string]bool, len(order))
	drop := make(map[string]map[string]bool)
	var walk func(string)
	walk = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		inStack[id] = true
		for _, child := range childrenOf[id] {
			if inStack[child] {
				if drop[id] == nil {
					drop[id] = map[string]bool{}
				}
				drop[id][child] = true
				continue
			}
			walk(child)
		}
		inStack[id] = false
	}
	for _, id := range order {
		n := byID[id]
		if n.Parent == "" || n.Parent == id || byID[n.Parent].ID == "" {
			walk(id)
		}
	}
	for _, id := range order {
		if !visited[id] {
			walk(id)
		}
	}

	var build func(string) nodeView
	build = func(id string) nodeView {
		n := byID[id]
		kids := childrenOf[id]
		n.Children = make([]nodeView, 0, len(kids))
		for _, child := range kids {
			if drop[id][child] {
				continue
			}
			n.Children = append(n.Children, build(child))
		}
		return n
	}
	isChild := make(map[string]bool, len(order))
	for parent, kids := range childrenOf {
		for _, child := range kids {
			if drop[parent][child] {
				continue
			}
			isChild[child] = true
		}
	}
	out := make([]nodeView, 0, len(order))
	for _, id := range order {
		if isChild[id] {
			continue
		}
		out = append(out, build(id))
	}
	return out
}

// eventData prepares one stored attempt-event payload for a JSON response.
// Valid JSON is left unchanged. Empty data becomes {}. Any other text is
// encoded as a JSON string so one bad event does not fail the card.
func eventData(data json.RawMessage) json.RawMessage {
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

func windowRemaining(w model.QuotaWindow) *float64 {
	if w.LimitTokens == 0 && w.LimitRequests == 0 {
		return nil
	}
	v := w.Remaining
	return &v
}

func (w windowView) RemainingLabel() string {
	if w.Remaining == nil {
		return "未知"
	}
	return strconv.FormatFloat(*w.Remaining, 'f', -1, 64)
}

func (e eventView) DataText() string {
	return string(e.Data)
}

func clampHistoryPage(page, total int) int {
	if page < 1 {
		return 1
	}
	pages := 1
	if total > 0 {
		pages = int((int64(total) + int64(historyPageSize) - 1) / int64(historyPageSize))
	}
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		return pages
	}
	return page
}
