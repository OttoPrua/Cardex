package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReadCardImportState checks one checkpointed DB view without allowing SQLite
// to create or modify sidecars, take the CardeX writer lock, or migrate a home.
func ReadCardImportState(ctx context.Context, path string, read func(context.Context, *Store) error) error {
	path, before, err := importDB(path)
	if err != nil {
		return err
	}
	if err := requireCheckpointedImportDB(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteURI(path)+"?mode=ro&immutable=1&_pragma=query_only(1)&_pragma=temp_store(2)&_pragma=trusted_schema(0)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	err = s.WithTx(ctx, func(ctx context.Context) error {
		if err := s.checkImportSchema(ctx); err != nil {
			return err
		}
		return read(ctx, s)
	})
	// Discard all results if a writer started or replaced the DB during the
	// preview. Applying does not rely on the preview as a reservation.
	if guardErr := requireCheckpointedImportDB(path); guardErr != nil {
		return guardErr
	}
	after, statErr := os.Lstat(path)
	if statErr != nil || !os.SameFile(before, after) ||
		before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return errors.New("card import preview unavailable: database changed during inspection")
	}
	return err
}

// WriteCardImport validates and writes the batch on one existing DB
// connection. BEGIN IMMEDIATE serializes validation with concurrent writers.
// Unlike the normal Open, this path does not create a database, migrate the
// schema, switch journal modes, create a lock file, or configure backups.
func WriteCardImport(ctx context.Context, path string, write func(context.Context, *Store) error) error {
	path, _, err := importDB(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteURI(path)+"?mode=rw&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	return s.WithTx(ctx, func(ctx context.Context) error {
		if err := s.checkImportSchema(ctx); err != nil {
			return err
		}
		return write(ctx, s)
	})
}

func importDB(path string) (string, os.FileInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	if err := refuseCardexHome(abs); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return "", nil, fmt.Errorf("cardex.db not found: %s", abs)
	}
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("card import requires a regular cardex.db file")
	}
	return abs, info, nil
}

func requireCheckpointedImportDB(path string) error {
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + sidecar); err == nil {
			return fmt.Errorf("card import preview unavailable: SQLite sidecar %s is present; use a safely checkpointed home and retry", sidecar)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Store) checkImportSchema(ctx context.Context) error {
	var version int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("card import requires an initialized database: %w", err)
	}
	if version != latestSchema {
		return fmt.Errorf("card import requires schema %d; found %d (this command does not migrate databases)", latestSchema, version)
	}
	return nil
}

// CardImportDependencies loads only the bounded dependency field, not the
// stored body or claims. Do not include malformed stored data in diagnostics.
func (s *Store) CardImportDependencies(ctx context.Context, id string) ([]string, error) {
	const maxEncodedDeps = 128 << 10
	var raw sql.NullString
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(deps AS BLOB)) <= ? THEN deps END FROM cards WHERE id=?`, maxEncodedDeps, id).Scan(&raw); err != nil {
		return nil, err
	}
	var deps []string
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &deps) != nil {
		return nil, errors.New("stored dependency data is malformed or too large")
	}
	return deps, nil
}
