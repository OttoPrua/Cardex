package runner

import (
	"net/http"
	"net/url"
)

// CompatibleComparisonHandlerForTest exposes the offline-only loopback
// fixture. No production call has this constructor or its caller's synthetic
// RoundTripper.
func CompatibleComparisonHandlerForTest(endpoint *url.URL, host, key, secret string, transport http.RoundTripper, relay *CompatibleRelay) http.Handler {
	return compatibleRelayHandler(endpoint, host, key, secret, transport, relay.comparison)
}
