package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func prepareBackupDestination(path string) error {
	// No directory creation on Windows: we cannot validate an already
	// visible directory created by another process as durable. Require a
	// pre-existing configured backup path rather than accepting mkdir races.
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("pre-create the configured backup directory on Windows: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup destination is not a directory: %s", path)
	}
	return nil
}

func exposeDurableBackup(staged, dest string) error {
	source, err := moveBackupPath(staged)
	if err != nil {
		return err
	}
	target, err := moveBackupPath(dest)
	if err != nil {
		return err
	}
	// MOVEFILE_WRITE_THROUGH requests an on-disk move for this file. Stage and destination are on the same volume. Do not pass
	// COPY_ALLOWED or REPLACE_EXISTING.
	// https://learn.microsoft.com/windows/win32/api/winbase/nf-winbase-movefileexw
	return windows.MoveFileEx(source, target, windows.MOVEFILE_WRITE_THROUGH)
}

func moveBackupPath(path string) (*uint16, error) {
	full, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Use Win32's extended path prefix so nested backup locations work
	// without depending on the process's MAX_PATH manifest or settings.
	switch {
	case strings.HasPrefix(full, `\\?\`):
	case strings.HasPrefix(full, `\\`):
		full = `\\?\UNC\` + strings.TrimPrefix(full, `\\`)
	default:
		full = `\\?\` + full
	}
	return windows.UTF16PtrFromString(full)
}
