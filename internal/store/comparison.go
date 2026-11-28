package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// StrictComparisonClaim is local to the host that acquired it. The durable
// record only includes a digest, not a provider credential or session token.
type StrictComparisonClaim struct {
	attempt, card, channel, model, binding, input, data string
	origin                                              apiExecutionOrigin
}

type comparisonClaimReceipt struct {
	Digest string `json:"claim_digest"`
	Origin string `json:"execution_origin_sha256"`
}

const comparisonClaimEvent = "strict_comparison_claim"
const comparisonGenerationEvent = "strict_comparison_generation"

func comparisonEventID(event, attempt string) string { return event + ":" + attempt }

// ClaimStrictComparison atomically claims an unfinished attempt, before
// credential resolution. Claims are never transferred, expired, or deleted:
// after a crash or duplicate invocation, another host cannot resolve
// credentials or try to send a second potentially chargeable generation.
func (s *Store) ClaimStrictComparison(ctx context.Context, a model.Attempt) (StrictComparisonClaim, bool, error) {
	if a.ID == "" {
		return StrictComparisonClaim{}, false, fmt.Errorf("comparison attempt id required")
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return StrictComparisonClaim{}, false, err
	}
	digest := sha256.Sum256(entropy[:])
	var claim StrictComparisonClaim
	var claimed bool
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		// Capture the stored origin before launch under the same writer that
		// reserves the one-shot claim. Neither a stale input nor a later row
		// reassignment supplies origin for these durable records.
		current, err := s.GetAttempt(ctx, a.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		c, err := s.GetCard(ctx, current.Card)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		pin, freshErr := apiOrigin(current, c)
		observed, oldErr := apiOrigin(a, c)
		if freshErr != nil || oldErr != nil || pin != observed || !pin.Strict {
			return nil
		}
		hash, err := apiCustodyDigest(pin)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(comparisonClaimReceipt{Digest: hex.EncodeToString(digest[:]), Origin: hash})
		if err != nil {
			return err
		}
		claim = StrictComparisonClaim{
			attempt: pin.Attempt, card: pin.Card, channel: pin.Channel, model: pin.Model,
			binding: pin.Binding, input: pin.Input, data: string(raw), origin: pin,
		}
		res, err := s.q(ctx).ExecContext(ctx, `INSERT INTO events(id, ts, entity, event_type, data)
			SELECT ?, ?, ?, ?, ? FROM attempts
			WHERE id=? AND card_id=? AND channel=? AND model=? AND ended_at='' AND exited=0 AND outcome=''
			ON CONFLICT(id) DO NOTHING`, comparisonEventID(comparisonClaimEvent, a.ID),
			formatTime(time.Now()), "attempt:"+a.ID, comparisonClaimEvent, claim.data,
			a.ID, a.Card, a.Route.Channel, a.Route.Model)
		if err != nil {
			return err
		}
		note, err := res.RowsAffected()
		claimed = note == 1
		return err
	})
	if err != nil || !claimed {
		return StrictComparisonClaim{}, false, err
	}
	return claim, true, nil
}

// AuthorizeStrictComparisonRequest confirms the same host still owns this
// unchanged, eligible attempt. Consuming the one allowed generation writes
// a second permanent event before the request goes out, even if it later
// times out and the provider result is unknown.
func (s *Store) AuthorizeStrictComparisonRequest(ctx context.Context, claim StrictComparisonClaim, profile, approvedBody string, generation bool) (bool, error) {
	if claim.attempt == "" || claim.binding == "" || claim.input == "" || profile == "" {
		return false, nil
	}
	const conditions = ` FROM attempts AS a
		JOIN cards AS c ON c.id=a.card_id
		JOIN channels AS h ON h.id=a.channel
		JOIN events AS claimed ON claimed.id=?
		WHERE a.id=? AND a.card_id=? AND a.channel=? AND a.model=?
		AND a.ended_at='' AND a.exited=0 AND a.outcome=''
		AND a.attempt_index=? AND a.worktree=? AND a.prompt_path=? AND a.branch=? AND a.base_commit=? AND c.objective_id=?
		AND json_extract(a.route_reason, '$.dispatch.strict_comparison')=1
		AND json_extract(a.route_reason, '$.dispatch.compatible_binding_id')=?
		AND json_extract(a.route_reason, '$.dispatch.comparison_input_digest')=?
		AND json_extract(a.route_reason, '$.dispatch.expected_channel')=a.channel
		AND json_extract(a.route_reason, '$.dispatch.expected_model')=a.model
		AND json_extract(a.route_reason, '$.dispatch.runner')='claude'
		AND c.status IN ('ready','running') AND c.kind='research' AND c.node_id=''
		AND c.channel=a.channel AND c.model=a.model AND c.sandbox<>'off' AND c.body=?
		AND json_array_length(c.requires)=1 AND json_extract(c.requires, '$[0]')='strict_model_comparison'
		AND h.runner='claude' AND h.health='ok' AND h.engine_profile=?
		AND EXISTS (SELECT 1 FROM json_each(h.models) WHERE value=a.model)
		AND claimed.entity=? AND claimed.event_type=? AND claimed.data=?`
	args := []any{comparisonEventID(comparisonClaimEvent, claim.attempt),
		claim.attempt, claim.card, claim.channel, claim.model,
		claim.origin.Index, claim.origin.Worktree, claim.origin.Prompt, claim.origin.Branch, claim.origin.Base, claim.origin.Objective,
		claim.binding, claim.input, approvedBody, profile,
		"attempt:" + claim.attempt, comparisonClaimEvent, claim.data}
	if !generation {
		var allowed bool
		err := s.q(ctx).QueryRowContext(ctx, "SELECT EXISTS(SELECT 1"+conditions+")", args...).Scan(&allowed)
		return allowed, err
	}
	values := []any{comparisonEventID(comparisonGenerationEvent, claim.attempt), formatTime(time.Now()),
		"attempt:" + claim.attempt, comparisonGenerationEvent, claim.data}
	result, err := s.q(ctx).ExecContext(ctx, "INSERT INTO events(id, ts, entity, event_type, data) SELECT ?, ?, ?, ?, ?"+
		conditions+" ON CONFLICT(id) DO NOTHING", append(values, args...)...)
	if err != nil {
		return false, err
	}
	note, err := result.RowsAffected()
	return note == 1, err
}

const comparisonResultEvent = "strict_comparison_result"

// CompleteStrictComparison stores one bounded authenticated result and settles
// the attempt/card together. It never examines an executor log or worktree.
func (s *Store) CompleteStrictComparison(ctx context.Context, claim StrictComparisonClaim, proof model.ProviderModelEvidence, answer string) error {
	if claim.attempt == "" || strings.TrimSpace(answer) == "" || len(answer) > model.ComparisonMaxAnswer || strings.ContainsRune(answer, '\x00') {
		return errors.New("strict comparison result is not eligible")
	}
	return s.WithTx(ctx, func(ctx context.Context) error {
		a, err := s.GetAttempt(ctx, claim.attempt)
		if err != nil {
			return err
		}
		c, err := s.GetCard(ctx, claim.card)
		if err != nil {
			return err
		}
		e, err := s.GetEvent(ctx, comparisonEventID(comparisonGenerationEvent, claim.attempt))
		if err != nil || e.Data == nil || string(e.Data) != claim.data || e.Type != comparisonGenerationEvent ||
			e.Entity != "attempt:"+claim.attempt || a.Outcome != "" || a.Exit != nil || !a.EndedAt.IsZero() ||
			(c.Status != model.CardReady && c.Status != model.CardRunning) {
			return errors.New("strict comparison result no longer owns an eligible generation")
		}
		origin, err := apiOrigin(a, c)
		if err != nil || origin != claim.origin {
			return errors.New("strict comparison execution origin changed")
		}
		a.RouteReason.ProviderModel = &proof
		if !a.VerifiedComparison(c) || a.RouteReason.Dispatch.ComparisonInputDigest != claim.input {
			return errors.New("strict comparison result has no matching provider identity")
		}
		data, err := json.Marshal(model.ComparisonResult{InputDigest: claim.input, Identity: proof, Answer: answer})
		if err != nil {
			return err
		}
		if err := s.AppendEvent(ctx, &model.Event{ID: comparisonEventID(comparisonResultEvent, a.ID), Entity: "attempt:" + a.ID, Type: comparisonResultEvent, Data: data}); err != nil {
			return err
		}
		a.EndedAt = time.Now().UTC()
		code := 0
		a.Exit, a.Outcome, a.ErrorClass = &code, model.OutcomePassed, model.ErrorPassed
		c.Status, c.WaitReason = model.CardDone, ""
		if err := s.UpdateAttempt(ctx, &a); err != nil {
			return err
		}
		return s.UpdateCard(ctx, &c)
	})
}

func (s *Store) GetStrictComparisonResult(ctx context.Context, attemptID string) (*model.ComparisonResult, error) {
	e, err := s.GetEvent(ctx, comparisonEventID(comparisonResultEvent, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result model.ComparisonResult
	if e.Type != comparisonResultEvent || e.Entity != "attempt:"+attemptID || json.Unmarshal(e.Data, &result) != nil {
		return nil, errors.New("strict comparison result is invalid")
	}
	return &result, nil
}
