package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A one-use repair of directory-only read grants, bound by the existing
// bootstrap authorization. It grants no additional source material or writes.
type bootstrapSandboxRepair struct {
	OriginalProfilePath   string                  `json:"original_profile_path"`
	OriginalProfileSHA256 string                  `json:"original_profile_sha256"`
	LaunchProfileDigest   string                  `json:"launch_profile_digest"`
	Materials             []bootstrapMaterialCopy `json:"materials"`
}

type bootstrapMaterialCopy struct {
	Source string `json:"source"`
	Copy   string `json:"copy"`
	SHA256 string `json:"sha256"`
}

func prepareBootstrapSandbox(root string, wf *WorkflowRecord, t *Task, auth *bootstrapRecoveryAuthorization) error {
	name := t.Goal.SandboxProfile
	if auth.SandboxRepair == nil {
		// Built-ins need no invocation selector. A custom profile must survive
		// the task reload; otherwise the launch would select the shared default.
		if validateInvocationSandboxSelector(name) == nil {
			t.Goal.LaunchSandboxSelector = name
			_, digest, _, err := resolveExistingSandboxSelector(t, name)
			if err != nil {
				return err
			}
			if digest != t.Goal.SandboxProfileDigest {
				return bootstrapRecoveryRefused("changed launch profile")
			}
		}
		return nil
	}
	fix := auth.SandboxRepair
	if validateInvocationSandboxSelector(name) != nil || len(fix.Materials) == 0 {
		return bootstrapRecoveryRefused("directory repair requires an existing custom profile and exact materials")
	}
	originalSHA, originalBytes, err := fileSHA256Hex(fix.OriginalProfilePath)
	if err != nil || originalSHA != fix.OriginalProfileSHA256 {
		return bootstrapRecoveryRefused("original profile snapshot drift")
	}
	profiles, err := parseGrokSandboxTOML(originalBytes, fix.OriginalProfilePath)
	if err != nil {
		return err
	}
	original, ok := profiles[name]
	if !ok || profileDigest(original) != t.Goal.SandboxProfileDigest {
		return bootstrapRecoveryRefused("original profile binding mismatch")
	}
	t.Goal.LaunchSandboxSelector = name
	_, digest, _, err := resolveExistingSandboxSelector(t, name)
	if err != nil {
		return err
	}
	if digest != fix.LaunchProfileDigest {
		return bootstrapRecoveryRefused("changed repair launch profile")
	}
	current, err := lookupExistingGrokSandboxProfile(t, name)
	if err != nil {
		return err
	}
	unchanged := current
	unchanged.ReadOnly = original.ReadOnly
	if profileDigest(unchanged) != profileDigest(original) {
		return bootstrapRecoveryRefused("repair changed non-read-only permissions")
	}
	worktree, err := filepath.EvalSymlinks(t.Dir)
	if err != nil {
		return err
	}
	worktree = filepath.Clean(worktree)
	allowedSources := map[string]bool{}
	for _, path := range original.ReadOnly {
		allowedSources[path] = true
	}
	expected := map[string]bool{}
	for path := range allowedSources {
		expected[path] = true
	}
	seen := map[string]bool{}
	directoryFiles := map[string]map[string]bool{}
	for _, material := range fix.Materials {
		if seen[material.Source] || !allowedSources[material.Source] || !filepath.IsAbs(material.Source) || !filepath.IsAbs(material.Copy) || filepath.Clean(material.Copy) != material.Copy {
			return bootstrapRecoveryRefused("repair material is not a unique original file grant")
		}
		seen[material.Source] = true
		for _, path := range []string{material.Source, material.Copy} {
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return bootstrapRecoveryRefused("repair materials must be regular files, not links")
			}
			sum, _, err := fileSHA256Hex(path)
			if err != nil || sum != material.SHA256 {
				return bootstrapRecoveryRefused("repair material bytes changed")
			}
		}
		dir := filepath.Dir(material.Copy)
		real, err := filepath.EvalSymlinks(dir)
		rel, relErr := filepath.Rel(filepath.Clean(t.Dir), dir)
		if err != nil || relErr != nil || filepath.Join(worktree, rel) != real || !strings.HasPrefix(real, worktree+string(filepath.Separator)) {
			return bootstrapRecoveryRefused("repair directory must be isolated inside the owned worktree")
		}
		if directoryFiles[dir] == nil {
			directoryFiles[dir] = map[string]bool{}
		}
		if directoryFiles[dir][filepath.Base(material.Copy)] {
			return bootstrapRecoveryRefused("duplicate repair copy")
		}
		directoryFiles[dir][filepath.Base(material.Copy)] = true
		delete(expected, material.Source)
		expected[dir] = true
	}
	for dir, files := range directoryFiles {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != len(files) {
			return bootstrapRecoveryRefused("repair directory has extra contents")
		}
		for _, entry := range entries {
			if !files[entry.Name()] || !entry.Type().IsRegular() {
				return bootstrapRecoveryRefused("repair directory has extra or linked contents")
			}
		}
	}
	actual := map[string]bool{}
	for _, path := range current.ReadOnly {
		actual[path] = true
	}
	if len(actual) != len(expected) {
		return bootstrapRecoveryRefused("repair widened read-only grants")
	}
	for path := range expected {
		if !actual[path] {
			return bootstrapRecoveryRefused("repair changed unrelated read-only grants")
		}
	}
	contract, err := readRegularFileNoBlock(filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt"))
	if err != nil || sha256Hex(string(contract)) != t.Goal.InputDigest {
		return bootstrapRecoveryRefused("frozen contract drift")
	}
	materials := append([]bootstrapMaterialCopy(nil), fix.Materials...)
	sort.Slice(materials, func(i, j int) bool { return materials[i].Source < materials[j].Source })
	var b strings.Builder
	b.WriteString("# Same-byte material paths for this authorized bootstrap recovery\n\nFor every reference to an original material path below, including references inside those materials, read its verified copy instead. Original paths are not claimed accessible. Verify each copy SHA256 before use. Scope, route, model, budget and original deadline are unchanged.\n\n")
	for _, material := range materials {
		fmt.Fprintf(&b, "- Original %q => copy %q; SHA256 %s\n", material.Source, material.Copy, material.SHA256)
	}
	fmt.Fprintf(&b, "\nOriginal profile digest: %s\nLaunch profile digest: %s\nOriginal frozen contract SHA256: %s\n\n# Original frozen contract, unchanged\n\n", t.Goal.SandboxProfileDigest, digest, t.Goal.InputDigest)
	b.Write(contract)
	dir := filepath.Join(worktree, ".cardex-stage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return bootstrapRecoveryRefused("launch envelope directory is linked")
	}
	path := filepath.Join(dir, "bootstrap-launch-"+auth.consumeDigest()[:16]+".md")
	content := []byte(b.String())
	if old, err := os.ReadFile(path); err == nil {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return bootstrapRecoveryRefused("launch envelope is not a regular file")
		}
		if string(old) != string(content) {
			return bootstrapRecoveryRefused("launch envelope already exists with different bytes")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = f.Write(content)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	t.Goal.RecoveryLaunchProfileDigest = digest
	t.Goal.RecoveryLaunchContractPath = path
	t.Goal.RecoveryLaunchContractDigest = sha256Hex(string(content))
	t.Goal.RecoveryLaunchAuthorization = auth
	return nil
}

func goalLaunchContractReference(root string, wf *WorkflowRecord, t *Task) (path, digest string) {
	if t.Goal.RecoveryLaunchContractPath != "" {
		return t.Goal.RecoveryLaunchContractPath, t.Goal.RecoveryLaunchContractDigest
	}
	return filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt"), t.Goal.InputDigest
}
