package store

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const snapshotReadFlags = unix.O_NOATIME

type linuxSnapshotWatch struct {
	anchor    *os.File
	inotify   int
	dir, base string
	identity  os.FileInfo
}

func prepareStrictSnapshotWatch(path string) (*strictSnapshotGuard, error) {
	watch, err := openLinuxSnapshotWatch(path, true)
	if err != nil {
		return nil, err
	}
	return &strictSnapshotGuard{watch.pinnedPath(), watch.close, watch.changed, watch.watchFile}, nil
}

func linuxOpenSnapshotDir(path string) (*os.File, error) {
	// Resolve one component at a time, never by readlink or by a syscall
	// that follows an alias. O_PATH does not enumerate/read directories.
	// A pointer-based openat2 cannot be flag-checked by our seccomp filter;
	// single-component openat has equivalent no-symlink behavior here.
	if !filepath.IsAbs(path) {
		return nil, unix.EINVAL
	}
	const flags = unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func localSnapshotFD(fd uintptr) bool {
	var fs unix.Statfs_t
	if unix.Fstatfs(int(fd), &fs) != nil {
		return false
	}
	switch uint32(fs.Type) {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC,
		unix.TMPFS_MAGIC, unix.F2FS_SUPER_MAGIC, unix.OVERLAYFS_SUPER_MAGIC:
		return true
	}
	return false
}

func singleSnapshotFileLink(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1
}

func procSnapshotFD(fd uintptr) string { return "/proc/self/fd/" + strconv.FormatUint(uint64(fd), 10) }

func (w *linuxSnapshotWatch) pinnedPath() string {
	return filepath.Join(procSnapshotFD(w.anchor.Fd()), w.base)
}

func (w *linuxSnapshotWatch) watchFile(file *os.File) error {
	// Inode watches also cover modifications through another name. Any
	// shared links are refused; this additional watch detects transient ones.
	if _, err := unix.InotifyAddWatch(w.inotify, procSnapshotFD(file.Fd()),
		unix.IN_MODIFY|unix.IN_ATTRIB|unix.IN_CLOSE_WRITE|unix.IN_MOVE_SELF|unix.IN_DELETE_SELF|unix.IN_UNMOUNT); err != nil {
		return ErrSnapshotUnavailable
	}
	info, err := file.Stat()
	if err != nil || !singleSnapshotFileLink(info) {
		return ErrSnapshotUnavailable
	}
	return nil
}

func openLinuxSnapshotWatch(path string, strict bool) (*linuxSnapshotWatch, error) {
	dir, base := filepath.Dir(path), filepath.Base(path)
	if !filepath.IsAbs(dir) {
		return nil, ErrSnapshotUnavailable
	}
	// Anchor both inotify and (in the live VFS) source opens to this exact
	// directory. An absolute pathname cannot silently point elsewhere.
	f, err := linuxOpenSnapshotDir(dir)
	if err == unix.ENOENT {
		return nil, ErrSnapshotNotFound
	}
	if err != nil {
		return nil, ErrSnapshotUnavailable
	}
	st, err := f.Stat()
	if err != nil || !st.IsDir() || !localSnapshotFD(f.Fd()) {
		_ = f.Close()
		return nil, ErrSnapshotUnavailable
	}
	inotify, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		_ = f.Close()
		return nil, ErrSnapshotUnavailable
	}
	flags := uint32(unix.IN_ONLYDIR | unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO |
		unix.IN_MOVE_SELF | unix.IN_DELETE_SELF | unix.IN_UNMOUNT)
	if strict {
		// The checkpointed original must remain quiescent, even when an
		// uncooperative writer restores the original bytes before we check.
		flags |= unix.IN_MODIFY | unix.IN_ATTRIB | unix.IN_CLOSE_WRITE
	}
	if _, err = unix.InotifyAddWatch(inotify, procSnapshotFD(f.Fd()), flags); err != nil {
		_ = unix.Close(inotify)
		_ = f.Close()
		return nil, ErrSnapshotUnavailable
	}
	watch := &linuxSnapshotWatch{anchor: f, inotify: inotify, dir: dir, base: base, identity: st}
	if watch.changed() {
		watch.close()
		return nil, ErrSnapshotUnavailable
	}
	return watch, nil
}

func (w *linuxSnapshotWatch) close() {
	_ = unix.Close(w.inotify)
	_ = w.anchor.Close()
}

func (w *linuxSnapshotWatch) changed() bool {
	named, err := linuxOpenSnapshotDir(w.dir)
	if err != nil {
		return true
	}
	st, err := named.Stat()
	_ = named.Close()
	if err != nil || !st.IsDir() || !os.SameFile(w.identity, st) {
		return true
	}
	// Keep memory and the amount of unrelated directory activity bounded.
	// Any overflow, parsing uncertainty, or directory change fails closed.
	var buf [4096]byte
	for range 8 {
		n, err := unix.Read(w.inotify, buf[:])
		if err == unix.EAGAIN {
			return false
		}
		if err != nil || n <= 0 {
			return true
		}
		for p := 0; p < n; {
			if n-p < unix.SizeofInotifyEvent {
				return true
			}
			mask := binary.NativeEndian.Uint32(buf[p+4 : p+8])
			l := int(binary.NativeEndian.Uint32(buf[p+12 : p+16]))
			p += unix.SizeofInotifyEvent
			if l <= 0 || l > n-p || mask&(unix.IN_Q_OVERFLOW|unix.IN_IGNORED) != 0 {
				return true
			}
			z := p
			for z < p+l && buf[z] != 0 {
				z++
			}
			switch string(buf[p:z]) {
			case w.base, w.base + ".lock", w.base + "-wal", w.base + "-shm", w.base + "-journal":
				return true
			}
			p += l
		}
	}
	return true
}
