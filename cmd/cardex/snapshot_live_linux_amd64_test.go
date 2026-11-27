package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
	"golang.org/x/sys/unix"
)

func snapshotTestManifest(args []string) (int, bool) {
	if len(args) == 2 && args[0] == "--test-live-snapshot-manifest" {
		data, err := coldSnapshotManifest(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1, true
		}
		if err := json.NewEncoder(os.Stdout).Encode(data); err != nil {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

type liveManifestEntry struct {
	Stat unix.Stat_t
	Hash [32]byte
}

func coldSnapshotManifest(path string) (map[string]liveManifestEntry, error) {
	out := make(map[string]liveManifestEntry)
	dir, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOATIME|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var folder liveManifestEntry
	if err := unix.Fstat(int(dir.Fd()), &folder.Stat); err != nil {
		return nil, err
	}
	out["."] = folder
	for _, entry := range entries {
		name, got := entry.Name(), liveManifestEntry{}
		full := filepath.Join(path, name)
		if err := unix.Lstat(full, &got.Stat); err != nil {
			return nil, err
		}
		if got.Stat.Mode&unix.S_IFMT == unix.S_IFREG {
			f, err := os.OpenFile(full, os.O_RDONLY|unix.O_NOATIME|unix.O_NOFOLLOW, 0)
			if err != nil {
				return nil, err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			_ = f.Close()
			if err != nil {
				return nil, err
			}
			copy(got.Hash[:], h.Sum(nil))
		}
		// Struct padding has no file semantics and may differ by invocation.
		out[name] = got
	}
	return out, nil
}

func snapshotLiveManifest(t *testing.T, path string) map[string]liveManifestEntry {
	t.Helper()
	// SQLite POSIX locks are per process. Closing any FD on an inode in the
	// writer's process could drop its locks. Hash in a separate observer.
	raw, err := exec.Command(os.Args[0], "--test-live-snapshot-manifest", path).CombinedOutput()
	if err != nil {
		t.Fatalf("snapshot manifest: %v %s", err, raw)
	}
	var got map[string]liveManifestEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func warmSnapshotFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "literal %20 # ?")
	path := filepath.Join(home, "cardex.db")
	initial, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "wal-fixture", Kind: model.KindDev, Status: model.CardReady, Title: "SYNTHETIC_TITLE"}
	if err := initial.CreateCard(context.Background(), &card); err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	uri := url.URL{Path: path}
	db, err := sql.Open("sqlite", "file:"+uri.EscapedPath()+"?_pragma=wal_autocheckpoint(0)&_pragma=cache_size(8)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("UPDATE cards SET status='done' WHERE id='wal-fixture'"); err != nil {
		t.Fatal(err)
	}
	return home, db
}

func TestSnapshotCheckpointedCopyFixture(t *testing.T) {
	// This test is in the Linux suite but exercises the offline path used
	// everywhere, without copying or inspecting a live database file.
	home, db := warmSnapshotFixture(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	for _, name := range []string{"cardex.db", "cardex.db.lock"} {
		raw, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, out, stderr := snapshotMain("--home", dest, "snapshot", "--json", "--checkpointed-copy", "--card", "wal-fixture")
	if code != 0 || stderr != "" || !strings.Contains(out, `"status": "done"`) {
		t.Fatalf("checkpointed backup: %d %s %s", code, out, stderr)
	}
}

func TestSnapshotReadsCommittedWALWithoutTouchingFiles(t *testing.T) {
	home, db := warmSnapshotFixture(t)
	// Main-only would show ready. Commit done to WAL, then force uncommitted
	// failed plus body pages to spill physically into the same WAL file.
	wal := filepath.Join(home, "cardex.db-wal")
	info, err := os.Stat(wal)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE cards SET status='failed', body=? WHERE id='wal-fixture'", strings.Repeat("SYNTHETIC_WAL_BODY", 60_000)); err != nil {
		t.Fatal(err)
	}
	if current, err := os.Stat(wal); err != nil || current.Size() <= info.Size() {
		t.Fatalf("uncommitted fixture didn't spill WAL frames: %v %v", current, err)
	}
	for _, name := range []string{".", "cardex.db", "cardex.db.lock", "cardex.db-wal", "cardex.db-shm"} {
		p := filepath.Join(home, name)
		before, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, time.Unix(1_600_000_000, 0), before.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotLiveManifest(t, home)
	for range 3 {
		code, out, stderr := snapshotMain("--home", home, "snapshot", "--json", "--card", "wal-fixture")
		if code != 0 || stderr != "" || strings.Contains(out, "SYNTHETIC") || !strings.Contains(out, `"status": "done"`) {
			t.Fatalf("did not return exact committed WAL state: %d %s %s", code, out, stderr)
		}
	}
	if after := snapshotLiveManifest(t, home); !reflect.DeepEqual(before, after) {
		t.Fatalf("live read changed source bytes or Linux metadata\nbefore: %+v\n after: %+v", before, after)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE cards SET status='paused' WHERE id='wal-fixture'"); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := snapshotMain("--home", home, "snapshot", "--json", "--card", "wal-fixture")
	if code != 0 || stderr != "" || !strings.Contains(out, `"status": "paused"`) {
		t.Fatalf("next snapshot missed the new commit: %d %s %s", code, out, stderr)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(filepath.Join(home, "cardex.db"+suffix)); !os.IsNotExist(err) {
			t.Fatalf("writer could not perform normal cleanup: %s: %v", suffix, err)
		}
	}
}

func TestSnapshotRejectsCopiedOrIncompleteLiveSidecars(t *testing.T) {
	home, db := warmSnapshotFixture(t)
	dest := t.TempDir()
	// Read in another process to avoid changing the real fixture's locks.
	for _, name := range []string{"cardex.db", "cardex.db.lock", "cardex.db-wal", "cardex.db-shm"} {
		// Synthetic private fixture only. Copying a live DB is intentionally
		// not a success path, even when all bytes are present.
		raw, err := exec.Command("/bin/cat", filepath.Join(home, name)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		before := snapshotLiveManifest(t, dest)
		code, out, stderr := snapshotMain("--home", dest, "snapshot", "--json", "--card", "wal-fixture")
		if code != 1 || out != "" || !strings.Contains(stderr, "unavailable") {
			t.Fatalf("presented copied or missing WAL as reliable: %d %s %s", code, out, stderr)
		}
		if after := snapshotLiveManifest(t, dest); !reflect.DeepEqual(before, after) {
			t.Fatal("refusal changed source bytes, metadata, or file list")
		}
		if err := os.Remove(filepath.Join(dest, "cardex.db-shm")); err != nil {
			if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotStrictCleanDoesNotUpdateColdAccessTimes(t *testing.T) {
	home, db := warmSnapshotFixture(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".", "cardex.db", "cardex.db.lock"} {
		p := filepath.Join(home, name)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, time.Unix(1_600_000_000, 0), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotLiveManifest(t, home)
	if before["cardex.db"].Stat.Atim.Sec != 1_600_000_000 {
		t.Fatal("fixture DB was not cold")
	}
	code, out, stderr := snapshotMain("--home", home, "snapshot", "--json", "--card", "wal-fixture")
	if code != 0 || stderr != "" || !strings.Contains(out, `"status": "done"`) {
		t.Fatalf("strict clean snapshot: %d %q %q", code, out, stderr)
	}
	if after := snapshotLiveManifest(t, home); !reflect.DeepEqual(before, after) {
		t.Fatalf("strict checkpoint reader mutated directory, main DB, or lock: before %+v after %+v", before, after)
	}
}

func TestSnapshotWorkerRejectsFileBackedOutputAndExtraFD(t *testing.T) {
	home, _ := warmSnapshotFixture(t)
	target := filepath.Join(t.TempDir(), "not-stdout")
	if err := os.WriteFile(target, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceBefore := snapshotLiveManifest(t, home)
	for _, name := range []string{"stdout", "stderr", "extra", "named-fifo"} {
		t.Run(name, func(t *testing.T) {
			destination := target
			if name == "named-fifo" {
				destination = filepath.Join(t.TempDir(), "not-anonymous")
				if err := unix.Mkfifo(destination, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			flags := os.O_WRONLY | unix.O_NONBLOCK
			if name == "named-fifo" {
				flags = os.O_RDWR | unix.O_NONBLOCK
			}
			fd, err := os.OpenFile(destination, flags, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer fd.Close()
			var out, stderr bytes.Buffer
			cmd := exec.Command("/proc/self/exe", liveSnapshotWorker, filepath.Join(home, "cardex.db"), "wal-fixture")
			cmd.Env = []string{"GOMAXPROCS=1"}
			cmd.Stdout, cmd.Stderr = &out, &stderr
			var fifoWrite *os.File
			switch name {
			case "stdout":
				cmd.Stdout = fd
			case "stderr":
				cmd.Stderr = fd
			case "extra":
				cmd.ExtraFiles = []*os.File{fd}
			case "named-fifo":
				fifoWrite, err = os.OpenFile(destination, os.O_WRONLY|unix.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer fifoWrite.Close()
				cmd.Stdout = fifoWrite
			}
			err = cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || out.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("unsafe fd not refused silently: %v %q %q", err, out.String(), stderr.String())
			}
			if raw, err := os.ReadFile(target); err != nil || string(raw) != "do not overwrite" {
				t.Fatalf("worker wrote marker: %v %q", err, raw)
			}
			if after := snapshotLiveManifest(t, home); !reflect.DeepEqual(sourceBefore, after) {
				t.Fatal("unsafe descriptor probe changed source manifest")
			}
		})
	}
}

func TestSnapshotWorkerOutputIsBoundedDuringWrite(t *testing.T) {
	var out snapshotWorkerOutput
	if n, err := out.Write(make([]byte, 4095)); n != 4095 || err != nil {
		t.Fatalf("small write %d %v", n, err)
	}
	if n, err := out.Write(make([]byte, 2)); n != 0 || !errors.Is(err, store.ErrSnapshotUnavailable) || out.buf.Len() != 4095 {
		t.Fatalf("over-limit write: n=%d err=%v cap=%d", n, err, out.buf.Len())
	}
}

func TestSnapshotStrictMissingDatabaseDoesNotEnumerateLegacyTasks(t *testing.T) {
	home := t.TempDir()
	tasks := filepath.Join(home, "tasks")
	if err := os.Mkdir(tasks, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasks, "legacy.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tasks, time.Unix(1_600_000_000, 0), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	var before, after unix.Stat_t
	if err := unix.Lstat(tasks, &before); err != nil || before.Atim.Sec != 1_600_000_000 {
		t.Fatalf("legacy fixture wasn't cold: %+v, %v", before, err)
	}
	code, out, stderr := snapshotMain("--home", home, "snapshot", "--json")
	if code != 1 || out != "" || !strings.Contains(stderr, "database does not exist") {
		t.Fatalf("unexpected strict missing-DB result: %d %q %q", code, out, stderr)
	}
	if err := unix.Lstat(tasks, &after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("missing original DB touched legacy tasks: before %+v, after %+v, error %v", before, after, err)
	}
}

func TestSnapshotStrictRefusesHomeAliasWithoutTouchingLink(t *testing.T) {
	for _, mode := range []string{"leaf", "intermediate", "database"} {
		t.Run(mode, func(t *testing.T) {
			home, db := warmSnapshotFixture(t)
			// Keep a real writer for the aliased home tests so both the
			// parent strict code and the live fallback must reject aliases.
			link := filepath.Join(t.TempDir(), "alias")
			source := home
			switch mode {
			case "leaf":
				if err := os.Symlink(home, link); err != nil {
					t.Fatal(err)
				}
				source = link
			case "intermediate":
				if err := os.Symlink(filepath.Dir(home), link); err != nil {
					t.Fatal(err)
				}
				source = filepath.Join(link, filepath.Base(home))
			case "database":
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(home, "cardex.db")
				away := filepath.Join(t.TempDir(), "real.db")
				if err := os.Rename(link, away); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(away, link); err != nil {
					t.Fatal(err)
				}
			}
			var before, after unix.Stat_t
			if err := unix.Lstat(link, &before); err != nil {
				t.Fatal(err)
			}
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, link,
				[]unix.Timespec{{Sec: 1_600_000_000}, before.Mtim}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				t.Fatal(err)
			}
			if err := unix.Lstat(link, &before); err != nil || before.Atim.Sec != 1_600_000_000 {
				t.Fatalf("symlink fixture wasn't cold: %+v %v", before, err)
			}
			code, out, stderr := snapshotMain("--home", source, "snapshot", "--json", "--card", "wal-fixture")
			if code != 1 || out != "" || !strings.Contains(stderr, "unavailable") {
				t.Fatalf("strict accepted alias: %d %q %q", code, out, stderr)
			}
			if err := unix.Lstat(link, &after); err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("strict dereferenced alias: before %+v, after %+v, error %v", before, after, err)
			}
		})
	}
}

func TestSnapshotStrictRejectsHardlinkedSourceFiles(t *testing.T) {
	for _, name := range []string{"cardex.db", "cardex.db.lock", "cardex.db-wal", "cardex.db-shm"} {
		t.Run(name, func(t *testing.T) {
			home, db := warmSnapshotFixture(t)
			if name == "cardex.db" || name == "cardex.db.lock" {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(t.TempDir(), "alias")
			if err := os.Link(filepath.Join(home, name), link); err != nil {
				t.Fatal(err)
			}
			before := snapshotLiveManifest(t, home)
			code, out, stderr := snapshotMain("--home", home, "snapshot", "--json", "--card", "wal-fixture")
			if code != 1 || out != "" || !strings.Contains(stderr, "unavailable") {
				t.Fatalf("strict accepted hardlink: %d %q %q", code, out, stderr)
			}
			if after := snapshotLiveManifest(t, home); !reflect.DeepEqual(before, after) {
				t.Fatal("strict changed hardlink-backed source")
			}
		})
	}
}
