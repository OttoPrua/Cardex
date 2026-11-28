package board

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/apiusage"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestAPI3BoardShowsOnlyNumericMeteringAndKeepsLegacyShape(t *testing.T) {
	db, _ := seedCurrent(t)
	ctx := t.Context()
	before := do(t, http.MethodGet, "/api/cards/c-run")
	if before.Code != http.StatusOK || strings.Contains(before.Body.String(), "api_metering") {
		t.Fatal("legacy card detail gained fabricated metering")
	}
	channel, name, binding := "fixture-channel", "fixture-model", strings.Repeat("f", 64)
	ch := model.Channel{ID: channel, Runner: model.RunnerClaude, Health: model.HealthDegraded,
		EngineProfile: "anthropic-compatible:fixture@" + binding, Models: []string{name}}
	must(t, db.CreateChannel(ctx, &ch))
	exit := 0
	a := model.Attempt{ID: "meter-fixture", Card: "c-run", Index: 10,
		Route: model.Route{Channel: channel, Model: name}, RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{
			Runner: model.RunnerClaude, Channel: channel, Model: name, CompatibleBindingID: binding,
		}}}
	must(t, db.CreateAttempt(ctx, &a))
	must(t, db.RecordAPI3SyntheticOrigin(ctx, a.ID))
	a.StartedAt, a.PID, a.Exit = time.Now().UTC(), 4201, &exit
	a.EndedAt, a.Outcome, a.ErrorClass = a.StartedAt, model.OutcomePassed, model.ErrorPassed
	must(t, db.UpdateAttempt(ctx, &a))
	must(t, db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: "host_start", TS: a.StartedAt, Data: []byte(`{"runner":"claude"}`)}))
	must(t, db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + a.ID, Type: "host_finish", TS: a.EndedAt, Data: []byte(`{"exit":0}`)}))
	must(t, db.RecordAPI3SyntheticCompletion(ctx, a.ID))
	zero := int64(0)
	e := apiusage.Evidence{ID: a.ID, Operation: apiusage.Attempt, Channel: channel, Model: name, Binding: binding, RecordedAt: a.StartedAt,
		Metering: apiusage.Measurement{Source: apiusage.RecordedCLI, Counts: apiusage.Counts{Input: &zero}}}
	must(t, db.RecordOfflineAPIAttemptMetering(ctx, e))
	out := do(t, http.MethodGet, "/api/cards/c-run")
	if out.Code != http.StatusOK || strings.Contains(out.Body.String(), "currency_micros") {
		t.Fatal("ordinary board showed an amount or failed to render")
	}
	var data struct {
		Attempts []struct {
			ID          string
			APIMetering *apiusage.View `json:"api_metering"`
		}
	}
	must(t, json.Unmarshal(out.Body.Bytes(), &data))
	found := false
	for _, a := range data.Attempts {
		if a.ID != e.ID {
			if a.APIMetering != nil {
				t.Fatal("legacy attempts inherited another attempt's metering")
			}
			continue
		}
		m := a.APIMetering
		if m == nil || m.Metering.Source != apiusage.RecordedCLI || m.Total != nil || m.Metering.Counts.Input == nil ||
			*m.Metering.Counts.Input != 0 || m.Metering.Counts.CacheRead != nil ||
			m.LowerBound == nil || *m.LowerBound != 0 || m.EvidenceTrust != "unverified_transcription" {
			t.Fatal("unknown/zero and recorded provenance lost")
		}
		found = true
	}
	html := do(t, http.MethodGet, "/cards/c-run")
	if !found || html.Code != http.StatusOK || !strings.Contains(html.Body.String(), "兼容 API 用量证据（非账单）") ||
		strings.Contains(html.Body.String(), "currency_micros") {
		t.Fatal("HTML metering missing or included a public amount")
	}
}
