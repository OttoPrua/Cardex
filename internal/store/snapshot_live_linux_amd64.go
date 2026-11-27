package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// ReadLiveSnapshotInWorker irreversibly restricts all threads in the current
// process to read-only filesystem access before using the native SQLite WAL
// reader. Only call it in a fresh, one-shot child, before opening any source
// files. There must be no inherited writable descriptors to the source.
func ReadLiveSnapshotInWorker(ctx context.Context, path, cardID string) (Snapshot, error) {
	if err := restrictLiveSnapshotWorker(); err != nil {
		return Snapshot{}, err
	}
	if err := verifySnapshotWorkerDescriptors(); err != nil {
		return Snapshot{}, err
	}
	return readSnapshotWithView(ctx, cardID, func(read func(context.Context, *sql.Tx) error) error {
		return withLiveSnapshotView(ctx, path, read)
	})
}

func withLiveSnapshotView(ctx context.Context, path string, read func(context.Context, *sql.Tx) error) error {
	// Anchor all source access without following aliases or scanning old
	// data directories. SQLite must receive only that pinned name too.
	watch, err := openLinuxSnapshotWatch(path, false)
	if err != nil {
		return err
	}
	defer watch.close()
	path = watch.pinnedPath()
	lock, lockInfo, err := openRegularSnapshotFile(path+".lock", true)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer lock.Close()
	unlock, err := lockSnapshotRead(lock)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer unlock()
	before, err := liveSnapshotIdentity(path)
	if err != nil {
		return err
	}
	for _, entry := range before {
		if os.SameFile(entry, lockInfo) {
			return ErrSnapshotUnavailable
		}
	}
	if err := installSnapshotNativeOpen(int(watch.anchor.Fd()), path, before); err != nil {
		return err
	}

	// SQLite uses its own POSIX locks to pin a committed read snapshot. It
	// requests READWRITE|CREATE for WAL even when the database is read-only;
	// the kernel denies that open and SQLite reopens the existing WAL read-only.
	// readonly_shm uses only a read-only mapping of an initialized existing SHM.
	// Never set immutable/nolock here and never fall back to checkpoint pages.
	db, err := sql.Open("sqlite", sqliteURI(path)+"?vfs="+snapshotPinnedVFS+"&mode=ro&readonly_shm=1"+
		"&_pragma=query_only(1)&_pragma=temp_store(2)&_pragma=trusted_schema(0)")
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer tx.Rollback()
	var mode string
	if err := tx.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&mode); err != nil || mode != "wal" {
		return ErrSnapshotUnavailable
	}
	readErr := read(ctx, tx)

	// Validate before releasing the last SQLite read lock. Native writers
	// may commit after this observation; the rows still represent the single
	// committed version pinned by this transaction.
	after, err := liveSnapshotIdentity(path)
	if err != nil || !matchesSnapshotFile(path+".lock", lockInfo) || !before.same(after) ||
		!snapshotOpenConfig.sharedLockWitnessed || snapshotOpenConfig.sourceProbeFailed || watch.changed() || tx.Rollback() != nil || ctx.Err() != nil {
		return ErrSnapshotUnavailable
	}
	return readErr
}

// SQLite's read-only opens can still update access times. Its documented Unix
// VFS xSetSystemCall hook lets this one-use process pin exact source inodes
// via the already opened directory, and supply O_NOATIME rather than mutating
// a cold file's metadata. The independent seccomp filter remains the source of
// truth for enforcing no writes; the hook cannot bypass it.
const (
	snapshotPinnedVFS       = "cardex-snapshot-pinned"
	snapshotNativeStatBytes = int(unsafe.Sizeof(sqlite3.Tstat{}))
)

type snapshotNativeOpen struct {
	anchor              int
	path                string
	before              liveSnapshotFiles
	shmFD               int32
	sharedLockWitnessed bool
	sourceProbeFailed   bool
}

var snapshotOpenConfig *snapshotNativeOpen

func installSnapshotNativeOpen(anchor int, path string, before liveSnapshotFiles) error {
	if snapshotOpenConfig != nil {
		return ErrSnapshotUnavailable
	}
	tls := libc.NewTLS()
	defer tls.Close()
	unixName, err := libc.CString("unix")
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer libc.Xfree(tls, unixName)
	p := sqlite3.Xsqlite3_vfs_find(tls, unixName)
	if p == 0 {
		return ErrSnapshotUnavailable
	}
	// In the translated modernc ABI these C pointer arguments are uintptrs.
	// Reinterpret the value rather than performing pointer arithmetic.
	vfs := *(**sqlite3.Tsqlite3_vfs)(unsafe.Pointer(&p))
	if vfs.FiVersion < 3 || vfs.FxSetSystemCall == 0 {
		return ErrSnapshotUnavailable
	}
	type openFn func(*libc.TLS, uintptr, int32, int32) int32
	type fcntlFn func(*libc.TLS, int32, int32, uintptr) int32
	type statFn func(*libc.TLS, uintptr, uintptr) int32
	type accessFn func(*libc.TLS, uintptr, int32) int32
	type setSystemCallFn func(*libc.TLS, uintptr, uintptr, uintptr) int32
	type fullPathFn func(*libc.TLS, uintptr, uintptr, int32, uintptr) int32
	set := *(*setSystemCallFn)(unsafe.Pointer(&vfs.FxSetSystemCall))
	// Same translated-C function value representation as
	// modernc.org/sqlite.cFuncPointer. Neither callback is a closure.
	snapshotOpenConfig = &snapshotNativeOpen{anchor: anchor, path: path, before: before, shmFD: -1}
	for _, hook := range []struct {
		name string
		fn   uintptr
	}{
		{"open", *(*uintptr)(unsafe.Pointer(&struct{ fn openFn }{liveSnapshotOpen}))},
		{"fcntl", *(*uintptr)(unsafe.Pointer(&struct{ fn fcntlFn }{liveSnapshotFcntl}))},
		// Native xAccess, fileHasMoved and getFileMode share these syscall
		// hooks. Even a temporary terminal link must never be followed.
		{"stat", *(*uintptr)(unsafe.Pointer(&struct{ fn statFn }{liveSnapshotStat}))},
		{"access", *(*uintptr)(unsafe.Pointer(&struct{ fn accessFn }{liveSnapshotAccess}))},
	} {
		name, err := libc.CString(hook.name)
		if err != nil {
			return ErrSnapshotUnavailable
		}
		rc := set(tls, p, name, hook.fn)
		libc.Xfree(tls, name)
		if rc != sqlite3.SQLITE_OK {
			return ErrSnapshotUnavailable
		}
	}

	// Keep the generated procfd pathname intact. Default Unix
	// xFullPathname resolves every symlink, including this trusted procfs
	// anchor, which would let SQLite walk the mutable original name again.
	// Register a child-only copy; do not edit the registered Unix VFS.
	name, err := libc.CString(snapshotPinnedVFS)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	copyPtr := sqlite3.Xsqlite3_malloc64(tls, uint64(unsafe.Sizeof(*vfs)))
	if copyPtr == 0 {
		libc.Xfree(tls, name)
		return ErrSnapshotUnavailable
	}
	pinned := *(**sqlite3.Tsqlite3_vfs)(unsafe.Pointer(&copyPtr))
	*pinned = *vfs
	pinned.FzName, pinned.FpNext = name, 0
	pinned.FxFullPathname = *(*uintptr)(unsafe.Pointer(&struct{ fn fullPathFn }{liveSnapshotFullPath}))
	if sqlite3.Xsqlite3_vfs_register(tls, copyPtr, 0) != sqlite3.SQLITE_OK {
		sqlite3.Xsqlite3_free(tls, copyPtr)
		libc.Xfree(tls, name)
		return ErrSnapshotUnavailable
	}
	// The single-use subprocess owns the registration until exit.
	return nil
}

func liveSnapshotFullPath(tls *libc.TLS, _ uintptr, zPath uintptr, nOut int32, zOut uintptr) int32 {
	if snapshotOpenConfig == nil || zPath == 0 || zOut == 0 {
		return sqlite3.SQLITE_CANTOPEN
	}
	path := libc.GoString(zPath)
	if path != snapshotOpenConfig.path || len(path) >= int(nOut) {
		return sqlite3.SQLITE_CANTOPEN
	}
	libc.Xmemcpy(tls, zOut, zPath, uint64(len(path)+1))
	return sqlite3.SQLITE_OK
}

func liveSnapshotOpen(tls *libc.TLS, cPath uintptr, flags int32, _ int32) int32 {
	if flags&int32(snapshotForbiddenOpen) != 0 || cPath == 0 || snapshotOpenConfig == nil {
		return snapshotNativeError(tls, syscall.EACCES)
	}
	p := libc.GoString(cPath)
	// The native VFS may open its RNG. It is not part of the source.
	if p == "/dev/urandom" {
		return libc.Xopen(tls, cPath, flags, 0)
	}
	cfg := snapshotOpenConfig
	for i, suffix := range []string{"", "-wal", "-shm"} {
		if p != cfg.path+suffix {
			continue
		}
		name := filepath.Base(cfg.path) + suffix
		fd, err := unix.Openat(cfg.anchor, name, unix.O_RDONLY|unix.O_NOATIME|
			unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return snapshotNativeError(tls, err)
		}
		var info unix.Stat_t
		if err := unix.Fstat(fd, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG ||
			!snapshotStatMatches(info, cfg.before[i]) || info.Nlink != 1 || !localSnapshotFD(uintptr(fd)) {
			_ = unix.Close(fd)
			return snapshotNativeError(tls, syscall.EACCES)
		}
		if suffix == "-shm" {
			cfg.shmFD = int32(fd)
		}
		return int32(fd)
	}
	return snapshotNativeError(tls, syscall.EACCES)
}

// liveSnapshotStat handles only exact anchored names, and writes the result
// with the libc ABI expected by translated SQLite. The rollback journal is
// required to be absent. A failed probe must not silently become "no WAL" and
// allow a main-file-only result.
func liveSnapshotStat(tls *libc.TLS, cPath, buf uintptr) int32 {
	cfg := snapshotOpenConfig
	if cfg == nil || cPath == 0 || buf == 0 {
		return snapshotNativeError(tls, syscall.EACCES)
	}
	path := libc.GoString(cPath)
	index := -1
	for i, suffix := range []string{"", "-wal", "-shm"} {
		if path == cfg.path+suffix {
			index = i
			break
		}
	}
	journal := path == cfg.path+"-journal"
	if index < 0 && !journal {
		return snapshotNativeError(tls, syscall.EACCES)
	}
	name, err := libc.CString(filepath.Base(path))
	if err != nil {
		cfg.sourceProbeFailed = true
		return snapshotNativeError(tls, err)
	}
	defer libc.Xfree(tls, name)
	if libc.Xfstatat(tls, int32(cfg.anchor), name, buf, unix.AT_SYMLINK_NOFOLLOW) != 0 {
		location := libc.X__errno_location(tls)
		if !journal || **(**int32)(unsafe.Pointer(&location)) != int32(syscall.ENOENT) {
			cfg.sourceProbeFailed = true
		}
		return -1
	}
	info := *(**sqlite3.Tstat)(unsafe.Pointer(&buf))
	if info.Fst_mode&unix.S_IFMT == unix.S_IFLNK {
		cfg.sourceProbeFailed = true
		return snapshotNativeError(tls, syscall.ELOOP)
	}
	if journal {
		cfg.sourceProbeFailed = true
		return snapshotNativeError(tls, syscall.EACCES)
	}
	before, ok := cfg.before[index].Sys().(*syscall.Stat_t)
	if !ok || info.Fst_mode&unix.S_IFMT != unix.S_IFREG || info.Fst_nlink != 1 ||
		info.Fst_dev != before.Dev || info.Fst_ino != before.Ino {
		cfg.sourceProbeFailed = true
		return snapshotNativeError(tls, syscall.EACCES)
	}
	return 0
}

func liveSnapshotAccess(tls *libc.TLS, cPath uintptr, mode int32) int32 {
	// SQLite's read-write access probes always return denied. Its fallback
	// existence test uses the same anchored, no-follow metadata check.
	if mode != unix.F_OK {
		return snapshotNativeError(tls, syscall.EACCES)
	}
	// The translated C ABI carries addresses as uintptr; TLS scratch
	// stays pinned even if Go grows/moves the goroutine stack.
	out := tls.Alloc(snapshotNativeStatBytes)
	defer tls.Free(snapshotNativeStatBytes)
	return liveSnapshotStat(tls, cPath, out)
}

func snapshotNativeError(tls *libc.TLS, err error) int32 {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		errno = syscall.EACCES
	}
	p := libc.X__errno_location(tls)
	**(**int32)(unsafe.Pointer(&p)) = int32(errno)
	return -1
}

func liveSnapshotFcntl(tls *libc.TLS, fd, cmd int32, args uintptr) int32 {
	cfg := snapshotOpenConfig
	if cfg == nil || fd != cfg.shmFD || cmd != unix.F_GETLK || args == 0 {
		return libc.Xfcntl(tls, fd, cmd, args)
	}
	va := args
	p := libc.VaUintptr(&va)
	if p == 0 {
		return snapshotNativeError(tls, syscall.EIO)
	}
	lock := *(**unix.Flock_t)(unsafe.Pointer(&p))
	// SQLite's Unix WAL-index deadman switch is immediately after the
	// eight ordinary WAL lock bytes at 120..127 (os_unix.c). If no other
	// process has DMS held, SQLite can otherwise fall back to scanning WAL
	// frames to construct a heap-only index instead of reading the live
	// published commit. This reader refuses that fallback.
	const dmsByte = 128
	checkDMS := lock.Type == unix.F_WRLCK && lock.Whence == 0 && lock.Start == dmsByte && lock.Len == 1
	rc := libc.Xfcntl(tls, fd, cmd, args)
	if rc == 0 && checkDMS {
		switch lock.Type {
		case unix.F_RDLCK:
			cfg.sharedLockWitnessed = true
		case unix.F_WRLCK:
			// SQLite handles this as busy and retries without using SHM.
		default:
			return snapshotNativeError(tls, syscall.EIO)
		}
	}
	return rc
}

func snapshotStatMatches(stat unix.Stat_t, file os.FileInfo) bool {
	before, ok := file.Sys().(*syscall.Stat_t)
	return ok && before.Dev == stat.Dev && before.Ino == stat.Ino
}

type liveSnapshotFiles [3]os.FileInfo

func liveSnapshotIdentity(path string) (out liveSnapshotFiles, err error) {
	if _, err := os.Lstat(path + "-journal"); !os.IsNotExist(err) {
		return out, ErrSnapshotUnavailable
	}
	for i, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if err != nil || !info.Mode().IsRegular() || !singleSnapshotFileLink(info) {
			return out, ErrSnapshotUnavailable
		}
		out[i] = info
	}
	// Prevent a bogus alias from being accepted as a companion file. An
	// empty or uninitialized WAL index is not a readable live snapshot.
	if out[2].Size() < 32768 || os.SameFile(out[0], out[1]) ||
		os.SameFile(out[0], out[2]) || os.SameFile(out[1], out[2]) {
		return out, ErrSnapshotUnavailable
	}
	return out, nil
}

func (a liveSnapshotFiles) same(b liveSnapshotFiles) bool {
	return os.SameFile(a[0], b[0]) && os.SameFile(a[1], b[1]) && os.SameFile(a[2], b[2])
}

// SQLite's Unix VFS may chmod an empty WAL/SHM, chown sidecars as root,
// truncate/recover a WAL, or delete sidecars at close. The filter uses a
// syscall allowlist (not a set of guesses about what this version writes).
// All opens are existing-file and read-only; the only writable descriptors
// are stdout/stderr, redirected by the parent to its own output pipe.
const snapshotForbiddenOpen = unix.O_ACCMODE | unix.O_CREAT | unix.O_TRUNC | unix.O_APPEND | (unix.O_TMPFILE &^ unix.O_DIRECTORY)

func restrictLiveSnapshotWorker() error {
	const (
		loadWord  = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		equal     = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		hasBits   = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
		returnVal = unix.BPF_RET | unix.BPF_K
		allow     = unix.SECCOMP_RET_ALLOW
		// SQLite's native VFS may retry an inaccessible WAL as read-only.
		deny = unix.SECCOMP_RET_ERRNO | uint32(unix.EACCES)

		// struct seccomp_data: number, audit architecture, instruction
		// pointer, then 64-bit arguments. We read the low 32 bits on amd64.
		number = 0
		arch   = 4
		arg0   = 16
		arg1   = 24
		arg2   = 32
		arg3   = 40
	)
	program := []unix.SockFilter{
		{Code: loadWord, K: arch},
		{Code: equal, Jt: 1, K: unix.AUDIT_ARCH_X86_64},
		{Code: returnVal, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: loadWord, K: number},
	}
	for _, open := range []struct{ syscall, flags uint32 }{{unix.SYS_OPEN, arg1}, {unix.SYS_OPENAT, arg2}} {
		program = append(program,
			unix.SockFilter{Code: equal, Jf: 4, K: open.syscall},
			unix.SockFilter{Code: loadWord, K: open.flags},
			unix.SockFilter{Code: hasBits, Jt: 1, K: snapshotForbiddenOpen},
			unix.SockFilter{Code: returnVal, K: allow},
			unix.SockFilter{Code: returnVal, K: deny})
	}
	for _, write := range []uint32{unix.SYS_WRITE, unix.SYS_WRITEV} {
		program = append(program,
			unix.SockFilter{Code: equal, Jf: 5, K: write},
			unix.SockFilter{Code: loadWord, K: arg0},
			unix.SockFilter{Code: equal, Jt: 1, K: 1},
			unix.SockFilter{Code: equal, Jf: 1, K: 2},
			unix.SockFilter{Code: returnVal, K: allow},
			unix.SockFilter{Code: returnVal, K: deny})
	}
	// The runtime may create threads but the worker never launches a process.
	// glibc's pthread_create retries clone without a pointer argument only
	// when clone3 is unavailable, so return ENOSYS for that syscall.
	program = append(program,
		unix.SockFilter{Code: equal, Jf: 1, K: unix.SYS_CLONE3},
		unix.SockFilter{Code: returnVal, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		unix.SockFilter{Code: equal, Jf: 4, K: unix.SYS_CLONE},
		unix.SockFilter{Code: loadWord, K: arg0},
		unix.SockFilter{Code: hasBits, Jf: 1, K: unix.CLONE_THREAD},
		unix.SockFilter{Code: returnVal, K: allow},
		unix.SockFilter{Code: returnVal, K: deny})
	// The only shared file mappings needed are SQLite's read-only SHM
	// pages. Private and anonymous mappings are needed by the Go runtime.
	program = append(program,
		unix.SockFilter{Code: equal, Jf: 6, K: unix.SYS_MMAP},
		unix.SockFilter{Code: loadWord, K: arg3},
		unix.SockFilter{Code: hasBits, Jf: 2, K: unix.MAP_SHARED},
		unix.SockFilter{Code: loadWord, K: arg2},
		unix.SockFilter{Code: hasBits, Jt: 1, K: unix.PROT_WRITE},
		unix.SockFilter{Code: returnVal, K: allow},
		unix.SockFilter{Code: returnVal, K: deny})
	// Preflight uses GETFD/GETFL; SQLite and the runtime also need normal
	// descriptor flags and POSIX lock operations. No duplication, leases,
	// ownership changes, or sealing.
	fcntlCommands := []uint32{unix.F_GETFD, unix.F_GETFL, unix.F_SETFD, unix.F_SETFL, unix.F_GETLK, unix.F_SETLK, unix.F_SETLKW}
	program = append(program,
		unix.SockFilter{Code: equal, Jf: uint8(len(fcntlCommands) + 3), K: unix.SYS_FCNTL},
		unix.SockFilter{Code: loadWord, K: arg1})
	for i, command := range fcntlCommands {
		program = append(program, unix.SockFilter{Code: equal, Jt: uint8(len(fcntlCommands) - i), K: command})
	}
	program = append(program,
		unix.SockFilter{Code: returnVal, K: deny},
		unix.SockFilter{Code: returnVal, K: allow})

	for _, n := range []uint32{
		unix.SYS_READ, unix.SYS_READV, unix.SYS_PREAD64, unix.SYS_GETDENTS64, unix.SYS_CLOSE, unix.SYS_LSEEK,
		unix.SYS_FSTAT, unix.SYS_FSTATFS, unix.SYS_NEWFSTATAT, unix.SYS_STAT, unix.SYS_LSTAT, unix.SYS_STATX,
		unix.SYS_ACCESS, unix.SYS_FACCESSAT, unix.SYS_FACCESSAT2, unix.SYS_READLINK, unix.SYS_READLINKAT, unix.SYS_GETCWD,
		unix.SYS_FLOCK, unix.SYS_INOTIFY_INIT1, unix.SYS_INOTIFY_ADD_WATCH,
		unix.SYS_FUTEX, unix.SYS_EPOLL_PWAIT, unix.SYS_EPOLL_CTL, unix.SYS_EPOLL_CREATE1, unix.SYS_EVENTFD2, unix.SYS_PIPE2,
		unix.SYS_RT_SIGACTION, unix.SYS_RT_SIGPROCMASK, unix.SYS_RT_SIGRETURN, unix.SYS_SIGALTSTACK, unix.SYS_TGKILL,
		unix.SYS_GETTID, unix.SYS_GETPID, unix.SYS_GETUID, unix.SYS_GETEUID, unix.SYS_GETGID, unix.SYS_GETEGID,
		unix.SYS_SCHED_YIELD, unix.SYS_SCHED_GETAFFINITY, unix.SYS_CLOCK_GETTIME, unix.SYS_CLOCK_NANOSLEEP, unix.SYS_NANOSLEEP,
		unix.SYS_GETRANDOM, unix.SYS_MADVISE, unix.SYS_MPROTECT, unix.SYS_MUNMAP, unix.SYS_MREMAP, unix.SYS_BRK,
		unix.SYS_RESTART_SYSCALL, unix.SYS_EXIT, unix.SYS_EXIT_GROUP, unix.SYS_SET_ROBUST_LIST, unix.SYS_RSEQ, unix.SYS_PRLIMIT64,
	} {
		program = append(program,
			unix.SockFilter{Code: equal, Jf: 1, K: n},
			unix.SockFilter{Code: returnVal, K: allow})
	}
	program = append(program, unix.SockFilter{Code: returnVal, K: deny})
	filter := unix.SockFprog{Len: uint16(len(program)), Filter: &program[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return ErrSnapshotUnavailable
	}
	// TSYNC must succeed for the entire process, not only this goroutine's
	// current thread. A positive return is the TID which prevented sync.
	result, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER,
		unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&filter)))
	runtime.KeepAlive(&filter)
	if errno != 0 || result != 0 {
		return ErrSnapshotUnavailable
	}
	return nil
}
