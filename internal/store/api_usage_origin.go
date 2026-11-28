package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/OttoPrua/Cardex/internal/apiusage"
	"github.com/OttoPrua/Cardex/internal/model"
)

// apiExecutionOrigin is valid only when captured in the same transaction as
// its prelaunch claim. Stored event data includes only its digest, not paths,
// prompt text or contents, which are never read by API3.
type apiExecutionOrigin struct {
	Card, Objective, Attempt               string
	Channel, Model, Binding, Runner, Input string
	Worktree, Prompt, Branch, Base         string
	Index                                  int
	Strict                                 bool
}

func apiOrigin(a model.Attempt, c model.Card) (apiExecutionOrigin, error) {
	d := a.RouteReason.Dispatch
	if d == nil || a.ID == "" || a.Card == "" || a.Card != c.ID || a.Index < 0 || d.Runner != model.RunnerClaude ||
		d.CompatibleBindingID == "" || d.Channel != a.Route.Channel || d.Model != a.Route.Model ||
		d.StrictComparison != c.StrictComparison() || (!d.StrictComparison && d.ComparisonInputDigest != "") ||
		(c.Channel != "" && d.Channel != c.Channel) || (c.Model != "" && d.Model != c.Model) {
		return apiExecutionOrigin{}, apiusage.ErrEvidence
	}
	input := d.ComparisonInputDigest
	if d.StrictComparison {
		_, approved := c.ComparisonInput()
		if approved == "" || input != approved {
			return apiExecutionOrigin{}, apiusage.ErrEvidence
		}
	} else {
		// Raw stored bytes are the canonical ordinary body. Hash locally
		// without re-encoding (which might normalize malformed byte strings)
		// or storing user text. An edit to the same card cannot inherit the
		// proof for the original prompt.
		digest := sha256.Sum256([]byte("cardex-api3-ordinary-card-body-v1\x00" + c.Body))
		input = hex.EncodeToString(digest[:])
	}
	return apiExecutionOrigin{
		Card: c.ID, Objective: c.Objective, Attempt: a.ID, Channel: a.Route.Channel, Model: a.Route.Model,
		Binding: d.CompatibleBindingID, Runner: string(d.Runner), Input: input,
		Worktree: a.Worktree, Prompt: a.PromptPath, Branch: a.Branch, Base: a.BaseCommit,
		Index: a.Index, Strict: d.StrictComparison,
	}, nil
}

const apiSyntheticOriginEvent = "api3_synthetic_prelaunch_origin"

type apiSyntheticOriginRecord struct {
	Digest string `json:"synthetic_origin_sha256"`
}

func apiSyntheticOriginID(attempt string) string { return apiSyntheticOriginEvent + ":" + attempt }

// RecordAPI3SyntheticOrigin seals an ordinary fixture's assigned origin
// before its simulated launch. It has no runtime or CLI caller, cannot attest
// a real execution, and cannot amend a started attempt or reconstruct history.
func (s *Store) RecordAPI3SyntheticOrigin(ctx context.Context, attempt string) error {
	if !apiusage.ValidID(attempt) {
		return apiusage.ErrEvidence
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		a, c, err := s.loadMeteredAttempt(ctx, attempt)
		if err != nil {
			return err
		}
		if a.PID != 0 || !a.StartedAt.IsZero() || !a.EndedAt.IsZero() || a.Exit != nil || a.Outcome != "" ||
			a.ErrorClass != "" || a.ErrorExcerpt != "" || a.HeadCommit != "" || a.RouteReason.ProviderModel != nil ||
			a.Usage != (model.Usage{}) || c.StrictComparison() || a.StrictComparison() {
			return apiusage.ErrEvidence
		}
		var spent bool
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE entity=?)`, "attempt:"+a.ID).Scan(&spent); err != nil {
			return err
		} else if spent {
			return apiusage.ErrEvidence
		}
		origin, err := apiOrigin(a, c)
		if err != nil {
			return err
		}
		if err := s.checkMeteredChannel(ctx, origin.Channel, origin.Model, origin.Binding); err != nil {
			return err
		}
		hash, err := apiCustodyDigest(origin)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(apiSyntheticOriginRecord{Digest: hash})
		if err != nil {
			return err
		}
		return s.AppendEvent(ctx, &model.Event{ID: apiSyntheticOriginID(a.ID), Entity: "attempt:" + a.ID, Type: apiSyntheticOriginEvent, Data: raw})
	})
}

func (s *Store) meteredSyntheticOrigin(ctx context.Context, a model.Attempt, origin apiExecutionOrigin) (string, error) {
	event, err := s.GetEvent(ctx, apiSyntheticOriginID(a.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", apiusage.ErrEvidence
	}
	if err != nil {
		return "", err
	}
	if event.Type != apiSyntheticOriginEvent || event.Entity != "attempt:"+a.ID || event.TS.IsZero() || event.TS.After(a.StartedAt) {
		return "", apiusage.ErrEvidence
	}
	hash, err := apiCustodyDigest(origin)
	if err != nil {
		return "", err
	}
	expected, err := json.Marshal(apiSyntheticOriginRecord{Digest: hash})
	if err != nil || !bytes.Equal(event.Data, expected) {
		return "", apiusage.ErrEvidence
	}
	return apiCustodyDigest(event)
}

const apiSyntheticCompletionEvent = "api3_synthetic_successful_completion"

func apiSyntheticCompletionID(attempt string) string {
	return apiSyntheticCompletionEvent + ":" + attempt
}

type apiSyntheticCompletionRecord struct {
	Digest string `json:"synthetic_completion_sha256"`
}

func (s *Store) syntheticCompletionPin(ctx context.Context, a model.Attempt, c model.Card, origin apiExecutionOrigin) (string, error) {
	if origin.Strict || c.StrictComparison() || a.PID <= 0 || a.StartedAt.IsZero() || a.EndedAt.IsZero() || a.EndedAt.Before(a.StartedAt) ||
		a.Exit == nil || *a.Exit != 0 || a.Outcome != model.OutcomePassed || a.ErrorClass != model.ErrorPassed || a.RouteReason.ProviderModel != nil ||
		c.Status == model.CardCanceled || c.WaitReason == model.CompatibleRuntimeHold || c.WaitReason == StrictComparisonDisabled {
		return "", apiusage.ErrEvidence
	}
	fixture, err := s.meteredSyntheticOrigin(ctx, a, origin)
	if err != nil {
		return "", err
	}
	host, err := s.meteredHostWitness(ctx, a)
	if err != nil {
		return "", err
	}
	return apiCustodyDigest(struct {
		Origin, Witness, Started, Ended string
		PID, Exit                       int
	}{fixture, host, formatTime(a.StartedAt), formatTime(a.EndedAt), a.PID, *a.Exit})
}

// RecordAPI3SyntheticCompletion is only for the controlled fixture writer.
// It seals a successful closure against the previously captured origin; it
// cannot manufacture a prior capture for a legacy or reassigned attempt.
func (s *Store) RecordAPI3SyntheticCompletion(ctx context.Context, attempt string) error {
	if !apiusage.ValidID(attempt) {
		return apiusage.ErrEvidence
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		a, c, err := s.loadMeteredAttempt(ctx, attempt)
		if err != nil {
			return err
		}
		origin, err := apiOrigin(a, c)
		if err != nil {
			return err
		}
		hash, err := s.syntheticCompletionPin(ctx, a, c, origin)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(apiSyntheticCompletionRecord{Digest: hash})
		if err != nil {
			return err
		}
		return s.putAPIMeteringEvent(ctx, apiSyntheticCompletionID(a.ID), "attempt:"+a.ID, apiSyntheticCompletionEvent, raw)
	})
}

func (s *Store) meteredSyntheticCompletion(ctx context.Context, a model.Attempt, c model.Card, origin apiExecutionOrigin) (string, error) {
	event, err := s.GetEvent(ctx, apiSyntheticCompletionID(a.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", apiusage.ErrEvidence
	}
	if err != nil {
		return "", err
	}
	if event.Type != apiSyntheticCompletionEvent || event.Entity != "attempt:"+a.ID || event.TS.IsZero() || event.TS.Before(a.EndedAt) {
		return "", apiusage.ErrEvidence
	}
	hash, err := s.syntheticCompletionPin(ctx, a, c, origin)
	if err != nil {
		return "", err
	}
	expected, err := json.Marshal(apiSyntheticCompletionRecord{Digest: hash})
	if err != nil || !bytes.Equal(event.Data, expected) {
		return "", apiusage.ErrEvidence
	}
	return apiCustodyDigest(event)
}
