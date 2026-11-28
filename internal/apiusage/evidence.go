// Package apiusage describes compatible-channel metering separately from
// legacy quota, rankings, and billing. It holds no provider or auth access.
package apiusage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
)

type Source string

const (
	// FixtureResponse is an explicitly synthetic, single Messages response.
	// It is not an attestation of a real provider, model, or account.
	FixtureResponse Source = "synthetic_messages_fixture"
	// RecordedCLI is a transcription, not a captured provider response.
	RecordedCLI Source = "recorded_claude_cli"
)

type Operation string

const (
	Attempt    Operation = "attempt"
	Comparison Operation = "comparison"
	Probe      Operation = "probe"
)

const MaxEvidenceBytes = 1 << 20
const EstimateRounding = "nearest_currency_micro_half_up"

var (
	ErrEvidence = errors.New("invalid or ambiguous API usage evidence")
	ErrPrice    = errors.New("invalid synthetic API price snapshot")
	ErrConflict = errors.New("immutable API metering record conflicts with existing evidence")
	identifier  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	currency    = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Counts holds only allowlisted numerical measurements. Nil is unknown; a
// known zero remains zero in JSON. Cache-write and reasoning detail are
// subsets; server request counters are not tokens.
type Counts struct {
	Input        *int64 `json:"input_tokens"`
	Output       *int64 `json:"output_tokens"`
	CacheRead    *int64 `json:"cache_read_input_tokens"`
	CacheWrite   *int64 `json:"cache_creation_input_tokens"`
	CacheWrite5m *int64 `json:"ephemeral_5m_input_tokens"`
	CacheWrite1h *int64 `json:"ephemeral_1h_input_tokens"`
	Reasoning    *int64 `json:"reasoning_tokens"`
	WebSearch    *int64 `json:"web_search_requests"`
	WebFetch     *int64 `json:"web_fetch_requests"`
	// ReportedTotal is preserved separately; it never becomes another addend.
	ReportedTotal *int64 `json:"reported_total_tokens"`
}

type Measurement struct {
	Source       Source `json:"source"`
	Counts       Counts `json:"counts"`
	ServiceTier  string `json:"service_tier,omitempty"`
	Speed        string `json:"speed,omitempty"`
	Disagreement bool   `json:"disagreement,omitempty"`
}

type Evidence struct {
	// ID is the immutable local attempt ID or independent synthetic probe ID.
	ID         string      `json:"id"`
	Operation  Operation   `json:"operation"`
	Channel    string      `json:"channel"`
	Model      string      `json:"model"`
	Binding    string      `json:"binding"`
	RecordedAt time.Time   `json:"recorded_at"`
	Metering   Measurement `json:"metering"`
	// Price refers to the exact opt-in fixture snapshot, not an executable
	// account profile. Missing reference is never replaced with a newer rate.
	Price        string `json:"price_snapshot,omitempty"`
	PriceVersion string `json:"price_version,omitempty"`
}

func ValidID(s string) bool { return identifier.MatchString(s) }

func ValidModel(s string) bool {
	if !ValidID(s) {
		return false
	}
	switch strings.ToLower(s) {
	case "unknown", "opus", "sonnet", "haiku", "fable":
		return false
	}
	return true
}

func validCategory(s string) bool {
	switch s {
	case "", "standard", "priority", "batch", "flex", "fast":
		return true
	default:
		return false
	}
}

func Category(s string) string {
	if validCategory(s) {
		return s
	}
	return ""
}

func validTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 2000 && t.Year() <= 9999 }

func (e Evidence) Validate() error {
	if !ValidID(e.ID) || !ValidID(e.Channel) || !ValidModel(e.Model) || !validDigest(e.Binding) || !validTime(e.RecordedAt) ||
		(e.Operation != Attempt && e.Operation != Comparison && e.Operation != Probe) ||
		(e.Price != "" && (!validDigest(e.Price) || !ValidID(e.PriceVersion))) ||
		(e.Price == "" && e.PriceVersion != "") ||
		(e.Metering.Source != FixtureResponse && e.Metering.Source != RecordedCLI) ||
		(e.Metering.Source == RecordedCLI && (e.Operation == Probe || e.Price != "")) ||
		!validCategory(e.Metering.Speed) || !validCategory(e.Metering.ServiceTier) {
		return ErrEvidence
	}
	return e.Metering.Counts.Validate()
}

func (c Counts) Validate() error {
	for _, n := range []*int64{c.Input, c.Output, c.CacheRead, c.CacheWrite, c.CacheWrite5m,
		c.CacheWrite1h, c.Reasoning, c.WebSearch, c.WebFetch, c.ReportedTotal} {
		if n != nil && *n < 0 {
			return ErrEvidence
		}
	}
	if c.Reasoning != nil && c.Output != nil && *c.Reasoning > *c.Output {
		return ErrEvidence
	}
	if c.CacheWrite != nil {
		for _, sub := range []*int64{c.CacheWrite5m, c.CacheWrite1h} {
			if sub != nil && *sub > *c.CacheWrite {
				return ErrEvidence
			}
		}
	}
	if c.CacheWrite5m != nil && c.CacheWrite1h != nil {
		a, b := *c.CacheWrite5m, *c.CacheWrite1h
		if b > math.MaxInt64-a || (c.CacheWrite != nil && a+b != *c.CacheWrite) {
			return ErrEvidence
		}
	}
	sum, _, complete, overflow := c.knownTokenSum()
	if overflow || (c.ReportedTotal != nil && (sum > *c.ReportedTotal || (complete && sum != *c.ReportedTotal))) {
		return ErrEvidence
	}
	return nil
}

// Total really is a complete compatible charge-unit token count only when
// input, output, cache read and cache write are all present. It does not write
// back to the legacy quota/token totals or guess an omitted zero.
func (c Counts) Total() (*int64, bool) {
	sum, _, known, overflow := c.knownTokenSum()
	if !known || overflow {
		return nil, overflow
	}
	return &sum, false
}

// knownTokenSum counts complete primary buckets when present. Reasoning and
// cache-duration components may contribute only as disjoint lower bounds of
// absent primary buckets. They must not synthesize the missing parent.
func (c Counts) knownTokenSum() (sum int64, anyKnown, complete, overflow bool) {
	complete = c.Input != nil && c.Output != nil && c.CacheRead != nil && c.CacheWrite != nil
	output := c.Output
	if output == nil {
		output = c.Reasoning
	}
	writes := []*int64{c.CacheWrite}
	if c.CacheWrite == nil {
		writes = []*int64{c.CacheWrite5m, c.CacheWrite1h}
	}
	for _, n := range append([]*int64{c.Input, output, c.CacheRead}, writes...) {
		if n == nil {
			continue
		}
		if *n < 0 || *n > math.MaxInt64-sum {
			return 0, false, false, true
		}
		anyKnown = true
		sum += *n
	}
	return sum, anyKnown, complete, false
}

func canonicalTime(t time.Time) time.Time { return t.UTC().Round(0) }

func (e Evidence) Canonical() ([]byte, error) {
	e.RecordedAt = canonicalTime(e.RecordedAt)
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validDigest(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == sha256.Size && s == strings.ToLower(s)
}

// ReadEvidence reads only the canonical stored schema, not raw provider data.
// Even a manually tampered store must not surface extra metadata or costs.
func ReadEvidence(raw []byte) (Evidence, error) {
	var out Evidence
	if err := decodeExact(raw, &out); err != nil {
		return Evidence{}, ErrEvidence
	}
	canon, err := out.Canonical()
	if err != nil || !bytes.Equal(canon, raw) {
		return Evidence{}, ErrEvidence
	}
	return out, nil
}

func decodeExact(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > MaxEvidenceBytes {
		return ErrEvidence
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return ErrEvidence
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrEvidence
	}
	return nil
}

// View is the metering-only display used by ordinary board and route reads.
// It deliberately has no amount, rate, billing, or account field.
type View struct {
	Evidence
	Total            *int64 `json:"complete_token_total"`
	LowerBound       *int64 `json:"known_token_lower_bound"`
	EvidenceTrust    string `json:"evidence_trust"`
	ModelAttribution string `json:"model_attribution"`
}

func (e Evidence) View() View {
	sum, known, complete, overflow := e.Metering.Counts.knownTokenSum()
	var total, lowerBound *int64
	if !overflow && !e.Metering.Disagreement && known {
		if complete {
			total = &sum
		} else {
			lowerBound = &sum
		}
	}
	attribution, trust := "scheduled_route_only", "unverified_transcription"
	if e.Metering.Source == FixtureResponse {
		attribution, trust = "synthetic_fixture_model_match", "synthetic_only"
	}
	return View{Evidence: e, Total: total, LowerBound: lowerBound, EvidenceTrust: trust, ModelAttribution: attribution}
}
