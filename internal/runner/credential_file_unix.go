//go:build linux || darwin

package runner

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func openCredentialFile(path string, protected ...os.FileInfo) (*os.File, error) {
	// O_NOFOLLOW and fstat are bound to the same descriptor that will later
	// be read. Nonblocking open prevents a FIFO from hanging validation.
	f, err := openCredentialPath(path, protected)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("credential file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		_ = f.Close()
		return nil, errors.New("credential file is readable by group or others")
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || int(st.Uid) != os.Geteuid() || st.Nlink != 1 {
		_ = f.Close()
		return nil, errors.New("credential file must be exclusively owned by the service user and not hard-linked")
	}
	if err := verifyCredentialACL(path, info); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// Walk relative to held descriptors. Each directory actually used to open
// the file is checked by filesystem identity, not by path spelling, so a
// case-insensitive alias cannot bypass a repository or worktree boundary.
func openCredentialPath(path string, protected []os.FileInfo) (*os.File, error) {
	rootFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("credential file workspace boundary cannot be verified")
	}
	dir := os.NewFile(uintptr(rootFD), "credential ancestor")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		if len(protected) != 0 {
			identity, err := dir.Stat()
			if err != nil || !identity.IsDir() {
				_ = dir.Close()
				return nil, errors.New("credential file workspace boundary cannot be verified")
			}
			for _, root := range protected {
				if os.SameFile(identity, root) {
					_ = dir.Close()
					return nil, errCredentialWorkspace
				}
			}
		}
		last := i == len(parts)-1
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if !last {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(dir.Fd()), part, flags, 0)
		_ = dir.Close()
		if err != nil {
			return nil, errors.New("credential file is unavailable or a symlink")
		}
		if last {
			return os.NewFile(uintptr(fd), "credential file"), nil
		}
		dir = os.NewFile(uintptr(fd), "credential ancestor")
	}
	_ = dir.Close()
	return nil, errors.New("credential file is unavailable")
}
