package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestCompatibleProbeValidatesStagedBindingAndSendsOneFixedRequest(t *testing.T) {
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("SSL_CERT_DIR", "")
	t.Setenv("CARDEX_SYNTH_PROBE", canaryA)
	home := t.TempDir()
	write := func(host string) {
		t.Helper()
		config := `{"engines":{"synthetic":{"protocol":"anthropic-compatible","base_url":"https://` + host + `/gateway","default_model":"fixture-model","credential":{"source":"env","name":"CARDEX_SYNTH_PROBE"}}}}`
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("first.example.test")
	ch, err := ValidateChannelBinding(home, model.Channel{ID: "fixture", Runner: model.RunnerClaude,
		EngineProfile: "synthetic", Family: "synthetic", Health: model.HealthDegraded}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, body, status, returned string
		code                         int
		fail                         error
	}{
		{"success", `{"type":"message","model":"fixture-model","content":[{"type":"text","text":"OK"}]}`, "ok", "fixture-model", 200, nil},
		{"mismatch", `{"type":"message","model":"another-model"}`, "model_mismatch", "another-model", 200, nil},
		{"unknown", `{"type":"message","content":[{"model":"fixture-model"}]}`, "model_unknown", "unknown", 200, nil},
		{"identity-duplicate", `{"type":"message","model":"another-model","model":"fixture-model"}`, "model_unknown", "unknown", 200, nil},
		{"auth", canaryA, "auth_failed", "unknown", 401, nil},
		{"unavailable", `{"type":"error","error":{"type":"invalid_request_error","code":"model_not_found","message":"` + canaryA + `"}}`, "model_unavailable", "unknown", 404, nil},
		{"endpoint-not-found", `{"type":"error","error":{"type":"not_found_error"}}`, "provider_error", "unknown", 404, nil},
		{"timeout", "", "timeout", "unknown", 0, context.DeadlineExceeded},
		{"redirect", canaryA, "redirect_refused", "unknown", 307, nil},
		{"transport", "", "transport_error", "unknown", 0, errors.New(canaryA)},
		{"identity-secret", `{"type":"message","model":"` + canaryA + `"}`, "model_unknown", "unknown", 200, nil},
		{"too-large", strings.Repeat("x", probeMaxResponse+1), "response_invalid", "unknown", 200, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			tpt := compatibleMockTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				deadline, hasDeadline := req.Context().Deadline()
				if !hasDeadline || deadline.IsZero() || req.Method != http.MethodPost ||
					req.URL.String() != "https://first.example.test/gateway/v1/messages" ||
					req.Header.Get("Authorization") != "Bearer "+canaryA || req.GetBody != nil {
					t.Fatal("probe request was not bounded and bound to selected origin")
				}
				body, _ := io.ReadAll(req.Body)
				if string(body) != `{"model":"fixture-model","max_tokens":8,"stream":false,"messages":[{"role":"user","content":"Reply with OK."}]}` {
					t.Fatal("probe used anything other than the fixed public input")
				}
				if tc.fail != nil {
					return nil, tc.fail
				}
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(tc.body)),
					Header: http.Header{"Location": []string{"https://elsewhere.example.test/" + canaryA}}}, nil
			})
			got := probeCompatible(context.Background(), home, ch, "fixture-model", nil, tpt)
			safe, _ := json.Marshal(got)
			if calls != 1 || got.Endpoint != "https://first.example.test" || !got.Attempted || got.Status != tc.status || got.ReturnedModel != tc.returned ||
				strings.Contains(string(safe), canaryA) || strings.Contains(string(safe), "elsewhere") {
				t.Fatalf("unexpected sanitized receipt or request count: status=%s model=%s calls=%d", got.Status, got.ReturnedModel, calls)
			}
			if (got.ModelSource == model.ModelSourceProvider) != (tc.returned != "unknown") {
				t.Fatal("misstated model identity provenance")
			}
		})
	}
	// Error receipts may expose the selected origin, never the staged path.
	t.Setenv("SSL_CERT_FILE", "synthetic-unsupported-trust-override")
	tpt := compatibleMockTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatal("stale binding or unoffered model contacted upstream")
		return nil, nil
	})
	if got := probeCompatible(context.Background(), home, ch, "fixture-model", nil, tpt); got.Attempted || got.Endpoint != "https://first.example.test" {
		t.Fatalf("TLS refusal exposed a non-origin field: %+v", got)
	}
	t.Setenv("SSL_CERT_FILE", "")
	write("changed.example.test")
	for _, requested := range []string{"fixture-model", "another-model"} {
		got := probeCompatible(context.Background(), home, ch, requested, nil, tpt)
		if got.Attempted || got.Status != "config_invalid" {
			t.Fatal("changed binding was not rejected before request")
		}
	}
}

func TestCompatiblePublicPaidProbeHasNoTransport(t *testing.T) {
	got := ProbeCompatible(context.Background(), t.TempDir(), model.Channel{ID: "fixture", Runner: model.RunnerClaude}, "fixture-model", nil)
	if got.Status != "approval_unavailable" || got.Attempted {
		t.Fatalf("public CLI path enabled a paid transport: %+v", got)
	}
}
