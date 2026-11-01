package scorecard

import (
	"context"
	"path/filepath"
	"sort"

	"cardex/internal/model"
	"cardex/internal/store"
)

const reviewPass = "loop.review_pass"

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
	passed := map[string]bool{}
	for _, ev := range events {
		if ev.Type == reviewPass {
			passed[ev.Entity] = true
		}
	}
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
				review: passed["card:"+card.ID],
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
