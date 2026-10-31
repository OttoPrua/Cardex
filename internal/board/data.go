package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"cardex/internal/model"
	"cardex/internal/store"
)

const historyPageSize = 50

type pageData struct {
	Title    string
	PollPath string
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
	ID                string `json:"id"`
	Status            string `json:"status"`
	OpenQuestionCount int    `json:"open_question_count"`
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
	Name       string  `json:"name"`
	Remaining  float64 `json:"remaining"`
	Confidence float64 `json:"confidence"`
	ResetAt    string  `json:"reset_at"`
}

type cardView struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Status   string        `json:"status"`
	Attempts []attemptView `json:"attempts"`
}

type attemptView struct {
	ID           string            `json:"id"`
	RouteReason  model.RouteReason `json:"route_reason"`
	Outcome      string            `json:"outcome"`
	ErrorExcerpt string            `json:"error_excerpt"`
	LogTailURL   string            `json:"log_tail_url"`
	Ended        bool              `json:"ended"`
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

// loadCurrent reads only active objectives. Cards of other objectives, including
// a large done/canceled history, are never loaded into this response.
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
	activeObj := map[string]bool{}
	cardsByID := map[string]model.Card{}
	for _, o := range objectives {
		if o.Status != model.ObjectiveActive {
			continue
		}
		activeObj[o.ID] = true
		nodes, err := db.ListNodes(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		ov := objectiveView{ID: o.ID, Title: o.Title, Status: string(o.Status), Nodes: []nodeView{}}
		for _, n := range nodes {
			ov.Nodes = append(ov.Nodes, nodeView{
				ID:                n.ID,
				Status:            string(n.Status),
				OpenQuestionCount: openQuestionCount(n),
			})
		}
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
		if q.Answer != "" || !activeObj[q.Objective] {
			continue
		}
		view.PendingQuestions = append(view.PendingQuestions, questionBrief{ID: q.ID, Objective: q.Objective, Text: q.Text})
	}
	return view, nil
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
				Remaining:  w.Remaining,
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
	view := &cardView{ID: card.ID, Title: card.Title, Status: string(card.Status), Attempts: []attemptView{}}
	for _, a := range attempts {
		view.Attempts = append(view.Attempts, attemptView{
			ID:           a.ID,
			RouteReason:  a.RouteReason,
			Outcome:      string(a.Outcome),
			ErrorExcerpt: a.ErrorExcerpt,
			LogTailURL:   "/attempts/" + a.ID + "/output.log",
			Ended:        !a.EndedAt.IsZero(),
		})
	}
	return view, nil
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
	start := (page - 1) * historyPageSize
	if start > total {
		start = total
	}
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
