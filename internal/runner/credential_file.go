package runner

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func cleanCredentialPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return errors.New("credential file path must be a clean absolute path")
	}
	if strings.ContainsAny(path, "$`\r\n\x00;&|<>") {
		return errors.New("credential file path contains shell syntax")
	}
	return nil
}

func readCredentialFile(path string, protected ...os.FileInfo) (string, error) {
	if err := cleanCredentialPath(path); err != nil {
		return "", err
	}
	f, err := openCredentialFile(path, protected...)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4099))
	if err != nil || len(data) > 4098 {
		return "", errors.New("credential file cannot be read or exceeds the token size limit")
	}
	value := strings.TrimSuffix(string(data), "\n")
	value = strings.TrimSuffix(value, "\r")
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("credential file is empty or not a single line")
	}
	return value, nil
}

func probeCredentialFile(path string) error {
	if err := cleanCredentialPath(path); err != nil {
		return err
	}
	f, err := openCredentialFile(path)
	if err == nil {
		_ = f.Close()
	}
	return err
}

var errCredentialWorkspace = errors.New("credential file must be outside CardeX data, repositories and worktrees")

// credentialFileOutside checks existing root identities and then the actual
// opened descriptor chain without reading the credential.
func credentialFileOutside(path string, protected []string) (string, error) {
	real, identities, err := credentialFileLocation(path, protected)
	if err != nil {
		return "", err
	}
	f, err := openCredentialFile(real, identities...)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	return real, nil
}

// In addition to spelling checks for prospective roots, capture each
// existing root's filesystem identity. A case-insensitive or otherwise
// aliased path can have a different spelling but still be that directory.
func credentialFileLocation(path string, protected []string) (string, []os.FileInfo, error) {
	if err := cleanCredentialPath(path); err != nil {
		return "", nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", nil, errors.New("credential file must be an available regular file with no final symlink")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, errors.New("credential file path cannot be resolved")
	}
	var identities []os.FileInfo
	for _, dir := range protected {
		root, err := filepath.Abs(dir)
		if err != nil || dir == "" {
			return "", nil, errors.New("credential file workspace boundary cannot be verified")
		}
		if pathAtOrBelow(root, path) || pathAtOrBelow(root, real) {
			return "", nil, errCredentialWorkspace
		}
		canonical, err := resolveCredentialRoot(root)
		if err != nil {
			return "", nil, err
		}
		if pathAtOrBelow(canonical, path) || pathAtOrBelow(canonical, real) {
			return "", nil, errCredentialWorkspace
		}
		before, err := os.Stat(canonical)
		if os.IsNotExist(err) {
			// A prospective protected directory is covered lexically above.
			continue
		}
		if err != nil || !before.IsDir() {
			return "", nil, errors.New("credential file workspace boundary cannot be verified")
		}
		handle, err := os.Open(canonical)
		if err != nil {
			return "", nil, errors.New("credential file workspace boundary cannot be verified")
		}
		identity, statErr := handle.Stat()
		_ = handle.Close()
		if statErr != nil || !identity.IsDir() || !os.SameFile(before, identity) {
			return "", nil, errors.New("credential file workspace boundary cannot be verified")
		}
		identities = append(identities, identity)
	}
	return real, identities, nil
}

func pathAtOrBelow(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveCredentialRoot(path string) (string, error) {
	root, err := filepath.EvalSymlinks(path)
	if err == nil {
		return root, nil
	}
	// A configured worktree root need not have been created yet. Resolve
	// the existing ancestors so a path through an alias is still checked.
	parent := filepath.Dir(path)
	if !os.IsNotExist(err) || parent == path {
		return "", errors.New("credential file workspace boundary cannot be verified")
	}
	root, err = resolveCredentialRoot(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.Base(path)), nil
}
