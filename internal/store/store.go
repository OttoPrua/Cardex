// Package store persists the design §3 entities in SQLite.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/homeguard"
	"github.com/OttoPrua/Cardex/internal/roots"
)

// latestSchema is the migration applied by Open.
const latestSchema = TierConventionSchema

// ErrAppendOnly is returned by event update and delete.
var ErrAppendOnly = errors.New("event is append-only")

type txKey struct{}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Store is a SQLite database opened with WAL and a busy timeout.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens or creates the database at path and applies migrations.
func Open(path string) (*Store, error) {
	return open(path, false)
}

// OpenForWrite also verifies a pre-write snapshot of an existing database
// when its user has set backups_dir. Use it for one owner command, not a
// long-running worker or internal per-row write.
func OpenForWrite(path string) (*Store, error) {
	return open(path, true)
}

func open(path string, backupBeforeWrite bool) (*Store, error) {
	return openWithSnapshot(path, backupBeforeWrite, (*Store).backup)
}

func openWithSnapshot(path string, backupBeforeWrite bool, snapshot func(*Store, context.Context, string) error) (*Store, error) {
	return openStore(path, backupBeforeWrite, false, snapshot)
}

// OpenTierUpgrade is the explicit, backup-backed upgrade entry point for an
// existing home. Ordinary Open never activates a new convention on old data.
func OpenTierUpgrade(path string) (*Store, error) {
	if info, err := os.Stat(path); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, errors.New("tier upgrade requires an initialized database")
	}
	return openStore(path, false, true, (*Store).backup)
}

func openStore(path string, backupBeforeWrite, tierUpgrade bool, snapshot func(*Store, context.Context, string) error) (*Store, error) {

	if err := refuseCardexHome(path); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	// SQLite can return SQLITE_BUSY immediately while the new file and WAL mode are created, before busy_timeout applies. Hold an exclusive lock across that setup.
	lockPath := abs + ".lock"
	unlock, err := lockExclusive(lockPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()

	backupDir := roots.Load(filepath.Dir(abs)).BackupsDir
	if tierUpgrade && backupDir == "" {
		backupDir = filepath.Join(filepath.Dir(abs), "backups")
	}
	info, statErr := os.Stat(abs)
	existing := statErr == nil && info.Size() != 0
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if tierUpgrade && !existing {
		return nil, errors.New("tier upgrade requires an initialized database")
	}
	if !existing {
		backupDir = ""
	}
	dsn := sqliteURI(abs) + "?_pragma=busy_timeout(30000)"
	if !existing {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	// Existing databases keep their original journal mode until the
	// activation check and every required pre-write snapshot succeeds.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{db: db, path: abs}
	var migrationBackup func(context.Context) error
	var backedUp bool
	if backupDir != "" {
		migrationBackup = func(ctx context.Context) error {
			err := snapshot(s, ctx, backupDir)
			backedUp = err == nil
			return err
		}
	}
	if err := s.migrate(context.Background(), migrationBackup, tierUpgrade); err != nil {
		_ = db.Close()
		return nil, err
	}
	if backupBeforeWrite && backupDir != "" && !backedUp {
		// A normal online backup does not need the exclusive setup lock
		// or a SQLite write lock during copy, verification, or file sync.
		unlock()
		unlock = nil
		if err := snapshot(s, context.Background(), backupDir); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("backup before database write: %w", err)
		}
		unlock, err = lockExclusive(lockPath)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !strings.EqualFold(mode, "wal") {
		// Clear the idle connections used during migration and copy before
		// SQLite attempts a journal mode transition on the source.
		db.SetMaxIdleConns(0)
		db.SetMaxIdleConns(8)
		if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if !strings.EqualFold(mode, "wal") {
		_ = db.Close()
		return nil, fmt.Errorf("journal_mode=%s", mode)
	}
	var busy int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		_ = db.Close()
		return nil, err
	}
	if busy < 1000 {
		_ = db.Close()
		return nil, fmt.Errorf("busy_timeout=%d", busy)
	}
	return s, nil
}

// Inspect reads an existing store without migrations. SQLite's ordinary
// read-only mode can create sidecars for a clean WAL database, so use an
// immutable read for that case and reject the result if the file changed.
func Inspect(path string, read func(*Store)) error {
	return inspect(path, false, read)
}

// InspectClean inspects only an already checkpointed database, with a
// post-check for concurrent file or WAL changes. It never opens live WAL
// sidecars, so a verdict cannot mistake a stale checkpoint for current state.
func InspectClean(path string, read func(*Store)) error {
	return inspect(path, true, read)
}

func inspect(path string, cleanOnly bool, read func(*Store)) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	stat := os.Stat
	if cleanOnly {
		stat = os.Lstat
	}
	before, err := stat(abs)
	if err != nil {
		return err
	}
	checkSidecars := func() error {
		for _, side := range []struct{ suffix, label string }{
			{"-wal", "WAL"}, {"-shm", "shared-memory sidecar"}, {"-journal", "rollback journal"},
		} {
			if _, err := os.Lstat(abs + side.suffix); err == nil {
				return fmt.Errorf("cannot verify the newest attempt while database %s exists", side.label)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	if cleanOnly {
		if !before.Mode().IsRegular() {
			return errors.New("cannot verify an indirect or irregular database file")
		}
		if err := checkSidecars(); err != nil {
			return err
		}
	}
	opts := "?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	_, walErr := os.Stat(abs + "-wal")
	clean := os.IsNotExist(walErr)
	if cleanOnly && !clean {
		return errors.New("cannot verify the newest attempt while database WAL exists")
	}

	if clean {
		opts += "&immutable=1"
	} else if walErr != nil {
		return walErr
	} else if _, err := os.Stat(abs + "-shm"); err != nil {
		return fmt.Errorf("cannot inspect the WAL without its shared-memory file: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteURI(abs)+opts)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return err
	}
	read(&Store{db: db, path: abs})
	if clean {
		after, err := stat(abs)
		if cleanOnly {
			if err := checkSidecars(); err != nil {
				return err
			}
		}
		_, walErr := os.Stat(abs + "-wal")
		if err != nil || !os.IsNotExist(walErr) || !os.SameFile(before, after) ||
			before.Size() != after.Size() || before.ModTime() != after.ModTime() {
			return errors.New("database changed during inspection")
		}
	}
	return nil
}

func sqliteURI(path string) string {
	u := url.URL{Path: filepath.ToSlash(path)}
	return "file:" + u.EscapedPath()
}

// Home is the directory with the database and home configuration.
func (s *Store) Home() string {
	if s == nil {
		return ""
	}
	return filepath.Dir(s.path)
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion returns the applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func refuseCardexHome(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return homeguard.Check(filepath.Dir(abs))
}

type commitNotes struct {
	pending []cardNote
}

type notesKey struct{}

// WithTx runs fn inside a transaction. Nested calls join the outer transaction.
// Lifecycle events are saved before commit and delivered after it.
func (s *Store) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(execer); ok {
		return fn(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return s.runTx(ctx, tx, fn, tx.Commit)
}

// WithWriteTx reserves the SQLite writer before reading data that the operation
// will change. A deferred read transaction cannot upgrade its snapshot after
// another WAL writer commits, even with busy_timeout. Nested calls still join
// the outer transaction; the owner of a read-then-write transaction must use
// WithWriteTx at the outermost boundary.
func (s *Store) WithWriteTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(execer); ok {
		return fn(ctx)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if _, err := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK"); err != nil {
				// A driver connection whose transaction could not be rolled
				// back must not be returned to the connection pool.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	return s.runTx(ctx, conn, fn, func() error {
		_, err := conn.ExecContext(ctx, "COMMIT")
		if err == nil {
			committed = true
			_ = conn.Close()
		}
		return err
	})
}

func (s *Store) runTx(ctx context.Context, conn execer, fn func(context.Context) error, commit func() error) error {
	callerCtx := ctx
	notes := &commitNotes{}
	ctx = context.WithValue(ctx, txKey{}, conn)
	ctx = context.WithValue(ctx, notesKey{}, notes)
	if err := fn(ctx); err != nil {
		return err
	}
	var deliveries []int64
	for _, note := range notes.pending {
		id, noteErr := s.persistLifecycle(ctx, note)
		if noteErr != nil {
			return noteErr
		}
		if id != 0 {
			deliveries = append(deliveries, id)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	if PublishCardNote != nil {
		for _, id := range deliveries {
			PublishCardNote(callerCtx, s, id)
		}
	}
	return nil
}

func (s *Store) q(ctx context.Context) execer {
	if tx, ok := ctx.Value(txKey{}).(execer); ok {
		return tx
	}
	return s.db
}

func (s *Store) migrate(ctx context.Context, beforeMigration func(context.Context) error, tierUpgrade bool) error {
	// BEGIN IMMEDIATE takes the write lock before the version is read again, so a
	// later opener skips DDL that an earlier opener already committed.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var present, current int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&present); err != nil {
		return err
	}
	if present != 0 {
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
			return err
		}
	}
	if current > latestSchema {
		return errors.New("database requires a newer cardex build")
	}
	if current >= TierConventionSchema {
		active, err := tierConventionActive(ctx, conn)
		if err != nil {
			return fmt.Errorf("check tier activation: %w", err)
		}
		if !active {
			return errors.New("database has no valid tier activation receipt")
		}
	}
	if current > 0 && current < TierConventionSchema && !tierUpgrade {
		return ErrTierUpgradeRequired
	}
	var missingScratch bool
	if err := conn.QueryRowContext(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM pragma_table_info('projects') WHERE name='scratch_dir'
	)`).Scan(&missingScratch); err != nil {
		return err
	}
	var scratchOnlyV8 bool
	if current == 8 {
		// An earlier storage candidate recorded v8 without the changed_at migration.
		if err := conn.QueryRowContext(ctx, `SELECT NOT EXISTS (
			SELECT 1 FROM sqlite_schema WHERE type='index' AND name='events_entity_type'
		)`).Scan(&scratchOnlyV8); err != nil {
			return err
		}
	}
	var missingStrictFence bool
	if current >= 13 {
		// Some unpublished v13 builds recorded the migration before the
		// strict runtime fence landed. Inspect the physical shape under
		// the same lock used for DDL and the pre-migration backup.
		missingStrictFence, err = strictRuntimeFenceMissing(ctx, conn)
		if err != nil {
			return fmt.Errorf("inspect strict runtime fence: %w", err)
		}
	}
	if (current < latestSchema || missingScratch || scratchOnlyV8 || missingStrictFence) && beforeMigration != nil {
		if err := beforeMigration(ctx); err != nil {
			return fmt.Errorf("backup before schema write: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if current < 1 {
		for _, stmt := range migration1 {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration 1: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (1, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		current = 1
	}
	if missingScratch {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN scratch_dir TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migration 8: %w", err)
		}
	}
	if scratchOnlyV8 {
		// Replace the old marker in the same transaction as the full v8 migration.
		if _, err := conn.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=8`); err != nil {
			return fmt.Errorf("migration 8: %w", err)
		}
		current = 7
	}
	if current < 2 {
		if err := migrateOpenQuestions(ctx, conn); err != nil {
			return fmt.Errorf("migration 2: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (2, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		current = 2
	}
	if current < 3 {
		if err := migrateLegacyOwnerQuestionTypes(ctx, conn); err != nil {
			return fmt.Errorf("migration 3: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (3, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		current = 3
	}
	if current < 4 {
		// Per-card sandbox switch. Older binaries name their columns, so they keep working on a migrated db.
		var has int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cards') WHERE name='sandbox'`).Scan(&has); err != nil {
			return fmt.Errorf("migration 4: %w", err)
		}
		if has == 0 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE cards ADD COLUMN sandbox TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migration 4: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (4, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		current = 4
	}
	if current < 5 {
		// Per-project sandbox default; cards with an empty sandbox inherit it.
		var has int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='sandbox'`).Scan(&has); err != nil {
			return fmt.Errorf("migration 5: %w", err)
		}
		if has == 0 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN sandbox TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migration 5: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (5, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		current = 5
	}
	if current < 6 {
		// Review mode on projects, objectives and cards; empty inherits.
		for _, table := range []string{"projects", "objectives", "cards"} {
			var has int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name='review'`).Scan(&has); err != nil {
				return fmt.Errorf("migration 6: %w", err)
			}
			if has == 0 {
				if _, err := conn.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN review TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migration 6: %w", err)
				}
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (6, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		current = 6
	}
	if current < 7 {
		// Positioning rows may be limited to card kinds; '[]' means every kind.
		var has int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('positioning') WHERE name='kinds'`).Scan(&has); err != nil {
			return fmt.Errorf("migration 7: %w", err)
		}
		if has == 0 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE positioning ADD COLUMN kinds TEXT NOT NULL DEFAULT '[]'`); err != nil {
				return fmt.Errorf("migration 7: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (7, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		current = 7
	}
	if current < 8 {
		for _, table := range []string{"cards", "attempts"} {
			var has int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name='changed_at'`).Scan(&has); err != nil {
				return fmt.Errorf("migration 8: %w", err)
			}
			if has == 0 {
				if _, err := conn.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN changed_at TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migration 8: %w", err)
				}
			}
		}
		if _, err := conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS events_entity_type ON events(entity, event_type)`); err != nil {
			return fmt.Errorf("migration 8: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (8, ?)`, formatTime(time.Now())); err != nil {
			return fmt.Errorf("migration 8: %w", err)
		}
	}
	if current < 9 {
		// Lifecycle event switch. Empty inherits (project empty means off).
		for _, table := range []string{"projects", "objectives"} {
			var has int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name='events'`).Scan(&has); err != nil {
				return fmt.Errorf("migration 9: %w", err)
			}
			if has == 0 {
				if _, err := conn.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN events TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migration 9: %w", err)
				}
			}
		}
		if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mgmt_reads (
			origin TEXT NOT NULL,
			card_id TEXT NOT NULL,
			command TEXT NOT NULL,
			n INTEGER NOT NULL,
			PRIMARY KEY (origin, card_id, command)
		)`); err != nil {
			return fmt.Errorf("migration 9: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (9, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if current < 10 {
		if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS lifecycle_outbox (
			id INTEGER PRIMARY KEY,
			event_key TEXT NOT NULL UNIQUE,
			objective_id TEXT NOT NULL,
			origin TEXT NOT NULL,
			payload TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			done INTEGER NOT NULL DEFAULT 0,
			available_at INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
			return fmt.Errorf("migration 10: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS lifecycle_outbox_pending
			ON lifecycle_outbox(done, available_at)`); err != nil {
			return fmt.Errorf("migration 10: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (10, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if current < 11 {
		var has int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cards') WHERE name='wait_question_id'`).Scan(&has); err != nil {
			return fmt.Errorf("migration 11: %w", err)
		}
		if has == 0 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE cards ADD COLUMN wait_question_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migration 11: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (11, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if current < 12 {
		if err := ensureHardRulesColumns(ctx, conn); err != nil {
			return fmt.Errorf("migration 12: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (12, ?)`, formatTime(time.Now())); err != nil {
			return err
		}
	}
	if current < 13 || missingStrictFence {
		// An unpublished D1 candidate used schema 12 for strict_deps.
		// Reconcile both shapes before recording 13; preserve existing values.
		if current == 12 {
			if err := ensureHardRulesColumns(ctx, conn); err != nil {
				return fmt.Errorf("migration 13: %w", err)
			}
		}
		if err := installStrictRuntimeFence(ctx, conn); err != nil {
			return fmt.Errorf("migration 13 fence: %w", err)
		}
		if current < 13 {
			if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (13, ?)`, formatTime(time.Now())); err != nil {
				return err
			}
		}
	}
	if current < 14 {
		for _, stmt := range migration14 {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration 14: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (14, ?)`, formatTime(time.Now())); err != nil {
			return err
		}
	}
	if current < TierConventionSchema {
		if err := validateTierUpgrade(ctx, conn); err != nil {
			return fmt.Errorf("migration %d (tier convention): %w", TierConventionSchema, err)
		}
		if err := recordTierActivation(ctx, conn); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func ensureHardRulesColumns(ctx context.Context, conn *sql.Conn) error {
	for _, table := range []string{"projects", "objectives"} {
		var has int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name='hard_rules'`, table).Scan(&has); err != nil {
			return err
		}
		if has == 0 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN hard_rules TEXT NOT NULL DEFAULT '[]'`); err != nil {
				return err
			}
		}
	}
	return nil
}

func mustJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseJSON(s string, v any) error {
	if s == "" || s == "null" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}

func normStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func rowsAffected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func invalid(name string, v any) error {
	return fmt.Errorf("invalid %s %q", name, v)
}
