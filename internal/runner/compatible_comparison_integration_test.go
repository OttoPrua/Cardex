package runner_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/apiusage"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/scorecard"
	"github.com/OttoPrua/Cardex/internal/store"
)

const comparisonFixtureSecret = "synthetic-comparison-fixture"
const comparisonFixtureKey = "synthetic-comparison-local-key"

type comparisonFixtureTransport func(*http.Request) (*http.Response, error)

func (f comparisonFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// External runner_test avoids a test dependency cycle with the typed ST2
// store, which depends on runner for its fixed Fake fixture contract.
func TestCompatibleSyntheticProviderProofSettlesOnlyIsolatedResult(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ch := model.Channel{ID: "strict-channel", Runner: model.RunnerClaude, Family: "fixture", Health: model.HealthOK,
		EngineProfile: "anthropic-compatible:fixture@" + strings.Repeat("a", 64), Models: []string{"fixture-model"}}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	c := model.Card{ID: "strict-card", Objective: "fixture-objective", Kind: model.KindResearch, Status: model.CardRunning, Title: "fixture title",
		Body: "specifically approved question?", Channel: ch.ID, Model: "fixture-model", Requires: []string{model.RequireStrictComparison}}
	if err := db.CreateCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	prompt, digest := c.ComparisonInput()
	a := model.Attempt{ID: "strict-attempt", Card: c.ID, Route: model.Route{Channel: c.Channel, Model: c.Model},
		Worktree: "comparison-fixture/worktree", PromptPath: "comparison-fixture/prompt", Branch: "fixture-branch", BaseCommit: "fixture-base",
		RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{Runner: model.RunnerClaude, Channel: c.Channel, Model: c.Model,
			StrictComparison: true, CompatibleBindingID: runner.CompatibleBindingID(ch.EngineProfile), ComparisonInputDigest: digest}}}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	claim, got, err := db.ClaimStrictComparison(ctx, a)
	if err != nil || !got {
		t.Fatalf("claim: %v %v", got, err)
	}
	if _, got, err := db.ClaimStrictComparison(ctx, a); err != nil || got {
		t.Fatalf("duplicate acquired claimant: %v %v", got, err)
	}
	altered := c
	altered.Body = "not approved"
	if err := db.UpdateCard(ctx, &altered); err != nil {
		t.Fatal(err)
	}
	if allowed, err := db.AuthorizeStrictComparisonRequest(ctx, claim, ch.EngineProfile, c.Body, true); err != nil || allowed {
		t.Fatalf("a different persisted question acquired the generation: %t %v", allowed, err)
	}
	if err := db.UpdateCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	// host.run claims before Spawn/StartedAt. Match that ordering against a
	// real synthetic local relay, not a fictitious earlier start instant.
	claimEvent, err := db.GetEvent(ctx, "strict_comparison_claim:"+a.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.PID, a.StartedAt = 4207, time.Now().UTC()
	if !claimEvent.TS.Before(a.StartedAt) {
		t.Fatal("synthetic host did not reproduce claim-before-start")
	}
	if err := db.UpdateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	auth := func(ctx context.Context, gen bool) bool {
		ok, err := db.AuthorizeStrictComparisonRequest(ctx, claim, ch.EngineProfile, c.Body, gen)
		if err != nil {
			t.Errorf("wire authorization: %v", err)
		}
		return ok
	}
	tptCalls := 0
	responseFixture := `{"type":"message","model":"fixture-model","content":[{"type":"text","text":"Bound answer."}],
		"usage":{"input_tokens":7,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},
		"unapproved_header":"` + comparisonFixtureSecret + `"}`
	upstream, _ := url.Parse("https://synthetic.example.test/private-path")
	fakeProvider := comparisonFixtureTransport(func(req *http.Request) (*http.Response, error) {
		tptCalls++
		raw, _ := io.ReadAll(req.Body)
		if req.Header.Get("Authorization") != "Bearer "+comparisonFixtureSecret || req.URL.String() != "https://synthetic.example.test/private-path/v1/messages" ||
			!strings.Contains(string(raw), "specifically approved question?") || strings.Contains(string(raw), "unapproved metadata") {
			t.Error("synthetic provider did not receive exactly the approved contract")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responseFixture))}, nil
	})
	send := func(proof *runner.CompatibleRelay, path string) (int, string) {
		t.Helper()
		srv := httptest.NewUnstartedServer(nil)
		srv.Config.Handler = runner.CompatibleComparisonHandlerForTest(upstream, srv.Listener.Addr().String(), comparisonFixtureKey, comparisonFixtureSecret, fakeProvider, proof)
		srv.Start()
		defer srv.Close()
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, strings.NewReader(`{"model":"fixture-model","system":"unapproved metadata"}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Api-Key", comparisonFixtureKey)
		resp, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		answer, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(answer)
	}
	proof := &runner.CompatibleRelay{}
	proof.RequireModelEvidence(c.Model, prompt, auth)
	if code, _ := send(proof, "/v1/messages/count_tokens"); code != 409 || tptCalls != 0 {
		t.Fatal("count_tokens was not blocked")
	}
	if code, out := send(proof, "/v1/messages?beta=true"); code != 200 || tptCalls != 1 || strings.Contains(out, comparisonFixtureSecret) || strings.Contains(out, "never forward raw metadata") ||
		strings.Contains(out, "unapproved_header") || strings.Contains(out, "usage") || !strings.Contains(out, "Bound answer.") {
		t.Fatal("verified synthetic request or redaction failed")
	}
	second := &runner.CompatibleRelay{}
	second.RequireModelEvidence(c.Model, prompt, auth)
	if code, _ := send(second, "/v1/messages"); code != 409 || tptCalls != 1 {
		t.Fatal("process replay reused the generation claim")
	}
	if err := db.CompleteStrictComparison(ctx, claim, proof.ModelEvidence(), proof.ComparisonAnswer()); err != nil {
		t.Fatal(err)
	}
	result, err := db.GetStrictComparisonResult(ctx, a.ID)
	if err != nil || result == nil || result.Answer != "Bound answer." || result.InputDigest != digest || result.Identity.Source != model.ModelSourceProvider {
		t.Fatalf("isolated result not recorded: %+v %v", result, err)
	}
	settled, err := db.GetCard(ctx, c.ID)
	if err != nil || settled.Status != model.CardDone {
		t.Fatalf("result didn't settle scheduler card: %v %v", settled.Status, err)
	}
	done, err := db.GetAttempt(ctx, a.ID)
	if err != nil || done.Outcome != model.OutcomePassed || done.Usage.Tokens != 0 {
		t.Fatalf("result entered ordinary billing/scoring: %s %v", done.Outcome, err)
	}
	meter, err := runner.ParseSyntheticMessagesUsage([]byte(responseFixture), c.Model)
	if err != nil {
		t.Fatal(err)
	}
	e := apiusage.Evidence{ID: done.ID, Operation: apiusage.Comparison, Channel: c.Channel, Model: c.Model,
		Binding: a.RouteReason.Dispatch.CompatibleBindingID, RecordedAt: done.StartedAt, Metering: meter}
	// Before API3 has written a receipt, copy the exact model, body and route
	// to a second card and move the settled row there. The original API2
	// origin must prevent that second card from inheriting the witness.
	rebound := c
	rebound.ID = "other-comparison-same-question"
	if err := db.CreateCard(ctx, &rebound); err != nil {
		t.Fatal(err)
	}
	moved := done
	moved.Card = rebound.ID
	if err := db.UpdateAttempt(ctx, &moved); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordOfflineAPIAttemptMetering(ctx, e); !errors.Is(err, apiusage.ErrEvidence) {
		t.Fatalf("pre-import rebound borrowed the original API2 result: %v", err)
	}
	changedPath := done
	changedPath.PromptPath += "-changed-after-exit"
	if err := db.UpdateAttempt(ctx, &changedPath); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordOfflineAPIAttemptMetering(ctx, e); !errors.Is(err, apiusage.ErrEvidence) {
		t.Fatalf("pre-import input custody change borrowed the original result: %v", err)
	}
	if err := db.UpdateAttempt(ctx, &done); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordOfflineAPIAttemptMetering(ctx, e); err != nil {
		t.Fatalf("genuine synthetic result in actual relay lifecycle stayed unknown: %v", err)
	}
	storedMeter, err := db.GetAPIAttemptMetering(ctx, done)
	if err != nil || storedMeter == nil || storedMeter.View().Total == nil || *storedMeter.View().Total != 10 ||
		storedMeter.View().EvidenceTrust != "synthetic_only" {
		t.Fatalf("valid exact synthetic closure lost metering or claimed trust: %+v %v", storedMeter, err)
	}
	rows, err := scorecard.Compute(ctx, home)
	if err != nil || len(rows) != 0 {
		t.Fatalf("strict result leaked into normal ranking input: %+v %v", rows, err)
	}
}
