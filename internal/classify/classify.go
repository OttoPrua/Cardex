package classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"cardex/internal/model"
)

// Cache stores successful model classifications by card-body hash.
type Cache struct {
	mu sync.Mutex
	by map[string]Result
}

// NewCache returns an empty body-hash cache.
func NewCache() *Cache {
	return &Cache{by: map[string]Result{}}
}

func (c *Cache) get(hash string) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.by[hash]
	return r, ok
}

func (c *Cache) put(hash string, r Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.by[hash] = r
}

// Classify returns the card tier when one is already set.
// Otherwise it calls the classifier once, caches a parsed tier by body hash, or falls back to the kind default.
func Classify(ctx context.Context, card model.Card, opt Options) (Result, error) {
	if card.Tier.Valid() && card.Tier != model.TierNone {
		return Result{Tier: card.Tier, Reason: "tier already set"}, nil
	}
	hash := bodyHash(card.Body)
	if opt.Cache != nil {
		if hit, ok := opt.Cache.get(hash); ok {
			hit.Source = SourceCache
			return hit, nil
		}
	}
	fallback := Result{Tier: model.DefaultTier(card.Kind), Source: SourceRule, Reason: "kind default"}
	if opt.Call == nil || strings.TrimSpace(opt.Channel) == "" {
		return fallback, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, ClassifyTimeout)
	defer cancel()
	prompt := PromptTemplate + "KIND: " + string(card.Kind) + "\nTITLE: " + card.Title + "\nBODY:\n" + card.Body
	out, err := opt.Call(callCtx, opt.Channel, prompt)
	if err != nil {
		return fallback, nil
	}
	tier, reason, ok := parseOutput(out)
	if !ok {
		return fallback, nil
	}
	res := Result{Tier: tier, Source: SourceModel, Reason: reason}
	if opt.Cache != nil {
		opt.Cache.put(hash, res)
	}
	return res, nil
}

func parseOutput(out string) (model.Tier, string, bool) {
	var tier model.Tier
	var reason string
	sawTier := false
	sawReason := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "TIER:") {
			tier = model.Tier(strings.ToUpper(strings.TrimSpace(line[len("TIER:"):])))
			sawTier = true
		}
		if strings.HasPrefix(upper, "REASON:") {
			reason = strings.TrimSpace(line[len("REASON:"):])
			sawReason = true
		}
	}
	if !sawTier || !sawReason || reason == "" || !tier.Valid() || tier == model.TierNone {
		return "", "", false
	}
	return tier, reason, true
}

func bodyHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
