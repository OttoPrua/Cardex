//go:build !windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func prepareBackupDestination(path string) error {
	return prepareBackupPath(path, func(path string) error {
		return os.Mkdir(path, 0o700)
	}, syncBackupDir)
}

func prepareBackupPath(path string, mkdir, syncDir func(string) error) error {
	real, err := realBackupPath(path)
	if err != nil {
		return err
	}
	return anchorBackupPath(real, mkdir, syncDir)
}

// Resolve an existing prefix even when the leaf is missing. Otherwise an
// alias could sync its own parent while missing the actual volume's entries.
func realBackupPath(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return real, err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	real, err = realBackupPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, filepath.Base(path)), nil
}

func anchorBackupPath(path string, mkdir, syncDir func(string) error) error {
	info, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info != nil && !info.IsDir() {
		return &os.PathError{Op: "prepare backup directory", Path: path, Err: syscall.ENOTDIR}
	}
	parent := filepath.Dir(path)
	if parent == path {
		return err
	}
	if info != nil && backupVolumeRoot(info, parent) {
		// Existing mount roots cannot be introduced by this mkdir code.
		// Their backing filesystem entries, if any, need not be synced
		// from a different volume.
		return nil
	}
	if err := anchorBackupPath(parent, mkdir, syncDir); err != nil {
		return err
	}

	created := false
	if info == nil {
		err := mkdir(path)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		created = err == nil
		info, err = os.Stat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return &os.PathError{Op: "prepare backup directory", Path: path, Err: syscall.ENOTDIR}
		}
	}
	// Do not skip this for directories that already exist. A different
	// writer can have made the entry visible but not yet have synced it.
	if err := syncDir(parent); err != nil {
		if created {
			return errors.Join(err, syscall.Rmdir(path))
		}
		return err
	}
	return nil
}

func backupVolumeRoot(info os.FileInfo, parent string) bool {
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return false
	}
	childStat, ok := info.Sys().(*syscall.Stat_t)
	parentStat, parentOK := parentInfo.Sys().(*syscall.Stat_t)
	return ok && parentOK && childStat.Dev != parentStat.Dev
}

func exposeDurableBackup(staged, dest string) error {
	// Stage and destination share a filesystem. Link is exclusive, unlike
	// Rename on Unix, and cannot replace a prior backup.
	if err := os.Link(staged, dest); err != nil {
		return err
	}
	// If this fails, retain the verified file and report failure to the
	// owner command. Its published name was not confirmed durable.
	return syncBackupDir(filepath.Dir(dest))
}

func syncBackupDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
