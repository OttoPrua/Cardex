package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/apiusage"
	"github.com/OttoPrua/Cardex/internal/model"
)

func meterInt(n int64) *int64 { return &n }
func meterExit(n int) *int    { return &n }

func seedAPI3HostWitness(t *testing.T, db *Store, a model.Attempt) {
	t.Helper()
	for _, e := range []model.Event{
		{Entity: "attempt:" + a.ID, Type: "host_start", TS: a.StartedAt, Data: []byte(`{"runner":"claude"}`)},
		{Entity: "attempt:" + a.ID, Type: "host_finish", TS: a.EndedAt, Data: []byte(`{"exit":0}`)},
	} {
		if err := db.AppendEvent(t.Context(), &e); err != nil {
			t.Fatal(err)
		}
	}
}

// prepareAPI3OfflineFixture mirrors a low-level fixture writer before and
// after execution. No genuine file, credential or process is inspected.
func prepareAPI3OfflineFixture(t *testing.T, db *Store, a model.Attempt, complete bool) model.Attempt {
	t.Helper()
	ctx := t.Context()
	prepared := a
	prepared.StartedAt, prepared.EndedAt = time.Time{}, time.Time{}
	prepared.PID, prepared.Exit, prepared.Outcome, prepared.ErrorClass, prepared.Usage = 0, nil, "", "", model.Usage{}
	if err := db.CreateAttempt(ctx, &prepared); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordAPI3SyntheticOrigin(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	a.StartedAt = time.Now().UTC()
	a.EndedAt = a.StartedAt
	if err := db.UpdateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if complete {
		seedAPI3HostWitness(t, db, a)
		if err := db.RecordAPI3SyntheticCompletion(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func TestAPI3ImmutableAttemptsPricesAndProbeSeparation(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()
	at := time.Now().UTC().Add(-time.Minute)
	card := model.Card{ID: "usage-fixture", Kind: model.KindResearch, Status: model.CardDone, Title: "fixture"}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	const name = "shared-fixture-model"
	makeChannel := func(id, binder string) string {
		t.Helper()
		binding := strings.Repeat(binder, 64)
		ch := model.Channel{ID: id, Runner: model.RunnerClaude, Health: model.HealthDegraded, Family: "fixture",
			EngineProfile: "anthropic-compatible:fixture@" + binding, Models: []string{name}}
		if err := db.CreateChannel(ctx, &ch); err != nil {
			t.Fatal(err)
		}
		return binding
	}
	bindA, bindB := makeChannel("fixture-a", "a"), makeChannel("fixture-b", "b")
	usage := apiusage.Measurement{Source: apiusage.FixtureResponse, ServiceTier: "standard", Speed: "standard",
		Counts: apiusage.Counts{Input: meterInt(10), Output: meterInt(20), CacheRead: meterInt(0), CacheWrite: meterInt(0),
			WebSearch: meterInt(0), WebFetch: meterInt(0)}}
	prepare := func(id, channel, binding string, index int) (model.Attempt, apiusage.Evidence) {
		t.Helper()
		a := model.Attempt{ID: id, Card: card.ID, Index: index, StartedAt: at, EndedAt: at.Add(time.Second),
			PID: 4200 + index, Exit: meterExit(0), ErrorClass: model.ErrorPassed,
			Outcome: model.OutcomePassed, Usage: model.Usage{Tokens: 99, Confidence: 1, Source: "legacy"},
			Route: model.Route{Channel: channel, Model: name},
			RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{Channel: channel, Model: name,
				Runner: model.RunnerClaude, CompatibleBindingID: binding}}}
		a = prepareAPI3OfflineFixture(t, db, a, true)
		return a, apiusage.Evidence{ID: id, Operation: apiusage.Attempt, Channel: channel, Model: name,
			Binding: binding, RecordedAt: a.StartedAt, Metering: usage}
	}
	a, refA := prepare("receipt-a", "fixture-a", bindA, 0)
	b, refB := prepare("receipt-b", "fixture-b", bindB, 1)
	makePrice := func(e apiusage.Evidence, currency string, input int64) apiusage.PriceSnapshot {
		return apiusage.PriceSnapshot{Source: apiusage.SyntheticPrice, Channel: e.Channel, Model: e.Model, Binding: e.Binding,
			Version: "fixture-v1", Currency: currency, ServiceTier: "standard", Speed: "standard",
			EffectiveFrom: at, EffectiveUntil: at.Add(time.Hour), Rates: apiusage.Rates{Input: meterInt(input), Output: meterInt(1_000_000),
				CacheRead: meterInt(0), CacheWrite: meterInt(0), WebSearch: meterInt(0), WebFetch: meterInt(0)}}
	}
	p, q := makePrice(refA, "USD", 1_000_000), makePrice(refB, "EUR", 2_000_000)
	for i, pair := range []struct {
		a model.Attempt
		e apiusage.Evidence
		p apiusage.PriceSnapshot
	}{{a, refA, p}, {b, refB, q}} {
		hash, err := db.RecordAPIPriceFixture(ctx, pair.p)
		if err != nil {
			t.Fatal(err)
		}
		if duplicate, err := db.RecordAPIPriceFixture(ctx, pair.p); err != nil || duplicate != hash {
			t.Fatal("identical fixture price replay failed")
		}
		e := pair.e
		e.PriceVersion, e.Price = pair.p.Version, hash
		if err := db.RecordOfflineAPIAttemptMetering(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := db.RecordOfflineAPIAttemptMetering(ctx, e); err != nil {
			t.Fatal("identical fixture receipt replay failed")
		}
		p2 := pair.p
		p2.Rates.Input = meterInt(7)
		if _, err := db.RecordAPIPriceFixture(ctx, p2); !errors.Is(err, apiusage.ErrConflict) {
			t.Fatal("existing version was silently replaced")
		}
		changed := e
		changed.Metering.Counts.Input = meterInt(100)
		if err := db.RecordOfflineAPIAttemptMetering(ctx, changed); !errors.Is(err, apiusage.ErrConflict) {
			t.Fatal("existing attempt receipt was silently replaced")
		}
		tampered := e
		tampered.Binding = strings.Repeat("c", 64)
		if err := db.RecordOfflineAPIAttemptMetering(ctx, tampered); !errors.Is(err, apiusage.ErrEvidence) {
			t.Fatal("a receipt for another binding was accepted")
		}
		stored, err := db.GetAPIAttemptMetering(ctx, pair.a)
		if err != nil || stored == nil || stored.Price != hash {
			t.Fatalf("frozen attempt price was not read: %+v %v", stored, err)
		}
		rates, err := db.GetAPIPriceFixture(ctx, e.Channel, e.Model, e.PriceVersion)
		if err != nil {
			t.Fatal(err)
		}
		quote := apiusage.Quote(*stored, rates, true)
		if quote.Amount == nil || *quote.Amount != int64(30+i*10) || quote.Currency != pair.p.Currency {
			t.Fatalf("same model retry used another channel price: %+v", quote)
		}
		probe := e
		probe.Operation = apiusage.Probe
		if err := db.RecordAPIProbeFixture(ctx, probe); err != nil {
			t.Fatal(err)
		}
		probeResult, err := db.GetAPIProbeMetering(ctx, e.ID)
		if err != nil || probeResult == nil || probeResult.Operation != apiusage.Probe {
			t.Fatal("independent probe not isolated from attempt receipt")
		}
		stored, err = db.GetAPIAttemptMetering(ctx, pair.a)
		if err != nil || stored == nil || stored.Operation != apiusage.Attempt {
			t.Fatal("probe changed ordinary attempt")
		}
	}
	summary, err := db.SummarizeCardUsage(ctx, card.ID)
	if err != nil || summary.Total == nil || *summary.Total != 198 || len(summary.Attempts) != 2 {
		t.Fatalf("legacy quota/token evidence changed: %+v %v", summary, err)
	}
}

func TestAPI3LegacyAndFalseComparisonReceiptsStayUnknown(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	a := model.Attempt{ID: "legacy-fixture", Card: "other-card", StartedAt: at, EndedAt: at, Outcome: model.OutcomePassed,
		Route: model.Route{Channel: "fixture-a", Model: "fixture-model"}}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	e := apiusage.Evidence{ID: a.ID, Operation: apiusage.Attempt, Channel: a.Route.Channel, Model: a.Route.Model, RecordedAt: at,
		Binding: strings.Repeat("a", 64), Metering: apiusage.Measurement{Source: apiusage.RecordedCLI}}
	if err := db.RecordOfflineAPIAttemptMetering(ctx, e); !errors.Is(err, apiusage.ErrEvidence) {
		t.Fatal("legacy attempt was retroactively priced or re-attributed")
	}
	got, err := db.GetAPIAttemptMetering(ctx, a)
	if err != nil || got != nil {
		t.Fatal("missing receipt didn't remain unknown")
	}
}

func TestAPI3ComparisonMeterRequiresIsolatedResultReceipt(t *testing.T) {
	ctx := t.Context()
	db := openDB(t)
	at := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	channel, name, binding := "fixture-channel", "fixture-model", strings.Repeat("b", 64)
	ch := model.Channel{ID: channel, Runner: model.RunnerClaude, Health: model.HealthOK,
		EngineProfile: "anthropic-compatible:fixture@" + binding, Models: []string{name}}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	c := model.Card{ID: "fixture-compare", Kind: model.KindResearch, Title: "fixture", Body: "synthetic only",
		Status: model.CardRunning, Channel: channel, Model: name, Requires: []string{model.RequireStrictComparison}}
	if err := db.CreateCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	_, input := c.ComparisonInput()
	d := &model.RouteDispatch{Channel: channel, Model: name, Runner: model.RunnerClaude, CompatibleBindingID: binding,
		StrictComparison: true, ComparisonInputDigest: input}
	proof := model.ProviderModelEvidence{Requested: name, Returned: name, Source: model.ModelSourceProvider, Status: "verified"}
	phony := model.Attempt{ID: "route-metadata-only", Card: c.ID, StartedAt: at, EndedAt: at.Add(time.Second),
		Exit: meterExit(0), ErrorClass: model.ErrorPassed, Outcome: model.OutcomePassed,
		Route: model.Route{Channel: channel, Model: name}, RouteReason: model.RouteReason{Dispatch: d, ProviderModel: &proof}}
	if err := db.CreateAttempt(ctx, &phony); err != nil {
		t.Fatal(err)
	}
	e := apiusage.Evidence{ID: phony.ID, Operation: apiusage.Comparison, Channel: channel, Model: name,
		Binding: binding, RecordedAt: at, Metering: apiusage.Measurement{Source: apiusage.FixtureResponse}}
	if err := db.RecordOfflineAPIAttemptMetering(ctx, e); !errors.Is(err, apiusage.ErrEvidence) {
		t.Fatalf("route metadata imitated an isolated result: %v", err)
	}
	// A hand-staged result without a tied claim and generation must also
	// stay unknown even when the answer and identity happen to match.
	if err := db.AppendEvent(ctx, &model.Event{ID: comparisonEventID(comparisonResultEvent, phony.ID),
		Entity: "attempt:" + phony.ID, Type: comparisonResultEvent, TS: at,
		Data: []byte(`{"input_digest":"` + input + `","identity":{"requested_model":"fixture-model","returned_model":"fixture-model","source":"provider_response","status":"verified"},"answer":"unowned"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordOfflineAPIAttemptMetering(ctx, e); !errors.Is(err, apiusage.ErrEvidence) {
		t.Fatalf("result unattached to one-shot ownership was accepted: %v", err)
	}

	a := model.Attempt{ID: "receipt-backed-comparison", Card: c.ID, Index: 1,
		Worktree: "isolated-fixture/worktree", PromptPath: "isolated-fixture/prepared-prompt", Branch: "fixture-branch", BaseCommit: "fixture-base",
		Route: model.Route{Channel: channel, Model: name}, RouteReason: model.RouteReason{Dispatch: d}}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := db.ClaimStrictComparison(ctx, a)
	if err != nil || !ok {
		t.Fatalf("fixture claim: %t %v", ok, err)
	}
	claimed, err := db.GetEvent(ctx, comparisonEventID(comparisonClaimEvent, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	// Same ordering as host.run: acquire one-shot custody, then set start
	// and PID, and only then send an admitted generation.
	a.StartedAt, a.PID = claimed.TS.Add(time.Nanosecond), 4050
	e.RecordedAt = a.StartedAt
	if err := db.UpdateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	original := a
	for _, change := range []func(*model.Attempt){
		func(a *model.Attempt) { a.PromptPath += "-changed" },
		func(a *model.Attempt) { a.Worktree += "-changed" },
		func(a *model.Attempt) { a.Branch += "-changed" },
	} {
		changed := original
		change(&changed)
		if err := db.UpdateAttempt(ctx, &changed); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.AuthorizeStrictComparisonRequest(ctx, claim, ch.EngineProfile, c.Body, true); err != nil || ok {
			t.Fatalf("changed execution input custody authorized generation: %t %v", ok, err)
		}
	}
	if err := db.UpdateAttempt(ctx, &original); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.AuthorizeStrictComparisonRequest(ctx, claim, ch.EngineProfile, c.Body, true); err != nil || !ok {
		t.Fatalf("fixture generation receipt: %t %v", ok, err)
	}
	// The fixture writes typed durable proof and does not call an endpoint.
	if err := db.CompleteStrictComparison(ctx, claim, proof, "synthetic bounded answer"); err != nil {
		t.Fatal(err)
	}
	e.ID = a.ID
	if err := db.RecordOfflineAPIAttemptMetering(ctx, e); err != nil {
		t.Fatal(err)
	}
	a, err = db.GetAttempt(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAPIAttemptMetering(ctx, a)
	if err != nil || got == nil || got.Operation != apiusage.Comparison {
		t.Fatal("the isolated metering receipt did not survive readback")
	}
}

func TestAPI3OfflineReceiptFailsClosedForStaleOrReboundCustody(t *testing.T) {
	for _, mode := range []string{"stale-card-strict", "stale-attempt-strict", "both-strict-without-receipt",
		"no-exit", "nonzero-exit", "held", "lost", "no-wire-event", "changed-pid", "changed-terminal-time",
		"rebound-attempt-card", "rebound-card-channel", "rebound-channel-binding"} {
		t.Run(mode, func(t *testing.T) {
			db, ctx := openDB(t), t.Context()
			at := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
			c := model.Card{ID: "custody-fixture", Kind: model.KindResearch, Status: model.CardDone,
				Title: "fixture", Body: "isolated example", Channel: "fixture-channel", Model: "fixture-model"}
			if err := db.CreateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			binding := strings.Repeat("d", 64)
			ch := model.Channel{ID: c.Channel, Runner: model.RunnerClaude, Health: model.HealthDegraded,
				EngineProfile: "anthropic-compatible:fixture@" + binding, Models: []string{c.Model}}
			if err := db.CreateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			a := model.Attempt{ID: "custody-attempt", Card: c.ID, PID: 4242, Index: 2, StartedAt: at, EndedAt: at.Add(time.Second),
				Exit: meterExit(0), Outcome: model.OutcomePassed, ErrorClass: model.ErrorPassed, Route: model.Route{Channel: c.Channel, Model: c.Model},
				RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{Channel: c.Channel, Model: c.Model,
					Runner: model.RunnerClaude, CompatibleBindingID: binding}}}
			a = prepareAPI3OfflineFixture(t, db, a, mode != "lost")
			e := apiusage.Evidence{ID: a.ID, Operation: apiusage.Attempt, Channel: ch.ID, Model: c.Model, Binding: binding,
				RecordedAt: a.StartedAt, Metering: apiusage.Measurement{Source: apiusage.RecordedCLI}}
			if mode == "lost" {
				// A permissive-looking terminal row cannot supply a lost
				// session's missing immutable host finish event.
				if err := db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: "host_start",
					TS: a.StartedAt, Data: []byte(`{"runner":"claude"}`)}); err != nil {
					t.Fatal(err)
				}
				if err := db.RecordOfflineAPIAttemptMetering(ctx, e); !errors.Is(err, apiusage.ErrEvidence) {
					t.Fatalf("lost executor was treated as sealed: %v", err)
				}
				return
			}
			if err := db.RecordOfflineAPIAttemptMetering(ctx, e); err != nil {
				t.Fatalf("initial offline receipt: %v", err)
			}
			original, err := db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "stale-card-strict":
				c.Requires = []string{model.RequireStrictComparison}
			case "stale-attempt-strict":
				a.RouteReason.Dispatch.StrictComparison = true
			case "both-strict-without-receipt":
				c.Requires = []string{model.RequireStrictComparison}
				a.RouteReason.Dispatch.StrictComparison = true
				_, a.RouteReason.Dispatch.ComparisonInputDigest = c.ComparisonInput()
			case "no-exit":
				a.Exit = nil
			case "nonzero-exit":
				a.Exit = meterExit(1)
			case "held":
				c.Status, c.WaitReason = model.CardNeedsInput, model.CompatibleRuntimeHold
			case "no-wire-event":
				if err := db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: EventComparisonRefusedNoWire}); err != nil {
					t.Fatal(err)
				}
			case "changed-pid":
				a.PID++
			case "changed-terminal-time":
				a.EndedAt = a.EndedAt.Add(time.Millisecond)
			case "rebound-attempt-card":
				other := c
				other.ID = "other-custody-fixture"
				if err := db.CreateCard(ctx, &other); err != nil {
					t.Fatal(err)
				}
				a.Card = other.ID
			case "rebound-card-channel":
				c.Channel = "other-fixture-channel"
			case "rebound-channel-binding":
				ch.EngineProfile = "anthropic-compatible:changed@" + strings.Repeat("e", 64)
				if err := db.UpdateChannel(ctx, &ch); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.UpdateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			fresh, err := db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, snapshot := range []model.Attempt{original, fresh} {
				if got, err := db.GetAPIAttemptMetering(ctx, snapshot); got != nil || !errors.Is(err, apiusage.ErrEvidence) {
					t.Fatalf("stale or rebound custody read as measured: %+v %v", got, err)
				}
			}
			// Rewrites must never replace the original pinned receipt; a
			// new closure could be eligible, but its differing pin conflicts.
			if err := db.RecordOfflineAPIAttemptMetering(ctx, e); !errors.Is(err, apiusage.ErrEvidence) && !errors.Is(err, apiusage.ErrConflict) {
				t.Fatalf("stale or rebound custody overwrote immutable evidence: %v", err)
			}
		})
	}
}

func TestAPI3UnrecognizedOrUnpinnedReceiptRemainsUnknown(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()
	for _, v := range []struct{ id, schema string }{
		{"not-pinned", apiUsageReceiptSchema}, {"future-version", "offline-api-metering-v99"},
	} {
		raw, err := (apiusage.Evidence{ID: v.id, Operation: apiusage.Attempt, Channel: "test-channel", Model: "test-model",
			Binding: strings.Repeat("a", 64), RecordedAt: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC),
			Metering: apiusage.Measurement{Source: apiusage.FixtureResponse}}).Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if err := db.appendOfflineMetering(ctx, v.id, false, storedAPIMetering{Schema: v.schema, Evidence: raw}); err != nil {
			t.Fatal(err)
		}
		if got, err := db.GetAPIAttemptMetering(ctx, model.Attempt{ID: v.id}); got != nil || !errors.Is(err, apiusage.ErrEvidence) {
			t.Fatalf("unpinned or unrecognized evidence was accepted: %+v %v", got, err)
		}
	}
}

func TestAPI3PreImportRebindAndUnverifiableHistoryStayUnknown(t *testing.T) {
	for _, mode := range []string{"legacy-history", "same-model-same-body-other-card", "same-card-body-revised", "worktree", "prompt", "branch", "base"} {
		t.Run(mode, func(t *testing.T) {
			db, ctx := openDB(t), t.Context()
			channel, name, binding := "fixture-a", "same-model", strings.Repeat("b", 64)
			ch := model.Channel{ID: channel, Runner: model.RunnerClaude, Health: model.HealthDegraded, EngineProfile: "anthropic-compatible:f@" + binding, Models: []string{name}}
			if err := db.CreateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			c := model.Card{ID: "original-card", Kind: model.KindResearch, Status: model.CardDone, Channel: channel,
				Model: name, Title: "same", Body: "same body"}
			if err := db.CreateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
			clone := c
			clone.ID = "other-card"
			if err := db.CreateCard(ctx, &clone); err != nil {
				t.Fatal(err)
			}
			a := model.Attempt{ID: "fixture-attempt", Card: c.ID, PID: 4421, Exit: meterExit(0),
				Outcome: model.OutcomePassed, ErrorClass: model.ErrorPassed,
				Route: model.Route{Channel: channel, Model: name}, Worktree: "fake/path", PromptPath: "fake/input", Branch: "fake-branch", BaseCommit: "fake-base",
				RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{Channel: channel, Model: name,
					Runner: model.RunnerClaude, CompatibleBindingID: binding}}}
			if mode == "legacy-history" {
				a.StartedAt = time.Now().UTC()
				a.EndedAt = a.StartedAt
				if err := db.CreateAttempt(ctx, &a); err != nil {
					t.Fatal(err)
				}
				seedAPI3HostWitness(t, db, a)
				if err := db.RecordAPI3SyntheticOrigin(ctx, a.ID); !errors.Is(err, apiusage.ErrEvidence) {
					t.Fatalf("historical execution obtained a retroactive origin: %v", err)
				}
				if err := db.RecordAPI3SyntheticCompletion(ctx, a.ID); !errors.Is(err, apiusage.ErrEvidence) {
					t.Fatalf("historical execution obtained a retroactive completion: %v", err)
				}
			} else {
				a = prepareAPI3OfflineFixture(t, db, a, true)
			}
			switch mode {
			case "same-model-same-body-other-card":
				a.Card = clone.ID
			case "same-card-body-revised":
				revised := c
				revised.Body += " revised after the original execution"
				if err := db.UpdateCard(ctx, &revised); err != nil {
					t.Fatal(err)
				}
			case "worktree":
				a.Worktree += "-rebound"
			case "prompt":
				a.PromptPath += "-rebound"
			case "branch":
				a.Branch += "-rebound"
			case "base":
				a.BaseCommit += "-rebound"
			}
			if err := db.UpdateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			e := apiusage.Evidence{ID: a.ID, Operation: apiusage.Attempt, Channel: channel, Binding: binding, Model: name,
				RecordedAt: a.StartedAt, Metering: apiusage.Measurement{Source: apiusage.RecordedCLI}}
			if err := db.RecordOfflineAPIAttemptMetering(ctx, e); !errors.Is(err, apiusage.ErrEvidence) {
				t.Fatalf("a first import legitimated unverifiable or rebound history: %v", err)
			}
			if got, err := db.GetAPIAttemptMetering(ctx, a); got != nil || err != nil {
				t.Fatalf("missing execution evidence should remain absent, not zero: %+v %v", got, err)
			}
			if mode == "same-card-body-revised" {
				if err := db.UpdateCard(ctx, &c); err != nil {
					t.Fatal(err)
				}
				if err := db.RecordOfflineAPIAttemptMetering(ctx, e); err != nil {
					t.Fatalf("unchanged original body did not match its own fixture: %v", err)
				}
				got, err := db.GetAPIAttemptMetering(ctx, a)
				if err != nil || got == nil || got.View().EvidenceTrust != "unverified_transcription" {
					t.Fatalf("restored original fixture lost its explicitly unverified metering: %+v %v", got, err)
				}
			}
		})
	}
}
