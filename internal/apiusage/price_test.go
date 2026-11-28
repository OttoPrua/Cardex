package apiusage

import (
	"math"
	"strings"
	"testing"
	"time"
)

func number(n int64) *int64 { return &n }

func syntheticEstimateFixture(t *testing.T) (Evidence, PriceSnapshot) {
	t.Helper()
	at := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	binding := strings.Repeat("d", 64)
	m := Measurement{Source: FixtureResponse, ServiceTier: "standard", Speed: "standard", Counts: Counts{
		Input: number(3), Output: number(4), CacheRead: number(1), CacheWrite: number(2), CacheWrite5m: number(1),
		CacheWrite1h: number(1), Reasoning: number(2), WebSearch: number(2), WebFetch: number(0), ReportedTotal: number(10),
	}}
	e := Evidence{ID: "a-fixture", Channel: "fixture-a", Model: "model-1", Binding: binding, Operation: Attempt, RecordedAt: at, Metering: m}
	p := PriceSnapshot{Source: SyntheticPrice, Channel: e.Channel, Model: e.Model, Binding: binding, Version: "v1", Currency: "USD",
		ServiceTier: "standard", Speed: "standard", EffectiveFrom: at, EffectiveUntil: at.Add(time.Hour), Rates: Rates{
			Input: number(1_000_000), Output: number(2_000_000), CacheRead: number(5_000_000), CacheWrite: number(7_000_000),
			WebSearch: number(3), WebFetch: number(0),
		}}
	linkPrice(t, &e, p)
	return e, p
}

func linkPrice(t *testing.T, e *Evidence, p PriceSnapshot) {
	t.Helper()
	_, hash, err := p.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	e.Price, e.PriceVersion = hash, p.Version
}

func expectEstimate(t *testing.T, got Estimate, amount int64, currency string) {
	t.Helper()
	if got.Status != "synthetic_estimate" || got.Amount == nil || *got.Amount != amount || got.Currency != currency {
		t.Fatalf("wrong fixture quote: %+v", got)
	}
}

func TestAPI3SyntheticCostSeparationAndExactScope(t *testing.T) {
	e, p := syntheticEstimateFixture(t)
	if hidden := Quote(e, &p, false); hidden.Status != "not_requested" || hidden.Amount != nil || hidden.Currency != "" {
		t.Fatal("ordinary display disclosed amount")
	}
	expectEstimate(t, Quote(e, &p, true), 36, "USD")
	p.Rates.CacheWrite, p.Rates.CacheWrite5m, p.Rates.CacheWrite1h = nil, number(2_000_000), number(7_000_000)
	p.Version = "split"
	linkPrice(t, &e, p)
	expectEstimate(t, Quote(e, &p, true), 31, "USD")
	other := p
	other.Channel, other.Currency, other.Version = "fixture-b", "EUR", "v2"
	other.Rates.Input = number(3_000_000)
	linkPrice(t, &e, other)
	if got := Quote(e, &other, true); got.Status != "unknown" || got.Amount != nil {
		t.Fatal("one channel borrowed another's price for a same-name model")
	}
	e.Channel = other.Channel
	expectEstimate(t, Quote(e, &other, true), 37, "EUR")
	e.RecordedAt = other.EffectiveUntil
	if got := Quote(e, &other, true); got.Status != "unknown" || got.Amount != nil {
		t.Fatal("effective_until was incorrectly inclusive")
	}
	e.RecordedAt = other.EffectiveFrom
	e.Binding = strings.Repeat("e", 64)
	if got := Quote(e, &other, true); got.Status != "unknown" || got.Amount != nil {
		t.Fatal("rebinding inherited old price")
	}
}

func TestAPI3UnknownDoesNotBecomeZeroAndOverflowIsSafe(t *testing.T) {
	e, p := syntheticEstimateFixture(t)
	e.Metering.Counts.WebFetch = nil
	if got := Quote(e, &p, true); got.Reason != "incomplete_metering_or_rates" || got.Amount != nil {
		t.Fatal("unknown measurement treated as zero")
	}
	e.Metering.Counts.WebFetch = number(0)
	p.Rates.WebFetch = nil
	linkPrice(t, &e, p)
	if got := Quote(e, &p, true); got.Reason != "incomplete_metering_or_rates" || got.Amount != nil {
		t.Fatal("missing rate treated as free")
	}
	p.Rates.WebFetch, p.Rates.WebSearch = number(0), number(math.MaxInt64)
	linkPrice(t, &e, p)
	if got := Quote(e, &p, true); got.Reason != "price_overflow" || got.Amount != nil {
		t.Fatal("money overflow was silently truncated")
	}
	e.Metering.Source, e.Price, e.PriceVersion = RecordedCLI, "", ""
	if got := Quote(e, &p, true); got.Status != "unknown" || got.Amount != nil {
		t.Fatal("CLI reported usage entered estimated supplier amounts")
	}
}

func TestAPI3PriceRoundsOnceAndCanonicalReads(t *testing.T) {
	e, p := syntheticEstimateFixture(t)
	p.Rates = Rates{Input: number(100_000), Output: number(50_000), CacheRead: number(0), CacheWrite: number(0),
		WebSearch: number(0), WebFetch: number(0)}
	linkPrice(t, &e, p)
	expectEstimate(t, Quote(e, &p, true), 1, "USD") // 0.3 + 0.2, then round once
	raw, hash, err := p.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	q, digest, err := ReadStoredPrice(raw)
	if err != nil || hash != digest || !q.Applicable(e) {
		t.Fatal("canonical price did not round trip")
	}
	bad := []byte(strings.Replace(string(raw), `"currency":"USD"`, `"currency":"USD","Currency":"EUR"`, 1))
	if _, err := ReadPriceFixture(bad); err == nil {
		t.Fatal("ambiguous price currency accepted")
	}
	eRaw, err := e.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvidence(eRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvidence(append(append([]byte{}, eRaw...), ' ')); err == nil {
		t.Fatal("noncanonical evidence was accepted as a stored immutable receipt")
	}
}

func TestAPI3PartialLowerBoundIsNeverPriceable(t *testing.T) {
	e, p := syntheticEstimateFixture(t)
	e.Metering.Counts.Output = nil
	e.Metering.Counts.CacheWrite = nil
	p.Rates.CacheWrite, p.Rates.CacheWrite5m, p.Rates.CacheWrite1h = nil, number(1_000_000), number(1_000_000)
	linkPrice(t, &e, p)
	v := e.View()
	if v.Total != nil || v.LowerBound == nil || *v.LowerBound != 8 || v.EvidenceTrust != "synthetic_only" {
		t.Fatalf("synthetic lower bound should be non-priceable and keep unknown parents: %+v", v)
	}
	if got := Quote(e, &p, true); got.Amount != nil || got.Status != "unknown" {
		t.Fatal("partial lower bound was priced")
	}
	e.Metering.Disagreement = true
	if got := e.View(); got.LowerBound != nil || got.Total != nil {
		t.Fatal("disagreement produced a trusted aggregate")
	}
}
