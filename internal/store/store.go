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
	db *sql.DB
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
	dsn := "file:" + filepath.ToSlash(abs) + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{db: db}
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

// SchemaVersion returns the applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func refuseCardexHome(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	banned := filepath.Join(home, ".cardex")
	if abs == banned || strings.HasPrefix(abs, banned+string(filepath.Separator)) {
		return fmt.Errorf("refusing path under ~/.cardex")
	}
	return nil
}

// WithTx runs fn inside a transaction. Nested calls join the outer transaction.
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
	ctx = context.WithValue(ctx, txKey{}, tx)
	if err = fn(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) q(ctx context.Context) execer {
	if tx, ok := ctx.Value(txKey{}).(execer); ok {
		return tx
	}
	return s.db
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if current >= 1 {
		return nil
	}
	return s.WithTx(ctx, func(ctx context.Context) error {
		for _, stmt := range migration1 {
			if _, err := s.q(ctx).ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration 1: %w", err)
			}
		}
		_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (1, ?)`, time.Now().UTC().Format(time.RFC3339Nano))
		return err
	})
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
