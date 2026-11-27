package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxCompatibleRequest = 32 << 20

// CompatibleRelay authenticates exactly two Anthropic API paths for one launch.
// The provider token stays in the host. The CLI receives an ephemeral loopback
// capability, not the provider token, and cannot request another origin.
type CompatibleRelay struct {
	LocalURL, LocalKey string
	secret             string
	close              func()
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

func StartCompatibleRelay(ctx context.Context, raw []byte, managedRoots ...string) (*CompatibleRelay, error) {
	prof, err := parseCompatible(raw)
	if err != nil {
		return nil, err
	}
	if err := validateCompatible(prof); err != nil {
		return nil, err
	}
	// Go's Unix platform certificate loader consults these process variables
	// even when a transport has no proxy and the child environment is clean.
	if os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "" {
		return nil, errors.New("api channel cannot verify system certificate roots with TLS overrides set")
	}
	var protected []os.FileInfo
	if prof.Credential.Source == "file" {
		if len(managedRoots) < 3 {
			return nil, errors.New("credential file workspace boundary cannot be verified")
		}
		real, identities, err := credentialFileLocation(prof.Credential.Path, managedRoots)
		if err != nil {
			return nil, err
		}
		if real != prof.Credential.Path {
			return nil, errors.New("credential file location changed after validation")
		}
		protected = identities
	}
	endpoint, _ := validateEndpoint(prof.BaseURL)
	target, _ := url.Parse(endpoint)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("api channel cannot start isolated auth transport")
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		_ = listener.Close()
		return nil, errors.New("api channel cannot create isolated auth transport capability")
	}
	secret, err := resolveCredential(prof.Credential, protected...)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	key := "cdx-relay-" + hex.EncodeToString(entropy[:])
	// Explicit transport: no inherited proxy, no custom CA from env, no
	// redirects (RoundTrip performs one exchange rather than following them).
	transport := newCompatibleTransport(target.Hostname(), target.Port(), net.DefaultResolver.LookupNetIP,
		(&net.Dialer{Timeout: 15 * time.Second}).DialContext)
	server := &http.Server{
		Handler:           compatibleRelayHandler(target, listener.Addr().String(), key, secret, transport),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    1 << 16,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = server.Serve(listener) }()
	return &CompatibleRelay{
		LocalURL: "http://" + listener.Addr().String(), LocalKey: key, secret: secret,
		close: func() {
			_ = server.Close()
			transport.CloseIdleConnections()
		},
	}, nil
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

func compatibleRelayHandler(endpoint *url.URL, localHost, key, secret string, transport http.RoundTripper) http.Handler {
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

		target := *endpoint
		target.Path = strings.TrimRight(target.Path, "/") + req.URL.Path
		target.RawPath = ""
		if beta {
			target.RawQuery = "beta=true"
		}
		up, err := http.NewRequestWithContext(req.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
		if err != nil {
			compatibleError(w, http.StatusBadGateway)
			return
		}
		up.Header.Set("Authorization", "Bearer "+secret)
		up.Header.Set("Content-Type", "application/json")
		for _, h := range []string{"Accept", "Anthropic-Version", "Anthropic-Beta"} {
			for _, value := range req.Header.Values(h) {
				up.Header.Add(h, value)
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
		// Do not propagate upstream headers that could reflect authentication.
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		filter := newCompatibleRedactor(w, secret)
		buf := make([]byte, 16<<10)
		for {
			n, err := resp.Body.Read(buf)
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
