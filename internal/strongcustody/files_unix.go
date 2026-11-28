//go:build linux || darwin

package strongcustody

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"golang.org/x/sys/unix"
)

const strongDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// Walk each path component relative to a pinned no-follow directory.
// Resolving a hostile ancestor and re-opening by absolute name is not safe.
func openStrongWorkspace(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, model.ErrStrongArtifact
	}
	fd, err := unix.Open("/", strongDirFlags, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, openErr := unix.Openat(fd, component, strongDirFlags, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "strong-workspace"), nil
}

func strongWorkspaceCurrent(dir *os.File, path string) bool {
	current, err := openStrongWorkspace(path)
	if err != nil {
		return false
	}
	defer current.Close()
	a, ae := dir.Stat()
	b, be := current.Stat()
	return ae == nil && be == nil && os.SameFile(a, b)
}

func strongSlotAbsent(dir *os.File, name string) bool {
	var st unix.Stat_t
	return unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) == unix.ENOENT
}

func readStrongAt(dir *os.File, name string, limit int64) ([]byte, error) {
	if name != strongResultName && name != strongReferencesName {
		return nil, model.ErrStrongArtifact
	}
	d := int(dir.Fd())
	var before unix.Stat_t
	if err := unix.Fstatat(d, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if !strongRegular(&before, limit) {
		return nil, model.ErrStrongArtifact
	}
	fd, err := unix.Openat(d, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, model.ErrStrongArtifact
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !strongRegular(&opened, limit) || !sameStrongFile(&before, &opened) {
		return nil, model.ErrStrongArtifact
	}
	info, err := f.Stat()
	if err != nil {
		return nil, model.ErrStrongArtifact
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	afterInfo, afterErr := f.Stat()
	var live, after unix.Stat_t
	if err != nil || afterErr != nil || int64(len(body)) != opened.Size || !info.ModTime().Equal(afterInfo.ModTime()) ||
		unix.Fstat(fd, &after) != nil || !sameStrongFile(&after, &opened) || !strongRegular(&after, limit) ||
		unix.Fstatat(d, name, &live, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameStrongFile(&live, &opened) || !strongRegular(&live, limit) {
		return nil, model.ErrStrongArtifact
	}
	return body, nil
}

func strongRegular(st *unix.Stat_t, limit int64) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Nlink == 1 && st.Size >= 0 && st.Size <= limit
}

func sameStrongFile(a, b *unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size
}

// Supported reports whether local output custody primitives exist. It is not a runner sandbox capability.
func Supported() bool { return true }
