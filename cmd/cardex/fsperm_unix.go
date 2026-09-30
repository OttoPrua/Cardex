//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"syscall"
)

func permissionClassError(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EROFS)
}
