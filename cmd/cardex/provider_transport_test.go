package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProviderChildEnvProjectsOnlyTransportAndNativeHome(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	t.Setenv("NO_PROXY", "localhost")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-leak")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")

	env := providerChildEnv("/native/auth/home", map[string]string{"NO_COLOR": "1", "OPENAI_API_KEY": "extra-must-not-leak"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{
		"\nHOME=/native/auth/home\n",
		"\nHTTPS_PROXY=http://127.0.0.1:7890\n",
		"\nNO_PROXY=localhost\n",
		"\nNO_COLOR=1\n",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing allowlisted child environment entry %q in %q", want, env)
		}
	}
	for _, forbidden := range []string{"AWS_SECRET_ACCESS_KEY=", "OPENAI_API_KEY=", "must-not-leak", "extra-must-not-leak"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("secret-bearing parent environment leaked through provider projection: %q", env)
		}
	}
}

func TestProviderPreflightStatesAreClosedAndValueBlind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		output string
		err    error
		want   providerPreflightState
	}{
		{"missing", "login required", os.ErrPermission, providerAuthMissing},
		{"expired-before-missing-substring", "Invalid or expired credentials (reason=no auth context)", os.ErrPermission, providerAuthExpiredRefreshable},
		{"proxy", "proxyconnect tcp: connection refused 127.0.0.1", os.ErrPermission, providerNetworkProxyUnreachable},
		{"rate", "HTTP 429: rate limit", os.ErrPermission, providerRateLimited},
		{"model", "model not found", os.ErrPermission, providerModelStartFailed},
		{"transport", "unexpected failure", os.ErrPermission, providerTransportFailed},
		{"ready", "ok", nil, providerReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProviderPreflight(tc.output, tc.err); got != tc.want {
				t.Fatalf("classifyProviderPreflight()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestProviderParallelLimitsDefaultAndOverrideIndependently(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		GrokBuild:   &GrokBuildRoute{},
		KimiCLIOpus: &KimiCLIOpusRoute{},
	}
	if got := providerParallelLimit(cfg, grokBuildRunnerName); got != 24 {
		t.Fatalf("Grok default cap=%d want 24", got)
	}
	if got := providerParallelLimit(cfg, kimiCLIRunnerName); got != 24 {
		t.Fatalf("Kimi default cap=%d want 24", got)
	}
	cfg.GrokBuild.MaxParallel = 7
	cfg.KimiCLIOpus.MaxParallel = 11
	if got := providerParallelLimit(cfg, grokBuildRunnerName); got != 7 {
		t.Fatalf("Grok override cap=%d want 7", got)
	}
	if got := providerParallelLimit(cfg, kimiCLIRunnerName); got != 11 {
		t.Fatalf("Kimi override cap=%d want 11", got)
	}
}

func TestAntigravityPreflightChoosesHighestActuallyAdvertisedOpus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	script := "#!/bin/sh\nprintf '%s\\n' 'claude-sonnet-4-6' 'claude-opus-4-6-thinking' 'claude-opus-4-10-thinking'\n"
	if err := writeProviderPreflightFixture(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig("")
	cfg.AntigravityBin = bin
	cfg.Antigravity = &AntigravityRoute{Enabled: true}
	r := runAntigravityPreflight(context.Background(), cfg, &Task{}, providerChildEnv(dir, nil))
	if r.State != providerReady || r.SelectedModel != "claude-opus-4-10-thinking" {
		t.Fatalf("dynamic Antigravity route readback mismatch: %+v", r)
	}
}

func TestAntigravityPreflightDoesNotFallBackToSonnet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	if err := writeProviderPreflightFixture(bin, []byte("#!/bin/sh\nprintf '%s\\n' 'claude-sonnet-4-6'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig("")
	cfg.AntigravityBin = bin
	cfg.Antigravity = &AntigravityRoute{Enabled: true}
	r := runAntigravityPreflight(context.Background(), cfg, &Task{}, providerChildEnv(dir, nil))
	if r.State != providerModelUnavailable || r.SelectedModel != "" {
		t.Fatalf("missing Opus must hold MODEL_UNAVAILABLE without Sonnet substitution: %+v", r)
	}
}

func TestAntigravityCatalogAuthRecoveryIsOrderedAndTerminal(t *testing.T) {
	t.Parallel()
	const model = "gemini-3.8-flash-high"
	const catalog = model + "\tGemini 3.8 Flash (High)\n"
	const startup = "W0922 18:52:15.000000 1 auth.go:101] not logged in\n"
	const oauth = "I0922 18:52:16.006284 1 server_oauth.go:201] OAuth: authenticated successfully as test@example.invalid\n"
	for _, tc := range []struct {
		name, stdout, stderr string
		exit                 int
		want                 providerPreflightState
	}{
		{"recovered startup", catalog, startup + oauth, 0, providerReady},
		{"recovered startup error", catalog, "E0922 18:52:15.000000 1 launchsteps.go:84] startup auth source failed\n" + startup + oauth, 0, providerReady},
		{"final auth failure", catalog, startup + oauth + "login required\n", 0, providerAuthMissing},
		{"final auth expired", catalog, startup + oauth + "401 unauthorized\n", 0, providerAuthExpiredRefreshable},
		{"final error", catalog, startup + oauth + "E0922 18:52:17.000000 1 models.go:12] model catalog failed\n", 0, providerTransportFailed},
		{"nonzero after success", catalog, oauth + "HTTP 503: service unavailable\n", 1, providerTransportFailed},
		{"no success marker", catalog, startup, 0, providerAuthMissing},
		{"keyring alone", catalog, startup + "I0922 18:52:16.006275 1 auth.go:148] ChainedAuth: authenticated via keyring (effective: keyring)\n", 0, providerAuthMissing},
		{"stdout auth error", catalog + "login required\n", startup + oauth, 0, providerAuthMissing},
		{"stdout error", catalog + "error: catalog incomplete\n", startup + oauth, 0, providerTransportFailed},
		{"model mentioned in prose", "Requested model " + model + " is unavailable\n", startup + oauth, 0, providerModelUnavailable},
		{"only near match", model + "-preview\tOther model\n", startup + oauth, 0, providerModelUnavailable},
		{"missing model", "claude-opus-4-6-thinking\tOpus\n", startup + oauth, 0, providerModelUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "agy")
			script := "#!/bin/sh\nprintf '%s' " + shSingleQuote(tc.stdout) + "\nprintf '%s' " + shSingleQuote(tc.stderr) + " >&2\nexit " + fmt.Sprint(tc.exit) + "\n"
			if err := writeProviderPreflightFixture(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			cfg := defaultConfig("")
			cfg.AntigravityBin = bin
			r := runAntigravityPreflight(context.Background(), cfg, &Task{AgyModel: model}, providerChildEnv(dir, nil))
			if r.State != tc.want || (r.State == providerReady) != (r.SelectedModel == model) {
				t.Fatalf("state=%s selected=%q want=%s", r.State, r.SelectedModel, tc.want)
			}
		})
	}
}

func TestAntigravityCatalogTimeoutNeverAuthorizesCachedSuccess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	script := "#!/bin/sh\nprintf '%s\\n' 'gemini-3.8-flash-high\tGemini 3.8 Flash (High)'\nprintf '%s\\n' 'I0922 18:52:16.006284 1 server_oauth.go:201] OAuth: authenticated successfully as test@example.invalid' >&2\nexec sleep 5\n"
	if err := writeProviderPreflightFixture(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig("")
	cfg.AntigravityBin = bin
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := runAntigravityPreflight(ctx, cfg, &Task{AgyModel: "gemini-3.8-flash-high"}, providerChildEnv(dir, nil))
	if r.State == providerReady || r.SelectedModel != "" {
		t.Fatal("timeout authorized a provider model")
	}
}

// Keep concurrent forks from briefly inheriting a writable script descriptor:
// Linux can otherwise reject the next exec with ETXTBSY before the mock runs.
func writeProviderPreflightFixture(path string, data []byte, mode os.FileMode) error {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	return os.WriteFile(path, data, mode)
}
