package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

const probePrompt = "Reply with OK."
const probeTimeout = 12 * time.Second
const probeMaxResponse = 64 << 10

// CompatibleProbeReceipt reports the single request without retaining provider
// content, errors, headers, authentication, or billing/health state.
type CompatibleProbeReceipt struct {
	Channel        string `json:"channel"`
	Endpoint       string `json:"endpoint,omitempty"`
	RequestedModel string `json:"requested_model"`
	ReturnedModel  string `json:"returned_model"`
	ModelSource    string `json:"model_source"`
	Status         string `json:"status"`
	Attempted      bool   `json:"attempted"`
}

// ProbeCompatible cannot attest operator presence from CLI arguments alone.
// The paid transport stays disabled until there is an independent approval
// boundary; the request contract is exercised only with synthetic transports.
func ProbeCompatible(ctx context.Context, home string, ch model.Channel, requested string, roots []string) CompatibleProbeReceipt {
	return probeCompatible(ctx, home, ch, requested, roots, nil)
}

func probeCompatible(ctx context.Context, home string, ch model.Channel, requested string, roots []string, transport http.RoundTripper) CompatibleProbeReceipt {
	receipt := CompatibleProbeReceipt{
		Channel: ch.ID, RequestedModel: model.UnknownProviderModel, ReturnedModel: model.UnknownProviderModel,
		ModelSource: model.ModelSourceUnknown, Status: "config_invalid",
	}
	if validModelID(requested) != nil {
		return receipt
	}
	receipt.RequestedModel = requested
	if transport == nil {
		receipt.Status = "approval_unavailable"
		return receipt
	}
	binding := CompatibleBindingID(ch.EngineProfile)
	if ch.Runner != model.RunnerClaude || binding == "" {
		return receipt
	}
	invalid, loaded, engines, err := LoadEngineConfig(home)
	if err != nil {
		return receipt
	}
	raw, reason := ResolveEngineJSON(invalid, loaded, engines, ch.EngineProfile)
	if reason != "" {
		return receipt
	}
	// One deliberate probe can validate a staged channel without enabling it.
	// Reuse the same binding/model/file-location check, on a copy, for all
	// configurations. The operator's stored health never changes.
	ch.Health = model.HealthOK
	managedRoots := append([]string{home}, roots...)
	raw, err = ValidateCompatibleLaunch(LaunchSpec{Script: raw, Model: requested, CardKind: model.KindResearch}, ch, binding, managedRoots...)
	if err != nil {
		return receipt
	}
	prof, err := parseCompatible(raw)
	if err != nil {
		return receipt
	}
	validatedEndpoint, err := validateEndpoint(prof.BaseURL)
	if err != nil {
		return receipt
	}
	endpoint, err := url.Parse(validatedEndpoint)
	if err != nil {
		return receipt
	}
	receipt.Endpoint = endpoint.Scheme + "://" + endpoint.Host
	if os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "" {
		return receipt
	}
	var protected []os.FileInfo
	if prof.Credential.Source == "file" {
		real, identities, err := credentialFileLocation(prof.Credential.Path, managedRoots)
		if err != nil || real != prof.Credential.Path {
			return receipt
		}
		protected = identities
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if ctx.Err() != nil {
		receipt.Status = probeTransportStatus(ctx, ctx.Err())
		return receipt
	}
	secret, err := resolveCredential(prof.Credential, protected...)
	if err != nil {
		receipt.Status = "credential_unavailable"
		return receipt
	}
	if ctx.Err() != nil {
		receipt.Status = probeTransportStatus(ctx, ctx.Err())
		return receipt
	}
	return exchangeCompatibleProbe(ctx, endpoint, secret, receipt, transport)
}

func exchangeCompatibleProbe(ctx context.Context, endpoint *url.URL, secret string, receipt CompatibleProbeReceipt, t http.RoundTripper) CompatibleProbeReceipt {
	type inputMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body, _ := json.Marshal(struct {
		Model     string         `json:"model"`
		MaxTokens int            `json:"max_tokens"`
		Stream    bool           `json:"stream"`
		Messages  []inputMessage `json:"messages"`
	}{receipt.RequestedModel, 8, false, []inputMessage{{"user", probePrompt}}})
	target := *endpoint
	target.Path = strings.TrimRight(target.Path, "/") + "/v1/messages"
	target.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return receipt
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	receipt.Attempted = true
	resp, err := t.RoundTrip(req)
	if err != nil {
		receipt.Status = probeTransportStatus(ctx, err)
		return receipt
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		receipt.Status = "auth_failed"
		return receipt
	case resp.StatusCode == http.StatusTooManyRequests:
		receipt.Status = "rate_limited"
		return receipt
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		receipt.Status = "redirect_refused"
		return receipt
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		receipt.Status = "response_invalid"
		return receipt
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		receipt.Status = "provider_error"
		// Do not mistake every unknown endpoint for an unavailable model.
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity {
			errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, probeMaxResponse+1))
			if readErr != nil {
				receipt.Status = probeTransportStatus(ctx, readErr)
			} else if len(errBody) <= probeMaxResponse && providerSaysModelUnavailable(errBody) {
				receipt.Status = "model_unavailable"
			}
		}
		return receipt
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, probeMaxResponse+1))
	if err != nil {
		receipt.Status = probeTransportStatus(ctx, err)
		return receipt
	}
	if len(data) > probeMaxResponse {
		receipt.Status = "response_invalid"
		return receipt
	}
	reported := parseProviderModel(data, false, secret)
	if reported == "" {
		receipt.Status = "model_unknown"
		return receipt
	}
	receipt.ReturnedModel, receipt.ModelSource = reported, model.ModelSourceProvider
	receipt.Status = "ok"
	if reported != receipt.RequestedModel {
		receipt.Status = "model_mismatch"
	}
	return receipt
}

func probeTransportStatus(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return "timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "transport_error"
}

func providerSaysModelUnavailable(raw []byte) bool {
	type providerError struct {
		Type  string `json:"type"`
		Code  string `json:"code"`
		Param string `json:"param"`
	}
	fields, ok := providerIdentityFields(raw)
	if !ok || providerIdentityString(fields["type"]) != "error" {
		return false
	}
	var e providerError
	if json.Unmarshal(fields["error"], &e) != nil {
		return false
	}
	if e.Param == "model" && e.Type == "not_found_error" {
		return true
	}
	for _, label := range []string{e.Type, e.Code} {
		switch label {
		case "model_not_found", "model_not_available", "model_unavailable":
			return true
		}
	}
	return false
}
