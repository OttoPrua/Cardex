package store

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// AttemptUsageSummary is numeric evidence for one persisted attempt. It does
// not count provider turns or tool calls as additional attempts.
type AttemptUsageSummary struct {
	ID     string `json:"id"`
	Index  int    `json:"index"`
	Total  *int64 `json:"total"`
	Source string `json:"source,omitempty"`
}

// CardUsageSummary separates the sum of known values from a complete total.
// Total is null when any attempt is unknown or the sum overflows.
type CardUsageSummary struct {
	Card            string                `json:"card"`
	Attempts        []AttemptUsageSummary `json:"attempts"`
	Total           *int64                `json:"total"`
	KnownTotal      *int64                `json:"known_total"`
	UnknownAttempts int                   `json:"unknown_attempts"`
	Overflow        bool                  `json:"overflow"`
}

// SummarizeCardUsage counts each persisted attempt exactly once. Looking at
// stream samples or host events here would double count cumulative usage.
func (s *Store) SummarizeCardUsage(ctx context.Context, cardID string) (CardUsageSummary, error) {
	if cardID == "" {
		return CardUsageSummary{}, fmt.Errorf("a card id is required")
	}
	if _, err := s.GetCard(ctx, cardID); err != nil {
		return CardUsageSummary{}, err
	}
	attempts, err := s.ListAttempts(ctx, cardID)
	if err != nil {
		return CardUsageSummary{}, err
	}
	sort.Slice(attempts, func(i, j int) bool {
		if attempts[i].Index == attempts[j].Index {
			return attempts[i].ID < attempts[j].ID
		}
		return attempts[i].Index < attempts[j].Index
	})
	report := CardUsageSummary{Card: cardID, Attempts: make([]AttemptUsageSummary, 0, len(attempts))}
	var sum int64
	var known int
	for _, a := range attempts {
		x := AttemptUsageSummary{ID: a.ID, Index: a.Index, Source: a.Usage.Source}
		if a.Usage.Source == "" || a.Usage.Confidence <= 0 || a.Usage.Tokens < 0 {
			report.UnknownAttempts++
		} else {
			n := a.Usage.Tokens
			x.Total = &n
			known++
			if n > math.MaxInt64-sum {
				report.Overflow = true
			} else {
				sum += n
			}
		}
		report.Attempts = append(report.Attempts, x)
	}
	if !report.Overflow && (known > 0 || len(attempts) == 0) {
		report.KnownTotal = &sum
		if report.UnknownAttempts == 0 {
			report.Total = &sum
		}
	}
	return report, nil
}
