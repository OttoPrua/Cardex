package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

const syntheticStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"model\":\"fixture-model\",\"ignored\":\"unused\"}}\n\n"
const syntheticBlockStart = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
const syntheticBlockDelta = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"SYNTH_ANSWER\"}}\n\n"
const syntheticBlockStop = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
const syntheticMessageStop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestCompatibleStrictComparisonUsesActualProviderIdentity(t *testing.T) {
	upstream, _ := url.Parse("https://vendor.example.test/gateway")
	for _, fixture := range []struct {
		name, model, body, status string
		stream                    bool
		outCode, calls            int
	}{
		{"json-match", "fixture-model", `{"type":"message","model":"fixture-model","content":[{"type":"text","text":"SYNTH_ANSWER"}],"discard":"unused"}`, "verified", false, 200, 1},
		{"json-mismatch", "fixture-model", `{"type":"message","model":"different-model","content":[{"type":"text","text":"SYNTH_ANSWER"}]}`, "identity_mismatch", false, 502, 1},
		{"json-missing", "fixture-model", `{"type":"message","content":[{"type":"text","text":"SYNTH_ANSWER"}]}`, "identity_unknown", false, 502, 1},
		{"outbound-mismatch", "different-model", `{"type":"message","model":"fixture-model"}`, "request_model_mismatch", false, 409, 0},
		{"stream-match", "fixture-model", syntheticStart + syntheticBlockStart + syntheticBlockDelta + syntheticBlockStop + syntheticMessageStop, "verified", true, 200, 1},
		{"stream-incomplete", "fixture-model", "data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"model\":\"fixture-model\"}}\n\n", "identity_unknown", true, 502, 1},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			proof := &compatibleComparison{}
			proof.setup("fixture-model", "approved standalone question")
			calls := 0
			tpt := compatibleMockTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(req.Body)
				if err != nil || !strings.Contains(string(body), "approved standalone question") ||
					strings.Contains(string(body), "unapproved context") || strings.Contains(string(body), "system") {
					t.Fatal("relay did not restrict wire content to the approved question")
				}
				content := "application/json"
				if fixture.stream {
					content = "text/event-stream"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{content}}, Body: io.NopCloser(strings.NewReader(fixture.body))}, nil
			})
			handler := compatibleRelayHandler(upstream, fakeRelayHost, fakeRelayKey, canaryA, tpt, proof)
			in := relayRequest(http.MethodPost, "/v1/messages", fakeRelayKey)
			in.Body = io.NopCloser(strings.NewReader(`{"model":"` + fixture.model + `","system":"unapproved context","messages":[]}`))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, in)
			got := proof.snapshot()
			if w.Code != fixture.outCode || calls != fixture.calls || got.Status != fixture.status {
				t.Fatalf("status=%s HTTP=%d calls=%d", got.Status, w.Code, calls)
			}
			if got.Status != "verified" && strings.Contains(w.Body.String(), "SYNTH_ANSWER") {
				t.Fatal("mismatched/unverified answer reached the executor")
			}
			if got.Status == "verified" {
				if got.Source != model.ModelSourceProvider || got.Returned != "fixture-model" || proof.verifiedAnswer() != "SYNTH_ANSWER" ||
					strings.Contains(w.Body.String(), "unused") || parseProviderModel(w.Body.Bytes(), fixture.stream, "") != "fixture-model" ||
					parseProviderComparisonAnswer(w.Body.Bytes(), fixture.stream, "") != "SYNTH_ANSWER" {
					t.Fatal("verified evidence lost real model or provenance")
				}
				w = httptest.NewRecorder()
				handler.ServeHTTP(w, relayRequest(http.MethodPost, "/v1/messages", fakeRelayKey))
				if w.Code != 409 || calls != 1 || proof.snapshot().Status != "request_repeated" {
					t.Fatal("strict comparison retried a generation")
				}
			}
		})
	}
}

func TestCompatibleStrictComparisonRechecksTargetAndRefusesProviderTools(t *testing.T) {
	upstream, _ := url.Parse("https://vendor.example.test/gateway")
	for _, tc := range []struct {
		status, body string
		ready        func(context.Context, bool) bool
	}{
		{"target_unavailable", `{"model":"fixture-model"}`, func(context.Context, bool) bool { return false }},
		{"request_tools_refused", `{"model":"fixture-model","tools":[{"name":"browser"}]}`, func(context.Context, bool) bool { return true }},
		{"request_tools_refused", `{"model":"fixture-model","mcp_servers":[{"name":"external"}]}`, func(context.Context, bool) bool { return true }},
		{"request_tools_refused", `{"model":"fixture-model","container":"other"}`, func(context.Context, bool) bool { return true }},
	} {
		proof := &compatibleComparison{}
		proof.setup("fixture-model", "approved standalone question", tc.ready)
		transport := compatibleMockTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("unapproved strict request forwarded")
			return nil, nil
		})
		handler := compatibleRelayHandler(upstream, fakeRelayHost, fakeRelayKey, canaryA, transport, proof)
		in := relayRequest(http.MethodPost, "/v1/messages", fakeRelayKey)
		in.Body = io.NopCloser(strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, in)
		if w.Code != http.StatusConflict || proof.snapshot().Status != tc.status {
			t.Fatalf("unexpected refused response: HTTP %d, status %s", w.Code, proof.snapshot().Status)
		}
	}
}

func TestCompatibleStrictComparisonRejectsRotatedCredentialReference(t *testing.T) {
	ref := []byte(`{"protocol":"anthropic-compatible","base_url":"https://vendor.example.test","credential":{"source":"env","name":"CARDEX_SYNTH_COMPARISON"}}`)
	relay := &CompatibleRelay{secret: canaryA}
	t.Setenv("CARDEX_SYNTH_COMPARISON", canaryA)
	if !relay.CredentialCurrent(ref) {
		t.Fatal("synthetic unchanged credential rejected")
	}
	t.Setenv("CARDEX_SYNTH_COMPARISON", canaryB)
	if relay.CredentialCurrent(ref) {
		t.Fatal("stale relay credential was reauthorized")
	}
}

func TestCompatibleStrictComparisonStreamRejectsTextOutsideVerifiedSpan(t *testing.T) {
	valid := syntheticStart + syntheticBlockStart + syntheticBlockDelta + syntheticBlockStop + syntheticMessageStop
	for name, stream := range map[string]string{
		"prefixed text":    syntheticBlockDelta + valid,
		"trailing text":    valid + syntheticBlockDelta,
		"no open block":    syntheticStart + syntheticBlockDelta + syntheticMessageStop,
		"block not closed": syntheticStart + syntheticBlockStart + syntheticBlockDelta + syntheticMessageStop,
		"wrong index":      syntheticStart + syntheticBlockStart + strings.Replace(syntheticBlockDelta, `"index":0`, `"index":1`, 1) + syntheticBlockStop + syntheticMessageStop,
		"another message":  valid + valid,
		"lying event type": strings.Replace(valid, "event: content_block_delta", "event: ping", 1),
		"wrong text type":  strings.Replace(valid, `"text":"SYNTH_ANSWER"`, `"text":false`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			reported, answer := parseStrictComparisonResponse([]byte(stream), true, canaryA)
			if reported != "" || answer != "" {
				t.Fatal("invalid span produced acceptable provider proof")
			}
		})
	}
}
