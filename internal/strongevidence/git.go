package strongevidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/OttoPrua/Cardex/internal/model"
)

const maxStrongSnapshotPaths = 16
const maxStrongSnapshotBytes = 4 << 20

// StrongSnapshotFile is trusted-controller-only evidence. The pinned commit
// and approved paths are private metadata; do not hand a runner the repository.
type StrongSnapshotFile struct {
	Path, SHA string
	Body      []byte
}

// ReadStrongSnapshot reads regular committed blobs by allowlisted repository
// path and exact commit from a trusted controller-owned bare object store.
// It never reads an active checkout, follows repository symlinks or expands
// directories. This pins evidence; it does not provide runner isolation.
func ReadStrongSnapshot(ctx context.Context, bareDir, commit string, paths []string) ([]StrongSnapshotFile, error) {
	if !filepath.IsAbs(bareDir) || !isOID(commit) || len(paths) == 0 || len(paths) > maxStrongSnapshotPaths {
		return nil, model.ErrStrongArtifact
	}
	for _, name := range []string{"alternates", "http-alternates"} {
		if _, err := os.Lstat(filepath.Join(bareDir, "objects", "info", name)); !errors.Is(err, os.ErrNotExist) {
			return nil, model.ErrStrongArtifact
		}
	}
	bare, err := BoundedGit(ctx, bareDir, 16, "rev-parse", "--is-bare-repository")
	if err != nil || !bytes.Equal(bytes.TrimSpace(bare), []byte("true")) {
		return nil, model.ErrStrongArtifact
	}
	format, err := BoundedGit(ctx, bareDir, 16, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return nil, model.ErrStrongArtifact
	}
	var oidLen int
	switch string(bytes.TrimSpace(format)) {
	case "sha1":
		oidLen = 40
	case "sha256":
		oidLen = 64
	default:
		return nil, model.ErrStrongArtifact
	}
	if len(commit) != oidLen {
		return nil, model.ErrStrongArtifact
	}
	// Git also resolves abbreviated OIDs and names. Require a commit whose
	// canonical storage ID is exactly the approved pin, not an alias or tag.
	resolved, err := BoundedGit(ctx, bareDir, oidLen+2, "rev-parse", "--verify", "--end-of-options", commit+"^{commit}")
	if err != nil || !bytes.Equal(bytes.TrimSpace(resolved), []byte(commit)) {
		return nil, model.ErrStrongArtifact
	}
	seen := make(map[string]bool, len(paths))
	out := make([]StrongSnapshotFile, 0, len(paths))
	total := 0
	for _, p := range paths {
		if !utf8.ValidString(p) || !fs.ValidPath(p) || p == "." || len(p) > 1024 ||
			strings.ContainsAny(p, "\\:\x00\r\n\t") || seen[p] {
			return nil, model.ErrStrongArtifact
		}
		seen[p] = true
		row, err := BoundedGit(ctx, bareDir, 2048, "ls-tree", "-z", "--full-tree", commit, "--", ":(literal)"+p)
		if err != nil || len(row) == 0 || row[len(row)-1] != 0 || bytes.Count(row, []byte{0}) != 1 {
			return nil, model.ErrStrongArtifact
		}
		header, path, ok := strings.Cut(string(row[:len(row)-1]), "\t")
		fields := strings.Split(header, " ")
		if !ok || path != p || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || len(fields[2]) != oidLen || !isOID(fields[2]) {
			return nil, model.ErrStrongArtifact
		}
		sizeRaw, err := BoundedGit(ctx, bareDir, 32, "cat-file", "-s", fields[2])
		size, numErr := strconv.Atoi(strings.TrimSpace(string(sizeRaw)))
		if err != nil || numErr != nil || size < 0 || size > model.StrongResultMax || total+size > maxStrongSnapshotBytes {
			return nil, model.ErrStrongArtifact
		}
		body, err := BoundedGit(ctx, bareDir, size, "cat-file", "blob", fields[2])
		if err != nil || len(body) != size {
			return nil, model.ErrStrongArtifact
		}
		total += size
		out = append(out, StrongSnapshotFile{Path: p, SHA: model.StrongArtifactDigest(body), Body: body})
	}
	return out, nil
}

func BoundedGit(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	// Cancellation only belongs to the Git child started by this invocation.
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(child, "git", append([]string{"--git-dir=" + dir, "--no-replace-objects", "-c", "protocol.allow=never"}, args...)...)
	// Inherit no Git overrides, credentials, or session state. Keep only OS
	// initialization variables needed by Windows Git, plus read-only options.
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_ALLOW_PROTOCOL="}
	for _, name := range []string{"SystemRoot", "WINDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(pipe, int64(limit)+1))
	if err != nil || len(out) > limit {
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("strong-thinking snapshot output limit")
	}
	if err := cmd.Wait(); err != nil {
		return nil, model.ErrStrongArtifact
	}
	return out, nil
}

func isOID(id string) bool {
	return (len(id) == 40 || len(id) == 64) && strings.Trim(id, "0123456789abcdef") == ""
}
