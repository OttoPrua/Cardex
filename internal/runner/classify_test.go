package runner

import (
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestClassifyAttemptCodexTLSFailure(t *testing.T) {
	out := []byte(`{"type":"error","message":"Reconnecting... 5/5 (stream disconnected before completion: tls handshake eof)"}
{"type":"error","message":"{\"detail\":\"Bad Request\"}"}
{"type":"turn.failed","error":{"message":"{\"detail\":\"Bad Request\"}"}}
`)
	if got := ClassifyAttempt(1, out); got != model.ErrorTransport {
		t.Fatalf("class=%q want transport", got)
	}
}

func TestClassifyAttemptReconnectThenSuccessIsUnclassified(t *testing.T) {
	out := []byte(`{"type":"error","message":"Reconnecting... 1/5 (stream disconnected before completion: tls handshake eof)"}
{"type":"turn.completed"}
`)
	if got := ClassifyAttempt(0, out); got != "" {
		t.Fatalf("class=%q want empty", got)
	}
}

func TestClassifyOutputPeerCertificate(t *testing.T) {
	msg := `failed to connect to websocket: IO error: invalid peer certificate: certificate not valid for name "chatgpt.com"`
	if got := ClassifyOutput(msg); got != model.ErrorTransport {
		t.Fatalf("class=%q", got)
	}
}

func TestClassifyRateLimitRetriesNotQuota(t *testing.T) {
	cases := map[string]model.ErrorClass{
		"HTTP 429 Too Many Requests":                         model.ErrorTransport,
		"error: rate limit reached for requests":             model.ErrorTransport,
		"concurrency limit exceeded, retry later":            model.ErrorTransport,
		"You've hit your usage limit. Your limit will reset": model.ErrorQuotaHit,
		"429: usage limit reached, resets at 5pm":            model.ErrorQuotaHit,
	}
	for text, want := range cases {
		if got := ClassifyOutput(text); got != want {
			t.Errorf("ClassifyOutput(%q)=%q want %q", text, got, want)
		}
	}
}

func TestClassifyOutputUnavailable(t *testing.T) {
	for _, text := range []string{
		`invalid model selection (--model "gemini-3.7-flash-high" --effort ""): model gemini-3.7-flash-high is not recognized as a known model or custom model in settings`,
		`FAILED_PRECONDITION (code 400): User location is not supported for the API use.`,
		`This model is not available in your country`,
	} {
		if got := ClassifyOutput(text); got != model.ErrorUnavailable {
			t.Errorf("%q: got %q", text, got)
		}
	}
	// A transient provider outage stays transport and keeps retrying.
	if got := ClassifyOutput("provider unavailable, model unavailable"); got != model.ErrorTransport {
		t.Errorf("outage classified %q", got)
	}
}
