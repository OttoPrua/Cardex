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
