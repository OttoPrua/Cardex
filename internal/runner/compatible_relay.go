package runner

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

const maxCompatibleRequest = 32 << 20

// CompatibleRelay is the per-attempt transport interface for an isolated
// child. Runtime construction is held in this build; tests use synthetic
// capabilities and providers.
type CompatibleRelay struct {
	LocalURL, LocalKey string
	secret             string
	close              func()
	comparison         *compatibleComparison
}

// RequireModelEvidence enables a strict per-attempt generation guard before
// the executor is allowed to know the loopback capability.
func (r *CompatibleRelay) RequireModelEvidence(expected, prompt string, ready ...func(context.Context, bool) bool) {
	if r.comparison == nil {
		r.comparison = &compatibleComparison{}
	}
	r.comparison.setup(expected, prompt, ready...)
}

func (r *CompatibleRelay) ModelEvidence() model.ProviderModelEvidence {
	if r.comparison == nil {
		return model.ProviderModelEvidence{Returned: model.UnknownProviderModel, Source: model.ModelSourceUnknown, Status: "request_missing"}
	}
	return r.comparison.snapshot()
}

// ComparisonAnswer returns only normalized text from a verified response.
func (r *CompatibleRelay) ComparisonAnswer() string {
	if r.comparison == nil {
		return ""
	}
	return r.comparison.verifiedAnswer()
}

func (r *CompatibleRelay) Close() {
	if r.close != nil {
		r.close()
	}
}

func (r *CompatibleRelay) CopyOutput(dst io.Writer, src io.Reader) {
	var writer io.Writer = dst
	redactKey := newCompatibleRedactor(dst, r.LocalKey)
	writer = redactKey
	redactSecret := newCompatibleRedactor(writer, r.secret)
	_, _ = io.Copy(redactSecret, src)
	_ = redactSecret.finish()
	_ = redactKey.finish()
}

// StartCompatibleRelay cannot construct a provider transport in this build.
// A local flag, healthy DB row, or already prepared attempt cannot authorize
// disclosure. There is no production listener or credential resolution here.
func StartCompatibleRelay(context.Context, []byte, ...string) (*CompatibleRelay, error) {
	return nil, errors.New(model.CompatibleRuntimeHold)
}

// CredentialCurrent revalidates and resolves only this same already approved
// reference; rotations or an inaccessible reference cannot authorize sending
// a stale in-memory credential. Neither value is recorded.
func (r *CompatibleRelay) CredentialCurrent(raw []byte, roots ...string) bool {
	if r.secret == "" {
		return false
	}
	prof, err := parseCompatible(raw)
	if err != nil || validateCompatible(prof) != nil {
		return false
	}
	current, err := resolveRelayCredential(prof, roots)
	return err == nil && subtle.ConstantTimeCompare([]byte(r.secret), []byte(current)) == 1
}

func resolveRelayCredential(prof compatibleProfile, managedRoots []string) (string, error) {
	var protected []os.FileInfo
	if prof.Credential.Source == "file" {
		if len(managedRoots) < 3 {
			return "", errors.New("credential file workspace boundary cannot be verified")
		}
		real, identities, err := credentialFileLocation(prof.Credential.Path, managedRoots)
		if err != nil {
			return "", err
		}
		if real != prof.Credential.Path {
			return "", errors.New("credential file location changed after validation")
		}
		protected = identities
	}
	return resolveCredential(prof.Credential, protected...)
}

func newCompatibleTransport(host, port string, lookup compatibleLookup, dial compatibleDial) *http.Transport {
	if port == "" {
		port = "443"
	}
	return &http.Transport{
		DialContext:            compatibleTargetDial(host, port, lookup, dial),
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    15 * time.Second,
		ResponseHeaderTimeout:  90 * time.Second,
		MaxResponseHeaderBytes: 1 << 20,
		IdleConnTimeout:        30 * time.Second,
		DisableCompression:     true,
		ForceAttemptHTTP2:      true,
	}
}

func compatibleRelayHandler(endpoint *url.URL, localHost, key, secret string, transport http.RoundTripper, comparisons ...*compatibleComparison) http.Handler {
	var comparison *compatibleComparison
	if len(comparisons) != 0 {
		comparison = comparisons[0]
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The Anthropic SDK adds the fixed beta qualifier to its beta Messages
		// routes. No arbitrary query, origin, or path can pass the relay.
		beta := req.URL.RawQuery == "beta=true"
		if req.Host != localHost || req.URL.IsAbs() || req.URL.Host != "" ||
			req.URL.RawPath != "" || (req.URL.RawQuery != "" && !beta) || req.URL.ForceQuery ||
			req.Method != http.MethodPost || (req.URL.Path != "/v1/messages" && req.URL.Path != "/v1/messages/count_tokens") {
			compatibleError(w, http.StatusNotFound)
			return
		}
		keys := req.Header.Values("X-Api-Key")
		if len(keys) != 1 || subtle.ConstantTimeCompare([]byte(keys[0]), []byte(key)) != 1 {
			compatibleError(w, http.StatusUnauthorized)
			return
		}
		if req.ContentLength > maxCompatibleRequest {
			compatibleError(w, http.StatusRequestEntityTooLarge)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxCompatibleRequest))
		if err != nil {
			compatibleError(w, http.StatusBadRequest)
			return
		}

		isGeneration := req.URL.Path == "/v1/messages"
		strict := false
		if comparison != nil {
			strict, body = comparison.authorizeRequest(req.Context(), body, isGeneration)
			if body == nil {
				compatibleError(w, http.StatusConflict)
				return
			}
		}
		target := *endpoint
		target.Path = strings.TrimRight(target.Path, "/") + req.URL.Path
		target.RawPath = ""
		if beta && !strict {
			target.RawQuery = "beta=true"
		}
		up, err := http.NewRequestWithContext(req.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
		if err != nil {
			compatibleError(w, http.StatusBadGateway)
			return
		}
		if strict {
			up.GetBody = nil
		}
		up.Header.Set("Authorization", "Bearer "+secret)
		up.Header.Set("Content-Type", "application/json")
		if strict {
			up.Header.Set("Accept", "application/json, text/event-stream")
			up.Header.Set("Anthropic-Version", "2023-06-01")
		} else {
			for _, h := range []string{"Accept", "Anthropic-Version", "Anthropic-Beta"} {
				for _, value := range req.Header.Values(h) {
					up.Header.Add(h, value)
				}
			}
		}
		resp, err := transport.RoundTrip(up)
		if err != nil {
			compatibleError(w, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			// Never propagate Location or let the CLI act on upstream redirects.
			compatibleError(w, http.StatusBadGateway)
			return
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			compatibleError(w, resp.StatusCode)
			return
		}
		if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
			compatibleError(w, http.StatusBadGateway)
			return
		}
		stream := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
		var response io.Reader = resp.Body
		if strict && isGeneration {
			// Before relaying answer bytes, verify the identity from the
			// authenticated provider response, never from caller/CLI output.
			raw, err := io.ReadAll(io.LimitReader(resp.Body, maxComparisonResponse+1))
			if err != nil || len(raw) > maxComparisonResponse || !comparison.observe(raw, stream, secret) {
				compatibleError(w, http.StatusBadGateway)
				return
			}
			reply := comparison.childReply(stream)
			if reply == nil {
				compatibleError(w, http.StatusConflict)
				return
			}
			response = bytes.NewReader(reply)
		}
		// Do not propagate upstream headers that could reflect authentication.
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		filter := newCompatibleRedactor(w, secret)
		buf := make([]byte, 16<<10)
		for {
			n, err := response.Read(buf)
			if n > 0 {
				if _, writeErr := filter.Write(buf[:n]); writeErr != nil {
					return
				}
				_ = http.NewResponseController(w).Flush()
			}
			if err != nil {
				break
			}
		}
		_ = filter.finish()
	})
}

func compatibleError(w http.ResponseWriter, status int) {
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"API channel request failed (`+strconv.Itoa(status)+`)"}}`)
}

// compatibleRedactor retains only a trailing partial match between writes, not
// the entire response. Credential syntax excludes characters that would have
// to be escaped in JSON. This is defense in depth, not a provider trust model.
type compatibleRedactor struct {
	out     io.Writer
	secret  []byte
	pending []byte
}

func newCompatibleRedactor(out io.Writer, secret string) *compatibleRedactor {
	return &compatibleRedactor{out: out, secret: []byte(secret)}
}

func (r *compatibleRedactor) Write(p []byte) (int, error) {
	if len(r.secret) == 0 {
		return r.out.Write(p)
	}
	buf := append(r.pending, p...)
	r.pending = nil
	for {
		i := bytes.Index(buf, r.secret)
		if i < 0 {
			break
		}
		if _, err := r.out.Write(buf[:i]); err != nil {
			return 0, err
		}
		if _, err := io.WriteString(r.out, "[redacted]"); err != nil {
			return 0, err
		}
		buf = buf[i+len(r.secret):]
	}
	// Most chunks have no trailing partial match. Use the maximum bounded
	// suffix size first and skip comparisons whose first byte cannot match.
	tail := min(len(buf), len(r.secret)-1)
	for tail > 0 && (buf[len(buf)-tail] != r.secret[0] || !bytes.HasSuffix(buf, r.secret[:tail])) {
		tail--
	}
	if _, err := r.out.Write(buf[:len(buf)-tail]); err != nil {
		return 0, err
	}
	r.pending = append(r.pending, buf[len(buf)-tail:]...)
	return len(p), nil
}

func (r *compatibleRedactor) finish() error {
	_, err := r.out.Write(r.pending)
	r.pending = nil
	return err
}
