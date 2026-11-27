package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
)

type backupPersistence struct {
	prepare func(string) error
	sync    func(string) error
	publish func(string, string) error
}

func systemBackupPersistence() backupPersistence {
	return backupPersistence{prepareBackupDestination, syncBackupFile, exposeDurableBackup}
}

// backup snapshots committed pages (including the WAL) and only publishes a
// complete, independently verified copy. During a migration the caller holds
// the SQLite write lock on a different connection.
func (s *Store) backup(ctx context.Context, dir string) error {
	return s.backupWith(ctx, dir, systemBackupPersistence())
}

func (s *Store) backupWith(ctx context.Context, dir string, io backupPersistence) error {
	if err := io.prepare(dir); err != nil {
		return fmt.Errorf("prepare durable backup directory: %w", err)
	}
	const prefix = ".cardex-backup-"
	stage, err := os.MkdirTemp(dir, prefix)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	path := filepath.Join(stage, filepath.Base(s.path))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := s.copyBackup(ctx, path); err != nil {
		return fmt.Errorf("online SQLite backup: %w", err)
	}
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" +
		filepath.Base(stage)[len(prefix):] + "-" + filepath.Base(s.path)
	return publishBackupWith(ctx, path, filepath.Join(dir, name), io)
}

func (s *Store) copyBackup(ctx context.Context, path string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) (err error) {
		source, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver does not support online backup")
		}
		b, err := source.NewBackup(sqliteURI(path))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, b.Finish()) }()
		more, err := b.Step(-1)
		if err != nil {
			return err
		}
		if more {
			return errors.New("SQLite reported an incomplete backup")
		}
		return ctx.Err()
	})
}

func syncBackupFile(path string) error {
	// FlushFileBuffers on Windows needs a writable file handle.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func publishBackupWith(ctx context.Context, staged, dest string, io backupPersistence) error {
	if err := checkBackupIntegrity(ctx, staged); err != nil {
		return fmt.Errorf("verify SQLite backup: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := io.sync(staged); err != nil {
		return fmt.Errorf("sync verified SQLite backup data: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := io.publish(staged, dest); err != nil {
		return fmt.Errorf("publish durable SQLite backup without overwrite: %w", err)
	}
	return nil
}

func checkBackupIntegrity(ctx context.Context, path string) error {
	// The copied header can retain WAL mode. The immutable read handles that
	// header without creating sidecars, and the private stage cannot change.
	db, err := sql.Open("sqlite", sqliteURI(path)+"?mode=ro&immutable=1")
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		count++
		if result != "ok" || count > 1 {
			return errors.New("integrity_check did not return exactly one ok")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("integrity_check did not return exactly one ok")
	}
	return nil
}
