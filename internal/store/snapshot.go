package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"modernc.org/sqlite/vfs"
)

var (
	ErrSnapshotNotFound    = errors.New("snapshot database does not exist")
	ErrSnapshotUnavailable = errors.New("snapshot needs a stable checkpointed CardeX database")
	ErrSnapshotSchema      = errors.New("snapshot database schema does not match this binary")
)

type SnapshotCard struct {
	ID     string           `json:"id"`
	Kind   model.CardKind   `json:"kind"`
	Status model.CardStatus `json:"status"`
}

type SnapshotAttempt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type SnapshotEvent struct {
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
}

// Snapshot contains only management identities and timestamps, not payloads.
type Snapshot struct {
	Schema  int              `json:"schema"`
	Card    *SnapshotCard    `json:"card"`
	Attempt *SnapshotAttempt `json:"attempt"`
	Route   string           `json:"route"`
	Events  []SnapshotEvent  `json:"events"`
}

// ReadSnapshot returns a small, consistent view of a quiescent CardeX home.
// Live WAL/SHM, rollback journals, and races deliberately make it unavailable.
// A nonblocking shared lock on the existing CardeX initialization lock
// excludes another CardeX Open, without requiring write access or creating it.
// Linux also watches for transient directory and file changes. SQLite
// receives only an immutable view of the pinned read-only descriptor.
func ReadSnapshot(ctx context.Context, path, cardID string) (out Snapshot, err error) {
	return readSnapshotWithView(ctx, cardID, func(read func(context.Context, *sql.Tx) error) error {
		return withSnapshotView(ctx, path, read)
	})
}

// ReadCheckpointedCopy reads a separately prepared, stable backup. Its caller
// must ensure the copy has no concurrent writers or replacements. Unlike
// strict Linux ReadSnapshot, this portable path cannot monitor source changes.
func ReadCheckpointedCopy(ctx context.Context, path, cardID string) (Snapshot, error) {
	return readSnapshotWithView(ctx, cardID, func(read func(context.Context, *sql.Tx) error) error {
		return withSnapshotViewPolicy(ctx, path, read, false)
	})
}

// ValidSnapshot validates the bounded machine reply from the isolated worker.
func ValidSnapshot(s Snapshot, cardID string) bool {
	if s.Schema != latestSchema || s.Events == nil || len(s.Events) > snapshotEventLimit {
		return false
	}
	if cardID == "" {
		return s.Card == nil && s.Attempt == nil && s.Route == "" && len(s.Events) == 0
	}
	if s.Card == nil || s.Card.ID != cardID || !snapshotLabel(cardID) || !s.Card.Kind.Valid() || !s.Card.Status.Valid() {
		return false
	}
	if s.Attempt != nil {
		if !snapshotLabel(s.Attempt.ID) || (s.Attempt.Status != "running" && s.Attempt.Status != "ended" && !model.Outcome(s.Attempt.Status).Valid()) {
			return false
		}
	} else if s.Route != "" {
		return false
	}
	if s.Route != "" && !snapshotLabelLimit(s.Route, 257) {
		return false
	}
	for _, event := range s.Events {
		if !slices.Contains(snapshotEventTypes, event.Type) {
			return false
		}
	}
	return true
}

func readSnapshotWithView(ctx context.Context, cardID string, view func(func(context.Context, *sql.Tx) error) error) (out Snapshot, err error) {
	err = view(func(ctx context.Context, tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&out.Schema)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no such table") {
				return ErrSnapshotSchema
			}
			return ErrSnapshotUnavailable
		}
		if out.Schema != latestSchema {
			return ErrSnapshotSchema
		}
		out.Events = []SnapshotEvent{}
		if cardID == "" {
			return nil
		}
		return readSnapshotCard(ctx, tx, cardID, &out)
	})
	if err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

const snapshotDBName = "cardex-snapshot.db"

func withSnapshotView(ctx context.Context, path string, read func(context.Context, *sql.Tx) error) error {
	return withSnapshotViewPolicy(ctx, path, read, true)
}

type strictSnapshotGuard struct {
	path      string
	close     func()
	changed   func() bool
	watchFile func(*os.File) error
}

func withSnapshotViewPolicy(ctx context.Context, path string, read func(context.Context, *sql.Tx) error, strict bool) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	var guard *strictSnapshotGuard
	if strict {
		// No legacy task enumeration, symlink resolution, or raw source path
		// probe in the strict mode: even these can change an access time.
		guard, err = prepareStrictSnapshotWatch(path)
		if err != nil {
			return err
		}
		defer guard.close()
		path = guard.path
	} else if err := refuseCardexHome(path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); os.IsNotExist(err) {
		return ErrSnapshotNotFound
	} else if err != nil || !info.Mode().IsRegular() {
		return ErrSnapshotUnavailable
	}

	lockPath := path + ".lock"
	lock, lockInfo, err := openRegularSnapshotFile(lockPath, strict)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer lock.Close()
	unlock, err := lockSnapshotRead(lock)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer unlock()

	if !snapshotNoSidecars(path) {
		return ErrSnapshotUnavailable
	}
	file, info, err := openRegularSnapshotFile(path, strict)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer file.Close()
	if strict {
		if os.SameFile(info, lockInfo) || guard.watchFile(lock) != nil || guard.watchFile(file) != nil {
			return ErrSnapshotUnavailable
		}
	}
	header, err := snapshotHeader(file)
	if err != nil || !sameSnapshotFiles(path, file, info, header, lockPath, lockInfo) {
		return ErrSnapshotUnavailable
	}

	// Never hand the OS path to SQLite. Even a filesystem or SQLite race can
	// only cause a failed read; SQLite cannot open or modify source sidecars.
	name, root, err := vfs.New(snapshotFileSystem{file, info})
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer root.Close()
	db, err := sql.Open("sqlite", "file:"+snapshotDBName+"?vfs="+name+
		"&mode=ro&immutable=1&_pragma=query_only(1)&_pragma=temp_store(2)&_pragma=trusted_schema(0)")
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
	readErr := read(ctx, tx)
	if err := tx.Rollback(); err != nil {
		return ErrSnapshotUnavailable
	}
	// Do not return any rows until the entire read has passed identity checks,
	// even if the SQL callback encountered a not-found or schema error.
	if ctx.Err() != nil || !sameSnapshotFiles(path, file, info, header, lockPath, lockInfo) || (strict && guard.changed()) {
		return ErrSnapshotUnavailable
	}
	return readErr
}

func openRegularSnapshotFile(path string, strict bool) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, nil, ErrSnapshotUnavailable
	}
	file, err := openSnapshotFile(path, strict)
	if err != nil {
		return nil, nil, ErrSnapshotUnavailable
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) ||
		(strict && (!singleSnapshotFileLink(info) || !localSnapshotFD(file.Fd()))) {
		file.Close()
		return nil, nil, ErrSnapshotUnavailable
	}
	return file, info, nil
}

func snapshotHeader(file *os.File) (header [100]byte, err error) {
	_, err = file.ReadAt(header[:], 0)
	if err == nil && !bytes.Equal(header[:16], []byte("SQLite format 3\x00")) {
		err = ErrSnapshotUnavailable
	}
	return
}

func snapshotNoSidecars(path string) bool {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func sameSnapshotFiles(path string, file *os.File, info os.FileInfo, header [100]byte, lockPath string, lockInfo os.FileInfo) bool {
	if !snapshotNoSidecars(path) {
		return false
	}
	current, err := snapshotHeader(file)
	if err != nil || current != header {
		return false
	}
	return matchesSnapshotFile(path, info) && matchesSnapshotFile(lockPath, lockInfo)
}

func matchesSnapshotFile(path string, before os.FileInfo) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && os.SameFile(before, info) &&
		before.Size() == info.Size() && before.ModTime().Equal(info.ModTime())
}

type snapshotFileSystem struct {
	file *os.File
	info os.FileInfo
}

func (s snapshotFileSystem) Open(name string) (fs.File, error) {
	if name != snapshotDBName {
		return nil, fs.ErrNotExist
	}
	return snapshotFile{io.NewSectionReader(s.file, 0, s.info.Size()), s.info}, nil
}

type snapshotFile struct {
	*io.SectionReader
	info os.FileInfo
}

func (f snapshotFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (snapshotFile) Close() error                 { return nil }

const snapshotEventLimit = 8

var snapshotEventTypes = []string{
	"created", "review_verdict", "rework", "merge_success", "dev_disposed",
	"loop.absorbed", "loop.unanswered", "loop.continued", "loop.research",
	"loop.freezable", "loop.review_pass", "loop.review_bind", "loop.reopen_used",
}

func readSnapshotCard(ctx context.Context, tx *sql.Tx, id string, out *Snapshot) error {
	card := new(SnapshotCard)
	err := tx.QueryRowContext(ctx, `SELECT id,kind,status FROM cards WHERE id=?`, id).
		Scan(&card.ID, &card.Kind, &card.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil || !card.Kind.Valid() || !card.Status.Valid() || !snapshotLabel(card.ID) {
		return ErrSnapshotUnavailable
	}
	out.Card = card

	attempt := new(SnapshotAttempt)
	var channel, name, ended string
	var outcome model.Outcome
	err = tx.QueryRowContext(ctx, `SELECT id,channel,model,outcome,ended_at FROM attempts
		WHERE card_id=? ORDER BY attempt_index DESC,id DESC LIMIT 1`, id).
		Scan(&attempt.ID, &channel, &name, &outcome, &ended)
	if err == nil {
		if !snapshotLabel(attempt.ID) || (channel != "" && !snapshotLabel(channel)) || (name != "" && !snapshotLabel(name)) {
			return ErrSnapshotUnavailable
		}
		attempt.Status = "running"
		if outcome.Valid() {
			attempt.Status = string(outcome)
		} else if ended != "" {
			attempt.Status = "ended"
		}
		out.Attempt = attempt
		out.Route = strings.Trim(channel+"/"+name, "/")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ErrSnapshotUnavailable
	}

	stamps, err := snapshotEvents(ctx, tx, id)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	out.Events = stamps
	return nil
}

func snapshotLabel(s string) bool { return snapshotLabelLimit(s, 128) }

func snapshotLabelLimit(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			strings.ContainsRune("._:-/", r) {
			continue
		}
		return false
	}
	return !strings.HasPrefix(s, "/") && !strings.Contains(s, "://")
}

func snapshotEvents(ctx context.Context, tx *sql.Tx, id string) ([]SnapshotEvent, error) {
	markers := make([]string, 0, len(snapshotEventTypes))
	args := []any{"card:" + id}
	for _, event := range snapshotEventTypes {
		markers = append(markers, "?")
		args = append(args, event)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,event_type,ts FROM events
		WHERE entity=? AND event_type IN (`+strings.Join(markers, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	const limit = snapshotEventLimit
	type stamp struct {
		SnapshotEvent
		id string
	}
	selected := make([]stamp, 0, limit)
	for rows.Next() {
		var s stamp
		var raw string
		if err := rows.Scan(&s.id, &s.Type, &raw); err != nil {
			return nil, err
		}
		s.TS, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			continue
		}
		i, _ := slices.BinarySearchFunc(selected, s, func(a, b stamp) int {
			if !a.TS.Equal(b.TS) {
				return b.TS.Compare(a.TS)
			}
			return strings.Compare(b.id, a.id)
		})
		if i < limit {
			if len(selected) == limit {
				copy(selected[i+1:], selected[i:limit-1])
				selected[i] = s
			} else {
				selected = slices.Insert(selected, i, s)
			}
		}
	}
	out := make([]SnapshotEvent, len(selected))
	for i, s := range selected {
		out[i] = s.SnapshotEvent
	}
	return out, rows.Err()
}
