package runner

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

const (
	canaryA = "cdx-canary-a-7f3e9c"
	canaryB = "cdx-canary-b-1a2b3c"
)

func TestCompatibleProfileRejectsLegacySecretsAndAliases(t *testing.T) {
	raw := []byte(`{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","default_model":"vendor-a-model-1","credential":{"source":"env","name":"CARDEX_FIXTURE_VENDOR_A"}}`)
	if _, ok := ParseEngineProfile(raw); ok {
		t.Fatal("compatible profile was accepted as a legacy profile")
	}
	if err := ValidateCompatibleJSON(raw); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`{"protocol":"anthropic-compatible","Protocol":"anthropic-compatible","base_url":"https://a.example.test","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://a.example.test","credential":{"source":"env","name":"KEY","Name":"OTHER"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://a.example.test?","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://a.example.test#","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"openai-compatible","base_url":"https://api.example.test/v1","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"http://api.example.test/anthropic","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://user:pass@api.example.test/anthropic","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic?q=1","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic#frag","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://api/anthropic","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","default_model":"opus","credential":{"source":"env","name":"KEY"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","credential":{"source":"env","name":"KEY"},"auth_value":"` + canaryA + `"}`,
		`{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","credential":{"source":"env","name":"KEY"},"extra_env":{"X":"1"}}`,
		`{"protocol":"anthropic-compatible","base_url":"https://api.example.test/$HOST","credential":{"source":"env","name":"KEY"}}`,
	}
	for _, doc := range cases {
		err := ValidateCompatibleJSON([]byte(doc))
		if err == nil {
			t.Fatalf("accepted %s", doc)
		}
		if strings.Contains(err.Error(), canaryA) {
			t.Fatal("profile error contained a credential")
		}
	}
	if !strings.Contains(ValidateCompatibleJSON([]byte(cases[4])).Error(), "not supported") {
		t.Fatal(ValidateCompatibleJSON([]byte(cases[4])))
	}
}

func TestCompatibleBuildIsolatesCredential(t *testing.T) {
	t.Setenv("CARDEX_FIXTURE_VENDOR_A", canaryA)
	t.Setenv("UNRELATED_PROVIDER_SECRET", canaryB)
	t.Setenv("ANTHROPIC_API_KEY", canaryB)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "leftover-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://leftover.example")
	t.Setenv("HTTPS_PROXY", "http://inherited.example:9")
	t.Setenv("NODE_TLS_REJECT_UNAUTHORIZED", "0")
	t.Setenv("NODE_OPTIONS", "--require profile")
	t.Setenv("HOME", "/ordinary")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const loopback = "http://127.0.0.1:38021"
	key := "cdx-relay-" + strings.Repeat("a", 64)
	raw := []byte(`{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","default_model":"vendor-a-model-1","credential":{"source":"env","name":"CARDEX_FIXTURE_VENDOR_A"}}`)
	spec := LaunchSpec{
		Model: "vendor-a-model-1", Script: raw, CardKind: model.KindReview,
		ConfigDir: dir, RelayURL: loopback, RelayKey: key,
	}
	argv, env, err := Claude{}.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := ProveAPIIsolation(spec, argv, env); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, needle := range []string{"--bare", "--restricted", "--no-session-persistence", "--model vendor-a-model-1", "--tools  --permission-mode", "--disallowedTools *", "--strict-mcp-config"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("expected flag %s", needle)
		}
	}
	for _, denied := range []string{"--dangerously-skip-permissions", "--allowedTools", canaryA, canaryB, key} {
		if strings.Contains(joined, denied) {
			t.Fatal("review argv granted tools or contained a credential")
		}
	}
	text := strings.Join(env, "\n")
	for _, denied := range []string{canaryA, canaryB, "leftover", "inherited.example", "NODE_OPTIONS", "NODE_TLS", "/ordinary"} {
		if strings.Contains(text, denied) {
			t.Fatal("API child environment inherited provider or startup settings")
		}
	}
	if envValue(env, "ANTHROPIC_API_KEY") != key || envValue(env, "ANTHROPIC_BASE_URL") != loopback ||
		envValue(env, "CLAUDE_CONFIG_DIR") != dir || envValue(env, "HOME") != dir {
		t.Fatal("resolved local capability or settings directory missing")
	}
	if err := ProveAPIIsolation(spec, append(argv, "--tools", "Bash"), env); err == nil {
		t.Fatal("accepted extra argv tool override")
	}

	for _, kind := range []model.CardKind{model.KindDev, model.KindRepair, model.KindSkeleton, model.KindRebase, model.KindSpike} {
		spec.CardKind = kind
		if argv, env, err := (Claude{}).Build(spec); err == nil || !strings.Contains(err.Error(), "workspace tool confinement") ||
			len(argv) != 0 || len(env) != 0 {
			t.Fatal("dev card was allowed to use a tool-enabled execution path")
		}
	}
	spec.CardKind = model.KindDesign
	if _, _, err := (Claude{}).Build(spec); err == nil || !strings.Contains(err.Error(), "cannot be proved") {
		t.Fatalf("design kind err %v", err)
	}
	spec.CardKind = model.KindReview
	spec.Model = "opus"
	if _, _, err := (Claude{}).Build(spec); err == nil || strings.Contains(err.Error(), canaryA) {
		t.Fatalf("alias err %v", err)
	}
	spec.Model = "vendor-a-model-1"
	spec.RelayURL = "https://unbound.example.test"
	if _, _, err := (Claude{}).Build(spec); err == nil {
		t.Fatal("build accepted direct unauthenticated origin")
	}
}

func TestCredentialFileAndKeychainDoNotLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
		dir := t.TempDir()
		path := filepath.Join(dir, "key")
		if err := os.WriteFile(path, []byte(canaryA), 0o600); err != nil {
			t.Fatal(err)
		}
		err := probeCredentialFile(path)
		if err == nil || !strings.Contains(err.Error(), "windows") || strings.Contains(err.Error(), canaryA) {
			t.Fatalf("windows file credential err %v", err)
		}
	} else {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "key")
		if err := os.WriteFile(path, []byte(canaryA+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err = probeCredentialFile(path)
		if err == nil || !strings.Contains(err.Error(), "readable") || strings.Contains(err.Error(), canaryA) {
			t.Fatalf("loose file err %v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readCredentialFile(path)
		if err != nil || got != canaryA {
			t.Fatal("protected file was not readable")
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if err := probeCredentialFile(link); err == nil {
			t.Fatal("symlink credential file was accepted")
		}
	}

	err := keychainCommandError("User interaction is not allowed. " + canaryA)
	if err == nil || err.Error() != "keychain permission denied" || strings.Contains(err.Error(), canaryA) {
		t.Fatal("keychain permission error leaked or changed")
	}
	dump := "class: \"genp\"\n\"svce\"<blob>=\"svc\"\n\"acct\"<blob>=\"one\"\n\"data\"<blob>=\"" + canaryA + "\"\nclass: \"genp\"\n\"svce\"<blob>=\"svc\"\n\"acct\"<blob>=\"two\"\n"
	accounts := parseKeychainAccounts(dump, "svc")
	if strings.Join(accounts, ",") != "one,two" {
		t.Fatalf("accounts %v", accounts)
	}
	for _, acct := range accounts {
		if strings.Contains(acct, canaryA) {
			t.Fatal("account parse returned secret data")
		}
	}
	args := keychainPasswordArgs("svc", "one")
	if strings.Join(args, " ") != "find-generic-password -s svc -a one -w" || strings.Contains(strings.Join(args, " "), canaryA) {
		t.Fatalf("args %v", args)
	}
	restore := SetSecurityRunnerForTest(&fakeSecurity{perm: true})
	defer restore()
	ref := credentialRef{Source: "keychain", Name: "svc"}
	if err := validateCredentialReference(ref); err == nil || strings.Contains(err.Error(), canaryA) || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("probe permission %v", err)
	}
	restore()
	restore = SetSecurityRunnerForTest(&fakeSecurity{accounts: map[string][]string{"svc": {"one", "two"}}})
	defer restore()
	if err := validateCredentialReference(ref); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("probe ambiguous %v", err)
	}
	if runtime.GOOS != "darwin" {
		restore()
		if _, err := newPlatformSecurity().Accounts("svc"); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("platform %v", err)
		}
	}
}

func TestChannelBindingValidatesBeforeWrite(t *testing.T) {
	home := t.TempDir()
	doc := `{
	  "engines": {
	    "vendor-a": {
	      "protocol": "anthropic-compatible",
	      "base_url": "https://api.example.test/anthropic",
	      "default_model": "vendor-a-model-1",
	      "credential": {"source": "env", "name": "CARDEX_FIXTURE_VENDOR_A"}
	    },
	    "other": {
	      "protocol": "anthropic-compatible",
	      "base_url": "https://b.example.test/v1",
	      "default_model": "m-1",
	      "credential": {"source": "env", "name": "OTHER_API_KEY"}
	    }
	  }
	}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := model.Channel{ID: "api-a", Runner: model.RunnerClaude, EngineProfile: "vendor-a", Family: "vendor-a"}
	got, err := ValidateChannelBinding(home, ch, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0] != "vendor-a-model-1" || got.Family != "vendor-a" {
		t.Fatalf("binding %+v", got)
	}
	again, err := ValidateChannelBinding(home, got, nil, nil)
	if err != nil || again.Models[0] != got.Models[0] {
		t.Fatalf("restart check %+v %v", again, err)
	}
	bad := got
	bad.Models = []string{"opus"}
	if _, err := ValidateChannelBinding(home, bad, nil, nil); err == nil {
		t.Fatal("tier alias was accepted")
	}
	other := model.Channel{ID: "grok", Runner: model.RunnerGrok, EngineProfile: "vendor-a", Family: "vendor-a", Models: []string{"vendor-a-model-1"}}
	if _, err := ValidateChannelBinding(home, other, nil, nil); err == nil {
		t.Fatal("non-claude runner accepted a profile")
	}
	clash := model.Channel{ID: "b", Runner: model.RunnerClaude, EngineProfile: "other", Family: "other", Models: []string{"vendor-a-model-1"}}
	if _, err := ValidateChannelBinding(home, clash, []model.Channel{got}, nil); err == nil || !strings.Contains(err.Error(), "routing conflict") {
		t.Fatalf("channel clash %v", err)
	}
	row := model.Positioning{ID: "pos", Model: "vendor-a-model-1", Family: "elsewhere"}
	if _, err := ValidateChannelBinding(home, got, nil, []model.Positioning{row}); err == nil || !strings.Contains(err.Error(), "positioning") {
		t.Fatalf("positioning clash %v", err)
	}
	legacy := model.Channel{ID: "k", Runner: model.RunnerClaude, EngineProfile: "kimi", Models: []string{"opus"}}
	if _, err := ValidateChannelBinding(t.TempDir(), legacy, nil, nil); err != nil {
		t.Fatal(err)
	}
	names := CompatibleChildDropNames(home)
	if !strings.Contains(strings.Join(names, ","), "CARDEX_FIXTURE_VENDOR_A") || !strings.Contains(strings.Join(names, ","), "OTHER_API_KEY") {
		t.Fatalf("drop names %v", names)
	}
	if strings.Contains(strings.Join(names, ","), canaryA) {
		t.Fatal("drop names contained a credential")
	}
}

type fakeSecurity struct {
	accounts map[string][]string
	perm     bool
	reads    int
}

func (f *fakeSecurity) Accounts(service string) ([]string, error) {
	if f.perm {
		return nil, errKeychainPermission
	}
	return append([]string{}, f.accounts[service]...), nil
}

func (f *fakeSecurity) Password(string, string) (string, error) {
	f.reads++
	return canaryA, nil
}

func TestKeychainValidationDoesNotReadTheSecret(t *testing.T) {
	fake := &fakeSecurity{accounts: map[string][]string{"svc": {"only"}}}
	restore := SetSecurityRunnerForTest(fake)
	defer restore()
	if err := validateCredentialReference(credentialRef{Source: "keychain", Name: "svc"}); err != nil {
		t.Fatal(err)
	}
	if fake.reads != 0 {
		t.Fatal("probe read the keychain secret")
	}
	got, err := resolveCredential(credentialRef{Source: "keychain", Name: "svc"})
	if err != nil || got != canaryA {
		t.Fatal("resolve failed")
	}
	if fake.reads != 1 {
		t.Fatalf("reads %d", fake.reads)
	}
}

func TestCompatibleCredentialFilesMustBeOutsideResolvedRoots(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("file credentials are unsupported")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(repo, "key")
	outside := filepath.Join(base, "key")
	for _, path := range []string{inside, outside} {
		if err := os.WriteFile(path, []byte(canaryA), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Bypass all path comparisons in this call: only the identity of the
	// descriptor chain can stop a credential that is actually in this root.
	rootHandle, err := os.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity, err := rootHandle.Stat()
	_ = rootHandle.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(inside, rootIdentity); !errors.Is(err, errCredentialWorkspace) {
		t.Fatalf("descriptor identity did not block the repository credential: %v", err)
	}
	if _, err := readCredentialFile(outside, rootIdentity); err != nil {
		t.Fatalf("safe descriptor path rejected: %v", err)
	}
	upper := filepath.Join(base, "REPO")
	if alias, err := os.Stat(upper); err == nil && os.SameFile(alias, rootIdentity) {
		if _, err := credentialFileOutside(filepath.Join(upper, "key"), []string{repo}); !errors.Is(err, errCredentialWorkspace) {
			t.Fatalf("case-insensitive workspace alias accepted: %v", err)
		}
	}
	repoAlias := filepath.Join(base, "alias")
	if err := os.Symlink(repo, repoAlias); err != nil {
		t.Fatal(err)
	}
	parentEscape := filepath.Join(repo, "outer")
	if err := os.Symlink(base, parentEscape); err != nil {
		t.Fatal(err)
	}
	for _, check := range [][2]string{
		{inside, repo},
		{inside, repoAlias},
		{filepath.Join(repoAlias, "key"), repo},
		{filepath.Join(parentEscape, "key"), repo},
	} {
		if _, err := credentialFileOutside(check[0], []string{check[1]}); err == nil || strings.Contains(err.Error(), canaryA) {
			t.Fatal("workspace file bypassed the credential location check")
		}
	}
	got, err := credentialFileOutside(outside, []string{repoAlias})
	if err != nil || got != outside {
		t.Fatal("separate protected credential file did not pass")
	}
	if err := os.Link(outside, filepath.Join(repo, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(got); err == nil || strings.Contains(err.Error(), canaryA) {
		t.Fatal("hard-linked credential file was read")
	}
}
