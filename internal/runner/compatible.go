package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

const compatibleProtocol = model.CompatibleEngineProtocol
const compatiblePrefix = model.CompatibleEnginePrefix

// IsCompatibleBinding identifies the persistent protocol tag in engine_profile.
// It prevents a replaced/removed config entry from falling back to legacy auth.
func IsCompatibleBinding(ref string) bool {
	return model.HasCompatibleEngineBinding(ref)
}

// CompatibleBindingID returns the staged, nonsecret API endpoint identity
// recorded in the channel and in the dispatch that selected it.
func CompatibleBindingID(ref string) string {
	_, binding := splitCompatibleReference(ref)
	return binding
}

func splitCompatibleReference(ref string) (string, string) {
	ref = strings.TrimSpace(ref)
	if !IsCompatibleBinding(ref) {
		return ref, ""
	}
	name := strings.TrimPrefix(ref, compatiblePrefix)
	i := strings.LastIndexByte(name, '@')
	if i < 0 || len(name)-i-1 != sha256.Size*2 {
		return name, ""
	}
	digest := name[i+1:]
	if _, err := hex.DecodeString(digest); err != nil {
		return name, ""
	}
	return name[:i], digest
}

const emptyMCP = `{"mcpServers":{}}`

var (
	modelIDRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	familyRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	envNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	bearerRe   = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)
	relayKeyRe = regexp.MustCompile(`^cdx-relay-[0-9a-f]{64}$`)
	dnsLabelRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
)

type compatibleProfile struct {
	Protocol     string        `json:"protocol"`
	BaseURL      string        `json:"base_url"`
	DefaultModel string        `json:"default_model,omitempty"`
	Credential   credentialRef `json:"credential"`
}

type credentialRef struct {
	Source  string `json:"source"`
	Name    string `json:"name,omitempty"`
	Account string `json:"account,omitempty"`
	Path    string `json:"path,omitempty"`
}

// IsCompatibleEngine reports whether raw is an API-channel profile document
// rather than a legacy engines profile. A credential or protocol field is enough.
func IsCompatibleEngine(raw []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(raw), &probe) != nil {
		return false
	}
	for name := range probe {
		if strings.EqualFold(name, "protocol") || strings.EqualFold(name, "credential") {
			return true
		}
	}
	return false
}

// ValidateCompatibleJSON checks the compatible schema without resolving a secret.
func ValidateCompatibleJSON(raw []byte) error {
	prof, err := parseCompatible(raw)
	if err != nil {
		return err
	}
	return validateCompatible(prof)
}

func parseCompatible(raw []byte) (compatibleProfile, error) {
	if err := uniqueCompatibleFields(bytes.TrimSpace(raw)); err != nil {
		return compatibleProfile{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.DisallowUnknownFields()
	var prof compatibleProfile
	if err := dec.Decode(&prof); err != nil {
		return compatibleProfile{}, tidyProfileError(err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return compatibleProfile{}, errors.New("profile is not a valid anthropic-compatible document")
	}
	return prof, nil
}

func uniqueCompatibleFields(raw []byte) error {
	var walk func(*json.Decoder, int) error
	walk = func(dec *json.Decoder, depth int) error {
		if depth > 2 {
			return errors.New("profile contains nested fields")
		}
		open, err := dec.Token()
		if err != nil || open != json.Delim('{') {
			return errors.New("profile must be an object")
		}
		seen := map[string]bool{}
		for dec.More() {
			name, err := dec.Token()
			key, ok := name.(string)
			if err != nil || !ok || seen[strings.ToLower(key)] {
				return errors.New("profile contains a duplicate or invalid field")
			}
			seen[strings.ToLower(key)] = true
			if depth == 1 && strings.EqualFold(key, "credential") {
				if err := walk(dec, depth+1); err != nil {
					return err
				}
			} else {
				var unused json.RawMessage
				if err := dec.Decode(&unused); err != nil {
					return errors.New("profile contains an invalid value")
				}
			}
		}
		_, err = dec.Token()
		if err != nil {
			return errors.New("profile is not a valid object")
		}
		return nil
	}
	return walk(json.NewDecoder(bytes.NewReader(raw)), 1)
}

func tidyProfileError(error) error {
	// A malformed property name or value may itself be credential material.
	return errors.New("profile is not a valid anthropic-compatible document or contains a forbidden field")
}

func validateCompatible(prof compatibleProfile) error {
	if prof.Protocol != compatibleProtocol {
		if strings.TrimSpace(prof.Protocol) == "" {
			return errors.New("protocol is required")
		}
		return errors.New("profile protocol is not supported")
	}
	if _, err := validateEndpoint(prof.BaseURL); err != nil {
		return err
	}
	if prof.DefaultModel != "" {
		if err := validModelID(prof.DefaultModel); err != nil {
			return fmt.Errorf("default_model %w", err)
		}
	}
	return validateCredentialShape(prof.Credential)
}

func validateEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\\$`?#%") {
		return "", errors.New("base_url must be an https URL with a qualified host")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("base_url scheme must be https and include a host")
	}
	if u.User != nil {
		return "", errors.New("base_url must not contain credentials")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("base_url must not contain a query or fragment")
	}
	if strings.Contains(u.Path, "..") {
		return "", errors.New("base_url path is not allowed")
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || len(host) > 253 {
		return "", errors.New("base_url host must be a qualified DNS name")
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabelRe.MatchString(label) {
			return "", errors.New("base_url host must be a qualified DNS name")
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("base_url port is invalid")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", errors.New("base_url port is invalid")
		}
	}
	return raw, nil
}

func validModelID(id string) error {
	if !modelIDRe.MatchString(id) {
		return errors.New("model id is invalid")
	}
	switch strings.ToLower(id) {
	case "opus", "sonnet", "haiku", "fable":
		return errors.New("model id is a tier alias")
	}
	return nil
}

func validateCredentialShape(ref credentialRef) error {
	switch ref.Source {
	case "env":
		if ref.Account != "" || ref.Path != "" || !envNameRe.MatchString(ref.Name) || permittedChildKey(ref.Name) {
			return errors.New("credential env reference is invalid")
		}
	case "keychain":
		if ref.Path != "" || !secretLabelOK(ref.Name) || (ref.Account != "" && !secretLabelOK(ref.Account)) {
			return errors.New("credential keychain reference is invalid")
		}
	case "file":
		if ref.Name != "" || ref.Account != "" {
			return errors.New("credential file reference is invalid")
		}
		if err := cleanCredentialPath(ref.Path); err != nil {
			return err
		}
	default:
		return errors.New("credential source must be env, keychain, or file")
	}
	return nil
}

func secretLabelOK(name string) bool {
	if name == "" || len(name) > 128 || strings.TrimSpace(name) != name {
		return false
	}
	return !strings.HasPrefix(name, "-") && !strings.ContainsAny(name, "$`\r\n\x00;&|<> \t\"")
}

// validateCredentialReference checks that a reference can be resolved. It does not return
// the secret. Keychain ambiguity and file mode are rejected here.
func validateCredentialReference(ref credentialRef) error {
	if err := validateCredentialShape(ref); err != nil {
		return err
	}
	switch ref.Source {
	case "env":
		return nil
	case "file":
		return probeCredentialFile(ref.Path)
	case "keychain":
		accounts, err := securityRunner.Accounts(ref.Name)
		if err != nil {
			return err
		}
		_, err = chooseKeychainAccount(accounts, ref.Account)
		return err
	default:
		return errors.New("credential source must be env, keychain, or file")
	}
}

func chooseKeychainAccount(accounts []string, wanted string) (string, error) {
	var matched string
	for _, account := range accounts {
		if wanted != "" && account != wanted {
			continue
		}
		if matched != "" {
			return "", errKeychainAmbiguous
		}
		if !secretLabelOK(account) {
			return "", errors.New("keychain account reference cannot be verified")
		}
		matched = account
	}
	if matched == "" {
		return "", errKeychainNotFound
	}
	return matched, nil
}

func resolveCredential(ref credentialRef, protected ...os.FileInfo) (string, error) {
	if err := validateCredentialShape(ref); err != nil {
		return "", err
	}
	var value string
	var err error
	switch ref.Source {
	case "env":
		value = os.Getenv(ref.Name)
		if value == "" {
			return "", errors.New("credential environment variable is empty")
		}
	case "file":
		value, err = readCredentialFile(ref.Path, protected...)
	case "keychain":
		accounts, accErr := securityRunner.Accounts(ref.Name)
		if accErr != nil {
			return "", accErr
		}
		account, accErr := chooseKeychainAccount(accounts, ref.Account)
		if accErr != nil {
			return "", accErr
		}
		value, err = securityRunner.Password(ref.Name, account)
	}
	if err != nil {
		return "", err
	}
	if len(value) < 8 || len(value) > 4096 || !bearerRe.MatchString(value) {
		return "", errors.New("credential value is empty or not a valid bearer token")
	}
	return value, nil
}

// ResolveEngineJSON applies the same profile lookup the host uses at launch.
func ResolveEngineJSON(invalid, loaded bool, engines map[string]json.RawMessage, ref string) ([]byte, string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, ""
	}
	compatible := IsCompatibleBinding(ref)
	name, _ := splitCompatibleReference(ref)
	if invalid {
		return nil, "engines profile unparseable: config.json"
	}
	if raw, ok := engines[name]; ok {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, "engines profile unparseable"
		}
		if IsCompatibleEngine(trimmed) {
			if err := ValidateCompatibleJSON(trimmed); err != nil {
				return nil, err.Error()
			}
			return bytes.Clone(trimmed), ""
		}
		if compatible {
			return nil, "api channel profile cannot fall back to legacy authentication"
		}
		if _, ok := ParseEngineProfile(trimmed); !ok {
			return nil, "engines profile unparseable"
		}
		return bytes.Clone(trimmed), ""
	}
	if compatible {
		return nil, "api channel profile is missing; legacy authentication fallback refused"
	}
	if profile, ok := EnginePreset(name); ok {
		out, err := json.Marshal(profile)
		if err != nil {
			return nil, "engines profile unparseable"
		}
		return out, ""
	}
	if loaded && engines != nil {
		return nil, "engines profile unknown"
	}
	return nil, "engines profile missing"
}

// LoadEngineConfig reads the engines object from home/config.json.
// A missing file is an empty config. Invalid JSON sets invalid.
func LoadEngineConfig(home string) (invalid, loaded bool, engines map[string]json.RawMessage, err error) {
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil, nil
		}
		return false, false, nil, err
	}
	var cfg struct {
		Engines map[string]json.RawMessage `json:"engines"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return true, false, nil, nil
	}
	return false, true, cfg.Engines, nil
}

// CompatibleChildDropNames lists env var names an API child must not inherit:
// preset and configured credential refs. The secret values are not read.
func CompatibleChildDropNames(home string) []string {
	names := map[string]bool{}
	for _, preset := range EnginePresets() {
		if preset.AuthEnv != "" {
			names[preset.AuthEnv] = true
		}
	}
	_, _, engines, err := LoadEngineConfig(home)
	if err != nil {
		return sortedKeys(names)
	}
	for _, raw := range engines {
		var legacy struct {
			AuthEnv string `json:"auth_env"`
		}
		_ = json.Unmarshal(raw, &legacy)
		if envNameRe.MatchString(legacy.AuthEnv) {
			names[legacy.AuthEnv] = true
		}
		if !IsCompatibleEngine(raw) {
			continue
		}
		prof, err := parseCompatible(raw)
		if err != nil {
			continue
		}
		if prof.Credential.Source == "env" && envNameRe.MatchString(prof.Credential.Name) {
			names[prof.Credential.Name] = true
		}
	}
	return sortedKeys(names)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateChannelFamily validates the persisted routing family identifier.
func ValidateChannelFamily(family string) error {
	if !familyRe.MatchString(family) {
		return errors.New("family is invalid")
	}
	return nil
}

// ValidateChannelBinding checks a create or update before it is stored.
// Legacy profiles keep their previous rules. A compatible profile must resolve,
// match the claude runner, and have a family, model ids, and no route conflict.
// On success, an empty model list is filled from default_model. Secrets are not read.
func ValidateChannelBinding(home string, ch model.Channel, others []model.Channel, rows []model.Positioning, managedRoots ...string) (model.Channel, error) {
	if strings.TrimSpace(ch.EngineProfile) == "" {
		return ch, nil
	}
	if ch.Runner != model.RunnerClaude {
		return model.Channel{}, errors.New("engine profile requires runner claude")
	}
	invalid, loaded, engines, err := LoadEngineConfig(home)
	if err != nil {
		return model.Channel{}, err
	}
	raw, reason := ResolveEngineJSON(invalid, loaded, engines, ch.EngineProfile)
	if reason != "" {
		return model.Channel{}, errors.New(reason)
	}
	if !IsCompatibleEngine(raw) {
		return ch, nil
	}
	prof, err := parseCompatible(raw)
	if err != nil {
		return model.Channel{}, err
	}
	if err := validateCompatible(prof); err != nil {
		return model.Channel{}, err
	}
	if prof.Credential.Source == "file" {
		path, err := credentialFileOutside(prof.Credential.Path, append([]string{home}, managedRoots...))
		if err != nil {
			return model.Channel{}, err
		}
		prof.Credential.Path = path
	} else if err := validateCredentialReference(prof.Credential); err != nil {
		return model.Channel{}, err
	}
	family := strings.TrimSpace(ch.Family)
	if err := ValidateChannelFamily(family); err != nil {
		return model.Channel{}, err
	}
	ch.Family = family
	models := make([]string, 0, len(ch.Models))
	seen := map[string]bool{}
	for _, m := range ch.Models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		models = append(models, m)
	}
	if len(models) == 0 {
		if prof.DefaultModel == "" {
			return model.Channel{}, errors.New("model id is required")
		}
		models = []string{prof.DefaultModel}
	}
	for _, m := range models {
		if err := validModelID(m); err != nil {
			return model.Channel{}, err
		}
		if seen[m] {
			return model.Channel{}, errors.New("model id is duplicated")
		}
		seen[m] = true
	}
	ch.Models = models
	profileName, _ := splitCompatibleReference(ch.EngineProfile)
	ch.EngineProfile = compatiblePrefix + profileName + "@" + compatibleBindingID(profileName, prof, ch)
	if err := routingConflict(ch, others, rows); err != nil {
		return model.Channel{}, err
	}
	return ch, nil
}

func routingConflict(ch model.Channel, others []model.Channel, rows []model.Positioning) error {
	for _, other := range others {
		if other.ID == ch.ID || other.Family == "" || other.Family == ch.Family {
			continue
		}
		for _, m := range ch.Models {
			for _, om := range other.Models {
				if m == om {
					return fmt.Errorf("routing conflict: model %q on channel %s has family %q", m, other.ID, other.Family)
				}
			}
		}
	}
	for _, row := range rows {
		if row.Family == "" || row.Family == ch.Family {
			continue
		}
		for _, m := range ch.Models {
			if row.Model == m {
				return fmt.Errorf("routing conflict: positioning %s gives model %q family %q", row.ID, m, row.Family)
			}
		}
	}
	return nil
}

func compatibleBindingID(name string, prof compatibleProfile, ch model.Channel) string {
	prof.BaseURL, _ = validateEndpoint(prof.BaseURL)
	models := slices.Clone(ch.Models)
	slices.Sort(models)
	identity := struct {
		Channel, Profile, Family string
		Models                   []string
		Config                   compatibleProfile
	}{ch.ID, name, ch.Family, models, prof}
	encoded, _ := json.Marshal(identity)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// CompatibleFileRef reports whether the already validated profile needs a
// repository/worktree check before resolving its credential file.
func CompatibleFileRef(raw []byte) bool {
	prof, err := parseCompatible(raw)
	return err == nil && prof.Credential.Source == "file"
}

// ValidateCompatibleLaunch checks the approved identity, unchanged profile,
// model and card type before a credential or transport can be opened.
func ValidateCompatibleLaunch(spec LaunchSpec, ch model.Channel, approved string, managedRoots ...string) ([]byte, error) {
	if ch.Health != model.HealthOK || approved == "" || approved != CompatibleBindingID(ch.EngineProfile) {
		return nil, errors.New("api channel activation or dispatch binding changed; select an approved route again")
	}
	if err := compatibleToolPolicy(spec.CardKind); err != nil {
		return nil, err
	}
	prof, err := parseCompatible(spec.Script)
	if err != nil {
		return nil, err
	}
	if err := validateCompatible(prof); err != nil {
		return nil, err
	}
	if prof.Credential.Source == "file" {
		path, err := credentialFileOutside(prof.Credential.Path, managedRoots)
		if err != nil {
			return nil, err
		}
		prof.Credential.Path = path
	}
	name, _ := splitCompatibleReference(ch.EngineProfile)
	if approved != compatibleBindingID(name, prof, ch) {
		return nil, errors.New("api channel profile changed; stage the new identity and approve it before use")
	}
	if err := validModelID(spec.Model); err != nil {
		return nil, err
	}
	if !slices.Contains(ch.Models, spec.Model) {
		return nil, errors.New("api channel selected model is no longer bound to the channel")
	}
	if err := requireNoManagedClaudePolicy(); err != nil {
		return nil, err
	}
	// Hand the host the exact verified document, including the already
	// resolved credential path; it must not re-resolve a movable alias.
	return json.Marshal(prof)
}

func buildCompatibleClaude(spec LaunchSpec) ([]string, []string, error) {
	if err := ValidateCompatibleJSON(spec.Script); err != nil {
		return nil, nil, err
	}
	if err := isolatedConfigDir(spec.ConfigDir); err != nil {
		return nil, nil, err
	}
	argv, err := compatibleArgv(spec.CardKind, spec.Model)
	if err != nil {
		return nil, nil, err
	}
	env := compatibleEnv(os.Environ(), spec.DropEnv, spec.Model, spec.ConfigDir, spec.RelayURL, spec.RelayKey)
	if err := ProveAPIIsolation(spec, argv, env); err != nil {
		return nil, nil, err
	}
	return argv, env, nil
}

func isolatedConfigDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("api channel requires an isolated config directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return errors.New("api channel config directory is not an isolated directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		return errors.New("api channel config directory is not empty")
	}
	return nil
}

func permittedChildKey(key string) bool {
	switch strings.ToUpper(key) {
	case "PATH", "SYSTEMROOT", "WINDIR":
		return true
	default:
		return false
	}
}

func compatibleEnv(base, drop []string, modelName, configDir, relayURL, relayKey string) []string {
	skip := map[string]bool{}
	for _, name := range drop {
		skip[strings.ToUpper(name)] = true
	}
	var env []string
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if ok && permittedChildKey(key) && !skip[strings.ToUpper(key)] {
			env = append(env, kv)
		}
	}
	return append(env,
		"HOME="+configDir,
		"USERPROFILE="+configDir,
		"APPDATA="+configDir,
		"LOCALAPPDATA="+configDir,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_CACHE_HOME="+configDir,
		"CLAUDE_CONFIG_DIR="+configDir,
		"ANTHROPIC_BASE_URL="+relayURL,
		"ANTHROPIC_API_KEY="+relayKey,
		"ANTHROPIC_MODEL="+modelName,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1",
		"CLAUDE_CODE_DISABLE_LEGACY_MODEL_REMAP=1",
		"CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1",
	)
}

func compatibleToolPolicy(kind model.CardKind) error {
	switch kind {
	case model.KindReview, model.KindResearch, model.KindRanking:
		return nil
	case model.KindDev, model.KindRepair, model.KindSkeleton, model.KindRebase, model.KindSpike:
		return errors.New("api channel refuses development cards: workspace tool confinement cannot be proved")
	default:
		return errors.New("api channel refuses this card kind: tool policy cannot be proved")
	}
}

func compatibleArgv(kind model.CardKind, modelName string) ([]string, error) {
	if err := validModelID(modelName); err != nil {
		return nil, err
	}
	if err := compatibleToolPolicy(kind); err != nil {
		return nil, err
	}
	return []string{
		"claude", "-p", "--bare", "--restricted", "--output-format", "json",
		"--no-session-persistence", "--model", modelName,
		"--setting-sources", "", "--tools", "", "--permission-mode", "default",
		"--strict-mcp-config", "--mcp-config", emptyMCP, "--disallowedTools", "*",
	}, nil
}

// ProveAPIIsolation checks the generated argv and per-attempt transport. A
// failure means do not start; errors may never include the token or capability.
func ProveAPIIsolation(spec LaunchSpec, argv, env []string) error {
	if spec.ConfigDir == "" || !filepath.IsAbs(spec.ConfigDir) ||
		envValue(env, "CLAUDE_CONFIG_DIR") != spec.ConfigDir {
		return errors.New("api channel has no isolated config directory")
	}
	u, err := url.Parse(spec.RelayURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" ||
		u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || spec.RelayURL != "http://"+u.Host ||
		envValue(env, "ANTHROPIC_BASE_URL") != spec.RelayURL {
		return errors.New("api channel has no isolated loopback transport")
	}
	if !relayKeyRe.MatchString(spec.RelayKey) || envValue(env, "ANTHROPIC_API_KEY") != spec.RelayKey {
		return errors.New("api channel has no isolated transport capability")
	}
	want, err := compatibleArgv(spec.CardKind, spec.Model)
	if err != nil || !slices.Equal(want, argv) {
		return errors.New("api channel tool denial cannot be proved")
	}
	return nil
}

func envValue(env []string, key string) string {
	var got string
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, key) {
			got = v
		}
	}
	return got
}
