package scorecard

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"cardex/internal/model"
	"cardex/internal/store"
)

const (
	reviewPass = "loop.review_pass"
	reviewBind = "loop.review_bind"
)

// reviewBindNote is the existing bind record. Its design field is the reviewed card.
// A loop.review_pass payload is not this record: that payload names the review card.
type reviewBindNote struct {
	Design string `json:"design"`
}

func openHome(home string) (*store.Store, error) {
	return store.Open(filepath.Join(home, "cardex.db"))
}

type bucketKey struct {
	model string
	kind  model.CardKind
	tier  model.Tier
}

type cardStat struct {
	rework int
	merged bool
	tokens int64
	review bool
}

type bucket struct {
	attempts int
	cards    map[string]*cardStat
}

func aggregate(ctx context.Context, db *store.Store) ([]Row, error) {
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return nil, err
	}
	cards, err := db.ListCards(ctx, "")
	if err != nil {
		return nil, err
	}
	events, err := db.ListEvents(ctx, "")
	if err != nil {
		return nil, err
	}
	byID := map[string]model.Card{}
	for _, card := range cards {
		byID[card.ID] = card
	}
	passed := passedCards(byID, events)
	groups := map[bucketKey]*bucket{}
	for _, attempt := range attempts {
		card, ok := byID[attempt.Card]
		if !ok {
			continue
		}
		key := bucketKey{model: attempt.Route.Model, kind: card.Kind, tier: card.Tier}
		group := groups[key]
		if group == nil {
			group = &bucket{cards: map[string]*cardStat{}}
			groups[key] = group
		}
		group.attempts++
		stat := group.cards[card.ID]
		if stat == nil {
			stat = &cardStat{
				rework: card.ReworkCount,
				merged: card.Status == model.CardMerged,
				review: passed[card.ID],
			}
			group.cards[card.ID] = stat
		}
		if stat.merged {
			stat.tokens += attempt.Usage.Tokens
		}
	}
	rows := make([]Row, 0, len(groups))
	for key, group := range groups {
		if group.attempts == 0 {
			continue
		}
		rows = append(rows, rowOf(key, group))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Model != rows[j].Model {
			return rows[i].Model < rows[j].Model
		}
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Tier < rows[j].Tier
	})
	return rows, nil
}

func passedCards(byID map[string]model.Card, events []model.Event) map[string]bool {
	bound := map[string]string{}
	for _, ev := range events {
		if ev.Type != reviewBind || len(ev.Data) == 0 {
			continue
		}
		reviewID, ok := cardEntity(ev.Entity)
		if !ok {
			continue
		}
		var note reviewBindNote
		if json.Unmarshal(ev.Data, &note) != nil || note.Design == "" || note.Design == reviewID {
			continue
		}
		if _, ok := byID[note.Design]; !ok {
			continue
		}
		bound[reviewID] = note.Design
	}
	passed := map[string]bool{}
	for _, ev := range events {
		if ev.Type != reviewPass {
			continue
		}
		id, ok := cardEntity(ev.Entity)
		if !ok {
			continue
		}
		card, ok := byID[id]
		if !ok {
			continue
		}
		if card.Kind == model.KindReview {
			id = reviewedCard(card, bound, byID)
			if id == "" {
				continue
			}
		}
		passed[id] = true
	}
	return passed
}

func reviewedCard(card model.Card, bound map[string]string, byID map[string]model.Card) string {
	for _, dep := range card.Deps {
		if _, ok := byID[dep]; ok {
			return dep
		}
	}
	if id := bound[card.ID]; id != "" {
		return id
	}
	return ""
}

func cardEntity(entity string) (string, bool) {
	const prefix = "card:"
	if !strings.HasPrefix(entity, prefix) {
		return "", false
	}
	id := entity[len(prefix):]
	if id == "" {
		return "", false
	}
	return id, true
}

func rowOf(key bucketKey, group *bucket) Row {
	n := len(group.cards)
	var passed, merged int
	var rework int
	var tokens int64
	for _, stat := range group.cards {
		if stat.review {
			passed++
		}
		if stat.merged {
			merged++
			tokens += stat.tokens
		}
		rework += stat.rework
	}
	row := Row{
		Model:        key.model,
		Kind:         key.kind,
		Tier:         key.tier,
		Sample:       group.attempts,
		Insufficient: group.attempts < 5,
	}
	if n > 0 {
		row.ReviewPassRate = float64(passed) / float64(n)
		row.MergeRate = float64(merged) / float64(n)
		row.AvgRework = float64(rework) / float64(n)
	}
	if merged > 0 {
		row.TokensPerMerge = float64(tokens) / float64(merged)
	}
	return row
}
