package apiusage

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"
	"time"
)

const SyntheticPrice = "synthetic_fixture"

type Rates struct {
	Input        *int64 `json:"input_currency_micros_per_million"`
	Output       *int64 `json:"output_currency_micros_per_million"`
	CacheRead    *int64 `json:"cache_read_currency_micros_per_million"`
	CacheWrite   *int64 `json:"cache_write_currency_micros_per_million"`
	CacheWrite5m *int64 `json:"cache_write_5m_currency_micros_per_million"`
	CacheWrite1h *int64 `json:"cache_write_1h_currency_micros_per_million"`
	WebSearch    *int64 `json:"web_search_currency_micros_per_request"`
	WebFetch     *int64 `json:"web_fetch_currency_micros_per_request"`
}

// PriceSnapshot contains no default or live rates. Both endpoints of its
// effective UTC interval are required: effective_from <= time < effective_until.
type PriceSnapshot struct {
	Source         string    `json:"source"`
	Channel        string    `json:"channel"`
	Model          string    `json:"model"`
	Binding        string    `json:"binding"`
	Version        string    `json:"version"`
	Currency       string    `json:"currency"`
	ServiceTier    string    `json:"service_tier"`
	Speed          string    `json:"speed"`
	EffectiveFrom  time.Time `json:"effective_from"`
	EffectiveUntil time.Time `json:"effective_until"`
	Rates          Rates     `json:"rates"`
}

func (p PriceSnapshot) Validate() error {
	if p.Source != SyntheticPrice || !ValidID(p.Channel) || !ValidModel(p.Model) || !validDigest(p.Binding) || !ValidID(p.Version) || !currency.MatchString(p.Currency) ||
		p.ServiceTier == "" || p.Speed == "" || !validCategory(p.ServiceTier) || !validCategory(p.Speed) ||
		!validTime(p.EffectiveFrom) || !validTime(p.EffectiveUntil) || !p.EffectiveUntil.After(p.EffectiveFrom) {
		return ErrPrice
	}
	for _, n := range []*int64{p.Rates.Input, p.Rates.Output, p.Rates.CacheRead, p.Rates.CacheWrite,
		p.Rates.CacheWrite5m, p.Rates.CacheWrite1h, p.Rates.WebSearch, p.Rates.WebFetch} {
		if n != nil && *n < 0 {
			return ErrPrice
		}
	}
	if p.Rates.CacheWrite != nil && (p.Rates.CacheWrite5m != nil || p.Rates.CacheWrite1h != nil) {
		return ErrPrice
	}
	return nil
}

func (p PriceSnapshot) Canonical() ([]byte, string, error) {
	p.EffectiveFrom, p.EffectiveUntil = canonicalTime(p.EffectiveFrom), canonicalTime(p.EffectiveUntil)
	if err := p.Validate(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(p)
	return raw, digest(raw), err
}

func ReadPriceFixture(raw []byte) (PriceSnapshot, error) {
	var p PriceSnapshot
	if err := rejectDuplicateFields(raw); err != nil {
		return PriceSnapshot{}, ErrPrice
	}
	if err := decodeExact(raw, &p); err != nil {
		return PriceSnapshot{}, ErrPrice
	}
	return p, p.Validate()
}

func ReadStoredPrice(raw []byte) (PriceSnapshot, string, error) {
	p, err := ReadPriceFixture(raw)
	if err != nil {
		return PriceSnapshot{}, "", err
	}
	canon, sha, err := p.Canonical()
	if err != nil || !bytes.Equal(raw, canon) {
		return PriceSnapshot{}, "", ErrPrice
	}
	return p, sha, nil
}

func (p PriceSnapshot) Applicable(e Evidence) bool {
	return p.Channel == e.Channel && p.Model == e.Model && p.Binding == e.Binding && p.ServiceTier == e.Metering.ServiceTier && p.Speed == e.Metering.Speed &&
		!e.RecordedAt.Before(p.EffectiveFrom) && e.RecordedAt.Before(p.EffectiveUntil)
}

// Estimate is a synthetic arithmetic result, never a provider invoice, real
// rate, or basis for routing, ranking, quotas, or payment.
type Estimate struct {
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Currency string `json:"currency,omitempty"`
	Amount   *int64 `json:"currency_micros,string"`
	Version  string `json:"price_version,omitempty"`
	Snapshot string `json:"price_snapshot,omitempty"`
	Rounding string `json:"rounding,omitempty"`
}

func Quote(e Evidence, p *PriceSnapshot, explicitlyShowSynthetic bool) Estimate {
	if !explicitlyShowSynthetic {
		return Estimate{Status: "not_requested"}
	}
	unknown := func(reason string) Estimate { return Estimate{Status: "unknown", Reason: reason} }
	if e.Validate() != nil || e.Metering.Source != FixtureResponse || e.Metering.Disagreement {
		return unknown("verified_synthetic_metering_required")
	}
	if p == nil {
		return unknown("missing_price_snapshot")
	}
	_, sha, err := p.Canonical()
	if err != nil || e.Price == "" || sha != e.Price || p.Version != e.PriceVersion || !p.Applicable(e) {
		return unknown("price_snapshot_scope_mismatch")
	}
	u, r := e.Metering.Counts, p.Rates
	type term struct {
		n, rate *int64
		perReq  bool
	}
	terms := []term{{u.Input, r.Input, false}, {u.Output, r.Output, false}, {u.CacheRead, r.CacheRead, false},
		{u.WebSearch, r.WebSearch, true}, {u.WebFetch, r.WebFetch, true}}
	if r.CacheWrite != nil {
		terms = append(terms, term{u.CacheWrite, r.CacheWrite, false})
	} else {
		if u.CacheWrite == nil || u.CacheWrite5m == nil || u.CacheWrite1h == nil {
			return unknown("incomplete_metering_or_rates")
		}
		terms = append(terms, term{u.CacheWrite5m, r.CacheWrite5m, false}, term{u.CacheWrite1h, r.CacheWrite1h, false})
	}
	// Accumulate one exact rational numerator. Units are either micros per
	// million tokens or micros per request, with a single shared denominator.
	// Never round a line item or use floating point on money.
	denom := big.NewInt(1_000_000)
	sum := new(big.Int)
	for _, term := range terms {
		if term.n == nil || term.rate == nil {
			return unknown("incomplete_metering_or_rates")
		}
		x := new(big.Int).Mul(big.NewInt(*term.n), big.NewInt(*term.rate))
		if term.perReq {
			x.Mul(x, denom)
		}
		sum.Add(sum, x)
	}
	sum.Add(sum, big.NewInt(500_000))
	sum.Quo(sum, denom)
	if !sum.IsInt64() {
		return unknown("price_overflow")
	}
	value := sum.Int64()
	return Estimate{Status: "synthetic_estimate", Currency: p.Currency, Amount: &value, Version: p.Version, Snapshot: sha, Rounding: EstimateRounding}
}

// Reject duplicates before struct decoding, which would otherwise use
// last-wins. This is bounded and only traverses the explicit fixture input.
func rejectDuplicateFields(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxEvidenceBytes {
		return ErrPrice
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 4 {
			return ErrPrice
		}
		t, err := d.Token()
		if err != nil {
			return ErrPrice
		}
		if t == json.Delim('[') {
			return ErrPrice
		}
		if t != json.Delim('{') {
			return nil
		}
		seen := map[string]bool{}
		for d.More() {
			t, err := d.Token()
			k, ok := t.(string)
			// Case variants also conflict because Go accepts both spellings.
			k = strings.ToLower(k)
			if err != nil || !ok || seen[k] {
				return ErrPrice
			}
			seen[k] = true
			if err := walk(depth + 1); err != nil {
				return err
			}
		}
		if t, err := d.Token(); err != nil || t != json.Delim('}') {
			return ErrPrice
		}
		return nil
	}
	return walk(0)
}
