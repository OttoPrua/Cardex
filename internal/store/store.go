// Package store persists the design §3 entities in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/OttoPrua/Cardex/internal/homeguard"
)

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
	home string
}

// Open opens or creates the database at path and applies migrations.
func Open(path string) (*Store, error) {
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
	unlock, err := lockExclusive(abs + ".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	dsn := "file:" + filepath.ToSlash(abs) + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{db: db, home: filepath.Dir(abs)}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		_ = db.Close()
		return nil, err
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

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Home is the directory that holds the database file.
func (s *Store) Home() string {
	if s == nil {
		return ""
	}
	return s.home
}

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
func (s *Store) WithTx(ctx context.Context, fn func(context.Context) error) (err error) {
	if _, ok := ctx.Value(txKey{}).(execer); ok {
		return fn(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	callerCtx := ctx
	notes := &commitNotes{}
	ctx = context.WithValue(ctx, txKey{}, tx)
	ctx = context.WithValue(ctx, notesKey{}, notes)
	if err = fn(ctx); err != nil {
		return err
	}
	var deliveries []int64
	for _, note := range notes.pending {
		ids, noteErr := s.persistLifecycle(ctx, note)
		if noteErr != nil {
			return noteErr
		}
		deliveries = append(deliveries, ids...)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if PublishCardNote != nil {
		for _, id := range deliveries {
			if id != 0 {
				PublishCardNote(callerCtx, s, id)
			}
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

func (s *Store) migrate(ctx context.Context) error {
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
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	var current int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
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
		// Lifecycle event switch. Empty inherits (project empty means off).
		for _, table := range []string{"projects", "objectives"} {
			var has int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name='events'`).Scan(&has); err != nil {
				return fmt.Errorf("migration 8: %w", err)
			}
			if has == 0 {
				if _, err := conn.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN events TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migration 8: %w", err)
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
			return fmt.Errorf("migration 8: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (8, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if current < 9 {
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
			return fmt.Errorf("migration 9: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS lifecycle_outbox_pending
			ON lifecycle_outbox(done, available_at)`); err != nil {
			return fmt.Errorf("migration 9: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (9, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
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
