package store

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"modernc.org/libc"
)

func TestSnapshotWorkerSubprocess(t *testing.T) {
	if os.Getenv("CARDEX_TEST_ISOLATED_SNAPSHOT") == "1" && len(os.Args) == 5 && os.Args[2] == "--" {
		os.Exit(isolatedSnapshotProbe(os.Args[3], os.Args[4]))
	}
}

func isolatedSnapshotProbe(mode, path string) int {
	var inherited int
	if mode == "deny" {
		// This deliberately preopens a writable synthetic FD to exercise
		// syscall denies independently of the native SQLite open hook.
		var err error
		inherited, err = unix.Open(path, unix.O_RDWR, 0)
		if err != nil {
			return 1
		}
		defer unix.Close(inherited)
	}
	if err := restrictLiveSnapshotWorker(); err != nil {
		return 70
	}
	if mode == "deny" {
		for _, flag := range []int{
			unix.O_RDWR, unix.O_WRONLY, unix.O_RDONLY | unix.O_CREAT,
			unix.O_RDONLY | unix.O_TRUNC, unix.O_RDONLY | unix.O_APPEND, unix.O_TMPFILE | unix.O_RDWR,
		} {
			if _, err := unix.Open(path, flag, 0o600); err != unix.EACCES {
				fmt.Fprintf(os.Stderr, "open flag %#x not denied: %v\n", flag, err)
				return 2
			}
		}
		_, write := unix.Pwrite(inherited, []byte("changed"), 0)
		_, sharedMap := unix.Mmap(inherited, 0, 4096, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		for name, err := range map[string]error{
			"pwrite": write, "ftruncate": unix.Ftruncate(inherited, 0), "mmap": sharedMap,
			"fchmod": unix.Fchmod(inherited, 0o777), "fchown": unix.Fchown(inherited, unix.Getuid(), unix.Getgid()),
			"rename": unix.Rename(path, path+".moved"), "unlink": unix.Unlink(path),
			"mkdir": unix.Mkdir(path+".dir", 0o700), "link": unix.Link(path, path+".link"),
		} {
			if err != unix.EACCES {
				fmt.Fprintf(os.Stderr, "syscall %s not denied: %v\n", name, err)
				return 2
			}
		}
		if _, err := unix.FcntlInt(uintptr(inherited), unix.F_DUPFD, 0); err != unix.EACCES {
			fmt.Fprintln(os.Stderr, "unexpected descriptor duplication", err)
			return 2
		}
		if _, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0); err != unix.EACCES {
			fmt.Fprintln(os.Stderr, "network socket not denied", err)
			return 2
		}
		return 0
	}

	err := withLiveSnapshotView(context.Background(), path, func(ctx context.Context, tx *sql.Tx) error {
		for i := range 2 {
			var row string
			if err := tx.QueryRowContext(ctx, "SELECT status FROM snapshot_race_probe WHERE id=1").Scan(&row); err != nil || row != "committed-in-wal" {
				return fmt.Errorf("read %d: status %s error %v", i, row, err)
			}
			if i == 0 {
				_, _ = fmt.Fprintln(os.Stdout, "transaction-pinned")
				var signal [1]byte
				if _, err := io.ReadFull(os.Stdin, signal[:]); err != nil {
					return err
				}
				if mode == "terminal-link" {
					tls := libc.NewTLS()
					defer tls.Close()
					cPath, err := libc.CString(snapshotOpenConfig.path)
					if err != nil {
						return err
					}
					defer libc.Xfree(tls, cPath)
					st := tls.Alloc(snapshotNativeStatBytes)
					defer tls.Free(snapshotNativeStatBytes)
					if liveSnapshotStat(tls, cPath, st) != -1 ||
						liveSnapshotAccess(tls, cPath, unix.F_OK) != -1 || !snapshotOpenConfig.sourceProbeFailed {
						return fmt.Errorf("native probes did not refuse terminal link")
					}
					_, _ = fmt.Fprintln(os.Stdout, "terminal-probes-refused")
				}
			}
		}
		return nil
	})
	switch {
	case err == nil:
		fmt.Fprintln(os.Stdout, "committed-snapshot")
		return 0
	case errors.Is(err, ErrSnapshotUnavailable):
		return 8
	default:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
}

func TestSnapshotWorkerKernelGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic")
	if err := os.WriteFile(path, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotManifest(t, filepath.Dir(path))
	cmd := exec.Command("/proc/self/exe", "-test.run=^TestSnapshotWorkerSubprocess$", "--", "deny", path)
	cmd.Env = []string{"GOMAXPROCS=1", "CARDEX_TEST_ISOLATED_SNAPSHOT=1"}
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kernel guard: %v %s", err, raw)
	}
	if after := snapshotManifest(t, filepath.Dir(path)); len(after) != 1 || after["synthetic"] != before["synthetic"] {
		t.Fatalf("guard probe changed fixture: before %+v after %+v", before, after)
	}
}

func TestSnapshotLiveTransactionPinsCommitAndRejectsSwap(t *testing.T) {
	for _, mode := range []string{"commit", "swap", "terminal-link"} {
		t.Run(mode, func(t *testing.T) {
			path := closedSnapshotHome(t)
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.db.Exec("CREATE TABLE snapshot_race_probe(id INTEGER PRIMARY KEY, status TEXT); INSERT INTO snapshot_race_probe VALUES (1,'committed-in-wal')"); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/proc/self/exe", "-test.run=^TestSnapshotWorkerSubprocess$", "--", mode, path)
			cmd.Env = []string{"GOMAXPROCS=1", "CARDEX_TEST_ISOLATED_SNAPSHOT=1"}
			cmd.Stderr = os.Stderr
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Wait()
			defer in.Close()
			lines := bufio.NewReader(out)
			if got, err := lines.ReadString('\n'); err != nil || got != "transaction-pinned\n" {
				t.Fatalf("child did not establish transaction: %q %v", got, err)
			}
			var coldLink unix.Stat_t
			var movedMain string
			if mode == "terminal-link" {
				movedMain = filepath.Join(t.TempDir(), "cold-target.db")
				if err := os.Rename(path, movedMain); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if movedMain != "" {
						_ = os.Remove(path)
						_ = os.Rename(movedMain, path)
					}
				}()
				if err := os.Symlink(movedMain, path); err != nil {
					t.Fatal(err)
				}
				if err := unix.Lstat(path, &coldLink); err != nil {
					t.Fatal(err)
				}
				if err := unix.UtimesNanoAt(unix.AT_FDCWD, path,
					[]unix.Timespec{{Sec: 1_600_000_000}, coldLink.Mtim}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
					t.Fatal(err)
				}
				if err := unix.Lstat(path, &coldLink); err != nil || coldLink.Atim.Sec != 1_600_000_000 {
					t.Fatalf("terminal link was not cold: %+v %v", coldLink, err)
				}
			} else if mode == "swap" {
				// Restore the *same* inode before allowing the reader to
				// validate. Pre/post identity alone cannot detect this race.
				scratch := filepath.Join(t.TempDir(), "wal")
				if err := os.Rename(path+"-wal", scratch); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(scratch, path+"-wal"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.db.Exec("UPDATE snapshot_race_probe SET status='next-commit' WHERE id=1"); err != nil {
				t.Fatal(err)
			}
			if _, err := in.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			_ = in.Close()
			got, err := io.ReadAll(lines)
			if err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if mode == "swap" || mode == "terminal-link" {
				var exit *exec.ExitError
				want := ""
				if mode == "terminal-link" {
					want = "terminal-probes-refused\n"
				}
				if string(got) != want || !errors.As(err, &exit) || exit.ExitCode() != 8 {
					t.Fatalf("transient %s accepted or not checked: %q %v", mode, got, err)
				}
				if mode == "terminal-link" {
					var after unix.Stat_t
					if err := unix.Lstat(path, &after); err != nil || after != coldLink {
						t.Fatalf("native probe followed the terminal link: before %+v after %+v error %v", coldLink, after, err)
					}
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(movedMain, path); err != nil {
						t.Fatal(err)
					}
					movedMain = ""
				}
			} else if string(got) != "committed-snapshot\n" || err != nil {
				t.Fatalf("transaction didn't retain original commit: %q %v", got, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path + "-wal"); !os.IsNotExist(err) {
				t.Fatalf("writer cleanup failed: %v", err)
			}
		})
	}
}

func TestSnapshotStrictCleanRejectsTransientChanges(t *testing.T) {
	for _, mode := range []string{"sidecar", "same-inode-rename", "attributes"} {
		t.Run(mode, func(t *testing.T) {
			path := closedSnapshotHome(t)
			err := withSnapshotView(context.Background(), path, func(ctx context.Context, tx *sql.Tx) error {
				var got int
				if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&got); err != nil || got != latestSchema {
					t.Fatalf("fixture read returned %d %v", got, err)
				}
				switch mode {
				case "sidecar":
					if err := os.WriteFile(path+"-wal", []byte("synthetic"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(path + "-wal"); err != nil {
						t.Fatal(err)
					}
				case "same-inode-rename":
					away := filepath.Join(t.TempDir(), "main")
					if err := os.Rename(path, away); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(away, path); err != nil {
						t.Fatal(err)
					}
				case "attributes":
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					mode := info.Mode().Perm()
					if err := os.Chmod(path, mode^0o100); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, mode); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			if !errors.Is(err, ErrSnapshotUnavailable) {
				t.Fatalf("transient %s escaped validation: %v", mode, err)
			}
		})
	}
}

func TestSnapshotWorkerPayloadChecksSchemaAndSelection(t *testing.T) {
	if ValidSnapshot(Snapshot{Schema: latestSchema + 1, Events: []SnapshotEvent{}}, "") ||
		ValidSnapshot(Snapshot{Schema: latestSchema, Events: []SnapshotEvent{}}, "another-card") ||
		ValidSnapshot(Snapshot{Schema: latestSchema, Events: make([]SnapshotEvent, snapshotEventLimit+1)}, "") ||
		!ValidSnapshot(Snapshot{Schema: latestSchema, Events: []SnapshotEvent{}}, "") {
		t.Fatal("worker payload schema, count or card selection not validated")
	}
}

func TestSnapshotCheckpointedCopyDoesNotRequireNoAtimeFlag(t *testing.T) {
	p := closedSnapshotHome(t)
	for _, strict := range []bool{false, true} {
		f, err := openSnapshotFile(p, strict)
		if err != nil {
			t.Fatal(err)
		}
		flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
		_ = f.Close()
		if err != nil || ((flags&unix.O_NOATIME) != 0) != strict {
			t.Fatalf("strict=%t got %#x %v", strict, flags, err)
		}
	}
}
