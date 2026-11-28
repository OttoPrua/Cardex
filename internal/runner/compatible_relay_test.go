package runner

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type compatibleMockTransport func(*http.Request) (*http.Response, error)

func (f compatibleMockTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const fakeRelayHost = "127.0.0.1:38021"
const fakeRelayKey = "fixture-local-capability"

func relayRequest(method, path, key string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(`{"model":"fixture-model","messages":[]}`))
	r.Host = fakeRelayHost
	r.Header.Set("X-Api-Key", key)
	return r
}

func TestCompatibleRelayBindsAuthToOriginWithoutRedirects(t *testing.T) {
	upstream, _ := url.Parse("https://vendor-a.example.test/anthropic")
	calls := 0
	handler := compatibleRelayHandler(upstream, fakeRelayHost, fakeRelayKey, canaryA,
		compatibleMockTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.String() != "https://vendor-a.example.test/anthropic/v1/messages?beta=true" ||
				r.URL.User != nil || r.Method != http.MethodPost ||
				r.Header.Get("Authorization") != "Bearer "+canaryA ||
				r.Header.Get("X-Api-Key") != "" || r.Header.Get("Proxy-Authorization") != "" ||
				r.Header.Get("Cookie") != "" || r.Header.Get("Anthropic-Version") != "fixture-v1" {
				t.Fatal("upstream method, destination, or auth was not pinned")
			}
			if bytes, _ := io.ReadAll(r.Body); !strings.Contains(string(bytes), "fixture-model") {
				t.Fatal("model request was not passed through")
			}
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": []string{"https://vendor-b.example.test/" + canaryA}},
				Body:       io.NopCloser(strings.NewReader(canaryA)),
			}, nil
		}))
	r := relayRequest(http.MethodPost, "/v1/messages?beta=true", fakeRelayKey)
	r.Header.Set("Authorization", "Bearer "+canaryB)
	r.Header.Set("Proxy-Authorization", canaryB)
	r.Header.Set("Cookie", canaryB)
	r.Header.Set("Anthropic-Version", "fixture-v1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if calls != 1 || w.Code != http.StatusBadGateway || w.Header().Get("Location") != "" ||
		strings.Contains(w.Body.String(), canaryA) || strings.Contains(w.Body.String(), "vendor-b") {
		t.Fatal("relay forwarded a redirect or leaked auth")
	}
}

func TestCompatibleRelayRejectsUnboundRequestsBeforeProvider(t *testing.T) {
	upstream, _ := url.Parse("https://vendor-a.example.test/anthropic")
	handler := compatibleRelayHandler(upstream, fakeRelayHost, fakeRelayKey, canaryA,
		compatibleMockTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("rejected request reached transport")
			return nil, nil
		}))
	for _, tuple := range [][3]string{
		{http.MethodConnect, "/v1/messages", fakeRelayKey},
		{http.MethodPost, "https://vendor-b.example.test/v1/messages", fakeRelayKey},
		{http.MethodPost, "/%76%31/messages", fakeRelayKey},
		{http.MethodPost, "/v1/messages/../messages", fakeRelayKey},
		{http.MethodPost, "/v1/messages?key=x", fakeRelayKey},
		{http.MethodPost, "/v1/messages?beta=true&target=vendor-b.example.test", fakeRelayKey},
		{http.MethodPost, "/v1/messages?beta=%74rue", fakeRelayKey},
		{http.MethodPost, "/v1/messages?", fakeRelayKey},
		{http.MethodPost, "/any-path", fakeRelayKey},
		{http.MethodPost, "/v1/messages", "wrong"},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, relayRequest(tuple[0], tuple[1], tuple[2]))
		if w.Code < 400 || strings.Contains(w.Body.String(), canaryA) {
			t.Fatal("relay accepted request or leaked auth")
		}
	}
	r := relayRequest(http.MethodPost, "/v1/messages", fakeRelayKey)
	r.Host = "other-host.example.test"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code < 400 {
		t.Fatal("relay accepted a different Host")
	}
}

func TestCompatibleRelaySanitizesProviderErrorsAndStreaming(t *testing.T) {
	upstream, _ := url.Parse("https://vendor-a.example.test/anthropic")
	secretReads := io.MultiReader(strings.NewReader(`data: {"text":"`+canaryA[:7]),
		strings.NewReader(canaryA[7:]+`"}`+"\n\n"))
	var fixtures = []struct {
		status int
		body   io.Reader
		err    error
		want   int
	}{
		{200, secretReads, nil, 200},
		{401, strings.NewReader(canaryA), nil, 401},
		{0, nil, errors.New("tls certificate verification failed: " + canaryA), 502},
	}
	for _, fixture := range fixtures {
		handler := compatibleRelayHandler(upstream, fakeRelayHost, fakeRelayKey, canaryA,
			compatibleMockTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/anthropic/v1/messages/count_tokens" {
					t.Fatal("count token path was not pinned")
				}
				if fixture.err != nil {
					return nil, fixture.err
				}
				return &http.Response{
					StatusCode: fixture.status,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Reflection": []string{canaryA}},
					Body:       io.NopCloser(fixture.body),
				}, nil
			}))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, relayRequest(http.MethodPost, "/v1/messages/count_tokens", fakeRelayKey))
		if w.Code != fixture.want || strings.Contains(w.Body.String(), canaryA) ||
			w.Header().Get("X-Reflection") != "" {
			t.Fatal("transport error leaked or had wrong status")
		}
		if w.Code == 200 && (!strings.Contains(w.Body.String(), "[redacted]") || !w.Flushed) {
			t.Fatal("streaming did not redact echoed auth")
		}
	}
}

func TestCompatibleRelayProfilesKeepDestinationAndAuthDistinct(t *testing.T) {
	var wg sync.WaitGroup
	for _, pair := range [][3]string{
		{"vendor-a.example.test", fakeRelayKey + "-a", canaryA},
		{"vendor-b.example.test", fakeRelayKey + "-b", canaryB},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, _ := url.Parse("https://" + pair[0])
			handler := compatibleRelayHandler(u, fakeRelayHost, pair[1], pair[2],
				compatibleMockTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != pair[0] || r.Header.Get("Authorization") != "Bearer "+pair[2] {
						t.Error("profile crossed its auth or bound destination")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, relayRequest(http.MethodPost, "/v1/messages", pair[1]))
			if w.Code != 200 {
				t.Error("valid per-attempt capability rejected")
			}
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, relayRequest(http.MethodPost, "/v1/messages", fakeRelayKey))
			if w.Code != 401 {
				t.Error("shared capability accepted")
			}
		}()
	}
	wg.Wait()
}

func TestCompatibleRelayOutputDropsSplitCredentials(t *testing.T) {
	relay := CompatibleRelay{LocalKey: fakeRelayKey, secret: canaryA}
	var dst bytes.Buffer
	relay.CopyOutput(&dst, io.MultiReader(strings.NewReader("hello "+canaryA[:4]),
		strings.NewReader(canaryA[4:]+" "+fakeRelayKey[:5]), strings.NewReader(fakeRelayKey[5:]+" end")))
	if dst.String() != "hello [redacted] [redacted] end" {
		t.Fatal("executor output contained an auth value or was truncated")
	}
}

func TestCompatibleProductionRelayDoesNotResolveOrListen(t *testing.T) {
	t.Setenv("CARDEX_API_TEST", "fixture-only")
	profile := []byte(`{"protocol":"anthropic-compatible","base_url":"https://fixtures.example.test","credential":{"source":"env","name":"CARDEX_API_TEST"}}`)
	relay, err := StartCompatibleRelay(t.Context(), profile)
	if relay != nil || err == nil || !strings.Contains(err.Error(), "runtime is disabled") {
		t.Fatal("production factory accepted a live descriptor")
	}
}

func TestCompatibleCredentialEnvCannotBeRepackagedByLegacyEngine(t *testing.T) {
	const name = "FIXTURE_SHARED_ENV"
	t.Setenv(name, "synthetic-only")
	_, _, err := (Claude{}).Build(LaunchSpec{
		DropEnv: []string{"fixture_shared_env"},
		Script:  []byte(`{"base_url":"https://legacy.example.test","auth_env":"FIXTURE_SHARED_ENV"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot reuse") || strings.Contains(err.Error(), name) {
		t.Fatal("legacy profile could repack a compatible ref as a general credential")
	}
}
