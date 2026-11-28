package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type optionalRepoProof struct {
	repo, admin         *os.Root
	repoInfo, adminInfo os.FileInfo
}

func proveOptionalRepo(ctx context.Context, path string) (*optionalRepoProof, error) {
	repo, info, err := openOptionalAbsoluteDir(path)
	if err != nil {
		return nil, errOptionalProof
	}
	admin, adminInfo, err := openOptionalChild(repo, ".git")
	if err != nil {
		repo.Close()
		return nil, errOptionalProof
	}
	p := &optionalRepoProof{repo: repo, admin: admin, repoInfo: info, adminInfo: adminInfo}
	if !p.safe(ctx, path) {
		p.close()
		return nil, errOptionalProof
	}
	return p, nil
}

func (p *optionalRepoProof) close() {
	_ = p.admin.Close()
	_ = p.repo.Close()
}

// Walk each component relative to a pinned parent. No Git command is used
// to discover the admin path, and a literal .git directory is required.
func openOptionalAbsoluteDir(path string) (*os.Root, os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "\\\\") {
		return nil, nil, errOptionalProof
	}
	volume := filepath.VolumeName(path)
	rootPath := volume + string(filepath.Separator)
	if path == rootPath {
		return nil, nil, errOptionalProof
	}
	dir, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, err
	}
	var st os.FileInfo
	for _, name := range strings.Split(strings.TrimPrefix(path, rootPath), string(filepath.Separator)) {
		next, info, e := openOptionalChild(dir, name)
		_ = dir.Close()
		if e != nil {
			return nil, nil, e
		}
		dir, st = next, info
	}
	return dir, st, nil
}

func openOptionalChild(parent *os.Root, name string) (*os.Root, os.FileInfo, error) {
	before, err := parent.Lstat(name)
	if err != nil || !before.IsDir() {
		return nil, nil, errOptionalProof
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := root.Stat(".")
	after, errAfter := parent.Lstat(name)
	if err != nil || errAfter != nil || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, nil, errOptionalProof
	}
	return root, before, nil
}

func (p *optionalRepoProof) safe(ctx context.Context, path string) bool {
	current, info, err := openOptionalAbsoluteDir(path)
	if err != nil {
		return false
	}
	defer current.Close()
	admin, ai, err := openOptionalChild(current, ".git")
	if err != nil {
		return false
	}
	defer admin.Close()
	if !os.SameFile(info, p.repoInfo) || !os.SameFile(ai, p.adminInfo) {
		return false
	}
	for _, name := range []string{"commondir", "config.worktree", "info/grafts", "objects/info/alternates", "objects/info/http-alternates"} {
		if _, err := admin.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	// Reject links and unsupported special files anywhere in the admin
	// tree before any project Git subprocess can interpret them.
	budget := 24000
	if !optionalPlainAdminTree(admin, ".", &budget, 0) {
		return false
	}
	cfg, err := optionalFileBytes(admin, "config")
	if err != nil {
		return false
	}
	return optionalConfigSafe(ctx, path, cfg, false)
}

func optionalPlainAdminTree(root *os.Root, path string, budget *int, depth int) bool {
	if depth > 32 {
		return false
	}
	d, err := root.Open(path)
	if err != nil {
		return false
	}
	defer d.Close()
	entries, err := d.ReadDir(*budget + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > *budget {
		return false
	}
	*budget -= len(entries)
	for _, item := range entries {
		info, err := root.Lstat(filepath.Join(path, item.Name()))
		if err != nil {
			return false
		}
		if info.IsDir() {
			if !optionalPlainAdminTree(root, filepath.Join(path, item.Name()), budget, depth+1) {
				return false
			}
		} else if !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func optionalFileBytes(root *os.Root, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errOptionalProof
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, errOptionalProof
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errOptionalProof
	}
	const limit = 128 << 10
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(b) > limit {
		return nil, errOptionalProof
	}
	return b, nil
}

// VerifyOptionalAcceptanceRepository qualifies only the configured local
// main repository. Acceptance must still use an exact detached candidate,
// sanitized no-replace/no-hook Git, and operator-configured commands only.
func VerifyOptionalAcceptanceRepository(ctx context.Context, repo string) bool {
	proof, err := proveOptionalRepo(ctx, repo)
	if err != nil {
		return false
	}
	defer proof.close()
	if _, err := optionalAdminStates(proof.admin, repo, ""); err != nil {
		return false
	}
	raw, err := optionalFileBytes(proof.admin, "config")
	return err == nil && optionalConfigSafe(ctx, repo, raw, true)
}

func optionalConfigSafe(ctx context.Context, repo string, raw []byte, acceptance bool) bool {
	// Parse only already-read bytes. The subprocess starts at the filesystem
	// root, not in the unverified project, and never follows Git includes.
	neutral := filepath.VolumeName(repo) + string(filepath.Separator)
	cmd := optionalGit(ctx, neutral, "config", "--file", "-", "--no-includes", "--null", "--list")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err != nil || len(out) > 128<<10 {
		return false
	}
	for _, item := range strings.Split(string(out), "\x00") {
		key, value, _ := strings.Cut(item, "\n")
		key = strings.ToLower(key)
		if acceptance {
			switch {
			case strings.HasPrefix(key, "filter."), strings.HasPrefix(key, "submodule."),
				key == "core.attributesfile", key == "core.autocrlf", key == "core.eol",
				key == "core.sparsecheckout", key == "core.sparsecheckoutcone",
				key == "index.sparse", key == "core.splitindex":
				return false
			}
		}
		switch {
		case strings.HasPrefix(key, "include."), strings.HasPrefix(key, "includeif."),
			key == "core.worktree", key == "extensions.worktreeconfig",
			key == "extensions.partialclone", key == "extensions.refstorage":
			return false
		case key == "core.bare" && value != "false":
			return false
		}
	}
	return true
}
