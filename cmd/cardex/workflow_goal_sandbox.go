package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type grokSandboxProfileDef struct {
	Name               string
	Extends            string
	ReadWrite          []string
	ReadOnly           []string
	Deny               []string
	RestrictNetwork    bool
	RestrictNetworkSet bool
	Source             string
}

func commandArgValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func goalLaunchSandboxSelector(t *Task) string {
	if t == nil || t.Goal == nil {
		return ""
	}
	return strings.TrimSpace(t.Goal.LaunchSandboxSelector)
}

func validateInvocationSandboxSelector(name string) error {
	if name == "" {
		return nil
	}
	if err := validateGrokWriteSandboxProfile(name); err != nil {
		return fmt.Errorf("%w: %v", errGoalUnsupportedTuple, err)
	}
	switch strings.ToLower(name) {
	case grokBuildWriteSandboxDefault, "devbox", grokBuildReadOnlySandboxDefault,
		grokBuildReadOnlySandboxMacOSNoopNetwork, "strict", "all", "any":
		return fmt.Errorf("%w: sandbox selector %q is broad/off-limits", errGoalUnsupportedTuple, name)
	}
	return nil
}

func canonicalGitCommonDir(dir string) string {
	common, ok := gitCommonDirKey(dir)
	if !ok {
		return ""
	}
	return common
}

func gitCommonDirOutsideWorktree(worktree, common string) bool {
	if strings.TrimSpace(common) == "" || strings.TrimSpace(worktree) == "" {
		return false
	}
	wt := filepath.Clean(worktree)
	cm := filepath.Clean(common)
	if samePath(wt, cm) {
		return false
	}
	sep := string(filepath.Separator)
	return !strings.HasPrefix(cm, wt+sep)
}

func sandboxGrantCovers(grant, target string) bool {
	g := filepath.Clean(strings.TrimSpace(grant))
	tg := filepath.Clean(strings.TrimSpace(target))
	if g == "" || tg == "" {
		return false
	}
	if samePath(g, tg) {
		return true
	}
	sep := string(filepath.Separator)
	prefix := g + sep
	if realG, err := filepath.EvalSymlinks(g); err == nil && realG != "" {
		g = filepath.Clean(realG)
		prefix = g + sep
	}
	if realT, err := filepath.EvalSymlinks(tg); err == nil && realT != "" {
		tg = filepath.Clean(realT)
	}
	return tg == g || strings.HasPrefix(tg, prefix)
}

func profileDigest(p grokSandboxProfileDef) string {
	rw := append([]string(nil), p.ReadWrite...)
	ro := append([]string(nil), p.ReadOnly...)
	deny := append([]string(nil), p.Deny...)
	sort.Strings(rw)
	sort.Strings(ro)
	sort.Strings(deny)
	var b strings.Builder
	fmt.Fprintf(&b, "name=%s\n", p.Name)
	fmt.Fprintf(&b, "extends=%s\n", p.Extends)
	fmt.Fprintf(&b, "restrict_network=%v\n", p.RestrictNetwork)
	fmt.Fprintf(&b, "read_write=%s\n", strings.Join(rw, ","))
	fmt.Fprintf(&b, "read_only=%s\n", strings.Join(ro, ","))
	fmt.Fprintf(&b, "deny=%s\n", strings.Join(deny, ","))
	return sha256Hex(b.String())
}

func grokUserSandboxTOML(t *Task) string {
	home := ""
	if t != nil && t.Goal != nil {
		home = strings.TrimSpace(t.Goal.GrokHome)
	}
	if home == "" {
		home = defaultGrokHome()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "sandbox.toml")
}

func grokProjectSandboxTOML(t *Task) string {
	if t == nil || strings.TrimSpace(t.Dir) == "" {
		return ""
	}
	return filepath.Join(t.Dir, ".grok", "sandbox.toml")
}

func loadEffectiveGrokSandboxProfiles(t *Task) (map[string]grokSandboxProfileDef, error) {
	out := map[string]grokSandboxProfileDef{}
	// Grok: when user and project define the same custom profile differently, the user file wins.
	for _, path := range []string{grokProjectSandboxTOML(t), grokUserSandboxTOML(t)} {
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("%w: read %s: %v", errGoalUnsupportedTuple, path, err)
		}
		parsed, err := parseGrokSandboxTOML(raw, path)
		if err != nil {
			return nil, err
		}
		for name, def := range parsed {
			out[name] = def
		}
	}
	return out, nil
}

func lookupExistingGrokSandboxProfile(t *Task, name string) (grokSandboxProfileDef, error) {
	var empty grokSandboxProfileDef
	profiles, err := loadEffectiveGrokSandboxProfiles(t)
	if err != nil {
		return empty, err
	}
	def, ok := profiles[name]
	if !ok {
		return empty, fmt.Errorf("%w: unknown sandbox profile %q", errGoalUnsupportedTuple, name)
	}
	return def, nil
}

func applyExistingSandboxProfileRules(def grokSandboxProfileDef, worktree, gitCommon string) error {
	if strings.EqualFold(def.Extends, grokBuildReadOnlySandboxDefault) ||
		strings.EqualFold(def.Extends, grokBuildReadOnlySandboxMacOSNoopNetwork) ||
		strings.EqualFold(def.Extends, "strict") {
		return fmt.Errorf("%w: sandbox profile %q is read-only/incompatible", errGoalUnsupportedTuple, def.Name)
	}
	if def.Extends != "" && !strings.EqualFold(def.Extends, grokBuildWriteSandboxDefault) {
		return fmt.Errorf("%w: sandbox profile %q is incompatible (extends %q)", errGoalUnsupportedTuple, def.Name, def.Extends)
	}
	if len(def.ReadWrite) == 0 && (len(def.ReadOnly) > 0 || len(def.Deny) > 0) {
		return fmt.Errorf("%w: sandbox profile %q is read-only", errGoalUnsupportedTuple, def.Name)
	}
	rw, err := normalizeLiteralGrants(def.Name, "read_write", def.ReadWrite)
	if err != nil {
		return err
	}
	ro, err := normalizeLiteralGrants(def.Name, "read_only", def.ReadOnly)
	if err != nil {
		return err
	}
	for _, grant := range rw {
		if grant == "/" || grant == filepath.VolumeName(grant)+string(filepath.Separator) {
			return fmt.Errorf("%w: sandbox profile %q is broad", errGoalUnsupportedTuple, def.Name)
		}
		if home, herr := os.UserHomeDir(); herr == nil && home != "" && (samePath(grant, home) || grant == filepath.Clean(home)) {
			return fmt.Errorf("%w: sandbox profile %q is broad", errGoalUnsupportedTuple, def.Name)
		}
	}
	if err := validateDenyPatterns(def.Name, def.Deny); err != nil {
		return err
	}
	if gitCommonDirOutsideWorktree(worktree, gitCommon) {
		covered := false
		for _, grant := range rw {
			if sandboxGrantCovers(grant, gitCommon) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("%w: sandbox profile %q lacks a literal directory grant covering git common-dir", errGoalUnsupportedTuple, def.Name)
		}
		for _, grant := range ro {
			if sandboxGrantCovers(grant, gitCommon) {
				return fmt.Errorf("%w: sandbox profile %q read_only overlaps git common-dir grant", errGoalUnsupportedTuple, def.Name)
			}
		}
		for _, pattern := range def.Deny {
			covers, derr := denyCoversPath(pattern, gitCommon)
			if derr != nil {
				return fmt.Errorf("%w: sandbox profile %q deny %v", errGoalUnsupportedTuple, def.Name, derr)
			}
			if covers {
				return fmt.Errorf("%w: sandbox profile %q deny overlaps git common-dir grant", errGoalUnsupportedTuple, def.Name)
			}
		}
	}
	return nil
}

func resolveExistingSandboxSelector(t *Task, selector string) (name, digest, gitCommon string, err error) {
	if err := validateInvocationSandboxSelector(selector); err != nil {
		return "", "", "", err
	}
	def, err := lookupExistingGrokSandboxProfile(t, selector)
	if err != nil {
		return "", "", "", err
	}
	worktree := ""
	if t != nil {
		worktree = t.Dir
	}
	gitCommon = canonicalGitCommonDir(worktree)
	if err := applyExistingSandboxProfileRules(def, worktree, gitCommon); err != nil {
		return "", "", "", err
	}
	return def.Name, profileDigest(def), gitCommon, nil
}

func bindGoalSandboxEvidence(root string, cfg *Config, t *Task) error {
	if t == nil || t.Goal == nil {
		return fmt.Errorf("%w: missing Goal task", errWorkflowMalformed)
	}
	sandbox, _, err := resolveManualGrokTuple(cfg, t)
	if err != nil {
		return err
	}
	digest := sha256Hex("sandbox=" + sandbox + "\n")
	gitCommon := canonicalGitCommonDir(t.Dir)
	if sel := goalLaunchSandboxSelector(t); sel != "" {
		name, d, common, rerr := resolveExistingSandboxSelector(t, sel)
		if rerr != nil {
			return rerr
		}
		sandbox = name
		digest = d
		gitCommon = common
	}
	t.Goal.SandboxProfile = sandbox
	t.Goal.SandboxProfileDigest = digest
	t.Goal.GitCommonDir = gitCommon
	t.Goal.CardexRoot = root
	t.Goal.SandboxWorktree = t.Dir
	t.Goal.LauncherSandbox = sandbox
	return nil
}

func verifySandboxLauncherEvidence(root string, t *Task, args []string) error {
	if t == nil || t.Goal == nil {
		return fmt.Errorf("%w: missing Goal task", errWorkflowMalformed)
	}
	got := commandArgValue(args, "--sandbox")
	if strings.TrimSpace(t.Goal.SandboxProfile) == "" {
		return fmt.Errorf("%w: missing bound sandbox profile evidence", errGoalUnsupportedTuple)
	}
	if got != t.Goal.SandboxProfile {
		return fmt.Errorf("%w: launcher sandbox %q does not match bound profile %q", errGoalUnsupportedTuple, got, t.Goal.SandboxProfile)
	}
	if t.Goal.CardexRoot != "" && t.Goal.CardexRoot != root {
		return fmt.Errorf("%w: cardex root evidence mismatch", errGoalUnsupportedTuple)
	}
	if t.Goal.SandboxWorktree != "" && t.Goal.SandboxWorktree != t.Dir {
		return fmt.Errorf("%w: worktree evidence mismatch", errGoalUnsupportedTuple)
	}
	if t.Goal.GitCommonDir != "" {
		current := canonicalGitCommonDir(t.Dir)
		if current != t.Goal.GitCommonDir && !samePath(current, t.Goal.GitCommonDir) {
			return fmt.Errorf("%w: git common-dir evidence mismatch", errGoalUnsupportedTuple)
		}
	}
	if sel := goalLaunchSandboxSelector(t); sel != "" && sel != t.Goal.SandboxProfile {
		return fmt.Errorf("%w: selector %q does not match bound profile %q", errGoalUnsupportedTuple, sel, t.Goal.SandboxProfile)
	}
	t.Goal.LauncherSandbox = got
	return nil
}

func parseGrokSandboxTOML(raw []byte, source string) (map[string]grokSandboxProfileDef, error) {
	out := map[string]grokSandboxProfileDef{}
	seenKeys := map[string]map[string]bool{}
	lines := strings.Split(string(raw), "\n")
	current := ""
	skip := false
	i := 0
	for i < len(lines) {
		lineNo := i + 1
		line := strings.TrimSpace(lines[i])
		i++
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || strings.Count(line, "[") != 1 {
				return nil, fmt.Errorf("%w: %s:%d unsupported table header", errGoalUnsupportedTuple, source, lineNo)
			}
			sec := strings.TrimSpace(line[1 : len(line)-1])
			if sec == "shell_environment_policy" {
				current = ""
				skip = true
				continue
			}
			name, err := grokSandboxProfileSection(sec)
			if err != nil {
				return nil, fmt.Errorf("%w: %s:%d %v", errGoalUnsupportedTuple, source, lineNo, err)
			}
			if _, exists := out[name]; exists {
				return nil, fmt.Errorf("%w: %s:%d duplicate table [profiles.%s]", errGoalUnsupportedTuple, source, lineNo, name)
			}
			skip = false
			current = name
			out[name] = grokSandboxProfileDef{Name: name, Source: source}
			seenKeys[name] = map[string]bool{}
			continue
		}
		if skip {
			if !strings.Contains(line, "=") {
				return nil, fmt.Errorf("%w: %s:%d unsupported syntax", errGoalUnsupportedTuple, source, lineNo)
			}
			continue
		}
		if current == "" {
			return nil, fmt.Errorf("%w: %s:%d key outside a [profiles.NAME] table", errGoalUnsupportedTuple, source, lineNo)
		}
		key, val, err := splitTOMLKeyValue(line)
		if err != nil {
			return nil, fmt.Errorf("%w: %s:%d %v", errGoalUnsupportedTuple, source, lineNo, err)
		}
		if strings.HasPrefix(val, "[") && !strings.Contains(val, "]") {
			joined := val
			for i < len(lines) && !strings.Contains(joined, "]") {
				joined += " " + strings.TrimSpace(lines[i])
				i++
			}
			if !strings.Contains(joined, "]") {
				return nil, fmt.Errorf("%w: %s:%d unclosed array", errGoalUnsupportedTuple, source, lineNo)
			}
			val = joined
		}
		if seenKeys[current][key] {
			return nil, fmt.Errorf("%w: %s:%d duplicate key %q in profile %q", errGoalUnsupportedTuple, source, lineNo, key, current)
		}
		seenKeys[current][key] = true
		def := out[current]
		switch key {
		case "extends":
			s, perr := parseTOMLQuotedString(val)
			if perr != nil {
				return nil, fmt.Errorf("%w: %s:%d extends: %v", errGoalUnsupportedTuple, source, lineNo, perr)
			}
			def.Extends = s
		case "restrict_network":
			b, perr := parseTOMLBool(val)
			if perr != nil {
				return nil, fmt.Errorf("%w: %s:%d restrict_network: %v", errGoalUnsupportedTuple, source, lineNo, perr)
			}
			def.RestrictNetwork = b
			def.RestrictNetworkSet = true
		case "read_write":
			arr, perr := parseTOMLQuotedStringArray(val)
			if perr != nil {
				return nil, fmt.Errorf("%w: %s:%d read_write: %v", errGoalUnsupportedTuple, source, lineNo, perr)
			}
			def.ReadWrite = arr
		case "read_only":
			arr, perr := parseTOMLQuotedStringArray(val)
			if perr != nil {
				return nil, fmt.Errorf("%w: %s:%d read_only: %v", errGoalUnsupportedTuple, source, lineNo, perr)
			}
			def.ReadOnly = arr
		case "deny":
			arr, perr := parseTOMLQuotedStringArray(val)
			if perr != nil {
				return nil, fmt.Errorf("%w: %s:%d deny: %v", errGoalUnsupportedTuple, source, lineNo, perr)
			}
			def.Deny = arr
		default:
			return nil, fmt.Errorf("%w: %s:%d unsupported key %q", errGoalUnsupportedTuple, source, lineNo, key)
		}
		out[current] = def
	}
	return out, nil
}

func grokSandboxProfileSection(sec string) (string, error) {
	sec = strings.TrimSpace(sec)
	const prefix = "profiles."
	if !strings.HasPrefix(sec, prefix) {
		return "", fmt.Errorf("unsupported table %q", sec)
	}
	rest := sec[len(prefix):]
	if strings.Contains(rest, ".") {
		return "", fmt.Errorf("unsupported nested table [profiles.%s]", rest)
	}
	if strings.HasPrefix(rest, "\"") && strings.HasSuffix(rest, "\"") && len(rest) >= 2 {
		s, err := parseTOMLQuotedString(rest)
		if err != nil {
			return "", err
		}
		rest = s
	}
	if !grokSandboxProfileIdent(rest) {
		return "", fmt.Errorf("invalid profile name %q", rest)
	}
	switch strings.ToLower(rest) {
	case grokBuildWriteSandboxDefault, grokBuildReadOnlySandboxDefault, "devbox", "strict", "off":
		return "", fmt.Errorf("custom profile cannot reuse built-in name %q", rest)
	}
	return rest, nil
}

func splitTOMLKeyValue(line string) (key, val string, err error) {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", "", fmt.Errorf("unsupported syntax")
	}
	key = strings.TrimSpace(line[:eq])
	val = strings.TrimSpace(line[eq+1:])
	if key == "" || val == "" || strings.ContainsAny(key, " \t.") {
		return "", "", fmt.Errorf("unsupported key")
	}
	return key, val, nil
}

func parseTOMLQuotedString(v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return "", fmt.Errorf("expected quoted string")
	}
	s, err := strconv.Unquote(v)
	if err != nil {
		return "", fmt.Errorf("malformed string")
	}
	return s, nil
}

func parseTOMLBool(v string) (bool, error) {
	switch strings.TrimSpace(v) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("expected boolean")
	}
}

func parseTOMLQuotedStringArray(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, fmt.Errorf("expected string array")
	}
	inner := strings.TrimSpace(v[1 : len(v)-1])
	if inner == "" {
		return []string{}, nil
	}
	var out []string
	for inner != "" {
		inner = strings.TrimSpace(strings.TrimPrefix(inner, ","))
		if inner == "" {
			break
		}
		if !strings.HasPrefix(inner, "\"") {
			return nil, fmt.Errorf("array entries must be quoted strings")
		}
		end := 1
		esc := false
		for end < len(inner) {
			c := inner[end]
			if esc {
				esc = false
				end++
				continue
			}
			if c == '\\' {
				esc = true
				end++
				continue
			}
			if c == '"' {
				break
			}
			end++
		}
		if end >= len(inner) {
			return nil, fmt.Errorf("unclosed string in array")
		}
		s, err := parseTOMLQuotedString(inner[:end+1])
		if err != nil {
			return nil, err
		}
		out = append(out, s)
		inner = strings.TrimSpace(inner[end+1:])
		if inner == "" {
			break
		}
		if !strings.HasPrefix(inner, ",") {
			return nil, fmt.Errorf("expected comma in array")
		}
		inner = strings.TrimSpace(inner[1:])
	}
	return out, nil
}

func normalizeLiteralGrants(profile, field string, paths []string) ([]string, error) {
	var out []string
	for _, raw := range paths {
		if raw != strings.TrimSpace(raw) {
			return nil, fmt.Errorf("%w: sandbox profile %q %s has significant whitespace", errGoalUnsupportedTuple, profile, field)
		}
		p := raw
		if strings.HasSuffix(p, "/**") {
			p = strings.TrimSuffix(p, "/**")
		} else if strings.HasSuffix(p, "/*") {
			p = strings.TrimSuffix(p, "/*")
		}
		if p == "" {
			p = "/"
		}
		if strings.ContainsAny(p, "*?[") {
			return nil, fmt.Errorf("%w: sandbox profile %q %s must be a literal directory", errGoalUnsupportedTuple, profile, field)
		}
		out = append(out, filepath.Clean(p))
	}
	return out, nil
}

func validateDenyPatterns(profile string, patterns []string) error {
	for _, p := range patterns {
		if p != strings.TrimSpace(p) || p == "" {
			return fmt.Errorf("%w: sandbox profile %q deny has empty or padded entry", errGoalUnsupportedTuple, profile)
		}
		if strings.ContainsAny(p, "{}\\") || strings.Contains(p, "//") {
			return fmt.Errorf("%w: sandbox profile %q deny uses unsupported glob syntax", errGoalUnsupportedTuple, profile)
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == "." || seg == ".." {
				return fmt.Errorf("%w: sandbox profile %q deny uses unsupported path segment", errGoalUnsupportedTuple, profile)
			}
		}
	}
	return nil
}

func denyCoversPath(pattern, target string) (bool, error) {
	pattern = strings.TrimSpace(pattern)
	target = filepath.Clean(strings.TrimSpace(target))
	if pattern == "" || target == "" {
		return false, nil
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return sandboxGrantCovers(pattern, target), nil
	}
	if strings.ContainsAny(pattern, "{}\\") {
		return false, fmt.Errorf("unsupported deny glob")
	}
	return denyGlobMatches(pattern, target), nil
}

func denyGlobMatches(pattern, target string) bool {
	patSegs := strings.Split(filepath.ToSlash(pattern), "/")
	tgtSegs := strings.Split(filepath.ToSlash(target), "/")
	return denyGlobMatchSegs(patSegs, tgtSegs)
}

func denyGlobMatchSegs(pat, tgt []string) bool {
	if len(pat) == 0 {
		return len(tgt) == 0
	}
	if pat[0] == "**" {
		if denyGlobMatchSegs(pat[1:], tgt) {
			return true
		}
		if len(tgt) == 0 {
			return false
		}
		return denyGlobMatchSegs(pat, tgt[1:])
	}
	if len(tgt) == 0 {
		return false
	}
	if !denyGlobSeg(pat[0], tgt[0]) {
		return false
	}
	return denyGlobMatchSegs(pat[1:], tgt[1:])
}

func denyGlobSeg(pat, tgt string) bool {
	pi, ti := 0, 0
	for pi < len(pat) {
		if pat[pi] == '*' {
			if pi+1 == len(pat) {
				return true
			}
			for ti <= len(tgt) {
				if denyGlobSeg(pat[pi+1:], tgt[ti:]) {
					return true
				}
				ti++
			}
			return false
		}
		if ti >= len(tgt) {
			return false
		}
		if pat[pi] == '?' {
			pi++
			ti++
			continue
		}
		if pat[pi] == '[' {
			end := strings.IndexByte(pat[pi:], ']')
			if end <= 1 {
				return false
			}
			class := pat[pi+1 : pi+end]
			neg := strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^")
			if neg {
				class = class[1:]
			}
			matched := strings.ContainsRune(class, rune(tgt[ti]))
			if neg {
				matched = !matched
			}
			if !matched {
				return false
			}
			pi += end + 1
			ti++
			continue
		}
		if pat[pi] != tgt[ti] {
			return false
		}
		pi++
		ti++
	}
	return ti == len(tgt)
}
