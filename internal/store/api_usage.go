package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/apiusage"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

const apiUsageEvent = "api3_usage_receipt"
const apiPriceEvent = "api3_synthetic_price_snapshot"

func apiPriceKey(channel, modelName, version string) string {
	hash := sha256.Sum256([]byte(channel + "\x00" + modelName + "\x00" + version))
	return apiPriceEvent + ":" + hex.EncodeToString(hash[:])
}

func apiUsageEntity(id string, probe bool) string {
	if probe {
		return "api3-probe:" + id
	}
	return "api3-attempt:" + id
}

// RecordAPIPriceFixture stages one immutable synthetic channel/model version.
// It does not change channel settings, credentials, health, or the quota ledger.
func (s *Store) RecordAPIPriceFixture(ctx context.Context, p apiusage.PriceSnapshot) (string, error) {
	raw, digest, err := p.Canonical()
	if err != nil {
		return "", err
	}
	err = s.WithWriteTx(ctx, func(ctx context.Context) error {
		if err := s.checkMeteredChannel(ctx, p.Channel, p.Model, p.Binding); err != nil {
			return err
		}
		return s.putAPIMeteringEvent(ctx, apiPriceKey(p.Channel, p.Model, p.Version), "api3-price:"+p.Channel, apiPriceEvent, raw)
	})
	if err != nil {
		return "", err
	}
	return digest, nil
}

func (s *Store) GetAPIPriceFixture(ctx context.Context, channel, modelName, version string) (*apiusage.PriceSnapshot, error) {
	if !apiusage.ValidID(channel) || !apiusage.ValidModel(modelName) || !apiusage.ValidID(version) {
		return nil, apiusage.ErrPrice
	}
	e, err := s.GetEvent(ctx, apiPriceKey(channel, modelName, version))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p, _, err := apiusage.ReadStoredPrice(e.Data)
	if err != nil || e.Type != apiPriceEvent || e.Entity != "api3-price:"+channel ||
		p.Channel != channel || p.Model != modelName || p.Version != version {
		return nil, apiusage.ErrPrice
	}
	return &p, nil
}

const apiUsageReceiptSchema = "offline-api-metering-v1"

// This private envelope pins the card and closure witness without exposing
// local process identifiers or host event contents in CLI/board output.
// It is not an authenticated supplier attestation.
type storedAPIMetering struct {
	Schema   string          `json:"schema"`
	Card     string          `json:"card,omitempty"`
	Custody  string          `json:"offline_custody_sha256,omitempty"`
	Evidence json.RawMessage `json:"evidence"`
}

func apiCustodyDigest(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// RecordOfflineAPIAttemptMetering stores only typed fixture/transcribed
// input for a successful, sealed local attempt. A synthetic or archived
// source remains such even if its local route and custody match. There is
// no runtime caller or signed/production evidence path.
func (s *Store) RecordOfflineAPIAttemptMetering(ctx context.Context, e apiusage.Evidence) error {
	if e.Operation != apiusage.Attempt && e.Operation != apiusage.Comparison {
		return apiusage.ErrEvidence
	}
	raw, err := e.Canonical()
	if err != nil {
		return err
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		a, c, err := s.loadMeteredAttempt(ctx, e.ID)
		if err != nil {
			return err
		}
		pin, err := s.meteringCustody(ctx, e, a, c)
		if err != nil {
			return err
		}
		if err := s.checkAPIMeteringPrice(ctx, e); err != nil {
			return err
		}
		return s.appendOfflineMetering(ctx, e.ID, false, storedAPIMetering{Schema: apiUsageReceiptSchema, Card: c.ID, Custody: pin, Evidence: raw})
	})
}

// RecordAPIProbeFixture keeps an independent synthetic counting record. It
// never updates an attempt, score, or channel quality and isn't returned by
// the attempt accessor even if a probe happens to use the same ID.
func (s *Store) RecordAPIProbeFixture(ctx context.Context, e apiusage.Evidence) error {
	if e.Operation != apiusage.Probe || e.Metering.Source != apiusage.FixtureResponse {
		return apiusage.ErrEvidence
	}
	raw, err := e.Canonical()
	if err != nil {
		return err
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		if err := s.checkMeteredChannel(ctx, e.Channel, e.Model, e.Binding); err != nil {
			return err
		}
		if err := s.checkAPIMeteringPrice(ctx, e); err != nil {
			return err
		}
		return s.appendOfflineMetering(ctx, e.ID, true, storedAPIMetering{Schema: apiUsageReceiptSchema, Evidence: raw})
	})
}

func (s *Store) appendOfflineMetering(ctx context.Context, id string, probe bool, receipt storedAPIMetering) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	entity := apiUsageEntity(id, probe)
	return s.putAPIMeteringEvent(ctx, entity, entity, apiUsageEvent, raw)
}

func (s *Store) GetAPIAttemptMetering(ctx context.Context, observed model.Attempt) (out *apiusage.Evidence, err error) {
	err = s.WithTx(ctx, func(ctx context.Context) error {
		e, receipt, err := s.getAPIMetering(ctx, observed.ID, false)
		if e == nil || err != nil {
			return err
		}
		// The caller's row is just a selection hint, not attribution. Load
		// the current attempt, card, channel and witnesses in this snapshot.
		a, c, err := s.loadMeteredAttempt(ctx, observed.ID)
		if err != nil {
			return err
		}
		if observed.Card != receipt.Card || observed.Index != a.Index || !observed.StartedAt.Equal(a.StartedAt) ||
			!observed.EndedAt.Equal(a.EndedAt) || observed.PID != a.PID || observed.Outcome != a.Outcome ||
			observed.ErrorClass != a.ErrorClass || observed.Exit == nil || a.Exit == nil || *observed.Exit != *a.Exit ||
			!meteringMatchesAttempt(*e, observed) || observed.StrictComparison() != a.StrictComparison() {
			return apiusage.ErrEvidence
		}
		pin, err := s.meteringCustody(ctx, *e, a, c)
		if err != nil {
			return err
		}
		if receipt.Card != c.ID || receipt.Custody != pin {
			return apiusage.ErrEvidence
		}
		out = e
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetAPIProbeMetering(ctx context.Context, id string) (*apiusage.Evidence, error) {
	e, _, err := s.getAPIMetering(ctx, id, true)
	return e, err
}

func (s *Store) getAPIMetering(ctx context.Context, id string, probe bool) (*apiusage.Evidence, storedAPIMetering, error) {
	var receipt storedAPIMetering
	if !apiusage.ValidID(id) {
		return nil, receipt, apiusage.ErrEvidence
	}
	entity := apiUsageEntity(id, probe)
	ev, err := s.GetEvent(ctx, entity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, receipt, nil
	}
	if err != nil {
		return nil, receipt, err
	}
	if ev.Type != apiUsageEvent || ev.Entity != entity || len(ev.Data) > apiusage.MaxEvidenceBytes ||
		json.Unmarshal(ev.Data, &receipt) != nil || receipt.Schema != apiUsageReceiptSchema {
		return nil, receipt, apiusage.ErrEvidence
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(canonical, ev.Data) ||
		(probe && (receipt.Card != "" || receipt.Custody != "")) ||
		(!probe && (!apiusage.ValidID(receipt.Card) || !model.StrongDigestKey(receipt.Custody))) {
		return nil, receipt, apiusage.ErrEvidence
	}
	e, err := apiusage.ReadEvidence(receipt.Evidence)
	if err != nil || e.ID != id || (e.Operation == apiusage.Probe) != probe {
		return nil, receipt, apiusage.ErrEvidence
	}
	return &e, receipt, nil
}

func (s *Store) loadMeteredAttempt(ctx context.Context, id string) (model.Attempt, model.Card, error) {
	a, err := s.GetAttempt(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		err = apiusage.ErrEvidence
	}
	if err != nil {
		return a, model.Card{}, err
	}
	c, err := s.GetCard(ctx, a.Card)
	if errors.Is(err, sql.ErrNoRows) {
		err = apiusage.ErrEvidence
	}
	return a, c, err
}

func meteringMatchesAttempt(e apiusage.Evidence, a model.Attempt) bool {
	d := a.RouteReason.Dispatch
	return a.ID == e.ID && !a.StartedAt.IsZero() && a.StartedAt.Equal(e.RecordedAt) && !a.EndedAt.IsZero() && !a.EndedAt.Before(a.StartedAt) &&
		a.Index >= 0 && a.Exit != nil && *a.Exit == 0 && a.Outcome == model.OutcomePassed && a.ErrorClass == model.ErrorPassed &&
		d != nil && d.Runner == model.RunnerClaude && d.CompatibleBindingID == e.Binding &&
		d.Channel == e.Channel && d.Model == e.Model && a.Route.Channel == e.Channel && a.Route.Model == e.Model
}

func (s *Store) meteringCustody(ctx context.Context, e apiusage.Evidence, a model.Attempt, c model.Card) (string, error) {
	if !meteringMatchesAttempt(e, a) || a.Card != c.ID || !apiusage.ValidID(c.ID) ||
		(c.Channel != "" && c.Channel != e.Channel) || (c.Model != "" && c.Model != e.Model) ||
		c.Status == model.CardCanceled || c.WaitReason == model.CompatibleRuntimeHold || c.WaitReason == StrictComparisonDisabled {
		return "", apiusage.ErrEvidence
	}
	if err := s.checkMeteredChannel(ctx, e.Channel, e.Model, e.Binding); err != nil {
		return "", err
	}
	// Strong/typed workflows have their own custody state machines. API3
	// must not declare one closed based on an ordinary attempt row.
	if owned, err := s.strictComparisonTypedOwner(ctx, a.ID); owned || err != nil {
		if err != nil {
			return "", err
		}
		return "", apiusage.ErrEvidence
	}
	var noWire bool
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE entity=? AND event_type IN (?,?))`,
		"attempt:"+a.ID, EventComparisonRefusedNoWire, "compatible_runtime_refused_no_wire").Scan(&noWire); err != nil {
		return "", err
	} else if noWire {
		return "", apiusage.ErrEvidence
	}
	d := a.RouteReason.Dispatch
	origin, err := apiOrigin(a, c)
	if err != nil {
		return "", err
	}
	var witness string
	if c.StrictComparison() || a.StrictComparison() {
		// One marker is enough for comparison isolation; two consistent
		// markers plus the real local API2 pipeline receipt are required
		// before any offline comparison fixture can be attached.
		if !c.StrictComparison() || !a.StrictComparison() || e.Operation != apiusage.Comparison || e.Metering.Source != apiusage.FixtureResponse {
			return "", apiusage.ErrEvidence
		}
		witness, err = s.meteredComparisonWitness(ctx, a, c, origin)
	} else {
		if e.Operation != apiusage.Attempt || d.ComparisonInputDigest != "" || a.RouteReason.ProviderModel != nil || a.PID <= 0 {
			return "", apiusage.ErrEvidence
		}
		witness, err = s.meteredSyntheticCompletion(ctx, a, c, origin)
	}
	if err != nil {
		return "", err
	}
	return apiCustodyDigest(struct {
		Card, Objective, PinChannel, PinModel, Attempt, Channel, Model, Binding, Witness string
		Index, PID, Exit                                                                 int
		Started, Ended                                                                   time.Time
		Outcome                                                                          model.Outcome
		Class                                                                            model.ErrorClass
	}{c.ID, c.Objective, c.Channel, c.Model, a.ID, e.Channel, e.Model, e.Binding, witness,
		a.Index, a.PID, *a.Exit, a.StartedAt.UTC(), a.EndedAt.UTC(), a.Outcome, a.ErrorClass})
}

func (s *Store) meteredHostWitness(ctx context.Context, a model.Attempt) (string, error) {
	var strict bool
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE entity=? AND event_type IN (?,?,?))`,
		"attempt:"+a.ID, comparisonClaimEvent, comparisonGenerationEvent, comparisonResultEvent).Scan(&strict); err != nil {
		return "", err
	} else if strict {
		return "", apiusage.ErrEvidence
	}
	// Only inspect allowlisted structural attributes, not runner argv,
	// prompts, output, or historic logs. Require exactly one start and one
	// terminal event; a lost/non-started/ambiguous session cannot qualify.
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id,ts,event_type,
		CASE WHEN event_type='host_start' THEN COALESCE(json_extract(data,'$.runner')='claude',0)
		ELSE COALESCE(json_type(data,'$.exit')='integer' AND json_extract(data,'$.exit')=0
			AND COALESCE(json_type(data,'$.started'),'true')='true',0) END
		FROM events WHERE entity=? AND event_type IN ('host_start','host_finish') LIMIT 3`, "attempt:"+a.ID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var start, finish string
	for rows.Next() {
		var id, ts, kind string
		var eligible bool
		if err := rows.Scan(&id, &ts, &kind, &eligible); err != nil {
			return "", err
		}
		t, err := parseTime(ts)
		if err != nil || !eligible || !apiusage.ValidID(id) || t.IsZero() {
			return "", apiusage.ErrEvidence
		}
		if kind == "host_start" {
			if start != "" || t.Before(a.StartedAt) || t.After(a.EndedAt) {
				return "", apiusage.ErrEvidence
			}
			start = id + ":" + ts
		} else {
			if finish != "" || t.Before(a.EndedAt) {
				return "", apiusage.ErrEvidence
			}
			finish = id + ":" + ts
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if start == "" || finish == "" {
		return "", apiusage.ErrEvidence
	}
	return apiCustodyDigest([]string{"offline-host-witness-v1", start, finish})
}

func (s *Store) meteredComparisonWitness(ctx context.Context, a model.Attempt, c model.Card, origin apiExecutionOrigin) (string, error) {
	if !a.VerifiedComparison(c) {
		return "", apiusage.ErrEvidence
	}
	// Strict route metadata does not prove the authenticated bounded
	// comparison path ran. Require the original linked one-shot claim and
	// generation plus exactly the isolated result accepted by API2.
	events := make([]model.Event, 0, 3)
	for _, kind := range []string{comparisonClaimEvent, comparisonGenerationEvent, comparisonResultEvent} {
		ev, err := s.GetEvent(ctx, comparisonEventID(kind, a.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return "", apiusage.ErrEvidence
		}
		if err != nil {
			return "", err
		}
		// The actual host takes the claim before setting StartedAt. Only
		// generation and result are bounded by the running interval; all
		// three receipts must remain in monotonic order.
		if ev.Type != kind || ev.Entity != "attempt:"+a.ID || ev.TS.IsZero() || ev.TS.After(a.EndedAt) ||
			(kind != comparisonClaimEvent && ev.TS.Before(a.StartedAt)) ||
			(len(events) > 0 && ev.TS.Before(events[len(events)-1].TS)) {
			return "", apiusage.ErrEvidence
		}
		events = append(events, ev)
	}
	var claim comparisonClaimReceipt
	hash, err := apiCustodyDigest(origin)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(events[0].Data, events[1].Data) || json.Unmarshal(events[0].Data, &claim) != nil ||
		!model.StrongDigestKey(claim.Digest) || claim.Origin != hash {
		return "", apiusage.ErrEvidence
	}
	claimRaw, err := json.Marshal(claim)
	if err != nil || !bytes.Equal(claimRaw, events[0].Data) {
		return "", apiusage.ErrEvidence
	}
	d, proof := a.RouteReason.Dispatch, a.RouteReason.ProviderModel
	var result model.ComparisonResult
	if len(events[2].Data) > 8*model.ComparisonMaxAnswer || json.Unmarshal(events[2].Data, &result) != nil ||
		result.InputDigest != d.ComparisonInputDigest || result.Identity != *proof ||
		strings.TrimSpace(result.Answer) == "" || len(result.Answer) > model.ComparisonMaxAnswer ||
		strings.ContainsRune(result.Answer, '\x00') {
		return "", apiusage.ErrEvidence
	}
	canonical, err := json.Marshal(result)
	if err != nil || !bytes.Equal(canonical, events[2].Data) {
		return "", apiusage.ErrEvidence
	}
	return apiCustodyDigest(events)
}

func (s *Store) checkMeteredChannel(ctx context.Context, channel, modelName, binding string) error {
	ch, err := s.GetChannel(ctx, channel)
	if err != nil {
		return err
	}
	if ch.Runner != model.RunnerClaude || binding == "" || runner.CompatibleBindingID(ch.EngineProfile) != binding || !slices.Contains(ch.Models, modelName) {
		return apiusage.ErrEvidence
	}
	return nil
}

func (s *Store) checkAPIMeteringPrice(ctx context.Context, e apiusage.Evidence) error {
	if e.Price == "" {
		return nil
	}
	p, err := s.GetAPIPriceFixture(ctx, e.Channel, e.Model, e.PriceVersion)
	if err != nil {
		return err
	}
	if p == nil || !p.Applicable(e) {
		return apiusage.ErrPrice
	}
	_, digest, err := p.Canonical()
	if err != nil || digest != e.Price {
		return apiusage.ErrPrice
	}
	return nil
}

func (s *Store) putAPIMeteringEvent(ctx context.Context, id, entity, kind string, raw []byte) error {
	ev, err := s.GetEvent(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return s.AppendEvent(ctx, &model.Event{ID: id, Entity: entity, Type: kind, Data: raw})
	}
	if err != nil {
		return err
	}
	if ev.Entity != entity || ev.Type != kind || !bytes.Equal(ev.Data, raw) {
		return apiusage.ErrConflict
	}
	return nil
}
